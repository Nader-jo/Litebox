package blobstore

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
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
