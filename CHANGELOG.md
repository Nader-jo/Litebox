# Changelog

All notable changes to Litebox are documented here. The project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add a configuration-lifecycle reference covering deployment settings,
  new-volume bootstrap values, SQLite-backed seeds, validation, and Compose-only
  variables.

### Changed

- Request Resend received-email HTML with `html_format=cid` so authenticated
  inline-image rewriting remains compatible with the sanitizer's `data:` URL
  rejection.
- Refactor command startup so `litebox migrate` opens and migrates only SQLite,
  without creating a master key or mailbox, initializing provider/storage
  services, enqueueing jobs, or starting HTTP infrastructure.
- Handle CLI help, version, unknown commands, invalid flags, missing required
  options, and unexpected positional arguments before loading configuration or
  touching installation data.
- Keep request and attachment limits SQLite-backed after setup, and apply saved
  log-level changes to the active logger immediately.
- Rotate and print a fresh setup token on every unconfigured production startup
  so restarting replaces a lost link and invalidates the previous token.
- Complete first-run setup as one transaction covering the token claim, primary
  mailbox, first owner, and encrypted settings, with rollback on any failure.
- Renew leases while durable jobs run and require lease ownership for completion,
  retry, and dead-letter transitions.
- Publish immutable filesystem blobs without replacing an existing key, and
  move draft submission/deletion, object-cleanup enqueueing, account-token
  consumption, and delivery-event reduction into race-safe transactions.
- Fall back to lock-serialized atomic rename when a filesystem cannot publish
  with hard links; honor cancellation while blob writers wait and report the
  exact `.litebox-lock` path for deliberate stale-lock recovery.
- Render summary periods in each subscriber's configured time zone and discard
  work for subscriptions disabled after enqueue.
- Run the maintenance scheduler every minute so its UTC-date-deduplicated
  expired-session cleanup is enqueued after midnight as well as at startup.
- Cache the SQLite and two-directory writable readiness probe for five seconds
  to bound healthcheck I/O without making readiness depend on Resend.
- Parse multipart uploads with a fixed 4 MiB file-part memory threshold, spill
  larger parts to bounded temporary storage, and clean request spill files.
- Apply configured raw-size, aggregate attachment-byte, and attachment-count
  limits to inbound archival. Oversized raw mail remains readable without its
  raw archive, while over-budget attachments retain unavailable metadata rather
  than hiding the message or retrying a deterministic limit failure.
- Require an existing installation to load its regular 32-byte master key before
  opening or migrating SQLite, while a new installation publishes the key before
  database creation. Keep `STORAGE_TMP_ROOT` as private scratch space while
  staging immutable blob commits beside their final destination.
- Correct deployment, recovery, release, search, configuration, and contributor
  documentation, including the `v0.4.1` deployment-template erratum and the
  optional Caddy profile required for the supplied public-HTTPS path.

### Fixed

- Preserve the active mailbox query parameter across keyboard navigation,
  settings forms, redirects, and other navigable actions so separate browser
  tabs retain independent mailbox context.
- Describe development as a real-provider-capable mode rather than a transport
  kill switch, and describe `create-admin` as bootstrap/recovery rather than a
  complete headless setup flow.
- Escape absolute SQLite file URIs for normal and backup opens so path characters
  such as `?` and `#` cannot become URI controls.
- Mark an inbound replay of the same provider email under a different Svix ID as
  terminal `duplicate` when its provider-level job deduplicates.

### Security

- Reject unknown `APP_ENV` and `APP_LOG_LEVEL` values instead of silently
  weakening production-only checks or falling back to a different log level.
- Reject malformed public origins, listeners, cookie names, and trusted-proxy
  CIDRs, numeric values above supported safety bounds, and overflowing duration
  conversions during configuration validation.
- Cap webhook/text values at 16 MiB, uploads at 128 MiB, aggregate attachments
  at 64 MiB, attachment count at 100, and workers at 32; also enforce a 256 MiB
  worker/attachment budget, a 50 ms minimum poll, and at most 64 claim attempts
  per second.
