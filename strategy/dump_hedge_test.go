package strategy

import (
	"math"
	"testing"
	"time"

	"github.com/seb/fivetrader/market"
)

func baseDumpHedgeCtx() *Context {
	now := time.Now()
	windowStart := now.Add(-100 * time.Second)
	return &Context{
		LivePrice:   85000.0,
		OraclePrice: 85000.0,
		Market: market.State{
			TokenIDUp:   "token-up-123",
			TokenIDDown: "token-down-456",
			AskUp:       0.45,
			AskDown:     0.48,
			WindowStart: windowStart,
			WindowEnd:   windowStart.Add(5 * time.Minute),
		},
		Now: now,
	}
}

func TestDumpHedge_Name(t *testing.T) {
	s := &DumpHedge{}
	if s.Name() != "dump_hedge" {
		t.Errorf("Name() = %q, want dump_hedge", s.Name())
	}
}

func TestDumpHedge_Signal_ArbitrageOpportunity(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	// sum = 0.45 + 0.48 = 0.93 < 0.98 → arbitrage

	s := &DumpHedge{}
	sig := s.Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal for sum < 0.98, got nil")
	}
	if sig.Strategy != "dump_hedge" {
		t.Errorf("Strategy = %q, want dump_hedge", sig.Strategy)
	}
	if sig.Direction != DirectionAbstain {
		t.Errorf("Direction = %v, want DirectionAbstain (BOTH sides)", sig.Direction)
	}
	if sig.TokenID != ctx.Market.TokenIDUp {
		t.Errorf("TokenID = %q, want TokenIDUp", sig.TokenID)
	}
	if sig.TokenIDDown != ctx.Market.TokenIDDown {
		t.Errorf("TokenIDDown = %q, want TokenIDDown", sig.TokenIDDown)
	}
	if sig.WinProb != 1.0 {
		t.Errorf("WinProb = %v, want 1.0 (guaranteed)", sig.WinProb)
	}
	// AskPrice must be the sum (total cost per token pair), not just askUp.
	// The executor derives askUp = AskPrice - AskPriceDown for per-leg sizing.
	wantSum := ctx.Market.AskUp + ctx.Market.AskDown
	if math.Abs(sig.AskPrice-wantSum) > 1e-9 {
		t.Errorf("AskPrice = %v, want sum %.4f (askUp+askDown)", sig.AskPrice, wantSum)
	}
}

func TestDumpHedge_NoSignal_SumAtThreshold(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskUp = 0.50
	ctx.Market.AskDown = 0.48 // sum = 0.98 = threshold (not strictly less)

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("sum == 0.98 threshold should return nil, got %+v", sig)
	}
}

func TestDumpHedge_NoSignal_SumAboveThreshold(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskUp = 0.50
	ctx.Market.AskDown = 0.50 // sum = 1.00

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("sum > 0.98 should return nil, got %+v", sig)
	}
}

func TestDumpHedge_NoSignal_AskTooLow(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskUp = 0.05 // < MinAskPerLeg=0.10 → stale/empty orderbook

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("askUp=0.05 < MinAskPerLeg should return nil (stale orderbook), got %+v", sig)
	}
}

func TestDumpHedge_NoSignal_SumTooLow(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskUp = 0.20
	ctx.Market.AskDown = 0.20 // sum = 0.40 < MinSumSanity=0.50 → impossible in healthy market

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("sum=0.40 < MinSumSanity=0.50 should return nil (bad data), got %+v", sig)
	}
}

func TestDumpHedge_NoSignal_AskUpZero(t *testing.T) {
	// This is the +9900% bug scenario — askUp = 0 (empty orderbook)
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskUp = 0

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("askUp=0 should return nil (empty orderbook), got %+v", sig)
	}
}

func TestDumpHedge_NoSignal_AskDownZero(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskDown = 0

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig != nil {
		t.Errorf("askDown=0 should return nil (empty orderbook), got %+v", sig)
	}
}

func TestDumpHedge_EdgeCalculation(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskUp = 0.45
	ctx.Market.AskDown = 0.48
	sum := 0.45 + 0.48 // = 0.93

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	wantEdge := (1.0 - sum) / sum
	if sig.Edge < wantEdge-1e-9 || sig.Edge > wantEdge+1e-9 {
		t.Errorf("Edge = %v, want %v", sig.Edge, wantEdge)
	}
}

func TestDumpHedge_Confidence_AlwaysOne(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	if sig.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", sig.Confidence)
	}
}

func TestDumpHedge_AskPriceDown_Set(t *testing.T) {
	ctx := baseDumpHedgeCtx()
	ctx.Market.AskDown = 0.48

	sig := (&DumpHedge{}).Evaluate(ctx)
	if sig == nil {
		t.Fatal("expected signal")
	}
	if sig.AskPriceDown != 0.48 {
		t.Errorf("AskPriceDown = %v, want 0.48", sig.AskPriceDown)
	}
}
