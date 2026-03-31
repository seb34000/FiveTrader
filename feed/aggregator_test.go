package feed

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ── aggregate() ──────────────────────────────────────────────────────────────

func TestAggregate_Empty(t *testing.T) {
	agg := aggregate(map[string]*sourceState{})
	if agg.Sources != 0 || agg.Value != 0 {
		t.Errorf("empty sources: got Value=%v Sources=%v, want 0/0", agg.Value, agg.Sources)
	}
}

func TestAggregate_SingleSource_NoValue(t *testing.T) {
	// Single source: Value must be 0 (need ≥2 to trade) but BySource should be populated for display.
	sources := map[string]*sourceState{
		"binance": {price: 85000.0, updatedAt: time.Now()},
	}
	agg := aggregate(sources)
	if agg.Value != 0 {
		t.Errorf("single source: Value = %v, want 0 (need ≥2)", agg.Value)
	}
	if agg.Sources != 1 {
		t.Errorf("single source: Sources = %d, want 1", agg.Sources)
	}
	if v, ok := agg.BySource["binance"]; !ok || v != 85000.0 {
		t.Errorf("single source: BySource[binance] = %v ok=%v, want 85000/true", v, ok)
	}
}

func TestAggregate_MultiSource_MedianOdd(t *testing.T) {
	now := time.Now()
	sources := map[string]*sourceState{
		"binance":  {price: 84000.0, updatedAt: now},
		"bitstamp": {price: 86000.0, updatedAt: now},
		"coinbase": {price: 85000.0, updatedAt: now},
	}
	agg := aggregate(sources)
	if agg.Sources != 3 {
		t.Errorf("Sources = %d, want 3", agg.Sources)
	}
	// Median of [84000, 85000, 86000] = 85000 (middle value)
	want := 85000.0
	if agg.Value != want {
		t.Errorf("Value = %v, want %v (median)", agg.Value, want)
	}
}

func TestAggregate_MultiSource_MedianEven(t *testing.T) {
	now := time.Now()
	sources := map[string]*sourceState{
		"binance":  {price: 84000.0, updatedAt: now},
		"bitstamp": {price: 86000.0, updatedAt: now},
	}
	agg := aggregate(sources)
	if agg.Sources != 2 {
		t.Errorf("Sources = %d, want 2", agg.Sources)
	}
	// Median of [84000, 86000] = average of two middle = 85000
	want := 85000.0
	if agg.Value != want {
		t.Errorf("Value = %v, want %v (median of 2)", agg.Value, want)
	}
}

func TestAggregate_StaleSourceExcluded(t *testing.T) {
	now := time.Now()
	sources := map[string]*sourceState{
		"binance":  {price: 85000.0, updatedAt: now},
		"coinbase": {price: 85100.0, updatedAt: now},
		"bitstamp": {price: 99999.0, updatedAt: now.Add(-60 * time.Second)}, // stale (> 30s)
	}
	agg := aggregate(sources)
	if agg.Sources != 2 {
		t.Errorf("Sources = %d, want 2 (stale excluded)", agg.Sources)
	}
	// Median of [85000, 85100] = 85050
	want := 85050.0
	if agg.Value != want {
		t.Errorf("Value = %v, want %v (stale excluded)", agg.Value, want)
	}
	if _, ok := agg.BySource["bitstamp"]; ok {
		t.Error("stale source should not be in BySource")
	}
}

func TestAggregate_AllStale(t *testing.T) {
	old := time.Now().Add(-60 * time.Second)
	sources := map[string]*sourceState{
		"binance":  {price: 85000.0, updatedAt: old},
		"bitstamp": {price: 86000.0, updatedAt: old},
	}
	agg := aggregate(sources)
	if agg.Sources != 0 {
		t.Errorf("all stale: Sources = %d, want 0", agg.Sources)
	}
	if agg.Value != 0 {
		t.Errorf("all stale: Value = %v, want 0", agg.Value)
	}
}

func TestAggregate_BySourceContainsOnlyFresh(t *testing.T) {
	now := time.Now()
	sources := map[string]*sourceState{
		"binance":  {price: 85000.0, updatedAt: now},
		"coinbase": {price: 85100.0, updatedAt: now},
		"stale":    {price: 1.0, updatedAt: now.Add(-60 * time.Second)}, // stale (> 30s)
	}
	agg := aggregate(sources)
	if agg.Sources != 2 {
		t.Errorf("Sources = %d, want 2 (stale excluded)", agg.Sources)
	}
	if _, ok := agg.BySource["stale"]; ok {
		t.Error("stale source should not appear in BySource")
	}
	if v, ok := agg.BySource["binance"]; !ok || v != 85000.0 {
		t.Errorf("BySource[binance] = %v, want 85000", v)
	}
}

// ── RunAggregator integration ─────────────────────────────────────────────────

func TestRunAggregator_EmitsOnTick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ticks := make(chan Tick, 10)
	out := make(chan AggregatedPrice, 10)

	go RunAggregator(ctx, ticks, out, zap.NewNop())

	// Need ≥2 sources; send both before checking output
	now := time.Now()
	ticks <- Tick{Source: "binance", Price: 85000.0, Timestamp: now}
	ticks <- Tick{Source: "bitstamp", Price: 85100.0, Timestamp: now}

	// Drain until we get a 2-source aggregate
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case agg := <-out:
			if agg.Sources >= 2 {
				want := 85050.0 // median of [85000, 85100]
				if agg.Value != want {
					t.Errorf("agg.Value = %v, want %v", agg.Value, want)
				}
				return
			}
		case <-deadline:
			t.Error("timeout: no 2-source aggregated price received")
			return
		}
	}
}

func TestRunAggregator_DropsBadPrice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ticks := make(chan Tick, 10)
	out := make(chan AggregatedPrice, 10)

	go RunAggregator(ctx, ticks, out, zap.NewNop())

	ticks <- Tick{Source: "binance", Price: 0.0, Timestamp: time.Now()} // bad

	select {
	case agg := <-out:
		t.Errorf("should not emit for price=0, got %v", agg)
	case <-ctx.Done():
		// expected: nothing emitted
	}
}

func TestRunAggregator_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan Tick)
	out := make(chan AggregatedPrice)

	done := make(chan struct{})
	go func() {
		RunAggregator(ctx, ticks, out, zap.NewNop())
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("RunAggregator did not stop after context cancel")
	}
}
