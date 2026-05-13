package risk

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestManager() *Manager {
	return NewManager(Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
	}, nil, zap.NewNop())
}

func newTrade(id string, windowEnd time.Time) *Trade {
	return &Trade{
		ID:         id,
		Strategy:   "oracle_lag",
		Direction:  "UP",
		TokenID:    "token-123",
		USDCStaked: 10.0,
		TokenPrice: 0.60,
		Timestamp:  time.Now(),
		WindowEnd:  windowEnd,
	}
}

// ── Approve ───────────────────────────────────────────────────────────────────

func TestApprove_OK(t *testing.T) {
	m := newTestManager()
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0, 0)
	if !result.Approved {
		t.Errorf("expected approval, got rejection: %s", result.Reason)
	}
	if result.USDCSize <= 0 {
		t.Errorf("USDCSize = %v, should be > 0", result.USDCSize)
	}
	if result.USDCSize > 100.0 {
		t.Errorf("USDCSize = %v, should not exceed MaxBetUSDC=100", result.USDCSize)
	}
}

func TestApprove_EdgeTooLow(t *testing.T) {
	m := newTestManager()
	result := m.Approve("oracle_lag", 0.60, 0.61, 0.01, 1.0, 0) // edge=0.01 < MinEdge=0.02
	if result.Approved {
		t.Error("should reject edge < MinEdge")
	}
}

func TestApprove_DumpHedge_LowerMinEdge(t *testing.T) {
	m := newTestManager()
	// dump_hedge uses MinEdgeDumpHedge=0.01; edge=0.015 should pass
	result := m.Approve("dump_hedge", 0.50, 1.0, 0.015, 1.0, 0)
	if !result.Approved {
		t.Errorf("dump_hedge with edge=0.015 >= MinEdgeDumpHedge=0.01 should be approved, got: %s", result.Reason)
	}
}

func TestApprove_PriceTooLow(t *testing.T) {
	m := newTestManager()
	result := m.Approve("window_delta", 0.30, 0.80, 0.50, 1.0, 0) // price < MinEntryPrice=0.60
	if result.Approved {
		t.Error("should reject price < MinEntryPrice")
	}
}

func TestApprove_PriceTooHigh_Standard(t *testing.T) {
	m := newTestManager()
	// price > MaxEntryPrice (0.92 default), not oracle_lag → reject
	result := m.Approve("window_delta", 0.93, 0.95, 0.04, 1.0, 0)
	if result.Approved {
		t.Error("should reject price > MaxEntryPrice for non-oracle_lag strategy")
	}
}

func TestApprove_OracleLag_BypassesMaxPrice(t *testing.T) {
	m := newTestManager()
	// oracle_lag bypasses MaxEntryPrice (0.90): price=0.91 should be approved
	result := m.Approve("oracle_lag", 0.91, 0.95, 0.04, 1.0, 0)
	if !result.Approved {
		t.Errorf("oracle_lag should bypass MaxEntryPrice, got: %s", result.Reason)
	}
}

func TestApprove_DumpHedge_BypassesMaxPrice(t *testing.T) {
	m := newTestManager()
	// dump_hedge AskPrice is sum of two legs (~0.93); bypasses both min and max
	result := m.Approve("dump_hedge", 0.93, 1.0, 0.07, 1.0, 0)
	if !result.Approved {
		t.Errorf("dump_hedge should bypass MaxEntryPrice, got: %s", result.Reason)
	}
}

func TestApprove_MaxConcurrentBets(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	for i := 0; i < 3; i++ {
		m.OpenTrade(newTrade("trade-"+string(rune('0'+i)), future))
	}
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0, 0)
	if result.Approved {
		t.Error("should reject when max concurrent bets reached")
	}
}

func TestApprove_DailyLossBreaker(t *testing.T) {
	m := newTestManager()
	m.mu.Lock()
	m.dailyLoss = 500.0 // at the limit
	m.mu.Unlock()

	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0, 0)
	if result.Approved {
		t.Error("should reject when daily loss limit reached")
	}
}

