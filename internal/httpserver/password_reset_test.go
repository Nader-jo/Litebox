package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/auth"
	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/service"
)

func TestPasswordResetResponseDoesNotWaitForProviderAndIsUniform(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	baseURL, err := url.Parse("http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Environment: "development", BaseURL: baseURL, ListenAddr: ":8080", DataDir: root,
		DBPath: filepath.Join(root, "mailbox.db"), CookieName: "test_session", SessionTTL: time.Hour, LogLevel: "error",
		PrimaryAddress: "hello@example.com", DisplayName: "Example", AllowedRecipients: map[string]struct{}{"hello@example.com": {}},
		StorageBackend: "filesystem", StorageRoot: filepath.Join(root, "objects"), StorageTempRoot: filepath.Join(root, "tmp"),
		MaxWebhookBodyBytes: 1 << 20, MaxMessageTextBytes: 5 << 20, MaxUploadRequestBytes: 30 << 20,
		MaxOutboundAttachmentBytes: 25 << 20, MaxAttachmentCount: 20, WorkerCount: 1,
		JobPollInterval: time.Second, JobLease: time.Minute,
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	if _, err := repo.EnsureMailbox(ctx, cfg.PrimaryAddress, cfg.DisplayName); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateFirstUser(ctx, "owner@example.com", "Owner", passwordHash); err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	blocked := newBlockingAccountEmailProvider()
	mailbox := service.NewMailbox(cfg, repo, store, blocked)
	server, err := New(cfg, repo, store, blocked, mailbox, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server.StartBackground()
	defer server.Close()
	httpServer := httptest.NewServer(server.HTTP().Handler)
	defer httpServer.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	hostile, err := http.NewRequest(http.MethodPost, httpServer.URL+"/login", strings.NewReader("email=owner%40example.com&password=irrelevant"))
	if err != nil {
		t.Fatal(err)
	}
	hostile.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hostile.Header.Set("Origin", "https://attacker.example")
	hostileResponse, err := client.Do(hostile)
	if err != nil {
		t.Fatal(err)
	}
	hostileResponse.Body.Close()
	if hostileResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d, want %d", hostileResponse.StatusCode, http.StatusForbidden)
	}

	knownStatus, knownDuration := postPasswordReset(t, client, httpServer.URL, "owner@example.com")
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("password-reset worker did not reach the blocked provider")
	}
	unknownStatus, unknownDuration := postPasswordReset(t, client, httpServer.URL, "missing@example.com")

	if knownStatus != http.StatusSeeOther || unknownStatus != knownStatus {
		t.Fatalf("known status=%d unknown status=%d, want uniform %d", knownStatus, unknownStatus, http.StatusSeeOther)
	}
	if knownDuration >= 750*time.Millisecond {
		t.Fatalf("known-account response waited %s for blocked provider", knownDuration)
	}
	delta := knownDuration - unknownDuration
	if delta < 0 {
		delta = -delta
	}
	if delta > 250*time.Millisecond {
		t.Fatalf("known/unknown response timing differs by %s (known=%s unknown=%s)", delta, knownDuration, unknownDuration)
	}

	close(blocked.release)
}

func postPasswordReset(t *testing.T, client *http.Client, baseURL, email string) (int, time.Duration) {
	t.Helper()
	started := time.Now()
	response, err := client.Post(baseURL+"/password-reset", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"email": {email}}.Encode()))
	duration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode, duration
}

type blockingAccountEmailProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingAccountEmailProvider() *blockingAccountEmailProvider {
	return &blockingAccountEmailProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (*blockingAccountEmailProvider) VerifyWebhook([]byte, provider.WebhookHeaders) error { return nil }

func (*blockingAccountEmailProvider) GetReceived(context.Context, string) (provider.ReceivedEmail, error) {
	return provider.ReceivedEmail{}, nil
}

func (*blockingAccountEmailProvider) GetReceivedAttachment(context.Context, string, string) (provider.ReceivedAttachment, error) {
	return provider.ReceivedAttachment{}, nil
}

func (*blockingAccountEmailProvider) Download(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}

func (p *blockingAccountEmailProvider) Send(ctx context.Context, _ provider.SendRequest) (string, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
		return "provider-message-id", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
