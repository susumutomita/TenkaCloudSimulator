# Native IPv4 default route configuration

This is a bounded local EC2 network configuration operation, not a packet path.
One explicitly created **native** route table may have one saved
`0.0.0.0/0` route. Its target must be the **native internet gateway currently
attached to the table's native VPC** in the same local region. Creating a VPC, an
attachment or a subnet association does not create any route. CloudFormation
routes and their ownership are unchanged.

Use the usual authenticated local Query endpoint and EC2 API version
`2016-11-15`. Use a local simulator URL and a host-issued simulator token in SDKs;
dummy SDK signing credentials do not authenticate. Scoped instance sessions
cannot read or mutate these route tables.

| Operation | Bounded contract |
| --- | --- |
| [CreateRoute][create] | Send one `RouteTableId`, IPv4 `DestinationCidrBlock` and `GatewayId`. Any valid IPv4 /0 in this operation normalizes to `0.0.0.0/0`. A missing gateway is not created; detached and other-VPC gateways are refused. An existing default route returns `RouteAlreadyExists` without replacing it. |
| [DescribeRouteTables][describe] | A saved route appears as `DestinationCidrBlock=0.0.0.0/0`, the target `GatewayId`, `Origin=CreateRoute` and `State=active`. This means validated, saved configuration only. Use a single exact `RouteTableId.1` (SDK `RouteTableIds=[id]`), an explicit `association.subnet-id` filter, or no selector. The exact selector has at most one response row; a missing table returns `InvalidRouteTableID.NotFound`. The full regional consistency check occurs before selecting any output. Selectors are not combined; pagination is not supported here. |
| [DeleteRoute][delete] | Send the exact `RouteTableId` and canonical `DestinationCidrBlock=0.0.0.0/0`. No target parameter. Removes only the saved route at that destination. An absent route returns `InvalidRoute.NotFound`. |

For writes, the [EC2 action][iam] must be allowed on the exact
`arn:aws:ec2:<region>:000000000000:route-table/<id>`. Ordinary
`ec2:DescribeRouteTables` is required on `*`, even with the exact selector.
`DryRun=true` for the two mutations checks permissions only, returns
`DryRunOperation` or `UnauthorizedOperation` and never reads or changes route
state. The native table, VPC, linked gateway, typed claims and regional stack
declarations must agree within the same SQLite transaction. Failures, including
quota or unsuccessful post-write state verification, cannot acknowledge a
change. The native route consumes one row of the existing shared 1,000-network-row
bound; this is not an AWS quota. Delete the saved route before deleting its
table or detaching its gateway. Either attempted removal while referenced
returns `DependencyViolation`. A native table must also be disassociated from
any subnet before deleting it.

[ReplaceRoute][replace], non-default routes, IPv6/prefix-list destinations,
other targets, gateway/main route-table associations and propagation are
outside this slice. There is no alternate eligible gateway in this slice to
replace a saved target with. It does not make a NIC, assign public addresses,
forward packets, or validate internet connectivity. Do not downgrade a
database containing a saved native route to an older binary that treats those
records as invalid. See [EC2 error codes][errors] for names; these are the
simulator's bounded refusal rules, not full AWS compatibility.

## Console, read-only reference, and local acceptance

In an ordinary signed-in **VPC** console, select an EC2-managed VPC and its
native **Route tables** row. Choose **Add IPv4 default route**. The dialog
reads the actual gateway inventory and offers only the current native
attachment, with no editable gateway selector; the server rechecks that
relationship. Choose **Remove IPv4 default route**, confirm the same table ID
and displayed destination. The API removes the route at that destination (no
server-side conditional revision or target match). Console shows success only
after a fresh exact-table readback for the same browser scope verifies the
route or its absence. A missing table, wrong target, damaged reply, or changed
scope cannot confirm success. Refresh after uncertainty instead of replaying a
mutation. This exact confirmation does not depend on reading a second regional
table list; the existing full regional browsing byte bound remains unchanged.

The shared Scalar example is **EC2 DescribeRouteTables** (read only); add
`RouteTableId.1` to verify one saved table after using the SDK or Console. There
are no mutation presets.

```sh
make build
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" python3 test/console/ec2_native_default_route_sdk.py
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" JSDOM_MODULE=/path/to/jsdom \
  python3 test/console/ec2_native_default_route_sdk.py --console
```

The focused test uses the pinned AWS SDK, separate local team databases, and a
process restart. The separate Console check drives the served bundle using
JSDOM and the real local API; it does not assert Chromium layout or real AWS
routing. See the [AWS console workflow][workflow] for the reference design
rather than expecting a complete AWS console clone.

[create]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRoute.html
[describe]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeRouteTables.html
[delete]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteRoute.html
[replace]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ReplaceRoute.html
[iam]: https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonec2.html
[errors]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/errors-overview.html
[workflow]: https://docs.aws.amazon.com/vpc/latest/userguide/WorkWithRouteTables.html#AddRemoveRoutes
