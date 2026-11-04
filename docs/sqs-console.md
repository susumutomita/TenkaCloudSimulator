# SQS console source candidate

This candidate adds a Cloudscape view over the existing nine native Standard
queue operations. It is source preparation at main `b61d029`, not an executed,
bundled or publicly integrated UI. No native API, server composition, core schema,
SNS or DynamoDB code is changed. Root owns final integration and publication.

## Flow and API contract

| Screen/action | Actual existing operation | Boundary |
| --- | --- | --- |
| Queues, prefix and next/previous page | ListQueues, GetQueueAttributes |25 entries per page; denied metadata is unavailable, never manufactured zeroes |
| Open queue by exact name | GetQueueUrl, GetQueueAttributes | Useful when ListQueues is denied; no automatic admin fallback |
| Create queue → Details | CreateQueue, GetQueueUrl, GetQueueAttributes | Standard only; VisibilityTimeout0–43200, DelaySeconds0–900, MessageRetentionPeriod60–1209600; values in seconds |
| Send and receive messages → Send message | SendMessage | Text body1–262144 UTF-8 bytes; per-message delay0–900; actual MessageId/MD5 only after acknowledgment |
| Poll for messages | ReceiveMessage | One explicit request, maximum1–10, wait0–20, visibility0–43200; no background poll or automatic delete |
| Change message visibility | ChangeMessageVisibility | Only the current in-memory receipt;0 releases and clears local receipt, requiring another poll |
| Delete message | DeleteMessage | Single selected message with its current receipt; no invented batch API |
| Delete queue | DeleteQueue | Confirm literal `delete`; removes its messages, including in-flight messages |

Every wire call is a relative-root POST using JSON1.0 and the exact
`AmazonSQS.<Operation>` target, selected region and SQS service. A returned QueueUrl
is an input identifier, never a URL the browser fetches. The parent authenticated
request helper handles401; IAM remains native and applies on every operation.

Message receipts are held only in a private component ref. They do not enter
React state, rendered markup, URLs, local storage, screenshots or application
logs. Message bodies are React text/read-only textarea values, never HTML.
Leaving or changing queue/region/principal/navigation clears messages, receipts
and send drafts. A generation guard accepts replies only for the active view;
unmount aborts requests. Poll cancellation may follow a completed server claim,
so the UI explains the uncertainty and does not report an unconfirmed undo.
Interrupted writes likewise require refresh before a deliberate retry.

## Public reference and deliberate differences

The public AWS [console overview](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-overview.html)
shows Queues and queue Details. The public [Standard queue creation/send guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/creating-sqs-standard-queues.html)
uses Standard, Name and Configuration followed by Send and receive messages.
The public [receive/delete guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/step-receive-delete-message.html)
describes explicit Poll for messages and message deletion. Queue deletion uses
the public [delete confirmation flow](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/step-delete-queue.html).
The public text guides were examined. Their queue-list/detail image URLs were
identified, but direct image retrieval returned403, so those pixels were not
visually inspected and no alternate retrieval was attempted. No authenticated
AWS console, real resources or billing operations were accessed. Pixel
equivalence and current authenticated-console behavior are not established.

Current AWS documentation permits1MiB messages and managed encryption. This
local implementation retains its explicit256KiB bound and does not implement
encryption/KMS. FIFO, editing attributes, queue policies/tags, DLQ/redrive,
PurgeQueue, batch operations, triggers, automatic Lambda delivery and CFN remain
unavailable; no simulated success or editable controls claim otherwise. Local
read-time counts and receipt strictness remain those of [native SQS](sqs.md), not
AWS eventual counts/distributed delivery. This view uses explicit native
ReceiveMessage visibility settings; it does not claim to reproduce every side
effect of AWS console polling.

## Required acceptance (unrun)

- Build the canonical cached Cloudscape bundle and verify rights/source hashes;
  run the actual single Go binary, not a frontend-only mock.
- Create from keyboard; SDK verifies all three attributes and actual persisted
  row. Cancel never submits; invalid fields create nothing.
- Send/poll native message, release with visibility0, redeliver with a new receipt,
  refuse stale native receipts, delete through UI then confirm native absence.
- Two authenticated processes/DBs/tokens and two regions; same-name independent
  state, wrong-token401, IAM denied actions and restart persistence/cleanup.
- Delay real detail/poll replies and switch queue, region, identity and service;
  stale data/receipts must not enter the new view. Check receipt values never
  appear in DOM, browser console output, reports or URL/storage.
- PC1440/mobile390, keyboard, native API request/response nonvacuity, pinned axe,
  no outside request; failed actions cannot reuse a prior success indicator.
- Register mandatory E2E and honest async-view formal applicability/model controls,
  preserve all main protected models, and pass full-module both90% floors and
  before-commit/E2E/formal gates. No success is inferred from this source file.

## Formal source registration

The newly registered SQSConsoleEpoch plus its four required sensitivity controls
model bounded view acceptance/receipt custody only; see the
[source/native correspondence](formal-sqs-console.md). Mainb61 ten models and
thirty-six controls remain unchanged. Policy fixtures protect all forty IDs and
each new critical property. Strict applicability records use baseb61; Conformance
includes the actual helper as mandatory source. Registration is not an execution
result. No Node/Go/Java/browser or new Python unit suite has run for this source.

## Canonical build and API-reference checkpoint

After the SOURCE25cc checkpoint, same-lock existing dependencies were physically
byte-verified (16047 files/3internal symlinks; no install), canonical Cloudscape
build/22 original package notices and asset checks passed, and the offline
CGO0 binary built. Its unchanged native nine-API SDK driver passed335 matched
calls/sends/responses over two persistent team DBs, two regions, restart,
receipts,60second retention and cleanup. This is not browser/UI or model proof.

That built binary preceded the new API-reference source below. Root POST now
has a prepared JSON1.0 content choice and nine exact static examples/request
subsets alongside the preserved43 operation IDs/25 paths, JSON1.1 and Query.
Use the matching AmazonSQS target/service/region. Create/Get actual QueueUrl
first; use a current ReceiveMessage receipt for visibility/delete. MessageId
is not a receipt, and __QUEUE_URL__/__RECEIPT_HANDLE__ are placeholders, not live
values. Static templates never mean resources exist. Native responses properly
return receipts to authorized API callers; this console still keeps its receipt
values transient and does not render/log them.

The mandatory `sqs_docs.py` helper is prepared to fetch the ACTUALLY SERVED spec,
execute all nine templates in sequence with explicit live substitutions, read
the same bodies/attributes through the real SDK and remove everything. The new
reference/helper is unrun until its new embedded binary is built. Existing SDK
results from the earlier binary are not relabeled as served-example results.
Browser, TLC, policy units, globalGo/both90 and immutable/current CI remain
pending; no public action has occurred.

### Actual served-reference result (working-tree checkpoint)

The newly embedded binary now serves the same JSON as source. All nine named
JSON1.0 examples executed in order with actual QueueUrl/current ReceiptHandle,
not static-placeholder success. HTTP13/13 responses include two refused
unsubstituted fixture placeholders; SDK10calls/sends/responses confirm exact
body/attributes in the same SQLite, fresh receipt/stale refusal and cleanup.
All43 IDs/25 paths and JSON1.1/Query formats remain served. No receipt/token
values appear in the helper report. These working-tree results do not replace
UI/browser, TLC/policy, full-module both90 or immutable/current hosted gates.
