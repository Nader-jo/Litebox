# Litebox user guide

This guide is for people who read and send mail in Litebox. Server setup,
backups, DNS, and upgrades are handled by an operator and are documented
separately.

## Sign in and choose a mailbox

Open the HTTPS address provided by your administrator and sign in with your
Litebox email and password. If your account can access several mailboxes, use
the mailbox selector in the top bar to switch between them. Each mailbox keeps
its own messages, drafts, unread state, aliases, and membership list.

Mailbox links include the selected mailbox in the URL, so you can keep two
mailboxes open in separate browser tabs without one tab changing the other.

Use **Settings → Sessions** to review browsers and devices signed in to your
account. Revoke anything you do not recognize. Revoking the current session
signs that browser out immediately.

If you forget your password, select **Forgot your password?** on the sign-in
page. Reset links are single-use and expire after 30 minutes; changing the
password signs out every existing session. Mailbox invitations are also
single-use links and expire after 72 hours. An invitation recipient chooses a
password before access is granted.

## Understand the folders

| Folder or state | Meaning |
| --- | --- |
| Inbox | Active received conversations that are neither archived nor trashed. |
| Starred | Conversations marked for quick return. Starring does not move mail out of its current folder. |
| Sent | Messages submitted from Litebox, together with their delivery state. |
| Drafts | Messages saved locally but not yet queued for delivery. |
| Archive | Conversations removed from Inbox without being deleted. **Move to Inbox** restores them. |
| Trash | Conversations hidden from normal folders. **Restore** recovers them; **Delete forever** is permanent. |
| Unread | Bold conversations that have not been opened. **Unread** can mark an open conversation for later. |

Opening a conversation marks it read unless your mailbox role is Viewer. A
Viewer has read-only access and cannot change folder state, send mail, or edit
mailbox settings.

## Read and organize mail

Select a conversation from the list. All messages in the thread appear in
chronological order. Use the toolbar to archive, star, mark unread, or move the
conversation to Trash. On a phone, use the back arrow to return to the list.

Litebox blocks remote images in received HTML email to prevent sender tracking.
Inline images already archived by Litebox remain available. Attachments are
served through authenticated download routes; HTML and SVG attachments download
instead of executing in the Litebox page.

## Compose, reply, and attach files

1. Select **Compose**, or press <kbd>C</kbd> outside a form field.
2. Choose a **From** identity. Owners and administrators manage available
   aliases under **Settings → Mailboxes**.
3. Enter To, optional Cc/Bcc, a subject, and a plain-text message.
4. Select **Save to add files** when starting a new message, then attach files.
5. Select **Send message**. Litebox shows progress immediately and confirms when
   the message is queued.

Queued means Litebox accepted the message for its durable background worker; it
does not yet mean the recipient received it. Sent messages may show:

- **Queued** — waiting for a worker;
- **Submitted** or **Sent** — accepted by Resend;
- **Delivery delayed** — delivery is still being retried;
- **Delivered** — the provider reported successful delivery;
- **Bounced**, **Failed**, **Suppressed**, or **Complained** — terminal provider
  outcomes that may require a corrected address or operator investigation.

Reply targets the sender. Reply all also includes the original To and Cc
recipients while excluding the active mailbox's own addresses.

## Search mail

Select the search field or press <kbd>/</kbd>. Plain terms search subjects,
senders, recipients, and indexed text. Combine terms and operators:

```text
invoice
"renewal notice"
from:alice@example.com
to:support@example.com
subject:invoice
has:attachment
is:unread
is:starred
after:2026-01-01
before:2026-08-01
```

Searches never change or move messages. Saved searches, automatic filters,
labels, and mail rules are not available yet. If you need that workflow, share
the concrete use case in a feature request rather than expecting a hidden
filter configuration.

## Keyboard shortcuts

Press <kbd>?</kbd> anywhere outside a form field for the in-app reference.

| Shortcut | Action |
| --- | --- |
| <kbd>C</kbd> | Compose a message |
| <kbd>/</kbd> | Focus search |
| <kbd>J</kbd> / <kbd>K</kbd> | Move through the conversation list |
| <kbd>R</kbd> | Reply to the open conversation |
| <kbd>G</kbd>, then <kbd>I</kbd> | Go to Inbox |
| <kbd>G</kbd>, then <kbd>S</kbd>, <kbd>D</kbd>, <kbd>A</kbd>, or <kbd>T</kbd> | Go to Sent, Drafts, Archive, or Trash |
| <kbd>Esc</kbd> | Close navigation, search, or shortcut help |

Shortcuts are disabled while you type in an input, select, editor, or message
body.

## Mailboxes, aliases, and roles

- A **mailbox** is an independent inbox and message history.
- An **alias** receives into one mailbox and can be used as a From identity.
- Each alias has a light color. The same color appears beside messages and
  thread-list rows received through that address, which makes shared inboxes
  easy to scan. Owners and
  administrators can choose a different swatch under **Settings → Mailboxes**.
- **Owner** and **Admin** can manage mailbox identity, aliases, and people.
- **Member** can read, organize, and send mail.
- **Viewer** can only read.

Access changes apply on the next request. Ask a mailbox owner or administrator
when an address, role, or mailbox selection is missing.

## Schedule a private summary

Open **Settings → Summary** to send a count-only report to a private address.
Choose daily or Monday-only weekly delivery, a time zone, the local send hour,
and all accessible mailboxes or a selected subset. Reports contain received,
unread, and sent totals only—never subjects, senders, or message bodies. The
summary is sent by the primary mailbox and can be disabled at any time. Use
**Send test now** to verify delivery immediately; the page also shows the
preview window, last counts, last successful delivery, and the most recent
error.

## When something goes wrong

- Refresh once after a stale-form or expired-session message, then sign in again
  if requested.
- Check the visible delivery state before sending a duplicate message.
- Ask an operator to inspect **System** when attachments fail, jobs die, or
  provider delivery repeatedly fails.
- Never send passwords, session cookies, API keys, webhook secrets, private
  email bodies, or attachments in a public issue.

For help, see [Support](../SUPPORT.md). Operators should use the
[deployment guide](DEPLOYMENT.md), [upgrade guide](UPGRADING.md), and
[backup guide](BACKUP_AND_RESTORE.md).
