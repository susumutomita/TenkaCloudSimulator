# Regional VPC console pages

Open **VPC** in the simulator console and use the sidebar to reach **Subnets**,
**Route tables**, **Internet gateways**, **Network ACLs**, and **Security groups**.
The separate pages need an ordinary signed-in local console session with the
corresponding EC2 Query operations advertised. The current exercise identity
still needs the server's IAM permission for each actual resource and operation.

| Page | Saved configuration in this simulator |
| --- | --- |
| VPCs | [Native IPv4 VPCs and the limited CloudFormation option](ec2-read-discovery.md#console-vpc-and-one-subnet). |
| Subnets | [Regional inventory, native IPv4 creation, and verified implicit or explicit route-table association](ec2-native-subnet.md). |
| Route tables | Tags, saved IPv4 routes, and subnet associations. Native operations are available where backed by the [route table](ec2-read-discovery.md#route-table-readback) and [route](ec2-native-default-route.md) APIs. |
| Internet gateways | [Regional inventory, detached creation/deletion, and VPC attachment/detachment](ec2-internet-gateways.md). CloudFormation-owned gateways remain read-only here. |
| Network ACLs | [Detached custom ACLs only](ec2-detached-network-acls.md). No default ACL, rule editing, subnet association, or traffic filtering. |
| Security groups | Select a VPC to see stored groups. [Native detached groups and supported TCP/IPv4 rules](ec2-read-discovery.md#security-group-readback) can be managed where advertised; CloudFormation groups are read-only here. |

The links use the selected region. A resolved subnet can link to its actual
route table and VPC. Route-table references only appear as links to destinations
available in this session. A security group is opened under its exact parent
VPC. Back and Forward revisit the prior address; Reload confirms the resource
from the API again. Changing region returns to the corresponding inventory,
without reusing another region's selection. After a confirmed mutation the
console can replace the address with the saved resource or, on deletion, the
remaining inventory. An unconfirmed write is not automatically replayed.

AWS references: [route tables](https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Route_Tables.html),
[internet gateways](https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Internet_Gateway.html),
[network ACLs](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-network-acls.html), and
[security groups](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-security-groups.html).
The corresponding simulator settings do **not** forward packets, establish
Internet access, evaluate guest reachability, or turn a native subnet and
security group into an EC2 launch target.
