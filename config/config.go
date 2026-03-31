package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Mode represents the bot operating mode.
type Mode int

const (
	ModeDryRun Mode = iota // paper trading, no real data (default)
	ModeSim                // live simulation: real feeds, simulated fills, real P&L tracking
	ModeLive               // real money trading
)

type Config struct {
	// Wallet
	PrivateKey string
	Address    string // derived from private key

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
