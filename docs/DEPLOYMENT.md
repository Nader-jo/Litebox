# Production deployment

This guide assumes a small Linux VPS, Docker Engine, and a domain already controlled by the operator.

Litebox publishes one OCI image index per release at `ghcr.io/nader-jo/litebox`. The supported platforms are:

- `linux/amd64` for Intel and AMD x86-64 VPS instances;
- `linux/arm64` for 64-bit ARM hosts such as Ampere instances and Raspberry Pi-class servers.

Docker automatically selects the matching image from a version tag. Thirty-two-bit ARM is not supported.

## Minimum resources

- 1 vCPU;
- 512 MiB RAM recommended;
- SSD-backed persistent storage;
- independent backups of the persistent dataset;
- public HTTPS ingress able to reach `/webhooks/resend`.

## Image-only installation

Releases contain a provenance-attested multi-platform image and no application
archive. On a fresh VPS, install the small deployment templates and pull the
image:

```bash
VERSION=0.4.1
# v0.4.1 erratum: use the corrected current templates, not its source tag.
git clone --depth 1 https://github.com/Nader-jo/Litebox.git /opt/litebox
cd /opt/litebox
cp .env.example .env
# Pin the desired release; Docker selects amd64 or arm64 automatically.
sed -i "s|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:${VERSION}|" .env
# Set the public HTTPS hostname used by the app and the optional Caddy profile.
# Do not include https://, a port, or a path.
sed -i "s|^LITEBOX_DOMAIN=.*|LITEBOX_DOMAIN=mail.example.com|" .env
docker compose --profile proxy pull
docker compose --profile proxy up -d
```

