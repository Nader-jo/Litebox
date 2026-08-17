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
- [x] signed Linux amd64/arm64 image releases;
- [x] private local demo using the published image;

## 0.3 — Product and community experience

- [x] end-user guide, safe upgrade runbook, and project vision;
- [x] mobile search, keyboard navigation, shortcut reference, and action feedback;
- [x] one-command contributor setup, development image, and golangci-lint gate;
- [x] contributor recognition and a visible contributions-welcome path;
- [ ] wider real-provider acceptance testing and full accessibility audit;
- [ ] performance measurements at 100,000 messages.

## 0.4 — Configurable multi-mailbox operations

- [x] database-backed onboarding and runtime settings with encrypted provider credentials;
- [x] multiple aliases with light, selectable colors carried into conversation messages;
- [x] per-admin daily or weekly metadata-only mailbox summaries with timezone and mailbox scope;
- [x] backup and restore handling for the installation master key;
- [x] image-only, multi-platform releases for Linux amd64 and arm64.

## Near-term candidates

- storage-capacity thresholds and notifications;
- better inline-CID message fixtures;
- richer webhook/provider reconciliation tooling;
- optional trash-retention cleanup;
- saved searches and carefully scoped user-defined filters;
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
