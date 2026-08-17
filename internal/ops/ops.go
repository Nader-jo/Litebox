// Package ops implements recovery-safe CLI operations.
package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/repository"
)

// Manifest is the portable integrity contract for a Litebox backup.
type Manifest struct {
	FormatVersion int            `json:"format_version"`
	CreatedAt     time.Time      `json:"created_at"`
	Database      ManifestObject `json:"database"`
	MasterKey     ManifestObject `json:"master_key,omitempty"`
	Blobs         []ManifestBlob `json:"blobs"`
}

// ManifestObject describes the backed-up SQLite file.
type ManifestObject struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ManifestBlob describes one logical private object.
type ManifestBlob struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Backup creates a new self-verifying directory. The server must be stopped.
func Backup(ctx context.Context, cfg config.Config, repo *repository.Repository, store blobstore.Store, output string) (Manifest, error) {
	absolute, err := filepath.Abs(output)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Mkdir(absolute, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create new backup directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(absolute)
		}
	}()
	if err := db.IntegrityCheck(ctx, repo.DB()); err != nil {
		return Manifest{}, err
	}
	databaseDestination := filepath.Join(absolute, "mailbox.db")
	var quoted string
	if err := repo.DB().QueryRowContext(ctx, "SELECT quote(?)", databaseDestination).Scan(&quoted); err != nil {
		return Manifest{}, fmt.Errorf("quote backup path: %w", err)
	}
	if _, err := repo.DB().ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return Manifest{}, fmt.Errorf("create SQLite snapshot: %w", err)
	}
	dbSize, dbHash, err := fileDigest(databaseDestination)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{FormatVersion: 1, CreatedAt: time.Now().UTC(), Database: ManifestObject{Path: "mailbox.db", Size: dbSize, SHA256: dbHash}}
	if cfg.MasterKeyPath != "" {
		keySize, keyHash, keyErr := fileDigest(cfg.MasterKeyPath)
		if keyErr != nil {
			return Manifest{}, fmt.Errorf("read master key: %w", keyErr)
		}
		keyDestination := filepath.Join(absolute, "master.key")
		if err := copyFile(cfg.MasterKeyPath, keyDestination, 0o600); err != nil {
			return Manifest{}, fmt.Errorf("copy master key: %w", err)
		}
		manifest.MasterKey = ManifestObject{Path: "master.key", Size: keySize, SHA256: keyHash}
	}
	references, err := repo.ReferencedBlobs(ctx)
	if err != nil {
		return Manifest{}, err
	}
	keys := make([]string, 0, len(references))
	for key := range references {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	backupStore, err := blobstore.NewFileStore(filepath.Join(absolute, "objects"), filepath.Join(absolute, "tmp"))
	if err != nil {
		return Manifest{}, err
	}
	for _, key := range keys {
		reader, info, err := store.Get(ctx, key)
		if err != nil {
			return Manifest{}, fmt.Errorf("referenced blob %q is missing", key)
		}
		copied, putErr := backupStore.Put(ctx, key, reader, info.Size, "application/octet-stream")
		reader.Close()
		if putErr != nil {
			return Manifest{}, fmt.Errorf("copy blob %q: %w", key, putErr)
		}
		if expected := references[key]; expected != "" && expected != copied.SHA256 {
			return Manifest{}, fmt.Errorf("blob %q checksum mismatch", key)
		}
		manifest.Blobs = append(manifest.Blobs, ManifestBlob{Key: key, Size: copied.Size, SHA256: copied.SHA256})
	}
	_ = os.RemoveAll(filepath.Join(absolute, "tmp"))
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(absolute, "manifest.json"), append(encoded, '\n'), 0o600); err != nil {
		return Manifest{}, err
	}
	cleanup = false
	return manifest, nil
}

