package httpserver

import (
	"testing"
	"time"
)

func TestWindowLimiter(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	limiter := newWindowLimiter(2, time.Hour)
	limiter.now = func() time.Time { return now }
	if !limiter.take("user") {
		t.Fatal("limiter rejected the first event")
	}
	if !limiter.take("user") {
		t.Fatal("limiter rejected an event within the configured limit")
	}
	if limiter.take("user") {
		t.Fatal("limiter accepted an event beyond the configured limit")
	}
	if !limiter.take("another-user") {
		t.Fatal("one user's limit affected another user")
	}
	now = now.Add(time.Hour + time.Second)
	if !limiter.take("user") {
		t.Fatal("expired events were not released")
	}
}
