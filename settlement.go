package main

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/seb/fivetrader/market"
	"github.com/seb/fivetrader/monitor"
	"github.com/seb/fivetrader/risk"
	"github.com/seb/fivetrader/sim"
)

// runSettlementLoop auto-settles trades once their market window has expired.
func runSettlementLoop(ctx context.Context, rm *risk.Manager, clob *market.Client, simulator *sim.Simulator, log *zap.Logger) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if simulator != nil {
				simulator.SettleSimTrades(rm)
			} else {
				rm.SettleExpiredTrades(ctx, makePnLLookup(clob), log)
			}
		}
	}
}

// makePnLLookup creates a PnLLookup that queries Polymarket for actual trade outcomes.
// Returns nil if clob is nil (dry-run mode where no real trades are placed).
func makePnLLookup(clob *market.Client) risk.PnLLookup {
	if clob == nil {
		return nil
	}
	return func(ctx context.Context, conditionID, direction string, usdcStaked, tokenPrice float64) (float64, bool, error) {
		res, err := clob.GetResolution(ctx, conditionID)
		if err != nil {
			return 0, false, err
		}
		if !res.Resolved {
			return 0, false, nil
		}
		tokens := usdcStaked / tokenPrice
		if strings.EqualFold(res.Winner, direction) {
			// We won: each token pays out $1.00
			return tokens - usdcStaked, true, nil // = usdcStaked * (1/tokenPrice - 1)
		}
		// We lost: tokens expire at $0.00
		return -usdcStaked, true, nil
	}
}



// simStratStats converts sim.StratStats to monitor.SimStratStats.
func simStratStats(simulator *sim.Simulator) map[string]monitor.SimStratStats {
	raw := simulator.StrategyStats()
	out := make(map[string]monitor.SimStratStats, len(raw))
	for k, v := range raw {
		out[k] = monitor.SimStratStats{Count: v.Count, PnL: v.PnL, WinRate: v.WinRate}
	}
	return out
}