- Require HTTPS for provider downloads and redirects in production, and reject
  localhost plus loopback, private, carrier-grade NAT, link-local, benchmark,
  documentation, unspecified, and reserved IP targets both before and after DNS
  resolution.
- Bound concurrent Argon2 password verification, perform a dummy verification
  for unknown accounts, and enforce both per-account and per-IP login limits.
- Give password-reset requests separate per-account/per-IP limits and a generic
  minimum-duration response, then deliver known-account notifications through a
  bounded process-local queue so provider timing cannot enumerate accounts.
- Trust `X-Forwarded-For` only from configured direct peers and walk proxy chains
  right-to-left across trusted hops; malformed chains fall back to the peer IP.
- Reject cross-site browser submissions to unauthenticated setup, login,
  invitation, and password-reset POST routes using fetch metadata plus
  `Origin`/`Referer` validation, while retaining headerless non-browser clients.
- Recompute outbound attachment size and SHA-256 before provider submission,
  and mark dynamic HTML plus authenticated attachment responses `no-store`.
- Preflight restore manifests for supported structure, canonical contained
  source paths, exact database/key objects, a 32-byte key, a 16 MiB/100,000-blob
  resource ceiling, unique logical blob keys, and valid size/digest metadata;
  decrypt settings and match database references before writes, verify installed
  content, and remove partial output after any failure.

## [0.4.1] - 2026-08-17

### Added

- Add digest previews, immediate test delivery, and persisted last-attempt, last-success, count, and error status in Settings → Summary.
- Add single-use mailbox invitations and password recovery links with hashed, expiring tokens and session revocation after a reset.
- Preserve mailbox context in URL query parameters so multiple tabs can operate independent mailboxes safely.
- Show the receiving or sending alias and its light color in thread-list rows as well as conversation messages.
- Add the required `LITEBOX_DOMAIN` container variable to bootstrap the public URL and optional Caddy hostname.

### Security

- Store invitation and password-reset tokens only as hashes and consume them atomically.

## [0.4.0] - 2026-08-17

### Added

- Add a guided first-run wizard for mailbox identity, public URL, Resend credentials, and the first administrator.
- Persist product settings in SQLite with AES-GCM encrypted provider credentials and a file-backed instance master key.
- Add light, deterministic colors for mailbox aliases and show the matching alias on every conversation message.
- Add per-user daily or weekly metadata-only email summaries with timezone, delivery hour, mailbox scope, and private recipient controls.

### Changed

- Import product-setting seeds once and treat SQLite as the source of truth
  afterward; manage the fields exposed by the product under Settings → System.
- Keep deployment topology, worker/proxy controls, and new-volume bootstrap
  seeds in environment configuration.
- Publish one provenance-attested GHCR manifest for Linux AMD64 and ARM64 instead of bundling native binaries, installers, or VPS archives.

### Security

- Protect first-run setup with a one-time token printed to production container logs.
- Never include message content in scheduled summaries; include the matching master-key file in the protected backup and restore it with the database and blobs.

## [0.3.1] - 2026-08-17

### Added

- Scan every AMD64 and ARM64 container build for fixed high and critical vulnerabilities in CI and before a GitHub release is made public.
- Enforce a 20 MiB production-image budget and assert that the runtime contains no shell.

### Changed

- Replace the Alpine production stage with a minimal `scratch` runtime while retaining CA certificates, timezone data, license notices, a non-root identity, and writable `/data` and `/tmp` mount points.
- Copy only the Go packages and embedded web assets required by the build, and verify downloaded Go modules before compiling.

### Security

- Remove the production shell, package manager, and operating-system packages, reducing the runtime filesystem and attack surface.
- Upgrade release and contributor builds to Go 1.26.6 to incorporate the latest standard-library security fixes.
- Pin the container scanner action to an immutable verified commit and the scanner itself to Trivy v0.74.0.

## [0.3.0] - 2026-08-11

### Added

