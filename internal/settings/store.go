// Package settings persists installation-wide settings and encrypted provider
// credentials. It intentionally has no knowledge of HTTP or UI concerns.
package settings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/secrets"
)

const aadPrefix = "litebox/installation-settings/"

// Values is the validated, decrypted installation configuration snapshot.
type Values struct {
	Configured                 bool
	BaseURL                    string
	SessionTTLHours            int
	LogLevel                   string
	MaxWebhookBodyBytes        int64
	MaxMessageTextBytes        int64
	MaxUploadRequestBytes      int64
	MaxOutboundAttachmentBytes int64
	MaxAttachmentCount         int
	ResendAPIKey               string
	ResendWebhookSecret        string
	ResendDomainID             string
}

// Store owns SQL and encryption-key access for installation settings.
type Store struct {
	db  *sql.DB
	key []byte
}

func New(database *sql.DB, key []byte) *Store { return &Store{db: database, key: key} }

// Load reads the singleton settings row and decrypts provider credentials.
func (s *Store) Load(ctx context.Context) (Values, error) {
	var value Values
	var configured, ttl, maxAttachments int
	var encryptedKey, encryptedSecret, encryptedDomain []byte
	var baseURL, logLevel string
	if err := s.db.QueryRowContext(ctx, `SELECT configured, base_url, session_ttl_hours, log_level,
        max_webhook_body_bytes, max_message_text_bytes, max_upload_request_bytes,
        max_outbound_attachment_bytes, max_attachment_count, resend_api_key,
        resend_webhook_secret, resend_domain_id FROM installation_settings WHERE id = 1`).Scan(
		&configured, &baseURL, &ttl, &logLevel, &value.MaxWebhookBodyBytes, &value.MaxMessageTextBytes,
		&value.MaxUploadRequestBytes, &value.MaxOutboundAttachmentBytes, &maxAttachments,
		&encryptedKey, &encryptedSecret, &encryptedDomain); err != nil {
		return value, err
	}
	value.Configured, value.BaseURL, value.SessionTTLHours, value.LogLevel, value.MaxAttachmentCount = configured != 0, baseURL, ttl, logLevel, maxAttachments
	var err error
	if value.ResendAPIKey, err = decryptOptional(s.key, encryptedKey, "resend_api_key"); err != nil {
		return value, err
	}
	if value.ResendWebhookSecret, err = decryptOptional(s.key, encryptedSecret, "resend_webhook_secret"); err != nil {
		return value, err
	}
	if value.ResendDomainID, err = decryptOptional(s.key, encryptedDomain, "resend_domain_id"); err != nil {
		return value, err
	}
	return value, nil
}

// Save atomically replaces the editable settings and encrypted credentials.
func (s *Store) Save(ctx context.Context, value Values) error {
	return s.save(ctx, s.db, value)
}

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) save(ctx context.Context, executor contextExecer, value Values) error {
	key, err := encryptOptional(s.key, value.ResendAPIKey, "resend_api_key")
	if err != nil {
		return err
	}
	secret, err := encryptOptional(s.key, value.ResendWebhookSecret, "resend_webhook_secret")
	if err != nil {
		return err
	}
	domain, err := encryptOptional(s.key, value.ResendDomainID, "resend_domain_id")
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `UPDATE installation_settings SET configured = ?, base_url = ?,
        session_ttl_hours = ?, log_level = ?, max_webhook_body_bytes = ?, max_message_text_bytes = ?,
        max_upload_request_bytes = ?, max_outbound_attachment_bytes = ?, max_attachment_count = ?,
        resend_api_key = ?, resend_webhook_secret = ?, resend_domain_id = ?,
        setup_completed_at = CASE WHEN ? = 1 AND setup_completed_at IS NULL THEN ? ELSE setup_completed_at END,
        updated_at = ? WHERE id = 1`, boolInt(value.Configured), value.BaseURL, value.SessionTTLHours,
		value.LogLevel, value.MaxWebhookBodyBytes, value.MaxMessageTextBytes, value.MaxUploadRequestBytes,
		value.MaxOutboundAttachmentBytes, value.MaxAttachmentCount, key, secret, domain,
		boolInt(value.Configured), time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli())
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("installation settings row is missing")
	}
	return nil
}

