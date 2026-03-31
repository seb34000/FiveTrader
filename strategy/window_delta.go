package strategy

import (
	"fmt"
	"math"
)

const (
	NameWindowDelta = "window_delta"

	MinWindowDelta       = 0.001 // 0.1% minimum window delta to act
	MaxEntryTokenPrice   = 0.78  // don't enter if token already > 0.78
	WindowDeltaEntryT    = 240.0 // earliest entry at T-60s (240s elapsed)
	WindowDeltaLastEntry = 292.0 // latest entry at T-8s
)

// WindowDelta implements Strategy 2: Window Delta T-60s.
//
// At T-60s before close, the current delta vs window open predicts
// the final outcome with 55-62% accuracy. Only enter when the token
// is still underpriced (<0.78) relative to the delta signal.
type WindowDelta struct{}

// Name returns the strategy identifier.
func (s *WindowDelta) Name() string { return NameWindowDelta }

// DiagnoseNil returns a short string explaining why Evaluate would return nil,
// or "ok" if a signal would be produced.
func (s *WindowDelta) DiagnoseNil(ctx *Context) string {
	elapsed := ctx.SecondsElapsed()
	if elapsed < WindowDeltaEntryT {
		return fmt.Sprintf("too_early:%.0fs<%.0fs", elapsed, WindowDeltaEntryT)
	}
	if elapsed > WindowDeltaLastEntry {
		return fmt.Sprintf("too_late:%.0fs>%.0fs", elapsed, WindowDeltaLastEntry)
	}
	if ctx.WindowOpen <= 0 || ctx.LivePrice <= 0 {
		return "missing_price"
	}
	delta := (ctx.LivePrice - ctx.WindowOpen) / ctx.WindowOpen
	if math.Abs(delta) < MinWindowDelta {
		return fmt.Sprintf("delta_too_small:%.3f%%", math.Abs(delta)*100)
	}
	var askPrice float64
	if delta > 0 {
		askPrice = ctx.Market.AskUp
	} else {
		askPrice = ctx.Market.AskDown
	}
	if askPrice <= 0 || ctx.Market.TokenIDUp == "" {
		return "no_market_data"
	}
	if askPrice > MaxEntryTokenPrice {
		return fmt.Sprintf("ask_too_high:%.3f>%.3f", askPrice, MaxEntryTokenPrice)
	}
	winProb := DeltaToWinProb(math.Abs(delta))
	timeFactor := 1.0 + 0.08*(elapsed-WindowDeltaEntryT)/(WindowDeltaLastEntry-WindowDeltaEntryT)
	winProb = math.Min(winProb*timeFactor, 0.95)
	edge := winProb - askPrice
	if edge <= 0 {
		return fmt.Sprintf("no_edge:winProb=%.3f ask=%.3f", winProb, askPrice)
	}
	return "ok"
}

// Evaluate returns a signal when the window delta is significant and the token is underpriced, or nil.
func (s *WindowDelta) Evaluate(ctx *Context) *Signal {
	elapsed := ctx.SecondsElapsed()

	// Only evaluate in the T-60s to T-8s window
	if elapsed < WindowDeltaEntryT || elapsed > WindowDeltaLastEntry {
		return nil
	}
	if ctx.WindowOpen <= 0 || ctx.LivePrice <= 0 {
		return nil
	}

	delta := (ctx.LivePrice - ctx.WindowOpen) / ctx.WindowOpen

	if math.Abs(delta) < MinWindowDelta {
		return nil // too close to call
	}

	var dir Direction
	var tokenID string
	var askPrice float64

	if delta > 0 {
		dir = DirectionUp
		tokenID = ctx.Market.TokenIDUp
		askPrice = ctx.Market.AskUp
	} else {
		dir = DirectionDown
		tokenID = ctx.Market.TokenIDDown
		askPrice = ctx.Market.AskDown
	}

	if tokenID == "" || askPrice <= 0 {
		return nil
	}
	if askPrice > MaxEntryTokenPrice {
		return nil // already priced in
	}

	// Win probability from delta magnitude (empirical curve from CLAUDE.md)
	absDelta := math.Abs(delta)
	winProb := DeltaToWinProb(absDelta)

	// Time-decay boost: entries closer to T-8s are more predictive (less reversal time).
	// Factor goes from 1.0 at T-30s (270s elapsed) to ~1.08 at T-8s (292s elapsed).
	timeFactor := 1.0 + 0.08*(elapsed-WindowDeltaEntryT)/(WindowDeltaLastEntry-WindowDeltaEntryT)
	winProb = math.Min(winProb*timeFactor, 0.95)

	edge := winProb - askPrice
	if edge <= 0 {
		return nil
	}

	confidence := math.Min(absDelta/0.0015, 1.0) * 0.7

	return &Signal{
		Strategy:    s.Name(),
		Direction:   dir,
		TokenID:     tokenID,
		AskPrice:    askPrice,
		WinProb:     winProb,
		Edge:        edge,
		Confidence:  confidence,
		GeneratedAt: ctx.Now,
	}
}

// DeltaToWinProb maps delta magnitude to estimated win probability.
// Based on empirical curve from CLAUDE.md:
// 0.005% → 0.50, 0.02% → 0.55, 0.05% → 0.65, 0.10% → 0.80, 0.15%+ → 0.92
func DeltaToWinProb(absDelta float64) float64 {
	switch {
	case absDelta >= 0.0015:
		return 0.92
	case absDelta >= 0.001:
		return 0.80 + (absDelta-0.001)/(0.0015-0.001)*0.12
	case absDelta >= 0.0005:
		return 0.65 + (absDelta-0.0005)/(0.001-0.0005)*0.15
	case absDelta >= 0.0002:
		return 0.55 + (absDelta-0.0002)/(0.0005-0.0002)*0.10
	default:
		return 0.50 + absDelta/0.0002*0.05
	}
}
