package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/resend/resend-go/v3"
)

// Resend implements Client using the official Go SDK and bounded HTTP downloads.
type Resend struct {
	client        *resend.Client
	http          *http.Client
	webhookSecret string
}

// NewResend creates a long-lived Resend adapter.
func NewResend(apiKey, webhookSecret string) *Resend {
	httpClient := &http.Client{Timeout: time.Minute}
	return &Resend{client: resend.NewCustomClient(httpClient, apiKey), http: httpClient, webhookSecret: webhookSecret}
}

// NewResendForTest creates an adapter with an injectable API endpoint and HTTP client.
func NewResendForTest(apiKey, webhookSecret string, httpClient *http.Client, baseURL *url.URL) *Resend {
	client := resend.NewCustomClient(httpClient, apiKey)
	client.BaseURL = baseURL
	return &Resend{client: client, http: httpClient, webhookSecret: webhookSecret}
}

// VerifyWebhook delegates raw-body signature verification to the official SDK.
func (r *Resend) VerifyWebhook(raw []byte, headers WebhookHeaders) error {
	err := r.client.Webhooks.Verify(&resend.VerifyWebhookOptions{
		Payload: string(raw), Headers: resend.WebhookHeaders{Id: headers.ID, Timestamp: headers.Timestamp, Signature: headers.Signature},
		WebhookSecret: r.webhookSecret,
	})
	if err != nil {
		return &Error{Kind: "authentication", Message: "invalid webhook signature", Cause: err}
	}
	return nil
}

// GetReceived retrieves normalized content and attachment metadata.
func (r *Resend) GetReceived(ctx context.Context, id string) (ReceivedEmail, error) {
	email, err := r.client.Emails.Receiving.GetWithContext(ctx, id)
	if err != nil {
		return ReceivedEmail{}, classify(err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, email.CreatedAt)
	if err != nil {
		return ReceivedEmail{}, &Error{Kind: "invalid_response", Message: "provider returned an invalid email timestamp", Cause: err}
	}
	result := ReceivedEmail{ID: email.Id, To: email.To, From: email.From, CreatedAt: createdAt.UTC(), Subject: email.Subject,
		HTML: email.Html, Text: email.Text, BCC: email.Bcc, CC: email.Cc, ReplyTo: email.ReplyTo, Headers: email.Headers,
		MessageID: email.MessageId, RawURL: email.Raw.DownloadUrl}
	for _, attachment := range email.Attachments {
		result.Attachments = append(result.Attachments, ReceivedAttachment{ID: attachment.Id, Filename: attachment.Filename,
			ContentType: attachment.ContentType, ContentDisposition: attachment.ContentDisposition, ContentID: attachment.ContentId, Size: -1})
	}
	return result, nil
}

// GetReceivedAttachment refreshes the provider's temporary attachment URL.
func (r *Resend) GetReceivedAttachment(ctx context.Context, emailID, attachmentID string) (ReceivedAttachment, error) {
	attachment, err := r.client.Emails.Receiving.GetAttachmentWithContext(ctx, emailID, attachmentID)
	if err != nil {
		return ReceivedAttachment{}, classify(err)
	}
	return ReceivedAttachment{ID: attachment.Id, Filename: attachment.Filename, ContentType: attachment.ContentType,
		ContentDisposition: attachment.ContentDisposition, ContentID: attachment.ContentId, DownloadURL: attachment.DownloadUrl, Size: -1}, nil
}

// Download opens a temporary provider URL for streaming into private storage.
func (r *Resend) Download(ctx context.Context, rawURL string) (io.ReadCloser, int64, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !isLocalTestURL(parsed)) {
		return nil, 0, &Error{Kind: "invalid_response", Message: "provider returned an invalid download URL", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, 0, &Error{Kind: "invalid_response", Message: "could not build provider download request", Cause: err}
	}
	response, err := r.http.Do(request)
	if err != nil {
		return nil, 0, classify(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		temporary := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return nil, 0, &Error{Kind: "download", Message: fmt.Sprintf("provider download returned HTTP %d", response.StatusCode), Temporary: temporary}
	}
	return response.Body, response.ContentLength, nil
}

// Send submits a bounded outbound message with a stable idempotency key.
func (r *Resend) Send(ctx context.Context, request SendRequest) (string, error) {
	attachments := make([]*resend.Attachment, 0, len(request.Attachments))
	for _, attachment := range request.Attachments {
		attachments = append(attachments, &resend.Attachment{Content: attachment.Content, Filename: attachment.Filename,
			ContentType: attachment.ContentType, ContentId: attachment.ContentID})
	}
	params := &resend.SendEmailRequest{
		From: formatAddress(request.From), To: formatAddressList(request.To), Cc: formatAddressList(request.CC),
		Bcc: formatAddressList(request.BCC), Subject: request.Subject, Text: request.Text, Headers: request.Headers,
		Attachments: attachments,
	}
	response, err := r.client.Emails.SendWithOptions(ctx, params, &resend.SendEmailOptions{IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		return "", classify(err)
	}
	if response == nil || response.Id == "" {
		return "", &Error{Kind: "invalid_response", Message: "provider accepted send without an email ID", Temporary: true}
	}
	return response.Id, nil
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, resend.ErrRateLimit) {
		return &Error{Kind: "rate_limited", Message: "email provider rate limit reached", Temporary: true, Cause: err}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return &Error{Kind: "network", Message: "email provider is temporarily unavailable", Temporary: true, Cause: err}
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unauthorized"), strings.Contains(message, "api key"), strings.Contains(message, "authentication"):
		return &Error{Kind: "authentication", Message: "email provider authentication failed", Cause: err}
	case strings.Contains(message, "invalid"), strings.Contains(message, "validation"), strings.Contains(message, "unprocessable"):
		return &Error{Kind: "validation", Message: "email provider rejected the message", Cause: err}
	case strings.Contains(message, "internal"), strings.Contains(message, "timeout"), strings.Contains(message, "temporar"):
		return &Error{Kind: "temporary", Message: "email provider is temporarily unavailable", Temporary: true, Cause: err}
	default:
		return &Error{Kind: "unknown", Message: "email provider request failed", Temporary: true, Cause: err}
	}
}

func formatAddress(address model.Address) string {
	return (&mail.Address{Name: address.Name, Address: address.Address}).String()
}

func formatAddressList(addresses []model.Address) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, formatAddress(address))
	}
	return result
}

func isLocalTestURL(value *url.URL) bool {
	host := strings.ToLower(value.Hostname())
	return value.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}
