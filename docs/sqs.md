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
introduce partial/cross-scope writes. The first immutable full-gate attempt passed
the original five models and controls but the new baseline hit the unchanged
60-second timeout; that attempt is a failure, not a counterexample or proof.
The model now normalizes deleted-row metadata (matching actual SQL DELETE) and
enumerates current/previous/unissued/peer receipt candidates instead of disabled
receipt tuples; the baseline receipt equality still admits exactly the same
current-handle transitions. All properties and eleven controls remain. At
immutable `9145461`, the strict merge-gate run completed with an explicit 512 MiB
Java heap, one worker and the unchanged 60-second per-case timeout: six baseline
models and sixteen required controls all passed. SQSLifecycle explored 5,518,200
generated / 413,116 distinct states. Each of its eleven controls returned exit 12
with the exact named NoOrphans, ScopedMutation, ReceiveOnlyEligible, FreshReceipt,
ReceiptMutationCurrent, AuthorizedMutations, ChangeOnlyInflight,
FailedMutationAtomic or IdempotentCreateUnchanged violation. The five original
models/configs/registry entries and all strict gate function bodies remain
unchanged; the added required SQS ID/property set is checked explicitly. Python
unit controls (10+13) and all seventeen exact changed-blob declarations passed.
These are local bounded-model results, not new-head hosted CI or coverage proof.

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


The installed development SDK's SQS metadata was also inspected: API model
`2012-11-05`, JSON protocol version `1.0`, target prefix `AmazonSQS`. The JSON
wire uses the target/content-type headers above; the legacy Query protocol's
Version parameter is not an extra supported JSON request field. Regional
production endpoint examples in the linked AWS reference are documentation only;
all native tests target the local simulator and use dummy credentials.


Additional acceptance checkpoint (source-only; not executed yet): explicit JSON
`MaxResults: 0` and `MaxNumberOfMessages: 0` now fail instead of selecting omitted
parameter defaults. Their documented minima are 1; omission retains 1000 and 1,
respectively ([ListQueues](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListQueues.html),
[ReceiveMessage](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)).
New regression cases cover these distinctions, GetQueueUrl, persisted nondefault
queue settings, per-message delay override, exact attribute selection, native
composition bounds, capacity/idempotency and real SQLite-to-HTTP rollback errors.
The unused isSQS helper was removed after a whole-repository reference search
found only its declaration; actual dispatch already selects the AmazonSQS target.
These new tests and changes require fresh execution. A previous whole-suite
attempt failed the unchanged bounded-output VM deadline; one isolated retry
passed. Its failed profile was inspected only for diagnostic uncovered blocks,
never combined with another profile or accepted as whole coverage evidence.
The finite model files remain unchanged; their previous bounded results do not
prove JSON validation, HTTP errors or the new acceptance tests.

The additional checkpoint also rejects unknown nested message-attribute members
(including unsupported StringListValues/BinaryListValues) instead of silently
dropping them during typed decoding. This enforces the documented local subset;
it does not add list-attribute support. Rejection/no-enqueue and supported custom
data-type suffix regressions accompany the decoder change. The 8bd checkpoint's
whole run was deliberately terminated after source review found this gap; its
partial profile is diagnostic only. New-head execution remains pending.

GetQueueAttributes now validates every requested name before applying All,
preventing unsupported names from being accepted depending on list order. A raw
MessageBody encoding guard refuses malformed UTF-8 and unpaired UTF-16 surrogate
escapes before Go's JSON decoder can silently replace them. This is an explicit
local no-silent-rewrite rule; malformed-input behavior has not been compared by
requests to real AWS. Valid surrogate pairs, escaped literal backslash-u text,
mixed literal/escaped UTF-8 and deliberate U+FFFD remain valid and have wire
roundtrip regression cases. The finite lifecycle model does not model this parser
or prove Unicode validation. All these latest source changes remain unexecuted.

The delivered/in-flight count excludes messages still under their initial send
or queue delay: NotVisible means already delivered and currently hidden, not all
future-visible rows ([GetQueueAttributes](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html)).
Native and wire regressions distinguish initial delay, available, received/hidden
and released visibility states. This subset still does not expose the separate
ApproximateNumberOfMessagesDelayed attribute. Counts changes await execution;
previous focused results apply only to the exact preceding 285827a checkpoint.


Latest local scoped verification: runtime/test checkpoint a938f3b passed the full
`^TestSQS` selection under race in internal/aws (10.262 s) and internal/server
(11.594 s), including the admission, raw-body roundtrip, real SQLite wire-error
rollback and delayed/in-flight counter regressions above. The preceding 285827a
checkpoint also passed normal and race selections before the Counts correction.
Policy/applicability unit controls passed 10+21 cases, and the exact nineteen
changed-blob record validated against base 2d8a41a. This supersedes the historical
UNRUN labels for these scoped tests only. A fresh successful whole-module run,
unfiltered 90% source floor, independent standalone SDK two-team/restart test
and new-head hosted CI are still pending. No new model execution or broad service
completion is claimed; the model/config/registry bytes are unchanged from 06d.


Current source integration includes the main SSMParameterState contract alongside
SQSLifecycle and the original five mandatory models. Both inventories and every
required invariant remain protected. The independent standalone SDK driver is
mandatory through test/e2e/check.py and uses two authenticated CLI processes,
separate SQLite/token state, two regions, restart, receipt rotation, real minimum
60-second retention and cleanup. Its source handoff is integrated; actual
execution on the new binary is still pending. No SQS console UI is implemented,
and native service support is not original-problem or full-service completion.
