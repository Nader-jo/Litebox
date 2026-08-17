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
)

// CreateDraft persists a new editable message.
func (r *Repository) CreateDraft(ctx context.Context, mailboxID string, draft model.Draft) (model.Draft, error) {
	if draft.ThreadID != "" {
		var exists int
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads WHERE id = ? AND mailbox_id = ?", draft.ThreadID, mailboxID).Scan(&exists); err != nil {
			return model.Draft{}, err
		}
		if exists == 0 {
			return model.Draft{}, ErrNotFound
		}
	}
	if draft.ReplyToMessageID != "" {
		var exists int
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ? AND mailbox_id = ?
			AND (? = '' OR thread_id = ?)`, draft.ReplyToMessageID, mailboxID, draft.ThreadID, draft.ThreadID).Scan(&exists); err != nil {
			return model.Draft{}, err
		}
		if exists == 0 {
			return model.Draft{}, ErrNotFound
		}
	}
	now := time.Now().UTC()
	if draft.ID == "" {
		draft.ID = ids.New()
	}
	draft.CreatedAt, draft.UpdatedAt = now, now
	toJSON, _ := json.Marshal(draft.To)
	ccJSON, _ := json.Marshal(draft.Cc)
	bccJSON, _ := json.Marshal(draft.Bcc)
	_, err := r.db.ExecContext(ctx, `INSERT INTO drafts
        (id, mailbox_id, thread_id, reply_to_message_id, to_json, cc_json, bcc_json, subject, text_body, created_at, updated_at)
        VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)`, draft.ID, mailboxID,
		draft.ThreadID, draft.ReplyToMessageID, string(toJSON), string(ccJSON), string(bccJSON), draft.Subject,
		draft.TextBody, millis(now), millis(now))
	return draft, err
}

// SaveDraft validates and stores editable fields.
func (r *Repository) SaveDraft(ctx context.Context, mailboxID string, draft model.Draft) error {
	toJSON, err := json.Marshal(draft.To)
	if err != nil {
		return err
	}
	ccJSON, _ := json.Marshal(draft.Cc)
	bccJSON, _ := json.Marshal(draft.Bcc)
	result, err := r.db.ExecContext(ctx, `UPDATE drafts SET to_json = ?, cc_json = ?, bcc_json = ?,
		subject = ?, text_body = ?, updated_at = ? WHERE id = ? AND mailbox_id = ?`, string(toJSON), string(ccJSON), string(bccJSON),
		draft.Subject, draft.TextBody, millis(time.Now()), draft.ID, mailboxID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}

// DraftByID loads one draft and its attachments.
func (r *Repository) DraftByID(ctx context.Context, mailboxID, id string) (model.Draft, error) {
	var draft model.Draft
	var threadID, replyID sql.NullString
	var toJSON, ccJSON, bccJSON string
	var createdAt, updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT id, thread_id, reply_to_message_id, to_json, cc_json, bcc_json,
		subject, text_body, created_at, updated_at FROM drafts WHERE id = ? AND mailbox_id = ?`, id, mailboxID).Scan(&draft.ID, &threadID,
		&replyID, &toJSON, &ccJSON, &bccJSON, &draft.Subject, &draft.TextBody, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Draft{}, ErrNotFound
	}
	if err != nil {
		return model.Draft{}, err
	}
	draft.ThreadID, draft.ReplyToMessageID = threadID.String, replyID.String
	if err := json.Unmarshal([]byte(toJSON), &draft.To); err != nil {
		return model.Draft{}, fmt.Errorf("decode draft recipients: %w", err)
	}
	_ = json.Unmarshal([]byte(ccJSON), &draft.Cc)
	_ = json.Unmarshal([]byte(bccJSON), &draft.Bcc)
	draft.CreatedAt, draft.UpdatedAt = fromMillis(createdAt), fromMillis(updatedAt)
	draft.Attachments, err = r.listAttachments(ctx, "draft_id", id)
	return draft, err
}

