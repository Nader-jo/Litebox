package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
)

// GetDigestSubscription loads a user's personal summary preference.
func (r *Repository) GetDigestSubscription(ctx context.Context, userID string) (model.DigestSubscription, error) {
	var sub model.DigestSubscription
	var enabled int
	var last sql.NullInt64
	var attempt, success, windowSince, windowUntil sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT id, user_id, recipient_email, frequency, timezone,
		send_hour, mailbox_scope, enabled, last_sent_at, last_attempt_at, last_success_at,
		COALESCE(last_error, ''), last_received, last_unread, last_sent, last_window_since, last_window_until
		FROM digest_subscriptions WHERE user_id = ?`, userID).
		Scan(&sub.ID, &sub.UserID, &sub.RecipientEmail, &sub.Frequency, &sub.Timezone, &sub.SendHour, &sub.MailboxScope, &enabled, &last,
			&attempt, &success, &sub.LastError, &sub.LastReceived, &sub.LastUnread, &sub.LastSent, &windowSince, &windowUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DigestSubscription{}, ErrNotFound
	}
	if err != nil {
		return sub, err
	}
	sub.Enabled = enabled != 0
	if last.Valid {
		value := fromMillis(last.Int64)
		sub.LastSentAt = &value
	}
	sub.LastAttemptAt, sub.LastSuccessAt = nullableTime(attempt), nullableTime(success)
	sub.LastWindowSince, sub.LastWindowUntil = nullableTime(windowSince), nullableTime(windowUntil)
	sub.MailboxIDs, err = r.digestMailboxIDs(ctx, sub.ID)
	return sub, err
}

func (r *Repository) DigestSubscriptionByID(ctx context.Context, id string) (model.DigestSubscription, error) {
	var sub model.DigestSubscription
	var enabled int
	var last sql.NullInt64
	var attempt, success, windowSince, windowUntil sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT id, user_id, recipient_email, frequency, timezone,
		send_hour, mailbox_scope, enabled, last_sent_at, last_attempt_at, last_success_at,
		COALESCE(last_error, ''), last_received, last_unread, last_sent, last_window_since, last_window_until
		FROM digest_subscriptions WHERE id = ?`, id).
		Scan(&sub.ID, &sub.UserID, &sub.RecipientEmail, &sub.Frequency, &sub.Timezone, &sub.SendHour, &sub.MailboxScope, &enabled, &last,
			&attempt, &success, &sub.LastError, &sub.LastReceived, &sub.LastUnread, &sub.LastSent, &windowSince, &windowUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DigestSubscription{}, ErrNotFound
	}
	if err != nil {
		return sub, err
	}
	sub.Enabled = enabled != 0
	if last.Valid {
		value := fromMillis(last.Int64)
		sub.LastSentAt = &value
	}
	sub.LastAttemptAt, sub.LastSuccessAt = nullableTime(attempt), nullableTime(success)
	sub.LastWindowSince, sub.LastWindowUntil = nullableTime(windowSince), nullableTime(windowUntil)
	sub.MailboxIDs, err = r.digestMailboxIDs(ctx, sub.ID)
	return sub, err
}

