// Package receivers implements independently authored SMTP and Slack protocol
// transports. It does not import or copy Grafana's AGPL notifier implementation.
// The setting names and attachment wire fields target the contracts pinned in
// docs/grafana-receivers-design.md (Grafana 13.2.3 / alerting e1a167a201a8).
//
// Protocol references:
//   - SMTP acceptance and 4xx/5xx: https://www.rfc-editor.org/rfc/rfc5321
//   - STARTTLS: https://www.rfc-editor.org/rfc/rfc3207
//   - SMTP AUTH: https://www.rfc-editor.org/rfc/rfc4954
//   - MIME: https://www.rfc-editor.org/rfc/rfc2045
//   - Slack bot API: https://docs.slack.dev/reference/methods/chat.postMessage/
//   - Slack webhooks: https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/
//   - Rate limits: https://docs.slack.dev/apis/web-api/rate-limits/
//
// Callers render and bound templates once, prepare bytes once, seal prepared
// snapshots/configuration/secrets, then persist Checkpoint successes using the
// current lease CAS. Retry only unfinished retryable operations and preserve
// their original payloads and receipts. SingleEmail/BCC use one DATA transaction
// with per-envelope-recipient receipts; ordinary email uses one transaction per
// recipient. An SMTP DATA acknowledgement is acceptance by a mail server, not
// proof of mailbox delivery. Stable Message-ID/operation IDs aid correlation,
// but do not provide exactly-once guarantees across a remote ACK/local commit.
//
// This package has no legacy contact-point, policy, queue, template-renderer,
// image-renderer, SMTP-profile storage, API or UI hooks. SMTP supports rendered
// plain/HTML MIME and inline/attachment bytes. Slack supports message attachments,
// mentions, image URLs, incoming webhooks and chat.postMessage; bot file-upload
// workflows and the common Grafana v1 OAuth2 token configuration are not added
// here. A caller must explicitly reject those unsupported configurations.
// SMTP authentication requires TLS even with NoStartTLS; an unencrypted relay
// remains available without credentials. This transport currently rejects UTF-8
// envelopes pending SMTPUTF8 capability and MIME tests; Go 1.27's SMTP client
// already adds SMTPUTF8 to MAIL when the server advertises that extension.
package receivers
