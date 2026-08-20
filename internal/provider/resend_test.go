package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

func TestResendAdapterReceivingSendingAndVerification(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/emails/receiving/r1":
			if r.URL.Query().Get("html_format") != "cid" {
				t.Errorf("received email did not request CID HTML: %q", r.URL.RawQuery)
			}
			writeJSON(w, map[string]any{"id": "r1", "object": "email", "to": []string{"hello@example.com"},
				"from": "alice@example.com", "created_at": "2026-08-10T10:00:00Z", "subject": "Hello",
				"html": "<p>Hello</p>", "text": "Hello", "message_id": "<r1@example.com>",
				"headers":     map[string]string{"from": "Alice <alice@example.com>"},
				"raw":         map[string]string{"download_url": server.URL + "/download/raw"},
				"attachments": []map[string]string{{"id": "a1", "filename": "note.txt", "content_type": "text/plain"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/emails/receiving/r1/attachments/a1":
			writeJSON(w, map[string]any{"id": "a1", "filename": "note.txt", "content_type": "text/plain", "download_url": server.URL + "/download/a1"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/download/"):
			_, _ = io.WriteString(w, "downloaded")
		case r.Method == http.MethodPost && r.URL.Path == "/emails":
			if r.Header.Get("Idempotency-Key") != "litebox-send/message" {
				t.Errorf("missing idempotency header: %q", r.Header.Get("Idempotency-Key"))
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			writeJSON(w, map[string]string{"id": "sent-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL + "/")
	secretBytes := []byte("01234567890123456789012345678901")
	secret := "whsec_" + base64.StdEncoding.EncodeToString(secretBytes)
	adapter := NewResendForTest("re_test", secret, server.Client(), baseURL)
	received, err := adapter.GetReceived(context.Background(), "r1")
	if err != nil || received.ID != "r1" || len(received.Attachments) != 1 {
		t.Fatal(received, err)
	}
	attachment, err := adapter.GetReceivedAttachment(context.Background(), "r1", "a1")
	if err != nil || attachment.DownloadURL == "" {
		t.Fatal(attachment, err)
	}
	body, _, err := adapter.Download(context.Background(), attachment.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := io.ReadAll(body)
	body.Close()
	if string(contents) != "downloaded" {
		t.Fatalf("unexpected download %q", contents)
	}
	sentID, err := adapter.Send(context.Background(), SendRequest{From: model.Address{Name: "Example", Address: "hello@example.com"},
		To: []model.Address{{Address: "alice@example.com"}}, Subject: "Hello", Text: "Body", IdempotencyKey: "litebox-send/message"})
	if err != nil || sentID != "sent-1" {
		t.Fatal(sentID, err)
	}
	payload := []byte(`{"type":"email.received"}`)
	id := "msg_1"
	timestamp := time.Now().Unix()
	signed := id + "." + fmt.Sprint(timestamp) + "." + string(payload)
	mac := hmac.New(sha256.New, secretBytes)
	_, _ = mac.Write([]byte(signed))
	signature := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if err := adapter.VerifyWebhook(payload, WebhookHeaders{ID: id, Timestamp: fmt.Sprint(timestamp), Signature: signature}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.VerifyWebhook(payload, WebhookHeaders{ID: id, Timestamp: fmt.Sprint(timestamp), Signature: "v1,bad"}); err == nil {
		t.Fatal("expected invalid signature")
	}
}

func TestDownloadRejectsUnsafeRedirect(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/insecure", http.StatusFound)
	}))
	defer redirect.Close()
	baseURL, _ := url.Parse(redirect.URL + "/")
	adapter := NewResendForTest("re_test", "whsec_test", redirect.Client(), baseURL)
	if _, _, err := adapter.Download(context.Background(), redirect.URL); err == nil {
		t.Fatal("expected unsafe redirect to be rejected")
	}
}

func TestGuardedDialRejectsHostnameResolvingToPrivateAddress(t *testing.T) {
	dialed := false
	dial := guardedDialContext(func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}, false, func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.0.1")}, nil
	})
	if _, err := dial(context.Background(), "tcp", "provider.example:443"); err == nil {
		t.Fatal("expected private DNS results to be rejected")
	}
	if dialed {
		t.Fatal("restricted DNS result reached the network dialer")
	}
}

func TestAllowedDialIPRejectsSpecialPurposeNetworks(t *testing.T) {
	for _, value := range []string{"100.64.0.1", "198.18.0.1", "192.0.2.1", "203.0.113.1", "2001:db8::1"} {
		if allowedDialIP("provider.example", net.ParseIP(value), false) {
			t.Errorf("allowed special-purpose address %s", value)
		}
	}
	if !allowedDialIP("provider.example", net.ParseIP("8.8.8.8"), false) {
		t.Fatal("rejected a public unicast address")
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
