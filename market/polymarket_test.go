package market

import (
	"testing"
	"time"
)

// ── WindowStart / WindowEnd / SecondsUntilClose ───────────────────────────────

func TestWindowStart_AlignedToFiveMinutes(t *testing.T) {
	ws := WindowStart()
	unix := ws.Unix()
	if unix%300 != 0 {
		t.Errorf("WindowStart unix=%d is not divisible by 300 (5 min)", unix)
	}
}

func TestWindowStart_NotInFuture(t *testing.T) {
	ws := WindowStart()
	if ws.After(time.Now()) {
		t.Errorf("WindowStart %v should not be in the future", ws)
	}
}

func TestWindowStart_WithinLastFiveMinutes(t *testing.T) {
	ws := WindowStart()
	age := time.Since(ws)
	if age < 0 || age >= 5*time.Minute {
		t.Errorf("WindowStart age = %v, should be in [0, 5min)", age)
	}
}

func TestWindowEnd_IsFiveMinutesAfterStart(t *testing.T) {
	ws := WindowStart()
	we := WindowEnd()
	diff := we.Sub(ws)
	if diff != 5*time.Minute {
		t.Errorf("WindowEnd - WindowStart = %v, want 5m", diff)
	}
}

func TestWindowEnd_InFuture(t *testing.T) {
	we := WindowEnd()
	if !we.After(time.Now()) {
		t.Errorf("WindowEnd %v should be in the future", we)
	}
}

func TestSecondsUntilClose_Positive(t *testing.T) {
	sec := SecondsUntilClose()
	if sec <= 0 {
		t.Errorf("SecondsUntilClose = %v, should be > 0", sec)
	}
}

func TestSecondsUntilClose_LessThanFiveMinutes(t *testing.T) {
	sec := SecondsUntilClose()
	if sec >= 300 {
		t.Errorf("SecondsUntilClose = %v, should be < 300", sec)
	}
}

// ── NewClient ─────────────────────────────────────────────────────────────────

func TestNewClient_SetsFields(t *testing.T) {
	c := NewClient("key", "secret", "pass", "0xABCD", nil)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.apiKey != "key" {
		t.Errorf("apiKey = %q, want key", c.apiKey)
	}
	if c.address != "0xABCD" {
		t.Errorf("address = %q, want 0xABCD", c.address)
	}
	if c.http == nil {
		t.Error("http client should not be nil")
	}
}
