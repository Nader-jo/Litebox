// Package service coordinates mailbox workflows across repositories, providers, and blob storage.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	config     atomic.Pointer[config.Config]
	repository *repository.Repository
	store      blobstore.Store
	provider   provider.Client
}

// NewMailbox creates a mailbox service.
func NewMailbox(cfg config.Config, repo *repository.Repository, store blobstore.Store, client provider.Client) *Mailbox {
	mailbox := &Mailbox{repository: repo, store: store, provider: client}
	mailbox.ApplyConfig(cfg)
	return mailbox
}

// ApplyConfig updates settings that are safe to reload while the process runs.
func (s *Mailbox) ApplyConfig(cfg config.Config) {
	snapshot := cfg
	s.config.Store(&snapshot)
}

func (s *Mailbox) currentConfig() config.Config { return *s.config.Load() }

// JobHandlers returns every supported durable work kind.
func (s *Mailbox) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{
		"ingest_inbound":         s.ingestJob,
		"send_outbound":          s.sendJob,
		"process_provider_event": s.providerEventJob,
		"delete_objects":         s.deleteObjectsJob,
		"cleanup_sessions":       s.cleanupSessionsJob,
		"send_digest":            s.digestJob,
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
	cfg := s.currentConfig()
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
	bcc, err := parseProviderAddresses(received.BCC)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("provider returned invalid BCC recipients"))
	}
	localRecipients := append(append(append([]model.Address{}, to...), cc...), bcc...)
	mailboxes, err := s.repository.MailboxesForRecipients(ctx, localRecipients)
	if err != nil {
		return err
	}
	if len(mailboxes) == 0 {
		return s.repository.CompleteWebhook(ctx, webhookEventID, "ignored_unknown_recipient")
	}
	replyTo, _ := parseProviderAddresses(received.ReplyTo)
	fromValue := received.From
	if header := headerValue(received.Headers, "from"); header != "" {
		fromValue = header
	}
	from, err := mailx.ParseOne(fromValue)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("provider returned an invalid sender"))
	}
	if int64(len(received.Text)) > cfg.MaxMessageTextBytes {
		received.Text = received.Text[:cfg.MaxMessageTextBytes]
	}
	if int64(len(received.HTML)) > cfg.MaxMessageTextBytes {
		received.HTML = received.HTML[:cfg.MaxMessageTextBytes]
	}
	for _, mailbox := range mailboxes {
		address, addressErr := s.repository.MailboxAddressForRecipients(ctx, mailbox.ID, localRecipients)
		if addressErr != nil && !errors.Is(addressErr, repository.ErrNotFound) {
			return addressErr
		}
		if err := s.ingestIntoMailbox(ctx, cfg, mailbox, address.ID, received, from, to, cc, bcc, replyTo, bestEffort); err != nil {
			return err
		}
	}
	return s.repository.CompleteWebhook(ctx, webhookEventID, "succeeded")
}

