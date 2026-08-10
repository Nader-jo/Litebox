# Mailbox — Product Requirements Document

**File:** `mailbox_prd.md`
**Document version:** 2.0
**Date:** 2026-08-10
**Status:** Build-ready MVP specification
**Primary implementation language:** Go
**Deployment:** Docker Compose
**Database:** SQLite
**Durable blob storage:** Local filesystem by default; optional S3-compatible backend (including RustFS)
**Email transport:** Resend Sending + Receiving APIs and webhooks

> **v2 architecture:** RustFS is no longer required. The default mailbox is one Go container with one durable `/data` volume containing SQLite plus raw email/attachment blobs. A provider-neutral `BlobStore` keeps S3/RustFS available as an optional advanced backend.

---

# 1. Executive Summary


Mailbox is a deliberately small, self-hosted web mailbox for a custom-domain address such as:

```text
hello@abc.com
```

It provides the normal human mailbox workflows required for a solo founder or small business:

- receive mail sent to `hello@abc.com`;
- Inbox and unread state;
- safe plain-text/HTML reading;
- attachments;
- Compose, Reply, and Reply all;
- conversation threading;
- Sent and provider delivery state;
- Drafts;
- Archive, Starred, Trash, and restore;
- historical search;
- durable local ownership of old mail independently of Resend retention.

Mailbox deliberately does **not** implement SMTP, IMAP, POP3, or a general-purpose mail server. Resend handles internet-facing email transport. The Go application owns the human mailbox: UI, state, threading, search, authentication, webhook ingestion, background work, and durable history.

The default architecture is intentionally tiny:

```text
                    Internet
                       |
             HTTPS / reverse proxy
                       |
                       v
            +----------------------+
            |   Mailbox Go binary  |
            |----------------------|
            | templ + HTMX UI      |
            | HTTP routes          |
            | Resend webhook       |
            | background workers   |
            | Resend API client    |
            | SQLite repositories  |
            | filesystem BlobStore |
            +----------+-----------+
                       |
                       | mounted /data
                       v
            +----------------------+
            |    durable volume    |
            |----------------------|
            | /data/mailbox.db     |
            | /data/objects/...    |
            +----------------------+

External:
Mailbox <------> Resend API
Resend --------> /webhooks/resend
```

The **default Docker Compose deployment contains one required container**. SQLite is embedded, the durable job queue is SQLite-backed, and raw `.eml` files plus attachments live on the mounted filesystem.

Object storage is an optional deployment choice, not an MVP prerequisite. The code must use a `BlobStore` abstraction so an optional S3 implementation can later target RustFS, Amazon S3, Cloudflare R2, or another compatible provider.

The first release is single-tenant, single-user, and centered on one primary mailbox address. The schema should avoid needless dead ends for future aliases/users, but multi-tenant SaaS behavior is out of scope.

Product positioning:

> **A brutally lightweight self-hosted custom-domain mailbox powered by Resend.**

It should not try to be Gmail.

---

# 2. Product Vision


The product solves one narrow problem:

> "I own a domain and want a real, usable custom email inbox without operating an SMTP/IMAP mail server or deploying a large mail stack."

Existing open-source mail clients validate the broad pattern of external mail transport + local mailbox state + durable attachment storage + browser UI. The product therefore should **not** pretend the web-inbox concept itself is novel.

The differentiation is operational simplicity:

1. one Go application;
2. one mounted data directory;
3. SQLite instead of a database server;
4. filesystem blobs instead of a mandatory object-storage service;
5. no Node.js runtime in production;
6. no Redis;
7. no queue broker;
8. no Elasticsearch;
9. no SMTP/IMAP daemon;
10. no mandatory cloud platform;
11. Resend for the difficult external email transport;
12. enough mailbox UX for daily use.

The default success condition is intentionally boring:

```text
docker compose up -d
```

plus DNS/Resend configuration, and the operator has a dependable browser mailbox.

Storage must remain provider-neutral so an advanced operator can later choose:

```text
filesystem       default / simplest
S3-compatible    optional
RustFS           optional S3-compatible target
Amazon S3        optional
Cloudflare R2    optional
```

without changing mailbox-domain logic.

---

# 3. Product Principles


## 3.1 One-container core

The required MVP runtime is one service:

```text
mailbox
```

The container uses one mounted `/data` volume:

```text
/data/mailbox.db
/data/objects/
/data/tmp/
```

SQLite is embedded. No database, object-storage, cache, or queue container is required.

A public deployment still needs HTTPS. Caddy/Nginx/Traefik or a trusted tunnel can exist outside the core, or Caddy can be supplied as an optional Compose profile.

## 3.2 Go owns both UI and backend

The application must not require React, Vue, Next.js, Node.js, npm, pnpm, Bun, Webpack, Vite, or a frontend runtime.

Use:

- `github.com/a-h/templ` for Go-authored HTML components;
- vendored HTMX for progressive enhancement;
- custom CSS;
- embedded SVG icons;
- tiny framework-free JavaScript only when browser behavior truly requires it.

Routing, validation, authorization, rendering, and business logic remain in Go.

## 3.3 Resend is transport, not the mailbox database

Resend is never the long-term source of truth for mailbox history.

Inbound:

- normalized metadata/searchable text -> SQLite;
- raw `.eml` -> configured `BlobStore`;
- attachments -> configured `BlobStore`.

Outbound:

- drafts, recipients, content, threads, delivery state -> SQLite;
- uploaded attachments -> configured `BlobStore`;
- provider IDs -> SQLite only for reconciliation/provider events.

Default `BlobStore`:

```text
filesystem at /data/objects
```

## 3.4 Abstract storage, not infrastructure

Domain/application code depends on:

```go
type BlobStore interface { ... }
```

The implementation order is:

```text
BlobStore
  |
  +-- FileStore     required MVP/default
  |
  +-- S3Store       optional later
        |
        +-- RustFS
        +-- AWS S3
        +-- R2
```

The abstraction is required. RustFS is not.

## 3.5 Webhooks are at-least-once

Assume duplicate, replayed, delayed, and out-of-order events. Idempotency is a core correctness requirement.

## 3.6 Inbound HTML is hostile input

Never render inbound HTML directly. Sanitize scripts, event handlers, forms, dangerous embeds/URLs, and prevent remote tracking images from loading automatically.

## 3.7 Blob files are private application data

`/data/objects` is never mounted into the static-file handler. Downloads go through authenticated Go routes.

## 3.8 Archive before provider retention expires

Inbound data must be copied locally promptly. Normal mailbox reads must not depend on old provider data still existing.

## 3.9 Simplicity is not redundancy

One volume is a deliberate single failure domain. A lost volume means lost mail unless it was backed up. The product must make backup/restore explicit and easy rather than hiding that tradeoff.

---

# 4. Goals

The MVP is successful when a user can reliably use `hello@abc.com` as a small-business mailbox from a browser.

## 4.1 Functional goals

The MVP must support:

1. secure login;
2. first-run administrator setup;
3. Inbox;
4. unread state;
5. threaded conversation view;
6. Sent;
7. Drafts;
8. Archive;
9. Trash;
10. Starred;
11. Compose;
12. Reply;
13. Reply all;
14. attachments;
15. outbound delivery state;
16. search;
17. permanent storage of inbound mail;
18. webhook diagnostics;
19. service-health diagnostics;
20. backup procedures.

## 4.2 Operational goals

The application must:

- run with `docker compose up -d`;
- start correctly after a server reboot;
- tolerate duplicate Resend webhooks;
- tolerate temporary Resend API failures;
- tolerate temporary local blob-storage I/O failures without losing the inbound webhook notification;
- recover queued work after restart;
- keep `/data/objects` inaccessible from the public/static HTTP surface;
- keep secrets outside the source repository;
- expose a health endpoint for Docker/Caddy monitoring.

## 4.3 Performance goals

For a mailbox containing up to approximately 100,000 messages:

- initial authenticated Inbox page should normally render in <300 ms on a modest VPS when data is local;
- list pagination queries should target <100 ms;
- opening an already-ingested message should target <150 ms excluding large attachment retrieval;
- application memory at idle should remain modest;
- the server must stream attachments instead of reading full files into RAM;
- normal browsing must not contact Resend.

These are engineering targets, not externally guaranteed SLAs.

---

# 5. Non-Goals

The following are explicitly out of scope for MVP.

## 5.1 Mail protocol server features

Do not implement:

- SMTP receiving server;
- SMTP submission server;
- IMAP;
- POP3;
- Exchange ActiveSync;
- JMAP.

The UI is the mailbox client.

## 5.2 Gmail-scale functionality

Do not implement in MVP:

- calendar;
- contacts synchronization;
- Google-style categories;
- sophisticated spam classification;
- vacation responder;
- email rules engine;
- delegated mailboxes;
- shared team inbox;
- read receipts;
- email scheduling;
- undo send;
- snooze;
- client-side offline mode;
- PGP;
- S/MIME;
- full rich-text/WYSIWYG editor;
- marketing/bulk-email tools.

## 5.3 Multi-tenancy

Do not implement:

- organizations;
- tenants;
- billing;
- tenant-level isolation;
- customer sign-up;
- per-tenant Resend API keys;
- custom domain onboarding wizard for arbitrary customers.

Future architecture may add these, but this PRD describes one self-hosted mailbox installation.

---

# 6. Target User

## 6.1 Primary persona

A technical solo founder or small-business owner who:

- owns a custom domain;
- wants `hello@domain.com`;
- receives a small to moderate amount of email;
- can deploy Docker Compose;
- wants a lightweight mailbox;
- does not need full Google Workspace or Microsoft 365 functionality.

## 6.2 Primary jobs to be done

### JTBD-1 — Receive a customer inquiry

> When somebody emails `hello@abc.com`, I want it to appear in my Inbox quickly so I can answer it.

### JTBD-2 — Reply and preserve thread context

> When I answer an incoming email, I want my reply to appear in the same conversation in my mailbox and in normal email clients.

### JTBD-3 — Send a new email

> I want to compose a message from `hello@abc.com`, attach a document, and send it.

### JTBD-4 — Find old information

> I want to search by sender, recipient, subject, content, date, or attachment presence.

### JTBD-5 — Own my history

> If Resend removes older provider-side data, I still want every message that Mailbox successfully archived.

---

# 7. Important Constraints and Provider Behavior

This implementation must account for the following Resend behavior as documented in August 2026.

## 7.1 Receiving flow

Resend can receive mail for a custom receiving domain and sends an `email.received` webhook.

The webhook contains metadata but not the complete body and attachment bytes. The application must subsequently call the Receiving API to retrieve full content and attachment information.

## 7.2 Any local-part may be received

Once receiving is enabled for a domain, mail to addresses on that receiving domain may arrive at Resend. Mailbox therefore needs an **application-layer allowed-recipient check**.

For MVP:

```text
MAILBOX_PRIMARY_ADDRESS=hello@abc.com
```

Only messages addressed to configured mailbox addresses are ingested into the normal Inbox.

Unknown local-parts should be recorded minimally for diagnostics and discarded by default.

Do not create a silent catch-all mailbox unless explicitly configured.

## 7.3 Webhook delivery semantics

Webhook handling must assume at-least-once delivery.

Use the `svix-id` header as an idempotency key for a delivery event.

Webhook events can arrive out of order.

## 7.4 Webhook signature

Verify:

- `svix-id`;
- `svix-timestamp`;
- `svix-signature`;

against `RESEND_WEBHOOK_SECRET` using the raw request body before JSON reserialization.

Use the Resend Go SDK's webhook verification support rather than writing custom cryptography.

