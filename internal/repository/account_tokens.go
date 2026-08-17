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

// CreateInvitation stores a hashed, expiring invitation for one mailbox.
// The caller is responsible for delivering the returned raw token.
func (r *Repository) CreateInvitation(ctx context.Context, createdBy, mailboxID, email, displayName, role string, tokenHash []byte, expiresAt time.Time) (model.Invitation, error) {
	email = strings.TrimSpace(email)
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return model.Invitation{}, fmt.Errorf("invitation email must be valid")
	}
	email = strings.ToLower(parsed.Address)
	if role == "" {
		role = "member"
	}
	if role != "owner" && role != "admin" && role != "member" && role != "viewer" {
		return model.Invitation{}, fmt.Errorf("invalid invitation role")
	}
	if role == "owner" {
		return model.Invitation{}, fmt.Errorf("owners must be granted by an existing owner")
	}
	mailbox, err := r.MailboxByID(ctx, mailboxID)
	if err != nil {
		return model.Invitation{}, err
	}
	now := time.Now().UTC()
	invitation := model.Invitation{ID: ids.New(), Email: email, DisplayName: strings.TrimSpace(displayName), MailboxID: mailboxID,
		MailboxName: mailbox.DisplayName, Role: role, ExpiresAt: expiresAt.UTC()}
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE account_tokens SET used_at = ? WHERE token_type = 'invitation' AND mailbox_id = ? AND email = ? AND used_at IS NULL`, millis(now), mailboxID, email); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO account_tokens
			(id, token_hash, token_type, mailbox_id, email, display_name, role, created_by, expires_at, created_at)
			VALUES (?, ?, 'invitation', ?, ?, ?, ?, ?, ?, ?)`, invitation.ID, tokenHash, mailboxID, email, invitation.DisplayName, role, createdBy, millis(expiresAt), millis(now))
		return err
	})
	return invitation, err
}

// InvitationByToken returns an unexpired, unused invitation.
func (r *Repository) InvitationByToken(ctx context.Context, tokenHash []byte) (model.Invitation, error) {
	var invitation model.Invitation
	var expires int64
	err := r.db.QueryRowContext(ctx, `SELECT t.id, t.email, t.display_name, t.mailbox_id, m.display_name, t.role, t.expires_at
		FROM account_tokens t JOIN mailboxes m ON m.id = t.mailbox_id
		WHERE t.token_hash = ? AND t.token_type = 'invitation' AND t.used_at IS NULL AND t.expires_at > ?`, tokenHash, millis(time.Now())).
		Scan(&invitation.ID, &invitation.Email, &invitation.DisplayName, &invitation.MailboxID, &invitation.MailboxName, &invitation.Role, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Invitation{}, ErrNotFound
	}
	if err != nil {
		return model.Invitation{}, err
	}
	invitation.ExpiresAt = fromMillis(expires)
	return invitation, nil
}

// AcceptInvitation atomically creates or reuses the invited user, grants
// mailbox access, and consumes the single-use token.
func (r *Repository) AcceptInvitation(ctx context.Context, tokenHash []byte, passwordHash string) (model.User, error) {
	var user model.User
	now := time.Now().UTC()
	err := r.Transaction(ctx, func(tx *sql.Tx) error {
		var invitation model.Invitation
		var expires int64
		if err := tx.QueryRowContext(ctx, `SELECT id, email, display_name, mailbox_id, role, expires_at
			FROM account_tokens WHERE token_hash = ? AND token_type = 'invitation' AND used_at IS NULL AND expires_at > ?`, tokenHash, millis(now)).
			Scan(&invitation.ID, &invitation.Email, &invitation.DisplayName, &invitation.MailboxID, &invitation.Role, &expires); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var createdAt, lastLogin sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT id, email, display_name, password_hash, created_at, last_login_at FROM users WHERE email = ? AND disabled_at IS NULL`, invitation.Email).
			Scan(&user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &createdAt, &lastLogin)
		if errors.Is(err, sql.ErrNoRows) {
			user = model.User{ID: ids.New(), Email: invitation.Email, DisplayName: invitation.DisplayName, PasswordHash: passwordHash, CreatedAt: now}
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, email, display_name, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, user.ID, user.Email, user.DisplayName, user.PasswordHash, millis(now), millis(now)); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			user.CreatedAt = fromMillis(createdAt.Int64)
			user.LastLoginAt = nullableTime(lastLogin)
		}
		role := invitation.Role
		var existingRole string
		if roleErr := tx.QueryRowContext(ctx, "SELECT role FROM mailbox_memberships WHERE user_id = ? AND mailbox_id = ?", user.ID, invitation.MailboxID).Scan(&existingRole); roleErr == nil && roleRank(existingRole) > roleRank(role) {
			role = existingRole
		} else if roleErr != nil && !errors.Is(roleErr, sql.ErrNoRows) {
			return roleErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mailbox_memberships(user_id, mailbox_id, role, created_by, created_at, updated_at)
			VALUES (?, ?, ?, NULL, ?, ?) ON CONFLICT(user_id, mailbox_id) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`, user.ID, invitation.MailboxID, role, millis(now), millis(now))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE account_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL", millis(now), invitation.ID)
		return err
	})
	return user, err
}

func roleRank(role string) int {
	switch role {
	case "owner":
		return 4
	case "admin":
		return 3
	case "member":
		return 2
	case "viewer":
		return 1
	default:
		return 0
	}
}

// CreatePasswordResetForUser creates a reset token for an enabled user.
func (r *Repository) CreatePasswordResetForUser(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("invalid reset user")
	}
	now := time.Now().UTC()
	var email string
	if err := r.db.QueryRowContext(ctx, "SELECT email FROM users WHERE id = ? AND disabled_at IS NULL", userID).Scan(&email); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE account_tokens SET used_at = ? WHERE token_type = 'password_reset' AND user_id = ? AND used_at IS NULL`, millis(now), userID)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO account_tokens(id, token_hash, token_type, user_id, email, expires_at, created_at)
		VALUES (?, ?, 'password_reset', ?, ?, ?, ?)`, ids.New(), tokenHash, userID, email, millis(expiresAt), millis(now))
	return err
}

// UserForPasswordReset returns the target user when a token is valid.
func (r *Repository) UserForPasswordReset(ctx context.Context, tokenHash []byte) (model.User, error) {
	var user model.User
	var created int64
	var lastLogin sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT u.id, u.email, u.display_name, u.password_hash, u.created_at, u.last_login_at
		FROM account_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ? AND t.token_type = 'password_reset' AND t.used_at IS NULL AND t.expires_at > ? AND u.disabled_at IS NULL`, tokenHash, millis(time.Now())).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &created, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	user.CreatedAt = fromMillis(created)
	user.LastLoginAt = nullableTime(lastLogin)
	return user, nil
}

// ConsumePasswordReset replaces the password and invalidates every session.
func (r *Repository) ConsumePasswordReset(ctx context.Context, tokenHash []byte, passwordHash string) error {
	now := millis(time.Now())
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		var tokenID, userID string
		if err := tx.QueryRowContext(ctx, `SELECT id, user_id FROM account_tokens WHERE token_hash = ? AND token_type = 'password_reset' AND used_at IS NULL AND expires_at > ?`, tokenHash, now).Scan(&tokenID, &userID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ? AND disabled_at IS NULL", passwordHash, now, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE account_tokens SET used_at = ? WHERE id = ?", now, tokenID)
		return err
	})
}
