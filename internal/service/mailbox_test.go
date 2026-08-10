package service

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
)

type fakeProvider struct {
	received  provider.ReceivedEmail
	download  map[string][]byte
	sent      provider.SendRequest
	sendCount int
}

func (f *fakeProvider) VerifyWebhook([]byte, provider.WebhookHeaders) error { return nil }
func (f *fakeProvider) GetReceived(context.Context, string) (provider.ReceivedEmail, error) {
	return f.received, nil
}
func (f *fakeProvider) GetReceivedAttachment(_ context.Context, _, id string) (provider.ReceivedAttachment, error) {
	for _, attachment := range f.received.Attachments {
		if attachment.ID == id {
			attachment.DownloadURL = "memory://" + id
			return attachment, nil
		}
	}
	return provider.ReceivedAttachment{}, io.EOF
}
func (f *fakeProvider) Download(_ context.Context, location string) (io.ReadCloser, int64, error) {
	value := f.download[location]
	return io.NopCloser(bytes.NewReader(value)), int64(len(value)), nil
}
func (f *fakeProvider) Send(_ context.Context, request provider.SendRequest) (string, error) {
	f.sent = request
	f.sendCount++
	return "sent-provider-id", nil
}

func TestInboundArchivalAndOutboundIdempotency(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base, _ := url.Parse("http://localhost:8080")
	cfg := config.Config{Environment: "test", BaseURL: base, ListenAddr: ":8080", DataDir: root,
		DBPath: filepath.Join(root, "mailbox.db"), CookieName: "session", SessionTTL: time.Hour,
		PrimaryAddress: "hello@example.com", DisplayName: "Example", AllowedRecipients: map[string]struct{}{"hello@example.com": {}},
		StorageBackend: "filesystem", StorageRoot: filepath.Join(root, "objects"), StorageTempRoot: filepath.Join(root, "tmp"),
		MaxMessageTextBytes: 5 << 20, MaxOutboundAttachmentBytes: 25 << 20, MaxAttachmentCount: 20,
		MaxWebhookBodyBytes: 1 << 20, MaxUploadRequestBytes: 30 << 20, WorkerCount: 1, JobPollInterval: time.Second, JobLease: time.Minute}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	mailboxID, _ := repo.EnsureMailbox(ctx, cfg.PrimaryAddress, cfg.DisplayName)
	store, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeProvider{received: provider.ReceivedEmail{ID: "received-1", To: []string{"hello@example.com"},
		From: "alice@example.com", CreatedAt: time.Now(), Subject: "Tracking test", Text: "Plain copy",
		HTML: `<p>Hello</p><img src="https://tracker.example/pixel"><script>bad()</script>`, MessageID: "<received-1@example.com>",
		RawURL: "memory://raw", Headers: map[string]string{"from": "Alice <alice@example.com>"},
		Attachments: []provider.ReceivedAttachment{{ID: "attachment-1", Filename: "note.txt", ContentType: "text/plain"}}},
		download: map[string][]byte{"memory://raw": []byte("raw eml"), "memory://attachment-1": []byte("attachment")}}
	service := NewMailbox(cfg, repo, store, fake, mailboxID)
	eventID := ids.New()
	event := model.WebhookEvent{ID: eventID, SvixID: "svix-1", EventType: "email.received", ResendEmailID: "received-1", RawPayload: `{}`, ReceivedAt: time.Now()}
	_, err = repo.PersistWebhook(ctx, event, time.Now(), "ingest_inbound", "ingest:received-1", map[string]string{"webhook_event_id": eventID, "resend_email_id": "received-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ingest(ctx, eventID, "received-1", false); err != nil {
		t.Fatal(err)
	}
	threads, err := repo.ListThreads(ctx, "inbox", 10)
	if err != nil || len(threads) != 1 || !threads[0].HasAttachments {
		t.Fatal(threads, err)
	}
	thread, err := repo.ThreadByID(ctx, threads[0].ID)
	if err != nil || len(thread.Messages) != 1 || !thread.Messages[0].RemoteImagesBlocked || len(thread.Messages[0].Attachments) != 1 {
		t.Fatal(thread, err)
	}
	if exists, _ := store.Exists(ctx, thread.Messages[0].RawStorageKey); !exists {
		t.Fatal("raw email was not archived")
	}

	fake.received = provider.ReceivedEmail{ID: "received-without-raw", To: []string{"hello@example.com"},
		From: "bob@example.com", CreatedAt: time.Now().Add(time.Minute), Subject: "Provider raw gap", Text: "Readable body",
		MessageID: "<received-without-raw@example.com>", Headers: map[string]string{"from": "Bob <bob@example.com>"}}
	missingRawEventID := ids.New()
	missingRawEvent := model.WebhookEvent{ID: missingRawEventID, SvixID: "svix-without-raw", EventType: "email.received",
		ResendEmailID: fake.received.ID, RawPayload: `{}`, ReceivedAt: time.Now()}
	if _, err := repo.PersistWebhook(ctx, missingRawEvent, time.Now(), "ingest_inbound", "ingest:"+fake.received.ID,
		map[string]string{"webhook_event_id": missingRawEventID, "resend_email_id": fake.received.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.Ingest(ctx, missingRawEventID, fake.received.ID, false); err == nil {
		t.Fatal("missing raw URL should remain retryable before the final attempt")
	}
	if err := service.Ingest(ctx, missingRawEventID, fake.received.ID, true); err != nil {
		t.Fatal(err)
	}
	stats, err := repo.SystemStats(ctx, cfg.DBPath)
	if err != nil || stats.MissingRawMessages != 1 {
		t.Fatalf("missing raw diagnostic=%d err=%v", stats.MissingRawMessages, err)
	}

	draft, err := repo.CreateDraft(ctx, mailboxID, model.Draft{To: []model.Address{{Address: "alice@example.com"}}, Subject: "Outbound", TextBody: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	draftKey := "drafts/" + draft.ID + "/outbound-attachment"
	attachmentBytes := []byte("bounded outbound attachment")
	attachmentInfo, err := store.Put(ctx, draftKey, bytes.NewReader(attachmentBytes), int64(len(attachmentBytes)), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddDraftAttachment(ctx, model.Attachment{ID: ids.New(), DraftID: draft.ID, Filename: "note.txt", SafeFilename: "note.txt",
		ContentType: "text/plain", StorageBackend: "filesystem", StorageKey: draftKey, SizeBytes: attachmentInfo.Size,
		SHA256: attachmentInfo.SHA256, StorageStatus: "ready", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	messageID, _, err := repo.QueueDraft(ctx, draft.ID, mailboxID, model.Address{Name: "Example", Address: cfg.PrimaryAddress})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Send(ctx, messageID); err != nil {
		t.Fatal(err)
	}
	if fake.sent.IdempotencyKey != "litebox-send/"+messageID {
		t.Fatalf("unexpected idempotency key %q", fake.sent.IdempotencyKey)
	}
	if len(fake.sent.Attachments) != 1 || !bytes.Equal(fake.sent.Attachments[0].Content, attachmentBytes) {
		t.Fatalf("outbound attachment was not submitted: %#v", fake.sent.Attachments)
	}
	if err := service.Send(ctx, messageID); err != nil {
		t.Fatal(err)
	}
	if fake.sendCount != 1 {
		t.Fatalf("logical message was submitted %d times", fake.sendCount)
	}
}
