package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
)

func TestQueueDraftConcurrentExactlyOnce(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := repo.CreateDraft(ctx, mailboxID, model.Draft{
		To:       []model.Address{{Address: "recipient@example.net"}},
		Subject:  "Exactly once",
		TextBody: "One durable send",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Hold SQLite's writer lock until both QueueDraft calls have acquired their
	// own transaction connections and are blocked at the claim statement. This
	// makes the two-submitter race deterministic instead of scheduler-dependent.
	blocker, err := repo.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, "UPDATE drafts SET updated_at = updated_at WHERE id = ?", draft.ID); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}

	type queueResult struct {
		messageID string
		threadID  string
		err       error
	}
	start := make(chan struct{})
	results := make(chan queueResult, 2)
	for range 2 {
		go func() {
			<-start
			messageID, threadID, err := repo.QueueDraft(ctx, draft.ID, mailboxID, model.Address{Address: "hello@example.com"})
			results <- queueResult{messageID: messageID, threadID: threadID, err: err}
		}()
	}
	close(start)
	if !waitForInUseConnections(repo.DB(), 3, 2*time.Second) {
		_ = blocker.Rollback()
		t.Fatal("concurrent draft submissions did not reach the database claim")
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}

	var succeeded, notFound int
	var sentMessageID string
	for range 2 {
		select {
		case result := <-results:
			switch {
			case result.err == nil:
				succeeded++
				sentMessageID = result.messageID
				if result.messageID == "" || result.threadID == "" {
					t.Fatalf("successful queue returned empty IDs: %+v", result)
				}
			case errors.Is(result.err, ErrNotFound):
				notFound++
				if result.messageID != "" || result.threadID != "" {
					t.Fatalf("losing queue returned IDs: %+v", result)
				}
			default:
				t.Fatalf("unexpected queue error: %v", result.err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("timed out waiting for concurrent draft submissions")
		}
	}
	if succeeded != 1 || notFound != 1 {
		t.Fatalf("queue outcomes: succeeded=%d not_found=%d", succeeded, notFound)
	}

	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM messages WHERE id = ? AND direction = 'outbound'", sentMessageID)
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM messages WHERE mailbox_id = ? AND direction = 'outbound'", mailboxID)
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM jobs WHERE kind = 'send_outbound'")
	assertRowCount(t, repo.DB(), 0, "SELECT COUNT(*) FROM drafts WHERE id = ?", draft.ID)
}

func TestCreateFirstUserConcurrentExactlyOnce(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	if _, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := range 2 {
		go func() {
			<-start
			_, err := repo.CreateFirstUser(ctx, fmt.Sprintf("owner-%d@example.com", index), "Owner", "password-hash")
			results <- err
		}()
	}
	close(start)
	var succeeded, rejected int
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if strings.Contains(err.Error(), "setup is already complete") {
			rejected++
		} else {
			t.Fatalf("concurrent first-user creation: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("first-user outcomes: succeeded=%d rejected=%d", succeeded, rejected)
	}
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM users")
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM mailbox_memberships WHERE role = 'owner'")
}

func TestPasswordResetIssuanceIsTransactionalAndConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	repo, user := resetTestRepository(t)
	hashes := [][]byte{[]byte("concurrent-reset-one"), []byte("concurrent-reset-two")}

	start := make(chan struct{})
	errorsByCall := make(chan error, len(hashes))
	var ready sync.WaitGroup
	ready.Add(len(hashes))
	for _, tokenHash := range hashes {
		tokenHash := tokenHash
		go func() {
			ready.Done()
			<-start
			errorsByCall <- repo.CreatePasswordResetForUser(ctx, user.ID, tokenHash, time.Now().Add(time.Hour))
		}()
	}
	ready.Wait()
	close(start)
	for range hashes {
		if err := <-errorsByCall; err != nil {
			t.Fatalf("concurrent reset issuance: %v", err)
		}
	}
	assertRowCount(t, repo.DB(), 1, `SELECT COUNT(*) FROM account_tokens
		WHERE token_type = 'password_reset' AND user_id = ? AND used_at IS NULL`, user.ID)

	var validHash []byte
	for _, tokenHash := range hashes {
		_, err := repo.UserForPasswordReset(ctx, tokenHash)
		if err == nil {
			validHash = tokenHash
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("read reset token: %v", err)
		}
	}
	if validHash == nil {
		t.Fatal("no valid reset token remained after concurrent issuance")
	}

	// Reusing the stored hash makes the insert fail its uniqueness constraint.
	// The preceding invalidation must roll back with that failed insert.
	if err := repo.CreatePasswordResetForUser(ctx, user.ID, validHash, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("expected duplicate token hash to fail")
	}
	if _, err := repo.UserForPasswordReset(ctx, validHash); err != nil {
		t.Fatalf("failed issuance invalidated the existing token: %v", err)
	}
}

func TestAcceptInvitationConcurrentReplayHasOneDeterministicWinner(t *testing.T) {
	ctx := context.Background()
	repo := testRepository(t)
	mailboxID, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "owner-hash")
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := []byte("single-use-invitation")
	if _, err := repo.CreateInvitation(ctx, owner.ID, mailboxID, "invitee@example.com", "Invitee", "member", tokenHash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := repo.AcceptInvitation(ctx, tokenHash, "invitee-hash")
			results <- err
		}()
	}
	close(start)
	var succeeded, notFound int
	for range 2 {
		select {
		case err := <-results:
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, ErrNotFound):
				notFound++
			default:
				t.Fatalf("concurrent invitation acceptance: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("timed out waiting for concurrent invitation acceptance")
		}
	}
	if succeeded != 1 || notFound != 1 {
		t.Fatalf("accept outcomes: succeeded=%d not_found=%d", succeeded, notFound)
	}
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM users WHERE email = ?", "invitee@example.com")
	assertRowCount(t, repo.DB(), 1, "SELECT COUNT(*) FROM mailbox_memberships WHERE mailbox_id = ? AND user_id = (SELECT id FROM users WHERE email = ?)", mailboxID, "invitee@example.com")
}

