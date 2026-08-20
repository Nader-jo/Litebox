// Package config loads and validates Litebox runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/http"
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
	maxConcurrentSendBytes  = int64(256 << 20)
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
	environment := strings.ToLower(env("APP_ENV", "development"))
	if environment != "development" && environment != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be development or production")
	}
	domain := strings.TrimSpace(os.Getenv("LITEBOX_DOMAIN"))
	baseURLValue := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
	if baseURLValue == "" && domain != "" {
		baseURLValue = "https://" + domain
	}
	if baseURLValue == "" {
		baseURLValue = "http://localhost:8080"
	}
	baseURL, err := ParseBaseURL(baseURLValue, environment == "production")
	if err != nil {
		return Config{}, fmt.Errorf("APP_BASE_URL: %w", err)
	}

	primary := strings.ToLower(strings.TrimSpace(env("MAILBOX_PRIMARY_ADDRESS", "hello@example.com")))
	allowed := parseList(env("MAILBOX_ALLOWED_RECIPIENTS", primary))
	cfg := Config{
		Environment:                environment,
		AllowUnconfigured:          true,
		Domain:                     domain,
		BaseURL:                    baseURL,
		ListenAddr:                 env("APP_LISTEN_ADDR", ":8080"),
		DataDir:                    dataDir,
		DBPath:                     env("APP_DB_PATH", filepath.Join(dataDir, "mailbox.db")),
		MasterKeyPath:              env("LITEBOX_MASTER_KEY_FILE", filepath.Join(dataDir, ".litebox", "master.key")),
		CookieName:                 env("APP_COOKIE_NAME", "litebox_session"),
		SessionTTL:                 durationValue("APP_SESSION_TTL_HOURS", 168, time.Hour),
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
		JobPollInterval:            durationValue("JOB_POLL_INTERVAL_MS", 500, time.Millisecond),
		JobLease:                   durationValue("JOB_LEASE_SECONDS", 120, time.Second),
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
	if c.Environment != "development" && c.Environment != "production" {
		problems = append(problems, errors.New("APP_ENV must be development or production"))
	}
	if strings.TrimSpace(c.Domain) != "" {
		if err := validateDomain(c.Domain); err != nil {
			problems = append(problems, err)
		}
	} else if c.Environment == "production" {
		problems = append(problems, errors.New("LITEBOX_DOMAIN is required in production"))
	}
	if err := validateBaseURL(c.BaseURL, c.Environment == "production"); err != nil {
		problems = append(problems, fmt.Errorf("APP_BASE_URL: %w", err))
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
	if c.ListenAddr != "" {
		_, port, err := net.SplitHostPort(c.ListenAddr)
		parsedPort, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			problems = append(problems, errors.New("APP_LISTEN_ADDR must include a valid non-zero port"))
		}
	}
	if c.CookieName != "" {
		if err := (&http.Cookie{Name: c.CookieName, Value: "value"}).Valid(); err != nil {
			problems = append(problems, fmt.Errorf("APP_COOKIE_NAME: %w", err))
		}
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		problems = append(problems, errors.New("APP_LOG_LEVEL must be debug, info, warn, or error"))
	}
	for _, value := range c.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(value); err != nil {
			problems = append(problems, fmt.Errorf("TRUSTED_PROXY_CIDRS contains invalid network %q", value))
		}
	}
	if c.SessionTTL <= 0 || c.MaxWebhookBodyBytes <= 0 || c.MaxMessageTextBytes <= 0 ||
		c.MaxUploadRequestBytes <= 0 || c.MaxOutboundAttachmentBytes <= 0 ||
		c.MaxAttachmentCount <= 0 || c.WorkerCount <= 0 || c.JobPollInterval <= 0 || c.JobLease <= 0 {
		problems = append(problems, errors.New("limits, session lifetime, and worker settings must be positive"))
	}
	if c.SessionTTL > 365*24*time.Hour {
		problems = append(problems, errors.New("APP_SESSION_TTL_HOURS must not exceed one year"))
	}
	if c.MaxWebhookBodyBytes > 16<<20 || c.MaxMessageTextBytes > 16<<20 ||
		c.MaxUploadRequestBytes > 128<<20 || c.MaxOutboundAttachmentBytes > 64<<20 {
		problems = append(problems, errors.New("configured byte limits exceed supported safety bounds"))
	}
	if c.MaxAttachmentCount > 100 || c.WorkerCount > 32 {
		problems = append(problems, errors.New("attachment count or worker count exceeds supported safety bounds"))
	}
	if c.MaxOutboundAttachmentBytes > 0 && int64(c.WorkerCount) > maxConcurrentSendBytes/c.MaxOutboundAttachmentBytes {
		problems = append(problems, errors.New("WORKER_COUNT and MAX_OUTBOUND_ATTACHMENT_BYTES exceed the 256 MiB concurrent send budget"))
	}
	if c.JobPollInterval > time.Hour || c.JobLease > 24*time.Hour {
		problems = append(problems, errors.New("job timing exceeds supported safety bounds"))
	}
	if c.JobPollInterval > 0 && c.JobPollInterval < 50*time.Millisecond {
		problems = append(problems, errors.New("JOB_POLL_INTERVAL_MS must be at least 50 milliseconds"))
	}
	if c.JobPollInterval > 0 && c.JobPollInterval <= time.Hour && c.WorkerCount > 0 &&
		int64(c.WorkerCount) > 64*int64(c.JobPollInterval)/int64(time.Second) {
		problems = append(problems, errors.New("WORKER_COUNT and JOB_POLL_INTERVAL_MS exceed the 64 claims-per-second polling budget"))
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
	if err != nil || parsed.Host != domain || parsed.Hostname() == "" || parsed.Port() != "" {
		return fmt.Errorf("LITEBOX_DOMAIN must be a valid hostname")
	}
	return nil
}

