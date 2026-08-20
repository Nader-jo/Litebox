package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/resend/resend-go/v3"
)

// Resend implements Client using the official Go SDK and bounded HTTP downloads.
type Resend struct {
	mu            sync.RWMutex
	client        *resend.Client
	http          *http.Client
	webhookSecret string
	allowLocal    bool
}

// NewResend creates a long-lived Resend adapter.
func NewResend(apiKey, webhookSecret string) *Resend {
	httpClient := guardedHTTPClient(&http.Client{Timeout: time.Minute}, false)
	return &Resend{client: resend.NewCustomClient(httpClient, apiKey), http: httpClient, webhookSecret: webhookSecret}
}

// UpdateCredentials replaces provider credentials for subsequent requests.
func (r *Resend) UpdateCredentials(apiKey, webhookSecret string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client = resend.NewCustomClient(r.http, apiKey)
	r.webhookSecret = webhookSecret
}

// NewResendForTest creates an adapter with an injectable API endpoint and HTTP client.
func NewResendForTest(apiKey, webhookSecret string, httpClient *http.Client, baseURL *url.URL) *Resend {
	httpClient = guardedHTTPClient(httpClient, true)
	client := resend.NewCustomClient(httpClient, apiKey)
	client.BaseURL = baseURL
	return &Resend{client: client, http: httpClient, webhookSecret: webhookSecret, allowLocal: true}
}

// VerifyWebhook delegates raw-body signature verification to the official SDK.
func (r *Resend) VerifyWebhook(raw []byte, headers WebhookHeaders) error {
	r.mu.RLock()
	client, secret := r.client, r.webhookSecret
	r.mu.RUnlock()
	err := client.Webhooks.Verify(&resend.VerifyWebhookOptions{
		Payload: string(raw), Headers: resend.WebhookHeaders{Id: headers.ID, Timestamp: headers.Timestamp, Signature: headers.Signature},
		WebhookSecret: secret,
	})
	if err != nil {
		return &Error{Kind: "authentication", Message: "invalid webhook signature", Cause: err}
	}
	return nil
}

// GetReceived retrieves normalized content and attachment metadata.
func (r *Resend) GetReceived(ctx context.Context, id string) (ReceivedEmail, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	htmlFormat := "cid"
	email, err := client.Emails.Receiving.GetWithOptions(ctx, id, &resend.GetReceivedEmailParams{HtmlFormat: &htmlFormat})
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
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	attachment, err := client.Emails.Receiving.GetAttachmentWithContext(ctx, emailID, attachmentID)
	if err != nil {
		return ReceivedAttachment{}, classify(err)
	}
	return ReceivedAttachment{ID: attachment.Id, Filename: attachment.Filename, ContentType: attachment.ContentType,
		ContentDisposition: attachment.ContentDisposition, ContentID: attachment.ContentId, DownloadURL: attachment.DownloadUrl, Size: -1}, nil
}

// Download opens a temporary provider URL for streaming into private storage.
func (r *Resend) Download(ctx context.Context, rawURL string) (io.ReadCloser, int64, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !validDownloadURL(parsed, r.allowLocal) {
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
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
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
	response, err := client.Emails.SendWithOptions(ctx, params, &resend.SendEmailOptions{IdempotencyKey: request.IdempotencyKey})
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

func guardedHTTPClient(base *http.Client, allowLocal bool) *http.Client {
	clone := *base
	var transport *http.Transport
	if configured, ok := clone.Transport.(*http.Transport); ok {
		transport = configured.Clone()
	} else {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	transport.Proxy = nil
	transport.DialContext = guardedDialContext(transport.DialContext, allowLocal, net.DefaultResolver.LookupIP)
	transport.DialTLSContext = nil
	clone.Transport = transport
	previous := clone.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !validDownloadURL(request.URL, allowLocal) {
			return errors.New("provider redirect target is not allowed")
		}
		if previous != nil {
			return previous(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

type ipLookup func(context.Context, string, string) ([]net.IP, error)

func guardedDialContext(base func(context.Context, string, string) (net.Conn, error), allowLocal bool, lookup ipLookup) func(context.Context, string, string) (net.Conn, error) {
	if base == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		base = dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid provider dial target: %w", err)
		}
		var addresses []net.IP
		if literal := net.ParseIP(host); literal != nil {
			addresses = []net.IP{literal}
		} else {
			addresses, err = lookup(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("resolve provider dial target: %w", err)
			}
		}
		var lastErr error
		for _, candidate := range addresses {
			if !allowedDialIP(host, candidate, allowLocal) {
				continue
			}
			connection, dialErr := base(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("provider dial target resolved only to restricted addresses")
	}
}

func allowedDialIP(host string, address net.IP, allowLocal bool) bool {
	literalHost := net.ParseIP(host)
	if allowLocal && (strings.EqualFold(host, "localhost") || literalHost != nil && literalHost.IsLoopback()) && address.IsLoopback() {
		return true
	}
	parsed, ok := netip.AddrFromSlice(address)
	if !ok {
		return false
	}
	parsed = parsed.Unmap()
	for _, prefix := range restrictedDialPrefixes {
		if prefix.Contains(parsed) {
			return false
		}
	}
	return address.IsGlobalUnicast() && !address.IsLoopback() && !address.IsPrivate() &&
		!address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsUnspecified()
}

var restrictedDialPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

func validDownloadURL(value *url.URL, allowLocal bool) bool {
	if value == nil || value.Host == "" {
		return false
	}
	host := strings.ToLower(value.Hostname())
	if allowLocal && value.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return true
	}
	if value.Scheme != "https" || host == "localhost" {
		return false
	}
	if address := net.ParseIP(host); address != nil && (address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsUnspecified()) {
		return false
	}
	return true
}
