// Package config loads and validates Litebox runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/settings"
)

const (
	defaultDataDir          = ".data"
	defaultWebhookBodyBytes = int64(1 << 20)
	defaultUploadBodyBytes  = int64(30 << 20)
	defaultAttachmentBytes  = int64(25 << 20)
)

// Config contains all runtime configuration. Packages outside config do not read
// environment variables directly, which keeps validation and tests deterministic.
type Config struct {
	Environment string
	// AllowUnconfigured permits the first-run wizard to start without provider
	// credentials. It is set only by Load; hand-built production configs remain
	// strict and must include provider credentials.
	AllowUnconfigured bool
	// Domain is the public hostname used to bootstrap the installation. It is
	// intentionally kept as deployment configuration so the first-run link and
	// reverse proxy can agree before SQLite settings exist.
	Domain                     string
	BaseURL                    *url.URL
	ListenAddr                 string
	DataDir                    string
	DBPath                     string
	MasterKeyPath              string
	CookieName                 string
	SessionTTL                 time.Duration
	LogLevel                   string
	PrimaryAddress             string
	DisplayName                string
	AllowedRecipients          map[string]struct{}
	ResendAPIKey               string
	ResendWebhookSecret        string
	ResendDomainID             string
	StorageBackend             string
	StorageRoot                string
	StorageTempRoot            string
	MaxWebhookBodyBytes        int64
	MaxMessageTextBytes        int64
	MaxUploadRequestBytes      int64
	MaxOutboundAttachmentBytes int64
	MaxAttachmentCount         int
	WorkerCount                int
	JobPollInterval            time.Duration
	JobLease                   time.Duration
	TrustedProxyCIDRs          []string
}