func TestConsumePasswordResetInvalidatesAllOutstandingTokens(t *testing.T) {
	ctx := context.Background()
	repo, user := resetTestRepository(t)
	firstHash := []byte("reset-first")
	secondHash := []byte("reset-second")
	if err := repo.CreatePasswordResetForUser(ctx, user.ID, firstHash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	insertResetToken(t, repo.DB(), user.ID, user.Email, secondHash, time.Now().Add(time.Hour))
	if _, err := repo.CreateSession(ctx, user.ID, []byte("session-token"), []byte("csrf-token"), time.Now().Add(time.Hour), nil, "test"); err != nil {
		t.Fatal(err)
	}

	if err := repo.ConsumePasswordReset(ctx, firstHash, "replacement-hash"); err != nil {
		t.Fatal(err)
	}
	for _, tokenHash := range [][]byte{firstHash, secondHash} {
		if _, err := repo.UserForPasswordReset(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
			t.Fatalf("reset token %q remained usable: %v", tokenHash, err)
		}
	}
	assertRowCount(t, repo.DB(), 0, `SELECT COUNT(*) FROM account_tokens
		WHERE token_type = 'password_reset' AND user_id = ? AND used_at IS NULL`, user.ID)
	assertRowCount(t, repo.DB(), 0, "SELECT COUNT(*) FROM sessions WHERE user_id = ?", user.ID)

	var passwordHash string
	if err := repo.DB().QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", user.ID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if passwordHash != "replacement-hash" {
		t.Fatalf("password hash = %q", passwordHash)
	}
}

func resetTestRepository(t *testing.T) (*Repository, model.User) {
	t.Helper()
	ctx := context.Background()
	repo := testRepository(t)
	if _, err := repo.EnsureMailbox(ctx, "hello@example.com", "Example"); err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	return repo, user
}

func insertResetToken(t *testing.T, database *sql.DB, userID, email string, tokenHash []byte, expiresAt time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := database.ExecContext(context.Background(), `INSERT INTO account_tokens
		(id, token_hash, token_type, user_id, email, expires_at, created_at)
		VALUES (?, ?, 'password_reset', ?, ?, ?, ?)`, ids.New(), tokenHash, userID, email, millis(expiresAt), millis(now)); err != nil {
		t.Fatal(err)
	}
}

func assertRowCount(t *testing.T, database *sql.DB, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("row count = %d, want %d for %s", got, want, fmt.Sprintf("%q", query))
	}
}

func waitForInUseConnections(database *sql.DB, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	var stableSince time.Time
	for time.Now().Before(deadline) {
		if database.Stats().InUse >= want {
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= 20*time.Millisecond {
				return true
			}
		} else {
			stableSince = time.Time{}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
