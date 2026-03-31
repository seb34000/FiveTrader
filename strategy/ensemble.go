package strategy

import "math"

// Ensemble combines signals from multiple strategies.
// Priority: DumpHedge (risk-free) > OracleLag (high edge) > WindowDelta.
type Ensemble struct {
	oracleLag         *OracleLag
	windowDelta       *WindowDelta
	dumpHedge         *DumpHedge
	enableOracleLag   bool
	enableWindowDelta bool
	enableDumpHedge   bool
}

// NewEnsemble creates an Ensemble with the specified strategies enabled.
func NewEnsemble(enableOracleLag, enableWindowDelta, enableDumpHedge bool) *Ensemble {
	return &Ensemble{
		oracleLag:         &OracleLag{},
		windowDelta:       &WindowDelta{},
		dumpHedge:         &DumpHedge{},
		enableOracleLag:   enableOracleLag,
		enableWindowDelta: enableWindowDelta,
		enableDumpHedge:   enableDumpHedge,
	}
}

// Evaluate returns the highest-priority actionable signal, or nil.
// For directional signals, a concordance bonus or discount is applied based on
// whether the other directional strategy agrees.
func (e *Ensemble) Evaluate(ctx *Context) *Signal {
	var sig *Signal
	if e.enableDumpHedge {
		sig = e.dumpHedge.Evaluate(ctx)
	}

	// Evaluate directional strategies and cache results to avoid re-evaluation
	// in concordanceAdjust (oracle_lag is evaluated first in priority order).
	var oracleSig *Signal
	if sig == nil && e.enableOracleLag {
		oracleSig = e.oracleLag.Evaluate(ctx)
		sig = oracleSig
	}
	if sig == nil && e.enableWindowDelta {
		sig = e.windowDelta.Evaluate(ctx)
	}

	if sig != nil {
		sig.NegRisk = ctx.Market.NegRisk
		if sig.Strategy != NameDumpHedge {
			sig.Confidence = e.concordanceAdjust(ctx, sig, oracleSig)
		}
	}
	return sig
}

// concordanceAdjust boosts confidence by 15% when a secondary strategy agrees
// on direction, or reduces it by 15% when they disagree.
// cachedOracleSig is the already-computed oracle_lag result (may be nil).
func (e *Ensemble) concordanceAdjust(ctx *Context, primary *Signal, cachedOracleSig *Signal) float64 {
	var secondary *Signal
	switch primary.Strategy {
	case NameOracleLag:
		if e.enableWindowDelta {
			secondary = e.windowDelta.Evaluate(ctx)
		}
	case NameWindowDelta:
		// oracle_lag was already evaluated in the priority chain; reuse the result.
		// If it was nil then, it's nil now — no need to re-evaluate.
		secondary = cachedOracleSig
	}
	if secondary == nil {
		return primary.Confidence
	}
	if primary.Direction == secondary.Direction {
		return math.Min(primary.Confidence*1.15, 1.0)
	}
	return primary.Confidence * 0.85
}

// EvaluateAll returns signals from all strategies (for logging/analysis).
func (e *Ensemble) EvaluateAll(ctx *Context) []*Signal {
	var signals []*Signal
	for _, s := range []Strategy{e.dumpHedge, e.oracleLag, e.windowDelta} {
		if sig := s.Evaluate(ctx); sig != nil {
			signals = append(signals, sig)
		}
	}
	return signals
}
