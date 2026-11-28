# Add IPv4 subnets to an existing local VPC

In the signed-in **VPC → Subnets** navigation, view the current region’s saved
subnets. Search by name or ID or filter the list by a VPC. **Create subnet** opens
a separate page: choose an EC2-managed VPC, check its IPv4 CIDR, and enter an IPv4
range and a local Availability Zone. **No preference** uses the region’s local
`a` zone. Native subnet creation does not accept tags. For an existing
native subnet, see the [post-creation Name and tag API](#native-subnet-tag-editing).
CloudFormation tags remain read-only in the native API. The older **Add subnet** control in a selected VPC also
uses the same native API. The Console says a subnet was saved only after verifying that the returned ID is present under that VPC; a confirmed deletion must disappear from the subsequent read. **Refresh subnets** reconciles a write whose outcome could not be verified. The Console does not automatically retry a write.

For a saved native subnet, **Edit subnet settings** toggles **Enable auto-assign
public IPv4 address**. The default is off. The list shows the saved preference and
reports success only after the same subnet is reread with the requested value.
This is configuration only: enabling it does not allocate a public IP, alter
routing, or enable a guest network. An unconfirmed write is never replayed;
refresh to see the durable state before deciding whether to send another change.

The standalone page opens the exact saved subnet after verifying the create
response and regional readback. If the outcome is unknown, return to **Subnets**
to inspect state before trying again. A subnet ID link opens the overview and,
when available, the confirmed explicit or native main route table. The
simulator's ACL API lists detached ACLs; it does not report an ACL association
for a subnet. This page does not estimate available IPs or test guest traffic.

This uses the same native Query API and SQLite state as SDK clients. It does not create a stack, attach to a CloudFormation VPC, add a gateway or route, start a VM, or verify packet reachability. Subnets owned by a CloudFormation stack are visible, not editable, here. The original operator-only **VPC and more** creates one VPC and one subnet together in a new stack and is unchanged.

`POST /`, `X-Simulator-Service: ec2`, `X-Simulator-Region: <region>`, and `Version=2016-11-15` select these bounded operations:

| Action | Input | Result |
| --- | --- | --- |
| `CreateSubnet` | `VpcId`, IPv4 `CidrBlock` (`/16`–`/28`, inside the VPC and nonoverlapping), optional `AvailabilityZone` (`<region>a`, `b`, or `c`, default `a`), optional `DryRun=false` | Saved `subnet` with canonical CIDR, VPC ID, availability zone, explicit `mapPublicIpOnLaunch=false`, and local `simulatorManagedBy=ec2` marker. |
| `DescribeSubnets` | No operation-specific members | Bounded regional list of saved native and CloudFormation subnet rows. Native rows include the saved Boolean `mapPublicIpOnLaunch`; the CloudFormation projection is unchanged. The Console validates the response and selects one VPC locally; this is not an IAM filter. |
| `ModifySubnetAttribute` | `SubnetId`, precisely one `MapPublicIpOnLaunch.Value=true\|false`, optional `DryRun=false` | Commits that preference for one EC2-owned row; `return=true` acknowledges the write. A repeat is a no-op; all other attributes are unsupported. |
| `DeleteSubnet` | `SubnetId`, optional `DryRun=false` | Removes exactly one unreferenced EC2-owned subnet in the current region. A saved [ALB placement](elb.md) referencing the subnet also blocks its deletion. The parent VPC cannot be deleted while a subnet is attached. |

### Native subnet tag editing

On an existing EC2-managed subnet, [CreateTags](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateTags.html)
and [DeleteTags](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteTags.html)
accept one `ResourceId.1` bearing the subnet ID and one explicit `Tag.1.Key`.
`Name` is a case-sensitive tag. For create, send `Tag.1.Value`, including an
explicit empty string if needed. For delete, omit the value for a key-only
match or supply the exact value, including empty. A missing key is rejected;
there is no clear-all operation. Additional resource IDs and tags, `DryRun=true`,
IPv6 parameters and reserved `aws:` keys are not supported. The same local
limits as VPC tags apply: 50 tags, keys of 1–128 UTF-8 bytes, and values of up
to 256 bytes, with valid XML text.

The caller needs the matching `ec2:CreateTags` or `ec2:DeleteTags` permission
on the exact subnet ARN in the request's region. CloudFormation subnets and
instance-scoped sessions cannot use these native writes. `return=true`
acknowledges the request. Read the same ID and parent from the existing
regional `DescribeSubnets`; its `tagSet` contains the validated saved tags.
If the readback is uncertain, inspect it again rather than repeating the write.
Tag changes neither advance the subnet's revision nor change its CIDR, AZ,
public IPv4 preference, parent VPC or route associations. The tags live in
the one saved subnet row and are removed with it after normal dependency checks.

Existing untagged state needs no migration. Versions predating native subnet
tags reject tagged native rows, so keep a database backup if downgrade access
is needed. A database change also invalidates any earlier offline route-repair
plan fingerprint for that file.

### Authorization and validation

The selected exercise identity needs `ec2:CreateSubnet` on the exact VPC ARN, `arn:aws:ec2:<region>:000000000000:subnet/*` and the newly allocated subnet ARN before any write transaction, `ec2:DescribeSubnets` on `*`, `ec2:ModifySubnetAttribute` on the exact target subnet ARN, and `ec2:DeleteSubnet` on the target subnet ARN. Scoped handoffs do not offer the pane and cannot use these APIs. Unsupported attributes, tags at creation, IPAM, IPv6, filters, pagination and `DryRun=true` are rejected. The existing shared 1,000 network-resource bound applies; the Console also refuses snapshots above 16 MiB rather than showing truncated or empty success. The VPC filter is applied after this unpaginated regional response, so it cannot reduce the wire size. If the Console limit is reached, use native SDK regional `DescribeSubnets`, subject to the server’s resource bound, and filter that response locally. The simulator does not accept explicit subnet selectors or filter/page parameters for this operation.

Validation: `go test ./internal/aws -run 'TestEC2NativeSubnet|TestEC2DescribeSubnetsContract'`; `node --test tools/console/test/ec2-native-subnets.test.mjs`. Scoped settings acceptance uses the pinned SDK and served console via `python3 test/console/ec2_subnet_public_ipv4.py` (set `SIMULATOR_BINARY` and `JSDOM_MODULE`). For the embedded Console against the built local simulator, set `SIMULATOR_BINARY` and `JSDOM_MODULE` and run `python3 test/console/ec2_native_subnet.py` with pinned boto3/botocore. The optional combined VPC pane check uses JSDOM rather than Chromium.

Opt-in real Chromium test against the embedded console (no changes to routine
CI): `SIMULATOR_BINARY=/abs/simulator PLAYWRIGHT_MODULE=/abs/playwright-core node test/console/ec2_subnets_console_browser.cjs`.
The opt-in `python3 test/e2e/ec2_subnet_tags.py` checks authenticated SDK tag
and subnet readback, restart, existing route association and cleanup.
Use the Chromium distribution matching the installed Playwright version and
optionally set `SCREENSHOTS_DIR` to retain synthetic screenshots.

References: [Create a subnet](https://docs.aws.amazon.com/vpc/latest/userguide/create-subnets.html),
[CreateSubnet](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateSubnet.html), [DescribeSubnets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSubnets.html), [DeleteSubnet](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteSubnet.html), [ModifySubnetAttribute](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ModifySubnetAttribute.html), [AWS subnet IP addressing](https://docs.aws.amazon.com/vpc/latest/userguide/subnet-public-ip.html), and [EC2 IAM actions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html).
