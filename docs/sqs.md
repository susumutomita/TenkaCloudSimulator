# SQS (Simple Queue Service) — supported subset

Standard queues only, over the SQS JSON 1.0 protocol
(`X-Amz-Target: AmazonSQS.<Operation>`, `application/x-amz-json-1.0`) that
current AWS SDKs speak. State lives in provider-owned SQLite tables
(`aws_sqs_queues`, `aws_sqs_messages`) and survives process restarts when the
simulator runs with `--state <file>`. No real AWS access is involved.

## Operations

| Operation | Notes |
| --- | --- |
| CreateQueue | Attributes: `VisibilityTimeout` (0–43200), `DelaySeconds` (0–900), `MessageRetentionPeriod` (60–1209600). Idempotent for identical attributes; different attributes fail with `QueueNameExists`. |
| ListQueues | `QueueNamePrefix`, `MaxResults` (1–1000) and opaque `NextToken` pagination. |
| GetQueueUrl | Resolves a queue name in the request region. |
| DeleteQueue | Deletes the queue and all of its messages. Missing queue: `AWS.SimpleQueueService.NonExistentQueue`. |
| SendMessage | Body 1–262144 UTF-8 bytes; optional per-message `DelaySeconds`; up to 10 message attributes (`String`/`Number`/`Binary`). Returns `MessageId`, `MD5OfMessageBody` and `MD5OfMessageAttributes` (AWS checksum algorithm). |
| ReceiveMessage | `MaxNumberOfMessages` 1–10, per-call `VisibilityTimeout` override, `WaitTimeSeconds` 0–20 long polling. Claimed messages become invisible, gain a fresh receipt handle and an incremented `ApproximateReceiveCount`. |
| DeleteMessage | Requires the current receipt handle; a stale/unknown handle fails with `ReceiptHandleIsInvalid`. |
| ChangeMessageVisibility | Sets a claimed message's invisibility deadline (0 makes it deliverable again). |
| GetQueueAttributes | `All` or a subset of `QueueArn`, `CreatedTimestamp`, `VisibilityTimeout`, `DelaySeconds`, `MessageRetentionPeriod`, `ApproximateNumberOfMessages`, `ApproximateNumberOfMessagesNotVisible`. Unknown names fail with `InvalidAttributeName`. |

Queue names are 1–80 letters, digits, hyphens or underscores, scoped per
region (us-east-1, us-west-2, ap-northeast-1, eu-west-1). At most 1000 queues
per process. Every operation is authorized as the IAM action `sqs:<Operation>`
against `arn:aws:sqs:<region>:000000000000:<queue>` (`*` for ListQueues), the
same identity-policy engine as the other services.

Queue URLs use the AWS shape `<scheme>://<host>/000000000000/<name>`; message
operations resolve the queue from the URL's final path segment, exactly like
SDK clients that post back the URL returned by CreateQueue.

## Deliberately unsupported (explicit errors, never silent success)

- FIFO queues (`.fifo` names, ordering, deduplication, content-based dedup).
- The SQS Query protocol (form-encoded/XML). Requests dispatched as
  CloudFormation form posts are unaffected.
- Batch operations: `SendMessageBatch`, `DeleteMessageBatch`,
  `ChangeMessageVisibilityBatch`.
- Dead-letter queues / redrive policies, SSE-SQS / SSE-KMS encryption
  attributes, queue tags, queue policies, `PurgeQueue`, and message/group
  system attributes beyond the documented response subset.
- Approximate counts are exact counts of the simulator's own state at read
  time; real SQS may report eventually consistent approximations.

## Verification

- `internal/aws/sqs_store_test.go`: store lifecycle, visibility timeout,
  receipt-handle errors, attribute checksum, and persistence across a SQLite
  reopen (simulated restart).
- `internal/server/sqs_test.go`: full HTTP lifecycle, idempotent/conflicting
  CreateQueue, FIFO rejection, unknown-operation 501, IAM allow/deny per
  action and queue ARN, region isolation, visibility override +
  ChangeMessageVisibility redelivery.

## Local repair candidate (PR #315)

This isolated proposal starts from public `95300b9` and normally merges formal
baseline `55a1119`; it does not overwrite the external service owner's branch.
The original Formal validation run failed before TLC because the old branch did
not yet contain `scripts/test-formal-gate.py`. Restoring the checker alone does
not establish SQS model applicability or a passing quality gate.

