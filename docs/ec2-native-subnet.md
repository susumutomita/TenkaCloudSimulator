# Add IPv4 subnets to an existing local VPC

In the signed-in **VPC → Subnets** navigation, view the current region’s saved
subnets. Search by name or ID or filter the list by a VPC. **Create subnet** opens
a separate page: choose an EC2-managed VPC, check its IPv4 CIDR, and enter an IPv4
range and a local Availability Zone. **No preference** uses the region’s local
`a` zone. In an advertised native session, **Subnet name** saves the optional
`Name` tag and **Tags** adds optional key/value entries, up to 50 tags total.
A supplied key can have an empty value. The request omits tags entirely when
no entry is supplied. Saved native tags appear in subnet details and the name
appears in the inventory. The console confirms the same complete tag set in
the creation receipt and native regional readback, never replaying or silently
dropping tags after a refusal. A saved CloudFormation Name tag remains
read-only. The older **Add subnet** control in a selected VPC also
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
| `CreateSubnet` | `VpcId`, IPv4 `CidrBlock` (`/16`–`/28`, inside the VPC and nonoverlapping), optional `AvailabilityZone` (`<region>a`, `b`, or `c`, default `a`), optional `DryRun=false`, optional `TagSpecification.1` (below) | Saved `subnet` with canonical CIDR, VPC ID, availability zone, explicit `mapPublicIpOnLaunch=false`, saved tags in `tagSet`, and local `simulatorManagedBy=ec2` marker. |
| `DescribeSubnets` | No operation-specific members | Bounded regional list of saved native and CloudFormation subnet rows. Native rows include saved tags and the Boolean `mapPublicIpOnLaunch`; the CloudFormation projection is unchanged. The Console validates the response and selects one VPC locally; this is not an IAM filter. |
| `ModifySubnetAttribute` | `SubnetId`, precisely one `MapPublicIpOnLaunch.Value=true\|false`, optional `DryRun=false` | Commits that preference for one EC2-owned row; `return=true` acknowledges the write. A repeat is a no-op; all other attributes are unsupported. |
| `DeleteSubnet` | `SubnetId`, optional `DryRun=false` | Removes exactly one unreferenced EC2-owned subnet in the current region. The parent VPC cannot be deleted while a subnet is attached. |

The selected exercise identity needs `ec2:CreateSubnet` on the exact VPC ARN, `arn:aws:ec2:<region>:000000000000:subnet/*` and the newly allocated subnet ARN before any write transaction, `ec2:DescribeSubnets` on `*`, `ec2:ModifySubnetAttribute` on the exact target subnet ARN, and `ec2:DeleteSubnet` on the target subnet ARN. Scoped handoffs do not offer the pane and cannot use these APIs. Unsupported attributes, IPAM, IPv6, filters, pagination and `DryRun=true` are rejected. The existing shared 1,000 network-resource bound applies; the Console also refuses snapshots above 16 MiB rather than showing truncated or empty success.

To save a subnet name with the SDK, include
`TagSpecifications=[{"ResourceType":"subnet","Tags":[{"Key":"Name","Value":"private-a"}]}]`
in `create_subnet`. The Query equivalent uses `TagSpecification.1.ResourceType=subnet`
and contiguous `TagSpecification.1.Tag.N.Key/Value` fields starting at 1. Send at
most 50 unique keys (1–128 bytes), with values of up to 256 bytes (omitted means
empty). Tag text must be valid XML text; the `aws:` prefix is reserved. There is no
separate `Name` parameter.

Only a nonempty tag set also requires `ec2:CreateTags` on the subnet wildcard ARN
and the newly allocated exact subnet ARN. A denial leaves no subnet or tags.
The simulator does not expose standalone EC2 `CreateTags`/post-create edits or
implement the AWS `ec2:CreateAction` condition for this operation. The group of
EC2 capabilities reports `nativeSubnetTagsOnCreate: true` for normal authenticated
sessions when native create and readback are mounted; the flag is not an IAM grant.

Validation: `go test ./internal/aws -run 'TestEC2NativeSubnet|TestEC2DescribeSubnetsContract'`; `node --test tools/console/test/ec2-native-subnets.test.mjs`. Scoped settings acceptance uses the pinned SDK and served console via `python3 test/console/ec2_subnet_public_ipv4.py` (set `SIMULATOR_BINARY` and `JSDOM_MODULE`). For the embedded Console against the built local simulator, set `SIMULATOR_BINARY` and `JSDOM_MODULE` and run `python3 test/console/ec2_native_subnet.py` with pinned boto3/botocore. The optional combined VPC pane check uses JSDOM rather than Chromium.

Opt-in real Chromium test against the embedded console (no changes to routine
CI): `SIMULATOR_BINARY=/abs/simulator PLAYWRIGHT_MODULE=/abs/playwright-core node test/console/ec2_subnets_console_browser.cjs`.
For tag-specific browser and native SDK acceptance of the same compiled binary, run
`node test/console/ec2_subnet_create_tags_browser.cjs` (with `PLAYWRIGHT_MODULE`)
and `python3 test/console/ec2_subnet_create_tags_sdk.py` with pinned boto3/botocore.
Use the Chromium distribution matching the installed Playwright version and
optionally set `SCREENSHOTS_DIR` to retain synthetic screenshots.

References: [Create a subnet](https://docs.aws.amazon.com/vpc/latest/userguide/create-subnets.html),
[CreateSubnet](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateSubnet.html), [DescribeSubnets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSubnets.html), [DeleteSubnet](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteSubnet.html), [ModifySubnetAttribute](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ModifySubnetAttribute.html), [AWS subnet IP addressing](https://docs.aws.amazon.com/vpc/latest/userguide/subnet-public-ip.html), [EC2 IAM actions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html), and [AWS permissions for tagging on creation](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/supported-iam-actions-tagging.html).
