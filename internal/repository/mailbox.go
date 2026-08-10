package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	mailx "github.com/Nader-jo/Litebox/internal/mail"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/search"
)

// InboundMessage is the fully archived provider-neutral input for one received email.
type InboundMessage struct {
	MailboxID           string
	ThreadID            string
	ResendEmailID       string
	RFCMessageID        string
	InReplyTo           string
	References          string
	From                model.Address
	Recipients          map[string][]model.Address
	Subject             string
	TextBody            string
	SanitizedHTML       string
	RemoteImagesBlocked bool
	RawStorageKey       string
	ReceivedAt          time.Time
	SizeBytes           int64
	Attachments         []model.Attachment
	IngestStatus        string
}

// ListThreads returns a bounded folder projection ordered by latest activity.
func (r *Repository) ListThreads(ctx context.Context, mailboxID, folder string, limit int) ([]model.ThreadSummary, error) {
	threads, _, err := r.ListThreadsPage(ctx, mailboxID, folder, limit, time.Time{}, "")
	return threads, err
}

// ListThreadsPage returns one keyset-paginated folder page. The cursor is the
// final (latest_message_at, id) tuple from the preceding page.
func (r *Repository) ListThreadsPage(ctx context.Context, mailboxID, folder string, limit int, before time.Time, beforeID string) ([]model.ThreadSummary, bool, error) {
	condition := "t.is_trashed = 0 AND t.is_archived = 0"
	switch folder {
	case "inbox", "":
	case "sent":
		condition = "t.is_trashed = 0 AND EXISTS (SELECT 1 FROM messages sx WHERE sx.thread_id = t.id AND sx.direction = 'outbound')"
	case "archive":
		condition = "t.is_trashed = 0 AND t.is_archived = 1"
	case "starred":
		condition = "t.is_trashed = 0 AND t.is_starred = 1"
	case "trash":
		condition = "t.is_trashed = 1"
	default:
		return nil, false, fmt.Errorf("unknown folder %q", folder)
	}
	var args []any
	if !before.IsZero() && beforeID != "" {
		condition += " AND (t.latest_message_at < ? OR (t.latest_message_at = ? AND t.id < ?))"
		args = append(args, millis(before), millis(before), beforeID)
	}
	threads, err := r.queryThreadSummaries(ctx, mailboxID, condition, args, limit+1)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(threads) > limit
	if hasMore {
		threads = threads[:limit]
	}
	return threads, hasMore, nil
}

