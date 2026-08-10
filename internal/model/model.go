// Package model defines provider-neutral Litebox domain data.
package model

import "time"

// User is an authenticated person. Mailbox permissions are held by memberships.
type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// Session is an authenticated browser session. Raw tokens never enter this model.
type Session struct {
	ID         string
	User       User
	CSRFHash   []byte
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
}

// Address stores a mailbox address independently from its display label.
type Address struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ThreadSummary is the bounded projection used by mailbox lists.
type ThreadSummary struct {
	ID              string
	Subject         string
	Participants    string
	Snippet         string
	LatestMessageAt time.Time
	MessageCount    int
	UnreadCount     int
	IsArchived      bool
	IsStarred       bool
	IsTrashed       bool
	HasAttachments  bool
	Direction       string
	DeliveryStatus  string
}

// Thread is a conversation and its ordered messages.
type Thread struct {
	ID         string
	Subject    string
	IsArchived bool
	IsStarred  bool
	IsTrashed  bool
	Messages   []Message
}

// Message is one normalized inbound or outbound email.
type Message struct {
	ID                   string
	ThreadID             string
	Direction            string
	ResendEmailID        string
	RFCMessageID         string
	InReplyTo            string
	References           string
	From                 Address
	Recipients           map[string][]Address
	Subject              string
	TextBody             string
	SanitizedHTML        string
	BodyFormat           string
	RemoteImagesBlocked  bool
	RawStorageKey        string
	OccurredAt           time.Time
	IsRead               bool
	IngestStatus         string
	DeliveryStatus       string
	ProviderErrorCode    string
	ProviderErrorMessage string
	SizeBytes            int64
	Attachments          []Attachment
}

// Attachment describes a private immutable blob.
type Attachment struct {
	ID                   string
	MessageID            string
	DraftID              string
	ProviderAttachmentID string
	Filename             string
	SafeFilename         string
	ContentType          string
	ContentDisposition   string
	ContentID            string
	StorageBackend       string
	StorageKey           string
	SizeBytes            int64
	SHA256               string
	StorageStatus        string
	CreatedAt            time.Time
}

// Draft is an editable outbound email.
type Draft struct {
	ID               string
	ThreadID         string
	ReplyToMessageID string
	To               []Address
	Cc               []Address
	Bcc              []Address
	Subject          string
	TextBody         string
	Attachments      []Attachment
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Job is durable work claimed through a lease.
type Job struct {
	ID           string
	Kind         string
	DedupeKey    string
	PayloadJSON  string
	Status       string
	Priority     int
	AttemptCount int
	MaxAttempts  int
	RunAfter     time.Time
	LeasedUntil  *time.Time
	LeaseOwner   string
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WebhookEvent is a verified, immutable provider delivery event.
type WebhookEvent struct {
	ID               string
	SvixID           string
	EventType        string
	ResendEmailID    string
	RawPayload       string
	ReceivedAt       time.Time
	ProcessedAt      *time.Time
	ProcessingStatus string
	AttemptCount     int
	LastError        string
}

// SystemStats contains safe operational diagnostics for the administrator.
type SystemStats struct {
	PendingJobs          int
	DeadJobs             int
	FailedAttachments    int
	MissingRawMessages   int
	DatabaseBytes        int64
	WALBytes             int64
	LastWebhookAt        *time.Time
	LastSuccessfulIngest *time.Time
}

// Mailbox is an independent inbox. Address is its primary sender identity.
type Mailbox struct {
	ID              string
	Address         string
	DisplayName     string
	IsPrimary       bool
	InboundEnabled  bool
	OutboundEnabled bool
	Role            string
	Addresses       []MailboxAddress
}

// MailboxAddress is a primary address or alias routed to one mailbox.
type MailboxAddress struct {
	ID              string
	MailboxID       string
	Address         string
	DisplayName     string
	IsPrimary       bool
	InboundEnabled  bool
	OutboundEnabled bool
}

// MailboxMembership grants one user a role in one mailbox.
type MailboxMembership struct {
	User      User
	MailboxID string
	Role      string
	CreatedAt time.Time
}
