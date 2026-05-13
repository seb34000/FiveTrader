package risk

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seb/fivetrader/strategy"
	"go.uber.org/zap"
)

const (
	MinEdge          = 0.015 // minimum 1.5% edge to trade
	MinEdgeDumpHedge = 0.01 // dump_hedge is risk-free; only needs marginal edge > 0

	stratOracleLag = strategy.NameOracleLag
	stratDumpHedge = strategy.NameDumpHedge
)

// FilterConfig holds entry-price, position-size, and consecutive-loss guardrails.
type FilterConfig struct {
	// Price band: trades outside [MinEntryPrice, MaxEntryPrice] are rejected.
	// oracle_lag bypasses MaxEntryPrice; dump_hedge bypasses both (its AskPrice = sum of two legs).
	MinEntryPrice float64 // default 0.60
	MaxEntryPrice float64 // default 0.90

	// Position limits per trade.
	MaxPositionSize float64 // max tokens (shares) per trade; 0 = no cap
	MaxLossPerTrade float64 // max USDC staked per trade; 0 = no cap

	// Consecutive-loss pause.
	MaxConsecLosses int           // trigger after this many consecutive losses; 0 = disabled
	PauseDuration   time.Duration // how long to pause; default 30 min
}

// Config holds risk parameters loaded from application config.
type Config struct {
	MaxBetUSDC        float64
	MaxDailyLossUSDC  float64
	MaxConcurrentBets int
	KellyFraction     float64
	Filters           FilterConfig
}

// PnLLookup resolves the actual P&L for a settled trade by querying the market.
// Returns (pnl, resolved, err). resolved=false means the market has not settled yet;
// the trade will be retried on the next settlement tick.
type PnLLookup func(ctx context.Context, conditionID, direction string, usdcStaked, tokenPrice float64) (pnl float64, resolved bool, err error)

// Trade records a single trade outcome.
type Trade struct {
	ID          string
	Strategy    string
	Direction   string
	TokenID     string
	ConditionID string    // Polymarket condition ID for resolution lookup
	USDCStaked  float64
	TokenPrice  float64
	Timestamp   time.Time
	WindowEnd   time.Time // when the Polymarket window expires
	Settled     bool
	PnL         float64
}

const maxRecentTrades = 50

// Manager enforces risk rules and sizes positions.
type Manager struct {
	cfg              Config
	mu               sync.Mutex
	walletBalanceBits *atomic.Uint64 // shared across per-asset Managers; read lock-free in Approve
	dailyLoss        float64
	dailySettled     int
	dailyPnL         float64
	dailyWins        int
	dailyWinRate     float64
	openTrades       map[string]*Trade
	recentTrades     []Trade // last maxRecentTrades settled, oldest first
	reservedBets     int     // slots reserved before goroutine launch (prevents TOCTOU race)
	dayStart         time.Time
	pauseUntil       time.Time // non-zero when in consecutive-loss cooldown
	log              *zap.Logger
}

// NewManager creates a Manager with the given risk configuration.
// walletBalanceBits is a shared atomic holding the live USDC balance as float64 bits.
// Pass nil to allocate a private atomic initialised to cfg.MaxBetUSDC (tests / sim / dry-run).
// Zero values in FilterConfig are replaced with safe defaults.
func NewManager(cfg Config, walletBalanceBits *atomic.Uint64, log *zap.Logger) *Manager {
	// Apply defaults for FilterConfig so zero-value configs behave sensibly.
	if cfg.Filters.MinEntryPrice <= 0 {
		cfg.Filters.MinEntryPrice = 0.50
	}
	if cfg.Filters.MaxEntryPrice <= 0 {
		cfg.Filters.MaxEntryPrice = 0.92
	}
	if cfg.Filters.MaxLossPerTrade <= 0 {
		cfg.Filters.MaxLossPerTrade = math.MaxFloat64 // no cap
	}
	if cfg.Filters.MaxPositionSize <= 0 {
		cfg.Filters.MaxPositionSize = math.MaxFloat64 // no cap
	}
	if cfg.Filters.MaxConsecLosses <= 0 {
		cfg.Filters.MaxConsecLosses = 3
	}
	if cfg.Filters.PauseDuration <= 0 {
		cfg.Filters.PauseDuration = 30 * time.Minute
	}
	if walletBalanceBits == nil {
		walletBalanceBits = new(atomic.Uint64)
		walletBalanceBits.Store(math.Float64bits(cfg.MaxBetUSDC))
	}
	return &Manager{
		cfg:               cfg,
		walletBalanceBits: walletBalanceBits,
		openTrades:        make(map[string]*Trade),
		dayStart:          today(),
		log:               log,
	}
}

