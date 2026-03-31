package sim

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/seb/fivetrader/risk"
)

const stratDumpHedge = "dump_hedge"

// polymarketFeeRate is the fee Polymarket deducts from gross winnings at settlement (2%).
const polymarketFeeRate = 0.02

// SimTrade wraps a risk.Trade with extra sim-only fields.
type SimTrade struct {
	*risk.Trade
	WindowOpenPrice    float64
	OraclePriceAtEntry float64
	WinProb            float64
	Edge               float64
	Confidence         float64
	// For dump_hedge: store down-leg ask price separately so PnL is correct.
	IsDumpHedge  bool
	AskPriceDown float64
}

// StratStats holds per-strategy aggregated stats.
type StratStats struct {
	Count   int
	PnL     float64
	WinRate float64
}

// Simulator tracks open simulated trades, resolves them at window expiry,
// and writes settled trades to a JSONL journal.
type Simulator struct {
	mu              sync.Mutex
	trades          map[string]*SimTrade
	stratTotals     map[string]*stratAccum // protected by mu
	journal         *TradeJournal
	livePriceBits   *atomic.Uint64 // float64 stored as bits for lock-free reads
	oraclePriceBits *atomic.Uint64 // Chainlink oracle price bits; 0 until first poll
	log             *zap.Logger
}

type stratAccum struct {
	count int
	pnl   float64
	wins  int
}

// NewSimulator creates a Simulator backed by the given journal file path.
// livePriceBits and oraclePriceBits are *atomic.Uint64 whose values are
// math.Float64bits(price), updated by the event loop on every tick/poll.
// Settlement uses the oracle price (Chainlink) when available, falling back to live.
func NewSimulator(livePriceBits, oraclePriceBits *atomic.Uint64, journalPath string, log *zap.Logger) (*Simulator, error) {
	j, err := newTradeJournal(journalPath)
	if err != nil {
		return nil, fmt.Errorf("sim journal: %w", err)
	}
	return &Simulator{
		trades:          make(map[string]*SimTrade),
		stratTotals:     make(map[string]*stratAccum),
		journal:         j,
		livePriceBits:   livePriceBits,
		oraclePriceBits: oraclePriceBits,
		log:             log,
	}, nil
}

// RegisterTrade adds a new simulated open trade.
// windowOpenPrice is the BTC price at the start of the current 5-minute window.
// askDown is used for dump_hedge only (both-leg trades); pass 0 for single-leg trades.
// oraclePriceAtEntry is the Chainlink oracle price at time of entry (for journal analytics).
func (s *Simulator) RegisterTrade(id string, t *risk.Trade, windowOpenPrice, askDown, oraclePriceAtEntry float64, winProb, edge, confidence float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trades[id] = &SimTrade{
		Trade:              t,
		WindowOpenPrice:    windowOpenPrice,
		OraclePriceAtEntry: oraclePriceAtEntry,
		WinProb:            winProb,
		Edge:               edge,
		Confidence:         confidence,
		IsDumpHedge:        t.Strategy == stratDumpHedge,
		AskPriceDown:       askDown,
	}
}

// SettleSimTrades resolves any trade whose WindowEnd + 30s has passed.
// Should be called every ~15s (same cadence as runSettlementLoop).
// Settlement uses the Chainlink oracle price (matching Polymarket's actual resolution)
// and falls back to the live exchange price if the oracle is not yet available.
func (s *Simulator) SettleSimTrades(rm *risk.Manager) {
	now := time.Now()
	livePrice := math.Float64frombits(s.livePriceBits.Load())
	if livePrice <= 0 {
		return // price not yet available, defer
	}
	oraclePrice := math.Float64frombits(s.oraclePriceBits.Load())
	// Use oracle price for settlement (matches Polymarket's Chainlink-based resolution).
	// Fall back to live price if oracle not yet polled.
	settlePrice := oraclePrice
	if settlePrice <= 0 {
		settlePrice = livePrice
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, st := range s.trades {
		if st.WindowEnd.IsZero() || now.Before(st.WindowEnd.Add(30*time.Second)) {
			continue
		}

		btcWentUp := settlePrice > st.WindowOpenPrice
		pnl := s.computePnL(st, btcWentUp)
		won := pnl > 0

		rm.SettleTrade(id, pnl)

		s.journal.record(TradeRecord{
			ID:                  id,
			Strategy:            st.Strategy,
			Direction:           st.Direction,
			TokenPrice:          st.TokenPrice,
			USDCStaked:          st.USDCStaked,
			WindowOpenPrice:     st.WindowOpenPrice,
			OraclePriceAtEntry:  st.OraclePriceAtEntry,
			SettlePrice:         settlePrice,
			OraclePriceAtSettle: oraclePrice,
			Won:                 won,
			PnL:                 pnl,
			WinProb:             st.WinProb,
			Edge:                st.Edge,
			Confidence:          st.Confidence,
			EntryTime:           st.Timestamp,
			SettledAt:           now,
		})

		acc := s.stratTotals[st.Strategy]
		if acc == nil {
			acc = &stratAccum{}
			s.stratTotals[st.Strategy] = acc
		}
		acc.count++
		acc.pnl += pnl
		if won {
			acc.wins++
		}

		s.log.Info("[SIM] trade settled",
			zap.String("id", id),
			zap.String("strategy", st.Strategy),
			zap.String("direction", st.Direction),
			zap.Float64("window_open_btc", st.WindowOpenPrice),
			zap.Float64("settle_price", settlePrice),
			zap.Float64("oracle_settle", oraclePrice),
			zap.Bool("won", won),
			zap.Float64("pnl", pnl),
		)

		delete(s.trades, id)
	}
}

// computePnL calculates the simulated P&L for a settled trade.
// Polymarket deducts a 2% fee from gross winnings at settlement.
func (s *Simulator) computePnL(st *SimTrade, btcWentUp bool) float64 {
	if st.IsDumpHedge {
		// TokenPrice = sum; gross profit = USDCStaked * (1/sum - 1), fee applied to profit.
		grossProfit := st.USDCStaked * (1.0/st.TokenPrice - 1.0)
		return grossProfit * (1.0 - polymarketFeeRate)
	}

	// Single-leg trade
	won := (st.Direction == "UP" && btcWentUp) || (st.Direction == "DOWN" && !btcWentUp)
	if won {
		// Gross profit = USDCStaked * (1/tokenPrice - 1); fee deducted from profit only.
		grossProfit := st.USDCStaked * (1.0/st.TokenPrice - 1.0)
		return grossProfit * (1.0 - polymarketFeeRate)
	}
	return -st.USDCStaked
}

// StrategyStats returns a copy of per-strategy aggregated stats.
func (s *Simulator) StrategyStats() map[string]StratStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]StratStats, len(s.stratTotals))
	for name, acc := range s.stratTotals {
		wr := 0.0
		if acc.count > 0 {
			wr = float64(acc.wins) / float64(acc.count)
		}
		out[name] = StratStats{Count: acc.count, PnL: acc.pnl, WinRate: wr}
	}
	return out
}

// JournalPath returns the file path of the trade journal.
func (s *Simulator) JournalPath() string {
	return s.journal.path
}

// Close flushes and closes the journal file.
func (s *Simulator) Close() error {
	return s.journal.close()
}
