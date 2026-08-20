package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileStoreRoundTripAndIdempotency(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("streamed-content"), 1024)
	first, err := store.Put(context.Background(), "attachments/2026/08/id", bytes.NewReader(payload), int64(len(payload)), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(context.Background(), "attachments/2026/08/id", bytes.NewReader(payload), int64(len(payload)), "application/octet-stream")
	if err != nil || second.SHA256 != first.SHA256 {
		t.Fatalf("idempotent put failed: %#v %v", second, err)
	}
	reader, info, err := store.Get(context.Background(), first.Key)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := io.ReadAll(reader)
	reader.Close()
	if !bytes.Equal(actual, payload) || info.Size != int64(len(payload)) {
		t.Fatal("stored bytes differ")
	}
	if err := store.Delete(context.Background(), first.Key); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), first.Key); err != nil {
		t.Fatal("delete should be idempotent")
	}
}

func TestPublishNoReplaceFallsBackWhenHardLinksAreUnavailable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("immutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsupported := func(string, string) error { return errors.New("hard links unsupported") }
	created, err := publishNoReplace(context.Background(), source, destination, unsupported)
	if err != nil || !created {
		t.Fatalf("fallback created=%v err=%v", created, err)
	}
	created, err = publishNoReplace(context.Background(), source, destination, unsupported)
	if err != nil || created {
		t.Fatalf("existing fallback created=%v err=%v", created, err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "immutable" {
		t.Fatalf("fallback contents=%q err=%v", contents, err)
	}
}

func TestPublishNoReplaceFallbackSerializesConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "destination")
	unsupported := func(string, string) error { return errors.New("hard links unsupported") }
	payloads := [][]byte{[]byte("fallback one"), []byte("fallback two")}
	start := make(chan struct{})
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, len(payloads))
	for index, payload := range payloads {
		source := filepath.Join(root, fmt.Sprintf("source-%d", index))
		if err := os.WriteFile(source, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		go func() {
			<-start
			created, err := publishNoReplace(context.Background(), source, destination, unsupported)
			results <- result{created: created, err: err}
		}()
	}
	close(start)
	created := 0
	for range payloads {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created writers = %d, want 1", created)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || (!bytes.Equal(contents, payloads[0]) && !bytes.Equal(contents, payloads[1])) {
		t.Fatalf("published fallback contents=%q err=%v", contents, err)
	}
}

func TestPublishNoReplaceFallbackHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("immutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination+".litebox-lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := publishNoReplace(ctx, source, destination, func(string, string) error { return errors.New("hard links unsupported") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publish error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("canceled publish blocked for %s", elapsed)
	}
}

func TestFileStoreConcurrentWritesNeverReplaceCommittedContent(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payloads := [][]byte{[]byte("first payload"), []byte("second, different payload")}
	start := make(chan struct{})
	errorsByWriter := make([]error, len(payloads))
	var wait sync.WaitGroup
	for index, payload := range payloads {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, errorsByWriter[index] = store.Put(context.Background(), "attachments/shared", bytes.NewReader(payload), int64(len(payload)), "text/plain")
		}()
	}
	close(start)
	wait.Wait()
	successes := 0
	for _, err := range errorsByWriter {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "collision") {
			t.Fatalf("unexpected concurrent write error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected one writer to commit, got %d", successes)
	}
	reader, _, err := store.Get(context.Background(), "attachments/shared")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stored, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payloads[0]) && !bytes.Equal(stored, payloads[1]) {
		t.Fatalf("stored content was replaced or corrupted: %q", stored)
	}
}

func TestFileStoreRejectsUnsafeAndTruncatedWrites(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../escape", "/absolute", "a/../../escape", ""} {
		if _, err := store.Put(context.Background(), key, strings.NewReader("x"), 1, "text/plain"); err == nil {
			t.Errorf("expected key %q to fail", key)
		}
	}
	if _, err := store.Put(context.Background(), "safe/key", strings.NewReader("small"), 100, "text/plain"); err == nil {
		t.Fatal("expected size mismatch")
	}
	exists, err := store.Exists(context.Background(), "safe/key")
	if err != nil || exists {
		t.Fatal("partial blob became visible")
	}
}
