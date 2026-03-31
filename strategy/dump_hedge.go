package strategy

import "fmt"

const (
	NameDumpHedge = "dump_hedge"
	MaxSumForArb  = 0.98 // buy both sides if sum < 0.98 = guaranteed ≥2%
	MinAskPerLeg  = 0.10 // below this, the orderbook is stale/empty
	MinSumSanity  = 0.50 // sum below 0.50 is impossible in a healthy binary market
)

// DumpHedge implements Strategy 3: Dump & Hedge Arbitrage.
//
// If askUp + askDown < 1.00, buying both sides is risk-free.
// We only act when the discount is significant enough (< 0.98).
// Returns a special "BOTH" signal; the executor handles both legs.
type DumpHedge struct{}

// Name returns the strategy identifier.
func (s *DumpHedge) Name() string { return NameDumpHedge }

// DiagnoseNil returns a short string explaining why Evaluate would return nil,
// or "ok" if a signal would be produced.
func (s *DumpHedge) DiagnoseNil(ctx *Context) string {
	askUp := ctx.Market.AskUp
	askDown := ctx.Market.AskDown
	if askUp <= 0 || askDown <= 0 {
		return "missing_ask"
	}
	if askUp < MinAskPerLeg || askDown < MinAskPerLeg {
		return fmt.Sprintf("ask_too_low:up=%.3f down=%.3f min=%.2f", askUp, askDown, MinAskPerLeg)
	}
	sum := askUp + askDown
	if sum < MinSumSanity {
		return fmt.Sprintf("sum_too_low:%.3f<%.2f", sum, MinSumSanity)
	}
	if sum >= MaxSumForArb {
		return fmt.Sprintf("no_arb:sum=%.4f>=%.2f", sum, MaxSumForArb)
	}
	return "ok"
}

// Evaluate returns a BOTH signal when askUp+askDown < MaxSumForArb, guaranteeing a profit, or nil.
func (s *DumpHedge) Evaluate(ctx *Context) *Signal {
	askUp := ctx.Market.AskUp
	askDown := ctx.Market.AskDown

	if askUp <= 0 || askDown <= 0 {
		return nil
	}
	// Reject individual asks that are unrealistically low — stale/empty orderbook.
	if askUp < MinAskPerLeg || askDown < MinAskPerLeg {
		return nil
	}

	sum := askUp + askDown
	// Reject sums that are impossible in a healthy binary market.
	if sum < MinSumSanity {
		return nil
	}
	if sum >= MaxSumForArb {
		return nil
	}

	edge := (1.0 - sum) / sum // guaranteed return as fraction of capital deployed

	return &Signal{
		Strategy:     s.Name(),
		Direction:    DirectionAbstain, // handled specially by executor
		TokenID:      ctx.Market.TokenIDUp,
		TokenIDDown:  ctx.Market.TokenIDDown,
		AskPrice:     sum,     // total cost per token pair; executor derives per-leg from AskPriceDown
		AskPriceDown: askDown,
		WinProb:      1.0, // guaranteed win
		Edge:         edge,
		Confidence:   1.0,
		GeneratedAt:  ctx.Now,
	}
}