// Load reads configuration from environment variables and applies safe local defaults.
func Load() (Config, error) {
	dataDir := env("APP_DATA_DIR", defaultDataDir)
	domain := strings.TrimSpace(os.Getenv("LITEBOX_DOMAIN"))
	baseURLValue := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
	if baseURLValue == "" && domain != "" {
		baseURLValue = "https://" + domain
	}
	if baseURLValue == "" {
		baseURLValue = "http://localhost:8080"
	}
	baseURL, err := url.Parse(baseURLValue)
	if err != nil {
		return Config{}, fmt.Errorf("parse APP_BASE_URL: %w", err)
	}

	primary := strings.ToLower(strings.TrimSpace(env("MAILBOX_PRIMARY_ADDRESS", "hello@example.com")))
	allowed := parseList(env("MAILBOX_ALLOWED_RECIPIENTS", primary))
	cfg := Config{
		Environment:                strings.ToLower(env("APP_ENV", "development")),
		AllowUnconfigured:          true,
		Domain:                     domain,
		BaseURL:                    baseURL,
		ListenAddr:                 env("APP_LISTEN_ADDR", ":8080"),
		DataDir:                    dataDir,
		DBPath:                     env("APP_DB_PATH", filepath.Join(dataDir, "mailbox.db")),
		MasterKeyPath:              env("LITEBOX_MASTER_KEY_FILE", filepath.Join(dataDir, ".litebox", "master.key")),
		CookieName:                 env("APP_COOKIE_NAME", "litebox_session"),
		SessionTTL:                 durationHours("APP_SESSION_TTL_HOURS", 168),
		LogLevel:                   strings.ToLower(env("APP_LOG_LEVEL", "info")),
		PrimaryAddress:             primary,
		DisplayName:                env("MAILBOX_DISPLAY_NAME", "Litebox"),
		AllowedRecipients:          make(map[string]struct{}, len(allowed)),
		ResendAPIKey:               strings.TrimSpace(os.Getenv("RESEND_API_KEY")),
		ResendWebhookSecret:        strings.TrimSpace(os.Getenv("RESEND_WEBHOOK_SECRET")),
		ResendDomainID:             strings.TrimSpace(os.Getenv("RESEND_DOMAIN_ID")),
		StorageBackend:             strings.ToLower(env("STORAGE_BACKEND", "filesystem")),
		StorageRoot:                env("STORAGE_ROOT", filepath.Join(dataDir, "objects")),
		StorageTempRoot:            env("STORAGE_TMP_ROOT", filepath.Join(dataDir, "tmp")),
		MaxWebhookBodyBytes:        int64Value("MAX_WEBHOOK_BODY_BYTES", defaultWebhookBodyBytes),
		MaxMessageTextBytes:        int64Value("MAX_MESSAGE_TEXT_BYTES", 5<<20),
		MaxUploadRequestBytes:      int64Value("MAX_UPLOAD_REQUEST_BYTES", defaultUploadBodyBytes),
		MaxOutboundAttachmentBytes: int64Value("MAX_OUTBOUND_ATTACHMENT_BYTES", defaultAttachmentBytes),
		MaxAttachmentCount:         intValue("MAX_ATTACHMENT_COUNT", 20),
		WorkerCount:                intValue("WORKER_COUNT", 2),
		JobPollInterval:            time.Duration(intValue("JOB_POLL_INTERVAL_MS", 500)) * time.Millisecond,
		JobLease:                   time.Duration(intValue("JOB_LEASE_SECONDS", 120)) * time.Second,
		TrustedProxyCIDRs:          parseList(os.Getenv("TRUSTED_PROXY_CIDRS")),
	}
	for _, address := range allowed {
		cfg.AllowedRecipients[strings.ToLower(address)] = struct{}{}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate reports all configuration errors that can be detected before startup.
func (c Config) Validate() error {
	var problems []error
	if strings.TrimSpace(c.Domain) != "" {
		if err := validateDomain(c.Domain); err != nil {
			problems = append(problems, err)
		}
	} else if c.Environment == "production" {
		problems = append(problems, errors.New("LITEBOX_DOMAIN is required in production"))
	}
	if c.BaseURL == nil || c.BaseURL.Scheme == "" || c.BaseURL.Host == "" {
		problems = append(problems, errors.New("APP_BASE_URL must be an absolute URL"))
	} else if c.Environment == "production" && c.BaseURL.Scheme != "https" {
		problems = append(problems, errors.New("APP_BASE_URL must use HTTPS in production"))
	}
	if _, err := mail.ParseAddress(c.PrimaryAddress); err != nil {
		problems = append(problems, fmt.Errorf("MAILBOX_PRIMARY_ADDRESS: %w", err))
	}
	if _, ok := c.AllowedRecipients[c.PrimaryAddress]; !ok {
		problems = append(problems, errors.New("MAILBOX_ALLOWED_RECIPIENTS must include MAILBOX_PRIMARY_ADDRESS"))
	}
	for address := range c.AllowedRecipients {
		parsed, err := mail.ParseAddress(address)
		if err != nil || !strings.EqualFold(parsed.Address, address) {
			problems = append(problems, fmt.Errorf("MAILBOX_ALLOWED_RECIPIENTS contains invalid address %q", address))
		}
	}
	if c.StorageBackend != "filesystem" {
		problems = append(problems, fmt.Errorf("unsupported STORAGE_BACKEND %q (only filesystem ships in the MVP)", c.StorageBackend))
	}
	if strings.TrimSpace(c.StorageRoot) == "" || strings.TrimSpace(c.StorageTempRoot) == "" {
		problems = append(problems, errors.New("STORAGE_ROOT and STORAGE_TMP_ROOT are required"))
	}
	if c.DBPath == "" || c.ListenAddr == "" || c.CookieName == "" {
		problems = append(problems, errors.New("APP_DB_PATH, APP_LISTEN_ADDR, and APP_COOKIE_NAME are required"))
	}
	if c.SessionTTL <= 0 || c.MaxWebhookBodyBytes <= 0 || c.MaxMessageTextBytes <= 0 ||
		c.MaxUploadRequestBytes <= 0 || c.MaxOutboundAttachmentBytes <= 0 ||
		c.MaxAttachmentCount <= 0 || c.WorkerCount <= 0 || c.JobPollInterval <= 0 || c.JobLease <= 0 {
		problems = append(problems, errors.New("limits, session lifetime, and worker settings must be positive"))
	}
	if c.Environment == "production" && !c.AllowUnconfigured {
		if strings.TrimSpace(c.ResendAPIKey) == "" {
			problems = append(problems, errors.New("RESEND_API_KEY is required in production"))
		}
		if strings.TrimSpace(c.ResendWebhookSecret) == "" {
			problems = append(problems, errors.New("RESEND_WEBHOOK_SECRET is required in production"))
		}
	}
	return errors.Join(problems...)
}

func validateDomain(value string) error {
	domain := strings.TrimSpace(value)
	if domain == "" || strings.Contains(domain, "://") || strings.ContainsAny(domain, "/?# \t\r\n") {
		return fmt.Errorf("LITEBOX_DOMAIN must be a hostname without a scheme or path")
	}
	parsed, err := url.Parse("https://" + domain)
	if err != nil || parsed.Host != domain || parsed.Hostname() == "" {
		return fmt.Errorf("LITEBOX_DOMAIN must be a valid hostname")
	}
	return nil
}

// ApplySettings overlays the persisted installation snapshot on bootstrap
// configuration. Paths, listener, storage topology, and proxy trust remain
// bootstrap-controlled; product settings are database-backed.
func (c Config) ApplySettings(value settings.Values) (Config, error) {
	if value.BaseURL != "" {
		baseURL, err := url.Parse(value.BaseURL)
		if err != nil {
			return Config{}, fmt.Errorf("parse persisted base URL: %w", err)
		}
		c.BaseURL = baseURL
	}
	if value.SessionTTLHours > 0 {
		c.SessionTTL = time.Duration(value.SessionTTLHours) * time.Hour
	}
	if value.LogLevel != "" {
		c.LogLevel = strings.ToLower(value.LogLevel)
	}
	if value.MaxWebhookBodyBytes > 0 {
		c.MaxWebhookBodyBytes = value.MaxWebhookBodyBytes
	}
	if value.MaxMessageTextBytes > 0 {
		c.MaxMessageTextBytes = value.MaxMessageTextBytes
	}
	if value.MaxUploadRequestBytes > 0 {
		c.MaxUploadRequestBytes = value.MaxUploadRequestBytes
	}
	if value.MaxOutboundAttachmentBytes > 0 {
		c.MaxOutboundAttachmentBytes = value.MaxOutboundAttachmentBytes
	}
	if value.MaxAttachmentCount > 0 {
		c.MaxAttachmentCount = value.MaxAttachmentCount
	}
	// Keep bootstrap credentials available to tests and legacy deployments until
	// the setup wizard has explicitly saved the database-backed snapshot.
	if value.Configured || value.ResendAPIKey != "" || value.ResendWebhookSecret != "" || value.ResendDomainID != "" {
		c.ResendAPIKey, c.ResendWebhookSecret, c.ResendDomainID = value.ResendAPIKey, value.ResendWebhookSecret, value.ResendDomainID
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// SecureCookies reports whether session cookies must carry the Secure attribute.
func (c Config) SecureCookies() bool {
	return c.BaseURL != nil && c.BaseURL.Scheme == "https"
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func intValue(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func int64Value(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func durationHours(name string, fallback int) time.Duration {
	return time.Duration(intValue(name, fallback)) * time.Hour
}

func parseList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
