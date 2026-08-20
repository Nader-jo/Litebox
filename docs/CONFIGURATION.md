# Configuration reference

Litebox deliberately separates process/deployment configuration from product
settings stored in SQLite. This distinction matters after first-run setup:
changing a seed in `.env` does not override the installation snapshot.

## Setting lifecycle

| Lifecycle | Meaning | How to change it |
| --- | --- | --- |
| Deployment | Read and validated on every process start. | Edit `.env` or the service environment, then restart. |
| New-volume bootstrap | Used only while creating the first primary mailbox. | Afterward use the authenticated mailbox settings. |
| SQLite seed | Supplies an unconfigured installation and is persisted by setup or the one-time legacy import. SQLite wins after configuration. | Use **Settings → System** when the field is exposed. |
| Compose only | Expanded by Docker Compose; the Go process does not read it. | Edit `.env`, then recreate the service. |

Restoring a backup also restores its SQLite settings and master key. Environment
seeds do not replace settings from the restored database.

## Application and deployment

| Variable | Default | Lifecycle | Notes |
| --- | --- | --- | --- |
| `APP_ENV` | `development` in the binary; `production` in Compose | Deployment | Exactly `development` or `production`. Production requires `LITEBOX_DOMAIN` and HTTPS. Development relaxes those checks; it does not disable provider traffic. |
| `LITEBOX_DOMAIN` | none | Deployment | Required in production. A hostname only, without scheme, port, or path. It seeds `https://<host>` and configures the optional Caddy profile. Keep it aligned with DNS, ingress, and the saved public URL. |
| `APP_BASE_URL` | `https://LITEBOX_DOMAIN`, otherwise `http://localhost:8080` | SQLite seed | Must be an HTTP(S) origin with no credentials, path, query, or fragment; production requires HTTPS. After setup, change it under **Settings → System**. |
| `APP_LISTEN_ADDR` | `:8080` | Deployment | Listener address with a valid non-zero TCP port. Compose fixes the container listener at `:8080`. |
| `APP_DATA_DIR` | `.data` in the binary; `/data` in Compose | Deployment | Base for default database, key, and object paths. |
| `APP_DB_PATH` | `<data>/mailbox.db` | Deployment | SQLite database path. Litebox converts it to an escaped absolute file URI, so literal `?`, `#`, spaces, and other path characters are not interpreted as SQLite URI controls. |
| `APP_COOKIE_NAME` | `litebox_session` | Deployment | Must be a valid cookie name. Changing it signs browsers out by making existing cookies undiscoverable. |
| `APP_SESSION_TTL_HOURS` | `168` | SQLite seed | From 1 through 8760 hours (one year). Editable under **Settings → System**; a change applies to newly created sessions and does not rewrite existing expiry timestamps. |
| `APP_LOG_LEVEL` | `info` | SQLite seed | Exactly `debug`, `info`, `warn`, or `error`. A saved UI change updates the running logger immediately. |
| `LITEBOX_MASTER_KEY_FILE` | `<data>/.litebox/master.key` | Deployment | Path to the regular 32-byte key that protects encrypted provider credentials. A new installation creates the key before its database; an existing database requires its existing key and fails before migration rather than generating a replacement. Read-only Docker-secret mounts are supported. `litebox backup` includes the key and `restore` reinstalls it. |

## Mailbox and provider bootstrap

| Variable | Default | Lifecycle | Notes |
| --- | --- | --- | --- |
| `MAILBOX_PRIMARY_ADDRESS` | `hello@example.com` | New-volume bootstrap | Must be one canonical email address. The wizard can replace it while the installation is unconfigured. Once created, manage the mailbox in the UI. |
| `MAILBOX_DISPLAY_NAME` | `Litebox` | New-volume bootstrap | `.env.example` uses `Example Company`. Later changes belong under **Settings → Mailboxes**. |
| `MAILBOX_ALLOWED_RECIPIENTS` | primary address | New-volume bootstrap | Comma-separated canonical addresses and must include the primary address. They seed aliases only when the primary mailbox is first created; removing or adding environment values later does nothing. |
| `RESEND_API_KEY` | empty | SQLite seed | Legacy non-interactive import. Prefer the setup wizard or **Settings → System**. |
| `RESEND_WEBHOOK_SECRET` | empty | SQLite seed | Imported with the API key, encrypted, and then sourced from SQLite. |
| `RESEND_DOMAIN_ID` | empty | SQLite seed | Optional provider domain identifier, encrypted in SQLite when present. |

Production may start without provider credentials only while exposing the
protected first-run flow. Completing production setup requires both the API key
and webhook secret; subsequent starts validate the decrypted SQLite values.

An unconfigured production start prints a fresh 30-day `/setup?token=...` URL.
Every restart rotates the token and invalidates the previous URL; completing
setup atomically claims the installation and token while creating the mailbox,
first owner, and encrypted settings. A concurrent or repeated submission cannot
partially complete setup; any failed transaction rolls the whole transition
back. If the current URL is lost, restart the mailbox service and read only that
startup's trusted container logs. Never publish the URL or token.

`create-admin` creates a login but does not persist the public URL, mailbox
identity, or Resend credentials. It is not a complete headless setup command.
For a legacy non-interactive bootstrap, supply both required Resend secrets and
the other seeds before the first start, then create the administrator.

## Storage and limits

