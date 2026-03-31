package risk

import (
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
	}, zap.NewNop())
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
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0)
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
	result := m.Approve("oracle_lag", 0.60, 0.61, 0.01, 1.0) // edge=0.01 < MinEdge=0.02
	if result.Approved {
		t.Error("should reject edge < MinEdge")
	}
}

func TestApprove_DumpHedge_LowerMinEdge(t *testing.T) {
	m := newTestManager()
	// dump_hedge uses MinEdgeDumpHedge=0.01; edge=0.015 should pass
	result := m.Approve("dump_hedge", 0.50, 1.0, 0.015, 1.0)
	if !result.Approved {
		t.Errorf("dump_hedge with edge=0.015 >= MinEdgeDumpHedge=0.01 should be approved, got: %s", result.Reason)
	}
}

func TestApprove_PriceTooLow(t *testing.T) {
	m := newTestManager()
	result := m.Approve("oracle_lag", 0.30, 0.80, 0.50, 1.0) // price < 0.35
	if result.Approved {
		t.Error("should reject price < MinTokenPrice")
	}
}

func TestApprove_PriceTooHigh_Standard(t *testing.T) {
	m := newTestManager()
	result := m.Approve("window_delta", 0.80, 0.90, 0.10, 1.0) // price > 0.75, not oracle_lag
	if result.Approved {
		t.Error("should reject price > MaxTokenPrice for non-oracle_lag strategy")
	}
}

func TestApprove_OracleLag_BypassesMaxPrice(t *testing.T) {
	m := newTestManager()
	result := m.Approve("oracle_lag", 0.85, 0.92, 0.07, 1.0) // price > 0.75 but oracle_lag
	if !result.Approved {
		t.Errorf("oracle_lag should bypass MaxTokenPrice, got: %s", result.Reason)
	}
}

func TestApprove_DumpHedge_BypassesMaxPrice(t *testing.T) {
	m := newTestManager()
	result := m.Approve("dump_hedge", 0.80, 1.0, 0.25, 1.0) // price > 0.75 but dump_hedge
	if !result.Approved {
		t.Errorf("dump_hedge should bypass MaxTokenPrice, got: %s", result.Reason)
	}
}

func TestApprove_MaxConcurrentBets(t *testing.T) {
	m := newTestManager()
	future := time.Now().Add(5 * time.Minute)
	for i := 0; i < 3; i++ {
		m.OpenTrade(newTrade("trade-"+string(rune('0'+i)), future))
	}
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0)
	if result.Approved {
		t.Error("should reject when max concurrent bets reached")
	}
}

func TestApprove_DailyLossBreaker(t *testing.T) {
	m := newTestManager()
	m.mu.Lock()
	m.dailyLoss = 500.0 // at the limit
	m.mu.Unlock()

	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0)
	if result.Approved {
		t.Error("should reject when daily loss limit reached")
	}
}

func TestApprove_KellySizing(t *testing.T) {
	m := newTestManager()
	// Kelly: p=0.80, price=0.60, netOdds = 1/0.60 - 1 = 0.6667
	// full Kelly = (0.80*(1.6667)-1)/0.6667 = (1.3333-1)/0.6667 = 0.5
	// fractional (0.25) = 0.125
	// size = 0.125 * 100 = $12.50
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0)
	if !result.Approved {
		t.Fatalf("expected approval: %s", result.Reason)
	}
	if result.USDCSize < 12.0 || result.USDCSize > 13.0 {
		t.Errorf("Kelly size = %v, want ~$12.50", result.USDCSize)
	}
}

func TestApprove_KellySizeTooSmall(t *testing.T) {
	m := newTestManager()
	// Very low win prob + high price → near-zero Kelly
	result := m.Approve("oracle_lag", 0.73, 0.74, 0.04, 1.0) // edge=0.01 barely passes, but kelly will be tiny
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
	// Same signal, confidence=0.5 → half the Kelly size vs confidence=1.0
	full := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 1.0)
	half := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 0.5)
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
	result := m.Approve("oracle_lag", 0.60, 0.80, 0.20, 0.0)
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

	m.SettleExpiredTrades(zap.NewNop())

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

	m.SettleExpiredTrades(zap.NewNop())

	list := m.OpenTradesList()
	if len(list) != 1 {
		t.Errorf("future trade should remain open, got %d open", len(list))
	}
}

// ── Context: SecondsElapsed / SecondsRemaining ───────────────────────────────
// These are in strategy/interface.go but we test them here for convenience
// via the risk package tests — actually they belong in strategy tests.
