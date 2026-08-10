package blobstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileStore persists blobs beneath one private filesystem root using atomic writes.
type FileStore struct {
	root    string
	tmpRoot string
}

// NewFileStore creates and validates a filesystem-backed store.
func NewFileStore(root, tmpRoot string) (*FileStore, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve storage root: %w", err)
	}
	absoluteTemp, err := filepath.Abs(tmpRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve storage temp root: %w", err)
	}
	for _, dir := range []string{absoluteRoot, absoluteTemp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create storage directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil && !errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("secure storage directory: %w", err)
		}
	}
	store := &FileStore{root: absoluteRoot, tmpRoot: absoluteTemp}
	if err := store.Health(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

// Put streams a blob into a same-filesystem temporary file before atomic rename.
func (s *FileStore) Put(ctx context.Context, key string, reader io.Reader, expectedSize int64, contentType string) (BlobInfo, error) {
	finalPath, err := s.path(key)
	if err != nil {
		return BlobInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return BlobInfo{}, fmt.Errorf("create blob directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(finalPath), ".litebox-*")
	if err != nil {
		return BlobInfo{}, fmt.Errorf("create blob temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return BlobInfo{}, fmt.Errorf("secure blob temp file: %w", err)
	}
	hash := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(tmp, hash), reader)
	if err != nil {
		return BlobInfo{}, fmt.Errorf("stream blob: %w", err)
	}
	if expectedSize >= 0 && written != expectedSize {
		return BlobInfo{}, fmt.Errorf("blob size mismatch: expected %d bytes, received %d", expectedSize, written)
	}
	if err := tmp.Sync(); err != nil {
		return BlobInfo{}, fmt.Errorf("sync blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return BlobInfo{}, fmt.Errorf("close blob: %w", err)
	}
	if existing, err := os.Stat(finalPath); err == nil {
		existingHash, hashErr := checksumFile(finalPath)
		incomingHash := fmt.Sprintf("%x", hash.Sum(nil))
		if hashErr != nil || existing.Size() != written || existingHash != incomingHash {
			return BlobInfo{}, fmt.Errorf("blob key collision: %s", key)
		}
		return BlobInfo{Key: key, Size: existing.Size(), SHA256: existingHash, ContentType: contentType, ModTime: existing.ModTime()}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return BlobInfo{}, fmt.Errorf("inspect blob target: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return BlobInfo{}, fmt.Errorf("commit blob: %w", err)
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		return BlobInfo{}, fmt.Errorf("stat committed blob: %w", err)
	}
	return BlobInfo{Key: key, Size: written, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), ContentType: contentType, ModTime: info.ModTime()}, nil
}

func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Get opens a blob for streaming.
func (s *FileStore) Get(_ context.Context, key string) (io.ReadCloser, BlobInfo, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, BlobInfo{}, err
	}
	return file, BlobInfo{Key: key, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Delete idempotently removes a blob.
func (s *FileStore) Delete(_ context.Context, key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

// Exists reports whether a blob exists.
func (s *FileStore) Exists(_ context.Context, key string) (bool, error) {
	path, err := s.path(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Health verifies that the storage root can durably create and remove a file.
func (s *FileStore) Health(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	file, err := os.CreateTemp(s.root, ".health-*")
	if err != nil {
		return fmt.Errorf("blob storage is not writable: %w", err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("clean blob health probe: %w", err)
	}
	return nil
}

// Root returns the resolved private root for operator diagnostics.
func (s *FileStore) Root() string { return s.root }

func (s *FileStore) path(key string) (string, error) {
	if key == "" || filepath.IsAbs(key) || strings.HasPrefix(key, "/") || strings.HasPrefix(key, "\\") || strings.ContainsRune(key, '\x00') {
		return "", fmt.Errorf("invalid blob key")
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("blob key escapes storage root")
	}
	path := filepath.Join(s.root, clean)
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("blob key escapes storage root")
	}
	return path, nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// Key constructs an application-controlled immutable storage key.
func Key(kind, id string, at time.Time) string {
	switch kind {
	case "raw":
		return fmt.Sprintf("raw/%04d/%02d/%s.eml", at.UTC().Year(), int(at.UTC().Month()), id)
	case "attachments":
		return fmt.Sprintf("attachments/%04d/%02d/%s", at.UTC().Year(), int(at.UTC().Month()), id)
	default:
		return filepath.ToSlash(filepath.Join(kind, id))
	}
}
