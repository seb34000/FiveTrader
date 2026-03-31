package sim

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/seb/fivetrader/risk"
)

func newTestSimulator(t *testing.T) *Simulator {
	t.Helper()
	var liveBits, oracleBits atomic.Uint64
	liveBits.Store(math.Float64bits(85000.0))
	s, err := NewSimulator(&liveBits, &oracleBits, t.TempDir()+"/journal.jsonl", zap.NewNop())
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newSimTrade(id, strategy, direction string, tokenPrice, usdcStaked float64, windowEnd time.Time) (*risk.Trade, float64) {
	return &risk.Trade{
		ID:         id,
		Strategy:   strategy,
		Direction:  direction,
		TokenID:    "token-123",
		USDCStaked: usdcStaked,
		TokenPrice: tokenPrice,
		Timestamp:  time.Now(),
		WindowEnd:  windowEnd,
	}, 0.0 // askDown = 0 for single-leg
}

// ── computePnL ────────────────────────────────────────────────────────────────

func TestComputePnL_SingleLeg_Win_UP(t *testing.T) {
	s := &Simulator{}
	st := &SimTrade{
		Trade: &risk.Trade{
			Direction:  "UP",
			USDCStaked: 10.0,
			TokenPrice: 0.60,
		},
		IsDumpHedge: false,
	}
	// BTC went up → UP wins (2% fee on gross profit)
	pnl := s.computePnL(st, true)
	want := 10.0 * (1.0/0.60 - 1.0) * 0.98
	if math.Abs(pnl-want) > 0.01 {
		t.Errorf("computePnL UP win = %v, want %v", pnl, want)
	}
}

func TestComputePnL_SingleLeg_Loss_UP(t *testing.T) {
	s := &Simulator{}
	st := &SimTrade{
		Trade: &risk.Trade{
			Direction:  "UP",
			USDCStaked: 10.0,
			TokenPrice: 0.60,
		},
		IsDumpHedge: false,
	}
	// BTC went down → UP loses
	pnl := s.computePnL(st, false)
	if pnl != -10.0 {
		t.Errorf("computePnL UP loss = %v, want -10.0", pnl)
	}
}

func TestComputePnL_SingleLeg_Win_DOWN(t *testing.T) {
	s := &Simulator{}
	st := &SimTrade{
		Trade: &risk.Trade{
			Direction:  "DOWN",
			USDCStaked: 10.0,
			TokenPrice: 0.45,
		},
		IsDumpHedge: false,
	}
	// BTC went down → DOWN wins (2% fee on gross profit)
	pnl := s.computePnL(st, false) // btcWentUp=false
	want := 10.0 * (1.0/0.45 - 1.0) * 0.98
	if math.Abs(pnl-want) > 0.01 {
		t.Errorf("computePnL DOWN win = %v, want %v", pnl, want)
	}
}

func TestComputePnL_SingleLeg_Loss_DOWN(t *testing.T) {
	s := &Simulator{}
	st := &SimTrade{
		Trade: &risk.Trade{
			Direction:  "DOWN",
			USDCStaked: 10.0,
			TokenPrice: 0.45,
		},
		IsDumpHedge: false,
	}
	pnl := s.computePnL(st, true) // BTC went up → DOWN loses
	if pnl != -10.0 {
		t.Errorf("computePnL DOWN loss = %v, want -10.0", pnl)
	}
}

func TestComputePnL_DumpHedge_BTCUp(t *testing.T) {
	s := &Simulator{}
	// TokenPrice = sum = askUp + askDown = 0.45 + 0.48 = 0.93
	// USDCStaked = total budget; nTokens = 10 / 0.93; payout = nTokens; pnl = payout - 10
	st := &SimTrade{
		Trade: &risk.Trade{
			USDCStaked: 10.0,
			TokenPrice: 0.93, // sum
		},
		IsDumpHedge: true,
	}
	pnl := s.computePnL(st, true)
	want := 10.0 * (1.0/0.93 - 1.0) * 0.98
	if math.Abs(pnl-want) > 0.01 {
		t.Errorf("dump_hedge BTC up pnl = %v, want %v", pnl, want)
	}
	if pnl <= 0 {
		t.Errorf("dump_hedge should always be profitable, got %v", pnl)
	}
}

func TestComputePnL_DumpHedge_BTCDown(t *testing.T) {
	s := &Simulator{}
	// PnL is direction-independent: same formula regardless of btcWentUp.
	st := &SimTrade{
		Trade: &risk.Trade{
			USDCStaked: 10.0,
			TokenPrice: 0.93, // sum
		},
		IsDumpHedge: true,
	}
	pnl := s.computePnL(st, false)
	want := 10.0 * (1.0/0.93 - 1.0) * 0.98
	if math.Abs(pnl-want) > 0.01 {
		t.Errorf("dump_hedge BTC down pnl = %v, want %v", pnl, want)
	}
	if pnl <= 0 {
		t.Errorf("dump_hedge should always be profitable, got %v", pnl)
	}
}

// ── RegisterTrade ─────────────────────────────────────────────────────────────

func TestRegisterTrade(t *testing.T) {
	s := newTestSimulator(t)
	trade, _ := newSimTrade("t1", "oracle_lag", "UP", 0.60, 10.0, time.Now().Add(5*time.Minute))
	s.RegisterTrade("t1", trade, 85000.0, 0, 0, 0.75, 0.15, 0.9)

	s.mu.Lock()
	_, ok := s.trades["t1"]
	s.mu.Unlock()

	if !ok {
		t.Error("trade should be registered")
	}
}

func TestRegisterTrade_DumpHedge_StoresAskDown(t *testing.T) {
	s := newTestSimulator(t)
	trade := &risk.Trade{
		ID: "hedge1", Strategy: "dump_hedge",
		Direction: "BOTH", USDCStaked: 10.0, TokenPrice: 0.93, // sum = askUp+askDown
		Timestamp: time.Now(), WindowEnd: time.Now().Add(5 * time.Minute),
	}
	s.RegisterTrade("hedge1", trade, 85000.0, 0.48, 0, 1.0, 0.07, 1.0)

	s.mu.Lock()
	st := s.trades["hedge1"]
	s.mu.Unlock()

	if !st.IsDumpHedge {
		t.Error("IsDumpHedge should be true for dump_hedge strategy")
	}
	if st.AskPriceDown != 0.48 {
		t.Errorf("AskPriceDown = %v, want 0.48", st.AskPriceDown)
	}
}

// ── SettleSimTrades ───────────────────────────────────────────────────────────

func TestSettleSimTrades_SettlesExpiredTrade(t *testing.T) {
	var liveBits, oracleBits atomic.Uint64
	liveBits.Store(math.Float64bits(86000.0)) // BTC went up
	s, err := NewSimulator(&liveBits, &oracleBits, t.TempDir()+"/j.jsonl", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rm := newTestRiskManager()
	// expired window (well past + 30s grace)
	expired := time.Now().Add(-2 * time.Minute)
	trade := &risk.Trade{
		ID: "t1", Strategy: "oracle_lag", Direction: "UP",
		USDCStaked: 10.0, TokenPrice: 0.60,
		Timestamp: time.Now(), WindowEnd: expired,
	}
	rm.OpenTrade(trade)
	s.RegisterTrade("t1", trade, 85000.0, 0, 0, 0.75, 0.15, 0.9) // window open < 86000 → UP wins

	s.SettleSimTrades(rm)

	stats := s.StrategyStats()
	if st, ok := stats["oracle_lag"]; !ok || st.Count != 1 {
		t.Errorf("expected 1 settled trade, got %+v", stats)
	}
	if st := stats["oracle_lag"]; st.WinRate != 1.0 {
		t.Errorf("UP trade when BTC went up should win, WinRate=%v", st.WinRate)
	}
}

func TestSettleSimTrades_IgnoresFutureTrade(t *testing.T) {
	var liveBits, oracleBits atomic.Uint64
	liveBits.Store(math.Float64bits(86000.0))
	s, err := NewSimulator(&liveBits, &oracleBits, t.TempDir()+"/j.jsonl", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rm := newTestRiskManager()
	future := time.Now().Add(5 * time.Minute)
	trade, _ := newSimTrade("t1", "oracle_lag", "UP", 0.60, 10.0, future)
	rm.OpenTrade(trade)
	s.RegisterTrade("t1", trade, 85000.0, 0, 0, 0.75, 0.15, 0.9)

	s.SettleSimTrades(rm)

	if len(rm.OpenTradesList()) != 1 {
		t.Error("future trade should not be settled")
	}
}

// ── StrategyStats ─────────────────────────────────────────────────────────────

func TestStrategyStats_Empty(t *testing.T) {
	s := newTestSimulator(t)
	stats := s.StrategyStats()
	if len(stats) != 0 {
		t.Errorf("expected empty stats, got %d entries", len(stats))
	}
}

func TestStrategyStats_MultipleStrategies(t *testing.T) {
	var liveBits, oracleBits atomic.Uint64
	liveBits.Store(math.Float64bits(86000.0)) // BTC went up
	s, _ := NewSimulator(&liveBits, &oracleBits, t.TempDir()+"/j.jsonl", zap.NewNop())
	defer s.Close()
	rm := newTestRiskManager()

	expired := time.Now().Add(-2 * time.Minute)

	// oracle_lag UP win
	t1 := &risk.Trade{ID: "t1", Strategy: "oracle_lag", Direction: "UP", USDCStaked: 10.0, TokenPrice: 0.60, Timestamp: time.Now(), WindowEnd: expired}
	rm.OpenTrade(t1)
	s.RegisterTrade("t1", t1, 85000.0, 0, 0, 0.75, 0.15, 0.9)

	// window_delta DOWN loss (BTC went up, DOWN loses)
	t2 := &risk.Trade{ID: "t2", Strategy: "window_delta", Direction: "DOWN", USDCStaked: 10.0, TokenPrice: 0.55, Timestamp: time.Now(), WindowEnd: expired}
	rm.OpenTrade(t2)
	s.RegisterTrade("t2", t2, 85000.0, 0, 0, 0.58, 0.03, 0.7)

	s.SettleSimTrades(rm)
	stats := s.StrategyStats()

	if stats["oracle_lag"].Count != 1 || stats["oracle_lag"].WinRate != 1.0 {
		t.Errorf("oracle_lag stats = %+v, want count=1 winrate=1.0", stats["oracle_lag"])
	}
	if stats["window_delta"].Count != 1 || stats["window_delta"].WinRate != 0.0 {
		t.Errorf("window_delta stats = %+v, want count=1 winrate=0.0", stats["window_delta"])
	}
}

// ── Journal ───────────────────────────────────────────────────────────────────

func TestJournal_RecordAndClose(t *testing.T) {
	path := t.TempDir() + "/journal.jsonl"
	j, err := newTradeJournal(path)
	if err != nil {
		t.Fatalf("newTradeJournal: %v", err)
	}

	rec := TradeRecord{
		ID:          "test-1",
		Strategy:    "oracle_lag",
		Direction:   "UP",
		TokenPrice:  0.60,
		USDCStaked:  10.0,
		Won:         true,
		PnL:         6.67,
		EntryTime:   time.Now(),
		SettledAt:   time.Now(),
	}
	j.record(rec)

	if err := j.close(); err != nil {
		t.Fatalf("journal close: %v", err)
	}

	// Reopen and verify
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
		var got TradeRecord
		if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
			t.Errorf("unmarshal line: %v", err)
		}
		if got.ID != "test-1" {
			t.Errorf("ID = %q, want test-1", got.ID)
		}
		if got.PnL < 6.66 || got.PnL > 6.68 {
			t.Errorf("PnL = %v, want ~6.67", got.PnL)
		}
	}
	if lines != 1 {
		t.Errorf("expected 1 JSONL line, got %d", lines)
	}
}

// ── Oracle settlement ─────────────────────────────────────────────────────────

func TestSettleSimTrades_UsesOraclePriceWhenAvailable(t *testing.T) {
	var liveBits, oracleBits atomic.Uint64
	// live says BTC went up, oracle says it went down — oracle should win
	liveBits.Store(math.Float64bits(86000.0))  // live: up from 85000
	oracleBits.Store(math.Float64bits(84000.0)) // oracle: down from 85000
	s, err := NewSimulator(&liveBits, &oracleBits, t.TempDir()+"/j.jsonl", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rm := newTestRiskManager()
	expired := time.Now().Add(-2 * time.Minute)
	trade := &risk.Trade{
		ID: "t1", Strategy: "oracle_lag", Direction: "UP",
		USDCStaked: 10.0, TokenPrice: 0.60,
		Timestamp: time.Now(), WindowEnd: expired,
	}
	rm.OpenTrade(trade)
	s.RegisterTrade("t1", trade, 85000.0, 0, 84500.0, 0.75, 0.15, 0.9)

	s.SettleSimTrades(rm)

	stats := s.StrategyStats()
	// Oracle says DOWN (84000 < 85000), UP bet loses
	if stats["oracle_lag"].WinRate != 0.0 {
		t.Errorf("oracle price should determine outcome: UP should lose when oracle went down, WinRate=%v", stats["oracle_lag"].WinRate)
	}
}

func TestSettleSimTrades_FallsBackToLiveWhenOracleZero(t *testing.T) {
	var liveBits, oracleBits atomic.Uint64
	liveBits.Store(math.Float64bits(86000.0)) // BTC went up
	// oracleBits = 0 (not yet polled)
	s, err := NewSimulator(&liveBits, &oracleBits, t.TempDir()+"/j.jsonl", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rm := newTestRiskManager()
	expired := time.Now().Add(-2 * time.Minute)
	trade := &risk.Trade{
		ID: "t1", Strategy: "oracle_lag", Direction: "UP",
		USDCStaked: 10.0, TokenPrice: 0.60,
		Timestamp: time.Now(), WindowEnd: expired,
	}
	rm.OpenTrade(trade)
	s.RegisterTrade("t1", trade, 85000.0, 0, 0, 0.75, 0.15, 0.9)

	s.SettleSimTrades(rm)

	stats := s.StrategyStats()
	// Falls back to live (86000 > 85000) → UP wins
	if stats["oracle_lag"].WinRate != 1.0 {
		t.Errorf("with oracle=0, should fallback to live price: UP should win when BTC went up, WinRate=%v", stats["oracle_lag"].WinRate)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestRiskManager() *risk.Manager {
	return risk.NewManager(risk.Config{
		MaxBetUSDC:        100.0,
		MaxDailyLossUSDC:  500.0,
		MaxConcurrentBets: 10,
		KellyFraction:     0.25,
	}, zap.NewNop())
}
