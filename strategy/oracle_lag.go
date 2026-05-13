package strategy

import (
	"fmt"
	"math"
	"time"
)

const (
	NameOracleLag = "oracle_lag"

	MinLagThreshold        = 0.0010            // 0.10% minimum price deviation to act
	MinLagAge              = 2 * time.Second   // ignore if oracle just updated (lag already closing)
	MaxLagAge              = 120 * time.Second // ignore if oracle too stale (data unreliable)
	MaxTokenPriceOracleLag = 0.92             // oracle lag allows higher entry
)

// OracleLag implements Strategy 1: Oracle Latency Arbitrage.
//
// When live BTC price diverges from Chainlink oracle by >0.15%,
// the Polymarket market is mispriced. Bet in the direction of
// the live price before the oracle catches up.
type OracleLag struct{}

// oracleLagWinProb returns a conservative win probability estimate as a function
// of |delta|. Range: 0.62 (at threshold) → 0.78 (at ≥0.8% delta).
//
// Rationale: oracle-lag arbitrage has structural edge, but market participants
// partly pre-price the lag. 0.62-0.78 is defensible without backtested data;
// it keeps Kelly bet sizes safe even if the true win rate is 5-8 pp lower.
// Recalibrate against --sim journal once ≥200 oracle_lag trades are logged.
func oracleLagWinProb(absDelta float64) float64 {
	return 0.62 + math.Min(absDelta/0.008, 1.0)*0.16
}

// Name returns the strategy identifier.
func (s *OracleLag) Name() string { return NameOracleLag }

// DiagnoseNil returns a short string explaining why Evaluate would return nil
// given this context, or "ok" if a signal would be produced.
func (s *OracleLag) DiagnoseNil(ctx *Context) string {
	if ctx.OracleAge < MinLagAge {
		return fmt.Sprintf("oracle_too_fresh:%.1fs", ctx.OracleAge.Seconds())
	}
	if ctx.OracleAge > MaxLagAge {
		return fmt.Sprintf("oracle_too_stale:%.0fs", ctx.OracleAge.Seconds())
	}
	if ctx.OraclePrice <= 0 || ctx.LivePrice <= 0 {
		return "missing_price"
	}
	if ctx.SecondsRemaining() < 5 {
		return "near_expiry"
	}
	delta := (ctx.LivePrice - ctx.OraclePrice) / ctx.OraclePrice
	absDelta := math.Abs(delta)
	if absDelta < MinLagThreshold {
		return fmt.Sprintf("delta_too_small:%.3f%%", absDelta*100)
	}
	if ctx.WindowOpen > 0 {
		relDeviation := (ctx.LivePrice - ctx.WindowOpen) / ctx.WindowOpen
		const windowAlignTolerance = 0.0005
		if delta > 0 && relDeviation < -windowAlignTolerance {
			return fmt.Sprintf("direction_mismatch:live=%.0f<window_open=%.0f", ctx.LivePrice, ctx.WindowOpen)
		}
		if delta < 0 && relDeviation > windowAlignTolerance {
			return fmt.Sprintf("direction_mismatch:live=%.0f>window_open=%.0f", ctx.LivePrice, ctx.WindowOpen)
		}
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
	if askPrice > MaxTokenPriceOracleLag {
		return fmt.Sprintf("ask_too_high:%.3f>%.3f", askPrice, MaxTokenPriceOracleLag)
	}
	winProb := oracleLagWinProb(absDelta)
	edge := winProb - askPrice
	if edge <= 0 {
		return fmt.Sprintf("no_edge:winProb=%.3f ask=%.3f", winProb, askPrice)
	}
	return "ok"
}

// Evaluate returns a signal when a significant Chainlink oracle lag is detected, or nil.
func (s *OracleLag) Evaluate(ctx *Context) *Signal {
	// Skip if oracle just updated (lag is already closing — no longer exploitable)
	if ctx.OracleAge < MinLagAge {
		return nil
	}
	// Skip if oracle data is unreliably stale
	if ctx.OracleAge > MaxLagAge {
		return nil
	}
	// Skip if oracle or live price unavailable
	if ctx.OraclePrice <= 0 || ctx.LivePrice <= 0 {
		return nil
	}
	// Skip if too close to expiration (< 5s)
	if ctx.SecondsRemaining() < 5 {
		return nil
	}

	delta := (ctx.LivePrice - ctx.OraclePrice) / ctx.OraclePrice

	if math.Abs(delta) < MinLagThreshold {
		return nil
	}

	// Direction must broadly align with window_open, with a 0.05% tolerance band.
	// Without this guard, a lag inside a strong counter-trend triggers false signals.
	// The tolerance allows entry when live ≈ windowOpen (common early in the window).
	if ctx.WindowOpen > 0 {
		relDeviation := (ctx.LivePrice - ctx.WindowOpen) / ctx.WindowOpen
		const windowAlignTolerance = 0.0005 // 0.05%
		if delta > 0 && relDeviation < -windowAlignTolerance {
			return nil // lag says UP, but BTC is meaningfully below window open
		}
		if delta < 0 && relDeviation > windowAlignTolerance {
			return nil // lag says DOWN, but BTC is meaningfully above window open
		}
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
	if askPrice > MaxTokenPriceOracleLag {
		return nil // market already priced in the lag
	}

	absDelta := math.Abs(delta)
	winProb := oracleLagWinProb(absDelta)

	edge := winProb - askPrice
	if edge <= 0 {
		return nil // no edge after market price — skip rather than emit noise
	}
	confidence := math.Min(absDelta/MinLagThreshold, 1.0) * 0.9

	return &Signal{
		Strategy:    s.Name(),
		Direction:   dir,
		TokenID:     tokenID,
		AskPrice:    askPrice,
		WinProb:     winProb,
		Edge:        edge,
		Confidence:  confidence,
		NegRisk:     ctx.Market.NegRisk,
		FeeRateBps:  ctx.Market.FeeRateBps,
		GeneratedAt: ctx.Now,
	}
}