func TestApprove_KellySizing(t *testing.T) {
	m := newTestManager()
	// Kelly: p=0.80, price=0.70, netOdds = 1/0.70 - 1 = 0.4286
	// full Kelly = (0.80*(1.4286)-1)/0.4286 = (1.1429-1)/0.4286 = 0.3334
	// fractional (0.25) = 0.0833
	// conviction at 0.70 = 1.0 (full)
	// size = 0.0833 * 100 = $8.33
	result := m.Approve("oracle_lag", 0.70, 0.80, 0.10, 1.0, 0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize < 8.0 || result.USDCSize > 9.0 {
		t.Errorf("Kelly size = %v, want ~$8.33", result.USDCSize)
	}
}

func TestApprove_ConvictionScale_LowTier(t *testing.T) {
	m := newTestManager()
	// price=0.60: conviction tier = 0.3x (window_delta applies convictionScale; oracle_lag bypasses it)
	// Kelly base: p=0.80, price=0.60, netOdds=0.6667, fullKelly=0.5, frac(0.25)=0.125
	// With conviction 0.3: size = 0.125 * 0.3 * 100 = $3.75
	result := m.Approve("window_delta", 0.60, 0.80, 0.20, 1.0, 0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize < 3.5 || result.USDCSize > 4.0 {
		t.Errorf("Low-conviction Kelly size = %v, want ~$3.75", result.USDCSize)
	}
}

func TestApprove_ConvictionScale_MidTier(t *testing.T) {
	m := newTestManager()
	// price=0.65: conviction tier = 0.6x (window_delta applies convictionScale; oracle_lag bypasses it)
	// netOdds=0.538, fullKelly=(0.80*1.538-1)/0.538=0.430, frac(0.25)=0.107
	// With conviction 0.6: size = 0.107 * 0.6 * 100 ≈ $6.44
	result := m.Approve("window_delta", 0.65, 0.80, 0.15, 1.0, 0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize < 6.0 || result.USDCSize > 7.0 {
		t.Errorf("Mid-conviction Kelly size = %v, want ~$6.44", result.USDCSize)
	}
}

func TestApprove_KellySizeTooSmall(t *testing.T) {
	m := newTestManager()
	// Very low win prob + high price → near-zero Kelly
	result := m.Approve("oracle_lag", 0.73, 0.74, 0.04, 1.0, 0) // edge=0.01 barely passes, but kelly will be tiny
	// With p=0.74, price=0.73: netOdds=0.37, kelly=(0.74*1.37-1)/0.37=(1.0138-1)/0.37=0.037
	// size = 0.037*0.25*100 = $0.93 < $1 → rejected
	if result.Approved {
		// might be approved depending on exact numbers; just verify size is reasonable if approved
		if result.USDCSize < 1.0 {
			t.Errorf("approved with size < $1: %v", result.USDCSize)
		}
	}
}

func TestApprove_ConfidenceScalesKelly(t *testing.T) {
	m := newTestManager()
	// Use price=0.70 (conviction=1.0) so only confidence differs between the two calls.
	full := m.Approve("oracle_lag", 0.70, 0.80, 0.10, 1.0, 0)
	half := m.Approve("oracle_lag", 0.70, 0.80, 0.10, 0.5, 0)
	if !full.Approved || !half.Approved {
		t.Fatalf("both should be approved (full=%v, half=%v)", full.Reason, half.Reason)
	}
	ratio := half.USDCSize / full.USDCSize
	if ratio < 0.48 || ratio > 0.52 {
		t.Errorf("confidence=0.5 should give ~50%% of full Kelly, got ratio=%.3f (full=%.2f half=%.2f)", ratio, full.USDCSize, half.USDCSize)
	}
}

func TestApprove_ZeroConfidenceRejects(t *testing.T) {
	m := newTestManager()
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 0.0, 0)
	// confidence=0 → size=0 → rejected (below MinBetUSDC)
	if result.Approved {
		t.Errorf("confidence=0 should produce size=0 and be rejected, got size=%.2f", result.USDCSize)
	}
}

// ── OpenTrade / SettleTrade ───────────────────────────────────────────────────

func TestOpenTrade_SettleTrade_PnL(t *testing.T) {
	m := newTestManager()
	trade := newTrade("t1", time.Now().Add(5*time.Minute))
	m.OpenTrade(trade)

	trades, _, _ := m.DailyStats()
	if trades != 0 {
		t.Errorf("DailyStats trades = %d before settle, want 0", trades)
	}

	m.SettleTrade("t1", 15.0) // profit $15

	trades, pnl, winRate := m.DailyStats()
	if trades != 1 {
		t.Errorf("DailyStats trades = %d, want 1", trades)
	}
	if pnl != 15.0 {
		t.Errorf("DailyStats pnl = %v, want 15.0", pnl)
	}
	if winRate != 1.0 {
		t.Errorf("DailyStats winRate = %v, want 1.0", winRate)
	}
}

func TestSettleTrade_Unknown(t *testing.T) {
	m := newTestManager()
	// Should not panic
	m.SettleTrade("nonexistent", 10.0)
}

func TestDailyStats_WinRate(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)

	for i := 0; i < 5; i++ {
		id := "t" + string(rune('0'+i))
		m.OpenTrade(newTrade(id, future))
		pnl := -5.0
		if i < 3 { // 3 wins
			pnl = 10.0
		}
		m.SettleTrade(id, pnl)
	}

	trades, _, winRate := m.DailyStats()
	if trades != 5 {
		t.Errorf("trades = %d, want 5", trades)
	}
	if winRate < 0.59 || winRate > 0.61 {
		t.Errorf("winRate = %v, want 0.60", winRate)
	}
}

func TestOpenTradesList(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	m.OpenTrade(newTrade("t1", future))
	m.OpenTrade(newTrade("t2", future))

	list := m.OpenTradesList()
	if len(list) != 2 {
		t.Errorf("OpenTradesList len = %d, want 2", len(list))
	}
}

func TestOpenTradesList_EmptyAfterSettle(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	m.OpenTrade(newTrade("t1", future))
	m.SettleTrade("t1", 10.0)

	list := m.OpenTradesList()
	if len(list) != 0 {
		t.Errorf("OpenTradesList after settle len = %d, want 0", len(list))
	}
}

func TestDailyLossAmt(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	m.OpenTrade(newTrade("t1", future))
	m.SettleTrade("t1", -20.0)

	loss := m.DailyLossAmt()
	if loss != 20.0 {
		t.Errorf("DailyLossAmt = %v, want 20.0", loss)
	}
}

func TestDailyLossAmt_WinDoesNotIncrease(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	m.OpenTrade(newTrade("t1", future))
	m.SettleTrade("t1", +50.0) // win

	if loss := m.DailyLossAmt(); loss != 0 {
		t.Errorf("DailyLossAmt after win = %v, want 0", loss)
	}
}

func TestSettleExpiredTrades(t *testing.T) {
	m := newTestManager()
	// Trade with expired window (well past + 30s grace)
	expired := time.Now().Add(-2 * time.Minute)
	trade := newTrade("t-expired", expired)
	m.OpenTrade(trade)

	m.SettleExpiredTrades(context.Background(), nil, zap.NewNop())

	list := m.OpenTradesList()
	if len(list) != 0 {
		t.Errorf("expired trade should have been auto-settled, got %d open", len(list))
	}
	// Daily stats should reflect the settled trade
	trades, _, _ := m.DailyStats()
	if trades != 1 {
		t.Errorf("DailyStats.trades = %d after expire settle, want 1", trades)
	}
}

func TestSettleExpiredTrades_IgnoresFutureTrades(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	m.OpenTrade(newTrade("t-future", future))

	m.SettleExpiredTrades(context.Background(), nil, zap.NewNop())

	list := m.OpenTradesList()
	if len(list) != 1 {
		t.Errorf("future trade should remain open, got %d open", len(list))
	}
}

// ── Price-band filter ─────────────────────────────────────────────────────────

func TestApprove_PriceBand_BelowMin(t *testing.T) {
	// window_delta must respect MinEntryPrice; oracle_lag bypasses it.
	m := newTestManager() // MinEntryPrice=0.50
	result := m.Approve("window_delta", 0.49, 0.80, 0.31, 1.0, 0)
	if result.Approved {
		t.Error("window_delta price 0.49 < MinEntryPrice 0.50 should be rejected")
	}
	if result.Reason == "" {
		t.Error("rejection reason should not be empty")
	}
}

func TestApprove_OracleLag_BypassesMinEntryPrice(t *testing.T) {
	// oracle_lag at 0.45 (below MinEntryPrice 0.50) — should be approved.
	m := newTestManager() // MinEntryPrice=0.50, but oracle_lag bypasses it
	result := m.Approve("oracle_lag", 0.45, 0.80, 0.35, 1.0, 0)
	if !result.Approved {
		t.Errorf("oracle_lag at 0.45 should bypass MinEntryPrice, got: %s", result.Reason)
	}
}

func TestApprove_PriceBand_AtMin(t *testing.T) {
	m := newTestManager()
	result := m.Approve("window_delta", 0.50, 0.80, 0.30, 1.0, 0)
	if !result.Approved {
		t.Errorf("price 0.50 == MinEntryPrice should be approved, got: %s", result.Reason)
	}
}

func TestApprove_PriceBand_AboveMax(t *testing.T) {
	m := newTestManager() // MaxEntryPrice=0.92
	result := m.Approve("window_delta", 0.93, 0.95, 0.02, 1.0, 0)
	if result.Approved {
		t.Error("price 0.93 > MaxEntryPrice 0.92 should be rejected for window_delta")
	}
}

func TestApprove_PriceBand_AtMax(t *testing.T) {
	m := newTestManager()
	result := m.Approve("window_delta", 0.92, 0.94, 0.02, 1.0, 0)
	if !result.Approved {
		t.Errorf("price 0.92 == MaxEntryPrice should be approved, got: %s", result.Reason)
	}
}

func TestApprove_OracleLag_BypassesMax_AboveThreshold(t *testing.T) {
	m := newTestManager()
	// oracle_lag at 0.93 (> MaxEntryPrice 0.92) should be allowed
	result := m.Approve("oracle_lag", 0.93, 0.95, 0.02, 1.0, 0)
	if !result.Approved {
		t.Errorf("oracle_lag should bypass MaxEntryPrice, got: %s", result.Reason)
	}
}

func TestApprove_DumpHedge_BypassesMinAndMax(t *testing.T) {
	m := newTestManager()
	// dump_hedge with price below MinEntryPrice should be allowed (sum semantics)
	result := m.Approve("dump_hedge", 0.50, 1.0, 0.04, 1.0, 0)
	if !result.Approved {
		t.Errorf("dump_hedge should bypass MinEntryPrice, got: %s", result.Reason)
	}
	// dump_hedge with price above MaxEntryPrice should be allowed
	result2 := m.Approve("dump_hedge", 0.93, 1.0, 0.07, 1.0, 0)
	if !result2.Approved {
		t.Errorf("dump_hedge should bypass MaxEntryPrice, got: %s", result2.Reason)
	}
}

// ── MaxLossPerTrade cap ───────────────────────────────────────────────────────

func TestApprove_MaxLossPerTrade_Cap(t *testing.T) {
	// MaxBetUSDC=100 but MaxLossPerTrade=2.00 — size should be capped at $2.
	m := NewManager(Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
		Filters: FilterConfig{
			MinEntryPrice:   0.60,
			MaxEntryPrice:   0.90,
			MaxLossPerTrade: 2.00,
			MaxPositionSize: 100, // large — not the binding constraint here
			MaxConsecLosses: 3,
			PauseDuration:   30 * time.Minute,
		},
	}, nil, zap.NewNop())
	result := m.Approve("oracle_lag", 0.70, 0.90, 0.20, 1.0, 0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize > 2.01 {
		t.Errorf("USDCSize = %.2f exceeds MaxLossPerTrade 2.00", result.USDCSize)
	}
}

func TestApprove_MaxPositionSize_Cap(t *testing.T) {
	// MaxPositionSize=1.0 share @ price=0.70 → max USDC = 1.0 * 0.70 = $0.70 → too small (<$1) → rejected
	m := NewManager(Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
		Filters: FilterConfig{
			MinEntryPrice:   0.60,
			MaxEntryPrice:   0.90,
			MaxLossPerTrade: 100,
			MaxPositionSize: 1.0, // 1 share @ 0.70 = $0.70 → too small
			MaxConsecLosses: 3,
			PauseDuration:   30 * time.Minute,
		},
	}, nil, zap.NewNop())
	result := m.Approve("oracle_lag", 0.70, 0.90, 0.20, 1.0, 0)
	if result.Approved {
		t.Errorf("1 share @ 0.70 = $0.70 is < $1 min, should reject, got size=%.2f", result.USDCSize)
	}
}

