package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/jobs"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/repository"
)

func TestSendDigestRejectsDisabledSubscriptionPermanently(t *testing.T) {
	ctx := context.Background()
	mailbox, repo, fake, sub := digestTestMailbox(t, false, "UTC")

	err := mailbox.SendDigest(ctx, sub.ID, time.Now().Add(-24*time.Hour), time.Now())
	var permanent *jobs.PermanentError
	if !errors.As(err, &permanent) || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled digest error = %v, want clear permanent error", err)
	}
	if fake.sendCount != 0 {
		t.Fatalf("disabled digest sent %d messages", fake.sendCount)
	}
	loaded, err := repo.GetDigestSubscription(ctx, sub.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastAttemptAt != nil || loaded.LastSentAt != nil {
		t.Fatalf("disabled digest recorded delivery state: %+v", loaded)
	}
}

func TestSendDigestFormatsPeriodInSubscriptionTimezone(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = originalLocal })

	ctx := context.Background()
	mailbox, _, fake, sub := digestTestMailbox(t, true, "Asia/Tokyo")
	since := time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	if err := mailbox.SendDigest(ctx, sub.ID, since, until); err != nil {
		t.Fatal(err)
	}
	wantPeriod := "Period: Jan 15, 2026 09:00 – Jan 15, 2026 10:00"
	if !strings.Contains(fake.sent.Text, wantPeriod) {
		t.Fatalf("digest body did not use subscription timezone:\n%s\nwant %q", fake.sent.Text, wantPeriod)
	}
}

func digestTestMailbox(t *testing.T, enabled bool, timezone string) (*Mailbox, *repository.Repository, *fakeProvider, model.DigestSubscription) {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	if _, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example"); err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	sub := model.DigestSubscription{
		ID:             "digest-test",
		UserID:         user.ID,
		RecipientEmail: "owner@example.com",
		Frequency:      "daily",
		Timezone:       timezone,
		SendHour:       8,
		MailboxScope:   "all",
		Enabled:        enabled,
	}
	if err := repo.SaveDigestSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProvider{}
	return NewMailbox(config.Config{}, repo, nil, fake), repo, fake, sub
}
