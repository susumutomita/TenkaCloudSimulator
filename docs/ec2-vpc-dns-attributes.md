# VPC DNS settings (local EC2 Query)

Describe one of `enableDnsSupport` or `enableDnsHostnames` with
`DescribeVpcAttribute`. Change one setting in an EC2-managed VPC with
`ModifyVpcAttribute`. The change is region-local and persists in the same
SQLite VPC row as the main route table relationship, including across restart.
For a new nondefault VPC, DNS support starts enabled and DNS hostnames disabled.
Enable DNS support before enabling hostnames. Both actions evaluate their own
permission on the selected VPC ARN. A verified CloudFormation VPC is readable,
but these API calls do not change its stack.

In a normal signed-in console, select a VPC to see **DNS settings** in its
details. **Edit VPC settings** saves and reads back the changed attributes
one at a time. If only the first change is confirmed, its status remains
visible and no subsequent requests are replayed automatically. The
CloudFormation pane is read-only.

This is control-plane configuration only. It does not create a resolver, issue
instance hostnames, configure private hosted zones or prove DNS connectivity.
Network-address-usage metrics, editing two flags in one request, and unrelated
attributes are not supported.

AWS contract: [DescribeVpcAttribute](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeVpcAttribute.html),
[ModifyVpcAttribute](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ModifyVpcAttribute.html),
[DNS settings and defaults](https://docs.aws.amazon.com/vpc/latest/userguide/AmazonDNS-concepts.html#vpc-dns-support),
[EC2 IAM actions and resource types](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonec2.html).
