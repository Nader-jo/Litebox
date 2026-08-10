package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
)

// ListMailboxesForUser returns every mailbox visible to a user, with the
// membership role attached. The primary installation mailbox sorts first.
func (r *Repository) ListMailboxesForUser(ctx context.Context, userID string) ([]model.Mailbox, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT m.id, m.address, m.display_name, m.is_primary,
		m.inbound_enabled, m.outbound_enabled, mm.role
		FROM mailbox_memberships mm JOIN mailboxes m ON m.id = mm.mailbox_id
		WHERE mm.user_id = ?
		ORDER BY m.is_primary DESC, LOWER(m.address)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Mailbox
	for rows.Next() {
		var mailbox model.Mailbox
		var primary, inbound, outbound int
		if err := rows.Scan(&mailbox.ID, &mailbox.Address, &mailbox.DisplayName, &primary,
			&inbound, &outbound, &mailbox.Role); err != nil {
			return nil, err
		}
		mailbox.IsPrimary, mailbox.InboundEnabled, mailbox.OutboundEnabled = boolean(primary), boolean(inbound), boolean(outbound)
		result = append(result, mailbox)
	}
	return result, rows.Err()
}

// MailboxForUser resolves an authorized mailbox. An inaccessible or empty
// preference falls back to the user's first mailbox, avoiding a mailbox oracle.
func (r *Repository) MailboxForUser(ctx context.Context, userID, preferredID string) (model.Mailbox, error) {
	mailboxes, err := r.ListMailboxesForUser(ctx, userID)
	if err != nil {
		return model.Mailbox{}, err
	}
	if len(mailboxes) == 0 {
		return model.Mailbox{}, ErrNotFound
	}
	for _, mailbox := range mailboxes {
		if mailbox.ID == preferredID {
			mailbox.Addresses, err = r.ListMailboxAddresses(ctx, mailbox.ID)
			return mailbox, err
		}
	}
	mailboxes[0].Addresses, err = r.ListMailboxAddresses(ctx, mailboxes[0].ID)
	return mailboxes[0], err
}

// MailboxByID returns one mailbox without applying user authorization. It is
// intended for provider-side routing and background jobs.
func (r *Repository) MailboxByID(ctx context.Context, mailboxID string) (model.Mailbox, error) {
	var mailbox model.Mailbox
	var primary, inbound, outbound int
	err := r.db.QueryRowContext(ctx, `SELECT id, address, display_name, is_primary, inbound_enabled, outbound_enabled
		FROM mailboxes WHERE id = ?`, mailboxID).Scan(&mailbox.ID, &mailbox.Address, &mailbox.DisplayName,
		&primary, &inbound, &outbound)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Mailbox{}, ErrNotFound
	}
	mailbox.IsPrimary, mailbox.InboundEnabled, mailbox.OutboundEnabled = boolean(primary), boolean(inbound), boolean(outbound)
	if err == nil {
		mailbox.Addresses, err = r.ListMailboxAddresses(ctx, mailbox.ID)
	}
	return mailbox, err
}

