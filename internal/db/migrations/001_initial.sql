CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_login_at INTEGER,
    disabled_at INTEGER
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE,
    csrf_token_hash BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    ip_hash BLOB,
    user_agent TEXT
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE mailboxes (
    id TEXT PRIMARY KEY,
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

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id),
    subject_norm TEXT NOT NULL DEFAULT '',
    subject_display TEXT NOT NULL DEFAULT '',
    latest_message_at INTEGER NOT NULL,
    first_message_at INTEGER NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0,
    unread_count INTEGER NOT NULL DEFAULT 0,
    is_archived INTEGER NOT NULL DEFAULT 0,
    is_starred INTEGER NOT NULL DEFAULT 0,
    is_trashed INTEGER NOT NULL DEFAULT 0,
    pre_trash_archived INTEGER NOT NULL DEFAULT 0,
    trashed_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_threads_inbox ON threads(mailbox_id, is_trashed, is_archived, latest_message_at DESC);
CREATE INDEX idx_threads_starred ON threads(mailbox_id, is_starred, latest_message_at DESC);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT REFERENCES threads(id) ON DELETE SET NULL,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id),
    direction TEXT NOT NULL CHECK(direction IN ('inbound', 'outbound')),
    resend_email_id TEXT,
    resend_message_id TEXT,
    rfc_message_id TEXT,
    in_reply_to TEXT,
    references_header TEXT,
    from_name TEXT NOT NULL DEFAULT '',
    from_address TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    subject_norm TEXT NOT NULL DEFAULT '',
    text_body TEXT NOT NULL DEFAULT '',
    sanitized_html TEXT NOT NULL DEFAULT '',
    body_format TEXT NOT NULL DEFAULT 'text',
    remote_images_blocked INTEGER NOT NULL DEFAULT 0,
    raw_storage_backend TEXT NOT NULL DEFAULT 'filesystem',
    raw_storage_key TEXT,
    received_at INTEGER,
    sent_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    is_read INTEGER NOT NULL DEFAULT 0,
    ingest_status TEXT NOT NULL DEFAULT 'ready',
    delivery_status TEXT,
    last_provider_event_at INTEGER,
    provider_error_code TEXT,
    provider_error_message TEXT,
    size_bytes INTEGER,
    has_attachments INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_messages_resend_email_id ON messages(resend_email_id) WHERE resend_email_id IS NOT NULL;
CREATE UNIQUE INDEX idx_messages_rfc_message_id ON messages(rfc_message_id) WHERE rfc_message_id IS NOT NULL;
CREATE INDEX idx_messages_thread ON messages(thread_id, COALESCE(received_at, sent_at, created_at));
CREATE INDEX idx_messages_direction_time ON messages(mailbox_id, direction, created_at DESC);
CREATE INDEX idx_messages_unread ON messages(mailbox_id, is_read, received_at DESC) WHERE direction = 'inbound';

CREATE TABLE message_recipients (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    recipient_type TEXT NOT NULL CHECK(recipient_type IN ('to', 'cc', 'bcc', 'reply_to')),
    name TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_message_recipients_message ON message_recipients(message_id, recipient_type, sort_order);
CREATE INDEX idx_message_recipients_address ON message_recipients(address);

CREATE TABLE drafts (
    id TEXT PRIMARY KEY,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id),
    thread_id TEXT REFERENCES threads(id) ON DELETE SET NULL,
    reply_to_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    to_json TEXT NOT NULL DEFAULT '[]',
    cc_json TEXT NOT NULL DEFAULT '[]',
    bcc_json TEXT NOT NULL DEFAULT '[]',
    subject TEXT NOT NULL DEFAULT '',
    text_body TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_drafts_updated ON drafts(mailbox_id, updated_at DESC);

CREATE TABLE attachments (
    id TEXT PRIMARY KEY,
    message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,
    draft_id TEXT REFERENCES drafts(id) ON DELETE CASCADE,
    provider_attachment_id TEXT,
    filename TEXT NOT NULL,
    safe_filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    content_disposition TEXT NOT NULL DEFAULT 'attachment',
    content_id TEXT,
    storage_backend TEXT NOT NULL DEFAULT 'filesystem',
    storage_key TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    storage_status TEXT NOT NULL DEFAULT 'ready',
    created_at INTEGER NOT NULL,
    CHECK ((message_id IS NOT NULL) != (draft_id IS NOT NULL))
);
CREATE INDEX idx_attachments_message ON attachments(message_id);
CREATE INDEX idx_attachments_draft ON attachments(draft_id);

CREATE TABLE webhook_events (
    id TEXT PRIMARY KEY,
    svix_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    resend_email_id TEXT,
    provider_created_at INTEGER,
    raw_payload TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    processed_at INTEGER,
    processing_status TEXT NOT NULL DEFAULT 'queued',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);
CREATE INDEX idx_webhook_events_status ON webhook_events(processing_status, received_at);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    dedupe_key TEXT,
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued', 'running', 'succeeded', 'failed', 'dead')),
    priority INTEGER NOT NULL DEFAULT 100,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 10,
    run_after INTEGER NOT NULL,
    leased_until INTEGER,
    lease_owner TEXT,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER
);
CREATE UNIQUE INDEX idx_jobs_dedupe ON jobs(dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX idx_jobs_claim ON jobs(status, run_after, priority, created_at);

CREATE TABLE provider_events (
    id TEXT PRIMARY KEY,
    svix_id TEXT NOT NULL UNIQUE,
    resend_email_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_at INTEGER NOT NULL,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_provider_events_email ON provider_events(resend_email_id, event_at);

CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    user_id TEXT,
    action TEXT NOT NULL,
    entity_type TEXT,
    entity_id TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_audit_log_created ON audit_log(created_at DESC);
