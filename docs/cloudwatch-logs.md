# CloudWatch Logs: bounded native API

This increment adds CloudWatch **Logs**, not CloudWatch metrics or alarms. Its API
and console use provider-owned SQLite tables. It does not deliver Lambda logs
automatically, provision an agent, enable a Challenge problem, or claim visual
parity with an authenticated AWS console. The Cloudscape console calls the same
seven native operations as SDK clients.

## Native protocol and supported operations

Use the authenticated AWS provider root endpoint, JSON 1.1 POST, and
`X-Amz-Target: Logs_20140328.<operation>`. The signing service is `logs`. Only
dummy `TCSIM` credentials or unsigned simulator requests are accepted, alongside
the local/team access token. Real AWS credential keys and session-token headers are rejected; dummy
signature bytes are routing data, not real AWS authentication. Region comes from the trusted Gateway's regional scope and must be one
of the simulator's four supported regions.

| Operation | Request subset | IAM resource |
| --- | --- | --- |
| CreateLogGroup | logGroupName, optional logGroupClass=STANDARD | group ARN |
| DescribeLogGroups | logGroupNamePrefix, logGroupClass=STANDARD, limit, nextToken | `*` |
| DeleteLogGroup | logGroupName | group ARN |
| CreateLogStream | logGroupName, logStreamName | stream ARN |
| DescribeLogStreams | logGroupName, logStreamNamePrefix, orderBy, descending, limit, nextToken | group ARN |
| PutLogEvents | logGroupName, logStreamName, logEvents, optional ignored sequenceToken | stream ARN |
| GetLogEvents | logGroupName, logStreamName, startTime, endTime, startFromHead, limit, nextToken, unmask=false | stream ARN |

Group IAM resource is `arn:aws:logs:<region>:000000000000:log-group:<name>`;
stream resource appends `:log-stream:<stream>`. DescribeLogGroups returns both
`logGroupArn` without a suffix and the legacy `arn` ending in `:*`. IAM uses the
service authorization reference's resource form. The existing limited identity
policy evaluator applies; tags/conditions, resource policies, cross-account
access and full AWS IAM are not implemented. Permission errors remain distinct
from database/policy infrastructure failures. Pagination reauthorizes every call.

Unknown operations return 501. Unsupported fields, nulls, duplicate keys and
incorrect field types return an explicit InvalidParameterException. Resource
absence/duplicates return AWS-style 400 ResourceNotFoundException or
ResourceAlreadyExistsException; explicit IAM denial returns 403; internal state
failures return a sanitized 500 ServiceUnavailableException. Successful create
and delete operations return an empty HTTP 200 body.

## Ingestion and retrieval

Minimal local SDK example (the token stays in its private file; dummy AWS
credentials are deliberate):

```python
import time
from pathlib import Path
import boto3
from botocore.config import Config

token = Path("simulator-token").read_text().strip()
logs = boto3.client("logs", endpoint_url="http://127.0.0.1:14566",
                    region_name="us-east-1", aws_access_key_id="TCSIMLOCAL",
                    aws_secret_access_key="dummy",
                    config=Config(retries={"max_attempts": 0}))
logs.meta.events.register("before-sign", lambda request, **kwargs:
                          request.headers.add_header("X-Simulator-Token", token))
logs.create_log_group(logGroupName="/aws/example")
logs.create_log_stream(logGroupName="/aws/example", logStreamName="application")
logs.put_log_events(logGroupName="/aws/example", logStreamName="application",
                    logEvents=[{"timestamp": int(time.time() * 1000),
                                "message": "Hello from the local simulator"}])
page = logs.get_log_events(logGroupName="/aws/example",
                           logStreamName="application", startFromHead=True)
assert page["events"][0]["message"] == "Hello from the local simulator"
logs.delete_log_group(logGroupName="/aws/example")
```