// ListMailboxAddresses returns the primary address followed by aliases.
func (r *Repository) ListMailboxAddresses(ctx context.Context, mailboxID string) ([]model.MailboxAddress, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, mailbox_id, address, display_name, is_primary,
		inbound_enabled, outbound_enabled FROM mailbox_addresses WHERE mailbox_id = ?
		ORDER BY is_primary DESC, LOWER(address)`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.MailboxAddress
	for rows.Next() {
		var address model.MailboxAddress
		var primary, inbound, outbound int
		if err := rows.Scan(&address.ID, &address.MailboxID, &address.Address, &address.DisplayName,
			&primary, &inbound, &outbound); err != nil {
			return nil, err
		}
		address.IsPrimary, address.InboundEnabled, address.OutboundEnabled = boolean(primary), boolean(inbound), boolean(outbound)
		result = append(result, address)
	}
	return result, rows.Err()
}

// EnsureMailboxAddress adds a configured alias idempotently.
func (r *Repository) EnsureMailboxAddress(ctx context.Context, mailboxID, value, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 128 {
		return fmt.Errorf("display name must be between 1 and 128 characters")
	}
	address, local, domain, err := normalizeAddress(value)
	if err != nil {
		return err
	}
	var owner string
	err = r.db.QueryRowContext(ctx, "SELECT mailbox_id FROM mailbox_addresses WHERE address = ?", address).Scan(&owner)
	if err == nil {
		if owner != mailboxID {
			return fmt.Errorf("address is already assigned to another mailbox")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := millis(time.Now())
	_, err = r.db.ExecContext(ctx, `INSERT INTO mailbox_addresses
		(id, mailbox_id, address, local_part, domain, display_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, ids.New(), mailboxID, address, local, domain, displayName, now, now)
	return err
}

// AddMailboxAddress creates a sendable and receivable alias.
func (r *Repository) AddMailboxAddress(ctx context.Context, mailboxID, value, displayName string) error {
	return r.EnsureMailboxAddress(ctx, mailboxID, value, displayName)
}

// DeleteMailboxAddress removes an alias; the primary address is immutable.
func (r *Repository) DeleteMailboxAddress(ctx context.Context, mailboxID, addressID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM mailbox_addresses
		WHERE id = ? AND mailbox_id = ? AND is_primary = 0`, addressID, mailboxID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

// MailboxesForRecipients resolves every enabled local recipient to its mailbox.
func (r *Repository) MailboxesForRecipients(ctx context.Context, recipients []model.Address) ([]model.Mailbox, error) {
	found := make(map[string]model.Mailbox)
	for _, recipient := range recipients {
		var mailbox model.Mailbox
		var primary, inbound, outbound int
		err := r.db.QueryRowContext(ctx, `SELECT m.id, m.address, m.display_name, m.is_primary,
			m.inbound_enabled, m.outbound_enabled FROM mailbox_addresses ma
			JOIN mailboxes m ON m.id = ma.mailbox_id
			WHERE LOWER(ma.address) = LOWER(?) AND ma.inbound_enabled = 1 AND m.inbound_enabled = 1`, recipient.Address).
			Scan(&mailbox.ID, &mailbox.Address, &mailbox.DisplayName, &primary, &inbound, &outbound)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		mailbox.IsPrimary, mailbox.InboundEnabled, mailbox.OutboundEnabled = boolean(primary), boolean(inbound), boolean(outbound)
		found[mailbox.ID] = mailbox
	}
	result := make([]model.Mailbox, 0, len(found))
	for _, mailbox := range found {
		result = append(result, mailbox)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// MailboxAddressSet returns every local address for reply-all de-duplication.
func (r *Repository) MailboxAddressSet(ctx context.Context, mailboxID string) (map[string]struct{}, error) {
	addresses, err := r.ListMailboxAddresses(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		result[strings.ToLower(address.Address)] = struct{}{}
	}
	return result, nil
}

// SenderForMailbox validates a requested From identity against enabled aliases.
func (r *Repository) SenderForMailbox(ctx context.Context, mailboxID, requested string) (model.Address, error) {
	addresses, err := r.ListMailboxAddresses(ctx, mailboxID)
	if err != nil {
		return model.Address{}, err
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	for _, address := range addresses {
		if address.OutboundEnabled && (requested == "" && address.IsPrimary || strings.EqualFold(address.Address, requested)) {
			return model.Address{Name: address.DisplayName, Address: address.Address}, nil
		}
	}
	return model.Address{}, ErrNotFound
}

// CreateMailbox creates an independent inbox and grants its creator ownership.
func (r *Repository) CreateMailbox(ctx context.Context, creatorID, value, displayName string) (model.Mailbox, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 128 {
		return model.Mailbox{}, fmt.Errorf("display name must be between 1 and 128 characters")
	}
	address, local, domain, err := normalizeAddress(value)
	if err != nil {
		return model.Mailbox{}, err
	}
	now := time.Now().UTC()
	mailbox := model.Mailbox{ID: ids.New(), Address: address, DisplayName: displayName, InboundEnabled: true, OutboundEnabled: true, Role: "owner"}
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mailboxes
			(id, address, local_part, domain, display_name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, mailbox.ID, address, local, domain, mailbox.DisplayName, millis(now), millis(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mailbox_addresses
			(id, mailbox_id, address, local_part, domain, display_name, is_primary, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, ids.New(), mailbox.ID, address, local, domain, mailbox.DisplayName, millis(now), millis(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO mailbox_memberships
			(user_id, mailbox_id, role, created_by, created_at, updated_at)
			VALUES (?, ?, 'owner', ?, ?, ?)`, creatorID, mailbox.ID, creatorID, millis(now), millis(now))
		return err
	})
	return mailbox, err
}

