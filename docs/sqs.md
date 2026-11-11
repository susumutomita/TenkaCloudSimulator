# SQS supported subset

The simulator implements nine Standard-queue operations over AWS JSON 1.0:
root `POST`, `Content-Type: application/x-amz-json-1.0`, and
`X-Amz-Target: AmazonSQS.<Operation>`. The provider prefix
`/_simulator/providers/aws/` reaches the same SQLite state. No real AWS access,
external queue service, host Python or Node runtime is required by the binary.
Use `--state <file>` to retain queues and messages across process restarts.

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


Receive returns a fresh opaque receipt on every delivery. Delete and visibility
changes require the current receipt; ChangeMessageVisibility also requires a
currently in-flight message. A visibility timeout of zero releases it. An expired
receipt cannot extend a visibility window; redelivery invalidates the old handle.
Explicit zero list/receive counts fail; omitted counts retain defaults of 1000/1.
Unknown operations return 501; unsupported request fields, nested attribute
members and invalid types return errors rather than being silently discarded.

## Authentication, scope and local limits

The CLI requires host-issued access tokens by default. Dummy `TCSIM...` signing
keys identify the wire format and request region, not the host identity. Real
AWS-style keys, session tokens and conflicting explicit/credential regions are
rejected. IAM evaluates `sqs:<Operation>` against
`arn:aws:sqs:<region>:000000000000:<queue>` (`*` for ListQueues). Identity
selection is not authentication. Hosts must allocate separate process, SQLite
file and token authority per team; see [the access boundary](../SECURITY.md).

Queue names use 1–80 ASCII letters, digits, underscore or hyphen. The supported
regions are us-east-1, us-west-2, ap-northeast-1 and eu-west-1. Limits below are
**local simulator limits**, not AWS service quotas:

| Limit | Behavior |
| --- | --- |
| HTTP request | At most 320 KiB |
| Message body | 1–262144 UTF-8 bytes; forbidden SQS body characters are rejected |
| Message attributes | At most 10; String/Number/Binary with optional custom data-type suffix |
| Queue inventory | At most 1000 queues per provider database |
| Physical message inventory | At most 10000 persisted messages across every queue/region |
| Stored payload | At most 64 MiB: UTF-8 body bytes plus exact serialized attribute JSON bytes, including encoding overhead |

SendMessage admits the new row only if both aggregate budgets fit in the same
SQLite transaction. A refusal returns HTTP 400 `SimulatorQuotaExceeded`. Expired
rows in other queues/regions still consume capacity until their own scoped
read/delete cleanup removes them. Send never purges foreign rows to make space.
Own expiry cleanup and insertion commit together or roll back together, including
on quota refusal. Usage is read from actual rows rather than a separate mutable
counter. Valid legacy over-cap state is preserved and remains readable/deletable;
new sends require their post-scoped-cleanup state to fit the limits. The payload
limit is not a promise about SQLite file size, metadata, WAL or filesystem quotas.

Startup refuses existing orphan message rows without deleting or adopting them.
Native operations recheck the parent inside the transaction. SQL/scan failures
abort instead of becoming empty successful results. Expected-single-row create,
send and receive writes check RowsAffected before acknowledging a queue/message/
receipt; queue deletion also checks its result. Real SQLite ABORT/IGNORE and
schema/corrupt-row fixtures test scoped rollback. IGNORE fixtures use defined
pure RAISE(IGNORE) behavior for UPDATE. These are tested storage-fault classes,
not protection against arbitrary malicious database/host writers, which the
security policy excludes.

## Compatibility boundaries

- The current AWS SendMessage reference permits 1 MiB; the simulator's body bound
  is deliberately smaller. JSON body encoding rejects malformed UTF-8/unpaired
  surrogate escapes before Go can replace them. Valid surrogate pairs, escaped
  literal backslash-u, mixed escaped/literal UTF-8 and intentional U+FFFD retain
  their decoded bytes. Malformed-input responses were not compared with live AWS.
- QueueUrl resolves its final name segment (or a bare name); the entire supplied
  host/account/region is not validated. Request region and IAM choose the local
  target. QueueUrl never causes an outbound connection.
