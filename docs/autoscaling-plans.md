# AWS Auto Scaling plans

## Contract

Five JSON 1.1 operations use the shared regional root POST with
`X-Amz-Target: AnyScaleScalingPlannerFrontendService.<operation>` and
`Content-Type: application/x-amz-json-1.1`: CreateScalingPlan,
DescribeScalingPlans, DescribeScalingPlanResources, UpdateScalingPlan and
DeleteScalingPlan. The wire contract follows the official AWS SDK service model
`autoscaling-plans` 2018-01-06 (signing name `autoscaling-plans`). State lives in
the provider-owned SQLite database of the process; `--state` keeps it across
restarts. No AWS, CloudWatch or EC2 connection is made.

The only scalable targets are **existing local PROVISIONED DynamoDB tables** in
the request region: `ServiceNamespace` `dynamodb`, `ResourceId` `table/<name>`,
and `ScalableDimension` `dynamodb:table:ReadCapacityUnits` or
`dynamodb:table:WriteCapacityUnits`. EC2 Auto Scaling groups, ECS services, Spot
Fleet, Aurora replicas and DynamoDB global secondary indexes are not provided by
this simulator, so those namespaces, dimensions and `table/<name>/index/<index>`
targets return ValidationException. On-demand (PAY_PER_REQUEST) tables have no
capacity units and are rejected. A missing table is rejected rather than accepted
as a plan that cannot apply.

### Local effect

CreateScalingPlan and UpdateScalingPlan validate the complete request, authorize
every action, then in **one SQLite transaction** bound each target table's
provisioned capacity to `MinCapacity..MaxCapacity` (raise a lower value, lower a
higher value, keep an in-range value), store the instructions and record the
table generation. Any failure — invalid input, denied authority, missing or
on-demand table, a target already in another plan, quota or storage fault — rolls
back every write, including capacity changes already made for earlier
instructions. DynamoDB DescribeTable, the SDK and the API reference observe the
same saved capacity.

UpdateScalingPlan replaces only the supplied parts. Supplied instructions replace
the whole instruction list and re-apply limits; removed targets keep their current
capacity. An update with neither ApplicationSource nor ScalingInstructions changes
nothing. DeleteScalingPlan removes the plan and its instructions and does not
revert capacity; the AWS model notes that resources a plan launched must be
deleted separately.

Target tracking configurations are validated, stored and returned as
`TargetTrackingScaling` policies, but **never evaluated**: the simulator has no
CloudWatch metrics, alarms or scaling activities. Capacity changes only at
create/update. This is not dynamic scaling.

### Statuses

Statuses are derived at read time from the stored instruction and the current
DynamoDB table generation. They are synchronous simulator states, not AWS's
asynchronous `CreationInProgress`/`UpdateInProgress` lifecycle, and
ConcurrentUpdateException is never returned.

| Resource status | Meaning |
|---|---|
| `PartiallyActive` | Capacity limits applied; target tracking stored but not evaluated. |
| `Active` | Capacity limits applied and `DisableDynamicScaling` is true. |
| `Inactive` | The table was deleted, or deleted and recreated (new generation); the message says which. A stale plan never changes a recreated table; update the plan to apply it to the new table. |

A plan is `Active` only when every resource is `Active`; otherwise it is
`ActiveWithProblems` with a message pointing to the resources. `StatusStartTime`
is the time of the last successful create or update; a later table deletion
changes the derived status without changing that time. Policy names are
`ScalingPlan-<plan>-<metric type>`; this naming is a simulator choice.

### Validation and limits

Field names are case-sensitive. Unknown fields, duplicate keys, JSON null,
invalid UTF-8, trailing data and wrong JSON types are rejected, never ignored.

- `ScalingPlanName`: 1..128 printable ASCII characters without `|`, `:` or `/`.
  Names are unique per region. `ScalingPlanVersion` must be `1`.
- `ApplicationSource`: exactly one of `CloudFormationStackARN` (a stack ARN in the
  request region and account `000000000000`) or 1..50 `TagFilters` (key 1..128,
  0..20 distinct values of 1..256 characters, keys not repeated). One plan per
  source per region; tag filters compare order-insensitively. The source is the
  plan's identity only: the simulator does **not** discover resources from stacks
  or tags or check that targets belong to the source. List every target in
  ScalingInstructions.
- `ScalingInstructions`: 1..50 per plan; one target/dimension appears once per
  plan and in at most one plan. `MinCapacity` and `MaxCapacity` are integers
  1..40000 with `MaxCapacity >= MinCapacity` (the upper bound is a simulator
  limit).
- `TargetTrackingConfigurations`: at least one unless `DisableDynamicScaling` is
  true; metrics must be unique, so effectively one per instruction. Only
  `PredefinedScalingMetricSpecification` with `DynamoDBReadCapacityUtilization`
  for the read dimension or `DynamoDBWriteCapacityUtilization` for the write
  dimension. `TargetValue` is a finite number within ±2^360; `ScaleInCooldown` and
  `ScaleOutCooldown` are 0..2147483647; `DisableScaleIn` is boolean.
  Customized metrics, `ResourceLabel` and `EstimatedInstanceWarmup` are rejected.
