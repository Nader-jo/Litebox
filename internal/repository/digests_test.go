package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

func TestDigestSubscriptionAndCountsRespectMailboxScope(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, _, err := repo.SaveInbound(ctx, InboundMessage{MailboxID: mailboxID, MailboxAddressID: "", ResendEmailID: "digest-received", From: model.Address{Address: "sender@example.net"}, Recipients: map[string][]model.Address{"to": {{Address: "hello@example.com"}}}, Subject: "Hello", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	sub := model.DigestSubscription{UserID: user.ID, RecipientEmail: "private@example.net", Frequency: "daily", Timezone: "UTC", SendHour: 8, MailboxScope: "all", Enabled: true}
	if err := repo.SaveDigestSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetDigestSubscription(ctx, user.ID)
	if err != nil || loaded.RecipientEmail != sub.RecipientEmail {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	counts, err := repo.DigestCounts(ctx, loaded, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if counts.Received != 1 || counts.Unread != 1 || len(counts.ByMailbox) != 1 {
		t.Fatalf("counts=%+v", counts)
	}
	if err := repo.MarkDigestSent(ctx, loaded.ID, now); err != nil {
		t.Fatal(err)
	}
}

func TestInvitationAndPasswordResetTokensAreSingleUse(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := repo.CreateInvitation(ctx, owner.ID, mailboxID, "member@example.com", "Member", "member", []byte("invitation-hash"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if invitation.Email != "member@example.com" {
		t.Fatalf("unexpected invitation: %+v", invitation)
	}
	if _, err := repo.AcceptInvitation(ctx, []byte("invitation-hash"), "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcceptInvitation(ctx, []byte("invitation-hash"), "new-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected consumed invitation, got %v", err)
	}
	user, err := repo.FindUserByEmail(ctx, "member@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePasswordResetForUser(ctx, user.ID, []byte("reset-hash"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumePasswordReset(ctx, []byte("reset-hash"), "replacement-hash"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumePasswordReset(ctx, []byte("reset-hash"), "replacement-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected consumed reset token, got %v", err)
	}
}