The commands above enable the supplied Caddy service so the first deployment
has public HTTPS. If the host already has a reverse proxy, omit
`--profile proxy`, follow [Existing reverse proxy](#existing-reverse-proxy), and
keep the application port bound to loopback.

> [!NOTE]
> The immutable `v0.4.1` source tag contains stale deployment defaults that can
> resolve the application image to `latest`. For `0.4.1`, use the corrected
> current templates above and explicitly pin `LITEBOX_IMAGE` as shown. Do not
> move or recreate the public tag. Starting with the next release, replace the
> clone command with
> `git clone --depth 1 --branch "v${VERSION}" https://github.com/Nader-jo/Litebox.git /opt/litebox`
> so deployment templates and the image tag come from the same release.

`LITEBOX_DOMAIN` is required by the production Compose stack. It bootstraps the
public URL used in the setup link and keeps Caddy on the same hostname. The
first-run wizard persists the URL in SQLite; later changes should be made in
**Settings → System** (and in the reverse proxy/DNS configuration). Keep an
explicit `APP_BASE_URL` only when you intentionally need a non-standard URL.
See [Configuration](CONFIGURATION.md) for every variable's default, validation,
and environment-versus-SQLite lifecycle.

The first-run wizard stores the public URL, mailbox identity, and provider
credentials in SQLite. Each unconfigured production startup rotates and prints
a fresh setup URL/token; restarting is the supported way to replace a lost
unexpired link. Setup completion claims the token, mailbox, first owner, and
encrypted settings in one transaction. Credentials are encrypted with
`/data/.litebox/master.key`, which a new installation creates before SQLite and
which `litebox backup` includes automatically. An existing database never
generates a replacement for a missing or invalid key; startup fails before
migration. Do not place secrets in `compose.yaml`, shell history, image build
arguments, GitHub issues, or untrusted logs.

## Private local demo

Anyone with Docker can explore the UI without a domain or provider credentials:

```bash
docker run --rm --name litebox-demo \
  -p 127.0.0.1:8080:8080 \
  -e APP_ENV=development \
  -e APP_BASE_URL=http://localhost:8080 \
  -v litebox-demo-data:/data \
  ghcr.io/nader-jo/litebox:0.4.1
```

The credential-free demo binds only to `127.0.0.1:8080` and persists in the
automatically created `litebox-demo-data` volume. It cannot send or receive real
email as shown. `APP_ENV=development` is not itself a transport kill switch;
valid Resend credentials enable provider workflows, so use a dedicated test
account and domain.

Follow the [safe upgrade guide](UPGRADING.md) for backup, image verification,
and restorative rollback procedures.

## Existing reverse proxy

The default Compose mapping is loopback-only:

```text
127.0.0.1:8080 -> mailbox:8080
```

Proxy `https://mail.example.com` to that address. Preserve the original host and
configure `TRUSTED_PROXY_CIDRS` only for every controlled proxy network if
client IP-based throttling must honor `X-Forwarded-For`. Litebox accepts the
header only from a trusted direct peer, then walks the chain right-to-left to the
first untrusted client; a malformed chain or untrusted direct peer falls back to
`RemoteAddr`.

Start and inspect:

```bash
docker compose pull
docker compose up -d
docker compose ps
docker compose logs --tail=100 mailbox
curl --fail http://127.0.0.1:8080/health/ready
```

The production `compose.yaml` is image-only. It never builds application source on the VPS. Maintainers and contributors who need a local source build use the separate `compose.build.yaml` override.

## Supplied Caddy profile

Set `LITEBOX_DOMAIN=mail.example.com`, point DNS to the host, and run:

```bash
docker compose --profile proxy up -d
```

Caddy obtains and renews TLS certificates. The Caddy service is optional infrastructure; it is not a mailbox dependency.

## Container hardening

The supplied image and Compose definition:

- use a minimal `scratch` runtime with no shell or package manager;
- run as UID/GID 10001;
- drop all capabilities from Litebox;
- set `no-new-privileges`;
- use a read-only image filesystem;
- provide a 64 MiB `/tmp` tmpfs for multipart parsing; file parts cross a fixed
  4 MiB in-memory threshold, spill there, and are removed after each request;
- persist only the named `/data` volume;
- include OCI source, license, version, revision, and build-date labels;
- expose a binary healthcheck.

The runtime intentionally has no `/bin/sh`. Run administrative subcommands
directly, for example `docker compose exec mailbox /app/litebox doctor --deep`,
instead of opening a shell in the container.

If your platform overrides these controls, preserve write access to `/data` and `/tmp` while keeping `/data/objects` private.

## Verify image platform and provenance

Inspect the published manifest before deployment:

```bash
docker buildx imagetools inspect ghcr.io/nader-jo/litebox:0.4.1
```

The manifest must include both `linux/amd64` and `linux/arm64`. Release automation smoke-tests both platform images under the same read-only, non-root constraints used by Compose.

If GitHub CLI is available, verify the image’s GitHub/Sigstore provenance attestation:

```bash
gh attestation verify \
  oci://ghcr.io/nader-jo/litebox:0.4.1 \
  --repo Nader-jo/Litebox
```

For strict change control, resolve the version tag to its OCI digest after testing and pin `LITEBOX_IMAGE` as `ghcr.io/nader-jo/litebox@sha256:...`.

## Upgrade

1. Read the release notes and backup the current installation.
2. Stop the mailbox for a supported quiesced backup.
3. Set `LITEBOX_IMAGE` to the new immutable image tag and pull it; avoid `latest` for production pinning.
4. Start Litebox. Migrations are embedded, ordered, and transactional.
5. Run `litebox doctor` and inspect `/admin/system`.

Example:

```bash
docker compose stop mailbox
# perform and export backup
sed -i 's|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:0.4.1|' .env
docker compose pull mailbox
docker compose up -d mailbox
docker compose exec mailbox /app/litebox doctor
```

Database downgrades are not automatically supported. Restore the pre-upgrade backup if a rollback requires an older schema.

## Monitoring

Monitor:

- `/health/live` for process liveness;
- `/health/ready` for database and blob-store readiness; its SQLite ping and
  writable probes of both configured storage directories are cached for five
  seconds to bound probe I/O, and do not depend on Resend;
- container restart count;
- free bytes and inode pressure on the backing volume;
- dead jobs and failed attachments in `/admin/system`;
- webhook failures in Resend;
- independent backup success and restore drills.

Do not remove Litebox from service merely because Resend is temporarily unreachable. Existing local mail remains usable, and queued work retries.

## Disaster assumptions

The default deployment has one data failure domain. RAID, cloud volume durability, or S3-compatible storage is not a backup. Maintain an independent, versioned backup and perform a restore drill before the mailbox is important.
