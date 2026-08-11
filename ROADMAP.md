# Roadmap

Litebox's roadmap favors reliability and operational simplicity over feature count. Discussion issues should explain the user problem and how a proposal preserves the one-container default.

## 0.1 — Reliable mailbox foundation

- [x] secure first-run administrator and sessions;
- [x] durable signed webhook ingestion;
- [x] local raw email and attachment ownership;
- [x] inbox, conversations, drafts, compose, reply, folders, and search;
- [x] outbound idempotency and delivery-state reconciliation;
- [x] diagnostics, backup, restore, doctor, CI, releases, and security policy;

## 0.2 — Multi-mailbox VPS release

- [x] multiple independent mailboxes and aliases;
- [x] multiple users, per-mailbox roles, mailbox switching, and session management;
- [x] signed Linux amd64/arm64 images and version-pinned VPS release bundles;
- [ ] wider real-provider acceptance testing and accessibility audit;
- [ ] performance measurements at 100,000 messages.

## Near-term candidates

- storage-capacity thresholds and notifications;
- better inline-CID message fixtures;
- richer webhook/provider reconciliation tooling;
- optional trash-retention cleanup;
- keyboard navigation that preserves server-rendered routes;
- import/export tooling;
- optional S3-compatible `BlobStore` plus explicit migration command.

## Later, only with demonstrated demand

- shared-inbox assignment and internal notes;
- rules and labels;
- browser notifications;
- simple rich-text composition;
- spam controls.

## Explicitly not planned for the MVP

SMTP/IMAP/POP3/JMAP servers, calendar, contacts sync, marketing email, tenant billing, Kubernetes requirements, mandatory Postgres/Redis/S3, end-to-end encryption protocols, and Gmail-scale feature parity.
