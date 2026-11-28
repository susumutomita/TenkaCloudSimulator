# Elastic Load Balancing (Application Load Balancer, Lambda targets)

## Contract

Thirteen Elastic Load Balancing v2 Query/XML operations (`Version=2015-12-01`)
are served at the regional root endpoint: CreateLoadBalancer,
DescribeLoadBalancers, DeleteLoadBalancer, CreateTargetGroup,
DescribeTargetGroups, ModifyTargetGroup, DeleteTargetGroup, RegisterTargets,
DeregisterTargets, DescribeTargetHealth, CreateListener, DescribeListeners and
DeleteListener. Every other official ELBv2 operation, and the Classic Load
Balancer API (`2012-06-01`), returns `InvalidAction`. Requests are routed by
`X-Simulator-Service: elasticloadbalancing` or a dummy `TCSIM` SigV4 scope for
`elasticloadbalancing`; signatures are not verified. Every field must be
consumed by the operation: unknown or unsupported fields, such as `Tags`,
`SubnetMappings`, `SslPolicy` or `Include`, return `ValidationError` instead of
being ignored. State is provider-owned SQLite, one database per team process.

The supported slice is an Application Load Balancer whose HTTP listeners
either return a fixed response or forward to one target group holding one
Python Lambda function. That is the only target with a real bounded backend in
this simulator. Instance, IP and ALB targets require target registration and forwarding integrations beyond this exercise. Other `TargetType` values return `InvalidConfigurationRequest`.

## Resources

### Use the Console

Open **Elastic Load Balancing** in the AWS Console and choose the region of
your load balancer. Filter by its exact name or load another page. Its detail
view reads the saved VPC, subnet IDs and Availability Zones, security groups,
listeners and linked target groups. Older ID-only placements display the
available IDs. Under a linked Lambda target group, the Console shows the
registered function, target state and reason returned by `DescribeTargetHealth`.

