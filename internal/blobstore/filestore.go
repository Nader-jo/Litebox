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

// Put streams a blob into a same-filesystem temporary file before publishing it
// without replacing an existing immutable key.
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
	incomingHash := fmt.Sprintf("%x", hash.Sum(nil))
	created, err := publishNoReplace(ctx, tmpName, finalPath, os.Link)
	if err != nil {
		return BlobInfo{}, fmt.Errorf("commit blob without replacement: %w", err)
	}
	if !created {
		existing, statErr := os.Stat(finalPath)
		if statErr != nil {
			return BlobInfo{}, fmt.Errorf("inspect concurrently committed blob: %w", statErr)
		}
		existingHash, hashErr := checksumFile(finalPath)
		if hashErr != nil || existing.Size() != written || existingHash != incomingHash {
			return BlobInfo{}, fmt.Errorf("blob key collision: %s", key)
		}
		return BlobInfo{Key: key, Size: existing.Size(), SHA256: existingHash, ContentType: contentType, ModTime: existing.ModTime()}, nil
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		return BlobInfo{}, fmt.Errorf("stat committed blob: %w", err)
	}
	return BlobInfo{Key: key, Size: written, SHA256: incomingHash, ContentType: contentType, ModTime: info.ModTime()}, nil
}

func publishNoReplace(ctx context.Context, source, destination string, link func(string, string) error) (bool, error) {
	if err := link(source, destination); err == nil {
		return true, nil
	} else if _, statErr := os.Stat(destination); statErr == nil {
		_ = os.Remove(destination + ".litebox-lock")
		return false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	lockPath := destination + ".litebox-lock"
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return false, closeErr
			}
			defer func() { _ = os.Remove(lockPath) }()
			if _, statErr := os.Stat(destination); statErr == nil {
				return false, nil
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return false, statErr
			}
			if err := os.Rename(source, destination); err != nil {
				return false, err
			}
			return true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		if _, statErr := os.Stat(destination); statErr == nil {
			_ = os.Remove(lockPath)
			return false, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("timed out waiting for immutable publish lock %s; verify no writer is active before removing it", lockPath)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, ctx.Err()
		case <-timer.C:
		}
	}
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

// Health verifies that both configured storage directories can create and
// remove private files. Blob commits themselves stage beside their final path
// so the final rename remains on one filesystem.
func (s *FileStore) Health(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	for _, directory := range []string{s.root, s.tmpRoot} {
		if err := probeWritable(directory); err != nil {
			return err
		}
	}
	return nil
}

func probeWritable(directory string) error {
	file, err := os.CreateTemp(directory, ".health-*")
	if err != nil {
		return fmt.Errorf("blob storage directory %q is not writable: %w", directory, err)
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