// SetBalance updates the shared wallet balance (called by the background poller in live mode).
func (m *Manager) SetBalance(usdc float64) {
	m.walletBalanceBits.Store(math.Float64bits(usdc))
}

// Balance returns the current wallet balance in USDC (atomic read, safe to call from any goroutine).
func (m *Manager) Balance() float64 {
	return math.Float64frombits(m.walletBalanceBits.Load())
}

// adjustBalance adds delta to the wallet balance using a CAS loop (safe under concurrent Store from poller).
func (m *Manager) adjustBalance(delta float64) {
	for {
		old := m.walletBalanceBits.Load()
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if m.walletBalanceBits.CompareAndSwap(old, next) {
			return
		}
	}
}

// ApproveResult contains the approved bet size or rejection reason.
type ApproveResult struct {
	Approved  bool
	USDCSize  float64
	Reason    string
}

// Approve checks risk rules and returns Kelly-sized bet amount.
// strategy is the strategy name (oracle_lag bypasses MaxTokenPrice).
// confidence (0–1) from the signal scales the Kelly fraction: lower confidence = smaller position.
// feeRateBps is the taker fee in basis points (200 = 2%); deducted from net odds inside Kelly.
func (m *Manager) Approve(strategy string, tokenPrice, winProb, edge, confidence float64, feeRateBps int64) ApproveResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resetDayIfNeeded()

	// Rule: minimum edge (dump_hedge is risk-free so uses a lower floor)
	minEdge := MinEdge
	if strategy == stratDumpHedge {
		minEdge = MinEdgeDumpHedge
	}
	if edge < minEdge {
		return ApproveResult{Reason: fmt.Sprintf("edge %.3f < min %.3f", edge, minEdge)}
	}

	// Rule: consecutive-loss pause
	if !m.pauseUntil.IsZero() && time.Now().Before(m.pauseUntil) {
		return ApproveResult{Reason: "consec_loss_pause"}
	}

	// Rule: token price bounds.
	// dump_hedge AskPrice = sum of both legs (> 0.90 in practice) — bypass both bounds.
	// oracle_lag bypasses both MinEntryPrice and MaxEntryPrice: it specifically targets
	// tokens at 0.50–0.60 that haven't priced in the lag yet (best entries) and high-
	// conviction near-certainties up to 0.92.
	if strategy != stratDumpHedge && strategy != stratOracleLag {
		if tokenPrice < m.cfg.Filters.MinEntryPrice {
			return ApproveResult{Reason: fmt.Sprintf("price_below_min (%.3f < %.3f)", tokenPrice, m.cfg.Filters.MinEntryPrice)}
		}
		if tokenPrice > m.cfg.Filters.MaxEntryPrice {
			return ApproveResult{Reason: fmt.Sprintf("price_above_max (%.3f > %.3f)", tokenPrice, m.cfg.Filters.MaxEntryPrice)}
		}
	}

	// Rule: concurrent bets (open + reserved slots to prevent TOCTOU race)
	if len(m.openTrades)+m.reservedBets >= m.cfg.MaxConcurrentBets {
		return ApproveResult{Reason: fmt.Sprintf("max concurrent bets (%d) reached", m.cfg.MaxConcurrentBets)}
	}

	// Rule: daily loss circuit breaker
	if m.dailyLoss >= m.cfg.MaxDailyLossUSDC {
		return ApproveResult{Reason: fmt.Sprintf("daily loss limit $%.2f reached", m.cfg.MaxDailyLossUSDC)}
	}

	// Kelly sizing: f* = (p*(b+1) - 1) / b
	// where p = win probability, b = net odds (1/price - 1)
	// Fees are deducted from profit, so effective net odds shrink by (1 - feeRate).
	// Without this, a 2% raw edge (~MinEdge) is fully consumed by ~200bps Polymarket taker fees.
	feeRate := float64(feeRateBps) / 10000.0
	if feeRate < 0 {
		feeRate = 0
	}
	if feeRate >= 1 {
		return ApproveResult{Reason: fmt.Sprintf("fee rate %.4f >= 1 (token price=%.3f)", feeRate, tokenPrice)}
	}
	netOdds := ((1.0 / tokenPrice) - 1.0) * (1.0 - feeRate)
	if netOdds <= 0 {
		return ApproveResult{Reason: fmt.Sprintf("net odds <= 0 after fees (token price=%.3f, fee=%.4f)", tokenPrice, feeRate)}
	}
	kellyFull := (winProb*(netOdds+1) - 1) / netOdds
	if kellyFull <= 0 {
		return ApproveResult{Reason: fmt.Sprintf("kelly fraction negative (%.4f)", kellyFull)}
	}
	conf := math.Min(math.Max(confidence, 0), 1.0) // clamp [0,1]
	kellySize := kellyFull * m.cfg.KellyFraction * conf

	// Conviction multiplier: scale position size by price-band confidence tier.
	// Bypassed for dump_hedge (sum of two legs, semantically different) and oracle_lag
	// (which already encodes confidence via its own confidence field — applying convictionScale
	// would double-count the price signal and make sub-$1 bets at 0.50–0.62 entries).
	if strategy != stratDumpHedge && strategy != stratOracleLag {
		kellySize *= convictionScale(tokenPrice)
	}

	walletUSDC := math.Float64frombits(m.walletBalanceBits.Load())
	usdcSize := kellySize * walletUSDC
	usdcSize = math.Min(usdcSize, m.cfg.MaxBetUSDC) // hard cap regardless of wallet size
	usdcSize = math.Min(usdcSize, m.cfg.MaxDailyLossUSDC-m.dailyLoss)

	// Per-trade caps: max USDC staked and max shares.
	usdcSize = math.Min(usdcSize, m.cfg.Filters.MaxLossPerTrade)
	if m.cfg.Filters.MaxPositionSize < math.MaxFloat64 {
		maxByShares := m.cfg.Filters.MaxPositionSize * tokenPrice
		usdcSize = math.Min(usdcSize, maxByShares)
	}

	usdcSize = math.Floor(usdcSize*100) / 100 // round to cents

	if usdcSize < 1.0 {
		return ApproveResult{Reason: fmt.Sprintf("kelly size too small: $%.2f", usdcSize)}
	}

	return ApproveResult{Approved: true, USDCSize: usdcSize}
}

