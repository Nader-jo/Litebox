package config

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func validTestConfig(t *testing.T) Config {
	t.Helper()
	baseURL, err := url.Parse("https://mail.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Environment: "production", BaseURL: baseURL, ListenAddr: ":8080", DataDir: "/data", DBPath: "/data/mailbox.db",
		CookieName: "litebox_session", SessionTTL: time.Hour, PrimaryAddress: "hello@example.com",
		AllowedRecipients: map[string]struct{}{"hello@example.com": {}}, ResendAPIKey: "re_test", ResendWebhookSecret: "whsec_test",
		StorageBackend: "filesystem", StorageRoot: "/data/objects", StorageTempRoot: "/data/tmp",
		MaxWebhookBodyBytes: 1, MaxMessageTextBytes: 1, MaxUploadRequestBytes: 1, MaxOutboundAttachmentBytes: 1,
		MaxAttachmentCount: 1, WorkerCount: 1, JobPollInterval: time.Millisecond, JobLease: time.Second,
	}
}

func TestProductionValidation(t *testing.T) {
	cfg := validTestConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.AllowedRecipients["not-an-address"] = struct{}{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not-an-address") {
		t.Fatalf("expected invalid recipient error, got %v", err)
	}
	delete(cfg.AllowedRecipients, "not-an-address")
	cfg.ResendWebhookSecret = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "RESEND_WEBHOOK_SECRET") {
		t.Fatalf("expected missing webhook secret error, got %v", err)
	}
}
