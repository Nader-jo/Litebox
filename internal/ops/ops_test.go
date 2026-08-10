package ops

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/repository"
)

func TestBackupRestoreAndDoctor(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	cfg := testConfig(sourceRoot)
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	mailboxID, _ := repo.EnsureMailbox(ctx, cfg.PrimaryAddress, cfg.DisplayName)
	store, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	attachmentID := ids.New()
	key := blobstore.Key("attachments", attachmentID, time.Now())
	info, err := store.Put(ctx, key, bytes.NewBufferString("important attachment"), int64(len("important attachment")), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = repo.SaveInbound(ctx, repository.InboundMessage{MailboxID: mailboxID, ResendEmailID: "backup-email",
		From: model.Address{Address: "alice@example.com"}, Recipients: map[string][]model.Address{"to": {{Address: cfg.PrimaryAddress}}},
		Subject: "Back me up", TextBody: "Durable history", ReceivedAt: time.Now(), IngestStatus: "ready",
		Attachments: []model.Attachment{{ID: attachmentID, Filename: "important.txt", SafeFilename: "important.txt",
			ContentType: "text/plain", ContentDisposition: "attachment", StorageBackend: "filesystem", StorageKey: key,
			SizeBytes: info.Size, SHA256: info.SHA256, StorageStatus: "ready", CreatedAt: time.Now()}}})
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(t.TempDir(), "backup")
	manifest, err := Backup(ctx, cfg, repo, store, backupDir)
	if err != nil || len(manifest.Blobs) != 1 {
		t.Fatal(manifest, err)
	}
	database.Close()

	restoreRoot := t.TempDir()
	restoreCfg := testConfig(restoreRoot)
	if err := Restore(ctx, restoreCfg, backupDir); err != nil {
		t.Fatal(err)
	}
	restoredDB, err := db.Open(ctx, restoreCfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	restoredRepo := repository.New(restoredDB)
	restoredStore, err := blobstore.NewFileStore(restoreCfg.StorageRoot, restoreCfg.StorageTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Doctor(ctx, restoreCfg, restoredRepo, restoredStore, true); err != nil {
		t.Fatal(err)
	}
	threads, err := restoredRepo.ListThreads(ctx, mailboxID, "inbox", 10)
	if err != nil || len(threads) != 1 || threads[0].Subject != "Back me up" {
		t.Fatal(threads, err)
	}
	if err := Restore(ctx, restoreCfg, backupDir); err == nil {
		t.Fatal("restore must refuse to overwrite an installation")
	}
}

func testConfig(root string) config.Config {
	base, _ := url.Parse("http://localhost:8080")
	return config.Config{Environment: "test", BaseURL: base, ListenAddr: ":8080", DataDir: root,
		DBPath: filepath.Join(root, "mailbox.db"), CookieName: "test_session", SessionTTL: time.Hour,
		PrimaryAddress: "hello@example.com", DisplayName: "Example", AllowedRecipients: map[string]struct{}{"hello@example.com": {}},
		StorageBackend: "filesystem", StorageRoot: filepath.Join(root, "objects"), StorageTempRoot: filepath.Join(root, "tmp"),
		MaxWebhookBodyBytes: 1 << 20, MaxMessageTextBytes: 5 << 20, MaxUploadRequestBytes: 30 << 20,
		MaxOutboundAttachmentBytes: 25 << 20, MaxAttachmentCount: 20, WorkerCount: 1, JobPollInterval: time.Second, JobLease: time.Minute}
}