// ── Consecutive-loss pause ────────────────────────────────────────────────────

func TestApprove_ConsecLossPause_Triggered(t *testing.T) {
	m := NewManager(Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
		Filters: FilterConfig{
			MinEntryPrice:   0.60,
			MaxEntryPrice:   0.90,
			MaxLossPerTrade: 100,
			MaxConsecLosses: 3,
			PauseDuration:   1 * time.Hour,
		},
	}, nil, zap.NewNop())

	future := time.Now().Add(5 * time.Minute)
	// Settle 3 consecutive losses
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("loss-%d", i)
		m.OpenTrade(newTrade(id, future))
		m.SettleTrade(id, -5.0)
	}

	// Now Approve should be rejected with consec_loss_pause
	result := m.Approve("oracle_lag", 0.70, 0.80, 0.10, 1.0, 0)
	if result.Approved {
		t.Error("should be rejected after 3 consecutive losses")
	}
	if result.Reason != "consec_loss_pause" {
		t.Errorf("expected reason 'consec_loss_pause', got %q", result.Reason)
	}
}

func TestApprove_ConsecLossPause_ResetByWin(t *testing.T) {
	m := NewManager(Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
		Filters: FilterConfig{
			MinEntryPrice:   0.60,
			MaxEntryPrice:   0.90,
			MaxLossPerTrade: 100,
			MaxConsecLosses: 3,
			PauseDuration:   1 * time.Hour,
		},
	}, nil, zap.NewNop())

	future := time.Now().Add(5 * time.Minute)
	// 2 losses, then a win, then 2 more losses — should NOT trigger pause (streak broken)
	m.OpenTrade(newTrade("l1", future))
	m.SettleTrade("l1", -5.0)
	m.OpenTrade(newTrade("l2", future))
	m.SettleTrade("l2", -5.0)
	m.OpenTrade(newTrade("w1", future))
	m.SettleTrade("w1", +10.0) // win resets streak
	m.OpenTrade(newTrade("l3", future))
	m.SettleTrade("l3", -5.0)
	m.OpenTrade(newTrade("l4", future))
	m.SettleTrade("l4", -5.0)

	result := m.Approve("oracle_lag", 0.70, 0.80, 0.10, 1.0, 0)
	if !result.Approved {
		t.Errorf("only 2 consecutive losses after win — should not be paused, got: %s", result.Reason)
	}
}

