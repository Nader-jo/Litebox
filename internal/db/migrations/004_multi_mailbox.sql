CREATE TABLE mailbox_addresses (
    id TEXT PRIMARY KEY,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    address TEXT NOT NULL UNIQUE,
    local_part TEXT NOT NULL,
    domain TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    is_primary INTEGER NOT NULL DEFAULT 0,
    inbound_enabled INTEGER NOT NULL DEFAULT 1,
    outbound_enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_mailbox_addresses_primary
    ON mailbox_addresses(mailbox_id) WHERE is_primary = 1;
CREATE INDEX idx_mailbox_addresses_mailbox
    ON mailbox_addresses(mailbox_id, address);

INSERT INTO mailbox_addresses (
    id, mailbox_id, address, local_part, domain, display_name, is_primary,
    inbound_enabled, outbound_enabled, created_at, updated_at
)
SELECT 'address_' || id, id, address, local_part, domain, display_name, 1,
       inbound_enabled, outbound_enabled, created_at, updated_at
FROM mailboxes;

CREATE TABLE mailbox_memberships (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK(role IN ('owner', 'admin', 'member', 'viewer')),
    created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, mailbox_id)
);
CREATE INDEX idx_mailbox_memberships_mailbox
    ON mailbox_memberships(mailbox_id, role, user_id);

-- Preserve access for installations created before memberships existed.
INSERT INTO mailbox_memberships (user_id, mailbox_id, role, created_at, updated_at)
SELECT users.id, mailboxes.id, 'owner',
       CAST(strftime('%s', 'now') AS INTEGER) * 1000,
       CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM users CROSS JOIN mailboxes
WHERE mailboxes.is_primary = 1;

-- One provider inbound message may legitimately be delivered to two independent
-- Litebox mailboxes. Outbound provider IDs remain globally unique.
DROP INDEX idx_messages_resend_email_id;
CREATE UNIQUE INDEX idx_messages_resend_outbound
    ON messages(resend_email_id)
    WHERE resend_email_id IS NOT NULL AND direction = 'outbound';
CREATE UNIQUE INDEX idx_messages_resend_inbound_mailbox
    ON messages(mailbox_id, resend_email_id)
    WHERE resend_email_id IS NOT NULL AND direction = 'inbound';

DROP INDEX idx_messages_rfc_message_id;
CREATE UNIQUE INDEX idx_messages_rfc_mailbox
    ON messages(mailbox_id, rfc_message_id)
    WHERE rfc_message_id IS NOT NULL;

