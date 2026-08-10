// Package service coordinates mailbox workflows across repositories, providers, and blob storage.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/jobs"
	mailx "github.com/Nader-jo/Litebox/internal/mail"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
)

// Mailbox owns recoverable application workflows.
type Mailbox struct {
	config     config.Config
	repository *repository.Repository
	store      blobstore.Store
	provider   provider.Client
	mailboxID  string
}

// NewMailbox creates a mailbox service.
func NewMailbox(cfg config.Config, repo *repository.Repository, store blobstore.Store, client provider.Client, mailboxID string) *Mailbox {
	return &Mailbox{config: cfg, repository: repo, store: store, provider: client, mailboxID: mailboxID}
}

// JobHandlers returns every supported durable work kind.
func (s *Mailbox) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{
		"ingest_inbound":         s.ingestJob,
		"send_outbound":          s.sendJob,
		"process_provider_event": s.providerEventJob,
		"delete_objects":         s.deleteObjectsJob,
		"cleanup_sessions":       s.cleanupSessionsJob,
	}
}

type inboundPayload struct {
	WebhookEventID string `json:"webhook_event_id"`
	ResendEmailID  string `json:"resend_email_id"`
}

func (s *Mailbox) ingestJob(ctx context.Context, job model.Job) error {
	var payload inboundPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.ResendEmailID == "" {
		return jobs.Permanent(fmt.Errorf("invalid inbound job payload"))
	}
	bestEffort := job.AttemptCount >= job.MaxAttempts
	if err := s.Ingest(ctx, payload.WebhookEventID, payload.ResendEmailID, bestEffort); err != nil {
		_ = s.repository.FailWebhook(ctx, payload.WebhookEventID, safeMessage(err))
		return err
	}
	return nil
}

// Ingest retrieves, archives, sanitizes, indexes, and threads one received email.
func (s *Mailbox) Ingest(ctx context.Context, webhookEventID, resendEmailID string, bestEffort bool) error {
	received, err := s.provider.GetReceived(ctx, resendEmailID)
	if err != nil {
		return err
	}
	to, err := parseProviderAddresses(received.To)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("provider returned invalid recipients"))
	}
	cc, err := parseProviderAddresses(received.CC)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("provider returned invalid CC recipients"))
	}
	if !mailx.IsAllowedRecipient(append(append([]model.Address{}, to...), cc...), s.config.AllowedRecipients) {
		return s.repository.CompleteWebhook(ctx, webhookEventID, "ignored_unknown_recipient")
	}
	bcc, _ := parseProviderAddresses(received.BCC)
	replyTo, _ := parseProviderAddresses(received.ReplyTo)
	fromValue := received.From
	if header := headerValue(received.Headers, "from"); header != "" {
		fromValue = header
	}
	from, err := mailx.ParseOne(fromValue)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("provider returned an invalid sender"))
	}
	textBody := received.Text
	if len(textBody) > int(s.config.MaxMessageTextBytes) {
		textBody = textBody[:s.config.MaxMessageTextBytes]
	}
	if len(received.HTML) > int(s.config.MaxMessageTextBytes) {
		received.HTML = received.HTML[:s.config.MaxMessageTextBytes]
	}
	var attachments []model.Attachment
	cidRoutes := make(map[string]string)
	now := received.CreatedAt
	for _, metadata := range received.Attachments {
		attachmentID := ids.Stable("resend-attachment", received.ID+"/"+metadata.ID)
		if metadata.ContentID != "" {
			cidRoutes[strings.Trim(metadata.ContentID, "<>")] = "/attachments/" + attachmentID + "/inline"
		}
	}
	sanitized, blocked := mailx.SanitizeHTML(received.HTML, func(cid string) string { return cidRoutes[strings.Trim(cid, "<>")] })
	if textBody == "" && sanitized != "" {
		textBody = mailx.TextFromHTML(sanitized)
	}
	input := repository.InboundMessage{MailboxID: s.mailboxID, ResendEmailID: received.ID, RFCMessageID: received.MessageID,
		InReplyTo: headerValue(received.Headers, "in-reply-to"), References: headerValue(received.Headers, "references"),
		From: from, Recipients: map[string][]model.Address{"to": to, "cc": cc, "bcc": bcc, "reply_to": replyTo},
		Subject: cleanHeader(received.Subject), TextBody: textBody, SanitizedHTML: sanitized, RemoteImagesBlocked: blocked,
		ReceivedAt: now, IngestStatus: "ready"}
	if received.RawURL == "" {
		if !bestEffort {
			return &SafeError{Message: "raw email archival failed", Temporary: true, Cause: errors.New("provider response omitted raw email URL")}
		}
		input.IngestStatus = "ready_without_raw"
	} else {
		rawKey := blobstore.Key("raw", ids.Stable("resend-raw", received.ID), now)
		body, expected, downloadErr := s.provider.Download(ctx, received.RawURL)
		if downloadErr == nil {
			info, putErr := s.store.Put(ctx, rawKey, body, expected, "message/rfc822")
			body.Close()
			if putErr == nil {
				input.RawStorageKey, input.SizeBytes = rawKey, info.Size
			} else {
				downloadErr = putErr
			}
		}
		if downloadErr != nil {
			if !bestEffort {
				return &SafeError{Message: "raw email archival failed", Temporary: true, Cause: downloadErr}
			}
			input.IngestStatus = "ready_without_raw"
		}
	}
	for _, metadata := range received.Attachments {
		attachmentID := ids.Stable("resend-attachment", received.ID+"/"+metadata.ID)
		attachment := model.Attachment{ID: attachmentID, ProviderAttachmentID: metadata.ID, Filename: metadata.Filename,
			SafeFilename: SafeFilename(metadata.Filename), ContentType: defaultContentType(metadata.ContentType),
			ContentDisposition: defaultDisposition(metadata.ContentDisposition), ContentID: strings.Trim(metadata.ContentID, "<>"),
			StorageBackend: "filesystem", StorageKey: blobstore.Key("attachments", attachmentID, now), CreatedAt: time.Now().UTC(), StorageStatus: "ready"}
		details, getErr := s.provider.GetReceivedAttachment(ctx, received.ID, metadata.ID)
		if getErr == nil {
			body, expected, downloadErr := s.provider.Download(ctx, details.DownloadURL)
			if downloadErr == nil {
				info, putErr := s.store.Put(ctx, attachment.StorageKey, body, expected, attachment.ContentType)
				body.Close()
				if putErr == nil {
					attachment.SizeBytes, attachment.SHA256 = info.Size, info.SHA256
				} else {
					downloadErr = putErr
				}
			}
			getErr = downloadErr
		}
		if getErr != nil {
			if !bestEffort {
				return &SafeError{Message: "attachment archival failed", Temporary: true, Cause: getErr}
			}
			attachment.StorageStatus = "error"
		}
		attachments = append(attachments, attachment)
	}
	input.Attachments = attachments
	input.ThreadID, err = s.repository.FindInboundThread(ctx, input.InReplyTo, input.References,
		mailx.NormalizeSubject(input.Subject), input.From.Address, input.ReceivedAt)
	if err != nil {
		return err
	}
	if _, _, err := s.repository.SaveInbound(ctx, input); err != nil {
		return err
	}
	return s.repository.CompleteWebhook(ctx, webhookEventID, "succeeded")
}