// ReserveBet atomically claims a concurrent bet slot before execution starts.
// Call this synchronously (before launching a goroutine) after Approve returns true.
// Returns false if the limit is already reached.
func (m *Manager) ReserveBet() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.openTrades)+m.reservedBets >= m.cfg.MaxConcurrentBets {
		return false
	}
	m.reservedBets++
	return true
}

// UnreserveBet releases a slot reserved by ReserveBet without opening a trade.
// Call this if execution fails so subsequent signals can use the slot.
func (m *Manager) UnreserveBet() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reservedBets > 0 {
		m.reservedBets--
	}
}

// OpenTrade records a new open position, consuming the slot reserved by ReserveBet.
func (m *Manager) OpenTrade(t *Trade) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Convert reservation to open trade (reservation may be 0 if called without ReserveBet)
	if m.reservedBets > 0 {
		m.reservedBets--
	}
	m.openTrades[t.ID] = t
	m.adjustBalance(-t.USDCStaked) // debit stake from local balance immediately
	m.log.Info("trade opened",
		zap.String("id", t.ID),
		zap.String("strategy", t.Strategy),
		zap.String("direction", t.Direction),
		zap.Float64("usdc", t.USDCStaked),
		zap.Float64("price", t.TokenPrice),
	)
}

// SettleTrade marks a trade as settled with its P&L.
func (m *Manager) SettleTrade(id string, pnl float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.openTrades[id]
	if !ok {
		m.log.Warn("SettleTrade: unknown trade ID", zap.String("id", id))
		return
	}
	t.Settled = true
	t.PnL = pnl
	if pnl < 0 {
		m.dailyLoss += -pnl
	}
	m.dailySettled++
	m.dailyPnL += pnl
	if pnl > 0 {
		m.dailyWins++
	}
	m.dailyWinRate = float64(m.dailyWins) / float64(m.dailySettled)
	// Keep a rolling history of settled trades.
	m.recentTrades = append(m.recentTrades, *t)
	if len(m.recentTrades) > maxRecentTrades {
		m.recentTrades = m.recentTrades[len(m.recentTrades)-maxRecentTrades:]
	}
	m.adjustBalance(t.USDCStaked + pnl) // credit payout: stake+profit on win, 0 on loss
	delete(m.openTrades, id)
	m.log.Info("trade settled",
		zap.String("id", id),
		zap.Float64("pnl", pnl),
		zap.Float64("daily_loss", m.dailyLoss),
		zap.Float64("wallet_balance", m.Balance()),
	)
	m.checkConsecLosses()
}