Group names use the AWS ASCII character set and cannot start with `aws/` (the
usual `/aws/` prefix is valid). Stream names contain 1–512 Unicode characters and
exclude `:`, `*` and, as a documented simulator restriction, NUL. Groups are
STANDARD with no expiry. KMS, tags, deletion protection, retention APIs, other
classes, identifiers/ARN selectors, linked accounts, data protection/unmask,
filters, subscriptions, Insights queries and exports are rejected or unsupported.

PutLogEvents accepts 1–10000 events, at most 1048576 bytes calculated as UTF-8
message bytes plus 26 bytes per event. Each message is nonempty. Events within a
batch must be chronological. Events older than 14 days or more than two hours in
the future are individually rejected with `rejectedLogEventsInfo`; valid events
are inserted atomically and may span at most 24 hours. No retention rejection
field is fabricated because retention is unavailable. The deprecated
sequenceToken is ignored, including intentionally incorrect tokens. Repeating a
batch therefore inserts duplicates, matching current AWS behavior. The entire
request fails without inserting valid events if structural/order/size/span,
storage quota or SQL errors occur.

GetLogEvents uses inclusive startTime and **exclusive** endTime. Stored ingestion
time is returned unchanged on subsequent reads/restarts. Selection is by event
time then persistent insertion ID; responses present the selected page in
ascending chronological order. startFromHead=false selects the latest page.
Each page is bounded by 10000 events and 1 MiB including local 26-byte overhead.
Forward/backward cursors point beyond the selected page; a forward cursor
requires startFromHead=true. An exhausted direction returns the same token,
including empty pages. A forward token can observe later events beyond its
ordering boundary. Reversing after an empty boundary page includes the first/last
boundary event. A subsequently ingested event before that boundary requires
a fresh query. This is an ordering cursor, not a snapshot or live-tail stream.

Tokens expire 24 hours after issuance. A page that advances issues new tokens;
an exhausted direction preserves the input token and its existing expiry. They bind the query's region,
resource names, time filters, stream generation and direction using a
database-specific persisted HMAC key. Changing the page limit is permitted;
changing query filters is not. Tokens survive restart but cannot be reused in
another team's database or against a deleted/recreated stream. Group/stream
listings use name order (or LastEventTime with stream-name tie breaking).
Concurrent mutations between listing pages are not a snapshot. Local stream
event metadata updates immediately; AWS LastEventTime metadata can be eventual.

Local per-database quotas are 100 groups, 1000 streams, 100000 events and 64 MiB
message storage. These are simulator bounds, not AWS account quotas. JSON
transport is capped at 8 MiB to allow escaped messages in a valid 1 MiB batch.
DeleteLogGroup atomically cascades all streams/events, including rollback if a
database failure occurs. Team isolation requires separate process/database/token
authorities; selecting a principal is not tenant authentication.

## Integration handoff and validation

`NewCloudWatchLogsStore(db)` owns schema/key initialization. Its HTTP method takes
`(writer, request, region, authorize func(action, resource string) error)`;
the Gateway owns identity resolution and IAM decision headers. `CreateGroupTx`
and `DeleteGroupTx` use a caller's short SQL transaction. A future CloudFormation
adapter must maintain its own ownership claims and reject cross-adoption; these
methods alone do not implement a CloudFormation resource.

Focused tests cover restart/order/cursor convergence, scoped/expired/stale
tokens, timestamp rejection, atomic insert/delete faults, regional boundaries,
strict JSON and infrastructure error preservation. The lifecycle model abstracts
SQL atomicity, time and bytes; it is not a refinement proof. Actual binary SDK,
two-team, UI, source coverage, license and registered formal gates are required
at integration. Until they run, source presence is not a completion claim.

## Sources and reuse

Current AWS references: [PutLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutLogEvents.html),
[GetLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html),
[DescribeLogGroups](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeLogGroups.html),
[DescribeLogStreams](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeLogStreams.html),
[CreateLogGroup](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_CreateLogGroup.html),
[CreateLogStream](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_CreateLogStream.html),
[DeleteLogGroup](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DeleteLogGroup.html),
[service authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html).

