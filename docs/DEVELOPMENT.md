# Development guide

## Toolchain

- Go 1.26.5 or newer (the patch-level floor includes required standard-library security fixes);
- Docker Engine and Compose for container verification;
- GNU Make for convenience targets (all commands can also run directly).

No Node.js toolchain is used. HTMX is vendored and the UI is authored in templ plus CSS.

## Bootstrap

```bash
git clone https://github.com/Nader-jo/Litebox.git
cd Litebox
go mod download
make generate
make test
```

Local development defaults to `.data/` and `hello@example.com`; Resend credentials are optional until a provider workflow is exercised.

```bash
APP_ENV=development go run ./cmd/mailbox serve
```

Open <http://localhost:8080/setup>.

## Generated UI

Edit `internal/ui/*.templ`, never `*_templ.go` directly.

```bash
make generate
git diff --check
```

Generated files are committed. CI regenerates them and rejects drift. Browser assets are embedded from `web/static`.

## Package rules

- Environment reads belong in `internal/config`.
- SQL belongs in `internal/repository`; use placeholders for every value.
- Provider SDK types stay in `internal/provider`.
- Mailbox workflows depend on `blobstore.Store`, not `os` or `filepath`.
- HTTP handlers perform boundary validation and call repository/service methods.
- Durable job handlers must be idempotent and must not log payloads containing personal data.
- Public errors must not contain secrets, SQL, absolute paths, signed URLs, or stack traces.

## Tests

```bash
make test
make test-race
make lint
make workflow-lint
make release-check
make vuln
```

The required high-risk suites cover:

- Argon2id and session tokens;
- address parsing and Reply all exclusion;
- subject and References normalization;
- hostile HTML and remote images;
- delivery event precedence;
- search operators and malformed filters;
- atomic/idempotent blob writes and traversal rejection;
- migrations and FTS5;
- webhook/job dedupe and lease recovery;
- inbound archival and outbound idempotency;
- first-run/login/CSRF;
- backup, restore, and deep doctor.

Provider tests use an `httptest` server through `provider.NewResendForTest`. Do not make live Resend sends part of the default suite. If a future live suite is added, gate it behind `LIVE_RESEND_TESTS=1` and use a dedicated test domain/account.

## Schema changes

Add a new monotonically named SQL file under `internal/db/migrations`. Never edit a released migration. Migrations are embedded, ordered, transactional, and recorded in `schema_migrations`.

Test:

- empty database migration;
- repeated migration;
- upgrade from the previous released schema;
- foreign keys and FTS;
- rollback behavior for failure.

## Browser checks

For UI changes, verify desktop and narrow layouts plus keyboard focus, labels, error states, reduced motion, long subjects, Unicode names, empty folders, multiple attachments, and provider-failure badges. Include screenshots in the pull request.

## Commit and pull-request style

Prefer focused Conventional Commit subjects:

```text
feat: add attachment storage diagnostics
fix: preserve terminal delivery precedence
docs: clarify quiesced backup contract
```

Pull requests must explain user impact, data/schema impact, security implications, validation performed, and documentation changes. Avoid mixing refactors with behavior changes unless the relationship is necessary.
