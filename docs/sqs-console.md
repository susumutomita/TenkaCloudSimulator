# SQS console: bounded Standard queues

> Current integration source: normal merge of public SQS HEAD725960e with
> main5c97dc5. The main22 mandatory cases remain unchanged and the two owned
> SQS console/reference cases remain mandatory (24 total). The registry union
> contains17 baselines/86 controls. Canonical UI rebuild, current-head native
> and browser execution, both90% coverage floors, E2E/formal/applicability and
> hosted CI are pending. Prior results below belong to the named older heads.


The embedded console uses official Cloudscape components and the same nine
native SQS HTTP operations and SQLite state as SDK clients. No separate browser
queue/message store, native API change, actual AWS connection or host runtime is
needed. The simulator remains independent and implements a limited local subset.

## Screens and operations

| Flow | Native operation | Supported behavior |
| --- | --- | --- |
| Queues and name prefix, next/previous page | ListQueues, GetQueueAttributes |25 queues per page; denied metadata is unavailable, never fabricated zeroes |
| Open queue by exact name | GetQueueUrl, GetQueueAttributes | Supports identities that cannot list queues; no admin fallback |
| Create queue → Details | CreateQueue, GetQueueUrl, GetQueueAttributes | Standard only; visibility0–43200seconds, delay0–900seconds, retention60–1209600seconds |
| Send and receive messages → Send message | SendMessage | Text body1–262144 UTF-8 bytes, delay0–900seconds; actual MessageId/MD5 after acknowledgment |
| Poll for messages | ReceiveMessage | One explicit call, maximum1–10, wait0–20seconds and visibility0–43200seconds; no automatic polling/deletion |
| Change message visibility | ChangeMessageVisibility | Uses the selected message's current receipt;0 releases it and clears local custody so another poll obtains a new receipt |
| Delete message | DeleteMessage | Single selected message with its current native receipt; no batch API |
| Delete queue | DeleteQueue | Confirm literal `delete`; removes all its messages, including in-flight messages |

Queue details show actual ARN, URL, creation time, supported configuration and
last-read available/in-flight counts. Refresh obtains current native metadata.
The UI does not edit queue settings, message attributes, resource policies, tags,
DLQ/redrive, encryption/KMS, FIFO, purge, batch operations or triggers. Maximum
message size stays at the local256KiB limit; no encryption is claimed. Native
CFN/Lambda/Challenge support is not expanded by this view. See [native limits and
AWS differences](sqs.md).

Every call is a relative-root POST with JSON1.0, the exact AmazonSQS target,
selected region and SQS service hint. A returned QueueUrl is only an identifier
in the request body, never a URL the browser fetches. The parent request helper
handles401; native IAM applies on every action. Hosts still allocate separate
processes, SQLite files and token authorities per team. Choosing an exercise
identity or numeric loopback alone does not authenticate a tenant.

## Receipt privacy and asynchronous operations

Raw receipts stay in a private component ref. Only MessageId, body and selected
metadata enter rendered rows. Receipts are absent from this console's DOM,
URLs, logs and browser storage. Native SDK/API-reference responses legitimately
return the current receipt to their authorized caller; MessageId cannot replace
it. Leaving or changing queue/region/principal/navigation clears local messages,
receipts and send drafts. Message bodies are rendered as text, not executable HTML.

A generation/alive guard accepts replies only for the active context; unmount
aborts pending requests. Stopping a poll cannot undo a server claim that already
completed. The UI explains that uncertainty, does not report an unconfirmed
rollback, and never automatically repeats a send after an interrupted request.
A stale native receipt is rejected under the documented local semantics.

## API reference

The existing shared root operation retains all61 operation IDs/34 paths,
JSON1.1 and Query forms. Its JSON1.0 content offers nine typed SQS request/response
subsets and named static examples. Select the matching AmazonSQS target, sqs
service hint and region. Create/Get actual QueueUrl first; replace receipt
placeholders only with a current ReceiveMessage response. Static templates are
not live saved state or evidence of a prior success.

The mandatory `test/e2e/sqs_docs.py` fetches the actual embedded spec, executes all
nine templates with live substitutions and verifies the same body/attributes via
the SDK. HTTP13/13 and SDK10/10/10 passed, including refused unsubstituted fixture
placeholders, receipt rotation/stale rejection and cleanup. Raw credential and
receipt values are omitted from reports.

## Verification and remaining gates

- Canonical pinned Cloudscape build,22 original package notices and asset checks
  passed. Existing cached dependencies were byte-verified; no new install.
- Native SDK335calls/sends/responses passed across two persistent team DBs,
  two regions, restart, real60second retention and cleanup.
- Actual focused embedded UI passed all nine operations with SDK25/25/25:
  three creation attributes, Enter/Cancel, current receipt/release/redelivery,
  stale refusal, peer/token separation, participant403 without mutation,
  a held actual west response rejected after east navigation and restart/relogin.
  Four pinned-axe states cover Details/Messages at1440/390pixels with zero
  violations, no outside requests and no browser errors. This is not a full
  accessibility certification or every mobile interaction.
- Full Go/race/vet/assets/module verification/build passed at code inpute039c50.
  One fresh full-module profile:5650/5960statements (94.80%) and unfiltered
  source7388/8190 (90.21%). Neither floor, exclusions nor profile unions changed.
- The last immutable finite run at341dec2 passed11baselines/40required controls;
  policy19/applicability21 units and20exact blob declarations also passed.
  [Model correspondence and limitations](formal-sqs-console.md) remain explicit.

At code inputebb3ba5, all23 Conformance cases passed in374.264seconds, plus
responsive layout, actual built/unbuilt make-start/TTY/auth/signals and the
50-screen global audit (zero automated violations). All21previous cases and both
new mandatory helpers remain enabled. Final documentation-HEAD immutable
formal/applicability receipts are recorded separately; older results are not
current CI. Current hosted CI/security/public merge have not been performed.

## AWS reference boundary

Public primary guides inform the [Queues/Details flow](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-overview.html),
[Standard creation and sending](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/creating-sqs-standard-queues.html),
[explicit receive/delete](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/step-receive-delete-message.html)
and [queue deletion confirmation](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/step-delete-queue.html).
Their two linked screenshot GETs returned403; no alternate route/retry was used.
No image pixels or authenticated AWS screen were observed. Current AWS permits
1MiB messages and managed encryption; these are not the local256KiB/plaintext
subset. AWS console polling side effects, distributed delivery/eventual counts
and pixel equivalence have not been verified. No actual AWS resource or billing
operation was used.