- Message attribute selection supports exact names, All and .*; prefix wildcard
  selection, complete AWS name/Number semantics, aggregate AWS message sizing
  and full system-attribute selection are not reproduced. The first-receive
  timestamp currently uses the stored send timestamp.
- NotVisible counts delivered messages still hidden, excluding initial-delay
  messages. These are local read-time counts, not AWS eventual approximations;
  the separate delayed-count attribute is unsupported.
- Stale receipt deletion is strictly rejected locally. AWS documents that old
  receipt deletion may succeed without deleting the message. No distributed
  replicas, probabilistic delivery or exactly-once processing is simulated.
- FIFO, batch APIs, Query/XML, DLQ/redrive, encryption, queue tags/policies,
  PurgeQueue, CloudFormation SQS resources and Lambda SQS event sources/guest SDK
  capability are not implemented. Existing unsupported
  Challenge problems are not marked runnable by this native API increment.

## Reproducible verification

```sh
make before-commit
AXE_SCRIPT=/path/to/pinned/axe.min.js make e2e
TLA_JAR=/path/to/pinned/tla2tools.jar make formal
```

The repository requires both full-module statement and unfiltered Cobertura
source-line coverage to reach 90%. Record results against the exact tested commit
in the relevant Issue or PR; focused or historical passes do not replace the
complete gates.

The [SQS SDK driver](../test/e2e/sqs.py) uses two authenticated CLI processes,
separate SQLite/token state and two regions. It checks all nine operations,
permuted cross-region receives, an empty-region/peer-only oracle, token rejection,
restart/persisted visibility, receipt rotation, actual delay/visibility and
minimum 60-second retention, prefix routing and cleanup. This covers the bounded
native lifecycle, not UI or full service/problem completion. Native Go tests
additionally exercise exact row/encoded-byte quota boundaries, two concurrent
last-slot writers, expired foreign charging, legacy over-cap read/delete, corrupt
persisted data, IAM and real SQL rollback/ack failures.

## Finite model contracts

SQSLifecycle bounds two queue scopes, one queue/message lifetime per scope, two
deliveries and a coarse clock. Its eleven sensitivity mutations cover parent
lifetime, scope/IAM, eligibility, rollback and current receipts. SQSStorageBudget
bounds two trusted scopes, three possible legacy rows plus one fresh ID,
abstract row weights1–2 and caps2rows/3units. Its seven mutants exercise row/byte
caps, expired foreign charging, scoped purge, rollback, live-row preservation
and legacy maintenance. The [formal registry](../formal/models.json) is the
executable inventory; all registered models and protected properties remain
required. Exact immutable-head execution is required separately.

These are finite abstract safety checks, not Go/SQLite refinement, actual
UTF-8/JSON arithmetic, crypto, SQL concurrency/crash or filesystem-size proofs.
They do not prove real time, long-poll liveness, distributed delivery or complete
AWS IAM. Native multi-row batch atomicity and two-process authority are separate
concrete tests. Bounds are exploration limits, not production quotas.

## Primary references

- [SQS JSON wire protocol](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-making-api-requests-json.html)
- [SendMessage body size and permitted Unicode](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html)
- [ListQueues pagination](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListQueues.html)
- [ReceiveMessage and receipt handles](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)
- [DeleteMessage distributed-delivery limits](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteMessage.html)
- [ChangeMessageVisibility](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html)
- [Queue count attributes](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html)
- [SQLite trigger cautions and RAISE](https://www.sqlite.org/lang_createtrigger.html)

## Basic console and served API reference

The [Cloudscape console](sqs-console.md) uses these exact nine native operations
for queues, Standard creation with the three supported attributes, details,
send/poll, current-receipt visibility/deletion and queue deletion. See the console
document for browser/SDK verification boundaries and the served API examples.
This does not add native API/schema/IAM semantics, FIFO/CFN/Lambda/Challenge,
resource policies, editing or encryption support. Static API examples require
actual QueueUrl/current ReceiptHandle substitution and are never live state.
