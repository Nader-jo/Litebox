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

Releases contain the signed multi-platform image only. On a fresh VPS, install
the small deployment templates from the tagged repository and pull the image:

```bash
VERSION=0.3.1 # replace with the target release
git clone --depth 1 --branch "v${VERSION}" https://github.com/Nader-jo/Litebox.git /opt/litebox
cd /opt/litebox
cp .env.example .env
# Pin the desired release; Docker selects amd64 or arm64 automatically.
sed -i "s|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:${VERSION}|" .env
docker compose pull
docker compose up -d
```

The first-run wizard stores the public URL, mailbox identity, and provider
credentials in SQLite. In production, the container log prints a one-time setup
URL/token. Credentials are encrypted with `/data/.litebox/master.key`; include
that file in backups. Do not place secrets in `compose.yaml`, shell history,
image build arguments, GitHub issues, or logs.

## Private local demo

Anyone with Docker can explore the UI without a domain or provider credentials:

```bash
docker volume create litebox-demo-data
docker run --rm --name litebox-demo \
  -p 127.0.0.1:8080:8080 \
  -e APP_ENV=development \
  -e APP_BASE_URL=http://localhost:8080 \
  -v litebox-demo-data:/data \
  ghcr.io/nader-jo/litebox:0.3.1
```

The demo binds only to `127.0.0.1:8080`, persists in the
`litebox-demo-data` volume, and cannot send or receive real email.

Follow the [safe upgrade guide](UPGRADING.md) for backup, image verification,
and restorative rollback procedures.

## Existing reverse proxy

The default Compose mapping is loopback-only:

```text
127.0.0.1:8080 -> mailbox:8080
```

Proxy `https://mail.example.com` to that address. Preserve the original host and configure `TRUSTED_PROXY_CIDRS` only for the proxy network if client IP-based throttling must honor `X-Forwarded-For`. Litebox ignores forwarded IP headers from every other peer.

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

Set `LITEBOX_HOST=mail.example.com`, point DNS to the host, and run:

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
- provide a bounded `/tmp` tmpfs for multipart parsing;
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
docker buildx imagetools inspect ghcr.io/nader-jo/litebox:0.3.1
```

The manifest must include both `linux/amd64` and `linux/arm64`. Release automation smoke-tests both platform images under the same read-only, non-root constraints used by Compose.

If GitHub CLI is available, verify the image’s GitHub/Sigstore provenance attestation:

```bash
gh attestation verify \
  oci://ghcr.io/nader-jo/litebox:0.3.1 \
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
sed -i 's|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:0.3.1|' .env
docker compose pull mailbox
docker compose up -d mailbox
docker compose exec mailbox /app/litebox doctor
```

Database downgrades are not automatically supported. Restore the pre-upgrade backup if a rollback requires an older schema.

## Monitoring

Monitor:

- `/health/live` for process liveness;
- `/health/ready` for database and blob-store readiness;
- container restart count;
- free bytes and inode pressure on the backing volume;
- dead jobs and failed attachments in `/admin/system`;
- webhook failures in Resend;
- independent backup success and restore drills.

Do not remove Litebox from service merely because Resend is temporarily unreachable. Existing local mail remains usable, and queued work retries.

## Disaster assumptions

The default deployment has one data failure domain. RAID, cloud volume durability, or S3-compatible storage is not a backup. Maintain an independent, versioned backup and perform a restore drill before the mailbox is important.