func (r *Repository) queryThreadSummaries(ctx context.Context, mailboxID, condition string, args []any, limit int) ([]model.ThreadSummary, error) {
	query := `WITH latest AS (
        SELECT m.*, ROW_NUMBER() OVER (PARTITION BY m.thread_id ORDER BY COALESCE(m.received_at, m.sent_at, m.created_at) DESC, m.id DESC) AS rank
        FROM messages m
    )
    SELECT t.id, t.subject_display, t.latest_message_at, t.message_count, t.unread_count,
        t.is_archived, t.is_starred, t.is_trashed, l.direction, COALESCE(l.delivery_status, ''),
        CASE WHEN l.direction = 'inbound' THEN COALESCE(NULLIF(l.from_name, ''), l.from_address)
             ELSE COALESCE((SELECT GROUP_CONCAT(COALESCE(NULLIF(mr.name, ''), mr.address), ', ')
                            FROM message_recipients mr WHERE mr.message_id = l.id AND mr.recipient_type = 'to'), 'No recipient') END,
        SUBSTR(REPLACE(REPLACE(l.text_body, CHAR(10), ' '), CHAR(13), ' '), 1, 180),
        EXISTS(SELECT 1 FROM attachments a WHERE a.message_id = l.id)
    FROM threads t JOIN latest l ON l.thread_id = t.id AND l.rank = 1
	WHERE t.mailbox_id = ? AND (` + condition + `) ORDER BY t.latest_message_at DESC, t.id DESC LIMIT ?`
	args = append([]any{mailboxID}, args...)
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []model.ThreadSummary
	for rows.Next() {
		var summary model.ThreadSummary
		var latest int64
		var archived, starred, trashed, hasAttachments int
		if err := rows.Scan(&summary.ID, &summary.Subject, &latest, &summary.MessageCount, &summary.UnreadCount,
			&archived, &starred, &trashed, &summary.Direction, &summary.DeliveryStatus, &summary.Participants,
			&summary.Snippet, &hasAttachments); err != nil {
			return nil, err
		}
		summary.LatestMessageAt = fromMillis(latest)
		summary.IsArchived, summary.IsStarred, summary.IsTrashed = boolean(archived), boolean(starred), boolean(trashed)
		summary.HasAttachments = boolean(hasAttachments)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// SearchThreads runs FTS and structured filters with parameterized values.
func (r *Repository) SearchThreads(ctx context.Context, mailboxID string, query search.Query, limit int) ([]model.ThreadSummary, error) {
	threads, _, err := r.SearchThreadsPage(ctx, mailboxID, query, limit, time.Time{}, "")
	return threads, err
}

// SearchThreadsPage runs FTS and structured filters with keyset pagination.
func (r *Repository) SearchThreadsPage(ctx context.Context, mailboxID string, query search.Query, limit int, before time.Time, beforeID string) ([]model.ThreadSummary, bool, error) {
	conditions := []string{"t.mailbox_id = ?", "t.is_trashed = 0"}
	args := []any{mailboxID}
	joinSearch := false
	if fts := query.FTS(); fts != "" {
		joinSearch = true
		conditions = append(conditions, "message_search MATCH ?")
		args = append(args, fts)
	}
	if query.From != "" {
		conditions = append(conditions, "LOWER(m.from_address) = LOWER(?)")
		args = append(args, query.From)
	}
	if query.To != "" {
		conditions = append(conditions, "EXISTS(SELECT 1 FROM message_recipients sr WHERE sr.message_id = m.id AND LOWER(sr.address) = LOWER(?))")
		args = append(args, query.To)
	}
	if query.Subject != "" {
		conditions = append(conditions, "LOWER(m.subject) LIKE LOWER(?)")
		args = append(args, "%"+query.Subject+"%")
	}
	if query.HasAttachment {
		conditions = append(conditions, "m.has_attachments = 1")
	}
	if query.Unread {
		conditions = append(conditions, "m.direction = 'inbound' AND m.is_read = 0")
	}
	if query.Starred {
		conditions = append(conditions, "t.is_starred = 1")
	}
	if query.After != nil {
		conditions = append(conditions, "COALESCE(m.received_at, m.sent_at, m.created_at) >= ?")
		args = append(args, millis(*query.After))
	}
	if query.Before != nil {
		conditions = append(conditions, "COALESCE(m.received_at, m.sent_at, m.created_at) < ?")
		args = append(args, millis(*query.Before))
	}
	if !before.IsZero() && beforeID != "" {
		conditions = append(conditions, "(t.latest_message_at < ? OR (t.latest_message_at = ? AND t.id < ?))")
		args = append(args, millis(before), millis(before), beforeID)
	}
	join := ""
	if joinSearch {
		join = " JOIN message_search ON message_search.message_id = m.id"
	}
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT t.id FROM threads t JOIN messages m ON m.thread_id = t.id`+join+
		` WHERE `+strings.Join(conditions, " AND ")+` ORDER BY t.latest_message_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var identifiers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		identifiers = append(identifiers, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(identifiers) == 0 {
		return nil, false, nil
	}
	hasMore := len(identifiers) > limit
	if hasMore {
		identifiers = identifiers[:limit]
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(identifiers)), ",")
	identifierArgs := make([]any, len(identifiers))
	for index, id := range identifiers {
		identifierArgs[index] = id
	}
	threads, err := r.queryThreadSummaries(ctx, mailboxID, "t.id IN ("+placeholders+")", identifierArgs, limit)
	return threads, hasMore, err
}

// ThreadByID loads a conversation and all of its normalized metadata.
func (r *Repository) ThreadByID(ctx context.Context, mailboxID, id string) (model.Thread, error) {
	var thread model.Thread
	var archived, starred, trashed int
	err := r.db.QueryRowContext(ctx, `SELECT id, subject_display, is_archived, is_starred, is_trashed FROM threads WHERE id = ? AND mailbox_id = ?`, id, mailboxID).
		Scan(&thread.ID, &thread.Subject, &archived, &starred, &trashed)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Thread{}, ErrNotFound
	}
	if err != nil {
		return model.Thread{}, err
	}
	thread.IsArchived, thread.IsStarred, thread.IsTrashed = boolean(archived), boolean(starred), boolean(trashed)
	rows, err := r.db.QueryContext(ctx, `SELECT id, thread_id, direction, COALESCE(resend_email_id, ''),
        COALESCE(rfc_message_id, ''), COALESCE(in_reply_to, ''), COALESCE(references_header, ''),
        from_name, from_address, subject, text_body, sanitized_html, body_format, remote_images_blocked,
        COALESCE(raw_storage_key, ''), COALESCE(received_at, sent_at, created_at), is_read, ingest_status,
        COALESCE(delivery_status, ''), COALESCE(provider_error_code, ''), COALESCE(provider_error_message, ''),
        COALESCE(size_bytes, 0) FROM messages WHERE thread_id = ?
        ORDER BY COALESCE(received_at, sent_at, created_at), id`, id)
	if err != nil {
		return model.Thread{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var message model.Message
		var occurred int64
		var read, blocked int
		if err := rows.Scan(&message.ID, &message.ThreadID, &message.Direction, &message.ResendEmailID,
			&message.RFCMessageID, &message.InReplyTo, &message.References, &message.From.Name,
			&message.From.Address, &message.Subject, &message.TextBody, &message.SanitizedHTML, &message.BodyFormat,
			&blocked, &message.RawStorageKey, &occurred, &read, &message.IngestStatus, &message.DeliveryStatus,
			&message.ProviderErrorCode, &message.ProviderErrorMessage, &message.SizeBytes); err != nil {
			return model.Thread{}, err
		}
		message.OccurredAt = fromMillis(occurred)
		message.IsRead, message.RemoteImagesBlocked = boolean(read), boolean(blocked)
		message.Recipients, err = r.messageRecipients(ctx, message.ID)
		if err != nil {
			return model.Thread{}, err
		}
		message.Attachments, err = r.listAttachments(ctx, "message_id", message.ID)
		if err != nil {
			return model.Thread{}, err
		}
		thread.Messages = append(thread.Messages, message)
	}
	return thread, rows.Err()
}

// MarkThreadRead changes all inbound messages and refreshes the denormalized count.
func (r *Repository) MarkThreadRead(ctx context.Context, mailboxID, id string, read bool) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads WHERE id = ? AND mailbox_id = ?", id, mailboxID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "UPDATE messages SET is_read = ?, updated_at = ? WHERE thread_id = ? AND mailbox_id = ? AND direction = 'inbound'", boolInt(read), millis(time.Now()), id, mailboxID); err != nil {
			return err
		}
		unread := 0
		if !read {
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE thread_id = ? AND mailbox_id = ? AND direction = 'inbound'", id, mailboxID).Scan(&unread); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE threads SET unread_count = ?, updated_at = ? WHERE id = ? AND mailbox_id = ?", unread, millis(time.Now()), id, mailboxID)
		return err
	})
}

