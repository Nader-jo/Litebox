# Changelog

All notable changes to Litebox are documented here. The project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/Nader-jo/Litebox/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/Nader-jo/Litebox/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/Nader-jo/Litebox/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Nader-jo/Litebox/releases/tag/v0.1.0
