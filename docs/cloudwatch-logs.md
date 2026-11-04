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

## Main integration status (source-only)

The clean historical candidate c005842 is preserved in its original worktree.
A separate maintenance worktree normally merges recorded main
`b61d0299a2139661f01ef29ae5826e4c070cc7bf`, retaining SQS, startup, Scalar docs,
SSM and the separate browser-session/logout implementation plus all ten main
models/thirty-six controls. Its seven native Logs operations remain the existing
implementation; no new Kumo engine/dependency or upstream copy is introduced.
Existing pinned MIT attribution/license remains intact.

The two Logs models/seven controls are now declared in the combined twelve-model,
forty-three-control registry with protected-removal tests. This source change is
not a TLC run or an implementation proof. The merged JSX retains the Account menu
and logout and adds the Logs entry/view. Canonical embedded JavaScript/metadata have now been rebuilt using an exact-byte
copy of the existing reviewed same-lock vendor tree (no install/new dependencies).
The actual rebuilt single binary passed PC/390px, five scoped axe screens, real
keyboard/native-state and delayed-response display checks with no outside calls.
This proves the bounded console flow, not full AWS screen fidelity. Original
generated outputs were backed up before the rebuild.

Focused current-source native checks now pass: all 15 TestLogs cases normally
(1.566s) and with race (10.812s), selected-package vet, offline standalone build,
and boto3 1.42.0 seven-operation lifecycle with 30 calls/sends/responses. Both
teams use separate persistent SQLite databases; checks cover region/token/IAM
rejection, original ingestion/cursor survival after restart, recreated-stream
token rejection, native cascade deletion and persisted peer cleanup. No browser
or full-module result is inferred from that focused proof.

The shared root POST OpenAPI mapping preserves all 43 operation IDs/25 paths,
adds seven exact Logs examples/request schemas, and names the required target,
service and JSON1.1 headers. Actual served examples were exercised through HTTP
with the explicitly documented current-clock substitution for PutLogEvents; SDK
readback confirmed the same SQLite bytes. Capabilities enumerate only those seven
operations, with metrics/alarms/CFN/automatic Lambda delivery false.

The working-tree registered suite passed all 12 baselines/43 intended mutants,
plus 18 policy and 21 applicability unit tests. Its 75 pre/post source hashes were
unchanged for that run; subsequent capabilities metadata and the insert-ack fix
mean this must not be represented as a new immutable-HEAD result.

The current local mainb61 integration passed whole-module race/vet/assets/build,
statement6192/6533 (94.78%) and unfiltered source8075/8966 (90.06%) from one
fresh coverage profile, all23 SDK/UI E2E cases, actual built/unbuilt make-start,
five target cross-builds and50 automated accessibility/keyboard screens. The
initial source89.63% failure was retained separately; no profile union, floor
change or exclusion was used. Four new persisted-state recovery/boundary tests
verify sanitized read failures, retained damaged data and repair, corrupt cursor
key refusal without rotation, global stream quota maintenance and transport/
missing-stream rejection. Host-owned SQL corruption is not an HTTP exploit.

Final immutable applicability/formal receipts must bind the resulting local
integration HEAD. Hosted CI and current canonical vulnerability clearance remain
pending; this candidate is not public main and all public merges remain on HOLD.
Historical c005842 focused/SDK/browser/finite-model results cannot be
used as a new-head pass. Metrics, alarms, CFN Logs resources, Lambda delivery and
additional Challenge workloads remain unsupported.

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
