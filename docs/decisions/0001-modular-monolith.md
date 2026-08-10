# ADR 0001: One-container modular monolith

- Status: Accepted
- Date: 2026-08-10

## Context

Litebox serves one user and one custom-domain mailbox. The difficult requirements are correctness and recovery around email transport, not horizontal scale.

## Decision

Use one Go process with server-rendered UI, HTTP routes, Resend adapter, SQLite repositories/jobs/FTS, and a filesystem `BlobStore` on one durable `/data` volume.

No database server, object-storage server, queue broker, cache, search service, frontend service, or JavaScript production runtime is required.

## Consequences

- Deployment, debugging, backup boundaries, and resource use remain understandable.
- SQLite write concurrency and one-volume durability are explicit constraints.
- Background work must use short SQLite leases and idempotent handlers.
- Operators must maintain independent backups.
- An optional S3-compatible adapter can be added behind `BlobStore` without changing mailbox-domain logic.

A proposal for a new required runtime service must include measurements showing why the one-container design is insufficient.
