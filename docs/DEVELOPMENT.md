# Development guide

## Toolchain

- Go 1.26.6 or newer (the patch-level floor includes required standard-library security fixes);
- Docker Engine and Compose for container verification;
- GNU Make for convenience targets (all commands can also run directly).

No Node.js toolchain is used. HTMX is vendored and the UI is authored in templ plus CSS.

## Bootstrap

```bash
git clone https://github.com/Nader-jo/Litebox.git
cd Litebox
make setup
```

This single command downloads modules, installs every pinned contributor tool,
generates committed templ output, validates Compose, and runs focused smoke
tests. Re-running it is safe.

Local development defaults to `.data/` and `hello@example.com`; Resend credentials are optional until a provider workflow is exercised.

```bash
APP_ENV=development go run ./cmd/mailbox serve
```

Open <http://localhost:8080/setup>.

To exercise the complete container locally from source:

```bash
docker compose -f compose.yaml -f compose.build.yaml up -d --build
make container-smoke
```

Production Compose intentionally has no `build` section; it consumes the published multi-platform, shell-free `scratch` image.

## Development container

`Dockerfile.dev` provides the pinned Go and C toolchains plus repository tools
without installing them on the host. The C toolchain keeps race-detector tests
available inside the container:

```bash
docker build -f Dockerfile.dev -t litebox:dev .
docker run --rm -it -p 8080:8080 \
  -v "$PWD:/workspace" -v litebox-go-modules:/go/pkg/mod \
  litebox:dev
```

The source bind mount keeps edits on the host. Development data is written to
the ignored `.data/` directory. Add `-v /var/run/docker.sock:/var/run/docker.sock`
only when you understand that this gives the container control of the host
Docker daemon and need to run Compose or container smoke tests inside it.

## Generated UI

Edit `internal/ui/*.templ`, never `*_templ.go` directly.

```bash
make generate
git diff --check
```

Generated files are committed. CI regenerates them and rejects drift. Browser assets are embedded from `web/static`.

## Interface icons

Interface icons use the exact outline paths from [Tabler Icons](https://tabler.io/icons), currently pinned to v3.46.0. Do not freehand replacement SVG paths. When adding an icon:

1. choose an existing Tabler outline icon on its 24×24 grid;
2. copy the upstream path data verbatim into `internal/ui/icons.templ`;
3. add the icon name to `internal/ui/icons_test.go`;
4. update the pinned version and MIT notice if the upstream version changes;
5. regenerate templ output and visually verify desktop and mobile rendering.

## Package rules

- Environment reads belong in `internal/config`.
- SQL belongs in `internal/repository`; use placeholders for every value.
- Interactive content repositories must require `mailbox_id`; an object ID alone is never an authorization boundary.
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
make golangci-lint
make workflow-lint
make vuln
make container-smoke
```

`make golangci-lint` runs golangci-lint v2 with the repository's checked-in
configuration. CI runs it for every push and pull request in addition to Go
vet and staticcheck.

CI builds, vulnerability-scans, and health-checks both `linux/amd64` and
`linux/arm64`. The release workflow repeats the scan and hardened smoke tests
against each published platform digest before making the draft GitHub release
public. Container smoke tests also enforce the 20 MiB size budget and verify
that the production filesystem has no shell.

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
- multi-mailbox routing, membership roles, session revocation, and cross-mailbox isolation;
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
