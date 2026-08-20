package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/repository"
)

func TestEnqueueMaintenanceSchedulesSessionCleanupForEachDate(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	first := time.Date(2026, time.August, 20, 23, 59, 0, 0, time.UTC)
	for _, now := range []time.Time{first, first, first.Add(2 * time.Minute)} {
		if err := enqueueMaintenance(ctx, repo, now); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE kind = 'cleanup_sessions'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("cleanup jobs = %d, want one per UTC date", count)
	}
}
