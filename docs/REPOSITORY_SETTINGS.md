# Repository settings

This checklist records the GitHub settings that complement the versioned files
in this repository. Review it after ownership changes and at least twice a year.

## Repository and collaboration

- Keep the repository public with `develop` as the default branch.
- Set the description to "A lightweight, self-hosted human mailbox powered by
  Resend" and add the `email`, `self-hosted`, `resend`, `golang`, `sqlite`,
  `mailbox`, and `docker` topics.
- Enable Issues, Discussions, and private vulnerability reporting. Keep the wiki
  disabled so operational documentation remains reviewed and versioned.
- Allow squash merges, auto-merge, and branch updates. Delete head branches
  automatically. Disable merge commits to retain a readable, linear history.
- Keep the issue forms and pull request template under `.github/` as the public
  contribution entry points.

## Default-branch ruleset

Create an active ruleset targeting `develop` with these protections:

- block deletion and force pushes;
- require changes through pull requests and require all conversations to be
  resolved;
- require branches to be up to date or use a merge queue;
- require the exact checks emitted for pull requests: `Test, lint, and build`,
  `Container (linux/amd64)`, `Container (linux/arm64)`, `Go vulnerability scan`,
  `CodeQL`, `Dependency review`, and `GitHub Actions security`;
- require code-owner review and one independent approval once a second
  maintainer or regular reviewer is available;
- limit bypass permission to maintainers for documented emergencies.

Create a second ruleset for tags matching `v*`. Block updates and deletion,
limit creation to maintainers, and keep the release workflow's annotated-tag
and default-branch checks enabled.

## Actions and releases

- Set the default `GITHUB_TOKEN` permission to read-only and do not allow Actions
  to create or approve pull requests.
- Require actions to be pinned to full-length commit SHAs. If an allow-list is
  used, include only the organizations and actions referenced by the workflows
  in this repository.
- Require approval for workflows from first-time external contributors. Never
  expose repository secrets to fork pull requests.
- Create a `release` environment, add a required maintainer reviewer, disallow
  self-review when multiple maintainers are available, and prevent bypass of
  its protection rules.
- Keep Actions artifact and log retention long enough for incident review while
  avoiding indefinite storage; 30 days is a practical project default.

## Security and supply chain

Enable all security features available to public repositories:

- dependency graph, Dependabot alerts, Dependabot security updates, and grouped
  security updates where changes can be reviewed together safely;
- CodeQL code scanning using the versioned advanced-setup workflow;
- secret scanning, push protection, and validity checks;
- private vulnerability reporting and repository security advisories.

Do not enable a second CodeQL default-setup workflow alongside the checked-in
workflow. Review code-scanning alerts, dependency alerts, and OpenSSF Scorecard
results before every release.

## Routine audit

Quarterly, verify repository administrators, deploy keys, webhooks, installed
GitHub Apps, Actions secrets, environment reviewers, ruleset bypass actors, and
GHCR package permissions. Remove anything unused and record material governance
changes in `GOVERNANCE.md`.
