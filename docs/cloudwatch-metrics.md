# CloudWatch Metrics — bounded local Query API

This document describes the Metrics-only integration candidate. It is not a release or an acceptance result. The three actions share the existing regional AWS endpoint; adding their OpenAPI examples does not register a handler, grant permission or establish full AWS compatibility. New combined SDK, console, reference, persistence and team-isolation acceptance must run against the final integrated binary.

Use the existing regional AWS root POST endpoint. `/_simulator/providers/aws/` also routes the same native protocol in the production adapter; it does not introduce three operation paths. Set `Content-Type: application/x-www-form-urlencoded`, `X-Simulator-Service: monitoring` and a supported `X-Simulator-Region`, such as `us-east-1`. **Clear `X-Amz-Target` and any previous service's Authorization header before Send.** Alternatively use dummy TCSIM SigV4 metadata for service `monitoring` with a matching Region. Choosing an example does not automatically change Scalar's independent request headers.

Browser requests require the local signed-in session; other clients require the host-issued `X-Simulator-Token`. Dummy AWS credentials and service/Region headers select a protocol, never grant permission. The selected exercise identity must allow the exact `cloudwatch:PutMetricData`, `cloudwatch:ListMetrics` or `cloudwatch:GetMetricStatistics` action on Resource `*`. Explicit Deny wins, and an operator selecting a reader keeps that reader's permissions. Policies are read inside the borrowed operation transaction after bounded form parsing, before semantic datum handling; this is not a before-body frozen policy snapshot.

## Three actions

All forms carry `Version=2010-08-01` and the exact Action. The pinned boto3 model uses Query/XML and signing service `monitoring`. Current AWS documentation also displays JSON examples; JSON/`X-Amz-Target` Metrics transport is unsupported in this local slice.

- `PutMetricData`:1–1000 raw `Value` records, ≤30 complete dimensions, optional standard `StorageResolution=60`, finite bounded values including zero/negative, timestamps14days past through2hours future. Omitted Timestamp uses trusted current time. Duplicate samples append independently. Values/Counts arrays, statistic sets, entities, resolution1 and `AWS/` namespace emission are unsupported.
- `ListMetrics`: optional exact Namespace/MetricName, up to10 subset dimension filters and opaque NextToken, up to500 results/page. Discovery requires a series ingested within14days and at least one point retained within15days of the captured page time; this is distinct from raw-sample retention for statistics. There is no Limit/MaxResults/RecentlyActive/linked-account support or simulated AWS eventual-visibility delay.
- `GetMetricStatistics`: exact full dimensions; SampleCount,Sum,Minimum,Maximum,Average; minute-rounded inclusive start/exclusive end, period multiple60, ≤1440 requested buckets and returned unit/time pairs. Omitted Unit returns separate recorded-unit streams; explicit Unit filters exactly. Missing data is empty, not a fabricated zero. No conversion, percentiles, rollups, metric math, GetMetricData/Insights or alarms.

The Put template writes one sample in `Simulator/Reference`, `Requests`, `Stage=local`, Value7, UnitCount. It is not live saved state. For the Statistics template, replace the explicit StartTime/EndTime placeholders with a recent RFC3339 UTC/offset window before Send. Unsubstituted placeholders fail400. XML result examples and RequestId are illustrative, not the last request or captured data. The new oneOf alternatives preserve the exact original generic form as their first branch; its Action enum is disjoint from the three Metrics actions. Native validation remains authoritative for contiguous list indices, scalar spelling, current time windows, quotas and cross-field constraints that JSON Schema cannot fully express.

## Local bounds and errors

Physical wire and expanded Query bodies each have an independent inclusive1MiB cap; output is bounded at2MiB. Gzip accepts one complete member only; malformed/truncated/CRC-invalid/second-member/trailing data and other encodings are refused. Go1.26's10000 raw Query-token guard remains active. These are local bounds, not proof that AWS uses the same decompression policy.

