# Backup and restore

Litebox's simple topology intentionally puts SQLite and private blobs under one `/data` volume. Losing that volume loses locally owned mail unless an independent backup exists.

## Dataset

```text
/data/mailbox.db
/data/objects/
/data/.litebox/master.key
```

The master key is part of the protected dataset and must remain paired with the
database. `litebox backup` copies it into the backup and records its size and
SHA-256 digest in the manifest; backup fails unless the key is exactly 32 bytes.
You may keep another protected copy in a secret manager, but never remove it
from the supported backup. Provider credentials are encrypted in SQLite;
without the matching key Litebox cannot decrypt settings and will fail before
migration. Litebox never creates a replacement key for an existing database,
and there is no UI-based recovery path from that state.

## Supported backup contract

Version 1 backups are quiesced. Stop the normal server before running the command so SQLite and blob references cannot change during the copy.

`litebox backup --output <new-directory>`:

1. runs SQLite integrity and foreign-key checks;
2. creates a compact SQLite snapshot with `VACUUM INTO`;
3. copies the matching instance master key;
4. enumerates every SQLite-referenced raw message and attachment;
5. copies each blob through the `BlobStore` interface;
6. verifies known attachment SHA-256 values;
7. emits `manifest.json` with format version, timestamp, database and master-key metadata, and every blob key/size/digest, refusing output above the supported 16 MiB or 100,000-blob manifest ceiling;
8. removes an incomplete newly-created output directory if the operation fails.

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

Restore is intentionally conservative: the configured database must not exist,
the storage root must be absent or empty, and an existing key must either match
the backup exactly or be absent.

1. Provision a fresh data location and the deployment configuration needed to
   locate it.
2. Keep the normal server stopped.
3. Run `litebox restore --input <backup-directory>`; this restores the database,
   master key, and blobs together.
4. Run `litebox doctor --deep`.
5. Start the server and open old messages and attachments.
6. Rebuild FTS with `litebox reindex` only if doctor or release notes request it.

Before writing destination data, restore validates the manifest version and
structure, a maximum 16 MiB manifest and 100,000 blob entries, the exact
`mailbox.db` and `master.key` object names, a 32-byte master key, canonical
relative source paths, regular-file containment (including resolved symlinks),
unique logical blob keys, non-negative declared sizes, and SHA-256 syntax.
Absolute paths, drive prefixes, backslashes, traversal, symlink escapes,
malformed digests, and duplicate blob entries are rejected. Restore verifies
every database, key, and blob digest and requires the backup database to pass
SQLite integrity checks, decrypt its persisted settings with that key, and
reference exactly the blob set declared by the manifest before destination
writes begin. It rechecks installed SQLite/blob state and removes partial
destination artifacts after any failure. Restore never overwrites a live
installation.

The manifest digests detect corruption and internal mismatch; they do **not**
authenticate who created the archive. An attacker able to replace
`manifest.json`, the database, the key, and the blobs can generate a different
self-consistent backup. Obtain archives through a trusted, authenticated channel
and protect them against replacement (for example with authenticated encrypted
backup storage or a separately verified signature). Treat every archive as
untrusted input during restore even when it came from expected storage.

A database copied without its original master key is not a supported backup and
cannot be repaired by entering provider credentials in the UI, because
encrypted settings are loaded before the server starts.

## Restore drill

At least quarterly for important installations:

- restore the newest backup to a fresh temporary environment;
- run deep doctor;
- verify thread counts and open representative old raw/attachment data;
- record recovery time and any missing operational knowledge;
- destroy the temporary copy securely after the drill.

## External tools

Restic, Borg, ZFS/Btrfs snapshots, and cloud-volume snapshots can protect `/data`, but consistency still matters. Until an online coordinated snapshot contract is implemented, stop Litebox briefly before capturing the dataset.
