# Amazon SNS (bounded subset)

TenkaCloudSimulator implements a deliberately small, local subset of the
Amazon SNS Query API (version `2010-03-31`, XML responses, namespace
`http://sns.amazonaws.com/doc/2010-03-31/`). State lives in the same SQLite
database as the other services (`aws_sns_topics`, `aws_sns_subscriptions`,
`aws_sns_messages`, `aws_sns_deliveries`) and survives restarts when the
simulator runs with `--state`.

## Supported operations

| Operation | Notes |
| --- | --- |
| CreateTopic | Idempotent like real SNS. `Name` plus `Attributes.entry.N` for `DisplayName` and `FifoTopic` only. FIFO names must end in `.fifo`. |
| ListTopics | Per region, scoped `NextToken` pagination bound to operation, region and topic (page size 100). |
| GetTopicAttributes | TopicArn, Owner, DisplayName, subscription counts, FifoTopic. |
| DeleteTopic | Removes the topic, subscriptions, messages and all delivery records in one transaction, including history from removed subscriptions. |
| Subscribe | Protocols: http, https, email, email-json, sms, lambda, application, firehose. Duplicate (topic, protocol, endpoint) returns the existing subscription. |
| Unsubscribe | Unknown subscription ARN is `NotFoundException`. |
| ListSubscriptions / ListSubscriptionsByTopic | `NextToken` pagination (page size 100). |
| Publish | Message up to 256 KiB UTF-8, optional Subject (fewer than 100 Unicode characters; no line breaks or controls). Standard topics only; FIFO publication is explicitly refused. Returns a MessageId. |

Requests are root `POST` form Query requests, routed by the SNS credential
scope (`/sns/aws4_request`) or the `X-Simulator-Service: sns` header (the
same convention IAM uses). Region comes from the credential scope or
`X-Simulator-Region`; the four simulator regions apply. Every operation is
authorized against identity policies as `sns:<Operation>` on the topic ARN
(`*` for list operations), like SSM.

Limits per simulator process/database (across all regions): 1000 topics,
10000 subscriptions, 10000 stored messages, 64 MiB of UTF-8 message plus Subject
bytes, and 100000 delivery rows. Unsubscribe retains delivery history until
DeleteTopic; DeleteTopic removes all topic-owned records and releases budgets.
SQLite validates live topic ownership and final persisted state in the same
write transaction. Ignored/vanishing writes or incomplete cleanup return an
error and roll back. A deleted topic snapshot cannot create new records.
Startup rejects orphan/over-budget SNS state without deleting it. Older SNS PR
databases containing audit messages for deleted topics require a fresh database.
These are local simulator limits and do not claim AWS quotas or process RSS. Errors use the SNS Query `ErrorResponse` shape:
`ValidationError`, `InvalidParameterException`, `InvalidAction` for
unimplemented operations (HTTP 400), `NotFoundException` (404),
`AuthorizationErrorException` (403), `LimitExceededException` (409).

## Deliberate deviations — read before relying on behavior

- **No outbound delivery.** Publish records the message and one local
  delivery row per current subscription of the topic. Nothing is sent to
  email, HTTP, SMS, Lambda or any other endpoint; the simulator never makes
  outbound cloud calls. Subscription "confirmation" is local and immediate:
  there is no pending-confirmation flow and `SubscriptionsPending` is 0.
- **SNS-to-SQS fanout: contract defined, adapter wiring pending.** SNS
  defines the `SNSQueueFanout` interface (`QueueExists` /
  `DeliverToQueue`) in `internal/aws/sns.go`. When a fanout adapter is
  wired into the Gateway, `Subscribe` with protocol `sqs` validates the
  queue ARN (same region, simulator account) and queue existence, and
  `Publish` delivers the standard SNS notification envelope JSON
  (Type/MessageId/TopicArn/Subject/Message/Timestamp) into each subscribed
  queue before recording the publish; a failed queue delivery fails the
  Publish and records no SNS message. This callback interface cannot roll back
  an earlier queue delivery when a later callback or SNS commit fails, and is
  not an atomic fanout integration. Keep it disabled in the standalone service. This behavior is covered by Go tests
  against a stub fanout. The standalone SNS branch wires no adapter
  (`Gateway.QueueFanout == nil`), so sqs subscriptions there are rejected
  with `InvalidParameterException` saying fanout is not integrated in
  this build.

- SNS-to-Lambda invocation is likewise not performed; `lambda` protocol
  subscriptions are stored (endpoint must be a Lambda function ARN) and
  receive only local delivery records.
- No topic policies, tags, SMS sandbox, mobile push credential management,
  message attributes, message structure JSON, FIFO deduplication/ordering
  semantics, or CloudFormation `AWS::SNS::Topic` support. Unsupported
  fields and attributes are rejected rather than ignored.
- No Cloudscape console page for SNS yet; the console and the capability
  table say so (`"console": false`).

## SDK example (boto3, local only)

```python
import boto3
from pathlib import Path

# Operator token supplied locally by the host; never commit or print it.
token = Path("simulator-token").read_text().strip()
sns = boto3.client(
    "sns", region_name="us-east-1", endpoint_url="http://127.0.0.1:4566",
    aws_access_key_id="TCSIMDUMMY", aws_secret_access_key="dummy",
)
sns.meta.events.register("before-sign.sns", lambda request, **kwargs:
    request.headers.add_header("X-Simulator-Token", token))
topic = sns.create_topic(Name="alerts")["TopicArn"]
sns.subscribe(TopicArn=topic, Protocol="email", Endpoint="team@example.com")
sns.publish(TopicArn=topic, Message="hello")
```

Query fields must occur exactly once. CreateTopic parses every
`Attributes.entry.N.key/value` pair, including sparse positive numeric indexes;
malformed/unpaired entries, duplicate attribute names and unsupported attributes
are refused before state changes. Unsupported trailing entries are never ignored.
`ReturnSubscriptionArn` accepts only `true` or `false`; confirmation remains the
local immediate-confirmation deviation described above. Unsubscribe uses the
selected region and cannot remove another region's subscription.

FIFO topic metadata can still be created/read/deleted, but Publish refuses FIFO
topics because ordering/deduplication/group semantics are not implemented.

The service has no SNS console, CloudFormation adapter or real cloud delivery.
SDK/HTTP operations are the supported workflow. The disabled queue callback
requires a separate transaction-aware contract and native SQS verification
before it can be advertised as supported fanout.

Real AWS credentials, session tokens and presigned requests are rejected,
consistent with the other services.

List tokens encode only a position and operation/region/topic scope; they are
not authentication or authorization credentials. Permission checks run on every
request. Tokens from the earlier unscoped SNS PR format are refused. Lists can
change between pages; continuation resumes after the last ARN, even if earlier
rows were deleted. No immutable-snapshot consistency is claimed.

The finite models `SNSAdmission` and `SNSLifecycle` cover normalized admission
witnesses, parent ownership, global abstract budgets, post-operation ACK and
cleanup/peer frames. Native malformed Query, SQLite ignored/vanishing/late-write,
read/commit failures and exact-byte quota tests remain separate obligations.
The models do not prove Go/SQL refinement, parsing, cryptography, UUID allocation,
actual cloud behavior or the disabled SQS callback. See the registered bounds.
