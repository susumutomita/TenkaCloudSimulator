# Amazon SNS (bounded subset)

TenkaCloudSimulator implements a deliberately small, local subset of the
Amazon SNS Query API (version `2010-03-31`, XML responses, namespace
`http://sns.amazonaws.com/doc/2010-03-31/`). State lives in the same SQLite
database as the other services (`aws_sns_topics`, `aws_sns_notification_policies`, `aws_sns_subscriptions`,
`aws_sns_messages`, `aws_sns_deliveries`) and survives restarts when the
simulator runs with `--state`.

## Supported operations

| Operation | Notes |
| --- | --- |
| CreateTopic | Idempotent like real SNS. `Name` plus `Attributes.entry.N` for `DisplayName` and `FifoTopic` only. FIFO names must end in `.fifo`. |
| ListTopics | Per region, scoped `NextToken` pagination bound to operation, region and topic (page size 100). |
| GetTopicAttributes | TopicArn, Owner, DisplayName, subscription counts, FifoTopic, and the exact Policy string when configured. |
| SetTopicAttributes | Set the Policy attribute on an existing Standard topic for the notification service described below. |
| DeleteTopic | Removes the topic, subscriptions, messages and all delivery records in one transaction, including history from removed subscriptions. A valid, authorized deletion of an absent topic succeeds without changing state; retries are idempotent. |
| Subscribe | Protocols: sqs, http, https, email, email-json, sms, lambda, application, firehose. SQS requires a same-region Standard queue and caller SendMessage permission. Duplicate (topic, protocol, endpoint) returns the existing subscription. |
| GetSubscriptionAttributes | Read the retained subscription by ARN in the selected region, with IAM on its topic. |
| Unsubscribe | Unknown subscription ARN is `NotFound`. |
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
`ValidationError`, `InvalidParameter`, `InvalidAction` for
unimplemented operations (HTTP 400), `NotFound` (404),
`AuthorizationError` (403), `LimitExceededException` (409).

## CodeStar Notifications topic policies

On an existing Standard topic, send an authenticated SNS Query
`SetTopicAttributes` with its `TopicArn`, `AttributeName=Policy` and a JSON
policy string in `AttributeValue`. The AWS notification guide uses this
statement for an existing topic:

```json
{"Version":"2008-10-17","Statement":[{"Sid":"AWSCodeStarNotifications_publish","Effect":"Allow","Principal":{"Service":["codestar-notifications.amazonaws.com"]},"Action":"SNS:Publish","Resource":"arn:aws:sns:us-east-1:000000000000:events"}]}
```

`GetTopicAttributes` returns the saved string verbatim. Each operation requires its
own IAM action on the topic ARN. An existing topic without a saved policy omits
the attribute and grants no service access. A policy edit keeps the topic's
internal identity. Deleting a topic retires its identity and policy. A newly
created topic with the same ARN starts without a policy.

The document accepts versions `2008-10-17` and `2012-10-17`. `Statement` can
be an object or a list of one to eight objects. Each statement applies only
to the exact local topic ARN, service principal and publish action shown above.
The three values accept either a string or a singleton string list. Action
names are ASCII case-insensitive. Optional document `Id` and statement `Sid`
contain 1–128 bytes of printable UTF-8. Statement identifiers must be unique.
An explicit `Deny` overrides any `Allow`. The local size limit is 8192 bytes
per document. Other principals, actions, resources, conditions and wildcard
values return `InvalidParameter`. The simulator checks ordinary callers and
SQS queue access through their existing IAM policies.

