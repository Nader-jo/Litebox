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
		if err := r.ensurePrimaryAddress(ctx, existing, normalized, local, domain, displayName); err != nil {
			return "", err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	now := time.Now().UTC()
	id := ids.New()
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mailboxes
			(id, address, local_part, domain, display_name, is_primary, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, id, normalized, local, domain, displayName, millis(now), millis(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO mailbox_addresses
			(id, mailbox_id, address, local_part, domain, display_name, is_primary, color, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, ids.New(), id, normalized, local, domain, displayName, colorForAddress(normalized), millis(now), millis(now))
		return err
	})
	return id, err
}

func (r *Repository) ensurePrimaryAddress(ctx context.Context, mailboxID, address, local, domain, displayName string) error {
	now := millis(time.Now())
	_, err := r.db.ExecContext(ctx, `INSERT INTO mailbox_addresses
		(id, mailbox_id, address, local_part, domain, display_name, is_primary, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET mailbox_id = excluded.mailbox_id, is_primary = 1,
			display_name = excluded.display_name, updated_at = excluded.updated_at`,
		ids.New(), mailboxID, address, local, domain, displayName, colorForAddress(address), now, now)
	return err
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

// UpdateMailboxDisplayName changes the human-facing name of one mailbox and its
// primary sender identity.
func (r *Repository) UpdateMailboxDisplayName(ctx context.Context, mailboxID, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 128 {
		return fmt.Errorf("display name must be between 1 and 128 characters")
	}
	now := millis(time.Now())
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE mailboxes SET display_name = ?, updated_at = ? WHERE id = ?", displayName, now, mailboxID)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE mailbox_addresses SET display_name = ?, updated_at = ?
			WHERE mailbox_id = ? AND is_primary = 1`, displayName, now, mailboxID)
		return err
	})
}

// HasUsers reports whether first-run setup has completed.
func (r *Repository) HasUsers(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE disabled_at IS NULL").Scan(&count)
	return count > 0, err
}

// CreateFirstUser atomically creates the installation owner and grants access to
// the bootstrapped primary mailbox.
func (r *Repository) CreateFirstUser(ctx context.Context, email, displayName, passwordHash string) (model.User, error) {
	user, err := initialUser(email, displayName, passwordHash, time.Now().UTC())
	if err != nil {
		return model.User{}, err
	}
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		return createFirstUserTx(ctx, tx, user)
	})
	return user, err
}

// CompleteInitialSetupTx changes the bootstrap mailbox and creates its first
// owner inside the caller's setup transaction.
func (r *Repository) CompleteInitialSetupTx(ctx context.Context, tx *sql.Tx, primaryAddress, mailboxName, email, displayName, passwordHash string) (model.User, error) {
	address, local, domain, err := normalizeAddress(primaryAddress)
	if err != nil {
		return model.User{}, err
	}
	mailboxName = strings.TrimSpace(mailboxName)
	if mailboxName == "" || len(mailboxName) > 128 {
		return model.User{}, fmt.Errorf("display name must be between 1 and 128 characters")
	}
	now := time.Now().UTC()
	user, err := initialUser(email, displayName, passwordHash, now)
	if err != nil {
		return model.User{}, err
	}
	if err := updatePrimaryMailboxTx(ctx, tx, address, local, domain, mailboxName); err != nil {
		return model.User{}, err
	}
	if err := createFirstUserTx(ctx, tx, user); err != nil {
		return model.User{}, err
	}
	return user, nil
}

func initialUser(email, displayName, passwordHash string, now time.Time) (model.User, error) {
	address, _, _, err := normalizeAddress(email)
	if err != nil {
		return model.User{}, fmt.Errorf("invalid administrator email")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 128 {
		return model.User{}, fmt.Errorf("display name must be between 1 and 128 characters")
	}
	if strings.TrimSpace(passwordHash) == "" {
		return model.User{}, fmt.Errorf("password hash is required")
	}
	return model.User{ID: ids.New(), Email: address, DisplayName: displayName, PasswordHash: passwordHash, CreatedAt: now.UTC()}, nil
}

func createFirstUserTx(ctx context.Context, tx *sql.Tx, user model.User) error {
	var mailboxID string
	if err := tx.QueryRowContext(ctx, `UPDATE mailboxes SET updated_at = updated_at
		WHERE id = (SELECT id FROM mailboxes WHERE is_primary = 1 LIMIT 1)
		RETURNING id`).Scan(&mailboxID); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("setup is already complete")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users
		(id, email, display_name, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, user.ID, user.Email, user.DisplayName, user.PasswordHash, millis(user.CreatedAt), millis(user.CreatedAt)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mailbox_memberships
		(user_id, mailbox_id, role, created_by, created_at, updated_at)
		VALUES (?, ?, 'owner', ?, ?, ?)`, user.ID, mailboxID, user.ID, millis(user.CreatedAt), millis(user.CreatedAt))
	return err
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
	var sessionCreatedAt, userCreatedAt, expiresAt, lastSeenAt int64
	var lastLogin sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT s.id, s.csrf_token_hash, s.created_at, s.expires_at, s.last_seen_at,
		COALESCE(s.user_agent, ''),
		u.id, u.email, u.display_name, u.password_hash, u.created_at, u.last_login_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`, tokenHash, millis(time.Now())).
		Scan(&session.ID, &session.CSRFHash, &sessionCreatedAt, &expiresAt, &lastSeenAt, &session.UserAgent,
			&session.User.ID, &session.User.Email,
			&session.User.DisplayName, &session.User.PasswordHash, &userCreatedAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, ErrNotFound
	}
	if err != nil {
		return model.Session{}, err
	}
	session.CreatedAt = fromMillis(sessionCreatedAt)
	session.ExpiresAt = fromMillis(expiresAt)
	session.LastSeenAt = fromMillis(lastSeenAt)
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