// DailyStats returns summary statistics for today.
func (m *Manager) DailyStats() (trades int, pnl float64, winRate float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dailySettled, m.dailyPnL, m.dailyWinRate
}

// resetDayIfNeeded clears daily counters when the UTC calendar date has advanced.
func (m *Manager) resetDayIfNeeded() {
	t := today()
	if t.After(m.dayStart) {
		m.dailyLoss = 0
		m.dailySettled = 0
		m.dailyPnL = 0
		m.dailyWins = 0
		m.dailyWinRate = 0
		m.dayStart = t
	}
}

// SettleExpiredTrades settles open trades whose window has expired (+ 30s grace).
// lookup queries Polymarket for the actual market outcome.
//
// Failure handling — never settle as $0 (neutral). On Polymarket binary markets, an
// expired position is either won or lost — never neutral. Settling at $0 understates
// dailyLoss (circuit breaker won't trigger) and inflates dailyWinRate.
//
//   - lookup error before 10min grace → defer (transient network blip; retry next tick).
//   - lookup error after 10min grace  → settle as -USDCStaked (assume worst case).
//   - market unresolved before 10min  → defer (Polymarket Gamma is slow but consistent).
//   - market unresolved after 10min   → settle as -USDCStaked (force-close to free slot).
//   - no lookup / no conditionID      → settle as -USDCStaked (config bug; fail safe).
func (m *Manager) SettleExpiredTrades(ctx context.Context, lookup PnLLookup, log *zap.Logger) {
	// Collect expired trades under lock (avoid holding lock during network calls).
	m.mu.Lock()
	now := time.Now()
	var expired []*Trade
	for _, t := range m.openTrades {
		if !t.WindowEnd.IsZero() && now.After(t.WindowEnd.Add(30*time.Second)) {
			expired = append(expired, t)
		}
	}
	m.mu.Unlock()

	if len(expired) == 0 {
		return
	}

	// Resolve each trade's P&L outside the lock (may involve network calls).
	type settlement struct {
		id  string
		pnl float64
	}
	var toSettle []settlement
	for _, t := range expired {
		past10min := now.After(t.WindowEnd.Add(10 * time.Minute))
		// No resolver / no conditionID — config bug. Fail safe by booking the loss.
		if lookup == nil || t.ConditionID == "" {
			log.Warn("no P&L resolver / conditionID — settling as loss (-stake)",
				zap.String("id", t.ID), zap.Float64("usdc_staked", t.USDCStaked),
				zap.Time("window_end", t.WindowEnd))
			toSettle = append(toSettle, settlement{id: t.ID, pnl: -t.USDCStaked})
			continue
		}
		actual, resolved, err := lookup(ctx, t.ConditionID, t.Direction, t.USDCStaked, t.TokenPrice)
		if err != nil {
			if past10min {
				log.Warn("P&L lookup failed past 10min grace — settling as loss (-stake)",
					zap.String("id", t.ID), zap.Float64("usdc_staked", t.USDCStaked), zap.Error(err))
				toSettle = append(toSettle, settlement{id: t.ID, pnl: -t.USDCStaked})
			} else {
				log.Info("P&L lookup failed (transient) — deferring settlement",
					zap.String("id", t.ID), zap.Error(err))
			}
			continue
		}
		if !resolved {
			if past10min {
				log.Warn("market still unresolved 10min after expiry — force-settling as loss (-stake)",
					zap.String("id", t.ID), zap.Float64("usdc_staked", t.USDCStaked),
					zap.Time("window_end", t.WindowEnd))
				toSettle = append(toSettle, settlement{id: t.ID, pnl: -t.USDCStaked})
			} else {
				log.Info("market not yet resolved — deferring settlement", zap.String("id", t.ID))
			}
			continue
		}
		toSettle = append(toSettle, settlement{id: t.ID, pnl: actual})
	}

	if len(toSettle) == 0 {
		return
	}

	// Apply settlements under lock.
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range toSettle {
		t, ok := m.openTrades[s.id]
		if !ok {
			continue // already settled concurrently
		}
		t.Settled = true
		t.PnL = s.pnl
		if s.pnl < 0 {
			m.dailyLoss += -s.pnl
		}
		m.dailySettled++
		m.dailyPnL += s.pnl
		if s.pnl > 0 {
			m.dailyWins++
		}
		m.dailyWinRate = float64(m.dailyWins) / float64(m.dailySettled)
		m.recentTrades = append(m.recentTrades, *t)
		if len(m.recentTrades) > maxRecentTrades {
			m.recentTrades = m.recentTrades[len(m.recentTrades)-maxRecentTrades:]
		}
		m.adjustBalance(t.USDCStaked + s.pnl) // credit payout: stake+profit on win, 0 on loss
		delete(m.openTrades, s.id)
		log.Info("trade settled",
			zap.String("id", s.id),
			zap.Float64("pnl", s.pnl),
			zap.Float64("daily_loss", m.dailyLoss),
			zap.Float64("wallet_balance", m.Balance()),
		)
		m.checkConsecLosses()
	}
}

