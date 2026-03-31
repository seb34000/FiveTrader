package strategy

import (
	"testing"
	"time"

	"github.com/seb/fivetrader/market"
)

func baseOracleLagCtx() *Context {
	now := time.Now()
	windowStart := now.Add(-150 * time.Second) // halfway through window
	return &Context{
		LivePrice:   85000.0,
		OraclePrice: 84745.5, // ~0.3% below live → delta +0.3%
		OracleAge:   10 * time.Second,
		Market: market.State{
			TokenIDUp:   "token-up-123",
			TokenIDDown: "token-down-456",
			AskUp:       0.52,
			AskDown:     0.50,
			WindowStart: windowStart,
			WindowEnd:   windowStart.Add(5 * time.Minute),
		},
		WindowOpen: 84900.0,
		Now:        now,
	}
}

func TestOracleLag_Name(t *testing.T) {
	s := &OracleLag{}
	if s.Name() != "oracle_lag" {
		t.Errorf("Name() = %q, want oracle_lag", s.Name())
	}
}

func TestOracleLag_Signal_Up(t *testing.T) {
	ctx := baseOracleLagCtx()
	// live > oracle by >0.3% → should signal UP
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0 // delta ~+0.354%

	s := &OracleLag{}
	sig := s.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Direction != DirectionUp {
		t.Errorf("Direction = %v, want Up", sig.Direction)
	}
	if sig.Strategy != "oracle_lag" {
		t.Errorf("Strategy = %q, want oracle_lag", sig.Strategy)
	}
	if sig.AskPrice != ctx.Market.AskUp {
		t.Errorf("AskPrice = %v, want %v", sig.AskPrice, ctx.Market.AskUp)
	}
	if sig.Edge <= 0 {
		t.Errorf("Edge = %v, should be positive", sig.Edge)
	}
}

func TestOracleLag_Signal_Down(t *testing.T) {
	ctx := baseOracleLagCtx()
	// live < oracle by >0.3% → should signal DOWN
	ctx.LivePrice = 84700.0
	ctx.OraclePrice = 85000.0 // delta ~-0.353%

	s := &OracleLag{}
	sig := s.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Direction != DirectionDown {
		t.Errorf("Direction = %v, want Down", sig.Direction)
	}
	if sig.AskPrice != ctx.Market.AskDown {
		t.Errorf("AskPrice = %v, want %v", sig.AskPrice, ctx.Market.AskDown)
	}
}

