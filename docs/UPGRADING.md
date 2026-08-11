# Safe upgrade guide

Litebox is pre-1.0: releases follow Semantic Versioning, but a minor release may
contain a documented breaking change. Patch releases are intended to remain
backward compatible within their minor line. Database migrations run
automatically on startup and are not automatically reversible.

This procedure favors recoverability over minimum downtime.

## 1. Read before changing anything

1. Read every release note between the installed and target versions.
2. Check for migration, configuration, provider, or minimum-Go/Docker notes.
3. Confirm enough free space for the live dataset, a backup, and the new image.
4. Record the current image and service state:

```bash
cd /opt/litebox
grep '^LITEBOX_IMAGE=' .env
docker compose ps
docker compose exec mailbox /app/litebox version
```

Copy `.env`, `compose.yaml`, and `Caddyfile` into your secret/configuration
backup. Do not publish them; `.env` contains provider credentials.

## 2. Create a quiesced, independent backup

Choose a new backup directory on storage independent from the Litebox data
volume:

```bash
cd /opt/litebox
BACKUP_ID="pre-upgrade-$(date -u +%Y%m%dT%H%M%SZ)"
docker compose stop mailbox
docker compose run --rm -v /srv/litebox-backups:/backup mailbox \
  backup --output "/backup/${BACKUP_ID}"
docker compose start mailbox
```

Copy the completed backup away from the VPS. A backup on the same disk is not a
recovery plan. For an important mailbox, restore the backup into a fresh data
location and run `doctor --deep` before continuing.

## 3. Upgrade with the guided installer

The installed `/opt/litebox/setup.sh` preserves `.env`, downloads the selected
release bundle, verifies its SHA-256 checksum, updates the immutable image tag,
validates Compose, and waits for health:

```bash
cd /opt/litebox
sudo sh setup.sh --version 0.3.0
```

Omit `--version` to select the latest stable release. Use `--no-start` to stage
and inspect configuration before starting. Use `--reconfigure` only when you
intend to replace installer-managed values.

If upgrading from a release that predates the installed script, download and
verify the current installer first as described in the
[deployment guide](DEPLOYMENT.md).

## 4. Verify the upgraded installation

```bash
cd /opt/litebox
docker compose ps
curl --fail http://127.0.0.1:8080/health/ready
docker compose exec mailbox /app/litebox version
docker compose exec mailbox /app/litebox doctor --deep
docker compose logs --tail=100 mailbox
```

Then sign in and verify:

- the expected mailbox and aliases are present;
- recent and old conversations open;
- a representative attachment downloads;
- search returns a known older message;
- a non-critical outbound message reaches its final delivery state;
- **System** reports healthy storage and no unexpected dead jobs.

Keep the pre-upgrade backup until the new version has operated normally for a
period appropriate to the mailbox's importance.

## Manual image upgrade

Operators who do not use the installer can pin and start the target image:

```bash
sed -i 's|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:0.3.0|' .env
docker compose pull mailbox
docker compose up -d mailbox
```

Never deploy `latest` in production. Verify the target release, image
attestation, and platform manifest before changing the pin.

## Rollback and failed migrations

Do not start an older Litebox binary against a database already migrated by a
newer release unless the release notes explicitly permit it. The supported
rollback is restorative:

1. stop the upgraded services;
2. provision a fresh, empty Litebox data volume;
3. restore the pre-upgrade backup with the previously used image/configuration;
4. run `doctor --deep`;
5. start the previous image and verify health.

Restore refuses to overwrite a live dataset. Keep the failed upgraded volume
offline until the incident is understood; it may be useful for diagnosis. See
[Backup and restore](BACKUP_AND_RESTORE.md) for the complete restore contract.

## Upgrade reporting

When reporting an upgrade problem, include source and target versions, host
architecture, redacted Compose state, the failing step, and relevant redacted
logs. Never attach `.env`, database files, messages, cookies, or provider
secrets to a public issue.
