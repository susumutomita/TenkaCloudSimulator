# Add IPv4 subnets to an existing local VPC

In the signed-in **VPC** Console, select an **EC2-managed** VPC. Under **Subnets**, use **Add subnet** and enter an IPv4 CIDR and a local Availability Zone. The Console says a subnet was saved only after verifying that the returned ID is present under that VPC; a confirmed deletion must disappear from the subsequent read. **Refresh subnets** reconciles a write whose outcome could not be verified. The Console does not automatically retry a write.

This uses the same native Query API and SQLite state as SDK clients. It does not create a stack, attach to a CloudFormation VPC, add a gateway or route, start a VM, or verify packet reachability. Subnets owned by a CloudFormation stack are visible, not editable, here. The original operator-only **VPC and one subnet (CloudFormation-managed)** creates both together in a new stack and is unchanged.

`POST /`, `X-Simulator-Service: ec2`, `X-Simulator-Region: <region>`, and `Version=2016-11-15` select these bounded operations:

| Action | Input | Result |
| --- | --- | --- |
| `CreateSubnet` | `VpcId`, IPv4 `CidrBlock` (`/16`–`/28`, inside the VPC and nonoverlapping), optional `AvailabilityZone` (`<region>a`, `b`, or `c`, default `a`), optional `DryRun=false` | Saved `subnet` with canonical CIDR, VPC ID, availability zone, and local `simulatorManagedBy=ec2` marker. |
| `DescribeSubnets` | No operation-specific members | Bounded regional list of saved native and CloudFormation subnet rows. The Console validates the response and selects one VPC locally; this is not an IAM filter. |
| `DeleteSubnet` | `SubnetId`, optional `DryRun=false` | Removes exactly one unreferenced EC2-owned subnet in the current region. The parent VPC cannot be deleted while a subnet is attached. |

The selected exercise identity needs `ec2:CreateSubnet` on the exact VPC ARN, `arn:aws:ec2:<region>:000000000000:subnet/*` and the newly allocated subnet ARN before any write transaction, `ec2:DescribeSubnets` on `*`, and `ec2:DeleteSubnet` on the target subnet ARN. Scoped handoffs do not offer the pane and cannot use these APIs. Unsupported attributes, tags at creation, IPAM, IPv6, filters, pagination and `DryRun=true` are rejected. The existing shared 1,000 network-resource bound applies; the Console also refuses snapshots above 16 MiB rather than showing truncated or empty success.

Validation: `go test ./internal/aws -run 'TestEC2NativeSubnet|TestEC2DescribeSubnetsContract'`; `node --test tools/console/test/ec2-native-subnets.test.mjs`. For the embedded Console against the built local simulator, set `SIMULATOR_BINARY` and `JSDOM_MODULE` and run `python3 test/console/ec2_native_subnet.py` with pinned boto3/botocore. The optional combined VPC pane check uses JSDOM rather than Chromium.

References: [CreateSubnet](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateSubnet.html), [DescribeSubnets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSubnets.html), [DeleteSubnet](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteSubnet.html), and [EC2 IAM actions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html).
