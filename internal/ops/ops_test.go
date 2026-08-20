package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/settings"
)

func TestBackupRestoreAndDoctor(t *testing.T) {
	ctx := context.Background()
	fixture := createBackupFixture(t)

	restoreRoot := t.TempDir()
	restoreCfg := testConfig(restoreRoot)
	if err := Restore(ctx, restoreCfg, fixture.directory); err != nil {
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
	threads, err := restoredRepo.ListThreads(ctx, fixture.mailboxID, "inbox", 10)
	if err != nil || len(threads) != 1 || threads[0].Subject != "Back me up" {
		t.Fatal(threads, err)
	}
	if err := Restore(ctx, restoreCfg, fixture.directory); err == nil {
		t.Fatal("restore must refuse to overwrite an installation")
	}
}

func TestBackupRejectsInvalidMasterKeySize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := testConfig(root)
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.MasterKeyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.MasterKeyPath, bytes.Repeat([]byte{1}, 31), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(ctx, cfg, repository.New(database), store, filepath.Join(t.TempDir(), "backup")); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("backup error = %v, want invalid key-size failure", err)
	}
}

func TestRestoreRejectsUnsafeManifestObjectPaths(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "database traversal", mutate: func(manifest *Manifest) { manifest.Database.Path = "../mailbox.db" }},
		{name: "database noncanonical traversal", mutate: func(manifest *Manifest) { manifest.Database.Path = "nested/../mailbox.db" }},
		{name: "database windows absolute", mutate: func(manifest *Manifest) { manifest.Database.Path = `C:\outside\mailbox.db` }},
		{name: "master key traversal", mutate: func(manifest *Manifest) {
			manifest.MasterKey = ManifestObject{Path: "../master.key", Size: manifest.Database.Size, SHA256: manifest.Database.SHA256}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createBackupFixture(t)
			manifest := fixture.manifest
			test.mutate(&manifest)
			writeBackupManifest(t, fixture.directory, manifest)
			restoreCfg := testConfig(t.TempDir())
			if err := Restore(context.Background(), restoreCfg, fixture.directory); err == nil {
				t.Fatal("restore accepted an unsafe manifest path")
			}
			assertNoRestoredDatabase(t, restoreCfg)
		})
	}
}

func TestRestoreRejectsManifestSymlinkEscape(t *testing.T) {
	fixture := createBackupFixture(t)
	externalDatabase := filepath.Join(t.TempDir(), "outside.db")
	if err := copyFile(filepath.Join(fixture.directory, fixture.manifest.Database.Path), externalDatabase, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixture.directory, fixture.manifest.Database.Path)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalDatabase, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	restoreCfg := testConfig(t.TempDir())
	err := Restore(context.Background(), restoreCfg, fixture.directory)
	if err == nil || !strings.Contains(err.Error(), "escapes backup root") {
		t.Fatalf("restore error = %v, want a symlink escape rejection", err)
	}
	assertNoRestoredDatabase(t, restoreCfg)
}

func TestRestoreRejectsInvalidManifestBlobEntries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "duplicate key", mutate: func(manifest *Manifest) {
			manifest.Blobs = append(manifest.Blobs, manifest.Blobs[0])
		}},
		{name: "traversing key", mutate: func(manifest *Manifest) {
			manifest.Blobs[0].Key = "attachments/../../escape"
		}},
		{name: "invalid blob digest", mutate: func(manifest *Manifest) {
			manifest.Blobs[0].SHA256 = "not-a-sha256"
		}},
		{name: "invalid database digest", mutate: func(manifest *Manifest) {
			manifest.Database.SHA256 = "not-a-sha256"
		}},
		{name: "missing master key", mutate: func(manifest *Manifest) {
			manifest.MasterKey = ManifestObject{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createBackupFixture(t)
			manifest := fixture.manifest
			test.mutate(&manifest)
			writeBackupManifest(t, fixture.directory, manifest)
			restoreCfg := testConfig(t.TempDir())
			if err := Restore(context.Background(), restoreCfg, fixture.directory); err == nil {
				t.Fatal("restore accepted an invalid manifest entry")
			}
			assertNoRestoredDatabase(t, restoreCfg)
		})
	}
}