[SetTopicAttributes](https://docs.aws.amazon.com/sns/latest/api/API_SetTopicAttributes.html) ·
[GetTopicAttributes](https://docs.aws.amazon.com/sns/latest/api/API_GetTopicAttributes.html) ·
[Configuring an SNS target for notifications](https://docs.aws.amazon.com/dtconsole/latest/userguide/set-up-sns.html)

## Deliberate deviations — read before relying on behavior

- **HTTP/S subscription endpoints.** Subscribe requires an absolute URL with a
  hostname and the scheme selected by the protocol (for example, `https`
  requires `https://`). Invalid endpoints return `InvalidParameter` before
  a subscription is saved. The simulator performs no DNS or reachability check.
- **No outbound delivery.** Publish records the message and one local
  delivery row per current subscription of the topic. SQS subscriptions additionally
  enqueue a real local SQS message in the same SQLite transaction. Nothing is sent to
  email, HTTP, SMS, Lambda or any other endpoint; the simulator never makes
  outbound cloud calls. Subscription "confirmation" is local and immediate:
  there is no pending-confirmation flow and `SubscriptionsPending` is 0.
- **Atomic native SNS-to-SQS fanout.** The composed server supports Standard
  topics and Standard queues in the same simulator process, region and fixed
  account. Subscribe validates the live queue inside its transaction. Publish
  takes one current subscription snapshot and commits every destination message
  together with the SNS message/delivery rows. Missing queues, quota errors,
  cancelled requests and persistence faults fail the whole publication; no
  preceding enqueue or scoped expiry cleanup remains after rollback. This
  synchronous all-or-nothing contract is a local deviation from AWS delivery
  retries, not a claim of distributed AWS transaction semantics.
- **Stricter local authorization.** In addition to the documented SNS action,
  Subscribe and Publish require the caller's `sqs:SendMessage` permission on each
  queue ARN. One immutable identity-policy snapshot covers the complete request;
  an explicit Deny rejects the entire fanout. Real AWS uses queue resource policy
  permissions for the SNS service principal. Those policies and service-trust
  conditions are not implemented here, and the simulator does not silently grant
  them. Ordinary SQS ReceiveMessage/DeleteMessage authorization is unchanged.
- Queue messages contain Type, MessageId, TopicArn, Message, Timestamp and optional
  Subject JSON fields. MessageId matches the SNS result; SQS assigns its own
  independent message ID. Signatures, certificate/unsubscribe URLs, raw delivery,
  attributes, filters, FIFO and dead-letter/retry workflows remain unsupported.
  The escaped envelope must fit the existing SQS body limit. A message valid for
  SNS alone can therefore be rejected when its envelope is too large for SQS.
- Queue delay and retention follow the native SQS store. Deleting a queue leaves
  its SNS subscription in place; Publish fails until the queue is recreated or
  the subscription is removed. A recreated same-name queue has the same ARN and
  can receive later publications. Unsubscribe and DeleteTopic never erase messages
  already delivered to SQS. Other regions, processes and SQLite databases receive
  no writes. The historical callback interface remains a compatibility/test seam;
  the composed server selects the same-database native transaction instead and
  never uses its nontransactional delivery callback.

- SNS-to-Lambda invocation is likewise not performed; `lambda` protocol
  subscriptions are stored (endpoint must be a Lambda function ARN) and
  receive only local delivery records.
- General topic policies, tags, SMS sandbox, mobile push credential management,
  message attributes, message structure JSON, FIFO deduplication/ordering
  semantics, and CloudFormation `AWS::SNS::Topic` are unsupported. Unsupported
  fields and attributes are rejected.
- The local Cloudscape SNS page provides bounded HTTP/HTTPS subscriptions.
  Topic creation and attribute-policy editing are not provided by this view.
  The local Scalar reference only sends Subscribe, in us-east-1 under the signed-in
  session identity. It does not inherit the Console’s selected exercise identity.
  Neither surface delivers notifications.

## Subscription read-back

[GetSubscriptionAttributes](https://docs.aws.amazon.com/sns/latest/api/API_GetSubscriptionAttributes.html)
returns `SubscriptionArn`, `TopicArn`, `Owner`, `Protocol` and `Endpoint` for a
retained local subscription. It also returns `PendingConfirmation=false`
(local immediate confirmation), `ConfirmationWasAuthenticated=false` (no
confirmation request), and `RawMessageDelivery=false` (unsupported).
No delivery, filter, replay, redrive or role policy is invented. The IAM
action is `sns:GetSubscriptionAttributes` on the parent topic ARN, checked
before reading state. Unsubscribed and topic-deleted subscriptions are not
readable, even when old delivery records still exist.

This is the read step and deletion-polling response expected by the
[pinned AWS Terraform provider v5.100.0](https://github.com/hashicorp/terraform-provider-aws/blob/v5.100.0/internal/service/sns/topic_subscription.go#L137-L152).
It does not provide full Terraform compatibility: subscription create with
policy, raw delivery or other nondefault attributes and attribute updates
remain unsupported. Topic tags, broader topic attributes and provider
authentication setup are separate acceptance requirements.

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

CloudFormation and real cloud delivery are not supported. The Console and the
local API reference expose the bounded HTTP/HTTPS subscription workflow
described above; the reference can send a real local Subscribe request. Other
supported operations use the local SDK/HTTP wire. The native queue transaction is
independently covered by real SDK and SQLite fault regressions; the legacy callback
is not an atomic integration contract.

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

SNS standard error codes use their wire names (`InvalidParameter`, `NotFound`,
`AuthorizationError`), so official SDK clients raise their modeled exception
classes (`InvalidParameterException`, `NotFoundException`,
`AuthorizationErrorException`). `LimitExceededException` remains an explicitly
local simulator quota error, not a modeled SNS error. DeleteTopic still validates
ARN/region and IAM authorization for absent topics; storage read/commit errors
remain failures. Unsubscribe retains its existing missing-subscription error.

## Native fanout sources and acceptance

The bounded wire shape follows [Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html),
[Publish](https://docs.aws.amazon.com/sns/latest/api/API_Publish.html) and the
[notification envelope](https://docs.aws.amazon.com/sns/latest/dg/sns-sqs-as-subscriber.html).
The [AWS queue subscription guide](https://docs.aws.amazon.com/sns/latest/dg/subscribe-sqs-queue-to-sns-topic.html)
describes service-principal resource policies, which are deliberately excluded
from this stricter local authorization model. These sources were checked on
2026-11-10; no real AWS operation or differential cloud test was performed.

Local checkpoint `7a1e022` passed all targeted SNS/SQS normal and race tests,
composed-server SNS tests, AWS/server vet and build. Its real boto3 driver passed
53 calls/sends/responses, envelope correlation, complete refusal, two-process
isolation, restart and cleanup; the inherited SNS SDK checks also passed. The
bounded fanout baseline and nine controls passed with immutable before/after
inputs. Independent source review identified and verified the fix for a mixed
policy-snapshot defect. These are historical fanout results, distinct from the
HTTP/HTTPS subscription and Console changes above. They do not establish
full SNS, CloudFormation, or end-to-end compatibility.
