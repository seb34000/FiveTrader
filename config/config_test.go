package config

import (
	"os"
	"testing"
)

// ── IsDryRun / IsSimOrDryRun ──────────────────────────────────────────────────

func TestIsDryRun(t *testing.T) {
	tests := []struct {
		mode Mode
		want bool
	}{
		{ModeDryRun, true},
		{ModeSim, false},
		{ModeLive, false},
	}
	for _, tt := range tests {
		c := &Config{Mode: tt.mode}
		if got := c.IsDryRun(); got != tt.want {
			t.Errorf("IsDryRun() mode=%v = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestIsSimOrDryRun(t *testing.T) {
	tests := []struct {
		mode Mode
		want bool
	}{
		{ModeDryRun, true},
		{ModeSim, true},
		{ModeLive, false},
	}
	for _, tt := range tests {
		c := &Config{Mode: tt.mode}
		if got := c.IsSimOrDryRun(); got != tt.want {
			t.Errorf("IsSimOrDryRun() mode=%v = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

// ── envOrDefault ─────────────────────────────────────────────────────────────

func TestEnvOrDefault_Set(t *testing.T) {
	const key = "TEST_ENV_OR_DEFAULT"
	t.Setenv(key, "hello")
	if got := envOrDefault(key, "fallback"); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestEnvOrDefault_Missing(t *testing.T) {
	const key = "TEST_ENV_OR_DEFAULT_MISSING"
	os.Unsetenv(key)
	if got := envOrDefault(key, "fallback"); got != "fallback" {
		t.Errorf("got %q, want %q", got, "fallback")
	}
}

// ── envBool ───────────────────────────────────────────────────────────────────

func TestEnvBool(t *testing.T) {
	tests := []struct {
		val  string
		set  bool
		def  bool
		want bool
	}{
		{"true", true, false, true},
		{"1", true, false, true},
		{"false", true, true, false},
		{"0", true, true, false},
		{"", false, true, true},    // missing → default
		{"BAD", true, true, true},  // invalid → default
	}
	const key = "TEST_ENVBOOL"
	for _, tt := range tests {
		if tt.set {
			t.Setenv(key, tt.val)
		} else {
			os.Unsetenv(key)
		}
		if got := envBool(key, tt.def); got != tt.want {
			t.Errorf("envBool(val=%q, def=%v) = %v, want %v", tt.val, tt.def, got, tt.want)
		}
	}
}

// ── envFloat ──────────────────────────────────────────────────────────────────

func TestEnvFloat(t *testing.T) {
	tests := []struct {
		val  string
		set  bool
		def  float64
		want float64
	}{
		{"3.14", true, 0, 3.14},
		{"50", true, 0, 50},
		{"", false, 9.99, 9.99},
		{"abc", true, 9.99, 9.99}, // invalid → default
	}
	const key = "TEST_ENVFLOAT"
	for _, tt := range tests {
		if tt.set {
			t.Setenv(key, tt.val)
		} else {
			os.Unsetenv(key)
		}
		if got := envFloat(key, tt.def); got != tt.want {
			t.Errorf("envFloat(val=%q, def=%v) = %v, want %v", tt.val, tt.def, got, tt.want)
		}
	}
}

// ── envInt ────────────────────────────────────────────────────────────────────

func TestEnvInt(t *testing.T) {
	tests := []struct {
		val  string
		set  bool
		def  int
		want int
	}{
		{"42", true, 0, 42},
		{"0", true, 5, 0},
		{"", false, 7, 7},
		{"abc", true, 7, 7},
	}
	const key = "TEST_ENVINT"
	for _, tt := range tests {
		if tt.set {
			t.Setenv(key, tt.val)
		} else {
			os.Unsetenv(key)
		}
		if got := envInt(key, tt.def); got != tt.want {
			t.Errorf("envInt(val=%q, def=%v) = %v, want %v", tt.val, tt.def, got, tt.want)
		}
	}
}

// ── mustEnv ───────────────────────────────────────────────────────────────────

func TestMustEnv_Set(t *testing.T) {
	const key = "TEST_MUST_ENV"
	t.Setenv(key, "value")
	if got := mustEnv(key); got != "value" {
		t.Errorf("mustEnv got %q, want %q", got, "value")
	}
}

func TestMustEnv_Panic(t *testing.T) {
	const key = "TEST_MUST_ENV_MISSING"
	os.Unsetenv(key)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for missing env var, got none")
		}
	}()
	mustEnv(key)
}

// ── Load ──────────────────────────────────────────────────────────────────────

func TestLoad_DryRun(t *testing.T) {
	t.Setenv("PRIVATE_KEY", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("SIM_MODE", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Mode != ModeDryRun {
		t.Errorf("Mode = %v, want ModeDryRun", cfg.Mode)
	}
}

func TestLoad_SimMode(t *testing.T) {
	t.Setenv("PRIVATE_KEY", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("SIM_MODE", "true")
	t.Setenv("DRY_RUN", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Mode != ModeSim {
		t.Errorf("Mode = %v, want ModeSim", cfg.Mode)
	}
}

func TestLoad_SimMode_OverridesDryRunFalse(t *testing.T) {
	// Bug fix: SIM_MODE=true must win even when DRY_RUN=false
	t.Setenv("PRIVATE_KEY", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("SIM_MODE", "true")
	t.Setenv("DRY_RUN", "false")
	// Provide API creds to prevent live-mode credential error
	t.Setenv("POLY_API_KEY", "key")
	t.Setenv("POLY_API_SECRET", "secret")
	t.Setenv("POLY_API_PASSPHRASE", "pass")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Mode != ModeSim {
		t.Errorf("SIM_MODE=true + DRY_RUN=false should give ModeSim, got %v", cfg.Mode)
	}
}

func TestLoad_LiveRequiresCredentials(t *testing.T) {
	t.Setenv("PRIVATE_KEY", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("DRY_RUN", "false")
	t.Setenv("SIM_MODE", "false")
	os.Unsetenv("POLY_API_KEY")
	os.Unsetenv("POLY_API_SECRET")
	os.Unsetenv("POLY_API_PASSPHRASE")

	_, err := Load()
	if err == nil {
		t.Error("Load() live mode without API creds should return error")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("PRIVATE_KEY", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("DRY_RUN", "true")
	os.Unsetenv("MAX_BET_USDC")
	os.Unsetenv("MAX_DAILY_LOSS_USDC")
	os.Unsetenv("MAX_CONCURRENT_BETS")
	os.Unsetenv("KELLY_FRACTION")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MaxBetUSDC != 50.0 {
		t.Errorf("MaxBetUSDC = %v, want 50.0", cfg.MaxBetUSDC)
	}
	if cfg.MaxDailyLossUSDC != 200.0 {
		t.Errorf("MaxDailyLossUSDC = %v, want 200.0", cfg.MaxDailyLossUSDC)
	}
	if cfg.MaxConcurrentBets != 3 {
		t.Errorf("MaxConcurrentBets = %v, want 3", cfg.MaxConcurrentBets)
	}
	if cfg.KellyFraction != 0.25 {
		t.Errorf("KellyFraction = %v, want 0.25", cfg.KellyFraction)
	}
}

func TestLoad_StrategiesDefaultEnabled(t *testing.T) {
	t.Setenv("PRIVATE_KEY", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("DRY_RUN", "true")
	os.Unsetenv("ENABLE_ORACLE_LAG")
	os.Unsetenv("ENABLE_WINDOW_DELTA")
	os.Unsetenv("ENABLE_DUMP_HEDGE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.EnableOracleLag {
		t.Error("EnableOracleLag should default to true")
	}
	if !cfg.EnableWindowDelta {
		t.Error("EnableWindowDelta should default to true")
	}
	if !cfg.EnableDumpHedge {
		t.Error("EnableDumpHedge should default to true")
	}
}
