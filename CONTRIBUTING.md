# Contributing to Litebox

Thank you for helping make self-hosted email simpler and safer. Contributions of
code, documentation, tests, design feedback, and reproducible bug reports are
welcome.

## First contribution in five commands

```bash
git clone https://github.com/YOUR-USER/Litebox.git
cd Litebox
git switch -c fix/short-description develop
make setup
make check
```

`make setup` downloads modules, installs pinned contributor tools, regenerates
committed UI code, validates both Compose configurations, and runs a small
environment smoke test. It is safe to run again.

If installing Go tools locally is inconvenient, build the development image:

```bash
docker build -f Dockerfile.dev -t litebox:dev .
docker run --rm -it -p 8080:8080 \
  -v "$PWD:/workspace" -v litebox-go-modules:/go/pkg/mod \
  litebox:dev
```

Open <http://localhost:8080/setup>. The development image contains the pinned
Go and C toolchains, Make, Docker CLI, templ, staticcheck, govulncheck,
actionlint, and golangci-lint. Mount the Docker socket only when you
intentionally need container checks from inside it.

## Before you begin

- Search existing issues and discussions before opening a new one.
- Use a security advisory, not a public issue, for suspected vulnerabilities;
  see [SECURITY.md](SECURITY.md).
- Keep changes focused. Large architectural changes should start with an issue
  so maintainers and contributors can agree on direction before implementation.
- Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Development workflow

1. Fork the repository and create a branch from `develop`.
2. Install the Go version declared in `go.mod`, Docker, and GNU Make—or use
   `Dockerfile.dev`.
3. Run `make setup` to prepare and validate the checkout.
4. Make the smallest coherent change, including tests and documentation.
5. Run `make check` before opening a pull request.
6. Complete the pull request template and link the relevant issue.

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for local setup, repository
layout, test conventions, and useful commands.

## Pull request expectations

A change is ready for review when it:

- has a clear motivation and a focused diff;
- includes automated tests for behavior changes;
- preserves database migration compatibility and existing stored data;
- documents new configuration, operations, or user-visible behavior;
- keeps generated `templ` files in sync;
- passes formatting, tests, static analysis, and vulnerability checks; and
- does not introduce secrets, private email content, or personal data into
  fixtures, logs, screenshots, or commit history.

Reviews prioritize correctness, security, recoverability, accessibility, and
operational simplicity. Maintainers may ask for an architectural decision
record under `docs/decisions/` when a choice creates a lasting constraint.

## Commit and change style

Use short, imperative commit subjects. Conventional Commit prefixes such as
`feat:`, `fix:`, `docs:`, `test:`, and `chore:` are encouraged but not required.
Avoid drive-by formatting or unrelated refactors in the same pull request.

Add user-visible changes to the `Unreleased` section of `CHANGELOG.md`. Do not
edit version numbers in source files; release automation derives them from Git
tags.

## Database migrations

Migrations are append-only once released. Never modify a migration that may
have run in a user's installation. Add a new numbered migration, make it safe
to run exactly once, and test upgrading a database created by the previous
release. Destructive or lossy schema changes require an explicit recovery plan.

## Dependencies and generated assets

Keep the dependency surface small. Explain new runtime dependencies in the pull
request and prefer the standard library where it is a good fit. Vendored browser
assets must include their upstream version, license, source URL, and checksum in
`NOTICE`.

GitHub Actions must be pinned to full-length commit SHAs with a version comment,
use the smallest practical token permissions, and avoid persisting checkout
credentials. Run `make workflow-lint` after changing workflow or Dependabot
configuration; CI also scans workflow security with zizmor.

Generated files are committed so release builds do not require a template
compiler. Run `make generate` after changing any `.templ` file, and verify that
`make generated-check` succeeds.

## Licensing

By submitting a contribution, you agree that it may be distributed under the
Apache License 2.0 in [LICENSE](LICENSE), and that you have the right to submit
it. Third-party work must retain its notices and use a compatible license.
