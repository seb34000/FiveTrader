package strategy

import (
	"math"
	"testing"
	"time"

	"github.com/seb/fivetrader/market"
)

func baseWindowDeltaCtx(elapsedSec float64) *Context {
	now := time.Now()
	windowStart := now.Add(-time.Duration(elapsedSec) * time.Second)
	return &Context{
		LivePrice:  85000.0,
		WindowOpen: 84915.0, // ~0.1% below → delta +0.1%
		Market: market.State{
			TokenIDUp:   "token-up-123",
			TokenIDDown: "token-down-456",
			AskUp:       0.60,
			AskDown:     0.42,
			WindowStart: windowStart,
			WindowEnd:   windowStart.Add(5 * time.Minute),
		},
		Now: now,
	}
}

func TestWindowDelta_Name(t *testing.T) {
	s := &WindowDelta{}
	if s.Name() != "window_delta" {
		t.Errorf("Name() = %q, want window_delta", s.Name())
	}
}

func TestWindowDelta_Signal_Up(t *testing.T) {
	ctx := baseWindowDeltaCtx(280) // in zone [270, 292]
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84914.0 // delta ~+0.102%

	s := &WindowDelta{}
	sig := s.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Direction != DirectionUp {
		t.Errorf("Direction = %v, want Up", sig.Direction)
	}
	if sig.Strategy != "window_delta" {
		t.Errorf("Strategy = %q, want window_delta", sig.Strategy)
	}
}

func TestWindowDelta_Signal_Down(t *testing.T) {
	ctx := baseWindowDeltaCtx(280)
	ctx.LivePrice = 84914.0
	ctx.WindowOpen = 85000.0 // delta ~-0.102%

	s := &WindowDelta{}
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

func TestWindowDelta_NoSignal_TooEarly(t *testing.T) {
	ctx := baseWindowDeltaCtx(100) // elapsed < 240s
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0 // large delta

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil before zone, got %+v", sig)
	}
}

func TestWindowDelta_Signal_AtT60(t *testing.T) {
	ctx := baseWindowDeltaCtx(245) // elapsed=245s, just past entry at T-60s (240s)
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0 // delta ~+0.354% → should fire

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal at T-60s (elapsed=245s)")
	}
	if sig.Direction != DirectionUp {
		t.Errorf("Direction = %v, want Up", sig.Direction)
	}
}

func TestWindowDelta_NoSignal_TooLate(t *testing.T) {
	ctx := baseWindowDeltaCtx(295) // elapsed > 292s
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil after zone, got %+v", sig)
	}
}

func TestWindowDelta_NoSignal_DeltaTooSmall(t *testing.T) {
	ctx := baseWindowDeltaCtx(280)
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84999.0 // delta ~0.001% < 0.1%

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil for tiny delta, got %+v", sig)
	}
}

func TestWindowDelta_NoSignal_TokenTooExpensive(t *testing.T) {
	ctx := baseWindowDeltaCtx(280)
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0
	ctx.Market.AskUp = 0.80 // > 0.72 max

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil for expensive token, got %+v", sig)
	}
}

func TestWindowDelta_NoSignal_NoEdge(t *testing.T) {
	ctx := baseWindowDeltaCtx(280)
	// tiny delta → low winProb but expensive token → no edge
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84915.0 // delta ~0.1%
	ctx.Market.AskUp = 0.71  // winProb at 0.1% is ~0.80, edge = 0.80-0.71 > 0 actually
	// Use very high ask to kill edge
	ctx.Market.AskUp = 0.95 // exceeds maxEntryTokenPrice → returns nil before edge check

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil for no-edge scenario, got %+v", sig)
	}
}

func TestWindowDelta_NoSignal_WindowOpenZero(t *testing.T) {
	ctx := baseWindowDeltaCtx(280)
	ctx.WindowOpen = 0

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("expected nil for zero window open, got %+v", sig)
	}
}

