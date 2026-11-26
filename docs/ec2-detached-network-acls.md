# EC2: detached custom IPv4 network ACLs

Issue [#56](https://github.com/susumutomita/TenkaCloudSimulator/issues/56)
tracks the broader VPC workflow. This slice stores and removes a custom ACL
inside an existing EC2-managed VPC; it **does not apply** to any subnet or
affect packet traffic. It does not create a VPC's default ACL or support rule
mutation, association, a console screen, Terraform, or CloudFormation ACLs.

The native entry point is authenticated EC2 Query/XML, version `2016-11-15`.

| Operation | Supported arguments and effect |
| --- | --- |
| [CreateNetworkAcl](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateNetworkAcl.html) | `VpcId`, optional `ClientToken`, optional one `TagSpecification` of type `network-acl` with at most 50 tags. Creates one durable ACL in the caller's region under the EC2-managed parent. |
| [DescribeNetworkAcls](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeNetworkAcls.html) | All detached ACLs, up to 50 explicit ACL IDs and/or one `vpc-id` filter with up to 50 VPC IDs. Results are ordered by ACL ID. Other filters and pagination return an error. |
| [DeleteNetworkAcl](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteNetworkAcl.html) | `NetworkAclId`. Only the requested detached ACL is deleted. Missing IDs return an error. Delete ACLs before deleting their VPC. |

`DryRun=true` and unrecognized parameters are not supported. A response
reports `default=false`, no associations, and the immutable terminal IPv4
deny entry for each direction, as specified for a new
[custom ACL](https://docs.aws.amazon.com/vpc/latest/userguide/custom-network-acl.html).
Those entries describe saved configuration only. The local XML extension
`simulatorNetworkAclMode=detached-config-only` makes this explicit.

The local IAM policy must allow `ec2:CreateNetworkAcl` for the existing VPC
ARN and the region's `network-acl/*` ARN, and `ec2:CreateTags` for the ACL
if tags are provided. The real assigned ACL ARN is checked again before
writing or replaying a request. Describe uses `ec2:DescribeNetworkAcls` on
`*`; delete needs `ec2:DeleteNetworkAcl` for the specific ACL ARN. See the
[EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html).

`ClientToken` accepts 1–64 printable ASCII characters. Only its digest and
the canonical request digest are stored, atomically with the new ACL. A
matching retry in the same region returns the existing ACL; a changed
request with the same token receives `IdempotentParameterMismatch`.
A matching retry after ACL removal returns `InvalidNetworkAclID.NotFound`
instead of silently recreating a resource. The existing 1,000-resource
network bound and a local 4,096-receipt bound apply; deletion does not
evict the replay receipt. This is a bounded local behavior, not a claim of
identical AWS receipt retention.

The opt-in check `python test/e2e/ec2_detached_nacl.py` uses an actual local
simulator binary and SDK, isolated databases, and numeric loopback only.
It proves saved state, scope and cleanup, not network enforcement.
