package httpserver

import (
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

func TestThreadCursorRoundTrip(t *testing.T) {
	wantTime := time.Date(2026, time.August, 10, 12, 30, 0, 123_000_000, time.UTC)
	wantID := "5b56d30a-c3de-4293-ae5e-5ed832281dae"
	encoded := encodeThreadCursor(model.ThreadSummary{ID: wantID, LatestMessageAt: wantTime})
	gotTime, gotID, err := parseThreadCursor(encoded)
	if err != nil || !gotTime.Equal(wantTime) || gotID != wantID {
		t.Fatalf("got (%v, %q, %v), want (%v, %q)", gotTime, gotID, err, wantTime, wantID)
	}
	for _, malformed := range []string{"%%%", "bm90LWEtY3Vyc29y", "MC5pZA"} {
		if _, _, err := parseThreadCursor(malformed); err == nil {
			t.Fatalf("cursor %q should fail", malformed)
		}
	}
}
