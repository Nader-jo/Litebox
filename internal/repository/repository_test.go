package repository

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/auth"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/search"
)

func TestMailboxMembershipsAliasesSessionsAndIsolation(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	primaryID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := repo.CreateMailbox(ctx, owner.ID, "billing@example.com", "Billing")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddMailboxAddress(ctx, secondary.ID, "invoices@example.com", "Billing team"); err != nil {
		t.Fatal(err)
	}
	mailboxes, err := repo.ListMailboxesForUser(ctx, owner.ID)
	if err != nil || len(mailboxes) != 2 {
		t.Fatalf("owner mailboxes=%#v err=%v", mailboxes, err)
	}
	routed, err := repo.MailboxesForRecipients(ctx, []model.Address{{Address: "hello@example.com"}, {Address: "invoices@example.com"}})
	if err != nil || len(routed) != 2 {
		t.Fatalf("routed mailboxes=%#v err=%v", routed, err)
	}

	_, primaryThread, err := repo.SaveInbound(ctx, InboundMessage{MailboxID: primaryID, ResendEmailID: "shared-provider-id",
		RFCMessageID: "<shared@example.com>", From: model.Address{Address: "sender@example.net"},
		Recipients: map[string][]model.Address{"to": {{Address: "hello@example.com"}}}, Subject: "Primary only", ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_, secondaryThread, err := repo.SaveInbound(ctx, InboundMessage{MailboxID: secondary.ID, ResendEmailID: "shared-provider-id",
		RFCMessageID: "<shared@example.com>", From: model.Address{Address: "sender@example.net"},
		Recipients: map[string][]model.Address{"to": {{Address: "billing@example.com"}}}, Subject: "Billing only", ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal("same provider message should be storable in two mailboxes:", err)
	}
	if _, err := repo.ThreadByID(ctx, primaryID, secondaryThread); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox thread read returned %v", err)
	}
	if _, err := repo.ThreadByID(ctx, secondary.ID, primaryThread); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reverse cross-mailbox thread read returned %v", err)
	}
	if err := repo.ThreadAction(ctx, primaryID, secondaryThread, "archive"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox mutation returned %v", err)
	}

	member, err := repo.CreateUserWithMembership(ctx, owner.ID, primaryID, "admin@example.com", "Admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	memberMailboxes, err := repo.ListMailboxesForUser(ctx, member.ID)
	if err != nil || len(memberMailboxes) != 1 || memberMailboxes[0].ID != primaryID {
		t.Fatalf("member access=%#v err=%v", memberMailboxes, err)
	}
	if err := repo.GrantMailboxAccess(ctx, owner.ID, secondary.ID, member.Email, "viewer"); err != nil {
		t.Fatal(err)
	}
	memberMailboxes, _ = repo.ListMailboxesForUser(ctx, member.ID)
	if len(memberMailboxes) != 2 {
		t.Fatalf("expected two assigned mailboxes, got %#v", memberMailboxes)
	}

	for index := 0; index < 2; index++ {
		if _, err := repo.CreateSession(ctx, owner.ID, auth.TokenHash(fmt.Sprintf("session-%d", index)),
			auth.TokenHash(fmt.Sprintf("csrf-%d", index)), time.Now().Add(time.Hour), nil, fmt.Sprintf("browser-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := repo.ListSessions(ctx, owner.ID)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if err := repo.DeleteUserSession(ctx, owner.ID, sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	sessions, _ = repo.ListSessions(ctx, owner.ID)
	if len(sessions) != 1 {
		t.Fatalf("expected one remaining session, got %d", len(sessions))
	}
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return New(database)
}

func TestAccountsSessionsWebhooksAndJobs(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil || mailboxID == "" {
		t.Fatal(mailboxID, err)
	}
	user, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateFirstUser(ctx, "second@example.com", "Second", "hash"); err == nil {
		t.Fatal("expected second first-run user to fail")
	}
	rawToken := "raw-session-token"
	csrf := "csrf-secret"
	if _, err := repo.CreateSession(ctx, user.ID, auth.TokenHash(rawToken), auth.TokenHash(csrf), time.Now().Add(time.Hour), nil, "test"); err != nil {
		t.Fatal(err)
	}
	session, err := repo.FindSession(ctx, auth.TokenHash(rawToken))
	if err != nil || session.User.ID != user.ID {
		t.Fatal(session, err)
	}
	event := model.WebhookEvent{ID: ids.New(), SvixID: "msg_test", EventType: "email.received", ResendEmailID: "received-1", RawPayload: `{}`, ReceivedAt: time.Now()}
	payload := map[string]string{"webhook_event_id": event.ID, "resend_email_id": event.ResendEmailID}
	inserted, err := repo.PersistWebhook(ctx, event, time.Now(), "ingest_inbound", "ingest:received-1", payload)
	if err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	inserted, err = repo.PersistWebhook(ctx, event, time.Now(), "ingest_inbound", "ingest:received-1", payload)
	if err != nil || inserted {
		t.Fatal("duplicate webhook was not acknowledged idempotently", inserted, err)
	}
	job, err := repo.ClaimJob(ctx, "worker", time.Minute)
	if err != nil || job.Kind != "ingest_inbound" || job.AttemptCount != 1 {
		t.Fatal(job, err)
	}
	if err := repo.FailJob(ctx, job.ID, "safe failure", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.ClaimJob(ctx, "worker", time.Minute)
	if err != nil || retry.ID != job.ID || retry.AttemptCount != 2 {
		t.Fatal(retry, err)
	}
}

func TestInboundDraftSearchAndFolderState(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	input := InboundMessage{MailboxID: mailboxID, ResendEmailID: "received-2", RFCMessageID: "<one@example.com>",
		From:       model.Address{Name: "Alice", Address: "alice@example.com"},
		Recipients: map[string][]model.Address{"to": {{Address: "hello@example.com"}}}, Subject: "Invoice renewal",
		TextBody: "The renewal total is 42.", ReceivedAt: time.Now(), IngestStatus: "ready"}
	messageID, threadID, err := repo.SaveInbound(ctx, input)
	if err != nil || messageID == "" || threadID == "" {
		t.Fatal(messageID, threadID, err)
	}
	threads, err := repo.ListThreads(ctx, mailboxID, "inbox", 10)
	if err != nil || len(threads) != 1 || threads[0].UnreadCount != 1 {
		t.Fatal(threads, err)
	}
	query, _ := search.Parse("renewal from:alice@example.com is:unread")
	results, err := repo.SearchThreads(ctx, mailboxID, query, 10)
	if err != nil || len(results) != 1 || results[0].ID != threadID {
		t.Fatal(results, err)
	}
	if err := repo.MarkThreadRead(ctx, mailboxID, threadID, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.ThreadAction(ctx, mailboxID, threadID, "archive"); err != nil {
		t.Fatal(err)
	}
	archived, err := repo.ListThreads(ctx, mailboxID, "archive", 10)
	if err != nil || len(archived) != 1 {
		t.Fatal(archived, err)
	}
	draft, err := repo.CreateDraft(ctx, mailboxID, model.Draft{ThreadID: threadID, ReplyToMessageID: messageID,
		To: []model.Address{{Address: "alice@example.com"}}, Subject: "Re: Invoice renewal", TextBody: "Thanks"})
	if err != nil {
		t.Fatal(err)
	}
	outboundID, outboundThreadID, err := repo.QueueDraft(ctx, draft.ID, mailboxID, model.Address{Name: "Example", Address: "hello@example.com"})
	if err != nil || outboundThreadID != threadID {
		t.Fatal(outboundID, outboundThreadID, err)
	}
	outbound, err := repo.OutboundMessage(ctx, outboundID)
	if err != nil || outbound.InReplyTo != "<one@example.com>" || outbound.DeliveryStatus != "queued" {
		t.Fatal(outbound, err)
	}
}

func TestThreadAndSearchKeysetPagination(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	for index := 0; index < 3; index++ {
		_, _, err := repo.SaveInbound(ctx, InboundMessage{
			MailboxID:     mailboxID,
			ResendEmailID: "page-message-" + string(rune('a'+index)),
			RFCMessageID:  "<page-" + string(rune('a'+index)) + "@example.com>",
			From:          model.Address{Name: "Pagination", Address: "pages@example.com"},
			Recipients:    map[string][]model.Address{"to": {{Address: "hello@example.com"}}},
			Subject:       "Page " + string(rune('A'+index)),
			TextBody:      "pageable content",
			ReceivedAt:    base.Add(time.Duration(index) * time.Minute),
			IngestStatus:  "ready",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	first, more, err := repo.ListThreadsPage(ctx, mailboxID, "inbox", 2, time.Time{}, "")
	if err != nil || !more || len(first) != 2 {
		t.Fatalf("first page: len=%d more=%v err=%v", len(first), more, err)
	}
	second, more, err := repo.ListThreadsPage(ctx, mailboxID, "inbox", 2, first[1].LatestMessageAt, first[1].ID)
	if err != nil || more || len(second) != 1 || second[0].ID == first[0].ID || second[0].ID == first[1].ID {
		t.Fatalf("second page: %#v more=%v err=%v", second, more, err)
	}

	parsed, err := search.Parse("pageable")
	if err != nil {
		t.Fatal(err)
	}
	searchFirst, more, err := repo.SearchThreadsPage(ctx, mailboxID, parsed, 2, time.Time{}, "")
	if err != nil || !more || len(searchFirst) != 2 {
		t.Fatalf("first search page: len=%d more=%v err=%v", len(searchFirst), more, err)
	}
	searchSecond, more, err := repo.SearchThreadsPage(ctx, mailboxID, parsed, 2, searchFirst[1].LatestMessageAt, searchFirst[1].ID)
	if err != nil || more || len(searchSecond) != 1 {
		t.Fatalf("second search page: len=%d more=%v err=%v", len(searchSecond), more, err)
	}
}

func TestExpiredJobLeaseIsRecovered(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	id, inserted, err := repo.EnqueueJob(ctx, "test", "lease:test", `{}`, 100, 3, time.Now().Add(-time.Second))
	if err != nil || !inserted {
		t.Fatal(id, inserted, err)
	}
	claimed, err := repo.ClaimJob(ctx, "crashed-worker", -time.Second)
	if err != nil || claimed.ID != id || claimed.AttemptCount != 1 {
		t.Fatal(claimed, err)
	}
	reclaimed, err := repo.ClaimJob(ctx, "replacement-worker", time.Minute)
	if err != nil || reclaimed.ID != id || reclaimed.AttemptCount != 2 || reclaimed.LeaseOwner != "replacement-worker" {
		t.Fatalf("expired job was not reclaimed: %#v err=%v", reclaimed, err)
	}
}