Selected wire shapes/lifecycle are adapted from pinned MIT Kumo, with its full
existing license retained. Kumo's retrieval-time ingestion timestamps, random
nonconverging page tokens and inclusive endTime were not copied. LocalStack's
archived Apache source was reviewed; its moto-backed Python implementation was
not imported. Exact paths, revisions and changes are recorded in
`third_party/kumo-cloudwatch-logs.json`.

The public [log group/stream console guide](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/Working-with-log-groups-and-streams.html)
was checked for the group → stream → event navigation and naming. It currently
describes Log Management, group creation, stream selection, expandable messages,
text mode, event filters and absolute/relative time controls. This component
implements the group/stream/event navigation and tables, group/stream creation,
confirmed group deletion, and page navigation. Text-mode switching, expandable
rows, an AWS filter language, AWS navigation layout and absolute/relative time
pickers remain UI gaps. The API supports time bounds, but the initial screen does
not present those as a complete AWS picker. No authenticated AWS screen was
accessed, so pixel/layout identity has not been verified. The explicit ingestion
helper is labeled simulator-only and does not invent an AWS console flow.
At a narrow viewport the event table scrolls inside its own container; the page
itself does not overflow. Message columns retain a readable minimum width.

## Integration and validation boundary

This slice normally incorporates main `0a4ed897f492011bcef0a5d8765f45c200809db9`
into the existing public PR407 ancestry. It retains the separate Azure/GCP
providers, authenticated actor mapping, raw provider paths, startup/logout and
all existing AWS services. The registry preserves all eighteen main models and
ninety-four required sensitivity controls and adds only CloudWatchLogsLifecycle
and CloudWatchLogsViewEpoch: twenty models and one hundred and one controls in total.
Those bounded abstractions do not prove Go, SQLite, token cryptography, HTTP
parsing or complete AWS console fidelity.

The protocol reference retains all sixty-one main OpenAPI operation IDs and
adds seven named Logs root-POST examples with typed request schemas and required
JSON1.1 target/service headers. Capabilities expose exactly seven native Logs
operations. Retention policies, metrics/alarms, CloudFormation Logs resources,
Lambda automatic delivery and additional Challenge workloads remain unsupported.

All twenty-four main mandatory Conformance method bodies remain unchanged; the
Logs SDK and console wrappers add two mandatory cases. The original Logs SDK,
console, native tests and both finite model inputs remain present. Historical
PR407 validation applies to its recorded source only. Current-main integration
requires canonical asset generation, whole-module race/vet/build and both
unchanged ninety-percent coverage floors from one fresh profile, full twenty-six
case E2E/start, exact-head twenty-model/one-hundred-and-one-control validation, cross
builds and accessibility review. None of those new-head runtime results is
inferred from the source merge. At the initial source checkpoint, generated
JavaScript and bundle metadata retain the main baseline pending canonical build.

This work introduces no new Kumo engine, copied upstream implementation or
runtime dependency. Existing pinned MIT attribution and license remain intact.

## Host-owned database fault boundary

Pure SQLite `BEFORE INSERT ... SELECT RAISE(IGNORE)` can return no Exec error
without inserting a row. A native regression reproduced the old false200 for
group/stream writes and a partly retained event batch. The implementation now
requires exactly one affected row for each group/stream/event insert and rolls
back the complete operation if any acknowledgment is missing. Public HTTP errors
remain sanitized ServiceUnavailableException500, distinct from IAM denial. A nil
database passed to the trusted Go constructor returns a configuration error.

The real SQLite fixtures cover all three insert frames, a second-event batch
rollback, exact existing graph preservation, successful repaired retries and
owned-only cleanup. They do not delete a BEFORE trigger's target row or rely on
undefined BEFORE UPDATE/DELETE behavior. These are host-owned DB fault tests,
not a claim that HTTP callers can install SQL triggers or a proof against all
host/filesystem/trigger tampering. SQL/driver fault semantics remain outside the
finite lifecycle model; native regression evidence is separate.