// CompleteSetup atomically claims the unconfigured installation, applies the
// mailbox/user mutation, persists encrypted settings, and consumes the setup
// token. Any failure rolls the entire first-run transition back.
func (s *Store) CompleteSetup(ctx context.Context, token string, requireToken bool, value Values, mutate func(*sql.Tx) error) error {
	if !value.Configured {
		return errors.New("completed setup settings must be configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var result sql.Result
	if requireToken {
		hash := sha256.Sum256([]byte(token))
		result, err = tx.ExecContext(ctx, `UPDATE installation_settings
			SET setup_token_hash = NULL, setup_token_expires_at = NULL, updated_at = ?
			WHERE id = 1 AND configured = 0 AND setup_token_hash = ?
				AND setup_token_expires_at IS NOT NULL AND setup_token_expires_at > ?`,
			now.UnixMilli(), hash[:], now.UnixMilli())
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE installation_settings SET updated_at = ?
			WHERE id = 1 AND configured = 0`, now.UnixMilli())
	}
	if err != nil {
		return err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if claimed != 1 {
		return ErrSetupUnavailable
	}
	if mutate != nil {
		if err := mutate(tx); err != nil {
			return err
		}
	}
	if err := s.save(ctx, tx, value); err != nil {
		return err
	}
	return tx.Commit()
}

// ImportLegacy fills the database once from the old environment-driven config.
func (s *Store) ImportLegacy(ctx context.Context, value Values) error {
	current, err := s.Load(ctx)
	if err != nil {
		return err
	}
	if current.Configured || current.ResendAPIKey != "" || current.ResendWebhookSecret != "" {
		return nil
	}
	return s.Save(ctx, value)
}

// RotateSetupToken creates a short-lived single-use token for an unconfigured
// production instance. Restarting replaces the previous hash so a usable raw
// token can always be printed again without persisting it in plaintext.
func (s *Store) RotateSetupToken(ctx context.Context, lifetime time.Duration) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `UPDATE installation_settings SET setup_token_hash = ?, setup_token_expires_at = ?, updated_at = ? WHERE id = 1`, hash[:], time.Now().Add(lifetime).UnixMilli(), time.Now().UnixMilli())
	return token, err
}

func (s *Store) VerifySetupToken(ctx context.Context, token string) (bool, error) {
	var expected []byte
	var expires sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT setup_token_hash, setup_token_expires_at FROM installation_settings WHERE id = 1").Scan(&expected, &expires); err != nil {
		return false, err
	}
	if token == "" || len(expected) == 0 || !expires.Valid || time.Now().UnixMilli() >= expires.Int64 {
		return false, nil
	}
	hash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(hash[:], expected) == 1, nil
}

func (s *Store) ConsumeSetupToken(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "UPDATE installation_settings SET setup_token_hash = NULL, setup_token_expires_at = NULL, updated_at = ? WHERE id = 1", time.Now().UnixMilli())
	return err
}

func encryptOptional(key []byte, value, name string) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return secrets.Encrypt(key, value, aadPrefix+name)
}

func decryptOptional(key, value []byte, name string) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	return secrets.Decrypt(key, value, aadPrefix+name)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// RedactedFingerprint returns a stable, non-secret key marker for diagnostics.
func RedactedFingerprint(value string) string {
	if value == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hash[:4])
}

var ErrUnconfigured = errors.New("installation is not configured")

// ErrSetupUnavailable means first-run setup was already completed or the
// supplied production token was invalid, expired, or already consumed.
var ErrSetupUnavailable = errors.New("setup is unavailable")
