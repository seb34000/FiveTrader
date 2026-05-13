package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/seb/fivetrader/asset"
	"github.com/seb/fivetrader/execution"
	"github.com/seb/fivetrader/feed"
	"github.com/seb/fivetrader/market"
	"github.com/seb/fivetrader/monitor"
	"github.com/seb/fivetrader/oracle"
	"github.com/seb/fivetrader/risk"
	"github.com/seb/fivetrader/sim"
	"github.com/seb/fivetrader/strategy"
	"github.com/seb/fivetrader/ui"
)

// runAssetLoop processes price, oracle, and market events for one asset, evaluates
// strategies, submits orders, and reports state via onState callback.
// It runs for the lifetime of ctx and is the single writer of livePriceBits/oraclePriceBits.
func runAssetLoop(
	ctx context.Context,
	a asset.Asset,
	exec *execution.Executor,
	rm *risk.Manager,
	coord *risk.Coordinator,
	simulator *sim.Simulator,
	liveJournal *monitor.Journal,
	ensemble *strategy.Ensemble,
	livePriceBits *atomic.Uint64,
	oraclePriceBits *atomic.Uint64,
	prices <-chan feed.AggregatedPrice,
	oraclePrices <-chan oracle.Price,
	marketStates <-chan market.State,
	onState func(ui.AssetState),
	log *zap.Logger,
) {
	strategyCooldowns := map[string]time.Duration{
		"dump_hedge":   0,
		"oracle_lag":   20 * time.Second,
		"window_delta": 5 * time.Second,
	}

	var (
		livePrice     float64
		prevLivePrice float64
		oraclePrice   oracle.Price
		currentMarket market.State
		windowOpen    float64

		priceBinance  float64
		priceBitstamp float64
		priceCoinbase float64

		lastWindowStart      time.Time
		lastSignalByStrategy = map[string]time.Time{}
		lastSignalDesc       string
		lastDiagTime         time.Time
	)

	// oracleAgeSince returns the age of the oracle price, or 0 if no data has been
	// received yet. time.Since(time.Time{}) overflows int64 (~2025 years > 290yr max).
	oracleAgeSince := func() time.Duration {
		if oraclePrice.UpdatedAt.IsZero() {
			return 0
		}
		return time.Since(oraclePrice.UpdatedAt)
	}

	pushState := func() {
		if onState == nil {
			return
		}
		snap := rm.Snapshot()
		oracleDelta := 0.0
		if oraclePrice.Value > 0 {
			oracleDelta = (livePrice - oraclePrice.Value) / oraclePrice.Value * 100
		}
		onState(ui.AssetState{
			Symbol:        a.Symbol,
			Name:          a.Name,
			LivePrice:     livePrice,
			OraclePrice:   oraclePrice.Value,
			OracleDelta:   oracleDelta,
			OracleAge:     oracleAgeSince(),
			PriceBinance:  priceBinance,
			PriceBitstamp: priceBitstamp,
			PriceCoinbase: priceCoinbase,
			WindowStart:   currentMarket.WindowStart,
			WindowEnd:     currentMarket.WindowEnd,
			WindowOpen:    windowOpen,
			AskUp:         currentMarket.AskUp,
			AskDown:       currentMarket.AskDown,
			SettledTrades: snap.Trades,
			PnL:           snap.PnL,
			WinRate:       snap.WinRate,
			DailyLoss:     snap.DailyLoss,
			OpenTrades:    snap.OpenTrades,
			RecentTrades:  snap.RecentTrades,
			LastSignal:    lastSignalDesc,
		})
	}

	for {
		select {
		case <-ctx.Done():
			return

		case p := <-prices:
			// Always update individual feed prices for display (even with < minSources).
			if v, ok := p.BySource["binance"]; ok {
				priceBinance = v
			}
			if v, ok := p.BySource["bitstamp"]; ok {
				priceBitstamp = v
			}
			if v, ok := p.BySource["coinbase"]; ok {
				priceCoinbase = v
			}
			// Only update livePrice when aggregation has ≥ minSources (Value > 0).
			if p.Value > 0 {
				prevLivePrice = livePrice
				livePrice = p.Value
				livePriceBits.Store(math.Float64bits(livePrice))
				ws := market.WindowStart()
				if ws.After(lastWindowStart) {
					lastWindowStart = ws
					if prevLivePrice > 0 {
						windowOpen = prevLivePrice
					} else {
						windowOpen = livePrice
					}
					log.Debug("new window",
						zap.String("asset", a.Symbol),
						zap.Time("start", ws),
						zap.Float64("open_price", windowOpen))
				}
			}
			pushState()

		case op := <-oraclePrices:
			oraclePrice = op
			oraclePriceBits.Store(math.Float64bits(op.Value))
			pushState()

		case ms := <-marketStates:
			currentMarket = ms
			pushState()
		}

		if livePrice <= 0 || currentMarket.TokenIDUp == "" {
			continue
		}

		sctx := &strategy.Context{
			LivePrice:   livePrice,
			OraclePrice: oraclePrice.Value,
			OracleAge:   oracleAgeSince(),
			Market:      currentMarket,
			WindowOpen:  windowOpen,
			Now:         time.Now(),
		}

		sig := ensemble.Evaluate(sctx)
		if sig == nil {
			if time.Since(lastDiagTime) >= 30*time.Second {
				lastDiagTime = time.Now()
				oracleAge := oracleAgeSince().Truncate(time.Second)
				oracleDelta := 0.0
				if oraclePrice.Value > 0 && livePrice > 0 {
					oracleDelta = (livePrice - oraclePrice.Value) / oraclePrice.Value * 100
				}
				log.Debug("no signal",
					zap.String("asset", a.Symbol),
					zap.Float64("oracle_delta_pct", oracleDelta),
					zap.Duration("oracle_age", oracleAge),
					zap.Bool("oracle_ok", oraclePrice.Value > 0),
					zap.Float64("window_elapsed_s", time.Since(currentMarket.WindowStart).Seconds()),
					zap.Float64("ask_up", currentMarket.AskUp),
					zap.Float64("ask_down", currentMarket.AskDown),
					zap.String("diag_oracle_lag", (&strategy.OracleLag{}).DiagnoseNil(sctx)),
					zap.String("diag_window_delta", (&strategy.WindowDelta{}).DiagnoseNil(sctx)),
					zap.String("diag_dump_hedge", (&strategy.DumpHedge{}).DiagnoseNil(sctx)),
				)
			}
			continue
		}

		if cd := strategyCooldowns[sig.Strategy]; cd > 0 {
			if time.Since(lastSignalByStrategy[sig.Strategy]) < cd {
				continue
			}
		}
		monitor.LogSignal(log, sig)
		lastSignalDesc = fmt.Sprintf("%s %s edge=%.3f", sig.Strategy, sig.Direction.String(), sig.Edge)
		pushState()

		result := rm.Approve(sig.Strategy, sig.AskPrice, sig.WinProb, sig.Edge, sig.Confidence, sig.FeeRateBps)
		if !result.Approved {
			log.Info("trade skipped",
				zap.String("asset", a.Symbol),
				zap.String("reason", result.Reason),
				zap.String("strategy", sig.Strategy),
				zap.Float64("ask_price", sig.AskPrice),
			)
			continue
		}

		// Cross-asset / per-window guard: max 1 trade per asset per window,
		// and correlated assets (ETH/XRP) cannot both trade in the same window.
		if coord != nil {
			if ok, reason := coord.ReserveWindow(a.Symbol, currentMarket.WindowEnd); !ok {
				log.Info("trade skipped",
					zap.String("asset", a.Symbol),
					zap.String("reason", reason),
					zap.String("strategy", sig.Strategy),
					zap.Float64("ask_price", sig.AskPrice),
				)
				continue
			}
		}

		if !rm.ReserveBet() {
			if coord != nil {
				coord.ReleaseWindow(a.Symbol, currentMarket.WindowEnd)
			}
			log.Info("trade skipped",
				zap.String("asset", a.Symbol),
				zap.String("reason", "max_concurrent_bets_reserved"),
				zap.String("strategy", sig.Strategy),
			)
			continue
		}
		// Set cooldown only after the trade is actually approved and reserved.
		// Previously set before Approve(), which burned the cooldown even on rejections.
		lastSignalByStrategy[sig.Strategy] = time.Now()
		log.Info("executing",
			zap.String("asset", a.Symbol),
			zap.String("strategy", sig.Strategy),
			zap.Float64("usdc", result.USDCSize))

		go func(s *strategy.Signal, size float64, winOpen float64, mkt market.State, oraclePx float64) {
			orderID, err := exec.Execute(ctx, s, size)
			if err != nil {
				rm.UnreserveBet()
				if coord != nil {
					coord.ReleaseWindow(a.Symbol, mkt.WindowEnd)
				}
				if errors.Is(err, execution.ErrOrderKilled) {
					log.Info("FOK order killed — no liquidity at this price, skipping",
						zap.String("asset", a.Symbol),
						zap.String("strategy", s.Strategy),
						zap.Float64("ask", s.AskPrice),
					)
				} else if errors.Is(err, execution.ErrPriceMovedAgainstUs) {
					log.Info("price moved past strategy cap during latency — skipping",
						zap.String("asset", a.Symbol),
						zap.String("strategy", s.Strategy),
						zap.Float64("signal_ask", s.AskPrice),
					)
				} else {
					log.Error("execution failed", zap.String("asset", a.Symbol), zap.Error(err))
				}
				return
			}
			trade := &risk.Trade{
				ID:          orderID,
				Strategy:    s.Strategy,
				Direction:   s.Direction.String(),
				TokenID:     s.TokenID,
				ConditionID: mkt.ConditionID,
				USDCStaked:  size,
				TokenPrice:  s.AskPrice,
				Timestamp:   time.Now(),
				WindowEnd:   mkt.WindowEnd,
			}
			rm.OpenTrade(trade)
			if liveJournal != nil {
				liveJournal.Record(monitor.TradeEntry{
					ID:         orderID,
					Strategy:   s.Strategy,
					Direction:  s.Direction.String(),
					TokenID:    s.TokenID,
					TokenPrice: s.AskPrice,
					USDCStaked: size,
					WindowEnd:  mkt.WindowEnd,
					EntryTime:  trade.Timestamp,
				})
			}
			if simulator != nil {
				simulator.RegisterTrade(orderID, trade, winOpen, s.AskPriceDown, oraclePx, s.WinProb, s.Edge, s.Confidence)
			}
		}(sig, result.USDCSize, windowOpen, currentMarket, oraclePrice.Value)
	}
}
