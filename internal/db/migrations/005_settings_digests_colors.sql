ALTER TABLE mailbox_addresses ADD COLUMN color TEXT NOT NULL DEFAULT '#e0f2fe';
ALTER TABLE messages ADD COLUMN mailbox_address_id TEXT REFERENCES mailbox_addresses(id) ON DELETE SET NULL;

CREATE TABLE installation_settings (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    configured INTEGER NOT NULL DEFAULT 0,
    base_url TEXT NOT NULL DEFAULT 'http://localhost:8080',
    session_ttl_hours INTEGER NOT NULL DEFAULT 168,
    log_level TEXT NOT NULL DEFAULT 'info',
    max_webhook_body_bytes INTEGER NOT NULL DEFAULT 1048576,
    max_message_text_bytes INTEGER NOT NULL DEFAULT 5242880,
    max_upload_request_bytes INTEGER NOT NULL DEFAULT 31457280,
    max_outbound_attachment_bytes INTEGER NOT NULL DEFAULT 26214400,
    max_attachment_count INTEGER NOT NULL DEFAULT 20,
    resend_api_key BLOB,
    resend_webhook_secret BLOB,
    resend_domain_id BLOB,
    setup_token_hash BLOB,
    setup_token_expires_at INTEGER,
    setup_completed_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE digest_subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recipient_email TEXT NOT NULL,
    frequency TEXT NOT NULL CHECK(frequency IN ('daily', 'weekly')) DEFAULT 'daily',
    timezone TEXT NOT NULL DEFAULT 'UTC',
    send_hour INTEGER NOT NULL DEFAULT 8 CHECK(send_hour BETWEEN 0 AND 23),
    mailbox_scope TEXT NOT NULL DEFAULT 'all' CHECK(mailbox_scope IN ('all', 'selected')),
    enabled INTEGER NOT NULL DEFAULT 1,
    last_sent_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(user_id)
);
CREATE INDEX idx_digest_subscriptions_due ON digest_subscriptions(enabled, send_hour, last_sent_at);

CREATE TABLE digest_subscription_mailboxes (
    subscription_id TEXT NOT NULL REFERENCES digest_subscriptions(id) ON DELETE CASCADE,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    PRIMARY KEY(subscription_id, mailbox_id)
);

INSERT INTO installation_settings (id, created_at, updated_at)
VALUES (1, CAST(strftime('%s', 'now') AS INTEGER) * 1000, CAST(strftime('%s', 'now') AS INTEGER) * 1000);

UPDATE mailbox_addresses
SET color = CASE (ABS(length(address) + unicode(substr(address, 1, 1))) % 8)
    WHEN 0 THEN '#e0f2fe'
    WHEN 1 THEN '#dcfce7'
    WHEN 2 THEN '#fef3c7'
    WHEN 3 THEN '#fce7f3'
    WHEN 4 THEN '#ede9fe'
    WHEN 5 THEN '#ffedd5'
    WHEN 6 THEN '#ccfbf1'
    ELSE '#f3e8ff'
END
WHERE color = '#e0f2fe';
