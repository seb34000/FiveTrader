package strategy

import (
	"math"
	"testing"
	"time"

	"github.com/seb/fivetrader/market"
)

// baseEnsembleCtx returns a context that triggers NO strategy by default,
// so individual tests can set up only what they need.
func baseEnsembleCtx() *Context {
	now := time.Now()
	windowStart := now.Add(-100 * time.Second)
	return &Context{
		LivePrice:   85000.0,
		OraclePrice: 85000.0, // no oracle lag
		OracleAge:   10 * time.Second,
		WindowOpen:  85000.0, // no window delta
		Market: market.State{
			TokenIDUp:   "token-up-123",
			TokenIDDown: "token-down-456",
			AskUp:       0.50,
			AskDown:     0.50,
			NegRisk:     true,
			WindowStart: windowStart,
			WindowEnd:   windowStart.Add(5 * time.Minute),
		},
		Now: now,
	}
}

func TestEnsemble_AllNil(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	// sum = 1.0 → no dump hedge; no lag; no window delta (not in zone)
	if sig := e.Evaluate(ctx); sig != nil {
		t.Errorf("expected nil, got %+v", sig)
	}
}

func TestEnsemble_DumpHedge_Priority(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	// Trigger dump hedge: sum < 0.96
	ctx.Market.AskUp = 0.45
	ctx.Market.AskDown = 0.48

	sig := e.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected dump_hedge signal")
	}
	if sig.Strategy != "dump_hedge" {
		t.Errorf("Strategy = %q, want dump_hedge (highest priority)", sig.Strategy)
	}
}

func TestEnsemble_OracleLag_WhenDumpHedgeNil(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	// No dump hedge (sum=1.0), trigger oracle lag
	ctx.Market.AskUp = 0.50
	ctx.Market.AskDown = 0.50
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0 // delta +0.354%
	ctx.OracleAge = 10 * time.Second

	sig := e.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected oracle_lag signal")
	}
	if sig.Strategy != "oracle_lag" {
		t.Errorf("Strategy = %q, want oracle_lag", sig.Strategy)
	}
}

func TestEnsemble_WindowDelta_LastFallback(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	// Put in window delta zone (T-30s to T-8s elapsed = 270–292s)
	now := time.Now()
	ctx.Now = now
	ctx.Market.WindowStart = now.Add(-280 * time.Second)
	ctx.Market.WindowEnd = ctx.Market.WindowStart.Add(5 * time.Minute)
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0  // delta ~+0.354% > 0.1%
	ctx.Market.AskUp = 0.60   // < 0.72 max
	ctx.OraclePrice = 85000.0 // no oracle lag

	sig := e.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected window_delta signal")
	}
	if sig.Strategy != "window_delta" {
		t.Errorf("Strategy = %q, want window_delta", sig.Strategy)
	}
}

func TestEnsemble_DisabledDumpHedge(t *testing.T) {
	e := NewEnsemble(true, true, false) // dump hedge disabled
	ctx := baseEnsembleCtx()
	ctx.Market.AskUp = 0.45
	ctx.Market.AskDown = 0.48 // would trigger dump_hedge

	sig := e.Evaluate(ctx)
	// dump hedge disabled → should not fire dump_hedge
	if sig != nil && sig.Strategy == "dump_hedge" {
		t.Error("dump_hedge should be disabled")
	}
}

func TestEnsemble_DisabledOracleLag(t *testing.T) {
	e := NewEnsemble(false, true, true) // oracle lag disabled
	ctx := baseEnsembleCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.OracleAge = 10 * time.Second

	sig := e.Evaluate(ctx)
	if sig != nil && sig.Strategy == "oracle_lag" {
		t.Error("oracle_lag should be disabled")
	}
}

func TestEnsemble_DisabledWindowDelta(t *testing.T) {
	e := NewEnsemble(true, false, true) // window delta disabled
	ctx := baseEnsembleCtx()
	now := time.Now()
	ctx.Now = now
	ctx.Market.WindowStart = now.Add(-280 * time.Second)
	ctx.Market.WindowEnd = ctx.Market.WindowStart.Add(5 * time.Minute)
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0

	sig := e.Evaluate(ctx)
	if sig != nil && sig.Strategy == "window_delta" {
		t.Error("window_delta should be disabled")
	}
}

func TestEnsemble_NegRiskPropagated(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	ctx.Market.AskUp = 0.45
	ctx.Market.AskDown = 0.48
	ctx.Market.NegRisk = true

	sig := e.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	if !sig.NegRisk {
		t.Error("NegRisk should be propagated from market context")
	}
}

func TestEnsemble_NegRiskFalse_Propagated(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	ctx.Market.AskUp = 0.45
	ctx.Market.AskDown = 0.48
	ctx.Market.NegRisk = false

	sig := e.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	if sig.NegRisk {
		t.Error("NegRisk=false should be propagated")
	}
}

