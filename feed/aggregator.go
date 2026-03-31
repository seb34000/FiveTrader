package feed

import (
	"context"
	"sort"
	"time"

	"go.uber.org/zap"
)

// AggregatedPrice is the median price across all non-stale sources.
type AggregatedPrice struct {
	Value     float64
	Sources   int
	Timestamp time.Time
	BySource  map[string]float64 // latest price per source (non-stale only)
}

const (
	staleDuration = 30 * time.Second // generous: catches disconnects without penalising low-volume assets
	minSources    = 2                // require at least 2 sources to emit a price
)

type sourceState struct {
	price     float64
	updatedAt time.Time
}

// RunAggregator consumes ticks from all sources and emits aggregated prices.
// It emits on every tick (with the latest known price from each source).
func RunAggregator(ctx context.Context, ticks <-chan Tick, out chan<- AggregatedPrice, log *zap.Logger) {
	sources := make(map[string]*sourceState)

	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticks:
			if tick.Price <= 0 {
				continue
			}
			s, ok := sources[tick.Source]
			if !ok {
				s = &sourceState{}
				sources[tick.Source] = s
			}
			s.price = tick.Price
			s.updatedAt = tick.Timestamp

			agg := aggregate(sources)
			if len(agg.BySource) == 0 {
				continue // no fresh source at all
			}
			select {
			case out <- agg:
			default:
				// consumer slow, drop
			}
		}
	}
}

// aggregate computes the median price from all non-stale sources.
// Returns a zero AggregatedPrice if fewer than minSources are fresh.
func aggregate(sources map[string]*sourceState) AggregatedPrice {
	now := time.Now()
	bySource := make(map[string]float64, len(sources))
	var prices []float64

	for name, s := range sources {
		if now.Sub(s.updatedAt) > staleDuration {
			continue // ignore stale sources
		}
		prices = append(prices, s.price)
		bySource[name] = s.price
	}
	// Always return bySource so callers can display individual feed prices.
	// Value is only set when we have enough sources for a reliable aggregate.
	agg := AggregatedPrice{Sources: len(prices), BySource: bySource}
	if len(prices) >= minSources {
		agg.Value = median(prices)
		agg.Timestamp = now
	}
	return agg
}

// median returns the median of a non-empty slice (sorts in-place).
func median(prices []float64) float64 {
	sort.Float64s(prices)
	n := len(prices)
	if n%2 == 1 {
		return prices[n/2]
	}
	return (prices[n/2-1] + prices[n/2]) / 2
}