type sendPayload struct {
	MessageID string `json:"message_id"`
}

func (s *Mailbox) sendJob(ctx context.Context, job model.Job) error {
	var payload sendPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.MessageID == "" {
		return jobs.Permanent(fmt.Errorf("invalid outbound job payload"))
	}
	return s.Send(ctx, payload.MessageID)
}

// Send submits one queued message with stable application and provider idempotency.
func (s *Mailbox) Send(ctx context.Context, messageID string) error {
	message, err := s.repository.OutboundMessage(ctx, messageID)
	if err != nil {
		return err
	}
	if message.ResendEmailID != "" {
		return nil
	}
	request := provider.SendRequest{From: message.From, To: message.Recipients["to"], CC: message.Recipients["cc"],
		BCC: message.Recipients["bcc"], Subject: message.Subject, Text: message.TextBody,
		Headers: map[string]string{}, IdempotencyKey: "litebox-send/" + message.ID}
	if message.InReplyTo != "" {
		request.Headers["In-Reply-To"] = message.InReplyTo
	}
	if message.References != "" {
		request.Headers["References"] = message.References
	}
	var total int64
	for _, attachment := range message.Attachments {
		body, _, err := s.store.Get(ctx, attachment.StorageKey)
		if err != nil {
			return &SafeError{Message: "outbound attachment storage is unavailable", Temporary: true, Cause: err}
		}
		contents, err := io.ReadAll(io.LimitReader(body, s.config.MaxOutboundAttachmentBytes-total+1))
		body.Close()
		if err != nil {
			return &SafeError{Message: "outbound attachment could not be read", Temporary: true, Cause: err}
		}
		total += int64(len(contents))
		if total > s.config.MaxOutboundAttachmentBytes {
			_ = s.repository.MarkMessageSendError(ctx, message.ID, "attachment_limit", "Attachments exceed the configured limit.", true)
			return jobs.Permanent(fmt.Errorf("outbound attachments exceed configured limit"))
		}
		request.Attachments = append(request.Attachments, provider.OutboundAttachment{Filename: attachment.Filename,
			ContentType: attachment.ContentType, ContentID: attachment.ContentID, Content: contents})
	}
	resendID, err := s.provider.Send(ctx, request)
	if err != nil {
		permanent := false
		var providerError *provider.Error
		if errors.As(err, &providerError) {
			permanent = !providerError.Temporary
			_ = s.repository.MarkMessageSendError(ctx, message.ID, providerError.Kind, providerError.Message, permanent)
			if permanent {
				return jobs.Permanent(err)
			}
		}
		return err
	}
	return s.repository.MarkMessageSubmitted(ctx, message.ID, resendID)
}