## 7.5 Attachment URLs are temporary

Inbound attachment download URLs returned by Resend are temporary. Copy attachment bytes into the configured BlobStore during ingestion.

Never store the temporary Resend URL as the permanent attachment.

## 7.6 Sending limits

Resend documents an email-size limit that includes Base64-encoded attachments. The product should therefore enforce a conservative local outbound attachment limit.

MVP default:

```text
MAX_OUTBOUND_ATTACHMENT_BYTES=26214400
```

That is 25 MiB total raw attachment data across all attachments.

This is intentionally below the provider's total encoded-message limit to leave room for Base64 expansion, MIME framing, body HTML/text, and headers.

## 7.7 Resend quotas

Provider quotas must be treated as external limits.

The application must surface provider errors clearly and never claim that "free mailbox" means unlimited mail.

Do not hardcode account quotas into business logic. They can change.

---

# 8. Architecture Decision

## 8.1 Chosen architecture

Use a **modular monolith**.

One Go process owns:

- authenticated UI;
- internal web API;
- webhook endpoint;
- Resend client;
- SQLite repositories;
- object storage client;
- job queue;
- background workers;
- email sanitization;
- search;
- session management.

Do not introduce microservices.

## 8.2 Why a modular monolith

A mailbox at this scale does not justify:

- Kafka;
- RabbitMQ;
- Redis;
- Postgres;
- Elasticsearch;
- Kubernetes;
- separate frontend service;
- separate worker service.

SQLite can provide a durable job queue for this workload.

The app can later be split if real load proves it necessary.

---

# 9. Technology Stack


## 9.1 Required Go stack

Use:

```text
Go
net/http
database/sql
os / io / filepath
```

Required libraries:

```text
github.com/a-h/templ
github.com/resend/resend-go/v3
modernc.org/sqlite
github.com/microcosm-cc/bluemonday
golang.org/x/crypto
golang.org/x/net/html
```

Optional test/development helpers:

```text
github.com/stretchr/testify
github.com/google/uuid
```

## 9.2 UI

Use `templ`, locally vendored HTMX, custom CSS, and embedded icons. No production Node runtime or external CDN dependency.

## 9.3 SQLite

Use `modernc.org/sqlite` to avoid CGO and keep the binary/deployment simple.

Required capabilities:

- WAL;
- foreign keys;
- busy timeout;
- FTS5;
- transactions;
- `RETURNING`.

## 9.4 Blob storage

Default:

```text
STORAGE_BACKEND=filesystem
STORAGE_ROOT=/data/objects
```

The Go standard library is sufficient for the required storage implementation.

Optional later backend:

```text
STORAGE_BACKEND=s3
```

If/when implemented, contain S3 SDK dependencies entirely inside the storage adapter. Possible targets include RustFS, AWS S3, R2, and compatible services.

Do not pull S3 libraries into the MVP solely to preserve a theoretical option.

## 9.5 Reverse proxy

Public production requires HTTPS. Use an existing Caddy/Nginx/Traefik instance, trusted tunnel, or optional Caddy Compose profile.

The Go application still sets application-level security headers.

## 9.6 Required runtime service budget

```text
mailbox containers       1
database containers      0
object-store containers  0
queue containers         0
frontend containers      0
```

Optional infrastructure must remain optional.

---

# 10. Repository Layout

Recommended repository structure:

```text
mailbox/
├── cmd/
│   └── mailbox/
│       └── main.go
│
├── internal/
│   ├── app/
│   │   ├── app.go
│   │   └── lifecycle.go
│   │
│   ├── config/
│   │   ├── config.go
│   │   └── validate.go
│   │
│   ├── auth/
│   │   ├── password.go
│   │   ├── session.go
│   │   ├── csrf.go
│   │   └── service.go
│   │
│   ├── db/
│   │   ├── db.go
│   │   ├── migrate.go
│   │   └── tx.go
│   │
│   ├── repository/
│   │   ├── users.go
│   │   ├── sessions.go
│   │   ├── mailboxes.go
│   │   ├── threads.go
│   │   ├── messages.go
│   │   ├── attachments.go
│   │   ├── drafts.go
│   │   ├── jobs.go
│   │   ├── webhook_events.go
│   │   └── settings.go
│   │
│   ├── mail/
│   │   ├── addresses.go
│   │   ├── subject.go
│   │   ├── threading.go
│   │   ├── sanitizer.go
│   │   ├── cid.go
│   │   └── formatter.go
│   │
│   ├── resendx/
│   │   ├── client.go
│   │   ├── inbound.go
│   │   ├── outbound.go
│   │   ├── webhook.go
│   │   ├── events.go
│   │   └── errors.go
│   │
│   ├── blobstore/
│   │   ├── store.go
│   │   ├── filestore.go
│   │   ├── keys.go
│   │   └── s3store.go       # optional backend
│   │
│   ├── jobs/
│   │   ├── runner.go
│   │   ├── lease.go
│   │   ├── ingest_email.go
│   │   ├── send_email.go
│   │   └── cleanup.go
│   │
│   ├── search/
│   │   ├── parser.go
│   │   └── service.go
│   │
│   ├── httpserver/
│   │   ├── server.go
│   │   ├── routes.go
│   │   ├── middleware.go
│   │   ├── errors.go
│   │   └── handlers/
│   │       ├── auth.go
│   │       ├── inbox.go
│   │       ├── thread.go
│   │       ├── compose.go
│   │       ├── attachments.go
│   │       ├── search.go
│   │       ├── settings.go
│   │       ├── health.go
│   │       └── webhooks.go
│   │
│   └── ui/
│       ├── layout/
│       ├── components/
│       ├── pages/
│       └── partials/
│
├── migrations/
│   ├── 001_initial.sql
│   ├── 002_fts.sql
│   └── 003_indexes.sql
│
├── web/
│   └── static/
│       ├── app.css
│       ├── htmx.min.js
│       └── app.js
│
├── scripts/
│   ├── backup.sh
│   ├── restore.sh
│   └── smoke-test.sh
│
├── test/
│   ├── fixtures/
│   └── integration/
│
├── Dockerfile
├── compose.yaml
├── compose.override.yaml
├── Caddyfile
├── .env.example
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

---

# 11. Core Domain Model

## 11.1 User

A person allowed to log into the mailbox application.

MVP: exactly one enabled user.

Fields:

- id;
- email;
- display name;
- password hash;
- created time;
- updated time;
- last login time;
- disabled time.

## 11.2 Mailbox

Represents the address used for inbound/outbound mail.

Example:

```text
display_name: ABC
address: hello@abc.com
```

MVP: one primary mailbox.

## 11.3 Thread

A logical conversation.

A thread contains one or more inbound/outbound messages.

Thread state includes:

- subject summary;
- latest message time;
- unread count;
- message count;
- archived flag;
- starred flag;
- trash flag.

## 11.4 Message

One email.

Direction:

```text
inbound
outbound
```

Lifecycle differs by direction.

## 11.5 Attachment

Metadata for a binary object stored through the configured BlobStore.

Never use the original filename as the object key.

## 11.6 Webhook event

Immutable record representing one Resend webhook delivery.

This is required for:

- deduplication;
- diagnostics;
- replay safety;
- event reconciliation.

## 11.7 Job

Durable asynchronous work item.

Examples:

```text
ingest_inbound
send_outbound
cleanup_trash
rebuild_search
```

---

# 12. Database Design

All IDs should be UUIDs stored as lowercase text, unless measurement shows that binary IDs are worth the complexity.

All timestamps should be UTC in ISO-8601 text or integer Unix milliseconds. Pick one representation and use it consistently.

Recommended: integer Unix milliseconds for sorting and compactness.

## 12.1 `users`

```sql
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
```

## 12.2 `sessions`

```sql
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

CREATE INDEX idx_sessions_user_id
ON sessions(user_id);

CREATE INDEX idx_sessions_expires_at
ON sessions(expires_at);
```

Never store the raw session cookie token.

## 12.3 `mailboxes`

```sql
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
```

## 12.4 `threads`

```sql
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
    trashed_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_threads_inbox
ON threads(mailbox_id, is_trashed, is_archived, latest_message_at DESC);

CREATE INDEX idx_threads_starred
ON threads(mailbox_id, is_starred, latest_message_at DESC);
```

Folder state belongs primarily to the thread for MVP so archiving a conversation behaves naturally.

## 12.5 `messages`

```sql
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

CREATE UNIQUE INDEX idx_messages_resend_email_id
ON messages(resend_email_id)
WHERE resend_email_id IS NOT NULL;

CREATE UNIQUE INDEX idx_messages_rfc_message_id
ON messages(rfc_message_id)
WHERE rfc_message_id IS NOT NULL;

CREATE INDEX idx_messages_thread
ON messages(thread_id, COALESCE(received_at, sent_at, created_at));

CREATE INDEX idx_messages_direction_time
ON messages(mailbox_id, direction, created_at DESC);