Edit **Health check path** to submit one `ModifyTargetGroup` request for that
group. Saving requires the named target-group action on its ARN and the
Describe permissions needed to verify the saved path and current health. The Console
checks the selected load balancer association before sending the change and again
in the returned group and fresh read. The native write addresses the target group
ARN. Another caller can move the group between these requests.
If the acknowledgement is lost, choose **Refresh affected target group** to
inspect its current state before making another change. A disabled health
check can retain a path. Exercise the authenticated listener separately to
observe the actual response. [ModifyTargetGroup](https://docs.aws.amazon.com/elasticloadbalancing/latest/APIReference/API_ModifyTargetGroup.html)

## Use the API reference

After signing in, open **API Reference → Shared AWS JSON/Query endpoint**.
Select `application/x-www-form-urlencoded` and one of the thirteen **ELBv2**
examples. In **Test Request**, select the `elasticloadbalancing` value for
`X-Simulator-Service`, clear `X-Amz-Target`, and choose the region that
contains your resources. Include the form fields for that operation. All
ELBv2 examples use `Version=2015-12-01`.

`CreateTargetGroup` can create a disposable Lambda target group in an empty
region. Copy its `TargetGroupArn` from the XML response, then use it in
`DescribeTargetHealth`. A registered function and its listener let you
inspect the target's actual status. For `CreateLoadBalancer`, paste two
subnet IDs and a security group ID from the same VPC. Paste the returned
load balancer ARN into `DescribeLoadBalancers` to inspect its zones and VPC.
The `REPLACE_WITH_…` fields are editable placeholders. Clean up using the
ARNs of your disposable resources, removing listeners before target groups.

- **Load balancers** are `application` only, `internet-facing` (default) or
  `internal`, `ipv4` only. Names are 1 to 32 alphanumeric characters or
  hyphens, unique per region. Create the EC2 VPC, subnets and security groups
  first. Select two or three native subnets, one per local Availability Zone,
  and one to five explicit native security groups in that same regional VPC.
  The simulator's VPC creation has no default security group. A missing subnet
  returns `SubnetNotFound`. Repeated zones, mixed VPCs and CloudFormation-owned
  subnets return `InvalidSubnet`. An unknown, omitted, foreign or stack-owned
  security group returns `InvalidSecurityGroup`.

  Create and Describe report the saved `VpcId` and each `SubnetId` with its
  actual `ZoneName`. The placement and dependency rows commit together. EC2
  returns `DependencyViolation` for deletion of a subnet or group still in
  use. Delete the ALB first to release its references. ALBs created by older
  simulator versions retain their original subnet IDs and ARN on read, identical
  create retry and deletion. Their original records contain no verified VPC
  or zone and acquire no EC2 references during upgrade.

  The state is immediately `active`. The synthetic `DNSName` has the form
  `name-NNNNNNNNNN.region.elb.simulator.invalid`. Exercise requests use the
  local HTTP data-plane route. This placement records configuration only.
  DNS resolution, ENIs, IP allocation, free-IP accounting, security-group
  traffic decisions and external reachability are outside its scope. An
  identical create returns the existing resource; different settings for the
  same name return `DuplicateLoadBalancerName`.
- **Target groups** use `TargetType=lambda`. `Protocol`, `Port`, `VpcId`,
  `ProtocolVersion`, `HealthCheckProtocol`, `HealthCheckPort` and
  `IpAddressType` do not apply and are refused. Lambda defaults: health checks
  disabled, path `/`, interval 35 s (5–300), timeout 30 s (2–120, smaller than
  the interval), healthy and unhealthy thresholds 5 (2–10), matcher `200`
  (codes or ranges from 200 to 499). ModifyTargetGroup changes only these
  health settings; re-applying identical settings is not a change. A target group is used by at most one load balancer
  (`TargetGroupAssociationLimit`); one in use by a listener cannot be deleted
  (`ResourceInUse`). Deleting a missing target group or load balancer succeeds.
- **Targets** are one unqualified same-region Lambda function ARN per target
  group (`TooManyTargets` otherwise). `AvailabilityZone` may only be `all`;
  ports are refused. The function must exist (`InvalidTarget`) and its resource
  policy must allow `elasticloadbalancing.amazonaws.com` to call
  `lambda:InvokeFunction` with an `AWS:SourceArn` matching the target group
  (`AccessDenied`, as AWS reports for this case). Grant it with Lambda
  AddPermission, whose `SourceArn` may be a same-region target group ARN or an
  ArnLike pattern over target groups. Deregistration is immediate (no draining
  delay); deregistering an unregistered target succeeds.
- **Listeners** are `HTTP` only (`UnsupportedProtocol` for HTTPS, which needs
  certificates). One default action: `forward` to one target group (by
  `TargetGroupArn` and/or a one-entry `ForwardConfig`, positive weight, no
  stickiness) or `fixed-response` (2XX/4XX/5XX, documented content types,
  body up to 1024 bytes). Redirect, authentication and JWT actions return
  `InvalidLoadBalancerAction`. Rules (CreateRule and the rest) are not served.
  Deleting a load balancer deletes its listeners in the same transaction.

Describe operations require their named resources to exist
(`LoadBalancerNotFound`, `TargetGroupNotFound`, `ListenerNotFound`), return a
stable order (names, then load balancer and port) and page with `PageSize`
1–400. `NextMarker` is bound to the operation, region and filter and names the
last returned item; it is a live position, not a snapshot or an authority.

Local limits: 50 load balancers and 100 target groups per region, 10 listeners
per load balancer, one target per target group, 64 KiB Query requests. These
are exercise limits, not AWS quotas.

## Data plane

`/_elb/{region}/{load-balancer-name}/{port}/{path}` (also under
`/_simulator/providers/aws/`) sends a request to the listener on that port.
Host token authentication precedes it and no IAM action applies, as for a real
listener. GET, HEAD, POST, PUT, PATCH, DELETE and OPTIONS are accepted.

A fixed-response listener returns its status and body. A forward listener
invokes the registered function with the documented Lambda-target event
(`requestContext.elb.targetGroupArn`, `httpMethod`, `path`,
`queryStringParameters`, `headers`, `body`, `isBase64Encoded`) with
multi-value headers disabled: duplicate names keep the last value and query
names and values are passed **without URL decoding**. Non-UTF-8 bodies are
base64-encoded. `host`, `x-forwarded-for`, `x-forwarded-proto` and
`x-forwarded-port` are added. Cookies, `X-Simulator-*` headers, SigV4
`Authorization` and session tokens never reach the guest. The function runs
with its saved execution role, region, timeout and the bounded SDK bridge, the
same as Lambda Invoke. The guest context carries no value from the incoming
request, so a caller's SDK session policy or identity never replaces the
execution role in guest SDK calls. The response must be one JSON object with
`statusCode` and optionally `statusDescription`, `headers`, `body` and
`isBase64Encoded`, each spelled exactly and at most once; any other member
(including `multiValueHeaders`) makes it malformed.

Status codes follow the load balancer's documented behavior where it is
defined: no registered target is 503; a function error, timeout, malformed or
oversized response, missing function or missing ELB invoke permission is 502.
A simulator storage or runtime failure is 500.
An unknown load balancer or a port without a listener is 404, because there is
no network endpoint to refuse a connection. Bodies and events are limited to
the simulator's 256 KiB Lambda event size, not the AWS 1 MB limit. As with
the local API Gateway proxy, responses are non-executing downloads: only
`ETag`, `Content-Language` and `X-App-*` headers are passed and the content
type is replaced, so exercise output never runs on the console origin.

## Health checks

When a health-enabled target group is used by a listener and has a registered
function, the process invokes the function with the documented health check
event (`GET` of `HealthCheckPath`, `user-agent: ELB-HealthChecker/2.0`) every
`HealthCheckIntervalSeconds`, measured from the start of its previous check,
one check at a time. A check succeeds when the
response status matches the matcher. Consecutive results move the target
between `initial` (`Elb.InitialHealthChecking`), `healthy` and `unhealthy`
with `Target.ResponseCodeMismatch` ("Health checks failed with these codes:
[N]"), `Target.Timeout` (no answer within `HealthCheckTimeoutSeconds` when
that is not longer than the function timeout), `Target.FailedHealthChecks`
(function error or timeout, malformed response, missing function, permission
or trusted role) or `Elb.InternalError` (simulator storage or runtime failure).
A target group without listeners reports `unused`/`Target.NotInUse` and is not
checked; disabled checks report `unavailable`/`Target.HealthCheckDisabled`;
a requested unregistered target reports `unused`/`Target.NotRegistered`.

Registration, listener and health-setting changes start a new health epoch
and restart from `initial`. The due checks are selected first; the function,
its resource policy and execution role are then read in one transaction that
closes before the guest runs; the result is recorded only if the epoch and
registration are still unchanged, so a stale result cannot overwrite a newer
configuration. If the health configuration cannot be read, recorded health is
left unchanged and the pass is retried on the next one-second tick; there is
no separate error log. A single unhealthy Lambda target still receives traffic
(the load balancer fails open). Health checks stop when the process stops;
time spent stopped is not replayed. There are no CloudWatch metrics, access
logs or anomaly detection.

## Authority and composition

`NewELBStore(db *sql.DB)` owns the tables; the gateway captures **one immutable
caller policy** before parsing and evaluates every action with it. Actions and
resources follow the service authorization reference:

| Action | Resource |
|---|---|
| CreateLoadBalancer, DeleteLoadBalancer, CreateListener | `arn:aws:elasticloadbalancing:REGION:000000000000:loadbalancer/app/NAME/ID` (an idempotent create uses the existing ARN) |
| CreateTargetGroup, ModifyTargetGroup, DeleteTargetGroup, RegisterTargets, DeregisterTargets | `arn:aws:elasticloadbalancing:REGION:000000000000:targetgroup/NAME/ID` |
| DeleteListener | `arn:aws:elasticloadbalancing:REGION:000000000000:listener/app/LB-NAME/LB-ID/ID` |
| DescribeLoadBalancers, DescribeTargetGroups, DescribeListeners, DescribeTargetHealth | `*` |

All mutation authorization completes before persistence and each mutation
commits atomically. Tags are refused, so the dependent
`elasticloadbalancing:AddTags` permission is never needed. Identity selection
is not authentication or team isolation: hosts allocate a separate process,
SQLite database and token authority per team.

## Dependencies

| Dependency | Use | Status |
|---|---|---|
| AWS Lambda (functions, resource policies, execution role, SDK bridge) | Required for forward targets and health checks | Implemented; reused unchanged except the ELB service principal |
| Amazon VPC subnets and security groups (#56) | Required for new regional ALB placement | Verified native subnet zones and explicit security groups in one VPC; deletion follows stored dependencies. |
| Amazon EC2 instances, IP targets | Instance/IP target types | Not implemented; refused |
| AWS Certificate Manager | HTTPS listeners | Not used; HTTPS refused |
| AWS WAF, Route 53 alias records, S3 access logs, CloudWatch metrics | Optional integrations | Not connected |
| CloudFormation `AWS::ElasticLoadBalancingV2::*` | Optional provisioning | Not supported |

## References

- [Elastic Load Balancing API Reference](https://docs.aws.amazon.com/elasticloadbalancing/latest/APIReference/Welcome.html); the operation, shape, error code and HTTP status contract was fixed from the official botocore 1.41.6 `elbv2/2015-12-01` model already pinned by this repository.
- [Lambda functions as targets](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/lambda-functions.html): event, response, permission and health check behavior.
- [Target group health checks](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/target-group-health-checks.html): states, reason codes and fail-open routing.
- [Actions, resources and condition keys for Elastic Load Balancing V2](https://docs.aws.amazon.com/service-authorization/latest/reference/list_elb-v2.html).
- [Elastic Load Balancing endpoints and quotas](https://docs.aws.amazon.com/general/latest/gr/elb.html).
