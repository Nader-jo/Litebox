# Resend and DNS setup

Provider UI and quotas change. Treat the current Resend dashboard and official documentation as the source of truth for assigned DNS values.

## Domain choice

Receiving for `hello@example.com` generally makes Resend responsible for inbound mail at the configured receiving domain. Any local part may reach the webhook. Litebox accepts only addresses registered in `mailbox_addresses` and discards unknown local parts before attachment download. `MAILBOX_ALLOWED_RECIPIENTS` idempotently seeds aliases for the primary mailbox; owners can manage additional aliases and independent mailboxes in the UI.

> **MX conflict warning:** If another provider already handles mail for the same domain, changing its MX records can break that service. Consider a dedicated subdomain or plan the migration deliberately.

## DNS

Add the exact records Resend displays for:

- domain verification;
- inbound MX;
- DKIM;
- SPF/return path;
- any provider-specific sending records.

Add DMARC according to the domain's policy and rollout plan. Do not copy placeholder record values from this repository.

The web host and mail domain are independent:

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

Store the signing secret in `RESEND_WEBHOOK_SECRET`. Litebox rejects missing, invalid, or stale signatures. It verifies the exact raw body before JSON parsing.

## Provider behavior Litebox relies on

- webhook delivery is at least once and can be out of order;
- `email.received` contains metadata, not complete body bytes;
- the Receiving API returns normalized content and a temporary raw-email URL;
- attachment download URLs expire and are refreshed on retry;
- outbound attachment size includes encoding overhead;
- idempotency keys have a finite provider lifetime.

Litebox therefore persists the verified event and job before returning 200, retrieves bytes asynchronously, copies all durable content locally, caps raw outbound attachments at 25 MiB by default, and maintains its own duplicate-send guard.

Official references:

- <https://resend.com/docs/webhooks/emails/received>
- <https://resend.com/docs/api-reference/emails/retrieve-received-email>
- <https://resend.com/docs/api-reference/emails/retrieve-received-email-attachment>
- <https://resend.com/docs/webhooks/introduction>
- <https://resend.com/docs/webhooks/verify-webhooks-requests>
- <https://resend.com/docs/api-reference/emails/send-email>
- <https://resend.com/docs/send-with-go>

## Acceptance test

Before production use, send inbound messages from at least Gmail and Outlook with plain text, HTML, Unicode, multiple attachments, an inline image, and a long subject. Test Reply, Reply all, CC/BCC, attachment sending, the provider's bounce address, duplicate webhook replay, and a temporary API outage.