// ListDrafts returns recent drafts without loading attachment bytes.
func (r *Repository) ListDrafts(ctx context.Context, mailboxID string, limit int) ([]model.Draft, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, thread_id, reply_to_message_id, to_json, cc_json, bcc_json,
		subject, text_body, created_at, updated_at FROM drafts WHERE mailbox_id = ? ORDER BY updated_at DESC LIMIT ?`, mailboxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drafts []model.Draft
	for rows.Next() {
		var draft model.Draft
		var threadID, replyID sql.NullString
		var toJSON, ccJSON, bccJSON string
		var createdAt, updatedAt int64
		if err := rows.Scan(&draft.ID, &threadID, &replyID, &toJSON, &ccJSON, &bccJSON, &draft.Subject,
			&draft.TextBody, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		draft.ThreadID, draft.ReplyToMessageID = threadID.String, replyID.String
		_ = json.Unmarshal([]byte(toJSON), &draft.To)
		_ = json.Unmarshal([]byte(ccJSON), &draft.Cc)
		_ = json.Unmarshal([]byte(bccJSON), &draft.Bcc)
		draft.CreatedAt, draft.UpdatedAt = fromMillis(createdAt), fromMillis(updatedAt)
		drafts = append(drafts, draft)
	}
	return drafts, rows.Err()
}

// DeleteDraft deletes a draft and returns storage keys that must be removed.
func (r *Repository) DeleteDraft(ctx context.Context, mailboxID, id string) ([]string, error) {
	var exists int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM drafts WHERE id = ? AND mailbox_id = ?", id, mailboxID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	attachments, err := r.listAttachments(ctx, "draft_id", id)
	if err != nil {
		return nil, err
	}
	result, err := r.db.ExecContext(ctx, "DELETE FROM drafts WHERE id = ? AND mailbox_id = ?", id, mailboxID)
	if err != nil {
		return nil, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return nil, ErrNotFound
	}
	keys := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		keys = append(keys, attachment.StorageKey)
	}
	return keys, nil
}

// AddDraftAttachment persists metadata after the blob commit succeeds.
func (r *Repository) AddDraftAttachment(ctx context.Context, attachment model.Attachment) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO attachments
        (id, draft_id, filename, safe_filename, content_type, content_disposition, storage_backend,
         storage_key, size_bytes, sha256, storage_status, created_at)
        VALUES (?, ?, ?, ?, ?, 'attachment', ?, ?, ?, ?, 'ready', ?)`, attachment.ID, attachment.DraftID,
		attachment.Filename, attachment.SafeFilename, attachment.ContentType, attachment.StorageBackend,
		attachment.StorageKey, attachment.SizeBytes, attachment.SHA256, millis(attachment.CreatedAt))
	return err
}

// AttachmentByID loads private blob metadata for an authenticated route.
func (r *Repository) AttachmentByID(ctx context.Context, mailboxID, id string) (model.Attachment, error) {
	var attachment model.Attachment
	var messageID, draftID, providerID, contentID sql.NullString
	var createdAt int64
	err := r.db.QueryRowContext(ctx, `SELECT id, message_id, draft_id, provider_attachment_id, filename,
        safe_filename, content_type, content_disposition, content_id, storage_backend, storage_key,
		size_bytes, sha256, storage_status, created_at FROM attachments a WHERE id = ? AND (
			EXISTS (SELECT 1 FROM messages m WHERE m.id = a.message_id AND m.mailbox_id = ?) OR
			EXISTS (SELECT 1 FROM drafts d WHERE d.id = a.draft_id AND d.mailbox_id = ?)
		)`, id, mailboxID, mailboxID).Scan(&attachment.ID,
		&messageID, &draftID, &providerID, &attachment.Filename, &attachment.SafeFilename, &attachment.ContentType,
		&attachment.ContentDisposition, &contentID, &attachment.StorageBackend, &attachment.StorageKey,
		&attachment.SizeBytes, &attachment.SHA256, &attachment.StorageStatus, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Attachment{}, ErrNotFound
	}
	attachment.MessageID, attachment.DraftID, attachment.ProviderAttachmentID, attachment.ContentID = messageID.String, draftID.String, providerID.String, contentID.String
	attachment.CreatedAt = fromMillis(createdAt)
	return attachment, err
}