// Restore restores only into an empty configured data location to prevent accidental overwrite.
func Restore(ctx context.Context, cfg config.Config, input string) error {
	if _, err := os.Stat(cfg.DBPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing database %s", cfg.DBPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if entries, err := os.ReadDir(cfg.StorageRoot); err == nil && len(entries) != 0 {
		return fmt.Errorf("refusing to restore into non-empty storage root %s", cfg.StorageRoot)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoded, err := os.ReadFile(filepath.Join(input, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil || manifest.FormatVersion != 1 {
		return fmt.Errorf("unsupported or invalid backup manifest")
	}
	databaseSource := filepath.Join(input, filepath.FromSlash(manifest.Database.Path))
	size, checksum, err := fileDigest(databaseSource)
	if err != nil || size != manifest.Database.Size || checksum != manifest.Database.SHA256 {
		return fmt.Errorf("backup database failed integrity validation")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return err
	}
	if err := copyFile(databaseSource, cfg.DBPath, 0o600); err != nil {
		return err
	}
	masterKeyPath := cfg.MasterKeyPath
	if masterKeyPath == "" {
		masterKeyPath = filepath.Join(filepath.Dir(cfg.DBPath), ".litebox", "master.key")
	}
	keyCreated := false
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(cfg.DBPath)
			if keyCreated {
				_ = os.Remove(masterKeyPath)
			}
			for _, blob := range manifest.Blobs {
				_ = destinationDelete(cfg.StorageRoot, blob.Key)
			}
		}
	}()
	if manifest.MasterKey.Path != "" {
		keySource := filepath.Join(input, filepath.FromSlash(manifest.MasterKey.Path))
		size, checksum, keyErr := fileDigest(keySource)
		if keyErr != nil || size != manifest.MasterKey.Size || checksum != manifest.MasterKey.SHA256 {
			return fmt.Errorf("backup master key failed integrity validation")
		}
		if _, statErr := os.Stat(masterKeyPath); statErr == nil {
			existingSize, existingHash, digestErr := fileDigest(masterKeyPath)
			if digestErr != nil || existingSize != size || existingHash != checksum {
				return fmt.Errorf("refusing to overwrite a different master key")
			}
		} else if errors.Is(statErr, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(masterKeyPath), 0o700); err != nil {
				return err
			}
			if err := copyFile(keySource, masterKeyPath, 0o600); err != nil {
				return err
			}
			keyCreated = true
		} else {
			return statErr
		}
	}
	temporary, err := os.MkdirTemp("", "litebox-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	sourceStore, err := blobstore.NewFileStore(filepath.Join(input, "objects"), temporary)
	if err != nil {
		return err
	}
	destinationStore, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		return err
	}
	for _, blob := range manifest.Blobs {
		reader, info, err := sourceStore.Get(ctx, blob.Key)
		if err != nil || info.Size != blob.Size {
			return fmt.Errorf("backup blob %q failed integrity validation", blob.Key)
		}
		copied, putErr := destinationStore.Put(ctx, blob.Key, reader, blob.Size, "application/octet-stream")
		reader.Close()
		if putErr != nil || copied.SHA256 != blob.SHA256 {
			return fmt.Errorf("restore blob %q: integrity failure", blob.Key)
		}
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := db.IntegrityCheck(ctx, database); err != nil {
		return err
	}
	failed = false
	return nil
}

// Doctor verifies the database, blob references, and optionally every blob checksum.
func Doctor(ctx context.Context, cfg config.Config, repo *repository.Repository, store blobstore.Store, deep bool) error {
	if err := db.IntegrityCheck(ctx, repo.DB()); err != nil {
		return err
	}
	if err := store.Health(ctx); err != nil {
		return err
	}
	references, err := repo.ReferencedBlobs(ctx)
	if err != nil {
		return err
	}
	for key, expected := range references {
		exists, err := store.Exists(ctx, key)
		if err != nil || !exists {
			return fmt.Errorf("referenced blob %q is missing", key)
		}
		if !deep || expected == "" {
			continue
		}
		reader, _, err := store.Get(ctx, key)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, reader)
		reader.Close()
		if copyErr != nil || hex.EncodeToString(hash.Sum(nil)) != expected {
			return fmt.Errorf("blob %q checksum mismatch", key)
		}
	}
	if _, err := repo.DB().ExecContext(ctx, "INSERT INTO message_search(message_search) VALUES('integrity-check')"); err != nil {
		return fmt.Errorf("FTS integrity check: %w", err)
	}
	_ = cfg
	return nil
}

func destinationDelete(root, key string) error {
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid manifest key")
	}
	return os.Remove(filepath.Join(root, clean))
}

func fileDigest(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		_ = os.Remove(destination)
		return err
	}
	return output.Close()
}
