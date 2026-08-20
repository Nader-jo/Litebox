package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	mailx "github.com/Nader-jo/Litebox/internal/mail"
	"github.com/Nader-jo/Litebox/internal/model"
)

// PersistWebhook stores a verified payload and its durable processing job in one transaction.
func (r *Repository) PersistWebhook(ctx context.Context, event model.WebhookEvent, providerCreatedAt time.Time, jobKind, dedupeKey string, payload any) (bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO webhook_events
        (id, svix_id, event_type, resend_email_id, provider_created_at, raw_payload, received_at, processing_status)
        VALUES (?, ?, ?, ?, ?, ?, ?, 'queued')`, event.ID, event.SvixID, event.EventType, event.ResendEmailID,
		millis(providerCreatedAt), event.RawPayload, millis(event.ReceivedAt))
	if err != nil {
		return false, rollback(tx, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, rollback(tx, err)
	}
	if inserted == 0 {
		return false, rollback(tx, nil)
	}
	_, jobInserted, err := enqueueJob(ctx, tx, jobKind, dedupeKey, string(encoded), 50, 10, time.Now())
	if err != nil {
		return false, rollback(tx, err)
	}
	if !jobInserted {
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_events SET processing_status = 'duplicate', processed_at = ?
			WHERE id = ?`, millis(time.Now()), event.ID); err != nil {
			return false, rollback(tx, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// CompleteWebhook marks a durable provider event after its job succeeds.
func (r *Repository) CompleteWebhook(ctx context.Context, id, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE webhook_events SET processing_status = ?, processed_at = ?,
        attempt_count = attempt_count + 1, last_error = NULL WHERE id = ?`, status, millis(time.Now()), id)
	return err
}

// FailWebhook stores a safe error summary for operator diagnostics.
func (r *Repository) FailWebhook(ctx context.Context, id, message string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE webhook_events SET processing_status = 'failed',
        attempt_count = attempt_count + 1, last_error = ? WHERE id = ?`, message, id)
	return err
}

// ListWebhooks returns recent verified webhook deliveries.
func (r *Repository) ListWebhooks(ctx context.Context, limit int) ([]model.WebhookEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, svix_id, event_type, COALESCE(resend_email_id, ''), raw_payload,
        received_at, processed_at, processing_status, attempt_count, COALESCE(last_error, '')
        FROM webhook_events ORDER BY received_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.WebhookEvent
	for rows.Next() {
		var event model.WebhookEvent
		var received int64
		var processed sql.NullInt64
		if err := rows.Scan(&event.ID, &event.SvixID, &event.EventType, &event.ResendEmailID, &event.RawPayload,
			&received, &processed, &event.ProcessingStatus, &event.AttemptCount, &event.LastError); err != nil {
			return nil, err
		}
		event.ReceivedAt = fromMillis(received)
		event.ProcessedAt = nullableTime(processed)
		events = append(events, event)
	}
	return events, rows.Err()
}

// StoreProviderEvent deduplicates a delivery event and advances an outbound message state.
func (r *Repository) StoreProviderEvent(ctx context.Context, svixID, resendID, eventType, payload string, eventAt time.Time) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO provider_events
            (id, svix_id, resend_email_id, event_type, event_at, payload_json, created_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)`, ids.New(), svixID, resendID, eventType, millis(eventAt), payload, millis(time.Now()))
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil || inserted == 0 {
			return err
		}
		var current sql.NullString
		var occurred sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT delivery_status, last_provider_event_at FROM messages
			WHERE resend_email_id = ? AND direction = 'outbound'`, resendID).Scan(&current, &occurred); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var currentAt time.Time
		if occurred.Valid {
			currentAt = fromMillis(occurred.Int64)
		}
		status := mailx.ReduceDelivery(current.String, currentAt, eventType, eventAt)
		effectiveAt := eventAt
		if currentAt.After(effectiveAt) {
			effectiveAt = currentAt
		}
		_, err = tx.ExecContext(ctx, `UPDATE messages SET delivery_status = ?, last_provider_event_at = ?, updated_at = ?
			WHERE resend_email_id = ? AND direction = 'outbound'`, status, millis(effectiveAt), millis(time.Now()), resendID)
		return err
	})
}

// WebhookByID loads one verified event for worker processing.
func (r *Repository) WebhookByID(ctx context.Context, id string) (model.WebhookEvent, error) {
	var event model.WebhookEvent
	var received int64
	var processed sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT id, svix_id, event_type, COALESCE(resend_email_id, ''), raw_payload,
        received_at, processed_at, processing_status, attempt_count, COALESCE(last_error, '')
        FROM webhook_events WHERE id = ?`, id).Scan(&event.ID, &event.SvixID, &event.EventType, &event.ResendEmailID,
		&event.RawPayload, &received, &processed, &event.ProcessingStatus, &event.AttemptCount, &event.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WebhookEvent{}, ErrNotFound
	}
	event.ReceivedAt = fromMillis(received)
	event.ProcessedAt = nullableTime(processed)
	return event, err
}