func TestRestoreBoundsUntrustedManifestResources(t *testing.T) {
	backup := t.TempDir()
	if err := os.WriteFile(filepath.Join(backup, "manifest.json"), bytes.Repeat([]byte{'x'}, maxManifestBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreCfg := testConfig(t.TempDir())
	if err := Restore(context.Background(), restoreCfg, backup); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized manifest error = %v", err)
	}
	if _, err := validateRestoreManifest(backup, Manifest{Blobs: make([]ManifestBlob, maxManifestBlobs+1)}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("too-many-blobs error = %v", err)
	}
}

func TestRestoreRejectsManifestOmittingDatabaseReferencedBlob(t *testing.T) {
	fixture := createBackupFixture(t)
	manifest := fixture.manifest
	manifest.Blobs = nil
	writeBackupManifest(t, fixture.directory, manifest)
	restoreCfg := testConfig(t.TempDir())
	err := Restore(context.Background(), restoreCfg, fixture.directory)
	if err == nil || !strings.Contains(err.Error(), "omitted from manifest") {
		t.Fatalf("restore error = %v, want omitted database blob rejection", err)
	}
	assertNoRestoredDatabase(t, restoreCfg)
	if entries, readErr := os.ReadDir(restoreCfg.StorageRoot); readErr == nil && len(entries) != 0 {
		t.Fatalf("failed restore left storage objects behind: %v", entries)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestRestoreRollbackRemovesCopiedBlobDirectories(t *testing.T) {
	fixture := createBackupFixture(t)
	manifest := fixture.manifest
	manifest.Blobs[0].SHA256 = strings.Repeat("0", 64)
	writeBackupManifest(t, fixture.directory, manifest)
	restoreCfg := testConfig(t.TempDir())
	if err := Restore(context.Background(), restoreCfg, fixture.directory); err == nil {
		t.Fatal("restore accepted a blob checksum mismatch")
	}
	assertNoRestoredDatabase(t, restoreCfg)
	entries, err := os.ReadDir(restoreCfg.StorageRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed restore left storage directories behind: %v", entries)
	}
}

func TestRestoreRejectsMasterKeyThatCannotDecryptSettings(t *testing.T) {
	fixture := createBackupFixture(t)
	wrongKey := bytes.Repeat([]byte{0x24}, 32)
	keyPath := filepath.Join(fixture.directory, fixture.manifest.MasterKey.Path)
	if err := os.WriteFile(keyPath, wrongKey, 0o600); err != nil {
		t.Fatal(err)
	}
	size, checksum, err := fileDigest(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fixture.manifest
	manifest.MasterKey.Size, manifest.MasterKey.SHA256 = size, checksum
	writeBackupManifest(t, fixture.directory, manifest)
	restoreCfg := testConfig(t.TempDir())
	err = Restore(context.Background(), restoreCfg, fixture.directory)
	if err == nil || !strings.Contains(err.Error(), "cannot decrypt installation settings") {
		t.Fatalf("restore error = %v, want master-key decryption failure", err)
	}
	assertNoRestoredDatabase(t, restoreCfg)
}

type backupFixture struct {
	directory string
	manifest  Manifest
	mailboxID string
}

func createBackupFixture(t *testing.T) backupFixture {
	t.Helper()
	ctx := context.Background()
	sourceRoot := t.TempDir()
	cfg := testConfig(sourceRoot)
	if err := os.MkdirAll(filepath.Dir(cfg.MasterKeyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.MasterKeyPath, bytes.Repeat([]byte{0x42}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := settings.New(database, bytes.Repeat([]byte{0x42}, 32)).Save(ctx, settings.Values{
		Configured: true, BaseURL: "http://localhost:8080", SessionTTLHours: 1, LogLevel: "info",
		MaxWebhookBodyBytes: 1 << 20, MaxMessageTextBytes: 5 << 20, MaxUploadRequestBytes: 30 << 20,
		MaxOutboundAttachmentBytes: 25 << 20, MaxAttachmentCount: 20,
		ResendAPIKey: "re_backup_test", ResendWebhookSecret: "whsec_backup_test",
	}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	mailboxID, err := repo.EnsureMailbox(ctx, cfg.PrimaryAddress, cfg.DisplayName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	attachmentID := ids.New()
	key := blobstore.Key("attachments", attachmentID, time.Now())
	contents := []byte("important attachment")
	info, err := store.Put(ctx, key, bytes.NewReader(contents), int64(len(contents)), "text/plain")
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
	return backupFixture{directory: backupDir, manifest: manifest, mailboxID: mailboxID}
}

func writeBackupManifest(t *testing.T, directory string, manifest Manifest) {
	t.Helper()
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoRestoredDatabase(t *testing.T, cfg config.Config) {
	t.Helper()
	for _, path := range []string{cfg.DBPath, cfg.DBPath + "-wal", cfg.DBPath + "-shm"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed restore left database artifact %q: %v", path, err)
		}
	}
}

func testConfig(root string) config.Config {
	base, _ := url.Parse("http://localhost:8080")
	return config.Config{Environment: "test", BaseURL: base, ListenAddr: ":8080", DataDir: root,
		DBPath: filepath.Join(root, "mailbox.db"), MasterKeyPath: filepath.Join(root, ".litebox", "master.key"), CookieName: "test_session", SessionTTL: time.Hour,
		PrimaryAddress: "hello@example.com", DisplayName: "Example", AllowedRecipients: map[string]struct{}{"hello@example.com": {}},
		StorageBackend: "filesystem", StorageRoot: filepath.Join(root, "objects"), StorageTempRoot: filepath.Join(root, "tmp"),
		MaxWebhookBodyBytes: 1 << 20, MaxMessageTextBytes: 5 << 20, MaxUploadRequestBytes: 30 << 20,
		MaxOutboundAttachmentBytes: 25 << 20, MaxAttachmentCount: 20, WorkerCount: 1, JobPollInterval: time.Second, JobLease: time.Minute}
}
