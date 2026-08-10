package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
