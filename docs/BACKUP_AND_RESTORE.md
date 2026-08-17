# Backup and restore

Litebox's simple topology intentionally puts SQLite and private blobs under one `/data` volume. Losing that volume loses locally owned mail unless an independent backup exists.

## Dataset

```text
/data/mailbox.db
/data/objects/
/data/.litebox/master.key
```

The master key is part of the protected dataset and must remain paired with the
database. Preserve it with the backup or store it separately in a secret
manager. Provider credentials are encrypted in SQLite from v0.4.0 onward; a
restore without the matching key requires re-entering them in **Settings →
System**.

## Supported backup contract

Version 1 backups are quiesced. Stop the normal server before running the command so SQLite and blob references cannot change during the copy.

`litebox backup --output <new-directory>`:

1. runs SQLite integrity and foreign-key checks;
2. creates a compact SQLite snapshot with `VACUUM INTO`;
3. enumerates every SQLite-referenced raw message and attachment;
4. copies each blob through the `BlobStore` interface;
5. verifies known attachment SHA-256 values;
6. emits `manifest.json` with format version, timestamp, DB digest, and every blob key/size/digest;
7. removes an incomplete newly-created output directory if the operation fails.

The output directory must not already exist. This prevents accidental overwrite of an earlier backup.

## Docker procedure

Prefer an independent host mount for backup output rather than a directory on the same volume. For example, extend Compose locally with a read/write `/backup` mount, then:

```bash
docker compose stop mailbox
docker compose run --rm -v /srv/litebox-backups:/backup mailbox \
  backup --output /backup/backup-2026-08-10
docker compose start mailbox
```

Immediately copy or replicate the result away from the host. A second directory on the same disk is not a disaster backup.

## Restore

Restore is intentionally conservative: the configured database must not exist, and the storage root must be absent or empty.

1. Provision a fresh data location.
2. Restore the secret/configuration material separately.
3. Run `litebox restore --input <backup-directory>`.
4. Run `litebox doctor --deep`.
5. Start the server and open old messages and attachments.
6. Rebuild FTS with `litebox reindex` only if doctor or release notes request it.

Restore validates the database and every blob digest before succeeding. It never overwrites a live installation.

## Restore drill

At least quarterly for important installations:

- restore the newest backup to a fresh temporary environment;
- run deep doctor;
- verify thread counts and open representative old raw/attachment data;
- record recovery time and any missing operational knowledge;
- destroy the temporary copy securely after the drill.

## External tools

Restic, Borg, ZFS/Btrfs snapshots, and cloud-volume snapshots can protect `/data`, but consistency still matters. Until an online coordinated snapshot contract is implemented, stop Litebox briefly before capturing the dataset.
