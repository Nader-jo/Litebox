package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenEscapesSQLiteURIControlCharactersInPath(t *testing.T) {
	ctx := context.Background()
	directory := "directory#fragment"
	if runtime.GOOS != "windows" {
		directory = "directory?query#fragment"
	}
	databasePath := filepath.Join(t.TempDir(), directory, "mailbox.db")
	database, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE uri_path_test(value TEXT NOT NULL)"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO uri_path_test(value) VALUES ('expected')"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(databasePath); err != nil || info.Size() == 0 {
		t.Fatalf("escaped database path was not created: info=%v err=%v", info, err)
	}

	database, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRowContext(ctx, "SELECT value FROM uri_path_test").Scan(&value); err != nil || value != "expected" {
		t.Fatalf("reopened escaped database value=%q err=%v", value, err)
	}
}

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

func TestMultiMailboxMigrationUpgradesReleasedSchema(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"001_initial.sql", "002_search.sql", "003_keyset_pagination.sql"} {
		contents, err := migrationFS.ReadFile("migrations/" + version)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(contents)); err != nil {
			t.Fatalf("apply fixture migration %s: %v", version, err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, 1)`, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO mailboxes
		(id, address, local_part, domain, display_name, is_primary, created_at, updated_at)
		VALUES ('mailbox-old', 'hello@example.com', 'hello', 'example.com', 'Example', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO users
		(id, email, display_name, password_hash, created_at, updated_at)
		VALUES ('user-old', 'owner@example.com', 'Owner', 'hash', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	var addressMailbox, role string
	if err := database.QueryRowContext(ctx, `SELECT mailbox_id FROM mailbox_addresses WHERE address = 'hello@example.com'`).Scan(&addressMailbox); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE user_id = 'user-old' AND mailbox_id = 'mailbox-old'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if addressMailbox != "mailbox-old" || role != "owner" {
		t.Fatalf("upgrade backfill mailbox=%q role=%q", addressMailbox, role)
	}
	if err := IntegrityCheck(ctx, database); err != nil {
		t.Fatal(err)
	}
}