- Add keyboard shortcuts for composing, searching, navigating folders and threads, replying, and opening an in-app shortcut reference.
- Add clear send, save, attachment, and discard feedback with accessible status toasts and pending-action states.
- Add an end-user guide, a data-safe upgrade guide, a project vision, and contributor recognition.
- Add a one-command `make setup` contributor bootstrap and a pinned development container.
- Add pinned `golangci-lint` checks locally and on every pull request.

### Changed

- Improve responsive mailbox, composer, search, and navigation layouts for small screens.
- Expand release and installer bundles with user, upgrade, and backup documentation.
- Clarify the roadmap, contribution path, support entry points, and release process.

### Security

- Allowlist UI notification codes instead of reflecting arbitrary query-string content.
- Keep static analysis reproducible by pinning the Go lint toolchain used by contributors and CI.

## [0.2.2] - 2026-08-11

### Added

- Add a single guided `setup.sh` for architecture checks, verified release download, secret-safe configuration, startup, and first-login guidance.
- Add a private `--demo` mode that runs locally with Docker and requires neither a domain nor Resend credentials.
- Publish the installer and its SHA-256 checksum as release assets and include the installer in every VPS bundle.

### Changed

- Replace the multi-step quick start with one command while retaining documented review-first and fully manual installation paths.
- Preserve existing environment configuration when the installer is re-run, updating only the immutable image version unless `--reconfigure` is requested.

### Security

- Validate release bundle paths and checksums before extraction, keep secret prompts out of terminal echo, and enforce installer lint and fixture tests in CI.

## [0.2.1] - 2026-08-11

### Fixed

- Verify each architecture by its immutable child-manifest digest so sequential multi-platform release smoke tests cannot collide in Docker's local image store.

## [0.2.0] - 2026-08-11

### Added

- Independent mailboxes with isolated threads, drafts, folders, search, and unread state.
- Multiple aliases per mailbox with inbound recipient routing and selectable outbound identities.
- Multiple administrators with owner, admin, member, and viewer roles scoped per mailbox.
- Mailbox switching and an active-session page with individual device revocation.
- A polished responsive interface using a navy/signal-blue design system and accessible Tabler Icons v3.46.0 SVG paths.
- Version-pinned VPS deployment bundles attached to every release.
- Linux AMD64 and ARM64 container smoke tests in CI and the release pipeline.

### Changed

- Make production Compose consume the published image exclusively; source builds now use `compose.build.yaml`.
- Cross-compile the Go binary with BuildKit target-platform arguments instead of compiling under CPU emulation.
- Keep GitHub releases in draft state until the multi-platform image is published, attested, and verified healthy.

### Security

- Scope interactive message, thread, draft, attachment, and search operations to an authorized mailbox membership.
- Treat the active-mailbox cookie as an untrusted preference and re-authorize it on every request.
- Preserve provider idempotency while isolating messages delivered to more than one local mailbox.
- Publish GitHub/Sigstore provenance for the multi-platform image digest.

## [0.1.0] - 2026-08-10

### Added

- Initial build-ready mailbox implementation.
- One-container Go, SQLite, and filesystem architecture.
- Signed durable Resend webhook ingestion and delivery reconciliation.
- Inbox, threads, drafts, compose, replies, attachments, search, folder state, and diagnostics.
- Backup, restore, doctor, migration, healthcheck, reindex, and admin recovery commands.
- Keyset pagination, documented REST compatibility routes, and per-user send throttling.
- Hardened container, CI, security scanning, SBOM-enabled releases, and community documentation.

### Security

- Require Go 1.26.5 or newer so CI, release builds, and contributors use a toolchain containing the current Go standard-library security fixes.
- Add automated GitHub Actions security analysis, retain OpenSSF Scorecard results, stop persisting checkout credentials, and validate release tags before publishing.
- Group coupled CodeQL updates so all analysis phases move to one immutable commit together.

[Unreleased]: https://github.com/Nader-jo/Litebox/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/Nader-jo/Litebox/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/Nader-jo/Litebox/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/Nader-jo/Litebox/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/Nader-jo/Litebox/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/Nader-jo/Litebox/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/Nader-jo/Litebox/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/Nader-jo/Litebox/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Nader-jo/Litebox/releases/tag/v0.1.0
