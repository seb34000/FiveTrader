package main

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/seb/fivetrader/monitor"
	"github.com/seb/fivetrader/risk"
	"github.com/seb/fivetrader/sim"
)

// runSettlementLoop auto-settles trades once their market window has expired.
func runSettlementLoop(ctx context.Context, rm *risk.Manager, simulator *sim.Simulator, log *zap.Logger) {
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
				rm.SettleExpiredTrades(log)
			}
		}
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