// ListMailboxMembers returns all users assigned to one mailbox.
func (r *Repository) ListMailboxMembers(ctx context.Context, mailboxID string) ([]model.MailboxMembership, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT u.id, u.email, u.display_name, u.created_at,
		COALESCE(u.last_login_at, 0), mm.role, mm.created_at
		FROM mailbox_memberships mm JOIN users u ON u.id = mm.user_id
		WHERE mm.mailbox_id = ? AND u.disabled_at IS NULL
		ORDER BY CASE mm.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END,
		LOWER(u.email)`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.MailboxMembership
	for rows.Next() {
		var membership model.MailboxMembership
		var userCreated, lastLogin, membershipCreated int64
		if err := rows.Scan(&membership.User.ID, &membership.User.Email, &membership.User.DisplayName,
			&userCreated, &lastLogin, &membership.Role, &membershipCreated); err != nil {
			return nil, err
		}
		membership.MailboxID = mailboxID
		membership.User.CreatedAt = fromMillis(userCreated)
		if lastLogin > 0 {
			value := fromMillis(lastLogin)
			membership.User.LastLoginAt = &value
		}
		membership.CreatedAt = fromMillis(membershipCreated)
		result = append(result, membership)
	}
	return result, rows.Err()
}

// MailboxRoleForUser returns one membership role.
func (r *Repository) MailboxRoleForUser(ctx context.Context, mailboxID, userID string) (string, error) {
	var role string
	err := r.db.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE mailbox_id = ? AND user_id = ?`, mailboxID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// CreateUserWithMembership creates login credentials and grants mailbox access.
func (r *Repository) CreateUserWithMembership(ctx context.Context, creatorID, mailboxID, email, displayName, passwordHash, role string) (model.User, error) {
	if !validRole(role) {
		return model.User{}, fmt.Errorf("invalid mailbox role")
	}
	address, _, _, err := normalizeAddress(email)
	if err != nil {
		return model.User{}, fmt.Errorf("invalid administrator email")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 128 {
		return model.User{}, fmt.Errorf("display name must be between 1 and 128 characters")
	}
	now := time.Now().UTC()
	user := model.User{ID: ids.New(), Email: address, DisplayName: displayName, PasswordHash: passwordHash, CreatedAt: now}
	err = r.Transaction(ctx, func(tx *sql.Tx) error {
		var creatorRole string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE user_id = ? AND mailbox_id = ?`, creatorID, mailboxID).Scan(&creatorRole); err != nil {
			return ErrNotFound
		}
		if creatorRole != "owner" && creatorRole != "admin" || role == "owner" && creatorRole != "owner" {
			return fmt.Errorf("insufficient mailbox permission")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users
			(id, email, display_name, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			user.ID, user.Email, user.DisplayName, user.PasswordHash, millis(now), millis(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO mailbox_memberships
			(user_id, mailbox_id, role, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			user.ID, mailboxID, role, creatorID, millis(now), millis(now))
		return err
	})
	return user, err
}

// GrantMailboxAccess gives an existing enabled user access to a mailbox.
func (r *Repository) GrantMailboxAccess(ctx context.Context, creatorID, mailboxID, email, role string) error {
	if !validRole(role) {
		return fmt.Errorf("invalid mailbox role")
	}
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		var creatorRole string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE user_id = ? AND mailbox_id = ?`, creatorID, mailboxID).Scan(&creatorRole); err != nil {
			return ErrNotFound
		}
		if creatorRole != "owner" && creatorRole != "admin" || role == "owner" && creatorRole != "owner" {
			return fmt.Errorf("insufficient mailbox permission")
		}
		var userID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ? AND disabled_at IS NULL`, strings.ToLower(strings.TrimSpace(email))).Scan(&userID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var currentRole string
		err := tx.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE user_id = ? AND mailbox_id = ?`, userID, mailboxID).Scan(&currentRole)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if currentRole == "owner" && creatorRole != "owner" {
			return fmt.Errorf("only an owner can change an owner")
		}
		if currentRole == "owner" && role != "owner" {
			var owners int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_memberships WHERE mailbox_id = ? AND role = 'owner'`, mailboxID).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return fmt.Errorf("a mailbox must keep at least one owner")
			}
		}
		now := millis(time.Now())
		_, err = tx.ExecContext(ctx, `INSERT INTO mailbox_memberships
			(user_id, mailbox_id, role, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, mailbox_id) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`,
			userID, mailboxID, role, creatorID, now, now)
		return err
	})
}

// RemoveMailboxAccess revokes a membership while protecting the last owner.
func (r *Repository) RemoveMailboxAccess(ctx context.Context, actorID, mailboxID, userID string) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		var actorRole string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE mailbox_id = ? AND user_id = ?`, mailboxID, actorID).Scan(&actorRole); err != nil {
			return ErrNotFound
		}
		if actorRole != "owner" && actorRole != "admin" {
			return fmt.Errorf("insufficient mailbox permission")
		}
		var role string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM mailbox_memberships WHERE mailbox_id = ? AND user_id = ?`, mailboxID, userID).Scan(&role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if role == "owner" {
			if actorRole != "owner" {
				return fmt.Errorf("only an owner can revoke an owner")
			}
			var owners int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_memberships WHERE mailbox_id = ? AND role = 'owner'`, mailboxID).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return fmt.Errorf("a mailbox must keep at least one owner")
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM mailbox_memberships WHERE mailbox_id = ? AND user_id = ?`, mailboxID, userID)
		return err
	})
}

// ListSessions returns active browser sessions for one user.
func (r *Repository) ListSessions(ctx context.Context, userID string) ([]model.Session, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, created_at, expires_at, last_seen_at, COALESCE(user_agent, '')
		FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, userID, millis(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Session
	for rows.Next() {
		var session model.Session
		var created, expires, lastSeen int64
		if err := rows.Scan(&session.ID, &created, &expires, &lastSeen, &session.UserAgent); err != nil {
			return nil, err
		}
		session.CreatedAt, session.ExpiresAt, session.LastSeenAt = fromMillis(created), fromMillis(expires), fromMillis(lastSeen)
		result = append(result, session)
	}
	return result, rows.Err()
}

// DeleteUserSession revokes one session owned by a user.
func (r *Repository) DeleteUserSession(ctx context.Context, userID, sessionID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func normalizeAddress(value string) (address, local, domain string, err error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return "", "", "", err
	}
	address = strings.ToLower(parsed.Address)
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || domain == "" {
		return "", "", "", fmt.Errorf("invalid email address")
	}
	return address, local, domain, nil
}

func validRole(role string) bool {
	switch role {
	case "owner", "admin", "member", "viewer":
		return true
	default:
		return false
	}
}
