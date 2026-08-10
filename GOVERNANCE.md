# Governance

Litebox uses a lightweight maintainer-led model suited to a focused
self-hosted application.

## Roles

- **Contributors** report issues, propose improvements, and submit changes.
- **Reviewers** are recurring contributors trusted to review specific areas.
- **Maintainers** merge changes, manage releases and security reports, enforce
  community standards, and steward the project's technical direction.

The repository owner is the initial maintainer. Additional reviewers and
maintainers are invited based on sustained, constructive contributions,
technical judgment, reliability, and care for users—not on employer or volume
of commits. Maintainer changes are recorded publicly in this file or release
notes.

## Decisions

Routine decisions are made through issues and pull request review. Maintainers
seek rough consensus, considering user impact, security, maintainability,
backward compatibility, and the project's scope. When consensus cannot be
reached, a maintainer makes and documents the decision.

Changes that create long-lived constraints—storage formats, provider
boundaries, security models, or major dependencies—require an architectural
decision record under `docs/decisions/`. Security-sensitive discussions may be
kept private until coordinated disclosure is safe.

## Releases

Maintainers approve and tag releases according to [docs/RELEASES.md](docs/RELEASES.md).
No individual may approve their own security-sensitive change without another
qualified review when another maintainer is available. Release artifacts are
produced by repository automation from protected tags.

## Project assets and continuity

Project names, domains, package registries, signing material, and release
credentials should use organization-controlled accounts with least privilege
where practical. If the lead maintainer becomes unavailable, active maintainers
may appoint a successor by public consensus. A fork is always permitted under
the Apache-2.0 license.
