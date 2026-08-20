package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

func TestAddDraftAttachmentWithinLimitsIsAtomic(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := repo.CreateDraft(ctx, mailboxID, model.Draft{Subject: "Attachments"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, id := range []string{"attachment-a", "attachment-b"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- repo.AddDraftAttachmentWithinLimits(ctx, model.Attachment{ID: id, DraftID: draft.ID, Filename: id,
				SafeFilename: id, ContentType: "text/plain", StorageBackend: "filesystem", StorageKey: "drafts/" + draft.ID + "/" + id,
				SizeBytes: 6, SHA256: "digest", CreatedAt: time.Now().UTC()}, 1, 10)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAttachmentLimit):
			limited++
		default:
			t.Fatalf("unexpected reservation error: %v", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("succeeded=%d limited=%d", succeeded, limited)
	}
}
