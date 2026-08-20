# ADR 0003: Authorize mailboxes through user memberships

- Status: Accepted
- Date: 2026-08-10

## Context

Litebox originally authenticated one administrator and stored one mailbox ID in the application server. Adding aliases alone would not provide independent inbox state, and attaching mailbox permissions to browser sessions would make access revocation inconsistent across devices.

## Decision

Sessions authenticate users only. A `mailbox_memberships` join table authorizes users for mailboxes with `owner`, `admin`, `member`, or `viewer` roles. `mailbox_addresses` maps each globally unique primary address or alias to one mailbox. Navigable links carry the active mailbox in a `mailbox` query parameter, while an HttpOnly cookie remains a non-authoritative fallback preference; both are resolved against memberships on every request.

Interactive repositories require an authorized mailbox ID for content reads and mutations. Inbound recipient routing resolves database addresses independently of browser authorization. One provider email delivered to multiple independent mailboxes is stored once per mailbox with per-mailbox idempotency.

## Consequences

- Membership changes take effect across all of a user's sessions on their next request.
- Object identifiers cannot cross mailbox boundaries without a matching membership and mailbox-scoped query.
- Existing installations migrate without losing their primary mailbox or administrator access.
- Navigable mailbox URLs let independent browser tabs preserve different selections; the fallback cookie still makes ordinary navigation convenient.
- The design provides isolation inside one trusted installation, not a hostile multi-tenant hosting guarantee.

## Alternatives considered

- **Mailbox IDs stored in sessions:** rejected because permissions would become stale and require updating every active session.
- **Aliases as independent mailboxes:** rejected because aliases commonly share one inbox and sender identity set.
- **One message row associated with many mailboxes:** rejected because unread, threading, deletion, and retention semantics are mailbox-specific; isolated local copies keep those boundaries explicit.
