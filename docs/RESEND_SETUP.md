# Resend and DNS setup

Provider UI and quotas change. Treat the current Resend dashboard and official documentation as the source of truth for assigned DNS values.

## Domain choice

Receiving for `hello@example.com` generally makes Resend responsible for inbound mail at the configured receiving domain. Any local part may reach the webhook. Litebox accepts only addresses registered in `mailbox_addresses` and discards unknown local parts before attachment download. `MAILBOX_ALLOWED_RECIPIENTS` seeds aliases only when the primary mailbox is first created; owners manage every later alias and independent mailbox in the UI.

> **MX conflict warning:** If another provider already handles mail for the same domain, changing its MX records can break that service. Consider a dedicated subdomain or plan the migration deliberately.

## DNS

Add the exact records Resend displays for:

- domain verification;
- inbound MX;
- DKIM;
- SPF/return path;
- any provider-specific sending records.

Add DMARC according to the domain's policy and rollout plan. Do not copy placeholder record values from this repository.

The web host and mail domain are independent. `LITEBOX_DOMAIN` names the public
application/proxy host; it does not have to be the domain used for email:

```text
mail.example.com        browser UI and webhook HTTPS host
example.com             email receiving/sending domain
hello@example.com       mailbox identity
```

## Webhook

Create one endpoint:

```text
https://mail.example.com/webhooks/resend
```

Subscribe to:

```text
email.received
email.sent
email.delivered
email.delivery_delayed
email.bounced
email.failed
email.suppressed
email.complained
```

Enter the API key, webhook signing secret, and optional domain ID in the first-run
wizard or **Settings → System**. Litebox encrypts the provider values in SQLite
with `/data/.litebox/master.key`. `RESEND_API_KEY`, `RESEND_WEBHOOK_SECRET`, and
`RESEND_DOMAIN_ID` remain supported as one-time legacy bootstrap values; after
the installation is configured, SQLite is the source of truth. Litebox rejects
missing, invalid, or stale webhook signatures and verifies the exact raw body
before JSON parsing.

## Provider behavior Litebox relies on

- webhook delivery is at least once and can be out of order;
- `email.received` contains metadata, not complete body bytes;
- the Receiving API returns normalized content and a temporary raw-email URL;
- Litebox requests `html_format=cid` explicitly because the provider default may
  encode inline images as `data:` URLs, which the sanitizer intentionally rejects;
- attachment download URLs expire and are refreshed on retry;
- outbound attachment size includes encoding overhead;
- idempotency keys have a finite provider lifetime.

Litebox therefore persists the verified event and job before returning 200 and
retrieves bytes asynchronously. Every production download and redirect requires
HTTPS; hostname resolution occurs inside a guarded dialer that refuses local,
private, carrier-grade NAT, link-local, benchmark, documentation, and reserved
addresses. Raw inbound archives use the upload-size cap. Inbound and outbound
attachments share the configured 25 MiB aggregate default per message, and the
attachment-count limit applies to both directions. Oversized inbound bytes
degrade archival metadata without hiding the readable message. Litebox also
maintains its own duplicate-send guard.

The primary mailbox is also used to deliver account invitations, password-reset
links, and optional metadata-only summaries. Password-reset requests return a
generic response without waiting for Resend and hand delivery to a bounded,
process-local queue. A provider outage, full queue, or shutdown can lose that
notification; request another link after delivery is restored. Verify outbound
sending before depending on those features.

Official references:

- <https://resend.com/docs/webhooks/emails/received>
- <https://resend.com/docs/api-reference/emails/retrieve-received-email>
- <https://resend.com/docs/api-reference/emails/retrieve-received-email-attachment>
- <https://resend.com/docs/webhooks/introduction>
- <https://resend.com/docs/webhooks/verify-webhooks-requests>
- <https://resend.com/docs/api-reference/emails/send-email>
- <https://resend.com/docs/send-with-go>

## Acceptance test

Before production use, send inbound messages from at least Gmail and Outlook
with plain text, HTML, Unicode, multiple attachments, an inline image, and a long
subject. Test Reply, Reply all, CC/BCC, attachment sending, an invitation and
password reset, the provider's bounce address, duplicate webhook replay, and a
temporary API outage. Confirm that a reset request returns promptly during the
outage and that requesting a fresh link succeeds after delivery recovers.
