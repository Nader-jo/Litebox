# Litebox vision

Litebox exists to make a real custom-domain mailbox understandable and
self-hostable without turning its owner into a mail-server administrator.

## The promise

A person or small team should be able to run a durable, private browser mailbox
on one modest VPS, use an established provider for internet transport, and
understand where their data lives. The default system should remain one
application container, one persistent volume, and one external transport
provider.

## Who Litebox serves

- individuals who want a human inbox on their own domain;
- families, studios, clubs, and small teams with several independent addresses;
- operators who value a small failure surface and boring recovery procedures;
- contributors who prefer explicit boundaries over infrastructure for its own
  sake.

Litebox is not intended to become a multi-tenant email SaaS, a marketing email
platform, or a replacement SMTP/IMAP server.

## Product principles

1. **Local ownership over provider dependence.** Raw mail and attachments are
   retained in operator-controlled storage.
2. **One-container by default.** New infrastructure must earn its operational
   cost and remain optional wherever possible.
3. **Safe failure over silent loss.** Durable jobs, idempotency, diagnostics,
   checksums, backups, and restore drills matter more than optimistic UI.
4. **Hostile-input assumptions.** Email HTML, attachments, webhooks, cookies,
   and forwarded headers are untrusted at every boundary.
5. **Human-scale design.** The interface, documentation, and errors should be
   useful to people who do not write Go or operate databases.
6. **Progressive enhancement.** Core workflows remain server-rendered and usable
   without a JavaScript build system.
7. **Transparent scope.** Unsupported features are named honestly instead of
   hidden behind vague roadmap promises.
8. **Community stewardship.** Decisions, release artifacts, security policy,
   governance, and contributor credit stay public and portable.

## What success looks like

- a new operator can install Litebox safely in minutes and recover it from a
  documented backup;
- an end user can understand folders, aliases, delivery states, and permissions
  without reading operator documentation;
- releases are reproducible enough to inspect, version-pinned, multi-platform,
  attested, and tested before publication;
- the project remains approachable to a small maintainer community;
- performance improvements preserve SQLite and filesystem simplicity for the
  intended human-scale workloads.

## How the vision guides decisions

Feature proposals should explain the user problem, recovery behavior, security
boundary, data ownership, and effect on the one-container default. Long-lived
architectural choices are recorded under `docs/decisions/`. Near-term work is
tracked in [ROADMAP.md](ROADMAP.md), while governance and maintainer succession
are defined in [GOVERNANCE.md](GOVERNANCE.md).
