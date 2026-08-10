package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateIsDeterministicAndEnforcesSQLiteFeatures(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("second migration run failed:", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO message_search(message_id, thread_id, subject, sender, recipients, body)
        VALUES ('m', 't', 'Invoice', 'alice@example.com', 'hello@example.com', 'renewal')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM message_search WHERE message_search MATCH 'invoice'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("FTS unavailable: count=%d err=%v", count, err)
	}
	if err := IntegrityCheck(ctx, database); err != nil {
		t.Fatal(err)
	}
}
