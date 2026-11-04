# CloudWatch Logs: bounded native API

This increment adds CloudWatch **Logs**, not CloudWatch metrics or alarms. Its API
and console use provider-owned SQLite tables. It does not deliver Lambda logs
automatically, provision an agent, enable a Challenge problem, or claim visual
parity with an authenticated AWS console. The isolated Cloudscape component must
be connected by the integration owner before the console is available.

## Native protocol and supported operations

Use the authenticated AWS provider root endpoint, JSON 1.1 POST, and
`X-Amz-Target: Logs_20140328.<operation>`. The signing service is `logs`. Only
dummy `TCSIM` credentials or unsigned simulator requests are accepted, alongside
the local/team access token. Real AWS signatures and session credentials are not
validated. Region comes from the trusted Gateway's regional scope and must be one
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
ordering boundary; a subsequently ingested event before that boundary requires
a fresh query. This is an ordering cursor, not a snapshot or live-tail stream.

Tokens expire 24 hours after the first page. They bind the query's region,
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
