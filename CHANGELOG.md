# Changelog

All notable changes to Litebox are documented here. The project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2026-08-17

### Added

- Add a guided first-run wizard for mailbox identity, public URL, Resend credentials, and the first administrator.
- Persist product settings in SQLite with AES-GCM encrypted provider credentials and a file-backed instance master key.
- Add light, deterministic colors for mailbox aliases and show the matching alias on every conversation message.
- Add per-user daily or weekly metadata-only email summaries with timezone, delivery hour, mailbox scope, and private recipient controls.

### Changed

- Existing environment settings are imported once; runtime settings are subsequently managed under Settings → System.
- Keep only deployment topology (database path, listener, storage paths, and trusted proxies) in environment configuration.
- Publish one signed GHCR manifest for Linux AMD64 and ARM64 instead of bundling native binaries, installers, or VPS archives.

### Security

- Protect first-run setup with a one-time token printed to production container logs.
- Never include message content in scheduled summaries; preserve the master-key file alongside backups or provide it separately during restore.

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

[Unreleased]: https://github.com/Nader-jo/Litebox/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/Nader-jo/Litebox/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/Nader-jo/Litebox/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/Nader-jo/Litebox/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/Nader-jo/Litebox/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/Nader-jo/Litebox/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/Nader-jo/Litebox/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Nader-jo/Litebox/releases/tag/v0.1.0