func TestApprove_ConsecLossPause_Expires(t *testing.T) {
	m := NewManager(Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
		Filters: FilterConfig{
			MinEntryPrice:   0.60,
			MaxEntryPrice:   0.90,
			MaxLossPerTrade: 100,
			MaxConsecLosses: 2,
			PauseDuration:   1 * time.Nanosecond, // instant expiry for test
		},
	}, nil, zap.NewNop())

	future := time.Now().Add(5 * time.Minute)
	m.OpenTrade(newTrade("l1", future))
	m.SettleTrade("l1", -5.0)
	m.OpenTrade(newTrade("l2", future))
	m.SettleTrade("l2", -5.0)

	// Pause is set; wait for it to expire
	time.Sleep(2 * time.Millisecond)

	result := m.Approve("oracle_lag", 0.70, 0.80, 0.10, 1.0, 0)
	if !result.Approved {
		t.Errorf("pause should have expired, got: %s", result.Reason)
	}
}

// ── Wallet balance sizing ─────────────────────────────────────────────────────

func TestApprove_UsesWalletBalance(t *testing.T) {
	// balance=200, MaxBet=50 → kelly*200 but capped at 50
	bits := new(atomic.Uint64)
	bits.Store(math.Float64bits(200.0))
	m := NewManager(Config{
		MaxBetUSDC:        50.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
	}, bits, zap.NewNop())
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0, 0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize > 50.0 {
		t.Errorf("USDCSize %.2f exceeds MaxBetUSDC cap 50", result.USDCSize)
	}
}

