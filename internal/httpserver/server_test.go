package httpserver_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/app"
	"github.com/Nader-jo/Litebox/internal/config"
)

func TestFirstRunLoginAndCSRF(t *testing.T) {
	baseURL, _ := url.Parse("http://localhost:8080")
	root := t.TempDir()
	cfg := config.Config{Environment: "test", BaseURL: baseURL, ListenAddr: ":8080", DataDir: root,
		DBPath: filepath.Join(root, "mailbox.db"), CookieName: "test_session", SessionTTL: time.Hour, LogLevel: "error",
		PrimaryAddress: "hello@example.com", DisplayName: "Example", AllowedRecipients: map[string]struct{}{"hello@example.com": {}},
		ResendWebhookSecret: "whsec_" + base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
		StorageBackend:      "filesystem", StorageRoot: filepath.Join(root, "objects"), StorageTempRoot: filepath.Join(root, "tmp"),
		MaxWebhookBodyBytes: 1 << 20, MaxMessageTextBytes: 5 << 20, MaxUploadRequestBytes: 30 << 20,
		MaxOutboundAttachmentBytes: 25 << 20, MaxAttachmentCount: 20, WorkerCount: 1,
		JobPollInterval: time.Second, JobLease: time.Minute}
	application, err := app.New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	server := httptest.NewServer(application.Server.HTTP().Handler)
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	payload := []byte(`{"type":"email.received","created_at":"2026-08-10T10:00:00Z","data":{"email_id":"received-http-1"}}`)
	svixID, timestamp := "msg_http_duplicate", fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte("01234567890123456789012345678901"))
	_, _ = mac.Write([]byte(svixID + "." + timestamp + "." + string(payload)))
	signature := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for attempt := 0; attempt < 2; attempt++ {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/webhooks/resend", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("svix-id", svixID)
		request.Header.Set("svix-timestamp", timestamp)
		request.Header.Set("svix-signature", signature)
		response, err := client.Do(request)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("webhook attempt %d status=%v err=%v", attempt, responseStatus(response), err)
		}
		response.Body.Close()
	}
	webhooks, err := application.Repository.ListWebhooks(context.Background(), 10)
	if err != nil || len(webhooks) != 1 {
		t.Fatalf("duplicate webhook persistence len=%d err=%v", len(webhooks), err)
	}

	response, err := client.Get(server.URL + "/setup")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	response.Body.Close()
	setup := url.Values{"email": {"owner@example.com"}, "display_name": {"Owner"},
		"password": {"correct horse battery staple"}, "password_confirmation": {"correct horse battery staple"}}
	response, err = client.PostForm(server.URL+"/setup", setup)
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatal(response, err)
	}
	response.Body.Close()
	response, err = client.PostForm(server.URL+"/login", url.Values{"email": {"owner@example.com"}, "password": {"correct horse battery staple"}})
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatal(response, err)
	}
	response.Body.Close()

	response, err = client.Get(server.URL + "/inbox")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), "Litebox") || response.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing app shell or security headers")
	}
	csrf := ""
	serverURL, _ := url.Parse(server.URL)
	for _, cookie := range jar.Cookies(serverURL) {
		if cookie.Name == cfg.CookieName+"_csrf" {
			csrf = cookie.Value
		}
	}
	draft := url.Values{
		"csrf_token": {csrf},
		"to":         {"hello@example.com"},
		"subject":    {"Integration draft"},
		"body":       {"Saved locally."},
		"intent":     {"save"},
	}
	response, err = client.PostForm(server.URL+"/drafts", draft)
	if err != nil || response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/drafts" {
		t.Fatalf("save new draft status=%v location=%q err=%v", responseStatus(response), response.Header.Get("Location"), err)
	}
	response.Body.Close()
	drafts, err := application.Repository.ListDrafts(context.Background(), 10)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("list drafts len=%d err=%v", len(drafts), err)
	}
	update := url.Values{
		"csrf_token": {csrf},
		"to":         {"hello@example.com"},
		"subject":    {"Updated integration draft"},
		"body":       {"Still saved locally."},
		"intent":     {"save"},
	}
	request, _ := http.NewRequest(http.MethodPatch, server.URL+"/drafts/"+drafts[0].ID, strings.NewReader(update.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatalf("patch draft status=%v err=%v", responseStatus(response), err)
	}
	response.Body.Close()
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	if err := writer.WriteField("csrf_token", csrf); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("attachment", "report.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("<script>not executable</script>")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/drafts/"+drafts[0].ID+"/attachments", &upload)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload attachment status=%v err=%v", responseStatus(response), err)
	}
	response.Body.Close()
	storedDraft, err := application.Repository.DraftByID(context.Background(), drafts[0].ID)
	if err != nil || len(storedDraft.Attachments) != 1 || storedDraft.Attachments[0].StorageStatus != "ready" {
		t.Fatalf("stored attachment: %#v err=%v", storedDraft.Attachments, err)
	}
	response, err = client.Get(server.URL + "/attachments/" + storedDraft.Attachments[0].ID)
	if err != nil || response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download attachment status=%v disposition=%q err=%v", responseStatus(response), response.Header.Get("Content-Disposition"), err)
	}
	response.Body.Close()
	for _, path := range []string{"/admin/jobs", "/admin/webhooks", "/admin/storage"} {
		response, err = client.Get(server.URL + path)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%v err=%v", path, responseStatus(response), err)
		}
		response.Body.Close()
	}
	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/drafts/"+drafts[0].ID, nil)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete draft status=%v err=%v", responseStatus(response), err)
	}
	response.Body.Close()

	request, _ = http.NewRequest(http.MethodPost, server.URL+"/logout", nil)
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("CSRF-less logout status=%v err=%v", responseStatus(response), err)
	}
	response.Body.Close()
	response, err = client.PostForm(server.URL+"/logout", url.Values{"csrf_token": {csrf}})
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatal(responseStatus(response), err)
	}
	response.Body.Close()
}

func responseStatus(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}
