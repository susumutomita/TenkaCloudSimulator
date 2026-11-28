# SES v2 local domain identities

The four SES v2 identity operations share the simulator's SQLite database with Route 53. Verification checks records in that database. Keep the same simulator process, team token and SES region throughout the workflow.

1. Use Route 53 `CreateHostedZone` to create one public hosted zone for a lowercase ASCII domain ending in `.test`, such as `mail.example.test`.
2. Use SES v2 `CreateEmailIdentity` with `EmailIdentity` set to that exact domain. Save the three distinct strings in `DkimAttributes.Tokens`. The initial status is `PENDING`.
3. In the returned Route 53 zone ID, use `ChangeResourceRecordSets` to create three CNAME records. For each token, the record name is `<token>._domainkey.mail.example.test.` and its single value is `<token>.dkim.amazonses.com.`. Supply a supported TTL, for example 60 seconds.
4. Use `GetEmailIdentity` or `ListEmailIdentities`. All three matching records yield `VerificationStatus=SUCCESS`. Removing one or changing its target returns the identity to `PENDING` on the next read. A duplicate exact zone name suspends verification. A replacement zone gets a new ID and cannot verify the old identity.
5. Use `DeleteEmailIdentity` to remove the identity. Delete the CNAMEs separately, supplying their full saved values and TTL, before deleting the hosted zone. Recreating the identity generates fresh verification tokens.

Create, Get and Delete require their respective `ses:` action on `arn:aws:ses:<region>:000000000000:identity/<domain>`. List requires `ses:ListEmailIdentities` on `*`. The native Route 53 actions retain their own permissions. Lists accept page sizes from 1 to 100 and return an opaque continuation token when another page exists. The local limit is 100 live identities per team and region.

This workflow checks local DNS control. The runtime performs no public DNS queries, external delivery or cryptographic DKIM signing. Individual address verification, identity tagging, BYODKIM and configuration options are outside this implementation. Existing stored email templates and their MIME preview use their original regional APIs.

[AWS SES v2 identity API](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_CreateEmailIdentity.html) · [AWS DNS verification](https://docs.aws.amazon.com/ses/latest/dg/creating-identities.html) · [Regional DKIM domains](https://docs.aws.amazon.com/general/latest/gr/ses.html) · [IAM permissions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonsimpleemailservicev2.html)

## Send and inspect a local message

Use SES v2 `SendEmail` with one bare sender under the currently verified domain, for example `sender@mail.example.test`. The local part is at most 64 ASCII characters. Supply one recipient in `Destination.ToAddresses`. Use either `Content.Simple` with a UTF-8 subject and text or HTML, or `Content.Template` with a saved regional `TemplateName` and flat string values in `TemplateData`. Template substitution uses the same rules as `TestRenderEmailTemplate`. Simple message text preserves literal braces.

The fixed mailbox `inbox@inbox.ses.test` stores the actual envelope and complete MIME. Other bare addresses at `inbox.ses.test` produce a saved recipient refusal with reason `mailbox-not-found`. Both outcomes belong to accepted submissions and return a new `MessageId`. Unverified senders, invalid inputs, outside destinations, denied authorization and full storage produce no receipt. `SendEmail` accepts no idempotency token. Repeating a successful request creates another submission. Use the operator message list to inspect an uncertain outcome before deciding to submit again.

The simulator evaluates `ses:SendEmail` on the verified domain identity ARN and on the named regional template ARN when supplied. It rechecks DNS and reads the template in the same transaction that stores the outcome. MIME is limited to 256 KiB including headers. Each region retains up to 100 submissions until an operator deletes them.

| Authenticated operator endpoint | Result |
|---|---|
| `GET /_simulator/v1/ses/sink/{region}/messages` | Accepted envelopes, latest first, with message ID, sender, recipient, time, outcome and content byte count |
| `GET /_simulator/v1/ses/sink/{region}/messages/{id}` | One saved receipt with the local mailbox outcome |
| `GET /_simulator/v1/ses/sink/{region}/messages/{id}/content` | Download the stored MIME as an attachment; a rejected recipient has no content |
| `DELETE /_simulator/v1/ses/sink/{region}/messages/{id}` | Atomically remove the addressed receipt and MIME bytes |

The endpoint requires this team's operator credential and operator identity. Participant sessions cannot inspect or delete envelopes. The in-process sink opens no SMTP connection. It supplies no AWS delivery, bounce, tracking or Virtual Deliverability Manager events.

[AWS SendEmail](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_SendEmail.html) · [AWS message insights events](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_InsightsEvent.html)