func (s *Mailbox) ingestIntoMailbox(ctx context.Context, cfg config.Config, mailbox model.Mailbox, mailboxAddressID string, received provider.ReceivedEmail,
	from model.Address, to, cc, bcc, replyTo []model.Address, bestEffort bool) error {
	var attachments []model.Attachment
	attachmentMetadata := received.Attachments
	if len(attachmentMetadata) > cfg.MaxAttachmentCount {
		attachmentMetadata = attachmentMetadata[:cfg.MaxAttachmentCount]
	}
	cidRoutes := make(map[string]string)
	now := received.CreatedAt
	for _, metadata := range attachmentMetadata {
		attachmentID := ids.Stable("resend-attachment", mailbox.ID+"/"+received.ID+"/"+metadata.ID)
		if metadata.ContentID != "" {
			cidRoutes[strings.Trim(metadata.ContentID, "<>")] = "/attachments/" + attachmentID + "/inline"
		}
	}
	sanitized, blocked := mailx.SanitizeHTML(received.HTML, func(cid string) string { return cidRoutes[strings.Trim(cid, "<>")] })
	textBody := received.Text
	if textBody == "" && sanitized != "" {
		textBody = mailx.TextFromHTML(sanitized)
	}
	input := repository.InboundMessage{MailboxID: mailbox.ID, MailboxAddressID: mailboxAddressID, ResendEmailID: received.ID, RFCMessageID: received.MessageID,
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
		rawKey := blobstore.Key("raw", ids.Stable("resend-raw", mailbox.ID+"/"+received.ID), now)
		body, expected, downloadErr := s.provider.Download(ctx, received.RawURL)
		if downloadErr == nil {
			info, putErr := s.putInboundBlob(ctx, rawKey, body, expected, "message/rfc822", cfg.MaxUploadRequestBytes)
			body.Close()
			if putErr == nil {
				input.RawStorageKey, input.SizeBytes = rawKey, info.Size
			} else {
				downloadErr = putErr
			}
		}
		if downloadErr != nil {
			if errors.Is(downloadErr, errInboundBlobLimit) {
				input.IngestStatus = "ready_without_raw"
			} else if !bestEffort {
				return &SafeError{Message: "raw email archival failed", Temporary: true, Cause: downloadErr}
			} else {
				input.IngestStatus = "ready_without_raw"
			}
		}
	}
	remainingAttachmentBytes := cfg.MaxOutboundAttachmentBytes
	for _, metadata := range attachmentMetadata {
		attachmentID := ids.Stable("resend-attachment", mailbox.ID+"/"+received.ID+"/"+metadata.ID)
		attachment := model.Attachment{ID: attachmentID, ProviderAttachmentID: metadata.ID, Filename: metadata.Filename,
			SafeFilename: SafeFilename(metadata.Filename), ContentType: defaultContentType(metadata.ContentType),
			ContentDisposition: defaultDisposition(metadata.ContentDisposition), ContentID: strings.Trim(metadata.ContentID, "<>"),
			StorageBackend: "filesystem", StorageKey: blobstore.Key("attachments", attachmentID, now), CreatedAt: time.Now().UTC(), StorageStatus: "ready"}
		var getErr error
		var details provider.ReceivedAttachment
		if remainingAttachmentBytes <= 0 {
			getErr = errInboundBlobLimit
		} else {
			details, getErr = s.provider.GetReceivedAttachment(ctx, received.ID, metadata.ID)
		}
		if getErr == nil {
			body, expected, downloadErr := s.provider.Download(ctx, details.DownloadURL)
			if downloadErr == nil {
				info, putErr := s.putInboundBlob(ctx, attachment.StorageKey, body, expected, attachment.ContentType, remainingAttachmentBytes)
				body.Close()
				if putErr == nil {
					attachment.SizeBytes, attachment.SHA256 = info.Size, info.SHA256
					remainingAttachmentBytes -= info.Size
				} else {
					downloadErr = putErr
				}
			}
			getErr = downloadErr
		}
		if getErr != nil {
			if errors.Is(getErr, errInboundBlobLimit) {
				attachment.StorageStatus = "error"
			} else if !bestEffort {
				return &SafeError{Message: "attachment archival failed", Temporary: true, Cause: getErr}
			} else {
				attachment.StorageStatus = "error"
			}
		}
		attachments = append(attachments, attachment)
	}
	input.Attachments = attachments
	var err error
	input.ThreadID, err = s.repository.FindInboundThread(ctx, mailbox.ID, input.InReplyTo, input.References,
		mailx.NormalizeSubject(input.Subject), input.From.Address, input.ReceivedAt)
	if err != nil {
		return err
	}
	if _, _, err := s.repository.SaveInbound(ctx, input); err != nil {
		return err
	}
	return nil
}

var errInboundBlobLimit = errors.New("inbound blob limit exceeded")

func (s *Mailbox) putInboundBlob(ctx context.Context, key string, body io.Reader, expected int64, contentType string, limit int64) (blobstore.BlobInfo, error) {
	if limit < 0 || expected > limit {
		return blobstore.BlobInfo{}, errInboundBlobLimit
	}
	info, err := s.store.Put(ctx, key, &boundedInboundReader{reader: body, remaining: limit}, expected, contentType)
	if errors.Is(err, errInboundBlobLimit) {
		return blobstore.BlobInfo{}, errInboundBlobLimit
	}
	return info, err
}

type boundedInboundReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedInboundReader) Read(buffer []byte) (int, error) {
	if r.remaining < 0 {
		return 0, errInboundBlobLimit
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	if r.remaining < 0 {
		return count, errInboundBlobLimit
	}
	return count, err
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
	cfg := s.currentConfig()
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
		contents, err := io.ReadAll(io.LimitReader(body, cfg.MaxOutboundAttachmentBytes-total+1))
		body.Close()
		if err != nil {
			return &SafeError{Message: "outbound attachment could not be read", Temporary: true, Cause: err}
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(contents))
		if int64(len(contents)) != attachment.SizeBytes || attachment.SHA256 != "" && !strings.EqualFold(digest, attachment.SHA256) {
			_ = s.repository.MarkMessageSendError(ctx, message.ID, "attachment_integrity", "An attachment failed its integrity check.", true)
			return jobs.Permanent(fmt.Errorf("outbound attachment %q failed integrity validation", attachment.ID))
		}
		total += int64(len(contents))
		if total > cfg.MaxOutboundAttachmentBytes {
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

type digestPayload struct {
	SubscriptionID string `json:"subscription_id"`
	Since          int64  `json:"since"`
	Until          int64  `json:"until"`
}

func (s *Mailbox) digestJob(ctx context.Context, job model.Job) error {
	var payload digestPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.SubscriptionID == "" || payload.Until <= payload.Since {
		return jobs.Permanent(fmt.Errorf("invalid digest job payload"))
	}
	return s.SendDigest(ctx, payload.SubscriptionID, time.UnixMilli(payload.Since).UTC(), time.UnixMilli(payload.Until).UTC())
}

// SendDigest sends a metadata-only summary to the recipient configured by the
// user. It deliberately excludes subjects, senders, and message bodies.
func (s *Mailbox) SendDigest(ctx context.Context, subscriptionID string, since, until time.Time) error {
	sub, err := s.repository.DigestSubscriptionByID(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if !sub.Enabled {
		return jobs.Permanent(fmt.Errorf("digest subscription is disabled"))
	}
	counts, err := s.repository.DigestCounts(ctx, sub, since, until)
	if err != nil {
		return err
	}
	if err := s.repository.MarkDigestAttempt(ctx, sub.ID, counts, time.Now().UTC(), nil); err != nil {
		return err
	}
	err = s.sendDigest(ctx, sub, counts, "Litebox mailbox summary")
	if err != nil {
		_ = s.repository.MarkDigestAttempt(ctx, sub.ID, counts, time.Now().UTC(), err)
		return err
	}
	return s.repository.MarkDigestSent(ctx, sub.ID, until)
}

// SendDigestPreview sends the same metadata-only report immediately without
// changing the subscription schedule or last successful delivery timestamp.
func (s *Mailbox) SendDigestPreview(ctx context.Context, sub model.DigestSubscription, since, until time.Time) error {
	counts, err := s.repository.DigestCounts(ctx, sub, since, until)
	if err != nil {
		return err
	}
	return s.sendDigest(ctx, sub, counts, "Litebox mailbox summary preview")
}

func (s *Mailbox) sendDigest(ctx context.Context, sub model.DigestSubscription, counts model.DigestCounts, subject string) error {
	primary, err := s.repository.PrimaryMailbox(ctx)
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(sub.Timezone)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("invalid digest timezone %q", sub.Timezone))
	}
	body := fmt.Sprintf("Litebox mailbox summary\n\nPeriod: %s – %s\nReceived: %d\nUnread: %d\nSent: %d\n\n",
		counts.Since.In(location).Format("Jan 2, 2006 15:04"), counts.Until.In(location).Format("Jan 2, 2006 15:04"), counts.Received, counts.Unread, counts.Sent)
	for _, mailbox := range counts.ByMailbox {
		body += fmt.Sprintf("%s — received %d, unread %d, sent %d\n", mailbox.Address, mailbox.Received, mailbox.Unread, mailbox.Sent)
	}
	_, err = s.provider.Send(ctx, provider.SendRequest{From: model.Address{Name: primary.DisplayName, Address: primary.Address},
		To: []model.Address{{Address: sub.RecipientEmail}}, Subject: subject, Text: body,
		Headers: map[string]string{"X-Litebox-Digest": "1"}, IdempotencyKey: "litebox-digest/" + sub.ID + "/" + fmt.Sprint(counts.Until.Unix())})
	if err != nil {
		return err
	}
	return nil
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
	fail := func(err error) error {
		_ = s.repository.FailWebhook(ctx, event.ID, safeMessage(err))
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
		return fail(jobs.Permanent(fmt.Errorf("invalid provider event payload")))
	}
	if err := s.repository.StoreProviderEvent(ctx, event.SvixID, body.Data.EmailID, body.Type, event.RawPayload, body.CreatedAt); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// The provider event may precede the local post-send update; retry safely.
			return fail(&SafeError{Message: "outbound message is not yet reconciled", Temporary: true, Cause: err})
		}
		return fail(err)
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