// OpenTradesList returns a value-copy snapshot of all currently open trades.
// Callers receive independent copies; concurrent SettleTrade writes cannot race them.
func (m *Manager) OpenTradesList() []Trade {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Trade, 0, len(m.openTrades))
	for _, t := range m.openTrades {
		out = append(out, *t)
	}
	return out
}

// DailyLossAmt returns the accumulated loss for today.
func (m *Manager) DailyLossAmt() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dailyLoss
}

// Snapshot returns all stats needed by the UI in a single lock acquisition.
type Snapshot struct {
	Trades       int
	PnL          float64
	WinRate      float64
	DailyLoss    float64
	OpenTrades   []Trade
	RecentTrades []Trade // last settled trades, newest first
}

// Snapshot returns all stats needed by the UI in a single lock acquisition.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	open := make([]Trade, 0, len(m.openTrades))
	for _, t := range m.openTrades {
		open = append(open, *t)
	}
	// Reverse recentTrades so newest is first.
	recent := make([]Trade, len(m.recentTrades))
	for i, t := range m.recentTrades {
		recent[len(m.recentTrades)-1-i] = t
	}
	return Snapshot{
		Trades:       m.dailySettled,
		PnL:          m.dailyPnL,
		WinRate:      m.dailyWinRate,
		DailyLoss:    m.dailyLoss,
		OpenTrades:   open,
		RecentTrades: recent,
	}
}

// convictionScale returns a Kelly multiplier based on token price tier.
// Higher price tokens represent higher-conviction signals and receive more capital.
// Applies to window_delta; dump_hedge and oracle_lag are exempted.
//
//	≥ 0.70 → 1.0  (full conviction)
//	≥ 0.62 → 0.6  (moderate conviction)
//	default → 0.3  (low conviction, 0.60–0.61 band)
func convictionScale(tokenPrice float64) float64 {
	switch {
	case tokenPrice >= 0.70:
		return 1.0
	case tokenPrice >= 0.62:
		return 0.6
	default:
		return 0.3
	}
}

// checkConsecLosses inspects the recent settled-trade ring and activates a cooldown
// pause if the last MaxConsecLosses trades are all losses.
// Must be called under m.mu.
func (m *Manager) checkConsecLosses() {
	n := m.cfg.Filters.MaxConsecLosses
	if n <= 0 || len(m.recentTrades) < n {
		return
	}
	// Use PnL >= 0 (not > 0): a neutral $0 settle is not a loss. Without this guard,
	// the legacy SettleExpiredTrades $0 fallback (now removed) caused spurious cooldowns.
	// Only n consecutive STRICT losses (PnL < 0) trigger the pause.
	for i := len(m.recentTrades) - n; i < len(m.recentTrades); i++ {
		if m.recentTrades[i].PnL >= 0 {
			return // at least one non-loss — no pause
		}
	}
	// All last n trades are strict losses.
	m.pauseUntil = time.Now().Add(m.cfg.Filters.PauseDuration)
	m.log.Warn("consecutive loss pause activated",
		zap.Int("consec_losses", n),
		zap.Time("resume_at", m.pauseUntil),
	)
}

// today returns midnight UTC of the current day.
func today() time.Time {
	y, mo, d := time.Now().UTC().Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC) // always UTC
}
