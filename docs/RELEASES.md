# Release process

Litebox follows Semantic Versioning. Before 1.0, minor versions may contain documented breaking changes; patch versions remain backward compatible within the current minor line.

## Automated artifacts

Pushing a signed `v*` tag runs `.github/workflows/release.yml` after tests. The pipeline publishes:

- Linux, macOS, and Windows archives for amd64 and arm64;
- SHA-256 checksums;
- archive SBOMs;
- signed GitHub/Sigstore provenance attestations for archives, checksums, and SBOMs;
- a generated GitHub release/changelog;
- a multi-platform GHCR image for Linux amd64/arm64;
- OCI metadata, BuildKit provenance, and image SBOM attestations.

GitHub Actions dependencies are pinned to immutable commit SHAs with human-readable version comments. Dependabot proposes updates.

## Maintainer checklist

1. Ensure `develop` is green and the working tree is clean.
2. Review dependency and vulnerability reports.
3. Run `make check`.
4. Build and smoke-test the container as a non-root user.
5. Perform a backup/restore drill for schema or storage changes.
6. Update `CHANGELOG.md`, migration notes, docs, and supported-version policy.
7. Choose the SemVer version and create an annotated, preferably signed, tag from a commit contained in `develop`.
8. Push the tag and watch the release workflow.
9. Verify checksums, archives, SBOMs, GHCR platforms, OCI labels, and release notes.
10. Deploy the exact version tag to a test installation and run `litebox doctor --deep`.
11. Announce material security or migration notes clearly.

Consumers can verify a downloaded archive with GitHub CLI:

```bash
gh attestation verify litebox_0.2.0_linux_amd64.tar.gz --repo Nader-jo/Litebox
```

The `release` GitHub environment should require maintainer approval. The
workflow rejects non-SemVer, lightweight, or off-branch tags before publishing
artifacts.

Example:

```bash
git tag -s v0.2.0 -m "Litebox v0.2.0"
git push origin v0.2.0
```

## Release failure

Do not move or recreate a public version tag. Fix the cause, document a partial release if users could have consumed it, and publish a new patch version. Container tags derived from SemVer are treated as immutable.

## Database compatibility

Every release note must state whether a migration runs and whether rollback requires restoring a backup. The project does not automatically downgrade SQLite schemas.