func TestEnsemble_EvaluateAll_ReturnsAllSignals(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	// Trigger dump hedge (easy, sum < 0.96)
	ctx.Market.AskUp = 0.45
	ctx.Market.AskDown = 0.48

	sigs := e.EvaluateAll(ctx)
	// At minimum dump_hedge should fire; oracle_lag/window_delta depend on context
	found := false
	for _, s := range sigs {
		if s.Strategy == "dump_hedge" {
			found = true
		}
	}
	if !found {
		t.Error("EvaluateAll should include dump_hedge signal")
	}
}

// ── Concordance bonus/discount ────────────────────────────────────────────────

func TestEnsemble_Concordance_Boost_WhenBothAgree(t *testing.T) {
	// oracle_lag fires UP; window_delta also fires UP → confidence boosted by 15%
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()

	// Trigger oracle_lag UP
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0 // delta +0.354%
	ctx.OracleAge = 10 * time.Second

	// Put in window_delta zone and matching UP delta
	now := time.Now()
	ctx.Now = now
	ctx.Market.WindowStart = now.Add(-280 * time.Second)
	ctx.Market.WindowEnd = ctx.Market.WindowStart.Add(5 * time.Minute)
	ctx.WindowOpen = 84700.0 // delta +0.354% UP
	ctx.Market.AskUp = 0.52  // affordable for both strategies

	sig := e.Evaluate(ctx)
	if sig == nil || sig.Strategy != "oracle_lag" {
		t.Fatalf("expected oracle_lag signal, got %v", sig)
	}

	// Compute what oracle_lag would give without concordance
	rawSig := (&OracleLag{}).Evaluate(ctx)
	if rawSig == nil {
		t.Fatal("raw oracle_lag signal is nil")
	}
	expected := math.Min(rawSig.Confidence*1.15, 1.0)
	if math.Abs(sig.Confidence-expected) > 0.001 {
		t.Errorf("concordance boost: Confidence = %.4f, want %.4f (raw=%.4f * 1.15)", sig.Confidence, expected, rawSig.Confidence)
	}
}

func TestEnsemble_OracleLag_DirectionMismatch_WindowDeltaTakesOver(t *testing.T) {
	// The direction alignment guard makes oracle_lag and window_delta always agree:
	// oracle_lag is filtered when its direction contradicts live vs window_open.
	// In this scenario: oracle_lag would say DOWN (live < oracle) but live > window_open,
	// so oracle_lag is filtered → window_delta fires UP instead.
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()

	now := time.Now()
	ctx.Now = now
	ctx.Market.WindowStart = now.Add(-280 * time.Second)
	ctx.Market.WindowEnd = ctx.Market.WindowStart.Add(5 * time.Minute)
	ctx.WindowOpen = 84200.0 // BTC opened low
	ctx.LivePrice = 84700.0   // BTC is UP from window open → window_delta says UP
	ctx.OraclePrice = 85100.0 // oracle above live → oracle_lag would say DOWN, but filtered (mismatch)
	ctx.OracleAge = 10 * time.Second
	ctx.Market.AskDown = 0.52
	ctx.Market.AskUp = 0.52

	// oracle_lag alone should be nil due to direction mismatch
	if oracleSig := (&OracleLag{}).Evaluate(ctx); oracleSig != nil {
		t.Fatalf("oracle_lag should be filtered by direction mismatch, got %+v", oracleSig)
	}

	// ensemble falls through to window_delta
	sig := e.Evaluate(ctx)
	if sig == nil || sig.Strategy != "window_delta" {
		t.Fatalf("expected window_delta signal after oracle_lag filtered, got %v", sig)
	}
	if sig.Direction != DirectionUp {
		t.Errorf("Direction = %v, want Up (live > window_open)", sig.Direction)
	}
}

func TestEnsemble_Concordance_NoChange_WhenNoSecondary(t *testing.T) {
	// oracle_lag fires, window_delta disabled → confidence unchanged
	e := NewEnsemble(true, false, true) // window_delta disabled
	ctx := baseEnsembleCtx()
	ctx.LivePrice = 85000.0
	ctx.OraclePrice = 84700.0
	ctx.OracleAge = 10 * time.Second

	sig := e.Evaluate(ctx)
	if sig == nil || sig.Strategy != "oracle_lag" {
		t.Fatalf("expected oracle_lag signal, got %v", sig)
	}
	rawSig := (&OracleLag{}).Evaluate(ctx)
	if rawSig == nil {
		t.Fatal("raw oracle_lag signal is nil")
	}
	if math.Abs(sig.Confidence-rawSig.Confidence) > 0.001 {
		t.Errorf("no secondary strategy → confidence should be unchanged: got %.4f, want %.4f", sig.Confidence, rawSig.Confidence)
	}
}

func TestEnsemble_EvaluateAll_Empty(t *testing.T) {
	e := NewEnsemble(true, true, true)
	ctx := baseEnsembleCtx()
	// No strategy fires
	sigs := e.EvaluateAll(ctx)
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals, got %d", len(sigs))
	}
}