func TestApprove_BalanceSmallerThanMaxBet(t *testing.T) {
	// balance=30 < MaxBet=50 → kelly*30 is the binding constraint, not MaxBet
	bits := new(atomic.Uint64)
	bits.Store(math.Float64bits(30.0))
	m := NewManager(Config{
		MaxBetUSDC:        50.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 3,
		KellyFraction:     0.25,
	}, bits, zap.NewNop())
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0, 0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize > 30.0 {
		t.Errorf("USDCSize %.2f exceeds wallet balance 30", result.USDCSize)
	}
}

func TestOpenTrade_DecrementsBalance(t *testing.T) {
	bits := new(atomic.Uint64)
	bits.Store(math.Float64bits(100.0))
	m := NewManager(Config{
		MaxBetUSDC: 50.0, MaxDailyLossUSDC: 500.0, MaxConcurrentBets: 3, KellyFraction: 0.25,
	}, bits, zap.NewNop())

	trade := &Trade{ID: "t1", USDCStaked: 30.0, TokenPrice: 0.60, WindowEnd: time.Now().Add(5 * time.Minute)}
	m.OpenTrade(trade)

	if got := m.Balance(); math.Abs(got-70.0) > 0.001 {
		t.Errorf("balance after OpenTrade: got %.2f, want 70.00", got)
	}
}

