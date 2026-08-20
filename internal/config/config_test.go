package config

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/settings"
)

func validTestConfig(t *testing.T) Config {
	t.Helper()
	baseURL, err := url.Parse("https://mail.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Environment: "production", Domain: "mail.example.com", BaseURL: baseURL, ListenAddr: ":8080", DataDir: "/data", DBPath: "/data/mailbox.db",
		CookieName: "litebox_session", SessionTTL: time.Hour, LogLevel: "info", PrimaryAddress: "hello@example.com",
		AllowedRecipients: map[string]struct{}{"hello@example.com": {}}, ResendAPIKey: "re_test", ResendWebhookSecret: "whsec_test",
		StorageBackend: "filesystem", StorageRoot: "/data/objects", StorageTempRoot: "/data/tmp",
		MaxWebhookBodyBytes: 1, MaxMessageTextBytes: 1, MaxUploadRequestBytes: 1, MaxOutboundAttachmentBytes: 1,
		MaxAttachmentCount: 1, WorkerCount: 1, JobPollInterval: 100 * time.Millisecond, JobLease: time.Second,
	}
}

func TestLoadRejectsEnvironmentTypos(t *testing.T) {
	t.Setenv("APP_ENV", "prodution")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("expected APP_ENV validation error, got %v", err)
	}
}

func TestValidateRejectsUnsafePublicURLAndRuntimeValues(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"base URL path":     func(cfg *Config) { cfg.BaseURL, _ = url.Parse("https://mail.example.com/subpath") },
		"empty URL query":   func(cfg *Config) { cfg.BaseURL, _ = url.Parse("https://mail.example.com?") },
		"unknown log level": func(cfg *Config) { cfg.LogLevel = "verbose" },
		"invalid cookie":    func(cfg *Config) { cfg.CookieName = "bad cookie" },
		"invalid listener":  func(cfg *Config) { cfg.ListenAddr = ":0" },
		"invalid proxy":     func(cfg *Config) { cfg.TrustedProxyCIDRs = []string{"not-a-network"} },
		"excess workers":    func(cfg *Config) { cfg.WorkerCount = 129 },
		"excess body limit": func(cfg *Config) { cfg.MaxUploadRequestBytes = 1<<30 + 1 },
		"excess job lease":  func(cfg *Config) { cfg.JobLease = 24*time.Hour + time.Second },
		"combined send memory": func(cfg *Config) {
			cfg.WorkerCount, cfg.MaxOutboundAttachmentBytes = 9, 32<<20
		},
		"combined poll churn": func(cfg *Config) {
			cfg.WorkerCount, cfg.JobPollInterval = 4, 50*time.Millisecond
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validTestConfig(t)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadRejectsOverflowingDurations(t *testing.T) {
	t.Setenv("APP_SESSION_TTL_HOURS", "9223372036854775807")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("expected overflowing duration validation error, got %v", err)
	}
}

func TestApplySettingsRejectsSessionDurationBeforeMultiplication(t *testing.T) {
	cfg := validTestConfig(t)
	_, err := cfg.ApplySettings(settings.Values{SessionTTLHours: int(^uint(0) >> 1)})
	if err == nil || !strings.Contains(err.Error(), "exceeds one year") {
		t.Fatalf("expected persisted duration bound error, got %v", err)
	}
}

func TestConfiguredSettingsRequireProductionCredentials(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.AllowUnconfigured = true
	cfg.ResendAPIKey, cfg.ResendWebhookSecret = "", ""
	_, err := cfg.ApplySettings(settings.Values{Configured: true, BaseURL: "https://mail.example.com", LogLevel: "info"})
	if err == nil || !strings.Contains(err.Error(), "RESEND_API_KEY") {
		t.Fatalf("expected configured production credentials to be required, got %v", err)
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
