package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/app"
	"github.com/Nader-jo/Litebox/internal/config"
)

func TestNewExistingDatabaseWithoutMasterKeyDoesNotMutateDatabase(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "mailbox.db")
	masterKeyPath := filepath.Join(root, ".litebox", "master.key")
	if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
		t.Fatalf("create empty database: %v", err)
	}
	baseURL, err := config.ParseBaseURL("http://localhost:8080", false)
	if err != nil {
		t.Fatalf("parse test base URL: %v", err)
	}
	cfg := config.Config{
		Environment:                "development",
		AllowUnconfigured:          true,
		BaseURL:                    baseURL,
		ListenAddr:                 ":8080",
		DataDir:                    root,
		DBPath:                     databasePath,
		MasterKeyPath:              masterKeyPath,
		CookieName:                 "test_session",
		SessionTTL:                 time.Hour,
		LogLevel:                   "error",
		PrimaryAddress:             "hello@example.com",
		DisplayName:                "Example",
		AllowedRecipients:          map[string]struct{}{"hello@example.com": {}},
		StorageBackend:             "filesystem",
		StorageRoot:                filepath.Join(root, "objects"),
		StorageTempRoot:            filepath.Join(root, "tmp"),
		MaxWebhookBodyBytes:        1 << 20,
		MaxMessageTextBytes:        5 << 20,
		MaxUploadRequestBytes:      30 << 20,
		MaxOutboundAttachmentBytes: 25 << 20,
		MaxAttachmentCount:         20,
		WorkerCount:                1,
		JobPollInterval:            time.Second,
		JobLease:                   time.Minute,
	}

	application, err := app.New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		if application != nil {
			_ = application.Close()
		}
		t.Fatal("New succeeded without the existing database's master key")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New error = %v, want missing master key error", err)
	}

	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat database after failed New: %v", err)
	}
	if databaseInfo.Size() != 0 {
		t.Fatalf("database size after failed New = %d, want 0", databaseInfo.Size())
	}
	if _, err := os.Stat(masterKeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("master key exists after failed New: %v", err)
	}
}
