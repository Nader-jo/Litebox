# ADR 0002: Provider transport, local mailbox ownership

- Status: Accepted
- Date: 2026-08-10

## Context

Resend receives, parses, and sends internet email, but provider retention and idempotency windows are not mailbox-history guarantees.

## Decision

Resend is an email transport adapter. Litebox persists normalized metadata/search text in SQLite and promptly archives raw inbound email plus attachments in its configured `BlobStore`. Normal browsing never retrieves historical content from Resend.

## Consequences

- Successful local archival defines durable mailbox ownership.
- Temporary signed provider URLs are never stored as durable attachment locations.
- Webhook/job persistence and retry safety are product-critical.
- Backup and restore must cover SQLite and every referenced object as one logical dataset.