type providerEventPayload struct {
	WebhookEventID string `json:"webhook_event_id"`
}

func (s *Mailbox) providerEventJob(ctx context.Context, job model.Job) error {
	var payload providerEventPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.WebhookEventID == "" {
		return jobs.Permanent(fmt.Errorf("invalid provider event job payload"))
	}
	event, err := s.repository.WebhookByID(ctx, payload.WebhookEventID)
	if err != nil {
		return err
	}
	var body struct {
		Type      string    `json:"type"`
		CreatedAt time.Time `json:"created_at"`
		Data      struct {
			EmailID string `json:"email_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(event.RawPayload), &body); err != nil || body.Data.EmailID == "" {
		return jobs.Permanent(fmt.Errorf("invalid provider event payload"))
	}
	current, currentAt, err := s.repository.MessageDelivery(ctx, body.Data.EmailID)
	if errors.Is(err, repository.ErrNotFound) {
		// The provider event may precede the local post-send update; retry safely.
		return &SafeError{Message: "outbound message is not yet reconciled", Temporary: true, Cause: err}
	}
	if err != nil {
		return err
	}
	status := mailx.ReduceDelivery(current, currentAt, body.Type, body.CreatedAt)
	if err := s.repository.StoreProviderEvent(ctx, event.SvixID, body.Data.EmailID, body.Type, event.RawPayload, status, body.CreatedAt); err != nil {
		return err
	}
	return s.repository.CompleteWebhook(ctx, event.ID, "succeeded")
}

func (s *Mailbox) deleteObjectsJob(ctx context.Context, job model.Job) error {
	var payload struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid delete job payload"))
	}
	for _, key := range payload.Keys {
		if err := s.store.Delete(ctx, key); err != nil {
			return &SafeError{Message: "private object deletion failed", Temporary: true, Cause: err}
		}
	}
	return nil
}

func (s *Mailbox) cleanupSessionsJob(ctx context.Context, _ model.Job) error {
	_, err := s.repository.CleanupSessions(ctx)
	return err
}

// SafeError separates administrator-safe context from detailed structured logs.
type SafeError struct {
	Message   string
	Temporary bool
	Cause     error
}

func (e *SafeError) Error() string       { return e.Message + ": " + e.Cause.Error() }
func (e *SafeError) Unwrap() error       { return e.Cause }
func (e *SafeError) SafeMessage() string { return e.Message }

func safeMessage(err error) string {
	type safe interface{ SafeMessage() string }
	var value safe
	if errors.As(err, &value) {
		return value.SafeMessage()
	}
	var providerError *provider.Error
	if errors.As(err, &providerError) {
		return providerError.Message
	}
	return "processing failed; inspect structured server logs"
}

// SafeFilename creates a display-only fallback and never participates in a storage path.
func SafeFilename(filename string) string {
	filename = filepath.Base(strings.Map(func(value rune) rune {
		if value < 32 || value == 127 || value == '/' || value == '\\' {
			return -1
		}
		return value
	}, strings.TrimSpace(filename)))
	if filename == "" || filename == "." {
		return "attachment"
	}
	return filename
}

func parseProviderAddresses(values []string) ([]model.Address, error) {
	var result []model.Address
	for _, value := range values {
		parsed, err := mailx.ParseAddresses(value)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed...)
	}
	return result, nil
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return cleanHeader(value)
		}
	}
	return ""
}

func cleanHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func defaultContentType(value string) string {
	if parsed, _, err := mime.ParseMediaType(value); err == nil && parsed != "" {
		return parsed
	}
	return "application/octet-stream"
}

func defaultDisposition(value string) string {
	if strings.EqualFold(value, "inline") {
		return "inline"
	}
	return "attachment"
}

// BytesReader is retained for provider fakes and streaming tests.
func BytesReader(value []byte) io.Reader { return bytes.NewReader(value) }
