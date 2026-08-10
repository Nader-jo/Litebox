package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
)

// EnsureMailbox creates the configured primary mailbox without mutating an existing identity.
func (r *Repository) EnsureMailbox(ctx context.Context, address, displayName string) (string, error) {
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		return "", fmt.Errorf("parse mailbox: %w", err)
	}
	normalized := strings.ToLower(parsed.Address)
	local, domain, ok := strings.Cut(normalized, "@")
	if !ok {
		return "", fmt.Errorf("invalid mailbox address")
	}
	var existing string
	err = r.db.QueryRowContext(ctx, "SELECT id FROM mailboxes WHERE address = ?", normalized).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	now := time.Now().UTC()
	id := ids.New()
	_, err = r.db.ExecContext(ctx, `INSERT INTO mailboxes
        (id, address, local_part, domain, display_name, is_primary, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, id, normalized, local, domain, displayName, millis(now), millis(now))
	return id, err
}

// PrimaryMailboxID returns the configured primary mailbox database ID.
func (r *Repository) PrimaryMailboxID(ctx context.Context) (string, error) {
	var id string
	if err := r.db.QueryRowContext(ctx, "SELECT id FROM mailboxes WHERE is_primary = 1 LIMIT 1").Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return id, nil
}

// PrimaryMailbox returns the current persisted mailbox identity.
func (r *Repository) PrimaryMailbox(ctx context.Context) (model.Mailbox, error) {
	var mailbox model.Mailbox
	err := r.db.QueryRowContext(ctx, `SELECT id, address, display_name FROM mailboxes WHERE is_primary = 1 LIMIT 1`).
		Scan(&mailbox.ID, &mailbox.Address, &mailbox.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Mailbox{}, ErrNotFound
	}
	return mailbox, err
}

// UpdateMailboxDisplayName changes only the human-facing sender name.
func (r *Repository) UpdateMailboxDisplayName(ctx context.Context, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 128 {
		return fmt.Errorf("display name must be between 1 and 128 characters")
	}
	_, err := r.db.ExecContext(ctx, "UPDATE mailboxes SET display_name = ?, updated_at = ? WHERE is_primary = 1", displayName, millis(time.Now()))
	return err
}

// HasUsers reports whether first-run setup has completed.
func (r *Repository) HasUsers(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE disabled_at IS NULL").Scan(&count)
	return count > 0, err
}

// CreateFirstUser atomically creates the only MVP administrator.
func (r *Repository) CreateFirstUser(ctx context.Context, email, displayName, passwordHash string) (model.User, error) {
	now := time.Now().UTC()
	user := model.User{ID: ids.New(), Email: strings.ToLower(strings.TrimSpace(email)), DisplayName: strings.TrimSpace(displayName), PasswordHash: passwordHash, CreatedAt: now}
	err := r.Transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("setup is already complete")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO users
            (id, email, display_name, password_hash, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?)`, user.ID, user.Email, user.DisplayName, user.PasswordHash, millis(now), millis(now))
		return err
	})
	return user, err
}

// FindUserByEmail finds an enabled user using normalized email comparison.
func (r *Repository) FindUserByEmail(ctx context.Context, email string) (model.User, error) {
	var user model.User
	var createdAt int64
	var lastLogin sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT id, email, display_name, password_hash, created_at, last_login_at
        FROM users WHERE email = ? AND disabled_at IS NULL`, strings.ToLower(strings.TrimSpace(email))).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	user.CreatedAt = fromMillis(createdAt)
	user.LastLoginAt = nullableTime(lastLogin)
	return user, err
}

// UpdatePassword replaces a user's password and revokes all active sessions.
func (r *Repository) UpdatePassword(ctx context.Context, email, hash string) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE email = ? AND disabled_at IS NULL", hash, millis(time.Now()), strings.ToLower(strings.TrimSpace(email)))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE email = ?)", strings.ToLower(strings.TrimSpace(email)))
		return err
	})
}

// CreateSession persists only hashes of browser secrets.
func (r *Repository) CreateSession(ctx context.Context, userID string, tokenHash, csrfHash []byte, expiresAt time.Time, ipHash []byte, userAgent string) (string, error) {
	now := time.Now().UTC()
	id := ids.New()
	_, err := r.db.ExecContext(ctx, `INSERT INTO sessions
        (id, user_id, token_hash, csrf_token_hash, created_at, expires_at, last_seen_at, ip_hash, user_agent)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, userID, tokenHash, csrfHash, millis(now), millis(expiresAt), millis(now), ipHash, userAgent)
	return id, err
}

// FindSession authenticates a non-expired session from its hashed cookie.
func (r *Repository) FindSession(ctx context.Context, tokenHash []byte) (model.Session, error) {
	var session model.Session
	var sessionCreatedAt, userCreatedAt, expiresAt int64
	var lastLogin sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT s.id, s.csrf_token_hash, s.created_at, s.expires_at,
        u.id, u.email, u.display_name, u.password_hash, u.created_at, u.last_login_at
        FROM sessions s JOIN users u ON u.id = s.user_id
        WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`, tokenHash, millis(time.Now())).
		Scan(&session.ID, &session.CSRFHash, &sessionCreatedAt, &expiresAt, &session.User.ID, &session.User.Email,
			&session.User.DisplayName, &session.User.PasswordHash, &userCreatedAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, ErrNotFound
	}
	if err != nil {
		return model.Session{}, err
	}
	session.CreatedAt = fromMillis(sessionCreatedAt)
	session.ExpiresAt = fromMillis(expiresAt)
	session.User.CreatedAt = fromMillis(userCreatedAt)
	session.User.LastLoginAt = nullableTime(lastLogin)
	return session, nil
}

// TouchSession records bounded activity and login metadata.
func (r *Repository) TouchSession(ctx context.Context, sessionID, userID string, login bool) error {
	now := millis(time.Now())
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE id = ?", now, sessionID); err != nil {
			return err
		}
		if login {
			_, err := tx.ExecContext(ctx, "UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?", now, now, userID)
			return err
		}
		return nil
	})
}

// DeleteSession revokes one browser session.
func (r *Repository) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// CleanupSessions deletes expired session records.
func (r *Repository) CleanupSessions(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", millis(time.Now()))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
