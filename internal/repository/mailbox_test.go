package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/search"
)

func TestInboxRequiresInboundMessageAndKeepsMixedThreads(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}

	queue := func(subject string) string {
		t.Helper()
		draft, err := repo.CreateDraft(ctx, mailboxID, model.Draft{
			To: []model.Address{{Address: "recipient@example.net"}}, Subject: subject, TextBody: "Outbound body",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, threadID, err := repo.QueueDraft(ctx, draft.ID, mailboxID, model.Address{Address: "hello@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		return threadID
	}

	outboundOnlyID := queue("Outbound only")
	mixedID := queue("Mixed conversation")
	if _, _, err := repo.SaveInbound(ctx, InboundMessage{
		MailboxID:     mailboxID,
		ThreadID:      mixedID,
		ResendEmailID: "inbound-mixed",
		RFCMessageID:  "<inbound-mixed@example.net>",
		From:          model.Address{Address: "recipient@example.net"},
		Recipients:    map[string][]model.Address{"to": {{Address: "hello@example.com"}}},
		Subject:       "Re: Mixed conversation",
		TextBody:      "Inbound reply",
		ReceivedAt:    time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	inbox, err := repo.ListThreads(ctx, mailboxID, "inbox", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != mixedID {
		t.Fatalf("inbox = %#v, want only mixed thread %q", inbox, mixedID)
	}
	defaultFolder, err := repo.ListThreads(ctx, mailboxID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultFolder) != 1 || defaultFolder[0].ID != mixedID {
		t.Fatalf("default folder = %#v, want only mixed thread %q", defaultFolder, mixedID)
	}
	sent, err := repo.ListThreads(ctx, mailboxID, "sent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || !containsThread(sent, outboundOnlyID) || !containsThread(sent, mixedID) {
		t.Fatalf("sent = %#v, want outbound-only and mixed threads", sent)
	}
}

func TestSearchThreadsPageOrdersEqualTimestampsByIDDescending(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	activity := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	identifiers := []string{"thread-search-a", "thread-search-b", "thread-search-c"}
	for index, threadID := range identifiers {
		if _, err := repo.DB().ExecContext(ctx, `INSERT INTO threads
			(id, mailbox_id, subject_norm, subject_display, latest_message_at, first_message_at,
			 message_count, unread_count, created_at, updated_at)
			VALUES (?, ?, 'search tie', 'Search tie', ?, ?, 0, 0, ?, ?)`,
			threadID, mailboxID, millis(activity), millis(activity), millis(activity), millis(activity)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.SaveInbound(ctx, InboundMessage{
			MailboxID:     mailboxID,
			ThreadID:      threadID,
			ResendEmailID: "search-tie-" + threadID,
			RFCMessageID:  "<search-tie-" + threadID + "@example.net>",
			From:          model.Address{Address: "sender@example.net"},
			Recipients:    map[string][]model.Address{"to": {{Address: "hello@example.com"}}},
			Subject:       "Search tie",
			TextBody:      "pagination needle " + string(rune('a'+index)),
			ReceivedAt:    activity,
		}); err != nil {
			t.Fatal(err)
		}
	}

	query, err := search.Parse("pagination needle")
	if err != nil {
		t.Fatal(err)
	}
	first, more, err := repo.SearchThreadsPage(ctx, mailboxID, query, 2, time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(first) != 2 || first[0].ID != identifiers[2] || first[1].ID != identifiers[1] {
		t.Fatalf("first page = %#v more=%v, want c then b", first, more)
	}
	second, more, err := repo.SearchThreadsPage(ctx, mailboxID, query, 2, first[1].LatestMessageAt, first[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(second) != 1 || second[0].ID != identifiers[0] {
		t.Fatalf("second page = %#v more=%v, want a", second, more)
	}
}

func containsThread(threads []model.ThreadSummary, id string) bool {
	for _, thread := range threads {
		if thread.ID == id {
			return true
		}
	}
	return false
}
