package jobs

import (
	"testing"
	"time"
)

func TestBackoffIsBounded(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= 12; attempt++ {
		value := Backoff(attempt)
		if value < 9*time.Second || value > 7*time.Hour {
			t.Fatalf("attempt %d produced invalid backoff %s", attempt, value)
		}
		if attempt > 1 && value < previous/3 {
			t.Fatalf("backoff unexpectedly regressed from %s to %s", previous, value)
		}
		previous = value
	}
}
