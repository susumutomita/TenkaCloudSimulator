# PCS queue associations

The local PCS catalog stores cluster, compute node group and queue metadata.
Resources remain `CREATING`. No Slurm controller, compute instances, scheduler,
job submission, or provisioned network is available.

`CreateQueue` and `UpdateQueue` resolve each supplied `computeNodeGroupId` as an
ID of an existing local group in the same Region and persisted parent cluster.
They retain the ID in the official response shape. Names and ARNs are not ID
aliases. Validation runs in the same SQLite transaction as the queue write.
A missing group returns `ResourceNotFoundException` without storing any part of
the requested association replacement. The simulator requires an ID in every
configuration entry; the official member is optional, so this is a local bound.
An omitted update member retains the existing associations; an explicit empty
list removes them. Existing saved metadata can still be read.

`DeleteComputeNodeGroup` returns `ConflictException` while a live queue in that
cluster names its ID. This check runs in the deleting transaction. Remove all
associations through queue updates or delete the referencing queues before
deleting the group. Multiple queues can name the same group.

Authorization uses `pcs:CreateQueue` on the parent cluster ARN,
`pcs:UpdateQueue` on both the parent cluster and queue ARN, and
`pcs:DeleteComputeNodeGroup` on both the parent cluster and group ARN. Resolving
a group does not introduce an extra IAM action on that reference.

`UpdateCluster` remains unsupported. The official operation permits only
`ACTIVE`, `UPDATE_FAILED`, or `SUSPENDED` clusters with `ACTIVE` child resources;
these local configuration records do not reach those lifecycle states. Other
stored dependencies, including subnets, instance profiles and launch templates,
are not provisioned or resolved by this association check.

Primary references:

- [CreateQueue](https://docs.aws.amazon.com/pcs/latest/APIReference/API_CreateQueue.html)
- [UpdateQueue](https://docs.aws.amazon.com/pcs/latest/APIReference/API_UpdateQueue.html)
- [DeleteComputeNodeGroup](https://docs.aws.amazon.com/pcs/latest/APIReference/API_DeleteComputeNodeGroup.html)
- [ComputeNodeGroupConfiguration](https://docs.aws.amazon.com/pcs/latest/APIReference/API_ComputeNodeGroupConfiguration.html)
- [UpdateCluster](https://docs.aws.amazon.com/pcs/latest/APIReference/API_UpdateCluster.html)
- [PCS IAM actions and resource types](https://docs.aws.amazon.com/service-authorization/latest/reference/list_pcs.html)