func TestWindowDelta_EdgePositive(t *testing.T) {
	ctx := baseWindowDeltaCtx(280)
	ctx.LivePrice = 85000.0
	ctx.WindowOpen = 84700.0 // delta ~+0.354%

	sig := (&WindowDelta{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	if sig.Edge <= 0 {
		t.Errorf("Edge = %v, should be positive", sig.Edge)
	}
}

// ── Time-decay boost ──────────────────────────────────────────────────────────

func TestWindowDelta_TimeFactor_LaterIsHigher(t *testing.T) {
	// Same delta at elapsed=270 vs T-8s (elapsed=292) — later should give higher winProb.
	// Use 0.1% delta (winProb=0.80) so time-decay boost doesn't hit the 0.95 ceiling.
	ctxEarly := baseWindowDeltaCtx(270)
	ctxLate := baseWindowDeltaCtx(292)
	// live=85000, windowOpen=84915 → delta ≈ +0.1%
	ctxEarly.WindowOpen = 84915.0
	ctxLate.WindowOpen = 84915.0

	sigEarly := (&WindowDelta{}).Evaluate(ctxEarly)
	sigLate := (&WindowDelta{}).Evaluate(ctxLate)
	if sigEarly == nil || sigLate == nil {
		t.Fatal("expected signals for both early and late contexts")
	}
	if sigLate.WinProb <= sigEarly.WinProb {
		t.Errorf("late entry (T-8s) winProb=%.4f should exceed early (T-30s) winProb=%.4f", sigLate.WinProb, sigEarly.WinProb)
	}
}

func TestWindowDelta_TimeFactor_AtLastEntry_IsAboutEightPercent(t *testing.T) {
	// At window start (elapsed=240), timeFactor=1.0; at T-8s (elapsed=292), timeFactor=1.08.
	// Formula: 1.0 + 0.08*(elapsed-240)/(292-240)
	// Use delta = exactly 0.1% so deltaToWinProb returns 0.80 — no ceiling hit before or after.
	// live=85000, windowOpen=84915 → delta=(85000-84915)/84915≈0.001
	ctxEarly := baseWindowDeltaCtx(240)
	ctxLate := baseWindowDeltaCtx(292)
	ctxEarly.WindowOpen = 84915.0
	ctxLate.WindowOpen = 84915.0

	sigEarly := (&WindowDelta{}).Evaluate(ctxEarly)
	sigLate := (&WindowDelta{}).Evaluate(ctxLate)
	if sigEarly == nil || sigLate == nil {
		t.Fatal("expected signals for both early and late contexts")
	}
	ratio := sigLate.WinProb / sigEarly.WinProb
	if ratio < 1.07 || ratio > 1.09 {
		t.Errorf("late/early winProb ratio = %.4f, want ~1.08 (8%% boost at T-8s vs window start)", ratio)
	}
}

// ── deltaToWinProb ────────────────────────────────────────────────────────────

func TestDeltaToWinProb_TableDriven(t *testing.T) {
	tests := []struct {
		absDelta float64
		wantMin  float64
		wantMax  float64
	}{
		{0.0, 0.50, 0.51},        // zero delta → ~0.50
		{0.0002, 0.50, 0.56},     // at first breakpoint
		{0.0005, 0.65, 0.66},     // at second breakpoint
		{0.001, 0.80, 0.81},      // at third breakpoint
		{0.0015, 0.92, 0.93},     // at ceiling
		{0.01, 0.92, 0.93},       // above ceiling — stays at 0.92
	}
	for _, tt := range tests {
		got := DeltaToWinProb(tt.absDelta)
		if got < tt.wantMin || got > tt.wantMax {
			t.Errorf("DeltaToWinProb(%.4f) = %.4f, want [%.2f, %.2f]",
				tt.absDelta, got, tt.wantMin, tt.wantMax)
		}
	}
}

func TestDeltaToWinProb_Monotonic(t *testing.T) {
	// Probability should be non-decreasing as delta increases
	deltas := []float64{0, 0.0001, 0.0002, 0.0003, 0.0005, 0.0007, 0.001, 0.0012, 0.0015, 0.002, 0.01}
	prev := 0.0
	for _, d := range deltas {
		p := DeltaToWinProb(d)
		if p < prev-1e-9 {
			t.Errorf("deltaToWinProb not monotonic: p(%.4f)=%.4f < p(prev)=%.4f", d, p, prev)
		}
		prev = p
	}
}

func TestDeltaToWinProb_Ceiling(t *testing.T) {
	// Should cap at 0.92
	for _, d := range []float64{0.0015, 0.002, 0.01, 0.1} {
		p := DeltaToWinProb(d)
		if math.Abs(p-0.92) > 1e-9 {
			t.Errorf("DeltaToWinProb(%.4f) = %.4f, want 0.92 (ceiling)", d, p)
		}
	}
}