func TestOracleLag_NoSignal_DeltaTooSmall(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84990.0 // delta ~0.012% < 0.3%

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for tiny delta, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_OracleTooFresh(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.OracleAge = 1 * time.Second // < 3s min

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for fresh oracle, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_OracleTooStale(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.OracleAge = 200 * time.Second // > 120s max

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for stale oracle, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_OraclePriceZero(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.OraclePrice = 0

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for zero oracle price, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_LivePriceZero(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 0

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for zero live price, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_TokenTooExpensive(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.Market.AskUp = 0.95 // > maxTokenPrice 0.92

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for expensive token, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_CloseToExpiration(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	// set window to expire in 3s (< 5s minimum)
	ctx.Now = ctx.Market.WindowEnd.Add(-3 * time.Second)

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal close to expiration, got %+v", sig)
	}
}

func TestOracleLag_NoSignal_NoTokenID(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.Market.TokenIDUp = "" // missing

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil signal for missing tokenID, got %+v", sig)
	}
}

func TestOracleLag_WinProbAt_MinDelta(t *testing.T) {
	ctx := baseOracleLagCtx()
	// delta exactly at minLagThreshold 0.15%
	// Formula: 0.72 + min(0.0015/0.008, 1)*0.20 = 0.72 + 0.1875*0.20 = 0.7575
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 85000.0 / 1.0015 // exactly +0.15%

	s := &OracleLag{}
	sig := s.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal at min delta threshold")
	}
	if sig.WinProb < 0.73 || sig.WinProb > 0.80 {
		t.Errorf("WinProb = %v, want in [0.73, 0.80] at min delta", sig.WinProb)
	}
}

func TestOracleLag_Signal_AtLowerThreshold(t *testing.T) {
	ctx := baseOracleLagCtx()
	// delta = 0.15% (MinLagThreshold) → should fire
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 85000.0 / 1.0015 // +0.15%

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal at delta=0.15% (MinLagThreshold)")
	}
	if sig.Direction != DirectionUp {
		t.Errorf("Direction = %v, want Up", sig.Direction)
	}
	if sig.WinProb <= 0 || sig.Edge <= 0 {
		t.Errorf("WinProb=%.3f Edge=%.3f, both should be positive", sig.WinProb, sig.Edge)
	}
}

func TestOracleLag_WinProbAt_HighDelta(t *testing.T) {
	ctx := baseOracleLagCtx()
	// delta >= 1% → winProb should be 0.92
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84100.0 // delta ~+1.07%

	s := &OracleLag{}
	sig := s.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal at high delta")
	}
	if sig.WinProb < 0.91 || sig.WinProb > 0.93 {
		t.Errorf("WinProb = %v, want ~0.92 at >=1%% delta", sig.WinProb)
	}
}

func TestOracleLag_NoSignal_NegativeEdge(t *testing.T) {
	ctx := baseOracleLagCtx()
	// delta ~+0.354%: winProb = 0.72 + min(0.00354/0.008, 1)*0.20 = 0.72 + 0.4425*0.20 ≈ 0.809
	// Set ask = 0.85 < 0.92 max but > winProb → no edge
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.Market.AskUp = 0.85

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil when edge <= 0 (ask=0.85 > winProb~0.809), got signal with WinProb=%.3f AskPrice=%.3f", sig.WinProb, sig.AskPrice)
	}
}

func TestOracleLag_EdgeIsWinProbMinusAsk(t *testing.T) {
	ctx := baseOracleLagCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	want := sig.WinProb - sig.AskPrice
	if sig.Edge != want {
		t.Errorf("Edge = %v, want WinProb-AskPrice = %v", sig.Edge, want)
	}
}

func TestOracleLag_NoSignal_DirectionMismatch_Up(t *testing.T) {
	// Reproduces the overnight loss scenario: oracle lag says UP (live > oracle),
	// but BTC is actually DOWN from window open — should be filtered out.
	ctx := baseOracleLagCtx()
	ctx.WindowOpen = 86000.0  // BTC opened high
	ctx.LivePrice = 85000.0   // BTC is below window open (DOWN trend)
	ctx.OraclePrice = 84700.0 // oracle lagging below live → delta positive → naively says UP

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil: live(%.0f) < window_open(%.0f) contradicts UP lag signal, got %+v",
			ctx.LivePrice, ctx.WindowOpen, sig)
	}
}

func TestOracleLag_NoSignal_DirectionMismatch_Down(t *testing.T) {
	// Oracle lag says DOWN (live < oracle), but BTC is actually UP from window open.
	ctx := baseOracleLagCtx()
	ctx.WindowOpen = 84000.0  // BTC opened low
	ctx.LivePrice = 84700.0   // BTC is above window open (UP trend)
	ctx.OraclePrice = 85000.0 // oracle lagging above live → delta negative → naively says DOWN

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil: live(%.0f) > window_open(%.0f) contradicts DOWN lag signal, got %+v",
			ctx.LivePrice, ctx.WindowOpen, sig)
	}
}

func TestOracleLag_Signal_DirectionAligned_Up(t *testing.T) {
	// Lag says UP and BTC is genuinely above window open — should fire.
	ctx := baseOracleLagCtx()
	ctx.WindowOpen = 84000.0  // opened low
	ctx.LivePrice = 85000.0   // BTC is UP from window open ✓
	ctx.OraclePrice = 84700.0 // oracle lagging → delta positive → UP

	sig := (&OracleLag{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal: direction aligns (live > window_open, lag says UP)")
	}
	if sig.Direction != DirectionUp {
		t.Errorf("Direction = %v, want Up", sig.Direction)
	}
}