// DeleteDraftAttachment deletes metadata and returns the blob key.
func (r *Repository) DeleteDraftAttachment(ctx context.Context, mailboxID, draftID, attachmentID string) (string, error) {
	var key string
	if err := r.db.QueryRowContext(ctx, `SELECT a.storage_key FROM attachments a JOIN drafts d ON d.id = a.draft_id
		WHERE a.id = ? AND a.draft_id = ? AND d.mailbox_id = ?`, attachmentID, draftID, mailboxID).Scan(&key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	_, err := r.db.ExecContext(ctx, "DELETE FROM attachments WHERE id = ? AND draft_id = ?", attachmentID, draftID)
	return key, err
}

// QueueDraft turns a draft into a locally durable outbound message and job.
func (r *Repository) QueueDraft(ctx context.Context, draftID, mailboxID string, from model.Address) (messageID, threadID string, err error) {
	from, err = r.SenderForMailbox(ctx, mailboxID, from.Address)
	if err != nil {
		return "", "", err
	}
	draft, err := r.DraftByID(ctx, mailboxID, draftID)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	messageID = ids.New()
	threadID = draft.ThreadID
	domain := "localhost"
	if _, after, ok := strings.Cut(from.Address, "@"); ok {
		domain = after
	}
	rfcMessageID := "<" + messageID + "@" + domain + ">"
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		if threadID == "" {
			threadID = ids.New()
			_, err := tx.ExecContext(ctx, `INSERT INTO threads
                (id, mailbox_id, subject_norm, subject_display, latest_message_at, first_message_at,
                 message_count, unread_count, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, ?)`, threadID, mailboxID, mailx.NormalizeSubject(draft.Subject),
				draft.Subject, millis(now), millis(now), millis(now), millis(now))
			if err != nil {
				return err
			}
		} else {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads WHERE id = ? AND mailbox_id = ?", threadID, mailboxID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
		}
		var inReplyTo, references string
		if draft.ReplyToMessageID != "" {
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(rfc_message_id, ''), COALESCE(references_header, '')
				FROM messages WHERE id = ? AND mailbox_id = ?`, draft.ReplyToMessageID, mailboxID).Scan(&inReplyTo, &references)
			references = mailx.References(references, inReplyTo)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO messages
            (id, thread_id, mailbox_id, mailbox_address_id, direction, rfc_message_id, in_reply_to, references_header,
             from_name, from_address, subject, subject_norm, text_body, body_format, sent_at,
             created_at, updated_at, is_read, ingest_status, delivery_status, has_attachments)
            VALUES (?, ?, ?, (SELECT id FROM mailbox_addresses WHERE mailbox_id = ? AND LOWER(address) = LOWER(?) LIMIT 1), 'outbound', ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, 'text', ?, ?, ?, 1, 'ready', 'queued', ?)`,
			messageID, threadID, mailboxID, mailboxID, from.Address, rfcMessageID, inReplyTo, references, from.Name, from.Address,
			draft.Subject, mailx.NormalizeSubject(draft.Subject), draft.TextBody, millis(now), millis(now), millis(now), boolInt(len(draft.Attachments) > 0))
		if err != nil {
			return err
		}
		for kind, addresses := range map[string][]model.Address{"to": draft.To, "cc": draft.Cc, "bcc": draft.Bcc} {
			for index, address := range addresses {
				if _, err := tx.ExecContext(ctx, `INSERT INTO message_recipients
                    (id, message_id, recipient_type, name, address, sort_order) VALUES (?, ?, ?, ?, ?, ?)`,
					ids.New(), messageID, kind, address.Name, address.Address, index); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE attachments SET message_id = ?, draft_id = NULL WHERE draft_id = ?", messageID, draftID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET subject_norm = ?, subject_display = ?, latest_message_at = ?,
			message_count = message_count + 1, updated_at = ? WHERE id = ? AND mailbox_id = ?`, mailx.NormalizeSubject(draft.Subject),
			draft.Subject, millis(now), millis(now), threadID, mailboxID); err != nil {
			return err
		}
		recipientText := mailx.FormatAddresses(append(append(draft.To, draft.Cc...), draft.Bcc...))
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_search(message_id, thread_id, subject, sender, recipients, body)
            VALUES (?, ?, ?, ?, ?, ?)`, messageID, threadID, draft.Subject, from.Address, recipientText, draft.TextBody); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"message_id": messageID})
		if _, _, err := enqueueJob(ctx, tx, "send_outbound", "send:"+messageID, string(payload), 50, 10, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM drafts WHERE id = ? AND mailbox_id = ?", draftID, mailboxID)
		return err
	})
	return messageID, threadID, err
}

func (r *Repository) listAttachments(ctx context.Context, column, id string) ([]model.Attachment, error) {
	if column != "message_id" && column != "draft_id" {
		return nil, fmt.Errorf("invalid attachment owner")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, message_id, draft_id, provider_attachment_id, filename,
        safe_filename, content_type, content_disposition, content_id, storage_backend, storage_key,
        size_bytes, sha256, storage_status, created_at FROM attachments WHERE `+column+` = ? ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Attachment
	for rows.Next() {
		var attachment model.Attachment
		var messageID, draftID, providerID, contentID sql.NullString
		var createdAt int64
		if err := rows.Scan(&attachment.ID, &messageID, &draftID, &providerID, &attachment.Filename,
			&attachment.SafeFilename, &attachment.ContentType, &attachment.ContentDisposition, &contentID,
			&attachment.StorageBackend, &attachment.StorageKey, &attachment.SizeBytes, &attachment.SHA256,
			&attachment.StorageStatus, &createdAt); err != nil {
			return nil, err
		}
		attachment.MessageID, attachment.DraftID, attachment.ProviderAttachmentID, attachment.ContentID = messageID.String, draftID.String, providerID.String, contentID.String
		attachment.CreatedAt = fromMillis(createdAt)
		result = append(result, attachment)
	}
	return result, rows.Err()
}
