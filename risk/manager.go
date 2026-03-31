package risk

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/seb/fivetrader/strategy"
	"go.uber.org/zap"
)

const (
	MinEdge          = 0.02 // minimum 2% edge to trade
	MinEdgeDumpHedge = 0.01 // dump_hedge is risk-free; only needs marginal edge > 0
	MinTokenPrice    = 0.35 // don't buy tokens below 0.35
	MaxTokenPrice    = 0.75 // don't buy tokens above 0.75 (except oracle lag)

	stratOracleLag = strategy.NameOracleLag
	stratDumpHedge = strategy.NameDumpHedge
)

// Config holds risk parameters loaded from application config.
type Config struct {
	MaxBetUSDC        float64
	MaxDailyLossUSDC  float64
	MaxConcurrentBets int
	KellyFraction     float64
}

// Trade records a single trade outcome.
type Trade struct {
	ID          string
	Strategy    string
	Direction   string
	TokenID     string
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
	cfg          Config
	mu           sync.Mutex
	dailyLoss    float64
	dailySettled int
	dailyPnL     float64
	dailyWins    int
	dailyWinRate float64
	openTrades   map[string]*Trade
	recentTrades []Trade // last maxRecentTrades settled, oldest first
	reservedBets int     // slots reserved before goroutine launch (prevents TOCTOU race)
	dayStart     time.Time
	log          *zap.Logger
}

// NewManager creates a Manager with the given risk configuration.
func NewManager(cfg Config, log *zap.Logger) *Manager {
	return &Manager{
		cfg:        cfg,
		openTrades: make(map[string]*Trade),
		dayStart:   today(),
		log:        log,
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
func (m *Manager) Approve(strategy string, tokenPrice, winProb, edge, confidence float64) ApproveResult {
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

	// Rule: token price bounds
	if tokenPrice < MinTokenPrice {
		return ApproveResult{Reason: fmt.Sprintf("price %.3f < min %.3f", tokenPrice, MinTokenPrice)}
	}
	if tokenPrice > MaxTokenPrice && strategy != stratOracleLag && strategy != stratDumpHedge {
		return ApproveResult{Reason: fmt.Sprintf("price %.3f > max %.3f", tokenPrice, MaxTokenPrice)}
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
	netOdds := (1.0 / tokenPrice) - 1.0
	if netOdds <= 0 {
		return ApproveResult{Reason: fmt.Sprintf("net odds <= 0 (token price=%.3f)", tokenPrice)}
	}
	kellyFull := (winProb*(netOdds+1) - 1) / netOdds
	if kellyFull <= 0 {
		return ApproveResult{Reason: fmt.Sprintf("kelly fraction negative (%.4f)", kellyFull)}
	}
	conf := math.Min(math.Max(confidence, 0), 1.0) // clamp [0,1]
	kellySize := kellyFull * m.cfg.KellyFraction * conf

	usdcSize := kellySize * m.cfg.MaxBetUSDC
	usdcSize = math.Min(usdcSize, m.cfg.MaxBetUSDC)
	usdcSize = math.Min(usdcSize, m.cfg.MaxDailyLossUSDC-m.dailyLoss)
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
	delete(m.openTrades, id)
	m.log.Info("trade settled",
		zap.String("id", id),
		zap.Float64("pnl", pnl),
		zap.Float64("daily_loss", m.dailyLoss),
	)
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
// In production, this should query the CLOB API for actual P&L.
// For now, we mark them settled with 0 P&L to unblock the concurrent bets counter.
func (m *Manager) SettleExpiredTrades(log *zap.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, t := range m.openTrades {
		if t.WindowEnd.IsZero() || now.Before(t.WindowEnd.Add(30*time.Second)) {
			continue
		}
		// Assume worst case — settle as total loss so the circuit breaker triggers correctly.
		// Conservative: better to stop early than to trade past the daily loss limit.
		log.Warn("auto-settling expired trade as loss (actual P&L unknown — check CLOB manually)",
			zap.String("id", id),
			zap.Time("window_end", t.WindowEnd),
			zap.Float64("assumed_loss", -t.USDCStaked),
		)
		t.Settled = true
		t.PnL = -t.USDCStaked
		m.dailyLoss += t.USDCStaked
		m.dailySettled++
		m.dailyPnL += t.PnL
		m.dailyWinRate = float64(m.dailyWins) / float64(m.dailySettled)
		delete(m.openTrades, id)
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

// today returns midnight UTC of the current day.
func today() time.Time {
	y, mo, d := time.Now().UTC().Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC) // always UTC
}