- Predictive-scaling fields (`PredefinedLoadMetricSpecification`,
  `CustomizedLoadMetricSpecification`, `ScheduledActionBufferTime`,
  `PredictiveScalingMode`, `PredictiveScalingMaxCapacityBehavior`,
  `PredictiveScalingMaxCapacityBuffer`) are rejected: predictive scaling applies to
  EC2 Auto Scaling groups. `ScalingPolicyUpdateBehavior` is accepted and stored;
  no external policies exist locally, so both values behave the same.
- Simulator quotas: 100 plans per database across regions
  (LimitExceededException), 256 KiB request transport, 32 JSON nesting levels.

DescribeScalingPlans takes 1..10 `ScalingPlanNames` (optionally with
`ScalingPlanVersion`) or 1..10 `ApplicationSources`, never both; unknown names are
omitted. Results are in name order. DescribeScalingPlanResources returns
instructions in stored order; an unknown plan returns ValidationException, which
is the operation's documented error type. Both use `MaxResults` 1..50 (default
50). `NextToken` is a live position bound to region and filter, or to the plan
generation for resources (a random identifier assigned at creation, so a plan
recreated with the same name in the same instant is still distinct); tokens for
another filter, region or recreated plan return InvalidNextTokenException. Tokens are standard search bounds, not authority:
every page is reauthorized, and changes between pages may change results.

GetScalingPlanResourceForecastData returns HTTP 501 UnknownOperationException:
forecasts need EC2 Auto Scaling groups and CloudWatch load history. Other unknown
targets also return 501.

Errors are JSON `{"__type": code, "message": text}` with `X-Amzn-Errortype`.
ValidationException, ObjectNotFoundException (Update/Delete of a missing plan),
LimitExceededException and InvalidNextTokenException return 400,
AccessDeniedException returns 403 and storage faults return a sanitized 500
InternalServiceException.

## Authority and composition

Integration interface: `NewAutoScalingPlansStore(db *sql.DB, dynamo
*DynamoDBStore)` requires the DynamoDB store of the same database, and
`store.ServeHTTP(w, r, region, authorize func(action, resource string) error)`.
The Gateway captures **one immutable caller policy** before dispatch and uses it
for every callback. Server token middleware remains in front; identity selection
is not authentication or team isolation. Hosts allocate separate processes,
SQLite databases and token authorities per team.

Scaling plans have no ARN in the service model, so every operation requires
`autoscaling-plans:<operation>` on `*`. AWS applies DynamoDB changes through a service-linked role; there is no such role
locally. So CreateScalingPlan and UpdateScalingPlan additionally require the
**caller's** `dynamodb:DescribeTable` and `dynamodb:UpdateTable` on
`arn:aws:dynamodb:<region>:000000000000:table/<name>` for every target in the
supplied instructions. This prevents a principal with only scaling-plan permission
from changing table capacity. All authorization completes before any table or plan
is read or written, so a denied caller cannot learn whether a table exists.

Optional SigV4-shaped requests must use dummy TCSIM access keys and
`<region>/autoscaling-plans/aws4_request`; signatures are not verified as AWS
credentials. Session tokens, conflicting `X-Simulator-Service`/region selectors
and duplicate routing headers are rejected. Startup refuses orphaned plan
resources without modifying them.

Not included: Cloudscape console, CloudFormation resources, the Lambda execution
role SDK bridge, predictive scaling, forecasts, resource discovery, scaling
activities and EC2/ECS/RDS/Spot targets. The API reference documents the five
operations under **AWS Auto Scaling plans**; its templates assume a PROVISIONED
table named `autoscaling-practice`.

## Verification

- Go: `go test ./internal/aws -run TestAutoScalingPlans` and
  `go test ./internal/server -run TestAutoScalingPlansComposition` (capacity
  bounds, atomic rollback, authority order, validation, pagination, derived
  status, storage faults, restart, participant tokens and two teams).
- SDK: `test/e2e/autoscaling_plans.py` with boto3 against two authenticated CLI
  processes, including the served templates and an actual reopen.
- Formal: `formal/AutoScalingPlanTargets.tla` is a finite two-team model of
  capacity bounds, ownership, authority, rollback and team frames with eleven
  mutation controls. It is bounded checking, not a proof of the Go/SQL code.

## References

- AWS SDK service model `autoscaling-plans` 2018-01-06 as shipped in botocore
  1.41.6 (`scripts/requirements-test.txt`): operations, shapes, enums, required
  fields, errors and documentation strings.
- The documentation host was not reachable from the implementation session, so
  the links below are the official sources to recheck, not fetched evidence:
- [AWS Auto Scaling API Reference](https://docs.aws.amazon.com/autoscaling/plans/APIReference/Welcome.html)
  and [endpoints](https://docs.aws.amazon.com/general/latest/gr/autoscaling_region.html).
- [Actions, resources and condition keys for AWS Auto Scaling](https://docs.aws.amazon.com/service-authorization/latest/reference/list_awsautoscaling.html).