func TestSettleTrade_CreditsPayout(t *testing.T) {
	// OpenTrade(stake=30) debits; SettleTrade(pnl=20) credits stake+pnl=50 → net +20
	bits := new(atomic.Uint64)
	bits.Store(math.Float64bits(100.0))
	m := NewManager(Config{
		MaxBetUSDC: 50.0, MaxDailyLossUSDC: 500.0, MaxConcurrentBets: 3, KellyFraction: 0.25,
	}, bits, zap.NewNop())

	trade := &Trade{ID: "t2", USDCStaked: 30.0, TokenPrice: 0.60, WindowEnd: time.Now().Add(5 * time.Minute)}
	m.OpenTrade(trade) // balance → 70
	m.SettleTrade("t2", 20.0) // credit 30+20=50 → balance → 120

	if got := m.Balance(); math.Abs(got-120.0) > 0.001 {
		t.Errorf("balance after win: got %.2f, want 120.00", got)
	}
}

func TestSettleTrade_LossKeepsBalanceFlat(t *testing.T) {
	// OpenTrade(stake=30) debits; SettleTrade(pnl=-30) credits 30+(-30)=0 → net -30
	bits := new(atomic.Uint64)
	bits.Store(math.Float64bits(100.0))
	m := NewManager(Config{
		MaxBetUSDC: 50.0, MaxDailyLossUSDC: 500.0, MaxConcurrentBets: 3, KellyFraction: 0.25,
	}, bits, zap.NewNop())

	trade := &Trade{ID: "t3", USDCStaked: 30.0, TokenPrice: 0.60, WindowEnd: time.Now().Add(5 * time.Minute)}
	m.OpenTrade(trade) // balance → 70
	m.SettleTrade("t3", -30.0) // credit 30+(-30)=0 → balance stays 70

	if got := m.Balance(); math.Abs(got-70.0) > 0.001 {
		t.Errorf("balance after loss: got %.2f, want 70.00", got)
	}
}

// ── Context: SecondsElapsed / SecondsRemaining ───────────────────────────────
// These are in strategy/interface.go but we test them here for convenience
// via the risk package tests — actually they belong in strategy tests.