// ParseBaseURL parses a public application origin. Paths, credentials, query
// parameters, and fragments are rejected because Litebox constructs absolute
// links by appending route paths to this value.
func ParseBaseURL(value string, production bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("must be a valid absolute URL: %w", err)
	}
	if err := validateBaseURL(parsed, production); err != nil {
		return nil, err
	}
	parsed.Path = ""
	return parsed, nil
}

func validateBaseURL(value *url.URL, production bool) error {
	if value == nil || value.Host == "" || (value.Scheme != "http" && value.Scheme != "https") {
		return errors.New("must be an absolute HTTP(S) URL")
	}
	if value.User != nil || (value.Path != "" && value.Path != "/") || value.ForceQuery || value.RawQuery != "" || value.Fragment != "" {
		return errors.New("must contain only a scheme and host")
	}
	if production && value.Scheme != "https" {
		return errors.New("must use HTTPS in production")
	}
	return nil
}

// ApplySettings overlays the persisted installation snapshot on bootstrap
// configuration. Paths, listener, storage topology, and proxy trust remain
// bootstrap-controlled; product settings are database-backed.
func (c Config) ApplySettings(value settings.Values) (Config, error) {
	if value.BaseURL != "" {
		baseURL, err := ParseBaseURL(value.BaseURL, c.Environment == "production")
		if err != nil {
			return Config{}, fmt.Errorf("parse persisted base URL: %w", err)
		}
		c.BaseURL = baseURL
	}
	if value.SessionTTLHours > 0 {
		if value.SessionTTLHours > 365*24 {
			return Config{}, errors.New("persisted session lifetime exceeds one year")
		}
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
	if value.Configured {
		c.AllowUnconfigured = false
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

func durationValue(name string, fallback int64, unit time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return time.Duration(fallback) * unit
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > int64((1<<63-1)/unit) {
		return 0
	}
	return time.Duration(parsed) * unit
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
