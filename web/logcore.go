package web

import (
	"fmt"
	"time"

	"go.uber.org/zap/zapcore"
)

// LogCore is a zapcore.Core that forwards every log entry to a LogHub.
// It wraps an inner core so all normal logging (stderr, file) still works.
type LogCore struct {
	inner  zapcore.Core
	hub    *LogHub
	fields []zapcore.Field // accumulated With() fields
}

// NewLogCore creates a LogCore wrapping inner and publishing to hub.
func NewLogCore(inner zapcore.Core, hub *LogHub) *LogCore {
	return &LogCore{inner: inner, hub: hub}
}

func (c *LogCore) Enabled(lvl zapcore.Level) bool {
	return c.inner.Enabled(lvl)
}

func (c *LogCore) With(fields []zapcore.Field) zapcore.Core {
	return &LogCore{
		inner:  c.inner.With(fields),
		hub:    c.hub,
		fields: append(append([]zapcore.Field{}, c.fields...), fields...),
	}
}

func (c *LogCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	// Only add ourselves — Write() delegates to inner, so we must NOT also forward
	// to inner.Check(). Doing so would add inner's cores to the CheckedEntry AND
	// have LogCore.Write() call inner.Write() — each log entry would be written twice.
	if c.Enabled(entry.Level) {
		ce = ce.AddCore(entry, c)
	}
	return ce
}

func (c *LogCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Publish to web hub
	all := append(c.fields, fields...)
	c.hub.Publish(LogEntry{
		TS:     entry.Time,
		Level:  entry.Level.CapitalString(),
		Msg:    entry.Message,
		Fields: encodeFields(all),
	})
	// Delegate to the original core (stderr / file)
	return c.inner.Write(entry, fields)
}

func (c *LogCore) Sync() error { return c.inner.Sync() }

// encodeFields converts zapcore fields to a plain map for JSON serialisation.
func encodeFields(fields []zapcore.Field) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	out := make(map[string]any, len(enc.Fields))
	for k, v := range enc.Fields {
		out[k] = formatFieldVal(v)
	}
	return out
}

// formatFieldVal makes sure all values are JSON-friendly (no raw time.Duration, etc.)
func formatFieldVal(v any) any {
	switch t := v.(type) {
	case time.Duration:
		return t.String()
	case time.Time:
		return t.Format(time.RFC3339)
	case fmt.Stringer:
		return t.String()
	default:
		return v
	}
}
