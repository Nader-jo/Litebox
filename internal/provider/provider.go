// Package provider defines the email-transport boundary used by application services.
package provider

import (
	"context"
	"io"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

// WebhookHeaders contains the three Svix signature headers.
type WebhookHeaders struct {
	ID        string
	Timestamp string
	Signature string
}

// ReceivedEmail is a provider-neutral retrieved inbound message.
type ReceivedEmail struct {
	ID          string
	To          []string
	From        string
	CreatedAt   time.Time
	Subject     string
	HTML        string
	Text        string
	BCC         []string
	CC          []string
	ReplyTo     []string
	Headers     map[string]string
	MessageID   string
	RawURL      string
	Attachments []ReceivedAttachment
}

// ReceivedAttachment is inbound attachment metadata plus a temporary URL.
type ReceivedAttachment struct {
	ID                 string
	Filename           string
	ContentType        string
	ContentDisposition string
	ContentID          string
	DownloadURL        string
	Size               int64
}

// OutboundAttachment is bounded content prepared for a provider SDK.
type OutboundAttachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Content     []byte
}

// SendRequest is one logical provider submission.
type SendRequest struct {
	From           model.Address
	To             []model.Address
	CC             []model.Address
	BCC            []model.Address
	Subject        string
	Text           string
	Headers        map[string]string
	Attachments    []OutboundAttachment
	IdempotencyKey string
}

// Client isolates Resend SDK models from the mailbox domain.
type Client interface {
	VerifyWebhook([]byte, WebhookHeaders) error
	GetReceived(context.Context, string) (ReceivedEmail, error)
	GetReceivedAttachment(context.Context, string, string) (ReceivedAttachment, error)
	Download(context.Context, string) (io.ReadCloser, int64, error)
	Send(context.Context, SendRequest) (string, error)
}

// Error classifies provider failures for retry behavior and safe UI messages.
type Error struct {
	Kind      string
	Message   string
	Temporary bool
	Cause     error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }
