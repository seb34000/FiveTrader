package risk

import (
	"sync"
	"time"
)

// Coordinator guards cross-asset position limits across all per-asset event loops:
//
//   - At most MaxTradesPerWindow trades per asset per 5-min window.
//   - Correlated assets (e.g. ETH and XRP) cannot both trade in the same window.
//
// One Coordinator is shared across all runAssetLoop goroutines via the main.go wiring.
type Coordinator struct {
	mu     sync.Mutex
	// reservations: windowEndUnix → assetSymbol → reserved
	reservations      map[int64]map[string]bool
	maxPerWindow      int
	correlationGroups [][]string // e.g. [["eth","xrp"]]
}

// NewCoordinator creates a Coordinator.
// maxPerWindow is the maximum number of trades per asset per window (typically 1).
// correlationGroups lists sets of assets that must not trade in the same window;
// pass nil to use the default [["eth","xrp"]].
func NewCoordinator(maxPerWindow int, correlationGroups [][]string) *Coordinator {
	if maxPerWindow <= 0 {
		maxPerWindow = 1
	}
	if correlationGroups == nil {
		correlationGroups = [][]string{{"eth", "xrp"}}
	}
	return &Coordinator{
		reservations:      make(map[int64]map[string]bool),
		maxPerWindow:      maxPerWindow,
		correlationGroups: correlationGroups,
	}
}

// ReserveWindow attempts to claim a slot for (asset, windowEnd).
// Returns (true, "") on success or (false, reason) on rejection.
// reason is one of "window_taken" or "correlated_asset".
//
// Call ReleaseWindow if the subsequent execution fails so the slot is freed.
// Successful trades do not need explicit release — the window expires naturally.
func (c *Coordinator) ReserveWindow(asset string, windowEnd time.Time) (bool, string) {
	if windowEnd.IsZero() {
		return true, "" // no window info yet — don't gate
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := windowEnd.Unix()
	c.ensureWindow(key)

	// Check per-asset limit within this window.
	if c.reservations[key][asset] {
		return false, "window_taken"
	}

	// Check correlation: reject if any other asset in the same group already has a slot.
	for _, group := range c.correlationGroups {
		if !inGroup(group, asset) {
			continue
		}
		for _, peer := range group {
			if peer != asset && c.reservations[key][peer] {
				return false, "correlated_asset"
			}
		}
	}

	c.reservations[key][asset] = true
	c.pruneOldWindows(key)
	return true, ""
}

// ReleaseWindow frees a reservation previously made by ReserveWindow.
// Call this when execution fails after a successful ReserveWindow call.
func (c *Coordinator) ReleaseWindow(asset string, windowEnd time.Time) {
	if windowEnd.IsZero() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := windowEnd.Unix()
	if m, ok := c.reservations[key]; ok {
		delete(m, asset)
	}
}

// ensureWindow initialises the inner map for key if it does not exist.
// Must be called under c.mu.
func (c *Coordinator) ensureWindow(key int64) {
	if _, ok := c.reservations[key]; !ok {
		c.reservations[key] = make(map[string]bool)
	}
}

// pruneOldWindows removes entries for windows that ended more than 10 minutes ago.
// Must be called under c.mu.
func (c *Coordinator) pruneOldWindows(currentKey int64) {
	cutoff := currentKey - 600 // 10-minute retention
	for k := range c.reservations {
		if k < cutoff {
			delete(c.reservations, k)
		}
	}
}

// inGroup reports whether asset is a member of group.
func inGroup(group []string, asset string) bool {
	for _, a := range group {
		if a == asset {
			return true
		}
	}
	return false
}
