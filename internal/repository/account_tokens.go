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
	now := time.Now().UTC()
	invitation := model.Invitation{ID: ids.New(), Email: email, DisplayName: strings.TrimSpace(displayName), MailboxID: mailboxID,
		Role: role, ExpiresAt: expiresAt.UTC()}
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		// Lock the write transaction before any authorization read so concurrent
		// invitations serialize instead of trying to upgrade read snapshots.
		if err := tx.QueryRowContext(ctx, `UPDATE mailboxes SET updated_at = updated_at
			WHERE id = ? RETURNING display_name`, mailboxID).Scan(&invitation.MailboxName); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if role == "owner" {
			var creatorRole string
			if err := tx.QueryRowContext(ctx, `UPDATE mailbox_memberships SET updated_at = updated_at
				WHERE mailbox_id = ? AND user_id = ? AND role = 'owner' RETURNING role`, mailboxID, createdBy).Scan(&creatorRole); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("owners must be granted by an existing owner")
				}
				return err
			}
		}
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
		// Claiming the token is the first statement and first write. Concurrent
		// acceptors therefore serialize here, and a later failure rolls the claim
		// back with every user and membership mutation.
		if err := tx.QueryRowContext(ctx, `UPDATE account_tokens SET used_at = ?
			WHERE token_hash = ? AND token_type = 'invitation' AND used_at IS NULL AND expires_at > ?
			RETURNING id, email, display_name, mailbox_id, role, expires_at`, millis(now), tokenHash, millis(now)).
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
		return nil
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
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		// Make validation the first write in the transaction. Concurrent reset
		// issuance is thereby serialized before any existing token is invalidated.
		var email string
		if err := tx.QueryRowContext(ctx, `UPDATE users SET updated_at = updated_at
			WHERE id = ? AND disabled_at IS NULL RETURNING email`, userID).Scan(&email); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE account_tokens SET used_at = ?
			WHERE token_type = 'password_reset' AND user_id = ? AND used_at IS NULL`, millis(now), userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO account_tokens(id, token_hash, token_type, user_id, email, expires_at, created_at)
			VALUES (?, ?, 'password_reset', ?, ?, ?, ?)`, ids.New(), tokenHash, userID, email, millis(expiresAt), millis(now))
		return err
	})
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
		// Claim the presented token and invalidate every other outstanding reset
		// token for the same user in the transaction's first write.
		rows, err := tx.QueryContext(ctx, `UPDATE account_tokens SET used_at = ?
			WHERE token_type = 'password_reset' AND used_at IS NULL AND user_id = (
				SELECT user_id FROM account_tokens
				WHERE token_hash = ? AND token_type = 'password_reset'
					AND used_at IS NULL AND expires_at > ?
			)
			RETURNING user_id`, now, tokenHash, now)
		if err != nil {
			return err
		}
		var userID string
		for rows.Next() {
			var currentUserID string
			if err := rows.Scan(&currentUserID); err != nil {
				rows.Close()
				return err
			}
			if userID == "" {
				userID = currentUserID
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if userID == "" {
			return ErrNotFound
		}
		result, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ? AND disabled_at IS NULL", passwordHash, now, userID)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated != 1 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
			return err
		}
		return nil
	})
}
