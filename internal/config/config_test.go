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
		Environment: "production", Domain: "mail.example.com", BaseURL: baseURL, ListenAddr: ":8080", DataDir: "/data", DBPath: "/data/mailbox.db",
		CookieName: "litebox_session", SessionTTL: time.Hour, PrimaryAddress: "hello@example.com",
		AllowedRecipients: map[string]struct{}{"hello@example.com": {}}, ResendAPIKey: "re_test", ResendWebhookSecret: "whsec_test",
		StorageBackend: "filesystem", StorageRoot: "/data/objects", StorageTempRoot: "/data/tmp",
		MaxWebhookBodyBytes: 1, MaxMessageTextBytes: 1, MaxUploadRequestBytes: 1, MaxOutboundAttachmentBytes: 1,
		MaxAttachmentCount: 1, WorkerCount: 1, JobPollInterval: time.Millisecond, JobLease: time.Second,
	}
}

func TestLoadUsesLiteboxDomainForPublicURL(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("LITEBOX_DOMAIN", "mail.example.com")
	t.Setenv("APP_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domain != "mail.example.com" || cfg.BaseURL.String() != "https://mail.example.com" {
		t.Fatalf("expected domain-derived public URL, got domain=%q base URL=%q", cfg.Domain, cfg.BaseURL)
	}
}

func TestProductionRequiresLiteboxDomain(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.Domain = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "LITEBOX_DOMAIN") {
		t.Fatalf("expected missing domain error, got %v", err)
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