// ThreadAction applies one explicit mailbox state transition.
func (r *Repository) ThreadAction(ctx context.Context, mailboxID, id, action string) error {
	statement := ""
	switch action {
	case "archive":
		statement = "is_archived = 1"
	case "unarchive":
		statement = "is_archived = 0"
	case "star":
		statement = "is_starred = 1"
	case "unstar":
		statement = "is_starred = 0"
	case "trash":
		statement = "pre_trash_archived = is_archived, is_trashed = 1, trashed_at = " + fmt.Sprint(millis(time.Now()))
	case "restore":
		statement = "is_trashed = 0, is_archived = pre_trash_archived, trashed_at = NULL"
	default:
		return fmt.Errorf("unknown thread action")
	}
	result, err := r.db.ExecContext(ctx, "UPDATE threads SET "+statement+", updated_at = ? WHERE id = ? AND mailbox_id = ?", millis(time.Now()), id, mailboxID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteThread permanently removes local rows and schedules idempotent blob deletion.
func (r *Repository) DeleteThread(ctx context.Context, mailboxID, id string) error {
	var keys []string
	rows, err := r.db.QueryContext(ctx, `SELECT storage_key FROM attachments WHERE message_id IN (SELECT id FROM messages WHERE thread_id = ? AND mailbox_id = ?)
		UNION ALL SELECT raw_storage_key FROM messages WHERE thread_id = ? AND mailbox_id = ? AND raw_storage_key IS NOT NULL`, id, mailboxID, id, mailboxID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	rows.Close()
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		var trashed int
		if err := tx.QueryRowContext(ctx, "SELECT is_trashed FROM threads WHERE id = ? AND mailbox_id = ?", id, mailboxID).Scan(&trashed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if trashed == 0 {
			return fmt.Errorf("thread must be in trash before permanent deletion")
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM message_search WHERE thread_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE thread_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM threads WHERE id = ?", id); err != nil {
			return err
		}
		if len(keys) > 0 {
			payload, _ := json.Marshal(map[string][]string{"keys": keys})
			_, _, err := enqueueJob(ctx, tx, "delete_objects", "delete-thread:"+id, string(payload), 100, 20, time.Now())
			return err
		}
		return nil
	})
}

// FindInboundThread uses message relationships first and a conservative participant fallback second.
func (r *Repository) FindInboundThread(ctx context.Context, mailboxID, inReplyTo, references, subjectNorm, participant string, receivedAt time.Time) (string, error) {
	identifiers := append([]string{inReplyTo}, strings.Fields(references)...)
	for _, identifier := range identifiers {
		if identifier == "" {
			continue
		}
		var threadID string
		err := r.db.QueryRowContext(ctx, "SELECT thread_id FROM messages WHERE mailbox_id = ? AND rfc_message_id = ? AND thread_id IS NOT NULL", mailboxID, identifier).Scan(&threadID)
		if err == nil {
			return threadID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if subjectNorm == "" || participant == "" {
		return "", nil
	}
	var threadID string
	err := r.db.QueryRowContext(ctx, `SELECT t.id FROM threads t JOIN messages m ON m.thread_id = t.id
		WHERE t.mailbox_id = ? AND t.subject_norm = ? AND t.latest_message_at >= ?
          AND (LOWER(m.from_address) = LOWER(?) OR EXISTS(
              SELECT 1 FROM message_recipients mr WHERE mr.message_id = m.id AND LOWER(mr.address) = LOWER(?)))
		ORDER BY t.latest_message_at DESC LIMIT 1`, mailboxID, subjectNorm, millis(receivedAt.Add(-30*24*time.Hour)), participant, participant).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return threadID, err
}

// SaveInbound atomically finalizes a locally archived message and its search index.
func (r *Repository) SaveInbound(ctx context.Context, input InboundMessage) (string, string, error) {
	messageID := ids.New()
	threadID := input.ThreadID
	if input.IngestStatus == "" {
		input.IngestStatus = "ready"
	}
	err := r.Transaction(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE mailbox_id = ? AND resend_email_id = ? AND direction = 'inbound'", input.MailboxID, input.ResendEmailID).Scan(&existing)
		if err == nil {
			messageID = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if threadID == "" {
			threadID = ids.New()
			_, err := tx.ExecContext(ctx, `INSERT INTO threads
                (id, mailbox_id, subject_norm, subject_display, latest_message_at, first_message_at,
                 message_count, unread_count, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, ?)`, threadID, input.MailboxID,
				mailx.NormalizeSubject(input.Subject), input.Subject, millis(input.ReceivedAt), millis(input.ReceivedAt),
				millis(time.Now()), millis(time.Now()))
			if err != nil {
				return err
			}
		} else {
			var valid int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads WHERE id = ? AND mailbox_id = ?", threadID, input.MailboxID).Scan(&valid); err != nil {
				return err
			}
			if valid == 0 {
				return ErrNotFound
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages
            (id, thread_id, mailbox_id, direction, resend_email_id, rfc_message_id, in_reply_to, references_header,
             from_name, from_address, subject, subject_norm, text_body, sanitized_html, body_format,
             remote_images_blocked, raw_storage_key, received_at, created_at, updated_at, is_read, ingest_status,
             size_bytes, has_attachments)
            VALUES (?, ?, ?, 'inbound', ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?,
                    NULLIF(?, ''), ?, ?, ?, 0, ?, ?, ?)`, messageID, threadID, input.MailboxID, input.ResendEmailID,
			input.RFCMessageID, input.InReplyTo, input.References, input.From.Name, input.From.Address, input.Subject,
			mailx.NormalizeSubject(input.Subject), input.TextBody, input.SanitizedHTML,
			map[bool]string{true: "html", false: "text"}[input.SanitizedHTML != ""], boolInt(input.RemoteImagesBlocked),
			input.RawStorageKey, millis(input.ReceivedAt), millis(time.Now()), millis(time.Now()), input.IngestStatus,
			input.SizeBytes, boolInt(len(input.Attachments) > 0))
		if err != nil {
			return err
		}
		var recipientText []string
		for kind, addresses := range input.Recipients {
			for index, address := range addresses {
				if _, err := tx.ExecContext(ctx, `INSERT INTO message_recipients
                    (id, message_id, recipient_type, name, address, sort_order) VALUES (?, ?, ?, ?, ?, ?)`, ids.New(),
					messageID, kind, address.Name, address.Address, index); err != nil {
					return err
				}
				recipientText = append(recipientText, address.Address)
			}
		}
		for _, attachment := range input.Attachments {
			if _, err := tx.ExecContext(ctx, `INSERT INTO attachments
                (id, message_id, provider_attachment_id, filename, safe_filename, content_type,
                 content_disposition, content_id, storage_backend, storage_key, size_bytes, sha256,
                 storage_status, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
				attachment.ID, messageID, attachment.ProviderAttachmentID, attachment.Filename, attachment.SafeFilename,
				attachment.ContentType, attachment.ContentDisposition, attachment.ContentID, attachment.StorageBackend,
				attachment.StorageKey, attachment.SizeBytes, attachment.SHA256, attachment.StorageStatus,
				millis(attachment.CreatedAt)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_search(message_id, thread_id, subject, sender, recipients, body)
            VALUES (?, ?, ?, ?, ?, ?)`, messageID, threadID, input.Subject,
			input.From.Name+" "+input.From.Address, strings.Join(recipientText, " "), input.TextBody); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE threads SET subject_norm = ?, subject_display = ?,
            latest_message_at = MAX(latest_message_at, ?), message_count = message_count + 1,
            unread_count = unread_count + 1, is_trashed = 0, updated_at = ? WHERE id = ?`,
			mailx.NormalizeSubject(input.Subject), input.Subject, millis(input.ReceivedAt), millis(time.Now()), threadID)
		return err
	})
	return messageID, threadID, err
}

// OutboundMessage loads one queued/sending message for a provider adapter.
func (r *Repository) OutboundMessage(ctx context.Context, id string) (model.Message, error) {
	var message model.Message
	var occurred int64
	err := r.db.QueryRowContext(ctx, `SELECT id, thread_id, COALESCE(resend_email_id, ''), COALESCE(rfc_message_id, ''),
        COALESCE(in_reply_to, ''), COALESCE(references_header, ''), from_name, from_address, subject, text_body,
        COALESCE(delivery_status, ''), COALESCE(sent_at, created_at) FROM messages WHERE id = ? AND direction = 'outbound'`, id).
		Scan(&message.ID, &message.ThreadID, &message.ResendEmailID, &message.RFCMessageID, &message.InReplyTo,
			&message.References, &message.From.Name, &message.From.Address, &message.Subject, &message.TextBody,
			&message.DeliveryStatus, &occurred)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, ErrNotFound
	}
	if err != nil {
		return model.Message{}, err
	}
	message.Direction, message.OccurredAt = "outbound", fromMillis(occurred)
	message.Recipients, err = r.messageRecipients(ctx, id)
	if err != nil {
		return model.Message{}, err
	}
	message.Attachments, err = r.listAttachments(ctx, "message_id", id)
	return message, err
}

// MarkMessageSubmitted records the provider resource ID after an idempotent send.
func (r *Repository) MarkMessageSubmitted(ctx context.Context, id, resendID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE messages SET resend_email_id = ?, delivery_status = 'submitted',
        updated_at = ?, provider_error_code = NULL, provider_error_message = NULL WHERE id = ? AND direction = 'outbound'`,
		resendID, millis(time.Now()), id)
	return err
}

// MarkMessageSendError stores a safe provider failure for display.
func (r *Repository) MarkMessageSendError(ctx context.Context, id, code, message string, permanent bool) error {
	status := "queued"
	if permanent {
		status = "failed"
	}
	_, err := r.db.ExecContext(ctx, `UPDATE messages SET delivery_status = ?, provider_error_code = ?,
        provider_error_message = ?, updated_at = ? WHERE id = ?`, status, code, message, millis(time.Now()), id)
	return err
}

func (r *Repository) messageRecipients(ctx context.Context, messageID string) (map[string][]model.Address, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT recipient_type, name, address FROM message_recipients
        WHERE message_id = ? ORDER BY recipient_type, sort_order`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]model.Address{"to": {}, "cc": {}, "bcc": {}, "reply_to": {}}
	for rows.Next() {
		var kind string
		var address model.Address
		if err := rows.Scan(&kind, &address.Name, &address.Address); err != nil {
			return nil, err
		}
		result[kind] = append(result[kind], address)
	}
	return result, rows.Err()
}