Native XML uses `http://monitoring.amazonaws.com/doc/2010-08-01/`. Put returns ResponseMetadata only; List has Metrics/member and optional NextToken; Statistics has Label and Datapoints/member, Unit and only requested basic fields. Requested numeric zero remains present. Native errors include400 MissingParameter/InvalidParameterValue/InvalidParameterCombination/SimulatorQuotaExceeded,403 AccessDenied,404 UnknownOperationException,413 RequestEntityTooLargeException and500 InternalServiceError. Outer local Access401 can be JSON before the native Query parser. No raw SQLite/policy information is returned by the native storage error.

Local retention is15days of raw samples without AWS long-term rollups. Reads never collect data. An authorized committed Put can atomically collect strictly expired rows only in its Region/namespace; peer expired rows still consume physical budget. Defaults are1000series/100000samples/64MiB logical payload/64Regions, not AWS quotas or SQLite file-size bounds. Tokens bind Region/options/revision/asOf/position, expire after1hour, survive sameDB restart and become stale after a successful Put in that Region. No DeleteMetric API is invented; development acceptance disposes of its own temporary processes/databases.

Automatic service/Lambda metric delivery, dashboards, CloudFormation Metrics and full AWS compatibility are not implemented. No authenticated AWS console comparison, full-cloud authentication, mathematical proof of the complete implementation or new SDK/reference verification flag is claimed by this change.

Primary references: [PutMetricData](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_PutMetricData.html), [ListMetrics](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_ListMetrics.html), [GetMetricStatistics](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_GetMetricStatistics.html). The operation links define the comparison scope; simulator-specific limits above come from the native implementation. Local tests are not differential execution against real AWS.

## Try the examples safely

1. Run the final integrated simulator on loopback, sign in, and open `/_simulator/docs/`. Select **AWS → CloudWatch Metrics**, then the shared AWS endpoint. The console uses **AWS → CloudWatch Metrics → All metrics** for read-only discovery/statistics; publish with a local API client.
2. Select the URL-encoded form media type and `CloudWatchPutMetricData`. Keep the form's native `Action` and `Version`, set the protocol headers above, and attach only your local simulator credential. Send writes the sample to your process/database/Region.
3. Select `CloudWatchListMetrics` to discover the series. For `CloudWatchGetMetricStatistics`, replace StartTime and EndTime with a recent UTC window enclosing your sample; copy its exact dimensions. Inspect the actual response panel, not the static XML examples.
4. A read-only exercise identity should receive `403 AccessDenied` on Put while permitted reads still work. A missing or expired simulator credential is a separate outer authentication failure. Hosts must allocate separate process/database/token authorities to separate teams; Region selection alone is not isolation.

The reference helper requires real Send responses and native-state cross-checks at desktop and mobile widths. Its strict accessibility check currently exposes an unresolved vendor combobox issue in retained diagnostic evidence; source examples and pure schema tests do not make that browser gate pass. No vendor assets or accessibility assertions are weakened by this reference change.

## Source contract and checks

The public baseline has 66 operation IDs and 36 paths. Metrics adds three form schemas (121 schemas total), three named form examples and the reviewed XML responses to the existing root POST. The closed inverse removes only those exact reviewed additions and requires canonical equality to the complete immutable public baseline. It refuses missing/renamed/duplicate cloud groups, changed old operations, schemas, authentication/routing fields and unknown additions. The static examples are also checked against the real JSON Schema branches.

The historical Metrics-only source `7246b71` retained the then-public 62 tests
and added three Metrics tests, requiring 65 E2E cases. The new private integration
preserves all 65 tests from main `3a9b0ce` and adds those separate native
SDK/two-process, console and served-reference Metrics tests: **68 required E2E
cases**. This is a source inventory, not a successful run. Source presence and
pure detector success do not establish execution of those flows. Build,
race/coverage, all 649 combined formal outcomes, whole E2E, review and exact-HEAD
hosted CI remain integration gates. The earlier 615-case formal attempt failed
at case 449 after 448 successes, leaving 166 unrun; an isolated case-449 success
does not establish aggregate acceptance.
