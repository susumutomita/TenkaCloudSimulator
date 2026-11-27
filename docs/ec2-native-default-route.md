# Native IPv4 route configuration

This is a bounded local EC2 network configuration operation, not a packet path.
Each **native** route table can contain up to 16 explicitly saved IPv4
routes, optionally including a default route. Every route targets the
**native internet gateway currently attached to the table's native VPC**
in the same local region. A non-default route lets you describe a narrow
outside-VPC destination without adding a catch-all route. New EC2-owned VPCs
also have a main table, and each of their tables includes a read-only `local`
route for the VPC CIDR. That row does not consume an explicit route slot.
Creating an attachment or subnet association adds no gateway routes. Existing
VPCs without a main table and CloudFormation ownership are unchanged.

Use the usual authenticated local Query endpoint and EC2 API version
`2016-11-15`. Use a local simulator URL and a host-issued simulator token in SDKs;
dummy SDK signing credentials do not authenticate. Scoped instance sessions
cannot read or mutate these route tables.

| Operation | Bounded contract |
| --- | --- |
| [CreateRoute][create] | Send one `RouteTableId`, IPv4 `DestinationCidrBlock` and `GatewayId`. The destination is canonicalized; for example, `198.51.100.19/24` becomes `198.51.100.0/24`. Detached and other-VPC gateways are refused. A duplicate canonical destination returns `RouteAlreadyExists` without replacing it. |
| [DescribeRouteTables][describe] | Each saved route appears with canonical `DestinationCidrBlock`, the target `GatewayId`, `Origin=CreateRoute` and `State=active`. This means validated, saved configuration only. Use a single exact `RouteTableId.1` (SDK `RouteTableIds=[id]`), an explicit `association.subnet-id` filter, or no selector. The exact selector has at most one response row; a missing table returns `InvalidRouteTableID.NotFound`. The full regional consistency check occurs before selecting any output. Exact-ID readback is unpaged and cannot be combined with a filter. The [regional list](ec2-read-discovery.md#route-table-readback) can be paged with or without an explicit subnet filter. |
| [DeleteRoute][delete] | Send the exact `RouteTableId` and exact canonical `DestinationCidrBlock`. No target parameter. Removes only the saved gateway route for that destination; other routes stay unchanged. An absent route returns `InvalidRoute.NotFound`. The managed `local` route cannot be removed and returns `InvalidParameterValue`. |

For writes, the [EC2 action][iam] must be allowed on the exact
`arn:aws:ec2:<region>:000000000000:route-table/<id>`. Ordinary
`ec2:DescribeRouteTables` is required on `*`, even with the exact selector.
`DryRun=true` for the two mutations checks permissions only, returns
`DryRunOperation` or `UnauthorizedOperation` and never reads or changes route
state. The native table, VPC, linked gateway, typed claims and regional stack
declarations must agree within the same SQLite transaction. Failures, including
quota or unsuccessful post-write state verification, cannot acknowledge a
change. Each native route consumes one row of the existing shared 1,000-network-row
bound. The simulator also limits a native table to 16 explicit IPv4 routes,
not the [AWS quota][quotas]. A valid request beyond either local bound gets
`RouteLimitExceeded`; after deleting a route, the released slot can be reused.
Delete **all** saved gateway routes referring to a table or gateway before deleting the table
or detaching the gateway. Either attempted removal while referenced
returns `DependencyViolation`. A native table must also be disassociated from
any subnet before deleting it. The main table itself can only be removed with
its parent VPC.

A non-default destination must be disjoint from the native VPC's IPv4 CIDR
and the reserved `169.254.168.0/22` service range ([AWS rules][local-ranges]).
This is intentionally narrower than AWS: a non-default supernet covering
either range is rejected too. Different permitted external routes may overlap
one another; the simulator stores each exact canonical prefix independently
and does not perform longest-prefix forwarding. Existing saved IPv4 default
routes keep the same durable identity and work without migration.

[ReplaceRoute][replace], IPv6/prefix-list destinations,
other targets, gateway/main route-table associations and propagation are
outside this slice. There is no alternate eligible gateway in this slice to
replace a saved target with. It does not make a NIC, assign public addresses,
forward packets, or validate internet connectivity. Do not downgrade a
database containing a saved native route to an older binary that treats those
records as invalid. See [EC2 error codes][errors] for names; these are the
simulator's bounded refusal rules, not full AWS compatibility.

## Console, read-only reference, and local acceptance

In an ordinary signed-in **VPC** console, select an EC2-managed VPC and its
native **Route tables** row. Choose **Add IPv4 route** and enter a CIDR. The
dialog previews normalization and discovers only the current native attachment;
the server rechecks that relationship. Choose **Remove IPv4 route**, choose
the saved destination and confirm that table ID. The API removes just that
canonical destination (no server-side conditional revision or target match).
Console shows success only after a fresh exact-table readback confirms the
entire expected route set, including siblings, in the same browser scope.
Missing siblings, a different table, wrong target, damaged reply or changed
scope cannot confirm success. Refresh after uncertainty instead of replaying a
mutation. This exact confirmation is bounded independently of regional inventory size. Browsing uses bounded regional pages; the exact confirmation does not page the inventory again.

The shared read-only Scalar example **EC2 DescribeRouteTables** starts with
`MaxResults=100`. Remove pagination fields before specifying `RouteTableId.1`
to verify one saved table after using the SDK or Console. There
are no mutation presets.

```sh
make build
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" python3 test/console/ec2_native_default_route_sdk.py
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" JSDOM_MODULE=/path/to/jsdom \
  python3 test/console/ec2_native_default_route_sdk.py --console
```

The focused test uses the pinned AWS SDK, separate local team databases, and a
process restart. The separate Console check drives the served bundle using
JSDOM and the real local API, including a successful response missing a sibling; it does not assert Chromium layout or real AWS
routing. See the [AWS console workflow][workflow] for the reference design
rather than expecting a complete AWS console clone.

[create]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRoute.html
[describe]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeRouteTables.html
[delete]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteRoute.html
[replace]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ReplaceRoute.html
[iam]: https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonec2.html
[errors]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/errors-overview.html
[local-ranges]: https://docs.aws.amazon.com/vpc/latest/userguide/subnet-route-tables.html
[quotas]: https://docs.aws.amazon.com/vpc/latest/userguide/amazon-vpc-limits.html#vpc-limits-route-tables
[workflow]: https://docs.aws.amazon.com/vpc/latest/userguide/WorkWithRouteTables.html#AddRemoveRoutes
