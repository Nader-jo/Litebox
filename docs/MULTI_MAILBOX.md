# Multi-mailbox access

Litebox is one trusted, self-hosted installation that can contain multiple independent mailboxes, aliases, users, and browser sessions. It is not a hostile multi-tenant SaaS boundary.

## Concepts

- A **user** is a login identity.
- A **session** is one browser/device login for a user. Sessions can be revoked independently.
- A **mailbox** is an isolated inbox with its own threads, messages, drafts, folders, search, unread state, aliases, and membership list.
- An **address** is the primary identity or an alias belonging to exactly one mailbox.
- Every address has a light, editable color. Inbound messages retain the matched address ID, so thread rows and conversation badges remain correct even when a mailbox has many aliases.
- A **membership** grants one user a role in one mailbox. A user can belong to many mailboxes and may have a different role in each.

## Roles

| Role | Read mail | Change state and send | Manage aliases/profile | Manage people | Grant/revoke owners |
| --- | --- | --- | --- | --- | --- |
| Owner | Yes | Yes | Yes | Yes | Yes |
| Admin | Yes | Yes | Yes | Yes | No |
| Member | Yes | Yes | No | No | No |
| Viewer | Yes | No | No | No | No |

A mailbox must always retain at least one owner. Password resets revoke all of that user's sessions; **Settings → Sessions** can revoke one browser without affecting the others.

Installation-wide job, webhook, and storage diagnostics are available only while operating the primary mailbox as an owner or administrator.

Owners and administrators can add a person with an initial password or send a
single-use invitation from **Settings → People**. Invitations expire after 72
hours; the recipient chooses a password before access is granted. Owners may
grant ownership, while administrators may grant only admin, member, or viewer
access.

## Address and delivery behavior

The primary address and every alias can receive mail and appear in the composer `From` selector. An address belongs to only one mailbox. Unknown inbound recipients are acknowledged and ignored before raw mail or attachments are downloaded.

If one provider email targets aliases of the same mailbox, Litebox stores one message. If it targets addresses belonging to two independent mailboxes, Litebox creates an isolated local message, raw archive, attachment set, and thread decision for each mailbox. Provider replay remains idempotent within each mailbox.

## Authorization boundary

The active mailbox is carried in URL query parameters for navigable links (for
example, `/inbox?mailbox=<id>`), with an HttpOnly preference cookie retained as
a convenient fallback. This lets separate tabs keep different mailboxes open.
The cookie is not trusted and does not grant access. On every authenticated request Litebox:

1. loads the user session from its hashed token;
2. resolves the requested mailbox through `mailbox_memberships`;
3. falls back to the user's first authorized mailbox if the preference is missing or stale;
4. passes the authorized mailbox ID into content repository methods;
5. applies the membership role before state changes or administrative actions.

Thread, message, draft, search, and attachment identifiers are insufficient on their own. Repository queries also require the active mailbox ID.

## Existing-installation migration

Migration `004_multi_mailbox.sql` is automatic and transactional. It:

- creates an address row for every existing mailbox;
- grants every existing enabled user ownership of the existing primary mailbox;
- introduces mailbox memberships;
- changes inbound provider uniqueness from global to per-mailbox while retaining global outbound provider uniqueness.

No environment change is required. Keep `MAILBOX_PRIMARY_ADDRESS` stable. Values in `MAILBOX_ALLOWED_RECIPIENTS` continue to be ensured as primary-mailbox aliases at startup; removing a value from the environment does not delete a database-managed alias.

Back up `/data` before upgrading and run `litebox doctor --deep` after the first startup on the new version.

After migration, alias colors and runtime settings are stored in SQLite. The environment aliases are used only for the one-time bootstrap import; manage future aliases in **Settings → Mailboxes**.