func (r *Repository) digestMailboxIDs(ctx context.Context, subscriptionID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT mailbox_id FROM digest_subscription_mailboxes WHERE subscription_id = ? ORDER BY mailbox_id", subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// SaveDigestSubscription validates and replaces one user's preference. Selected
// mailboxes must already be visible to that user.
func (r *Repository) SaveDigestSubscription(ctx context.Context, sub model.DigestSubscription) error {
	parsed, err := mail.ParseAddress(strings.TrimSpace(sub.RecipientEmail))
	if err != nil || !strings.EqualFold(parsed.Address, sub.RecipientEmail) {
		return fmt.Errorf("digest recipient must be a valid email address")
	}
	sub.RecipientEmail = strings.ToLower(parsed.Address)
	if sub.Frequency != "daily" && sub.Frequency != "weekly" {
		return fmt.Errorf("digest frequency must be daily or weekly")
	}
	if sub.SendHour < 0 || sub.SendHour > 23 {
		return fmt.Errorf("digest send hour must be between 0 and 23")
	}
	if _, err := time.LoadLocation(sub.Timezone); err != nil {
		return fmt.Errorf("invalid digest timezone")
	}
	if sub.MailboxScope != "all" && sub.MailboxScope != "selected" {
		return fmt.Errorf("invalid mailbox scope")
	}
	if sub.ID == "" {
		sub.ID = ids.New()
	}
	now := millis(time.Now())
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		if sub.MailboxScope == "selected" {
			for _, mailboxID := range sub.MailboxIDs {
				var allowed int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM mailbox_memberships WHERE user_id = ? AND mailbox_id = ?", sub.UserID, mailboxID).Scan(&allowed); err != nil {
					return err
				}
				if allowed == 0 {
					return fmt.Errorf("selected mailbox is not accessible")
				}
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO digest_subscriptions
			(id, user_id, recipient_email, frequency, timezone, send_hour, mailbox_scope, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET recipient_email = excluded.recipient_email,
			frequency = excluded.frequency, timezone = excluded.timezone, send_hour = excluded.send_hour,
			mailbox_scope = excluded.mailbox_scope, enabled = excluded.enabled, updated_at = excluded.updated_at`,
			sub.ID, sub.UserID, sub.RecipientEmail, sub.Frequency, sub.Timezone, sub.SendHour, sub.MailboxScope, boolInt(sub.Enabled), now, now)
		if err != nil {
			return err
		}
		var actualID string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM digest_subscriptions WHERE user_id = ?", sub.UserID).Scan(&actualID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM digest_subscription_mailboxes WHERE subscription_id = ?", actualID); err != nil {
			return err
		}
		for _, mailboxID := range sub.MailboxIDs {
			if _, err := tx.ExecContext(ctx, "INSERT INTO digest_subscription_mailboxes(subscription_id, mailbox_id) VALUES (?, ?)", actualID, mailboxID); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (r *Repository) DisableDigestSubscription(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE digest_subscriptions SET enabled = 0, updated_at = ? WHERE user_id = ?", millis(time.Now()), userID)
	return err
}

// DueDigestSubscriptions returns enabled subscriptions for the scheduler to
// evaluate in their own timezone.
func (r *Repository) DueDigestSubscriptions(ctx context.Context) ([]model.DigestSubscription, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, user_id, recipient_email, frequency, timezone, send_hour, mailbox_scope, enabled, last_sent_at,
		last_attempt_at, last_success_at, COALESCE(last_error, ''), last_received, last_unread, last_sent, last_window_since, last_window_until
		FROM digest_subscriptions WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.DigestSubscription
	for rows.Next() {
		var sub model.DigestSubscription
		var enabled int
		var last, attempt, success, windowSince, windowUntil sql.NullInt64
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.RecipientEmail, &sub.Frequency, &sub.Timezone, &sub.SendHour, &sub.MailboxScope, &enabled, &last,
			&attempt, &success, &sub.LastError, &sub.LastReceived, &sub.LastUnread, &sub.LastSent, &windowSince, &windowUntil); err != nil {
			return nil, err
		}
		sub.Enabled = enabled != 0
		if last.Valid {
			value := fromMillis(last.Int64)
			sub.LastSentAt = &value
		}
		sub.LastAttemptAt, sub.LastSuccessAt = nullableTime(attempt), nullableTime(success)
		sub.LastWindowSince, sub.LastWindowUntil = nullableTime(windowSince), nullableTime(windowUntil)
		sub.MailboxIDs, err = r.digestMailboxIDs(ctx, sub.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

// DigestCounts returns metadata counts constrained by user mailbox access.
func (r *Repository) DigestCounts(ctx context.Context, sub model.DigestSubscription, since, until time.Time) (model.DigestCounts, error) {
	result := model.DigestCounts{Since: since, Until: until}
	args := []any{sub.UserID}
	filter := "mm.user_id = ?"
	if sub.MailboxScope == "selected" {
		if len(sub.MailboxIDs) == 0 {
			return result, nil
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(sub.MailboxIDs)), ",")
		filter += " AND m.mailbox_id IN (" + placeholders + ")"
		for _, id := range sub.MailboxIDs {
			args = append(args, id)
		}
	}
	query := `SELECT m.mailbox_id, mb.address,
		SUM(CASE WHEN m.direction = 'inbound' THEN 1 ELSE 0 END),
		SUM(CASE WHEN m.direction = 'inbound' AND m.is_read = 0 THEN 1 ELSE 0 END),
		SUM(CASE WHEN m.direction = 'outbound' THEN 1 ELSE 0 END)
		FROM messages m JOIN mailbox_memberships mm ON mm.mailbox_id = m.mailbox_id
		JOIN mailboxes mb ON mb.id = m.mailbox_id
		WHERE ` + filter + ` AND COALESCE(m.received_at, m.sent_at, m.created_at) >= ? AND COALESCE(m.received_at, m.sent_at, m.created_at) < ?
		GROUP BY m.mailbox_id, mb.address ORDER BY mb.address`
	args = append(args, millis(since), millis(until))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item model.DigestMailboxCount
		if err := rows.Scan(&item.MailboxID, &item.Address, &item.Received, &item.Unread, &item.Sent); err != nil {
			return result, err
		}
		result.ByMailbox = append(result.ByMailbox, item)
		result.Received += item.Received
		result.Unread += item.Unread
		result.Sent += item.Sent
	}
	return result, rows.Err()
}

func (r *Repository) MarkDigestSent(ctx context.Context, subscriptionID string, sentAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE digest_subscriptions SET last_sent_at = ?, last_attempt_at = ?,
		last_success_at = ?, last_error = '', updated_at = ? WHERE id = ?`, millis(sentAt), millis(sentAt), millis(sentAt), millis(time.Now()), subscriptionID)
	return err
}

// MarkDigestAttempt records metadata-only counts and the outcome of a digest
// delivery attempt. It never stores message content.
func (r *Repository) MarkDigestAttempt(ctx context.Context, subscriptionID string, counts model.DigestCounts, attemptedAt time.Time, deliveryErr error) error {
	message := ""
	if deliveryErr != nil {
		message = deliveryErr.Error()
		if len(message) > 500 {
			message = message[:500]
		}
	}
	_, err := r.db.ExecContext(ctx, `UPDATE digest_subscriptions SET last_attempt_at = ?, last_error = ?,
		last_received = ?, last_unread = ?, last_sent = ?, last_window_since = ?, last_window_until = ?, updated_at = ? WHERE id = ?`,
		millis(attemptedAt), message, counts.Received, counts.Unread, counts.Sent, millis(counts.Since), millis(counts.Until), millis(time.Now()), subscriptionID)
	return err
}
