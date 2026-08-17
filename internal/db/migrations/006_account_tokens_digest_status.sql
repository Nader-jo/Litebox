ALTER TABLE digest_subscriptions ADD COLUMN last_attempt_at INTEGER;
ALTER TABLE digest_subscriptions ADD COLUMN last_success_at INTEGER;
ALTER TABLE digest_subscriptions ADD COLUMN last_error TEXT;
ALTER TABLE digest_subscriptions ADD COLUMN last_received INTEGER NOT NULL DEFAULT 0;
ALTER TABLE digest_subscriptions ADD COLUMN last_unread INTEGER NOT NULL DEFAULT 0;
ALTER TABLE digest_subscriptions ADD COLUMN last_sent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE digest_subscriptions ADD COLUMN last_window_since INTEGER;
ALTER TABLE digest_subscriptions ADD COLUMN last_window_until INTEGER;

-- Account tokens are single-use, hashed bearer credentials. The raw token is
-- only delivered in the invitation/reset email and is never persisted.
CREATE TABLE account_tokens (
    id TEXT PRIMARY KEY,
    token_hash BLOB NOT NULL UNIQUE,
    token_type TEXT NOT NULL CHECK(token_type IN ('invitation', 'password_reset')),
    user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
    mailbox_id TEXT REFERENCES mailboxes(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('owner', 'admin', 'member', 'viewer')),
    created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_account_tokens_lookup ON account_tokens(token_type, expires_at, used_at);
