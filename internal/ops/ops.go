// Package ops implements recovery-safe CLI operations.
package ops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/settings"
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

const (
	maxManifestBytes = 16 << 20
	maxManifestBlobs = 100_000
)

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
	if cfg.MasterKeyPath == "" {
		return Manifest{}, errors.New("master key path is required for backup")
	}
	keySize, keyHash, keyErr := fileDigest(cfg.MasterKeyPath)
	if keyErr != nil {
		return Manifest{}, fmt.Errorf("read master key: %w", keyErr)
	}
	if keySize != 32 {
		return Manifest{}, fmt.Errorf("master key must contain 32 bytes")
	}
	keyDestination := filepath.Join(absolute, "master.key")
	if err := copyFile(cfg.MasterKeyPath, keyDestination, 0o600); err != nil {
		return Manifest{}, fmt.Errorf("copy master key: %w", err)
	}
	copiedSize, copiedHash, keyErr := fileDigest(keyDestination)
	if keyErr != nil || copiedSize != keySize || copiedHash != keyHash {
		return Manifest{}, fmt.Errorf("verify copied master key: source changed during backup")
	}
	manifest.MasterKey = ManifestObject{Path: "master.key", Size: keySize, SHA256: keyHash}
	references, err := repo.ReferencedBlobs(ctx)
	if err != nil {
		return Manifest{}, err
	}
	if len(references) > maxManifestBlobs {
		return Manifest{}, fmt.Errorf("backup contains too many blobs (maximum %d)", maxManifestBlobs)
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
	if len(encoded)+1 > maxManifestBytes {
		return Manifest{}, fmt.Errorf("backup manifest exceeds %d-byte limit", maxManifestBytes)
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
	encoded, err := readBoundedFile(filepath.Join(input, "manifest.json"), maxManifestBytes)
	if err != nil {
		return fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil || manifest.FormatVersion != 1 {
		return fmt.Errorf("unsupported or invalid backup manifest")
	}
	validated, err := validateRestoreManifest(input, manifest)
	if err != nil {
		return fmt.Errorf("invalid backup manifest: %w", err)
	}
	databaseSource := validated.databaseSource
	size, checksum, err := fileDigest(databaseSource)
	if err != nil || size != manifest.Database.Size || !strings.EqualFold(checksum, manifest.Database.SHA256) {
		return fmt.Errorf("backup database failed integrity validation")
	}
	masterKeyPath := cfg.MasterKeyPath
	if masterKeyPath == "" {
		masterKeyPath = filepath.Join(filepath.Dir(cfg.DBPath), ".litebox", "master.key")
	}
	keySource := validated.masterKeySource
	keySize, keyChecksum, err := fileDigest(keySource)
	if err != nil || keySize != manifest.MasterKey.Size || !strings.EqualFold(keyChecksum, manifest.MasterKey.SHA256) {
		return fmt.Errorf("backup master key failed integrity validation")
	}
	keyMaterial, err := os.ReadFile(keySource)
	if err != nil || len(keyMaterial) != 32 {
		return fmt.Errorf("backup master key failed integrity validation")
	}
	if err := preflightBackupDatabase(ctx, databaseSource, keyMaterial, validated.blobs); err != nil {
		return err
	}
	for _, blob := range manifest.Blobs {
		blobSize, blobChecksum, digestErr := fileDigest(validated.blobSources[blob.Key])
		if digestErr != nil || blobSize != blob.Size || !strings.EqualFold(blobChecksum, blob.SHA256) {
			return fmt.Errorf("backup blob %q failed integrity validation", blob.Key)
		}
	}
	keyExists := false
	if _, statErr := os.Stat(masterKeyPath); statErr == nil {
		existingSize, existingHash, digestErr := fileDigest(masterKeyPath)
		if digestErr != nil || existingSize != keySize || !strings.EqualFold(existingHash, keyChecksum) {
			return fmt.Errorf("refusing to overwrite a different master key")
		}
		keyExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	keyCreated := false
	failed := true
	defer func() {
		if failed {
			for _, databasePath := range []string{cfg.DBPath, cfg.DBPath + "-wal", cfg.DBPath + "-shm"} {
				_ = os.Remove(databasePath)
			}
			if keyCreated {
				_ = os.Remove(masterKeyPath)
			}
			for _, blob := range manifest.Blobs {
				_ = destinationDelete(cfg.StorageRoot, blob.Key)
			}
		}
	}()
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return err
	}
	if err := copyFile(databaseSource, cfg.DBPath, 0o600); err != nil {
		return err
	}
	if !keyExists {
		if err := os.MkdirAll(filepath.Dir(masterKeyPath), 0o700); err != nil {
			return err
		}
		if err := copyFile(keySource, masterKeyPath, 0o600); err != nil {
			return err
		}
		keyCreated = true
	}
	destinationStore, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		return err
	}
	for _, blob := range manifest.Blobs {
		reader, err := os.Open(validated.blobSources[blob.Key])
		if err != nil {
			return fmt.Errorf("backup blob %q failed integrity validation", blob.Key)
		}
		info, statErr := reader.Stat()
		if statErr != nil || info.Size() != blob.Size {
			reader.Close()
			return fmt.Errorf("backup blob %q failed integrity validation", blob.Key)
		}
		copied, putErr := destinationStore.Put(ctx, blob.Key, reader, blob.Size, "application/octet-stream")
		reader.Close()
		if putErr != nil || !strings.EqualFold(copied.SHA256, blob.SHA256) {
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
	restoredRepository := repository.New(database)
	if err := verifyRestoredBlobs(ctx, restoredRepository, destinationStore, validated.blobs); err != nil {
		return err
	}
	failed = false
	return nil
}

type validatedManifest struct {
	databaseSource  string
	masterKeySource string
	blobs           map[string]ManifestBlob
	blobSources     map[string]string
}

func validateRestoreManifest(input string, manifest Manifest) (validatedManifest, error) {
	if len(manifest.Blobs) > maxManifestBlobs {
		return validatedManifest{}, fmt.Errorf("manifest contains too many blobs")
	}
	validated := validatedManifest{
		blobs:       make(map[string]ManifestBlob, len(manifest.Blobs)),
		blobSources: make(map[string]string, len(manifest.Blobs)),
	}
	if manifest.Database.Path != "mailbox.db" {
		return validated, fmt.Errorf("database path must be mailbox.db")
	}
	if manifest.Database.Size < 0 || !validSHA256(manifest.Database.SHA256) {
		return validated, fmt.Errorf("database metadata is invalid")
	}
	databaseSource, err := containedManifestFile(input, manifest.Database.Path)
	if err != nil {
		return validated, fmt.Errorf("database path: %w", err)
	}
	validated.databaseSource = databaseSource

	if manifest.MasterKey.Path != "master.key" {
		return validated, fmt.Errorf("master key path must be master.key")
	}
	if manifest.MasterKey.Size != 32 || !validSHA256(manifest.MasterKey.SHA256) {
		return validated, fmt.Errorf("master key metadata is invalid")
	}
	masterKeySource, err := containedManifestFile(input, manifest.MasterKey.Path)
	if err != nil {
		return validated, fmt.Errorf("master key path: %w", err)
	}
	validated.masterKeySource = masterKeySource

	for index, blob := range manifest.Blobs {
		if err := validateManifestBlobKey(blob.Key); err != nil {
			return validated, fmt.Errorf("blob %d key: %w", index, err)
		}
		if _, duplicate := validated.blobs[blob.Key]; duplicate {
			return validated, fmt.Errorf("duplicate blob key %q", blob.Key)
		}
		if blob.Size < 0 || !validSHA256(blob.SHA256) {
			return validated, fmt.Errorf("blob %q metadata is invalid", blob.Key)
		}
		source, err := containedManifestFile(input, path.Join("objects", blob.Key))
		if err != nil {
			return validated, fmt.Errorf("blob %q path: %w", blob.Key, err)
		}
		validated.blobs[blob.Key] = blob
		validated.blobSources[blob.Key] = source
	}
	return validated, nil
}

func readBoundedFile(filePath string, limit int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > limit {
		return nil, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	return encoded, nil
}

func containedManifestFile(root, relative string) (string, error) {
	if err := validateRelativeManifestPath(relative); err != nil {
		return "", err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve backup root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("resolve backup root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("resolve file: %w", err)
	}
	relativeToRoot, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeToRoot) {
		return "", fmt.Errorf("path escapes backup root")
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	return resolvedPath, nil
}

func validateRelativeManifestPath(value string) error {
	converted := filepath.FromSlash(value)
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, `\`) || path.IsAbs(value) ||
		filepath.IsAbs(converted) || filepath.VolumeName(converted) != "" || hasWindowsDrivePrefix(value) {
		return fmt.Errorf("path must be relative")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || clean != value || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path must be canonical and contained")
	}
	return nil
}

func validateManifestBlobKey(key string) error {
	if err := validateRelativeManifestPath(key); err != nil {
		return fmt.Errorf("invalid blob key: %w", err)
	}
	return nil
}

func hasWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	return value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z'
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func preflightBackupDatabase(ctx context.Context, databasePath string, key []byte, manifestBlobs map[string]ManifestBlob) error {
	dsn, err := db.SQLiteURI(databasePath, url.Values{"mode": {"ro"}, "immutable": {"1"}, "_pragma": {"foreign_keys(1)"}})
	if err != nil {
		return fmt.Errorf("resolve backup database path: %w", err)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open backup database read-only: %w", err)
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("open backup database read-only: %w", err)
	}
	if err := db.IntegrityCheck(ctx, database); err != nil {
		return fmt.Errorf("backup database failed integrity validation: %w", err)
	}
	if _, err := settings.New(database, key).Load(ctx); err != nil {
		return fmt.Errorf("backup master key cannot decrypt installation settings: %w", err)
	}
	references, err := repository.New(database).ReferencedBlobs(ctx)
	if err != nil {
		return fmt.Errorf("read backup blob references: %w", err)
	}
	for key, expected := range references {
		blob, exists := manifestBlobs[key]
		if !exists {
			return fmt.Errorf("restored database references blob %q omitted from manifest", key)
		}
		if expected != "" && !strings.EqualFold(expected, blob.SHA256) {
			return fmt.Errorf("restored database checksum for blob %q does not match manifest", key)
		}
	}
	for key := range manifestBlobs {
		if _, exists := references[key]; !exists {
			return fmt.Errorf("manifest blob %q is not referenced by restored database", key)
		}
	}
	return nil
}

func verifyRestoredBlobs(ctx context.Context, repo *repository.Repository, store blobstore.Store, manifestBlobs map[string]ManifestBlob) error {
	references, err := repo.ReferencedBlobs(ctx)
	if err != nil {
		return fmt.Errorf("read restored blob references: %w", err)
	}
	referenceKeys := make([]string, 0, len(references))
	for key := range references {
		referenceKeys = append(referenceKeys, key)
	}
	sort.Strings(referenceKeys)
	for _, key := range referenceKeys {
		if _, exists := manifestBlobs[key]; !exists {
			return fmt.Errorf("restored database references blob %q omitted from manifest", key)
		}
	}
	manifestKeys := make([]string, 0, len(manifestBlobs))
	for key := range manifestBlobs {
		manifestKeys = append(manifestKeys, key)
	}
	sort.Strings(manifestKeys)
	for _, key := range manifestKeys {
		if _, exists := references[key]; !exists {
			return fmt.Errorf("manifest blob %q is not referenced by restored database", key)
		}
	}

	for _, key := range referenceKeys {
		blob := manifestBlobs[key]
		if expected := references[key]; expected != "" && !strings.EqualFold(expected, blob.SHA256) {
			return fmt.Errorf("restored database checksum for blob %q does not match manifest", key)
		}
		reader, info, err := store.Get(ctx, key)
		if err != nil || info.Size != blob.Size {
			if reader != nil {
				reader.Close()
			}
			return fmt.Errorf("restored blob %q failed integrity validation", key)
		}
		checksum, digestErr := readerDigest(ctx, reader)
		closeErr := reader.Close()
		if digestErr != nil || closeErr != nil || !strings.EqualFold(checksum, blob.SHA256) {
			return fmt.Errorf("restored blob %q failed checksum validation", key)
		}
	}
	return nil
}

func readerDigest(ctx context.Context, reader io.Reader) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if _, writeErr := hash.Write(buffer[:count]); writeErr != nil {
				return "", writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return hex.EncodeToString(hash.Sum(nil)), nil
			}
			return "", err
		}
	}
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
	if err := validateManifestBlobKey(key); err != nil {
		return fmt.Errorf("invalid manifest key")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	destination := filepath.Join(absoluteRoot, filepath.FromSlash(key))
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for directory := filepath.Dir(destination); directory != absoluteRoot; directory = filepath.Dir(directory) {
		relative, err := filepath.Rel(absoluteRoot, directory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			break
		}
		if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	return nil
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