CREATE INDEX idx_messages_unread
ON messages(mailbox_id, is_read, received_at DESC)
WHERE direction = 'inbound';
```

`rfc_message_id` is the actual email Message-ID used for threading.

`resend_email_id` is the provider API resource ID.

Do not confuse them.

## 12.6 `message_recipients`

```sql
CREATE TABLE message_recipients (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    recipient_type TEXT NOT NULL
        CHECK(recipient_type IN ('to', 'cc', 'bcc', 'reply_to')),
    name TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_message_recipients_message
ON message_recipients(message_id, recipient_type, sort_order);

CREATE INDEX idx_message_recipients_address
ON message_recipients(address);
```

Normalize email addresses to lowercase for comparison while preserving user-facing name formatting separately.

## 12.7 `attachments`

```sql
CREATE TABLE attachments (
    id TEXT PRIMARY KEY,
    message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,
    draft_id TEXT,

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

    created_at INTEGER NOT NULL
);

CREATE INDEX idx_attachments_message
ON attachments(message_id);

CREATE INDEX idx_attachments_draft
ON attachments(draft_id);
```

## 12.8 `drafts`

```sql
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

CREATE INDEX idx_drafts_updated
ON drafts(mailbox_id, updated_at DESC);
```

JSON is acceptable for draft recipient editing because these values are transient. Final sent recipients are normalized into `message_recipients`.

## 12.9 `webhook_events`

```sql
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

CREATE INDEX idx_webhook_events_status
ON webhook_events(processing_status, received_at);
```

`svix_id UNIQUE` is the primary webhook dedupe guarantee.

## 12.10 `jobs`

```sql
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    dedupe_key TEXT,
    payload_json TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'queued'
        CHECK(status IN ('queued', 'running', 'succeeded', 'failed', 'dead')),

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

CREATE UNIQUE INDEX idx_jobs_dedupe
ON jobs(dedupe_key)
WHERE dedupe_key IS NOT NULL;

CREATE INDEX idx_jobs_claim
ON jobs(status, run_after, priority, created_at);
```

## 12.11 `provider_events`

Store outbound lifecycle events independently.

```sql
CREATE TABLE provider_events (
    id TEXT PRIMARY KEY,
    svix_id TEXT NOT NULL UNIQUE,
    resend_email_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_at INTEGER NOT NULL,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_provider_events_email
ON provider_events(resend_email_id, event_at);
```

## 12.12 `audit_log`

```sql
CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    user_id TEXT,
    action TEXT NOT NULL,
    entity_type TEXT,
    entity_id TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_audit_log_created
ON audit_log(created_at DESC);
```

Do not store raw message bodies in audit records.

---

# 13. SQLite Configuration

On application startup, configure SQLite:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA temp_store = MEMORY;
```

Do not set `synchronous=OFF`.

Open the database once and reuse the pool.

For this workload:

```go
db.SetMaxOpenConns(8)
db.SetMaxIdleConns(8)
```

If lock contention occurs, reduce writer concurrency before replacing SQLite.

All state transitions that affect multiple rows must use transactions.

Examples:

- creating an outbound message + recipients + queue job;
- completing inbound ingest + attachment metadata + thread counters;
- moving a thread to trash;
- permanently deleting a thread and attachment metadata.

---

# 14. Search Design

Use SQLite FTS5.

## 14.1 Searchable fields

Index:

- subject;
- from name;
- from address;
- recipient addresses;
- plain-text body.

Do not index sanitized HTML.

## 14.2 FTS table

Example:

```sql
CREATE VIRTUAL TABLE message_search USING fts5(
    message_id UNINDEXED,
    thread_id UNINDEXED,
    subject,
    sender,
    recipients,
    body,
    tokenize='unicode61'
);
```

Application code should explicitly update this table after a message becomes `ready`.

Avoid complex triggers during initial implementation. Keep index writes in the same transaction as final ingest wherever possible.

## 14.3 Search syntax

MVP supports:

```text
invoice
"renewal notice"
from:john@example.com
to:hello@abc.com
subject:invoice
has:attachment
is:unread
is:starred
after:2026-01-01
before:2026-08-01
```

Terms without operators go to FTS.

Filters compile to normal SQL predicates.

Reject malformed date filters with a user-friendly message instead of silently ignoring them.

## 14.4 Pagination

Use cursor/keyset pagination for thread lists:

```text
(latest_message_at, id)
```

Do not use large `OFFSET` values for deep pagination.

---

# 15. Threading Algorithm

Threading correctness matters more than clever subject matching.

## 15.1 Primary threading signals

For inbound messages, inspect in this order:

1. `In-Reply-To`;
2. identifiers in `References`;
3. known RFC `Message-ID` relationships.

If any referenced message is known, use that message's thread.

## 15.2 Subject fallback

If no message-ID relationship exists, subject-only merging is risky.

MVP policy:

- normalize leading `Re:`, `Fwd:`, localized repetitions, and whitespace;
- subject fallback may only be used when:
  - normalized subject matches;
  - participant overlap exists;
  - the candidate thread is recent;
  - no contradictory Message-ID information exists.

Recommended recent window:

```text
30 days
```

If uncertain, create a new thread rather than merging unrelated mail.

## 15.3 Outbound replies

When replying to a message:

```text
In-Reply-To: <original RFC Message-ID>
References: <existing references> <original RFC Message-ID>
Subject: Re: original subject
```

Deduplicate identifiers in `References`.

## 15.4 Provider event reconciliation

The initial Resend send API response identifies the provider email resource.

Later provider webhook events may supply the outgoing RFC message identifier.

When it becomes available:

- update `messages.rfc_message_id`;
- never overwrite a conflicting non-empty value without logging an anomaly.

---

# 16. Durable Blob Storage Design

The mailbox needs durable byte storage, but it does not need a dedicated object-storage server in the default deployment.

## 16.1 Stored blob classes

Store:

1. raw inbound `.eml` files;
2. inbound attachments;
3. draft/outbound attachments;
4. optional export artifacts.

Large binary data must not live in SQLite.

## 16.2 Default layout

```text
/data/
├── mailbox.db
├── mailbox.db-wal
├── mailbox.db-shm
├── objects/
│   ├── raw/
│   │   └── 2026/08/<uuid>.eml
│   ├── attachments/
│   │   └── 2026/08/<uuid>
│   └── drafts/
│       └── <draft-uuid>/<attachment-uuid>
└── tmp/
```

Everything required by the default mailbox therefore fits under one persistent volume.

## 16.3 Storage keys

Never use an untrusted filename as a storage path.

Generated logical keys:

```text
raw/2026/08/<uuid>.eml
attachments/2026/08/<uuid>
drafts/<draft-uuid>/<attachment-uuid>
```

The original filename remains metadata in SQLite.

Keys must be relative, application-generated, contain no traversal segments, and resolve strictly beneath `STORAGE_ROOT`.

## 16.4 BlobStore interface

```go
type BlobStore interface {
    Put(ctx context.Context, key string, r io.Reader, expectedSize int64, contentType string) (BlobInfo, error)
    Get(ctx context.Context, key string) (io.ReadCloser, BlobInfo, error)
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
    Health(ctx context.Context) error
}
```

No mailbox service may reach directly into filesystem or S3 APIs.

## 16.5 FileStore write semantics

For every `Put`:

1. validate logical key;
2. construct final path under the root;
3. create parent directory;
4. create a unique temp file on the same filesystem;
5. stream bytes into it while computing SHA-256;
6. verify expected size when known;
7. `Sync()` and close temp file;
8. atomically rename it to the final path;
9. best-effort sync the containing directory where appropriate;
10. return final size/checksum.

Never write directly to the final pathname. A crash must not leave a partial blob that appears complete.

## 16.6 FileStore reads/deletes

Reads are streaming and read-only. Do not load arbitrary attachments into RAM.

Deletes are idempotent where appropriate: an already-missing blob should not make cleanup retry forever.

Best-effort removal of empty directories is allowed but not required for correctness.

## 16.7 Permissions

Run the container as a non-root user.

Recommended effective permissions:

```text
/data             owner: mailbox
/data/objects     0700-ish
blob files        0600-ish
```

Never expose `/data` as static content.

## 16.8 Integrity metadata

Persist for each blob:

- `storage_backend`;
- logical `storage_key`;
- byte count;
- SHA-256;
- final storage status.

A message/attachment becomes storage-ready only after the blob write succeeds.

## 16.9 Authenticated serving

Browser:

```text
GET /attachments/{id}
```

Go:

1. authenticate/authorize;
2. load attachment metadata;
3. call `BlobStore.Get`;
4. stream using `io.Copy`;
5. set safe `Content-Type`;
6. set safe `Content-Disposition`.

Storage paths/keys are never exposed as browser-accessible filesystem URLs.

## 16.10 Optional S3Store

An optional S3 implementation may be added after the default path is stable.

```dotenv
STORAGE_BACKEND=s3
S3_ENDPOINT=
S3_REGION=
S3_BUCKET=
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
S3_FORCE_PATH_STYLE=true
```

The same logical storage key becomes the S3 object key.

RustFS must be treated as one endpoint compatible with this adapter, not as a special domain concept.

## 16.11 Backend switching

Do not allow an operator to point a non-empty installation at another backend and assume old data follows automatically.

Future command:

```text
mailbox storage migrate --from filesystem --to s3
```

must enumerate SQLite references, copy, checksum-verify, update rows transactionally, retain source until completion, and emit a migration report.

## 16.12 Durability warning

The default local filesystem is one failure domain. If the volume dies and no independent backup exists, mailbox history is lost. Backups are mandatory for important installations.

---

# 17. Inbound Email Flow

## 17.1 End-to-end flow

```text
Sender
  |
  v
DNS MX
  |
  v
Resend Inbound
  |
  | email.received webhook
  v
POST /webhooks/resend
  |
  | verify signature
  | dedupe svix-id
  | insert webhook event
  | enqueue ingest job
  v
HTTP 200
  |
  +----------------------------+
                               |
                               v
                        background worker
                               |
                               v
                       Resend Receiving API
                         |             |
                         | content     | attachments
                         v             v
                       normalize    download streams
                         |             |
                         |             v
                         |        BlobStore
                         v
                       SQLite
                         |
                         v
                       Inbox
```

## 17.2 Webhook handler requirements

Route:

```text
POST /webhooks/resend
```

The handler must:

1. limit request body size;
2. read raw bytes;
3. read required Svix headers;
4. verify signature before trusting event content;
5. parse JSON;
6. reject unsupported malformed payloads;
7. insert `webhook_events` using unique `svix_id`;
8. enqueue a deduplicated job;
9. commit transaction;
10. return `200 OK`.

If `svix_id` already exists:

- return `200`;
- do not enqueue another ingest.

## 17.3 Do not perform full ingestion inline

Do not:

- call Resend Receiving API;
- download attachment bytes;
- sanitize large HTML;
- persist to the configured BlobStore;

inside the webhook request transaction.

Those operations belong in a durable worker.

## 17.4 Inbound job payload

Example:

```json
{
  "webhook_event_id": "uuid",
  "resend_email_id": "provider-id"
}
```

Dedupe key:

```text
ingest:<resend_email_id>
```

## 17.5 Worker claim algorithm

The worker polls SQLite for runnable work.

Pseudo-transaction:

```sql
BEGIN IMMEDIATE;

SELECT id
FROM jobs
WHERE status = 'queued'
  AND run_after <= :now
ORDER BY priority ASC, created_at ASC
LIMIT 1;

UPDATE jobs
SET
    status = 'running',
    lease_owner = :worker,
    leased_until = :lease_expiry,
    attempt_count = attempt_count + 1,
    updated_at = :now
WHERE id = :id;

COMMIT;
```

Prefer a single atomic statement using `UPDATE ... RETURNING` when cleanly supported.

A job whose lease expires can be reclaimed.

## 17.6 Inbound retrieval

Worker:

1. call Resend Receiving `Get` for `resend_email_id`;
2. capture:
   - from;
   - to;
   - cc;
   - bcc;
   - reply-to;
   - subject;
   - text;
   - HTML;
   - headers;
   - message ID;
   - raw email signed URL if available;
   - attachment metadata;
3. validate that at least one recipient is an allowed mailbox;
4. normalize addresses;
5. determine thread;
6. download raw `.eml`;
7. save raw `.eml` to the configured BlobStore;
8. list/retrieve attachment URLs;
9. stream attachments into the configured BlobStore;
10. sanitize HTML;
11. write normalized SQLite message data;
12. update FTS index;
13. update thread counters;
14. mark webhook and job succeeded.

## 17.7 Raw email download

If the Receiving API exposes a signed raw email URL:

- fetch immediately during ingestion;
- stream it directly into the configured BlobStore;
- do not depend on that provider URL later.

If raw retrieval fails after all retries but normalized message retrieval succeeds:

- message may enter `ready_without_raw`;
- show an administrative warning;
- do not hide the email from the user solely because raw archival failed.

## 17.8 Attachment failure behavior

If one attachment download fails transiently:

- keep job retryable;
- do not duplicate already-persisted objects;
- identify attachments by provider attachment ID + message ID;
- use UPSERT logic safely.

After maximum retries:

- message becomes visible;
- attachment shows `Storage error`;
- admin diagnostics exposes retry action.

Do not make one corrupt attachment permanently hide an otherwise readable email.

---

# 18. Outbound Email Flow

## 18.1 Compose flow

User opens Compose.

Fields:

```text
From: hello@abc.com
To:
Cc:
Bcc:
Subject:
Body:
Attachments:
```

MVP body is plain text.

This avoids dragging a complex browser rich-text editor into the first version.

Plain text is rendered by mail clients normally and is sufficient for business correspondence.

## 18.2 Draft flow

Draft creation:

```text
POST /drafts
```

Draft updates:

```text
PATCH /drafts/{id}
```

Autosave through HTMX with debounce is recommended after the basic save path works.

## 18.3 Attachment upload

Route:

```text
POST /drafts/{id}/attachments
```

Requirements:

- multipart upload;
- per-request body limit;
- total draft attachment-size limit;
- sanitized filename;
- object key generated by UUID;
- stream to the configured BlobStore;
- compute SHA-256;
- persist attachment row;
- never buffer arbitrary file size fully in memory.

## 18.4 Send command

Route:

```text
POST /drafts/{id}/send
```

Do not call Resend synchronously from the user's HTTP request.

Transaction:

1. load draft;
2. validate recipients;
3. validate attachment budget;
4. create outbound `messages` row;
5. normalize final recipients;
6. associate draft attachments with final message;
7. construct stable send idempotency key;
8. enqueue `send_outbound` job;
9. delete/mark draft consumed;
10. commit;
11. immediately navigate user to Sent/thread view with status `Queued`.

## 18.5 Outbound job

Worker:

1. load message and attachments;
2. abort if already has successful `resend_email_id`;
3. read attachments from the configured BlobStore;
4. build Resend request;
5. include:
   - From;
   - To;
   - Cc;
   - Bcc;
   - Subject;
   - Text;
   - headers;
   - attachments;
6. send with Resend API idempotency key;
7. on success:
   - persist Resend email ID;
   - set status `submitted`;
8. provider webhooks later advance delivery state.

## 18.6 Idempotency key

Use a stable value tied to the logical message:

```text
mailbox-send/<message-uuid>
```

Do not generate a new idempotency key on every retry.

Application database state remains the primary duplicate-send guard because provider idempotency windows are finite.

## 18.7 Retry policy

Retry:

- timeouts;
- connection failures;
- HTTP 429;
- provider 5xx.

Do not automatically retry normal permanent 4xx validation failures.

Backoff example:

```text
10 s
30 s
2 min
10 min
30 min
2 h
6 h
```

Add jitter.

Store the exact last error.

## 18.8 Delivery events

Subscribe the same Resend webhook endpoint to outbound lifecycle events required by the product, including:

```text
email.sent
email.delivered
email.delivery_delayed
email.bounced
email.failed
email.suppressed
email.complained
```

Store every event before updating the aggregate message status.

---

# 19. Delivery State Machine

Suggested outbound status progression:

```text
queued
  |
  v
submitted
  |
  +------> sent
             |
             +------> delivered
             |
             +------> delivery_delayed
             |
             +------> bounced
             |
             +------> complained
             |
             +------> failed
             |
             +------> suppressed
```

Events may arrive out of order.

Never implement state changes as "whatever event arrived last wins" without considering event timestamp and terminal semantics.

## 19.1 Terminal states

Treat as terminal for user display:

```text
delivered
bounced
failed
suppressed
complained
```

If a contradictory later event arrives:

- retain both provider events;
- apply an explicit precedence rule;
- log anomaly.

---

# 20. HTML Email Safety

This section is mandatory.

## 20.1 Threat model

An inbound email can contain:

- JavaScript;
- `onerror`/`onclick`;
- `<iframe>`;
- `<object>`;
- forms;
- phishing links;
- CSS designed to overlay UI;
- tracking pixels;
- remote images;
- `cid:` images;
- malformed HTML;
- enormous DOM structures;
- malicious URLs.

## 20.2 Sanitization pipeline

For each inbound message:

1. keep the raw original in the configured BlobStore;
2. parse HTML;
3. normalize encoding;
4. identify `cid:` references;
5. rewrite recognized CID attachments to local authenticated routes;
6. block external images by default;
7. sanitize using a strict Bluemonday policy;
8. store only the sanitized version for normal rendering.

## 20.3 Allowed HTML

Permit a conservative subset:

- paragraphs;
- headings;
- lists;
- tables;
- blockquotes;
- emphasis;
- links;
- basic layout spans/divs;
- safe inline styling if necessary.

Strip:

- script;
- form;
- iframe;
- object;
- embed;
- SVG unless explicitly proven safe;
- MathML;
- event attributes;
- meta refresh;
- base;
- link;
- dangerous URI schemes.

## 20.4 Remote images

Default behavior:

```text
Remote images blocked
[Load images]
```

Do not automatically request `https://sender.example/pixel?id=...`.

This prevents automatic read tracking and IP leakage.

## 20.5 Content Security Policy

Recommended baseline:

```text
default-src 'self';
script-src 'self';
style-src 'self';
img-src 'self' data:;
font-src 'self';
connect-src 'self';
frame-src 'none';
object-src 'none';
base-uri 'none';
form-action 'self';
frame-ancestors 'none';
```

If remote image loading is later implemented, do not simply loosen `img-src *` globally.

---

# 21. Authentication

## 21.1 First-run setup

If no user exists:

```text
GET /setup
```

renders first-run setup.

Require:

- admin email;
- display name;
- strong password;
- password confirmation.

After one user exists:

- `/setup` returns 404 or redirects to login;
- setup cannot create additional unauthenticated administrators.

## 21.2 Password hashing

Use Argon2id from `golang.org/x/crypto/argon2`.

Store encoded parameters with hash.

Use a unique random salt per password.

Never store plaintext.

## 21.3 Sessions

On login:

1. generate at least 32 random bytes;
2. use the random value as cookie token;
3. store only SHA-256/token hash server-side;
4. generate session-specific CSRF secret;
5. set expiry.

Cookie:

```text
HttpOnly
Secure
SameSite=Lax
Path=/
```

Recommended session lifetime:

```text
7 days
```

Sliding refresh can update last-seen time but should not make sessions immortal.

## 21.4 CSRF

Every unsafe authenticated request must have a valid CSRF token:

```text
POST
PUT
PATCH
DELETE
```

Token is bound to the active session.

HTMX requests send it as:

```text
X-CSRF-Token
```

Normal forms send hidden input.

Use constant-time comparison.

## 21.5 Login throttling

Implement lightweight throttling per normalized IP and account.

Example:

```text
5 attempts / 10 min
progressive delay afterward
```

Do not expose whether a user email exists.

---

# 22. HTTP Routes

## 22.1 Public

```text
GET  /login
POST /login

GET  /setup
POST /setup

GET  /health/live
GET  /health/ready

POST /webhooks/resend
```

Webhook is public but signature-verified.

## 22.2 Authenticated mailbox

```text
GET  /
GET  /inbox
GET  /sent
GET  /drafts
GET  /archive
GET  /starred
GET  /trash

GET  /threads/{threadID}
POST /threads/{threadID}/read
POST /threads/{threadID}/unread
POST /threads/{threadID}/archive
POST /threads/{threadID}/unarchive
POST /threads/{threadID}/star
POST /threads/{threadID}/unstar
POST /threads/{threadID}/trash
POST /threads/{threadID}/restore
DELETE /threads/{threadID}

GET  /compose
POST /drafts
GET  /drafts/{draftID}
PATCH /drafts/{draftID}
DELETE /drafts/{draftID}

POST   /drafts/{draftID}/attachments
DELETE /drafts/{draftID}/attachments/{attachmentID}
POST   /drafts/{draftID}/send

POST /threads/{threadID}/reply
POST /threads/{threadID}/reply-all

GET /attachments/{attachmentID}
GET /attachments/{attachmentID}/inline

GET /search

GET /settings
POST /settings/profile

POST /logout
```

## 22.3 Administrative diagnostics

```text
GET  /admin/system
GET  /admin/jobs
POST /admin/jobs/{jobID}/retry
GET  /admin/webhooks
GET  /admin/storage
```

Do not expose secrets on these pages.

---

# 23. UI Information Architecture

## 23.1 Desktop shell

Use three primary regions:

```text
+--------------------------------------------------------------+
| Header / search / account                                    |
+--------------+-----------------------+-----------------------+
| Sidebar      | Thread list           | Conversation           |
|              |                       |                       |
| Compose      | sender                | Subject               |
| Inbox    12  | subject               | participants          |
| Starred      | snippet               |                       |
| Sent         | time                  | message cards         |
| Drafts       |                       | attachments           |
| Archive      |                       | reply composer        |
| Trash        |                       |                       |
+--------------+-----------------------+-----------------------+
```

Suggested desktop widths:

```text
sidebar:      220 px
thread list:  380–460 px
reader:       flexible
```

## 23.2 Responsive layout

At smaller widths:

- sidebar becomes a drawer;
- thread list and reader become route-driven screens;
- composer becomes full-screen sheet;
- attachment actions remain touch-friendly.

## 23.3 Visual direction

The interface should feel:

- professional;
- clean;
- light;
- quiet;
- fast;
- dense enough for work;
- not like a consumer social app.

Recommended palette behavior:

- neutral white/light-gray surfaces;
- subtle blue primary accent;
- clear unread weight;
- minimal borders;
- status communicated with text + icon, not color alone.

Do not copy Gmail pixel-for-pixel.

---

# 24. Screen Specifications

## 24.1 Login

Elements:

- product mark/name;
- email;
- password;
- Sign in;
- generic invalid credentials error.

No "forgot password" in MVP unless an email-independent recovery mechanism is implemented.

For self-hosted MVP, password reset can be a CLI/admin operation.

## 24.2 Inbox

Header:

- current mailbox address;
- search field;
- refresh action;
- account menu.

Sidebar:

- Compose;
- Inbox unread count;
- Starred;
- Sent;
- Drafts count;
- Archive;
- Trash;
- Settings.

Thread list row:

- unread indicator;
- star;
- sender/participants;
- subject;
- one-line text snippet;
- attachment icon;
- latest timestamp;
- delivery warning where relevant.

Unread rows use stronger type weight.

## 24.3 Conversation

Top action bar:

- Archive;
- Star;
- Mark unread;
- Trash;
- More.

Conversation header:

- subject;
- participants summary;
- message count.

Message card:

- sender name/address;
- recipients expandable;
- date/time;
- delivery state for outbound;
- body;
- blocked-images banner if applicable;
- attachments.

Collapsed historical messages show a compact header.

Newest message starts expanded.

## 24.4 Reply

At bottom of thread:

- Reply;
- Reply all.

Composer:

- recipients;
- text area;
- attachment button;
- Send;
- discard.

Sending changes button state immediately to:

```text
Queued
```

Do not block UI waiting for final delivery.

## 24.5 Compose

Use modal or right-side sheet on desktop.

Fields:

- From (fixed primary mailbox in MVP);
- To;
- Cc/Bcc toggle;
- Subject;
- body;
- attachments;
- Send;
- Save & close;
- Discard.

Recipient inputs should accept:

```text
Jane Doe <jane@example.com>
jane@example.com
```

Display validation errors next to invalid recipients.

## 24.6 Sent

Rows show:

- recipient;
- subject;
- snippet;
- timestamp;
- status.

Statuses:

```text
Queued
Submitted
Sent
Delivered
Delayed
Bounced
Failed
Suppressed
Complaint
```

Only show detailed provider diagnostics after opening the message or through a status popover.

## 24.7 Drafts

Rows show:

- recipient or "No recipient";
- subject or "(No subject)";
- snippet;
- updated time;
- attachment icon.

## 24.8 Trash

Show:

- restore;
- delete permanently.

MVP can retain trash indefinitely until user explicitly deletes.

Optional later feature:

```text
auto-delete after 30 days
```

## 24.9 Search

Search results should look like the thread list and preserve the query in the search bar.

Show active filter chips for structured operators.

Example:

```text
from:alice@example.com   has:attachment   after:2026-01-01
```

## 24.10 Settings

Sections:

### Mailbox

- display name;
- address;
- inbound enabled;
- outbound enabled.

### Resend

Read-only diagnostics:

- domain identifier if configured;
- last webhook received;
- last inbound successfully archived;
- last outbound send;
- recent provider error.

Do not display API key.

### Storage

- configured backend (`filesystem` or optional `s3`);
- blob-storage health;
- filesystem root or S3 bucket name as appropriate;
- free disk bytes for filesystem backend;
- approximate attachment count;
- approximate stored bytes where cheaply available.

### Database

- SQLite health;
- database file size;
- WAL file size;
- last backup timestamp if backup integration reports it.

---

# 25. HTMX Strategy

Use HTMX only where it materially improves UX.

Good uses:

- switching folders without full-shell reload;
- loading reader pane;
- marking read;
- archiving;
- starring;
- compose draft save;
- sending reply;
- pagination;
- search result update.

Do not build an opaque pseudo-SPA.

Every important route should work as a normal server-rendered route.

Handler checks:

```text
HX-Request: true
```

If present:

- return partial.

Otherwise:

- return full layout.

This makes the application debuggable and resilient.

---

# 26. Application Configuration


`.env.example`:

```dotenv
# Application
APP_ENV=development
APP_BASE_URL=http://localhost:8080
APP_LISTEN_ADDR=:8080
APP_DATA_DIR=/data
APP_DB_PATH=/data/mailbox.db
APP_COOKIE_NAME=mailbox_session
APP_SESSION_TTL_HOURS=168
APP_LOG_LEVEL=info

# Mailbox
MAILBOX_PRIMARY_ADDRESS=hello@abc.com
MAILBOX_DISPLAY_NAME=ABC
MAILBOX_ALLOWED_RECIPIENTS=hello@abc.com

# Resend
RESEND_API_KEY=
RESEND_WEBHOOK_SECRET=
RESEND_DOMAIN_ID=

# Storage — required/default
STORAGE_BACKEND=filesystem
STORAGE_ROOT=/data/objects
STORAGE_TMP_ROOT=/data/tmp

# Optional S3-compatible backend; ignored unless STORAGE_BACKEND=s3
S3_ENDPOINT=
S3_REGION=us-east-1
S3_BUCKET=mailbox
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
S3_FORCE_PATH_STYLE=true

# Limits
MAX_WEBHOOK_BODY_BYTES=1048576
MAX_MESSAGE_TEXT_BYTES=5242880
MAX_UPLOAD_REQUEST_BYTES=31457280
MAX_OUTBOUND_ATTACHMENT_BYTES=26214400
MAX_ATTACHMENT_COUNT=20

# Workers
WORKER_COUNT=2
JOB_POLL_INTERVAL_MS=500
JOB_LEASE_SECONDS=120

# Security / proxy
TRUSTED_PROXY_CIDRS=
```

Always validate the primary mailbox, Resend credentials, numeric limits, SQLite path, and writable data directory.

For `STORAGE_BACKEND=filesystem`:

- require a non-empty `STORAGE_ROOT`;
- create it if permitted;
- verify it is writable;
- verify it is outside the public static asset tree.

For `STORAGE_BACKEND=s3`, validate S3 configuration and perform a bounded health check.

The default filesystem deployment must not require any S3 variables.

---

# 27. Dockerfile

Use a multi-stage build.

Concept:

```dockerfile
FROM golang:<pinned-version>-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Generate templ sources if generated files are not committed.
RUN go generate ./...

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/mailbox \
    ./cmd/mailbox

FROM alpine:<pinned-version>

RUN addgroup -S mailbox && adduser -S mailbox -G mailbox

WORKDIR /app
COPY --from=builder /out/mailbox /app/mailbox

RUN mkdir -p /data && chown -R mailbox:mailbox /data /app

USER mailbox
EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/app/mailbox"]
```

Pin real image versions before release.

Do not ship `latest` for the application base image in production.

---

# 28. Docker Compose


The **default** Compose graph contains exactly one required service:

```yaml
services:
  mailbox:
    build:
      context: .
    restart: unless-stopped
    env_file:
      - .env
    volumes:
      - mailbox-data:/data
    ports:
      - "127.0.0.1:8080:8080"
    healthcheck:
      test: ["CMD", "/app/mailbox", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s

volumes:
  mailbox-data:
```

Running:

```bash
docker compose up -d
```

starts one mailbox container.

## 28.1 Public HTTPS

A real Resend webhook needs public HTTPS. Use one of:

1. an existing host Caddy/Nginx/Traefik;
2. a trusted ingress/tunnel;
3. an optional Caddy Compose profile supplied by this project.

Example optional service:

```yaml
caddy:
  image: caddy:<PINNED_VERSION>
  profiles: ["proxy"]
  restart: unless-stopped
  ports:
    - "80:80"
    - "443:443"
  volumes:
    - ./Caddyfile:/etc/caddy/Caddyfile:ro
    - caddy-data:/data
    - caddy-config:/config
  depends_on:
    mailbox:
      condition: service_healthy
```

Then:

```bash
docker compose --profile proxy up -d
```

The proxy is deployment infrastructure, not a mailbox dependency.

## 28.2 Optional RustFS/S3 topology

RustFS must **not** exist in the default Compose file/graph.

If `S3Store` ships, provide a separate advanced file/profile such as:

```text
compose.s3.yaml
```

Operators who deliberately choose it configure:

```dotenv
STORAGE_BACKEND=s3
S3_ENDPOINT=http://rustfs:9000
```

This must be documented as optional. Personal/small installations should start with filesystem storage unless they have a concrete reason not to.

---

# 29. Caddy Configuration

Example:

```caddyfile
mail.abc.com {
    encode zstd gzip

    reverse_proxy mailbox:8080

    header {
        X-Content-Type-Options "nosniff"
        Referrer-Policy "no-referrer"
        X-Frame-Options "DENY"
    }
}
```

The application itself owns CSP because it knows which routes/content require exceptions.

The webhook URL becomes:

```text
https://mail.abc.com/webhooks/resend
```

This web hostname does not have to equal the email receiving domain.

---

# 30. Domain and Resend Setup

## 30.1 DNS

For `hello@abc.com`, the receiving domain is:

```text
abc.com
```

Configure the exact DNS records shown by the Resend dashboard for:

- domain verification;
- inbound MX;
- sending authentication;
- DKIM;
- SPF/return path where required.

Add DMARC separately according to the domain's policy.

Do **not** copy example MX/SPF values from this PRD. Provider-assigned DNS values are the source of truth.

## 30.2 MX conflict warning

If another email provider already handles `abc.com`, changing root MX records can break that existing mail setup.

This PRD assumes Resend is intended to own inbound mail for the chosen receiving domain.

## 30.3 Webhook setup

Configure one endpoint:

```text
https://mail.abc.com/webhooks/resend
```

Subscribe to:

```text
email.received
email.sent
email.delivered
email.delivery_delayed
email.bounced
email.failed
email.suppressed
email.complained
```

Store the signing secret as:

```text
RESEND_WEBHOOK_SECRET
```

---

# 31. Resend Go Integration

Use:

```go
import "github.com/resend/resend-go/v3"
```

Create one long-lived client:

```go
client := resend.NewClient(cfg.ResendAPIKey)
```

Inject it through an interface so tests can replace it.

Example internal interface:

```go
type EmailProvider interface {
    GetReceived(ctx context.Context, id string) (*ReceivedEmail, error)
    ListReceivedAttachments(ctx context.Context, id string) ([]ReceivedAttachment, error)
    Send(ctx context.Context, msg SendRequest) (SendResult, error)
    VerifyWebhook(raw []byte, headers WebhookHeaders) error
}
```

Do not expose Resend SDK types throughout domain/application layers.

Map provider models at the adapter boundary.

---

# 32. Webhook Verification Implementation

Pseudo-Go:

```go
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)

    raw, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "invalid request", http.StatusBadRequest)
        return
    }

    headers := resend.WebhookHeaders{
        Id:        r.Header.Get("svix-id"),
        Timestamp: r.Header.Get("svix-timestamp"),
        Signature: r.Header.Get("svix-signature"),
    }

    err = h.resend.Webhooks.Verify(&resend.VerifyWebhookOptions{
        Payload:       string(raw),
        Headers:       headers,
        WebhookSecret: h.webhookSecret,
    })
    if err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }

    // Parse only after verification.
    // Persist svix-id + payload + durable job in one transaction.
    // Return 200 after commit.
}
```

Never parse then marshal the JSON again before signature verification.

---

# 33. Inbound Recipient Filtering

Because domain-level inbound can receive arbitrary local-parts, implement:

```go
func IsAllowedRecipient(to []Address, allowed map[string]struct{}) bool
```

Normalize:

- trim whitespace;
- lowercase domain;
- lowercase address for matching.

MVP action:

- if at least one allowed address is in To/Cc:
  - ingest;
- otherwise:
  - mark webhook processed as `ignored_unknown_recipient`;
  - do not download attachments;
  - return no error.

Optional configuration:

```dotenv
MAILBOX_CATCH_ALL=false
```

Do not enable catch-all by default.

---

# 34. Email Address Parsing

Use `net/mail` for conventional address parsing where possible.

Never split recipient input by comma using naïve string operations because display names can contain commas when quoted.

Normalize comparison form:

```text
user@example.com
```

Preserve display form separately:

```text
Jane Doe
```

Reject header injection characters in user-supplied recipient/subject fields.

---

# 35. Attachment Security

## 35.1 Filename handling

Keep:

```text
original filename
```

for display.

Generate safe fallback filename:

```text
attachment
attachment.pdf
```

Remove control characters.

Do not permit filename to affect object path.

## 35.2 Download response

Set:

```text
X-Content-Type-Options: nosniff
Content-Disposition: attachment; filename*=UTF-8''...
```

Default to attachment rather than inline.

Only `/inline` route may render known safe media types such as:

- image/png;
- image/jpeg;
- image/gif;
- image/webp.

SVG should download, not render inline, in MVP.

HTML should download, not render inline.

## 35.3 Malware scanning

Antivirus scanning is not required for MVP.

However, the architecture should permit a future asynchronous scanner before a file becomes downloadable.

The UI should not claim attachments are "safe."

---

# 36. Deletion Semantics

## 36.1 Trash

Trash is soft deletion.

Set:

```text
threads.is_trashed = 1
threads.trashed_at = now
```

Keep messages and objects.

## 36.2 Restore

Clear trash flags.

Restore to Inbox or Archive based on stored previous folder state.

Add `pre_trash_archived` if needed.

## 36.3 Permanent delete

Permanent deletion:

1. collect attachment storage keys;
2. collect raw storage keys;
3. delete database records transactionally;
4. enqueue blob-deletion job;
5. worker deletes blobs through BlobStore.

Why blob deletion is asynchronous:

- storage can be temporarily unavailable;
- database should not remain locked while deleting many objects.

Use a tombstone/blob-delete queue so failed blob deletion can retry.

## 36.4 Provider retention caveat

Deleting a local message cannot be assumed to erase all temporary/provider-side copies immediately.

Mailbox controls its SQLite and local archived copies.

---

# 37. Background Job Runner

## 37.1 Worker pool

Default:

```text
WORKER_COUNT=2
```

Kinds:

```text
ingest_inbound
send_outbound
delete_objects
reindex_message
cleanup_sessions
```

## 37.2 Crash recovery

When app starts:

```text
running jobs whose lease expired -> queued
```

Do not reset actively leased jobs blindly.

## 37.3 Dead letter state

After `max_attempts`:

```text
status = dead
```

Admin UI displays:

- job kind;
- entity;
- attempts;
- last error;
- Retry.

Manual retry:

- resets to queued;
- preserves historical attempt count in audit metadata or job history if implemented.

---

# 38. Error Handling

## 38.1 User-visible error classes

### Validation

Example:

```text
"jane@" is not a valid email address.
```

### Temporary service failure

Example:

```text
Your email is queued, but the mail provider is temporarily unavailable.
Mailbox will retry automatically.
```

### Permanent send failure

Example:

```text
This message could not be sent.
Provider response: invalid recipient address.
```

### Storage failure

Example:

```text
The message was received, but one attachment could not be archived.
```

## 38.2 Never expose

Do not show:

- Resend API key;
- webhook secret;
- S3 credentials;
- raw stack traces;
- internal SQL;
- absolute server filesystem paths.

---

# 39. Logging

Use structured JSON logs in production.

Fields:

```text
timestamp
level
message
request_id
user_id
thread_id
message_id
job_id
webhook_svix_id
resend_email_id
duration_ms
error
```

Do not log:

- message body;
- attachment bytes;
- passwords;
- cookies;
- API keys;
- webhook secrets.

Sender/recipient addresses are personal data. Avoid logging them by default.

---

# 40. Observability

MVP must expose:

```text
GET /health/live
GET /health/ready
```

## 40.1 Liveness

Returns 200 if process event loop/server is alive.

Must not depend on Resend.

## 40.2 Readiness

Check:

- SQLite simple query;
- ability to access required local configuration;
- blob-storage health/object API.

Do not make readiness depend on Resend internet reachability, otherwise a Resend outage could remove the UI from service.

## 40.3 Admin metrics

Show:

- pending jobs;
- dead jobs;
- last inbound event;
- last successful ingest;
- last outbound submit;
- failed attachment count;
- DB size;
- blob storage reachable yes/no.

Prometheus is not required in MVP.

---

# 41. Backup and Restore


Backups are mandatory because the default one-volume deployment is intentionally not redundant.

## 41.1 Persistent dataset

```text
/data/
├── mailbox.db
└── objects/
```

Also preserve deployment secrets/configuration through the operator's secret-management process.

## 41.2 Simplest correct MVP backup

Favor a **quiesced backup** over clever online consistency logic.

Recommended flow:

```bash
docker compose stop mailbox
# snapshot/copy mailbox-data to an independent destination
docker compose start mailbox
```

A second directory on the same physical disk is not a disaster backup.

## 41.3 `mailbox backup`

Implement:

```text
mailbox backup --output <directory>
```

MVP contract: run it while the normal server process is stopped.

It must:

1. validate SQLite;
2. create a safe SQLite copy using the backup API or `VACUUM INTO`;
3. copy all SQLite-referenced blobs;
4. produce a manifest containing backup format version, DB checksum, blob count, logical keys, sizes, SHA-256 hashes, and timestamp;
5. fail if a referenced blob is missing.

Online coordinated backups may be added later.

## 41.4 External backup tools

Because default state lives in one volume, operators can use restic, Borg, filesystem snapshots, or cloud-volume snapshots. The consistency procedure still matters; stopping the app briefly is the default supported path until tested online snapshots exist.

## 41.5 S3 backend

If an optional S3 backend is used, back up SQLite separately and give the bucket its own versioning/replication/backup policy. S3-compatible storage by itself is not a backup.

## 41.6 Restore — filesystem backend

1. stop Mailbox;
2. restore `/data/mailbox.db`;
3. restore `/data/objects`;
4. restore ownership/permissions;
5. start Mailbox;
6. run `mailbox doctor`;
7. rebuild FTS if needed.

## 41.7 `mailbox doctor`

Check:

- migrations;
- foreign keys;
- SQLite integrity;
- FTS;
- configured storage backend;
- missing raw/attachment blobs;
- unreferenced blobs;
- stuck/dead jobs;
- mailbox configuration.

Optional:

```text
mailbox doctor --deep
```

re-hashes all blobs and compares stored SHA-256 values.

---

# 42. CLI Commands

The same Go binary should support operational subcommands.

```text
mailbox serve
mailbox migrate
mailbox healthcheck
mailbox doctor
mailbox backup
mailbox create-admin
mailbox reset-password
mailbox reindex
```

This avoids separate administration binaries.

Default entrypoint can behave as `serve`.

---

# 43. Security Requirements

## 43.1 Required

- HTTPS in production;
- secure cookies;
- Argon2id passwords;
- CSRF validation;
- webhook signature validation;
- inbound HTML sanitization;
- no remote images by default;
- `/data` excluded from static/public serving;
- least-privilege filesystem permissions;
- API keys via environment/secrets;
- request body limits;
- file upload limits;
- SQL parameterization;
- no HTML injection;
- no open redirects;
- no secrets in logs;
- session revocation;
- login throttling.

## 43.2 HTTP headers

Application should set:

```text
Content-Security-Policy
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy
X-Frame-Options: DENY
Cross-Origin-Opener-Policy: same-origin
```

Test compatibility with sanitized email rendering.

## 43.3 Proxy trust

Do not trust arbitrary `X-Forwarded-For`.

Only use forwarded headers if remote address belongs to configured trusted proxy networks.

---

# 44. Rate and Abuse Controls

Even a personal mailbox is internet-facing through its receiving domain.

## 44.1 Webhook endpoint

- signature required;
- body cap;
- timeout;
- no expensive work inline.

## 44.2 Compose/send endpoint

- authenticated;
- CSRF-protected;
- reasonable send rate cap per user;
- cap recipients per message.

Suggested MVP:

```text
max To + Cc + Bcc recipients: 20
```

This is a human mailbox, not bulk marketing infrastructure.

## 44.3 Storage abuse

Inbound unexpected local-parts are discarded before attachment download.

This prevents someone from using random addresses under the domain to intentionally consume local storage.

---

# 45. Data Retention

Default local behavior:

- Inbox/Sent/Archive: indefinite;
- Drafts: indefinite;
- Trash: indefinite until permanent delete;
- sessions: expired sessions cleaned daily;
- webhook raw payloads: retain 30 days by default;
- provider event payloads: retain 90 days or indefinitely if size remains trivial;
- audit logs: 180 days by default.

Configuration may later expose these.

---

# 46. Detailed Implementation Plan

The implementation should happen in the following order.

Do not build polished UI before proving inbound persistence and outbound idempotency.

---

## Phase 0 — Repository and Tooling

### Step 0.1 — Initialize repository

Create:

```bash
go mod init <module-path>
```

Add:

- `.gitignore`;
- `.env.example`;
- `Makefile`;
- `README.md`;
- standard directories.

### Step 0.2 — Add dependencies

Add:

- templ;
- Resend Go SDK v3;
- modernc SQLite;
- Bluemonday;
- x/crypto;
- x/net.

Pin versions in `go.mod`.

### Step 0.3 — Configure formatting/static checks

Required CI commands:

```bash
go fmt ./...
go vet ./...
go test ./...
```

Recommended:

```text
staticcheck
```

### Step 0.4 — Create Make targets

```text
make generate
make test
make build
make run
make compose-up
make compose-down
make lint
make doctor
```

**Exit criteria**

- clean repository builds;
- empty server starts;
- CI passes.

---

## Phase 1 — Configuration and Application Lifecycle

### Step 1.1 — Implement typed configuration

Create `internal/config`.

Load env once on startup.

Do not let random packages call `os.Getenv`.

### Step 1.2 — Validate configuration

Validate:

- valid URLs;
- valid mailbox address;
- numeric limits >0;
- production HTTPS;
- required secrets.

### Step 1.3 — Build application container

Create central `App`:

```go
type App struct {
    Config Config
    DB *sql.DB
    Store BlobStore
    Provider EmailProvider
    Jobs *jobs.Runner
    Server *http.Server
}
```

### Step 1.4 — Graceful shutdown

On SIGTERM/SIGINT:

1. stop accepting new requests;
2. stop claiming new jobs;
3. allow active requests/jobs bounded time;
4. close DB;
5. exit.

**Exit criteria**

- configuration errors fail fast;
- Ctrl+C cleanly stops local server.

---

## Phase 2 — SQLite and Migrations

### Step 2.1 — Open SQLite

Create DB parent directory.

Configure WAL and pragmas.

### Step 2.2 — Implement migration runner

Embed SQL migrations with `go:embed`.

Maintain:

```sql
schema_migrations(version, applied_at)
```

Migrations execute in order.

### Step 2.3 — Create initial schema

Implement tables from Section 12.

### Step 2.4 — Add FTS5 migration

At startup migration/test time, execute a smoke FTS query.

If FTS5 is unavailable, fail deployment rather than silently shipping broken search.

### Step 2.5 — Repository interfaces

Keep SQL in repository package.

Handlers must not contain SQL.

**Tests**

- empty DB migration;
- migrate twice;
- foreign key enforcement;
- FTS insert/query;
- rollback on migration failure.

**Exit criteria**

- new DB can be created from zero;
- all migrations deterministic.

---

## Phase 3 — BlobStore and Local Filesystem Storage


### Step 3.1 — Define `BlobStore`

```go
type BlobStore interface {
    Put(ctx context.Context, key string, r io.Reader, expectedSize int64, contentType string) (BlobInfo, error)
    Get(ctx context.Context, key string) (io.ReadCloser, BlobInfo, error)
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
    Health(ctx context.Context) error
}
```

### Step 3.2 — Generate safe logical keys

Implement/test:

```text
raw/<yyyy>/<mm>/<uuid>.eml
attachments/<yyyy>/<mm>/<uuid>
drafts/<draft-uuid>/<attachment-uuid>
```

### Step 3.3 — Implement `FileStore`

Implement safe root resolution, traversal rejection, temp-write + sync + atomic rename, streaming get, streaming SHA-256, idempotent delete, and health checks.

### Step 3.4 — Bootstrap `/data`

On startup verify/create SQLite parent, storage root, and temp directory; fail fast on unwritable storage.

### Step 3.5 — Integration tests

Test put/get/delete, byte equality, Unicode display filename independence, 10+ MiB streaming, truncated input, interrupted temp write, traversal attempts, unwritable root, and missing blob.

### Step 3.6 — Optional S3 adapter boundary

Do not block MVP on S3. If implemented, isolate SDK dependencies to `blobstore/s3store.go`, reuse logical keys, and test against an S3-compatible service. RustFS-specific details must not escape the adapter/configuration layer.

**Exit criteria**

- fresh installation requires one Go container plus `/data`;
- raw email/attachments are durable through `FileStore`;
- application services know only `BlobStore`.

---

## Phase 4 — Authentication and Base UI

### Step 4.1 — First-run user setup

Implement `/setup`.

### Step 4.2 — Password hashing

Argon2id helper with versioned encoded format.

### Step 4.3 — Session repository

Create/revoke/touch sessions.

### Step 4.4 — Auth middleware

Resolve session cookie -> user.

### Step 4.5 — CSRF

Generate and validate per-session token.

### Step 4.6 — Login/logout

Implement generic errors.

### Step 4.7 — UI shell

Create templ components:

```text
AppShell
Header
Sidebar
Flash
Button
Icon
EmptyState
```

### Step 4.8 — CSS foundation

Implement:

- design tokens;
- spacing;
- typography;
- buttons;
- input;
- modal/sheet;
- list row;
- responsive breakpoints.

**Exit criteria**

- authenticated empty Inbox shell works;
- no Node toolchain required.

---

## Phase 5 — Resend Domain/Client Foundations

### Step 5.1 — Create Resend adapter

Wrap SDK.

### Step 5.2 — Implement provider interfaces

Inbound retrieval.

Attachment list/get.

Outbound send with options/idempotency.

Webhook verify.

### Step 5.3 — Provider error mapping

Convert provider-specific errors into:

```text
temporary
rate-limited
validation
authentication
not-found
unknown
```

### Step 5.4 — Test against mocked HTTP

Use Resend custom HTTP client or adapter fake.

Do not require live provider for most tests.

**Exit criteria**

- provider adapter has deterministic tests;
- application layers do not depend directly on SDK models.

---

## Phase 6 — Webhook Ingestion Queue

### Step 6.1 — Public webhook route

Implement raw-body and signature handling.

### Step 6.2 — Deduplication

Unique insert on `svix_id`.

Duplicate returns 200.

### Step 6.3 — Persist payload

Store verified payload.

### Step 6.4 — Create job

`email.received` -> `ingest_inbound`.

Outbound events -> lightweight event-processing job or process durable payload after persistence.

### Step 6.5 — Fast acknowledgement

Return 200 after transaction commit.

### Step 6.6 — Retry semantics tests

Test:

- same Svix ID twice;
- same Resend email with different replay Svix ID;
- invalid signature;
- missing signature;
- malformed verified payload;
- unsupported event.

**Exit criteria**

- webhook endpoint is safe to expose publicly.

---

## Phase 7 — Durable Job Runner

### Step 7.1 — Job claim

Implement lease.

### Step 7.2 — Retry policy

Exponential backoff + jitter.

### Step 7.3 — Dead jobs

Move after max attempts.

### Step 7.4 — Restart recovery

Expired leases reclaimed.

### Step 7.5 — Admin job diagnostics

Basic list page.

**Exit criteria**

- kill app during running job;
- restart;
- job resumes without duplicate final record.

---

## Phase 8 — Inbound Message Ingestion

### Step 8.1 — Retrieve message

Fetch full received email.

### Step 8.2 — Allowed-recipient gate

Reject unknown aliases before expensive attachment work.

### Step 8.3 — Parse headers

Extract:

- Message-ID;
- In-Reply-To;
- References.

Prefer provider structured fields where present, but retain raw headers.

### Step 8.4 — Normalize body

Prefer provider plain text.

If plain text is absent:

- derive readable text from sanitized HTML.

### Step 8.5 — Raw archive

Download `.eml` and store it through BlobStore.

### Step 8.6 — Attachments

List attachments.

For each:

- obtain fresh temporary URL;
- stream download;
- stream to the configured BlobStore;
- checksum;
- persist metadata.

### Step 8.7 — CID rewrite

Map content IDs to stored attachment IDs.

### Step 8.8 — Sanitize HTML

Store safe HTML.

### Step 8.9 — Thread assignment

Use algorithm in Section 15.

### Step 8.10 — DB transaction

Insert final:

- thread;
- message;
- recipients;
- attachments metadata;
- FTS row;
- counters.

### Step 8.11 — Mark job success

**Exit criteria**

Send real test email to domain.

Verify:

- appears in Inbox;
- raw `.eml` exists in the configured BlobStore;
- attachments exist;
- Resend can later lose provider data without affecting UI.

---

## Phase 9 — Inbox UI

### Step 9.1 — Inbox repository query

Return thread summaries.

### Step 9.2 — Thread list

Display:

- sender;
- subject;
- snippet;
- timestamp;
- unread;
- attachment;
- star.

### Step 9.3 — Reader

Open thread.

### Step 9.4 — Read state

Opening newest inbound marks thread read, or mark only messages rendered.

Recommended MVP: mark thread read when opened.

### Step 9.5 — Actions

Implement:

- star;
- archive;
- trash;
- mark unread.

### Step 9.6 — HTMX partials

Update row/sidebar counts without full reload.

**Exit criteria**

Inbound mailbox is usable for reading and organization.

---

## Phase 10 — Secure Attachment Serving

### Step 10.1 — Attachment authorization

Require logged-in user.

### Step 10.2 — Stream object

No full memory buffering.

### Step 10.3 — Disposition

Default attachment.

### Step 10.4 — Inline endpoint

Only known image types.

### Step 10.5 — Missing object behavior

Show clean 404 plus admin diagnostic link.

**Exit criteria**

Attachments download correctly and dangerous types do not execute in app origin.

---

## Phase 11 — Drafts and Compose

### Step 11.1 — Draft CRUD

Create/update/delete.

### Step 11.2 — Recipient parser

Support display names.

### Step 11.3 — Attachment upload

Stream into the configured BlobStore.

### Step 11.4 — Compose UI

Build polished sheet/modal.

### Step 11.5 — Autosave

Add HTMX debounce after explicit Save works.

**Exit criteria**

User can create a draft, close browser, reopen it, and retain content/files.

---

## Phase 12 — Outbound Sending

### Step 12.1 — Send transaction

Convert draft -> message + job.

### Step 12.2 — Build provider request

Use text body.

Set From to configured mailbox only.

### Step 12.3 — Idempotency

Stable provider idempotency key.

### Step 12.4 — Attachments

Read blobs through BlobStore and provide them to Resend in the expected attachment representation.

Be careful: if SDK requires bytes, total outbound size is already capped. If streaming is not supported by provider SDK, bounded memory use is acceptable under the local 25 MiB cap.

### Step 12.5 — Persist result

Resend email ID.

### Step 12.6 — Sent UI

Show queued/submitted status.

**Exit criteria**

User can send from `hello@abc.com` to Gmail/Outlook and receive it.

---

## Phase 13 — Reply and Thread Continuity

### Step 13.1 — Reply target

Use Reply-To when present, otherwise From.

### Step 13.2 — Reply all

Recipients include:

- original From/Reply-To;
- original To;
- original Cc;

excluding own mailbox addresses and duplicates.

### Step 13.3 — Thread headers

Set `In-Reply-To` and `References`.

### Step 13.4 — Subject

Normalize exactly one `Re:` prefix for display/send.

### Step 13.5 — Test externally

Conversation must group in mainstream clients where their threading rules permit.

**Exit criteria**

Two-way multi-message conversation remains one Mailbox thread and normally one external client thread.

---

## Phase 14 — Provider Delivery Events

### Step 14.1 — Persist all relevant events

`provider_events`.

### Step 14.2 — Match by Resend email ID

Update outbound message.

### Step 14.3 — Event precedence

Implement explicit reducer.

### Step 14.4 — UI badges

Display final state.

### Step 14.5 — Failure details

Bounced/failed message gets expandable diagnostics.

**Exit criteria**

A known Resend test bounce updates Sent state without manual refresh after next page update/poll.

---

## Phase 15 — Search

### Step 15.1 — FTS indexing

Existing messages.

### Step 15.2 — Parser

Implement supported operators.

### Step 15.3 — Query compiler

Use parameterized SQL.

Never concatenate raw values into SQL.

### Step 15.4 — Results UI

Thread-centric results.

### Step 15.5 — Reindex command

```text
mailbox reindex
```

**Exit criteria**

Search finds subject/body/sender and filter combinations.

---

## Phase 16 — Settings and Diagnostics

### Step 16.1 — Mailbox settings

Display name editable.

Do not allow arbitrary primary address change until reconfiguration implications are handled.

### Step 16.2 — System page

Show health.

### Step 16.3 — Webhook page

Latest verified events, type, status.

### Step 16.4 — Jobs page

Retry dead jobs.

### Step 16.5 — Storage page

blob-storage connectivity.

**Exit criteria**

Operator can diagnose common failure without SSH/log digging.

---

## Phase 17 — Trash and Permanent Deletion

### Step 17.1 — Soft trash

Thread state.

### Step 17.2 — Restore

Correct previous state.

### Step 17.3 — Permanent delete

DB tombstone + blob-deletion job.

### Step 17.4 — Verify object removal

Tests.

**Exit criteria**

No orphaned blob under the normal delete path.

---

## Phase 18 — Backup, Restore, Doctor

### Step 18.1 — Safe SQLite backup

Implement CLI.

### Step 18.2 — Blob backup procedure

Document and test copying `/data/objects` to an independent backup destination together with the SQLite snapshot/manifest.

### Step 18.3 — Doctor

Integrity scan.

### Step 18.4 — Restore test

Automated integration scenario where possible.

**Exit criteria**

Fresh Compose deployment can restore prior mailbox and open old messages.

---

## Phase 19 — Production Hardening

### Step 19.1 — CSP

Enforce.

### Step 19.2 — Proxy trust

Enforce.

### Step 19.3 — Production secrets

No defaults.

### Step 19.4 — Body limits

All upload/webhook routes.

### Step 19.5 — Timeouts

HTTP server:

```text
ReadHeaderTimeout
ReadTimeout where appropriate
WriteTimeout where appropriate
IdleTimeout
```

Be careful not to set a write timeout that kills large streamed attachment downloads unexpectedly.

### Step 19.6 — Dependency scan

Run vulnerability tooling.

### Step 19.7 — Recovery tests

Restart app, unwritable/full storage simulation, provider outage.

**Exit criteria**

Production readiness checklist passes.

---

# 47. Testing Strategy

## 47.1 Unit tests

Required coverage areas:

### Address parsing

- simple address;
- display name;
- quoted display name;
- Unicode display name;
- invalid address;
- duplicate recipients;
- own-address exclusion.

### Subject normalization

- `Re:`;
- `RE:`;
- multiple prefixes;
- `Fwd:`;
- whitespace;
- empty subject.

### Threading

- direct In-Reply-To;
- References match;
- no match;
- misleading identical subject;
- participant mismatch.

### Sanitizer

- script removed;
- inline event removed;
- iframe removed;
- remote image blocked;
- `javascript:` link removed;
- CID rewritten;
- safe text preserved.

### Search parser

- operators;
- quoted strings;
- malformed dates;
- combined filters;
- literal colon.

### Job backoff

- retryable error;
- permanent error;
- max attempts;
- lease reclaim.

### Delivery reducer

- normal sequence;
- out-of-order sequence;
- terminal conflict.

---

# 48. Integration Tests

Use Docker Compose test profile.

## 48.1 SQLite

- migration;
- concurrency;
- WAL;
- FTS;
- backup.

## 48.2 Blob storage

- put/get/delete;
- unavailable/unwritable storage;
- large stream;
- checksum;
- no public/static exposure.

## 48.3 Resend adapter

Most tests use fake provider.

Add a separate opt-in live test suite:

```text
LIVE_RESEND_TESTS=1
```

Never run live sending automatically on every local test.

## 48.4 Webhooks

Create signed fixture payloads or use SDK-compatible test helper.

Verify:

- valid;
- invalid;
- duplicate;
- replay;
- out of order.

---

# 49. End-to-End Tests

Critical browser flows:

1. first-run setup;
2. login;
3. empty Inbox;
4. fixture inbound appears;
5. open message;
6. archive;
7. restore;
8. star;
9. compose draft;
10. upload attachment;
11. send with fake provider;
12. status updates;
13. reply;
14. search;
15. trash;
16. permanent delete.

Use a Go-friendly browser automation solution if desired, but E2E tooling does not become a runtime dependency.

---

# 50. Manual Acceptance Tests

Before MVP release, manually verify:

## Inbound

- Gmail -> `hello@abc.com`;
- Outlook -> `hello@abc.com`;
- plain text;
- HTML;
- multipart alternative;
- Unicode;
- long subject;
- PDF attachment;
- multiple attachments;
- inline image;
- unknown local-part;
- duplicate webhook replay.

## Outbound

- Gmail;
- Outlook;
- reply;
- reply all;
- CC;
- BCC;
- attachment;
- Unicode;
- bounce test;
- provider timeout simulation.

## Security

- script in inbound HTML;
- tracking pixel;
- HTML attachment;
- SVG attachment;
- filename `../../x`;
- oversized upload;
- invalid CSRF;
- invalid webhook signature;
- expired session.

---

# 51. Acceptance Criteria

The MVP is accepted only when all of the following are true.

## 51.1 Receiving

- A real external sender can email the configured address.
- Mail appears without manual provider polling.
- Duplicate webhook replay does not duplicate mail.
- Message body remains available from local storage/database.
- Attachments are copied into the configured BlobStore.

## 51.2 Sending

- User can compose and send.
- Stable idempotency prevents duplicate provider submission under retry.
- Sent item appears immediately.
- Delivery event updates status.
- Reply preserves standard thread headers.

## 51.3 Data ownership

- Old messages do not require live Resend retrieval for display.
- Raw inbound emails are archived in the configured BlobStore.
- Attachments are private.
- Backup procedure succeeds.

## 51.4 Security

- Inbound scripts cannot execute.
- Remote tracking images do not load automatically.
- `/data/objects` is not publicly accessible.
- Webhook signature is mandatory.
- Session/CSRF controls are enforced.

## 51.5 Operations

- `docker compose up -d` starts stack.
- reboot/restart does not lose DB/object data.
- expired running jobs recover.
- admin can inspect failed jobs.
- health endpoints work.

---

# 52. Failure Scenarios and Required Behavior

## Scenario A — Resend webhook arrives twice

Expected:

- first event persisted;
- second `svix-id` duplicate is acknowledged;
- one message only.

## Scenario B — Same email replayed under another webhook delivery ID

Expected:

- webhook event can be distinct;
- ingest job dedupe by `resend_email_id`;
- message unique by provider email ID;
- no duplicate email.

## Scenario C — Local blob storage write fails during inbound ingest

Expected:

- webhook is already durably recorded in SQLite;
- ingest job remains retryable;
- error is visible in diagnostics;
- a partial temp file never becomes a valid final blob;
- once storage becomes writable, ingestion completes;
- a full disk triggers backoff rather than a hot retry loop.

## Scenario D — App down when email arrives

Expected:

- Resend retries webhook according to provider behavior;
- once app is online and event is delivered, normal ingest occurs.

Operationally, long outages can exceed provider retry/retention assumptions; uptime and monitoring still matter.

## Scenario E — App dies after Resend accepted outbound mail but before SQLite update

This is one of the most dangerous paths.

Mitigation:

- stable provider idempotency key;
- retry with same key;
- optional reconciliation via provider/event data;
- never generate new send key for same logical message.

## Scenario F — Attachment URL expires before worker downloads it

Expected:

- worker requests attachment metadata again to obtain a fresh temporary URL;
- then retries download.

## Scenario G — Malicious HTML mail

Expected:

- raw preserved privately;
- UI displays sanitized content;
- no script execution;
- remote images blocked.

## Scenario H — SQLite DB is busy

Expected:

- busy timeout;
- short transactions;
- worker retries;
- no HTTP 500 from transient writer contention where a short retry can recover.

---

# 53. Performance and Resource Budget

Target deployment:

```text
1 vCPU minimum
512 MB RAM recommended
256 MB may be viable for very small installations after measurement
SSD-backed persistent volume
```

The default stack has no separate database or object-storage process, so resource use is driven mainly by the Go process, SQLite cache, HTML sanitization, and bounded attachment handling.

Mailbox Go process target:

```text
idle RSS: as low as practical
normal request concurrency: low tens
worker count: 2
```

Avoid:

- loading entire Inbox;
- unbounded Goroutines;
- unbounded email bodies;
- full attachment buffering;
- expensive per-request Resend calls.

---

# 54. Production Deployment Topology


Recommended minimal VPS topology:

```text
VPS
|
+-- Docker
    |
    +-- mailbox
         localhost/internal :8080
         |
         +-- /data/mailbox.db
         +-- /data/objects/
```

Public ingress is separate:

```text
Internet
   |
   v
Caddy / Nginx / Traefik / trusted tunnel
   |
   v
127.0.0.1:8080
   |
   v
mailbox container
```

If the operator has no existing ingress, enable the optional Caddy profile, resulting in two containers. RustFS is **not** part of the recommended topology.

Advanced optional S3 topology:

```text
mailbox
   |
   +-- SQLite on /data
   +-- BlobStore(S3) --> RustFS / S3 / R2
```

DNS:

```text
mail.abc.com -> public ingress
```

Email MX/DKIM/SPF:

```text
abc.com -> exact records provided by Resend
```

Web UI:

```text
https://mail.abc.com
```

Mailbox identity:

```text
hello@abc.com
```

Web-host DNS and mail DNS are separate concerns.

---

# 55. Recommended MVP Cut Line


### Must ship

- authentication;
- Inbox/read/thread;
- sanitized HTML and blocked remote images;
- attachments;
- Compose;
- Reply/Reply all;
- Sent + delivery state;
- Drafts;
- Archive/Trash/Starred;
- search;
- durable local `.eml` storage;
- durable local attachment storage;
- signed resilient webhook ingestion;
- SQLite durable jobs;
- backup/restore/doctor;
- diagnostics.

### Explicitly defer

- RustFS/S3 as a required dependency;
- rich-text composer;
- keyboard shortcuts;
- labels/rules;
- sophisticated spam model;
- remote image proxy;
- multiple mailboxes/users;
- alias-management UI;
- scheduled send;
- templates;
- contacts;
- IMAP/JMAP.

The `BlobStore` abstraction must ship in MVP. The optional S3 implementation does **not** need to ship before the filesystem-backed mailbox is reliable.

---

# 56. Future Extensions

## 56.1 Multiple aliases

Example:

```text
hello@abc.com
support@abc.com
billing@abc.com
```

One Inbox with alias filters or separate inboxes.

## 56.2 Multiple users

Add:

- memberships;
- per-mailbox permissions;
- assignment;
- internal notes.

This starts turning product into a shared inbox.

## 56.3 Rich text

Add a small editor only after plain-text workflows are stable.

## 56.4 Rules

Example:

```text
from:*@stripe.com -> label Finance
subject contains invoice -> star
```

## 56.5 Spam handling

Add:

- sender block list;
- heuristics;
- external spam classification;
- spam folder.

## 56.6 Notifications

Browser/Web Push or mobile PWA notification when inbound mail is archived.

## 56.7 Optional S3/RustFS backend

Add `S3Store` when a real requirement appears, for example remote storage, an existing S3 environment, local disk limitations, or future multi-replica deployment. RustFS is one compatible option, not a product dependency.

## 56.8 IMAP/JMAP

Only if a real use case emerges.

Implementing a standards-compliant mail protocol server is a different project and should not be casually added to this MVP.

---

# 57. Key Engineering Risks


## Risk 1 — Single-volume data loss

The simplest topology puts SQLite and blobs on one volume. Losing it loses mail.

Mitigation: explicit external backups, `mailbox backup`, `mailbox doctor`, tested restore procedures, and optional S3 storage for users with a concrete need.

## Risk 2 — Resend API/product changes

Mitigation: isolate the provider adapter, pin SDK versions per release, run provider contract tests, and keep SDK models out of domain code.

## Risk 3 — Provider retention mistaken for mailbox retention

Mitigation: archive inbound content promptly and make normal mailbox browsing fully local.

## Risk 4 — Unsafe HTML

Mitigation: strict sanitization, CSP, no automatic remote images, and regression fixtures for hostile email.

## Risk 5 — Duplicate outbound send

Mitigation: durable job state, stable Resend idempotency key, unique provider ID, and explicit reconciliation.

## Risk 6 — Partial/corrupt local blobs

Mitigation: temp file + fsync + atomic rename, checksums, immutable blob semantics, doctor scans, and backup.

## Risk 7 — SQLite contention

Mitigation: WAL, short transactions, bounded workers, no binary blobs in SQLite, measure before replacing SQLite.

## Risk 8 — Disk exhaustion

Mitigation: expose free-space diagnostics, configurable low/critical thresholds, graceful ingestion failure, retry backoff, and optional external storage later.

## Risk 9 — Premature infrastructure growth

The biggest architecture risk is recreating a cloud mail platform around a one-mailbox use case.

Mitigation: one-container core is an explicit constraint; no new required service without a measured need; advanced storage remains an adapter/profile.

---

# 58. Definition of Done

A feature is not "done" merely because it works in the browser.

For each feature, Definition of Done requires:

1. domain logic implemented;
2. authorization implemented;
3. validation implemented;
4. error states implemented;
5. structured logs added where operationally useful;
6. unit tests;
7. integration tests where infrastructure involved;
8. mobile/responsive behavior checked;
9. accessibility basics checked;
10. security implications reviewed;
11. no new secrets logged;
12. documentation updated.

---

# 59. Build Order Summary


```text
1. Go skeleton
2. SQLite + migrations
3. BlobStore + FileStore
4. authentication
5. Resend adapter
6. signed webhook persistence
7. durable SQLite jobs
8. inbound archival
9. Inbox/read UI
10. authenticated attachment serving
11. drafts
12. outbound send
13. replies/thread headers
14. delivery events
15. search
16. archive/star/trash
17. diagnostics
18. backup/restore/doctor
19. disk/security hardening
20. production release
21. optional S3/RustFS adapter only if justified
```

Do not begin with a visually complete mailbox mockup, and do not implement RustFS before local filesystem storage has proven insufficient.

The difficult parts are reliable ingestion, idempotent sending, safe HTML, correct-enough threading, durable storage semantics, and recovery. **The storage server is not the product.**

---

# 60. Reference Documentation


Recheck provider documentation during implementation because APIs and limits can change.

## Resend

```text
https://resend.com/docs/dashboard/receiving/introduction
https://resend.com/docs/dashboard/receiving/custom-domains
https://resend.com/docs/api-reference/emails/retrieve-received-email
https://resend.com/docs/api-reference/emails/list-received-emails
https://resend.com/docs/dashboard/receiving/attachments
https://resend.com/docs/api-reference/emails/list-received-email-attachments
https://resend.com/docs/dashboard/receiving/reply-to-emails
https://resend.com/docs/webhooks/introduction
https://resend.com/docs/webhooks/verify-webhooks-requests
https://resend.com/docs/api-reference/emails/send-email
https://resend.com/docs/send-with-go
https://github.com/resend/resend-go
https://resend.com/docs/knowledge-base/account-quotas-and-limits
https://resend.com/docs/dashboard/emails/attachments
```

## Optional RustFS/S3 reference

```text
https://docs.rustfs.com/
https://docs.rustfs.com/features/s3-compatibility/
```

RustFS documentation is only relevant to the optional S3 deployment.

## Architectural reference projects

Useful for studying mailbox UX and transport/storage boundaries, not for copying their stacks:

```text
https://github.com/cloudflare/agentic-inbox
https://github.com/maillab/cloud-mail
https://github.com/resend/resend-webhooks-ingester
```

The goal of this project is deliberately smaller and operationally simpler.

---

# 61. Final Product Statement

The first release is:

> **A brutally lightweight, self-hosted human mailbox powered by Resend — not a self-hosted mail server and not a Gmail clone.**

Responsibility boundaries:

```text
Resend
    internet email transport
    inbound parsing
    outbound delivery
    delivery webhooks

Go application
    UI + auth
    threading + mailbox state
    webhook ingestion
    background jobs
    search
    attachment serving

SQLite
    metadata
    mailbox state
    job queue
    FTS

Local filesystem
    raw .eml archives
    attachments
    draft files

Optional S3-compatible backend
    RustFS / S3 / R2 / compatible services
```

The critical architectural constraint is:

> **The default mailbox must remain deployable as one Go container with one durable `/data` volume.**

Any future required component that breaks that property must justify itself with a measured requirement.

The product should win on simplicity, low resource usage, self-hostability, understandable failure modes, minimal infrastructure, and durable ownership of mailbox history.

The storage server is optional.

The mail server is outsourced.

**The mailbox is the product.**
