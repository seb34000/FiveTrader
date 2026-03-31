package strategy

import (
	"testing"
	"time"

	"github.com/seb/fivetrader/market"
)

func TestSecondsElapsed(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-90 * time.Second)
	ctx := &Context{
		Market: market.State{
			WindowStart: windowStart,
			WindowEnd:   windowStart.Add(5 * time.Minute),
		},
		Now: now,
	}
	elapsed := ctx.SecondsElapsed()
	if elapsed < 89 || elapsed > 91 {
		t.Errorf("SecondsElapsed() = %v, want ~90", elapsed)
	}
}

func TestSecondsRemaining(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-90 * time.Second)
	windowEnd := windowStart.Add(5 * time.Minute)
	ctx := &Context{
		Market: market.State{
			WindowStart: windowStart,
			WindowEnd:   windowEnd,
		},
		Now: now,
	}
	remaining := ctx.SecondsRemaining()
	want := 210.0 // 300 - 90
	if remaining < want-1 || remaining > want+1 {
		t.Errorf("SecondsRemaining() = %v, want ~%v", remaining, want)
	}
}

func TestSecondsElapsed_Plus_SecondsRemaining_EqualWindowDuration(t *testing.T) {
	now := time.Now()
	windowStart := now.Add(-150 * time.Second)
	windowEnd := windowStart.Add(5 * time.Minute)
	ctx := &Context{
		Market: market.State{
			WindowStart: windowStart,
			WindowEnd:   windowEnd,
		},
		Now: now,
	}
	total := ctx.SecondsElapsed() + ctx.SecondsRemaining()
	if total < 299 || total > 301 {
		t.Errorf("elapsed+remaining = %v, want ~300", total)
	}
}
