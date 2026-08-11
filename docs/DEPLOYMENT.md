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

## Guided installation

On a fresh VPS, run the release-attached installer:

```bash
curl --proto '=https' --tlsv1.2 -fsSL \
  https://github.com/Nader-jo/Litebox/releases/latest/download/setup.sh | \
  sudo sh
```

The script:

1. verifies a supported 64-bit AMD/Intel or ARM host and Docker Compose v2;
2. resolves the latest stable release unless a version is pinned;
3. downloads the VPS bundle and verifies its attached SHA-256 checksum;
4. prompts on the controlling terminal, with echo disabled for secrets;
5. writes a mode-`0600` environment file, validates Compose, and starts the services;
6. waits for Litebox to become healthy and prints the setup and Resend URLs.

To inspect and verify the script before execution:

```bash
curl --fail --location --remote-name \
  https://github.com/Nader-jo/Litebox/releases/latest/download/setup.sh
curl --fail --location --remote-name \
  https://github.com/Nader-jo/Litebox/releases/latest/download/setup.sh.sha256
sha256sum --check setup.sh.sha256
less setup.sh
sudo sh setup.sh
```

Useful options are `--version 0.2.2`, `--install-dir /srv/litebox`,
`--no-start`, and `--reconfigure`. Run `sh setup.sh --help` for the complete
environment-variable interface used by unattended provisioning.

Re-running the installer is safe: it preserves the existing `.env` file and
advances only `LITEBOX_IMAGE` to the selected immutable release. Pass
`--reconfigure` when managed configuration should be prompted for again. Always
take a backup and read the release notes before an upgrade.

## Private local demo

Anyone with Docker can explore the UI without a domain or provider credentials:

```bash
curl --proto '=https' --tlsv1.2 -fsSL \
  https://github.com/Nader-jo/Litebox/releases/latest/download/setup.sh | \
  sh -s -- --demo
```

The demo binds only to `127.0.0.1:8080`, uses the hardened production image,
and persists in the `litebox-demo-data` volume. It cannot send or receive real
email. The installer prints exact stop, restart, and deletion commands.

## Manual installation

Download the VPS bundle attached to the release rather than cloning and compiling the repository on the server:

```bash
VERSION=0.2.2 # replace with the current release
install -d -m 0750 /opt/litebox
cd /opt/litebox
curl --fail --location --output litebox-vps.tar.gz \
  "https://github.com/Nader-jo/Litebox/releases/download/v${VERSION}/litebox_${VERSION}_vps.tar.gz"
curl --fail --location --output litebox-vps.tar.gz.sha256 \
  "https://github.com/Nader-jo/Litebox/releases/download/v${VERSION}/litebox_${VERSION}_vps.tar.gz.sha256"
sha256sum --check litebox-vps.tar.gz.sha256
tar -xzf litebox-vps.tar.gz
cp .env.example .env
chmod 0600 .env
```

The bundled `.env.example` pins `LITEBOX_IMAGE` to the same complete release version. Set production values in `.env`. `APP_BASE_URL` must be HTTPS. `RESEND_API_KEY` and `RESEND_WEBHOOK_SECRET` are mandatory in production.

Do not place secrets in `compose.yaml`, shell history, image build arguments, GitHub issues, or logs.

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

- run as UID/GID 10001;
- drop all capabilities from Litebox;
- set `no-new-privileges`;
- use a read-only image filesystem;
- provide a bounded `/tmp` tmpfs for multipart parsing;
- persist only the named `/data` volume;
- include OCI source, license, version, revision, and build-date labels;
- expose a binary healthcheck.

If your platform overrides these controls, preserve write access to `/data` and `/tmp` while keeping `/data/objects` private.

## Verify image platform and provenance

Inspect the published manifest before deployment:

```bash
docker buildx imagetools inspect ghcr.io/nader-jo/litebox:0.2.2
```

The manifest must include both `linux/amd64` and `linux/arm64`. Release automation smoke-tests both platform images under the same read-only, non-root constraints used by Compose.

If GitHub CLI is available, verify the image’s GitHub/Sigstore provenance attestation:

```bash
gh attestation verify \
  oci://ghcr.io/nader-jo/litebox:0.2.2 \
  --repo Nader-jo/Litebox
```

For strict change control, resolve the version tag to its OCI digest after testing and pin `LITEBOX_IMAGE` as `ghcr.io/nader-jo/litebox@sha256:...`.

## Upgrade

1. Read the release notes and backup the current installation.
2. Stop the mailbox for a supported quiesced backup.
3. Download the new release bundle and pull its immutable image tag; avoid `latest` for production pinning.
4. Start Litebox. Migrations are embedded, ordered, and transactional.
5. Run `litebox doctor` and inspect `/admin/system`.

Example:

```bash
docker compose stop mailbox
# perform and export backup
sed -i 's|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:0.2.2|' .env
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
