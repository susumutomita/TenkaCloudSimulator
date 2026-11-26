# Local EC2 Internet Gateway allocation

[EC2/VPC discovery](ec2-read-discovery.md) | [CFN/EC2 contract evidence](ec2-network-contracts.md)

The Internet gateways pane and the EC2 Query API manage **detached EC2-owned
configuration**. A gateway is first allocated independently of any VPC, as in
[the AWS workflow][aws-console]. To get actual internet access in AWS, operators
would separately attach the gateway, configure a route and make the resource
reachable. This local workflow stops at allocate, inspect and clean up. Native
attach/detach, route creation and packet forwarding are not implemented here.

Use the authenticated local EC2 Query endpoint, `POST /` with
`Content-Type: application/x-www-form-urlencoded`, `X-Simulator-Service: ec2`,
the local `X-Simulator-Region` (default `us-east-1`) and `Version=2016-11-15`.
SDKs use the same endpoint, selected region and existing local access token;
dummy SDK signing credentials are not authentication.

| AWS operation | Supported local request | State and permissions |
| --- | --- | --- |
| [CreateInternetGateway][create] | Optional `TagSpecification.1.ResourceType=internet-gateway` with up to 50 contiguous, unique non-reserved tags. Keys fit in 1–128 UTF-8 bytes, values in 0–256 bytes, both valid XML text. | Creates one `igw-…` with an empty attachment set. `ec2:CreateInternetGateway` on local region's `internet-gateway/*` and its generated ARN; `ec2:CreateTags` on the same resources if tagged. No client token or replay; refresh before retrying a write whose outcome is unknown. |
| [DescribeInternetGateways][describe] | No selector (all), or up to eight unique, contiguous `InternetGatewayId.N` with exact lowercase 8- or 17-hex IDs, or an unfiltered page request (`MaxResults` / `NextToken`). | Requires `ec2:DescribeInternetGateways` on `*`. Returns saved native and CloudFormation gateways in the same region, ordered by ID. An explicit missing ID is `InvalidInternetGatewayID.NotFound`; other regions do not contribute rows. |
| [DeleteInternetGateway][delete] | Exactly one `InternetGatewayId`. | Requires `ec2:DeleteInternetGateway` on the exact local gateway ARN. Deletes only a detached EC2-owned row; ownership, live references and row removal are checked in the same transaction. A stack-owned gateway must be removed through its stack, even while detached. |

The regional read uses the typed stack claims, declarations and committed network
graph to verify CloudFormation gateways and their links. A saved attachment is
returned with its VPC ID and `state=available`: this reports the configuration,
not successful traffic. A mismatched row or relationship fails the read without
returning partial results, even when specific IDs were requested. The local
`simulatorManagedBy` XML extension is used by the console; official SDKs ignore
it. Each team uses its own simulator process, SQLite store and token authority.

On an unfiltered read, add `MaxResults=5..1000` to start paging. A local page
contains at most 100 records, even if a higher maximum is requested. Continue
with the opaque `NextToken` until the response omits `nextToken`. If you repeat
`MaxResults`, it must match the request that started the list. As in the
[AWS Query contract][pagination], IDs and paging cannot be combined. The
unpaginated and exact-ID calls remain available.

Each page requires the usual IAM grant and a verified local actor. Its token
is bound to the authenticated credential identity, selected exercise identity
and role, region, unfiltered selection, requested limit, validated snapshot
and page offset. A different active inventory projection, simulator restart,
or scope makes the token invalid. Snapshot identity is based on content, not
a mutation counter: the same validated content can be continued by the same
caller. Start again without the token after `InvalidNextToken`.
A token is not an access credential, and pages are not served from cached
snapshots.

`DryRun=false` is accepted. AWS-style `DryRun=true`, arbitrary filters,
native attach/detach and standalone tag updates are explicitly unsupported.
The simulator's shared 1,000 network-row bound is not an AWS quota.
See [IAM action and resource scope][iam] and [AWS error names][errors] for the
source contract; the local policy subset does not implement AWS condition keys.
A stack's gateway and attachments are unchanged by native mutations.

## Console and reference

In an ordinary signed-in **VPC** console, see **Internet gateways** below the VPC
panel. Create a detached gateway with an optional Name tag; to delete an
EC2-managed row, select it and confirm by typing its exact ID. CloudFormation
rows display only their saved attachment and cannot be deleted here. The pane
does not offer networking actions or appear in an instance-scoped handoff. It
uses the active region and exercise identity and assembles all bounded pages
before showing the regional list. The maximum encoded 100-row page stays
below the unchanged 16 MiB per-response console bound, including XML escaping
of all 50 allowed tags per gateway. The console first reads the exact new ID
after Create; after either write it verifies that exact ID's expected presence
or absence in a complete fresh list before showing success. An unconfirmed
result requires refresh, not automatic retry. An empty continuation is an
incomplete list, and replaced in-flight pages cannot update the current view.

The shared reference includes the **EC2 DescribeInternetGateways** preset.
Its `MaxResults=100` example reads the first page, using the EC2 routing
headers above; use the returned `nextToken` in `NextToken` to continue. The
reference does not add mutation examples. The optional
acceptance uses the existing pinned SDK and a separate JSDOM console run:

```sh
make build
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" python3 test/console/ec2_internet_gateway_sdk.py
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" JSDOM_MODULE=/path/to/jsdom \
  python3 test/console/ec2_internet_gateway_sdk.py --console
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" JSDOM_MODULE=/path/to/jsdom \
  python3 test/console/ec2_internet_gateway_sdk.py --paged-console
```

This proves a local configuration lifecycle and its readback, not a real cloud,
guest, packet path or Chromium layout.

[aws-console]: https://docs.aws.amazon.com/vpc/latest/userguide/working-with-igw.html
[create]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateInternetGateway.html
[describe]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeInternetGateways.html
[delete]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteInternetGateway.html
[iam]: https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html
[pagination]: https://docs.aws.amazon.com/ec2/latest/devguide/Query-Requests.html#Pagination
[errors]: https://docs.aws.amazon.com/ec2/latest/devguide/errors-overview.html
