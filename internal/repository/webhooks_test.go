package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

func TestStoreProviderEventReducesAgainstCurrentStateTransactionally(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := repo.CreateDraft(ctx, mailboxID, model.Draft{To: []model.Address{{Address: "recipient@example.net"}}, Subject: "Status"})
	if err != nil {
		t.Fatal(err)
	}
	messageID, _, err := repo.QueueDraft(ctx, draft.ID, mailboxID, model.Address{Address: "hello@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkMessageSubmitted(ctx, messageID, "resend-status"); err != nil {
		t.Fatal(err)
	}
	inboundID, _, err := repo.SaveInbound(ctx, InboundMessage{MailboxID: mailboxID, ResendEmailID: "resend-status",
		From: model.Address{Address: "sender@example.net"}, Recipients: map[string][]model.Address{"to": {{Address: "hello@example.com"}}},
		Subject: "Unrelated inbound", ReceivedAt: time.Now(), IngestStatus: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, "UPDATE messages SET delivery_status = 'inbound-sentinel' WHERE id = ?", inboundID); err != nil {
		t.Fatal(err)
	}
	newer := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)
	if err := repo.StoreProviderEvent(ctx, "svix-delivered", "resend-status", "email.delivered", `{}`, newer); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreProviderEvent(ctx, "svix-sent", "resend-status", "email.sent", `{}`, older); err != nil {
		t.Fatal(err)
	}
	var status string
	var occurred int64
	if err := repo.db.QueryRowContext(ctx, "SELECT delivery_status, last_provider_event_at FROM messages WHERE id = ?", messageID).Scan(&status, &occurred); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || occurred != newer.UnixMilli() {
		t.Fatalf("delivery state regressed: status=%q occurred=%d", status, occurred)
	}
	if err := repo.db.QueryRowContext(ctx, "SELECT delivery_status FROM messages WHERE id = ?", inboundID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "inbound-sentinel" {
		t.Fatalf("inbound collision was mutated: status=%q", status)
	}
}

func TestPersistWebhookMarksDifferentDeliveryOfSameEmailDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	now := time.Now().UTC()
	for index, svixID := range []string{"svix-first", "svix-retry"} {
		event := model.WebhookEvent{ID: fmt.Sprintf("event-%d", index), SvixID: svixID, EventType: "email.received",
			ResendEmailID: "same-provider-email", RawPayload: `{}`, ReceivedAt: now.Add(time.Duration(index) * time.Second)}
		inserted, err := repo.PersistWebhook(ctx, event, now, "ingest_inbound", "ingest:same-provider-email",
			map[string]string{"webhook_event_id": event.ID, "resend_email_id": event.ResendEmailID})
		if err != nil || !inserted {
			t.Fatalf("persist delivery %d inserted=%v err=%v", index, inserted, err)
		}
	}
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM jobs WHERE dedupe_key = 'ingest:same-provider-email'")
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM webhook_events WHERE svix_id = 'svix-retry' AND processing_status = 'duplicate' AND processed_at IS NOT NULL")
}
