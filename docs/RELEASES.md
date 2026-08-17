# Release process

Litebox follows Semantic Versioning. Before 1.0, minor versions may contain documented breaking changes; patch versions remain backward compatible within the current minor line.

## Automated artifacts

Pushing a signed `v*` tag runs `.github/workflows/release.yml` after tests. The pipeline publishes:

- a GitHub release with generated notes and no application archives;
- one multi-platform GHCR image for Linux amd64 and arm64;
- OCI metadata, BuildKit provenance, an image SBOM, GitHub/Sigstore image provenance, and high/critical vulnerability scans for both platforms.

The workflow creates the GitHub release as a draft, then publishes it only after
the GHCR manifest contains both supported platforms and both published images
pass the vulnerability gate and hardened health smoke test. A container failure
therefore cannot advertise a completed GitHub release. GitHub's automatic source
zip/tar links are the only files shown on the release page; deployers consume
the image from GHCR.

GitHub Actions dependencies are pinned to immutable commit SHAs with human-readable version comments. Dependabot proposes updates.

## Maintainer checklist

1. Ensure `develop` is green and the working tree is clean.
2. Review dependency and vulnerability reports.
3. Run `make check`.
4. Build and smoke-test the container as a non-root user on both Linux amd64 and arm64.
5. Perform a backup/restore drill for schema or storage changes.
6. Update `CHANGELOG.md`, migration notes, docs, and supported-version policy.
7. Choose the SemVer version and create an annotated, preferably signed, tag from a commit contained in `develop`.
8. Push the tag and watch the release workflow.
9. Verify the GHCR platforms, OCI labels, image attestation, vulnerability scans, and release notes.
10. Deploy the exact image tag to a test installation and run `litebox doctor --deep`.
11. Announce material security or migration notes clearly.

Verify the container image and inspect its platforms:

```bash
gh attestation verify oci://ghcr.io/nader-jo/litebox:0.4.0 --repo Nader-jo/Litebox
docker buildx imagetools inspect ghcr.io/nader-jo/litebox:0.4.0
```

The `release` GitHub environment should require maintainer approval. The
workflow rejects non-SemVer, lightweight, or off-branch tags before publishing
artifacts.

Example:

```bash
git tag -s v0.4.0 -m "Litebox v0.4.0"
git push origin v0.4.0
```

## Release failure

Do not move or recreate a public version tag. If verification fails while the release is still a draft, fix the cause and publish a new version tag. If users could have consumed any image tag or artifact, document the partial release and publish a new patch version. Container tags derived from SemVer are treated as immutable.

## Database compatibility

Every release note must state whether a migration runs and whether rollback requires restoring a backup. The project does not automatically downgrade SQLite schemas.

Version 0.4.0 applies migration `005_settings_digests_colors.sql`. The current
unreleased v0.4.1 work also applies `006_account_tokens_digest_status.sql`,
which adds digest delivery status and hashed invitation/password-reset tokens.
Back up `/data`—including
`/data/.litebox/master.key`—before upgrading. Restoring the database without
the matching master key requires entering provider credentials again.