| Variable | Default | Lifecycle | Notes |
| --- | --- | --- | --- |
| `STORAGE_BACKEND` | `filesystem` | Deployment | Only `filesystem` is supported. |
| `STORAGE_ROOT` | `<data>/objects` | Deployment | Private durable object root; never expose it as static content. |
| `STORAGE_TMP_ROOT` | `<data>/tmp` | Deployment | Required private temporary-work directory. Immutable filesystem objects are staged beside their final destination—not here—so publication stays on one filesystem and remains atomic. |
| `MAX_WEBHOOK_BODY_BYTES` | `1048576` (1 MiB) | SQLite seed | From 1 byte through 16 MiB. |
| `MAX_MESSAGE_TEXT_BYTES` | `5242880` (5 MiB) | SQLite seed | From 1 byte through 16 MiB for normalized message text. |
| `MAX_UPLOAD_REQUEST_BYTES` | `31457280` (30 MiB) | SQLite seed | From 1 byte through 128 MiB. Caps a complete browser multipart request and each inbound raw `.eml` archive. |
| `MAX_OUTBOUND_ATTACHMENT_BYTES` | `26214400` (25 MiB) | SQLite seed | From 1 byte through 64 MiB. Aggregate raw attachment budget for one outbound message or one inbound message, before outbound provider encoding overhead. It also participates in the concurrent-send budget below. |
| `MAX_ATTACHMENT_COUNT` | `20` | SQLite seed | From 1 through 100 attachments per message. |

The limit snapshot remains SQLite-backed after setup so an environment change
cannot silently alter an established installation. These limits are not
currently exposed in the settings UI: choose them before setup. Direct SQLite
edits are unsupported.

Inbound size failures are intentionally best effort. A raw message over
`MAX_UPLOAD_REQUEST_BYTES` remains readable as `ready_without_raw`. Attachment
metadata is limited to the first `MAX_ATTACHMENT_COUNT` entries; an attachment
that exceeds the remaining aggregate byte budget is retained as unavailable
metadata with a storage error. These deterministic limit outcomes do not keep a
job retrying indefinitely. Outbound browser requests are rejected before
parsing when the request cap is exceeded, and outbound sending rechecks the
aggregate attachment budget, stored size, and SHA-256 digest before provider
submission.

Multipart parsing uses a fixed 4 MiB file-part memory threshold rather than the
request limit. Larger parts spill to the process temporary directory and are
removed after the request. The supplied container bounds `/tmp` at 64 MiB, so
its available space can impose a lower practical upload ceiling than a custom
`MAX_UPLOAD_REQUEST_BYTES` above that size.

On a filesystem without hard-link support, each immutable blob or initial
master-key publication briefly creates an adjacent `.litebox-lock` sidecar and
falls back to an atomic rename. A crash can leave that lock behind. If Litebox
reports a publish-lock timeout, first verify that no other Litebox process or
restore is writing the reported destination, then remove only the exact lock
path named in the error and retry. Never bulk-delete lock files while a writer
may be active.

## Workers and proxy trust

| Variable | Default | Lifecycle | Notes |
| --- | --- | --- | --- |
| `WORKER_COUNT` | `2` | Deployment | From 1 through 32 workers, subject to both combined budgets below. |
| `JOB_POLL_INTERVAL_MS` | `500` | Deployment | Queue poll interval from 50 milliseconds through one hour. Together with worker count, it must not exceed 64 claim attempts per second. |
| `JOB_LEASE_SECONDS` | `120` | Deployment | Positive lease duration up to 24 hours. Workers renew active leases; completion and failure updates must still own the lease. |
| `TRUSTED_PROXY_CIDRS` | empty | Deployment | Comma-separated proxy networks allowed to supply `X-Forwarded-For`. Litebox trusts the header only when the direct peer is listed, then walks the chain right-to-left across listed proxies to find the first untrusted client. Malformed chains fall back to the direct peer. |

Startup rejects `WORKER_COUNT × MAX_OUTBOUND_ATTACHMENT_BYTES` above 256 MiB,
because each send worker may hold one message's raw attachments concurrently.
It also rejects configurations where `WORKER_COUNT ÷ JOB_POLL_INTERVAL` exceeds
64 job-claim attempts per second. These coupled checks can make the effective
worker maximum lower than 32; for example, a 64 MiB attachment budget permits
at most four workers.

## Compose conveniences

| Variable | Default in `.env.example` | Lifecycle | Notes |
| --- | --- | --- | --- |
| `LITEBOX_IMAGE` | `ghcr.io/nader-jo/litebox:0.4.1` | Compose only | Pin a complete release tag or a verified digest; never use `latest` in production. |
| `LITEBOX_PORT` | `8080` | Compose only | Host loopback port mapped to container port 8080. |

## Validation and secret handling

Invalid enum values, addresses, URLs, hostnames, CIDRs, non-positive or
out-of-range numeric settings, overflowing duration multiplications,
unsupported storage backends, or incomplete production settings fail startup
instead of silently selecting another behavior. `APP_LOG_LEVEL` controls
verbosity only; provider credentials, cookies, tokens, message bodies, and
attachments must never be logged at any level.

The first-run and settings forms encrypt provider values with the instance
master key before storing them in SQLite. Restrict access to `.env`, `/data`,
backup directories, reverse-proxy configuration, and container logs. See
[Backup and restore](BACKUP_AND_RESTORE.md) before changing paths or moving an
installation.