The native repair rechecks the actual queue in the same SQLite transaction as
send/receive/count/receipt operations. Expiry cleanup errors propagate and roll
back the operation. A failed multi-message receive or queue deletion preserves
the whole graph. ChangeMessageVisibility rejects a message whose visibility
window has expired; redelivery rotates its receipt handle. Invalid persisted
attribute data fails instead of being acknowledged. Startup refuses existing
orphan message rows and preserves them for explicit repair; it does not silently
delete or adopt corrupt original state.

Message bodies are limited to **256 KiB locally**. The current AWS SendMessage
reference permits **1 MiB**; this simulator's smaller bound is not an AWS quota.
Forbidden message Unicode such as NUL returns InvalidMessageContents. Existing
strict-current-receipt rejection is also bounded behavior: AWS documents that
an old handle may return success without deleting the message. The simulator
does not reproduce distributed replicas, probabilistic delivery, exact-once
processing or every AWS receipt/error timing detail.

At immutable native repair checkpoint `0a28266`, focused `^TestSQS` tests
passed in internal/aws and internal/server: normal 1.047s/1.144s and race
13.968s/11.339s with GOMAXPROCS=1 and -p 1. These include actual rollback,
cached-parent deletion, expiry/redelivery, regional receipt rejection and
non-destructive orphan startup failure. No guest VM was executed. These focused
checks do not establish whole-module coverage or real standalone SDK acceptance
for the repaired source. Full unfiltered source coverage remains a separate blocker:
the original hosted run reported 86.92% against the unchanged 90% floor. Adding
formal files or exempting runtime paths cannot resolve that test deficit.


## Formal applicability and bounded contract

`SQSLifecycle` models two distinct queue scopes (database/region/name tuples),
one queue creation and one message per scope, at most two deliveries, clock
0..2, delay/visibility 0..1 and retention two abstract ticks. A cached queue
lookup may interleave with deletion before the send transaction. Queue creation
is idempotent for equal attributes; mismatches and storage errors preserve state.
The model checks parent lifetime, abstract caller authorization, scoped mutation,
failed-operation atomicity, receive eligibility, fresh/current receipt use and
in-flight visibility changes. Eleven targeted mutants remove those guards or
introduce partial/cross-scope writes. **TLC has not yet run for this proposal.**

The actual native repair precedes these bindings: sqsQueueTx rechecks the parent
inside Send/Receive/Counts/receipt transactions; purgeExpiredTx errors propagate;
Receive commits fresh receipt updates atomically; ChangeVisibility verifies both
retention and the current in-flight deadline. SQL trigger tests exercise failed
purge, insert, multi-row receive and cascade deletion rollback. NoOrphans assumes
valid initial state; the constructor enforces that precondition by refusing
orphans without deleting them. Startup/reopen persistence is separately tested.

The model does not prove Go/SQLite refinement, random-ID collision resistance,
real time, long-poll liveness/cancellation, multiple-message batch atomicity,
queue-name reuse beyond one lifetime, message-body/attribute checksums, size or
Unicode correctness, full QueueUrl parsing, IAM policy matching or distributed
SQS delivery semantics. It assumes fresh opaque receipt handles. It does not
prove FIFO or exactly-once processing. Those limits also apply to the formal
record; existing unrelated models are retained, not presented as SQS proofs.

The current URL parser accepts the final queue-name segment (including a bare
name); it does not validate the entire supplied host/account/region URL. No
outbound connection is made from QueueUrl, and the native request region and
IAM queue ARN still select the local target. This convenience is a local
compatibility limit, not complete AWS QueueUrl validation. Some returned system
attributes are simplified; in particular ApproximateFirstReceiveTimestamp uses
the stored send timestamp and must not be claimed as an accurate first-delivery
clock. Full attribute-name selection, attribute/body aggregate sizing and
number-attribute semantic validation also remain outside this repair.

Primary references (inspected without real AWS requests):

- [SQS JSON protocol and independent regional endpoints](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-making-api-requests-json.html)
- [SendMessage: current 1 MiB bound and permitted Unicode](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html)
- [ReceiveMessage: per-call visibility and receipt handles](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)
- [DeleteMessage: most-recent receipt and distributed-delivery limits](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteMessage.html)
- [ChangeMessageVisibility: in-flight clock semantics](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html)
- [Regional endpoints](https://docs.aws.amazon.com/general/latest/gr/sqs-service.html)
