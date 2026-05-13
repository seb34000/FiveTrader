package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Mode represents the bot operating mode.
type Mode int

const (
	ModeDryRun Mode = iota // paper trading, no real data (default)
	ModeSim                // live simulation: real feeds, simulated fills, real P&L tracking
	ModeLive               // real money trading
)

// TradeFilters holds entry-price, position-size, and drawdown guardrails.
// These values are loaded from environment variables and passed to the risk manager.
type TradeFilters struct {
	// Price band: skip trades outside [MinEntryPrice, MaxEntryPrice].
	// oracle_lag bypasses MaxEntryPrice; dump_hedge bypasses both.
	MinEntryPrice float64 // default 0.60
	MaxEntryPrice float64 // default 0.90

	// Position limits
	MaxPositionSize float64 // max tokens (shares) per trade; default no cap
	MaxLossPerTrade float64 // max USDC staked per trade; default no cap

	// Consecutive-loss pause: stop trading for PauseDuration after MaxConsecLosses losses in a row.
	MaxConsecLosses int           // default 3
	PauseDuration   time.Duration // default 30 min

	// Per-window gating: max 1 trade per asset per 5-min window (enforced by Coordinator).
	MaxTradesPerWindow int // default 1
}

type Config struct {
	// Wallet
	PrivateKey  string
	Address     string // derived from private key
	ProxyWallet string // optional: Polymarket proxy wallet (POLY_PROXY_WALLET)

	// Polymarket API credentials
	PolyAPIKey        string
	PolyAPISecret     string
	PolyAPIPassphrase string

	// Network
	PolygonRPC string

	// Operating mode
	Mode Mode

	// Risk (non-negotiable)
	MaxBetUSDC        float64
	MaxDailyLossUSDC  float64
	MaxConcurrentBets int
	KellyFraction     float64

	// Strategy toggles
	EnableOracleLag   bool
	EnableWindowDelta bool
	EnableDumpHedge   bool

	// EnableDumpHedgeLive allows dump_hedge in LIVE mode.
	// Default false: two-leg FOK execution leaves naked directional exposure
	// if the DOWN leg fails after the UP leg fills (cancel is a no-op on filled FOK).
	// Set ENABLE_DUMP_HEDGE_LIVE=true only when atomic two-leg submission is available.
	EnableDumpHedgeLive bool

	// SlippageTicks is the number of 0.01 ticks added to the ask price when placing FOK
	// orders to absorb orderbook staleness (~1-2s between poll and CLOB submission).
	// Default 1 (= +$0.01). Set 0 to disable, 2 for volatile markets.
	SlippageTicks int

	// Trade filters: entry-price band, position caps, consecutive-loss pause.
	Filters TradeFilters
}

// IsDryRun returns true for dry-run mode (backward compat helper).
func (c *Config) IsDryRun() bool { return c.Mode == ModeDryRun }

// IsSimOrDryRun returns true when no real orders are placed.
func (c *Config) IsSimOrDryRun() bool { return c.Mode != ModeLive }

// Load reads configuration from environment variables (and an optional .env file).
// Returns an error in live mode if required API credentials are missing.
func Load() (*Config, error) {
	// Load .env if present (ignore error if not found)
	_ = godotenv.Load()

	mode := ModeDryRun
	if !envBool("DRY_RUN", true) {
		mode = ModeLive
	}
	if envBool("SIM_MODE", false) {
		mode = ModeSim // SIM_MODE always overrides DRY_RUN=false
	}

	c := &Config{
		PrivateKey:        mustEnv("PRIVATE_KEY"),
		ProxyWallet:       os.Getenv("POLY_PROXY_WALLET"),
		PolyAPIKey:        os.Getenv("POLY_API_KEY"),
		PolyAPISecret:     os.Getenv("POLY_API_SECRET"),
		PolyAPIPassphrase: os.Getenv("POLY_API_PASSPHRASE"),
		PolygonRPC:        envOrDefault("POLYGON_RPC", "https://polygon-rpc.com"),
		Mode:              mode,
		MaxBetUSDC:        envFloat("MAX_BET_USDC", 50.0),
		MaxDailyLossUSDC:  envFloat("MAX_DAILY_LOSS_USDC", 200.0),
		MaxConcurrentBets: envInt("MAX_CONCURRENT_BETS", 3),
		KellyFraction:     envFloat("KELLY_FRACTION", 0.25),
		EnableOracleLag:     envBool("ENABLE_ORACLE_LAG", true),
		EnableWindowDelta:   envBool("ENABLE_WINDOW_DELTA", true),
		EnableDumpHedge:     envBool("ENABLE_DUMP_HEDGE", true),
		EnableDumpHedgeLive: envBool("ENABLE_DUMP_HEDGE_LIVE", false),
		SlippageTicks:       envInt("SLIPPAGE_TICKS", 1),
		Filters: TradeFilters{
			MinEntryPrice:      envFloat("MIN_ENTRY_PRICE", 0.60),
			MaxEntryPrice:      envFloat("MAX_ENTRY_PRICE", 0.90),
			MaxPositionSize:    envFloat("MAX_POSITION_SIZE", 0),    // 0 = no cap
			MaxLossPerTrade:    envFloat("MAX_LOSS_PER_TRADE", 0), // 0 = disabled; Kelly + MaxBetUSDC already cap size
			MaxConsecLosses:    envInt("MAX_CONSEC_LOSSES", 3),
			PauseDuration:      time.Duration(envInt("PAUSE_DURATION_MIN", 30)) * time.Minute,
			MaxTradesPerWindow: envInt("MAX_TRADES_PER_WINDOW", 1),
		},
	}

	// Dry-run and sim don't need API credentials (read-only Polymarket access)
	if c.Mode != ModeLive {
		return c, nil
	}
	if c.PolyAPIKey == "" || c.PolyAPISecret == "" || c.PolyAPIPassphrase == "" {
		return nil, fmt.Errorf("POLY_API_KEY, POLY_API_SECRET, POLY_API_PASSPHRASE required in live mode")
	}
	return c, nil
}

// mustEnv returns the value of key or panics if it is not set.
func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

// envOrDefault returns the value of key, or def if not set.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool parses key as a boolean, returning def if the variable is absent or unparseable.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// envFloat parses key as a float64, returning def if absent or unparseable.
func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// envInt parses key as an int, returning def if absent or unparseable.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}
