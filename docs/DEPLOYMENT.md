# Production deployment

This guide assumes a small Linux VPS, Docker Engine, and a domain already controlled by the operator.

## Minimum resources

- 1 vCPU;
- 512 MiB RAM recommended;
- SSD-backed persistent storage;
- independent backups of the persistent dataset;
- public HTTPS ingress able to reach `/webhooks/resend`.

## Prepare configuration

Copy `.env.example` to `.env`, restrict it to the deployment account, and set production values. `APP_BASE_URL` must be HTTPS. `RESEND_API_KEY` and `RESEND_WEBHOOK_SECRET` are mandatory in production.

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

## Upgrade

1. Read the release notes and backup the current installation.
2. Stop the mailbox for a supported quiesced backup.
3. Pull the new immutable version tag; avoid `latest` for production pinning.
4. Start Litebox. Migrations are embedded, ordered, and transactional.
5. Run `litebox doctor` and inspect `/admin/system`.

Example:

```bash
docker compose stop mailbox
# perform and export backup
LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:0.2.0 docker compose pull mailbox
LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:0.2.0 docker compose up -d mailbox
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
