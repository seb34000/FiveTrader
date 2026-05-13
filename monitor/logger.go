package monitor

import (
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/seb/fivetrader/config"
	"github.com/seb/fivetrader/risk"
	"github.com/seb/fivetrader/strategy"
)

// NewLogger creates a structured zap logger.
// All levels (DEBUG+) go to stderr. If errLogPath is non-empty, WARN+ are also
// written to that file in JSON format (one entry per line, append mode).
func NewLogger(_ config.Mode, errLogPath string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.EncodeDuration = zapcore.StringDurationEncoder
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)

	base, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	if errLogPath == "" {
		return base, nil
	}

	f, err := os.OpenFile(errLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open error log: %w", err)
	}
	fileCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(cfg.EncoderConfig),
		zapcore.AddSync(f),
		zap.NewAtomicLevelAt(zap.WarnLevel),
	)
	return base.WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core {
		return zapcore.NewTee(c, fileCore)
	})), nil
}

// LogSignal logs a strategy signal at INFO level.
func LogSignal(log *zap.Logger, sig *strategy.Signal) {
	log.Debug("signal generated",
		zap.String("strategy", sig.Strategy),
		zap.String("direction", sig.Direction.String()),
		zap.Float64("ask", sig.AskPrice),
		zap.Float64("win_prob", sig.WinProb),
		zap.Float64("edge", sig.Edge),
		zap.Float64("confidence", sig.Confidence),
	)
}

// LogStats logs a periodic P&L summary.
func LogStats(log *zap.Logger, rm *risk.Manager) {
	trades, pnl, winRate := rm.DailyStats()
	log.Debug("daily stats",
		zap.Int("trades", trades),
		zap.String("pnl", fmt.Sprintf("$%.2f", pnl)),
		zap.String("win_rate", fmt.Sprintf("%.1f%%", winRate*100)),
	)
}

// LogPrice logs aggregated price at debug level.
func LogPrice(log *zap.Logger, live, oracle float64, oracleAge time.Duration) {
	delta := 0.0
	if oracle > 0 {
		delta = (live - oracle) / oracle * 100
	}
	log.Debug("price update",
		zap.Float64("live", live),
		zap.Float64("oracle", oracle),
		zap.String("delta_pct", fmt.Sprintf("%.4f%%", delta)),
		zap.Duration("oracle_age", oracleAge.Truncate(time.Second)),
	)
}

// SimStratStats is a per-strategy summary passed to LogSimStats.
type SimStratStats struct {
	Count   int
	PnL     float64
	WinRate float64
}

// LogSimStats logs overall P&L and a per-strategy breakdown for sim mode.
func LogSimStats(log *zap.Logger, rm *risk.Manager, byStrategy map[string]SimStratStats) {
	trades, pnl, winRate := rm.DailyStats()
	log.Info("=== SIM STATS ===",
		zap.Int("settled_trades", trades),
		zap.String("total_pnl", fmt.Sprintf("$%.2f", pnl)),
		zap.String("win_rate", fmt.Sprintf("%.1f%%", winRate*100)),
	)
	for name, s := range byStrategy {
		log.Info("  strategy",
			zap.String("name", name),
			zap.Int("trades", s.Count),
			zap.String("pnl", fmt.Sprintf("$%.2f", s.PnL)),
			zap.String("win_rate", fmt.Sprintf("%.1f%%", s.WinRate*100)),
		)
	}
}
