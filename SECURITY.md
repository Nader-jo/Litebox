# Security policy

Litebox stores private email and credentials, so security reports are treated
with urgency and discretion.

## Supported versions

Security fixes are released for the latest stable minor release. Users should
upgrade to the newest patch as soon as practical. The unreleased `develop` branch
receives fixes but is not a supported production release.

| Version | Supported |
| --- | --- |
| `0.4.x` | Yes |
| `0.3.x` and older | No |
| `develop` snapshots | No |

## Report a vulnerability

Do not open a public issue. Use GitHub's **Security → Report a vulnerability**
workflow to submit a private repository security advisory. Include:

- affected versions and deployment assumptions;
- reproduction steps or a minimal proof of concept;
- likely impact and any known mitigations; and
- a safe way to contact you for follow-up.

If private advisories are temporarily unavailable, use the private contact
method on the repository owner's GitHub profile and include only enough detail
to establish a secure follow-up channel.

You should receive an acknowledgement within 3 business days and an initial
assessment within 7 business days. Timelines for a fix and coordinated
disclosure depend on severity and complexity. Please allow a reasonable period
for supported users to update before publishing details.

## Safe-harbor intent

Good-faith research that avoids privacy violations, data destruction, service
disruption, social engineering, and access beyond what is necessary to
demonstrate the issue is welcomed. Test only systems and accounts you own or
have explicit permission to assess. This statement does not authorize testing
third-party services such as Resend.

## Security design and operations

The security boundaries and residual risks are documented in
[docs/THREAT_MODEL.md](docs/THREAT_MODEL.md). Deployment guidance in
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) is part of the supported security
posture. Never send production secrets, mailbox data, databases, or blob stores
as part of a report unless a secure transfer method has been agreed in advance.
