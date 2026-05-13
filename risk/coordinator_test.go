package risk

import (
	"testing"
	"time"
)

// windowAt returns a fake window-end time N minutes in the future to use as a stable key.
func windowAt(n int) time.Time {
	return time.Now().Add(time.Duration(n) * time.Minute)
}

func newTestCoord() *Coordinator {
	return NewCoordinator(1, nil) // max 1 per window, default ETH/XRP correlation
}

// ── ReserveWindow ─────────────────────────────────────────────────────────────

func TestCoordinator_FirstReservationSucceeds(t *testing.T) {
	c := newTestCoord()
	ok, reason := c.ReserveWindow("btc", windowAt(5))
	if !ok {
		t.Errorf("first reservation should succeed, got reason=%q", reason)
	}
}

func TestCoordinator_SameAssetSameWindow_Rejected(t *testing.T) {
	c := newTestCoord()
	w := windowAt(5)
	c.ReserveWindow("eth", w) // first — succeeds
	ok, reason := c.ReserveWindow("eth", w)
	if ok {
		t.Error("second reservation for same asset/window should be rejected")
	}
	if reason != "window_taken" {
		t.Errorf("expected reason 'window_taken', got %q", reason)
	}
}

func TestCoordinator_SameAssetDifferentWindows_OK(t *testing.T) {
	c := newTestCoord()
	c.ReserveWindow("eth", windowAt(5))
	ok, reason := c.ReserveWindow("eth", windowAt(10))
	if !ok {
		t.Errorf("same asset in different window should succeed, got reason=%q", reason)
	}
}

func TestCoordinator_CorrelatedAssets_SameWindow_Rejected(t *testing.T) {
	c := newTestCoord()
	w := windowAt(5)
	c.ReserveWindow("eth", w) // ETH claims window
	ok, reason := c.ReserveWindow("xrp", w)
	if ok {
		t.Error("XRP should be rejected when ETH is already trading in the same window")
	}
	if reason != "correlated_asset" {
		t.Errorf("expected reason 'correlated_asset', got %q", reason)
	}
}

func TestCoordinator_CorrelatedAssets_DifferentWindows_OK(t *testing.T) {
	c := newTestCoord()
	c.ReserveWindow("eth", windowAt(5))
	ok, reason := c.ReserveWindow("xrp", windowAt(10))
	if !ok {
		t.Errorf("correlated assets in different windows should succeed, got reason=%q", reason)
	}
}

func TestCoordinator_CorrelationIsSymmetric(t *testing.T) {
	c := newTestCoord()
	w := windowAt(5)
	c.ReserveWindow("xrp", w) // XRP first
	ok, reason := c.ReserveWindow("eth", w)
	if ok {
		t.Error("ETH should be rejected when XRP is already trading in the same window")
	}
	if reason != "correlated_asset" {
		t.Errorf("expected reason 'correlated_asset', got %q", reason)
	}
}

func TestCoordinator_BTCNotCorrelated(t *testing.T) {
	c := newTestCoord()
	w := windowAt(5)
	c.ReserveWindow("eth", w)
	ok, reason := c.ReserveWindow("btc", w)
	if !ok {
		t.Errorf("BTC is not correlated with ETH, should succeed, got reason=%q", reason)
	}
}

// ── ReleaseWindow ─────────────────────────────────────────────────────────────

func TestCoordinator_ReleaseAllowsRetry(t *testing.T) {
	c := newTestCoord()
	w := windowAt(5)
	c.ReserveWindow("eth", w)
	c.ReleaseWindow("eth", w) // release on execution failure
	ok, reason := c.ReserveWindow("eth", w)
	if !ok {
		t.Errorf("after release, reservation should succeed again, got reason=%q", reason)
	}
}

func TestCoordinator_ReleaseUnblocksCorrelatedAsset(t *testing.T) {
	c := newTestCoord()
	w := windowAt(5)
	c.ReserveWindow("eth", w)
	c.ReleaseWindow("eth", w)
	// XRP should now be free to trade since ETH released
	ok, reason := c.ReserveWindow("xrp", w)
	if !ok {
		t.Errorf("XRP should succeed after ETH released, got reason=%q", reason)
	}
}

// ── Zero windowEnd (no market state yet) ─────────────────────────────────────

func TestCoordinator_ZeroWindowEnd_AlwaysAllows(t *testing.T) {
	c := newTestCoord()
	ok, reason := c.ReserveWindow("eth", time.Time{})
	if !ok {
		t.Errorf("zero windowEnd should always allow (no state yet), got reason=%q", reason)
	}
}
