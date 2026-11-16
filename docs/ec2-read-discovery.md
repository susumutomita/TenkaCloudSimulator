# Local EC2/VPC read-only discovery

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and `Action=DescribeInstances` or
`Action=DescribeVpcs`. Responses use the EC2 namespace and lower-camel-case
`reservationSet/instancesSet/item` and `vpcSet/item` fields respectively.

This reads CloudFormation-owned resources from the same local SQLite database,
checking saved resource, region, stack ownership and network identity consistency.
It does not create resources or make cloud calls. The host's existing local token
and browser session authentication still applies. Selected exercise identities
need the matching `ec2:DescribeInstances` or `ec2:DescribeVpcs` action on `*`.
One database/token authority per team is still required; exercise identities are
not tenant isolation. Native STS session credentials are not supported here.

There are no mutation operations, filters, selected-ID queries or pagination.
Unsupported fields are rejected, not silently ignored. More than 1000 matching
resources fails the whole request, rather than returning a truncated list.

Instances expose only IDs, image/type, subnet/VPC, security-group IDs and tags.
UserData, runtime plans and internal access information are never returned.
`instanceState` is deliberately absent: successful provisioning or a saved
`ready` journal entry does not prove a live running VM. The extension
`simulatorRuntimeState` is the recorded `launch_pending`, `ready` or `seal_pending`
value; absent/unrecognized state is `unknown`. VPC CIDR and tags are saved local
configuration, not AWS availability evidence. Deleted ownership disappears from
results; inconsistent surviving ownership fails closed.
