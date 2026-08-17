package repository

import (
	"context"
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
