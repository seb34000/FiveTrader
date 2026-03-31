package ui

import (
	"time"

	"github.com/seb/fivetrader/risk"
)

// AssetState carries the latest state for a single asset's event loop.
type AssetState struct {
	Symbol string
	Name   string

	// Prices
	LivePrice   float64
	OraclePrice float64
	OracleDelta float64 // (live-oracle)/oracle * 100
	OracleAge   time.Duration

	// Feeds
	PriceBinance  float64
	PriceBitstamp float64
	PriceCoinbase float64

	// Current window
	WindowStart time.Time
	WindowEnd   time.Time
	WindowOpen  float64
	AskUp       float64
	AskDown     float64

	// Per-asset stats
	SettledTrades int
	PnL           float64
	WinRate       float64
	DailyLoss     float64

	// Open trades snapshot (value copies — safe to read without mutex)
	OpenTrades []risk.Trade

	// Settled trade history, newest first (capped at maxRecentTrades)
	RecentTrades []risk.Trade

	// Last signal description (plain text)
	LastSignal string
}

// Update carries the latest state of all assets to any UI renderer.
type Update struct {
	Mode    string
	Address string

	// Aggregated totals across all assets
	TotalPnL      float64
	TotalTrades   int
	TotalWinRate  float64
	TotalDailyLoss float64

	// Per-asset states, keyed by lower-case symbol ("btc", "eth", …)
	Assets map[string]AssetState
}
