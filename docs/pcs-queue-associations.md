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

PCS IAM uses the official action/resource table:

| Operations | Required policy resources |
| --- | --- |
| `CreateCluster`, `ListClusters`, `ListTagsForResource` | `*` |
| `GetCluster`, `DeleteCluster` | The exact cluster ARN |
| Create or list compute node groups and queues | The exact parent cluster ARN |
| Get, update or delete a compute node group or queue | Both exact parent cluster and child ARNs |
| `TagResource`, `UntagResource` | The actual resource's exact ARN |

New child ARNs are `arn:aws:pcs:<region>:<account>:cluster/<clusterId>/computenodegroup/<groupId>`
and `arn:aws:pcs:<region>:<account>:cluster/<clusterId>/queue/<queueId>`.
Valid earlier flat child ARNs remain in their original stored rows and responses,
including after restart and ordinary updates. No migration rewrites them.
Authorization nevertheless evaluates only their canonical hierarchical ARN,
never a flat ARN as an alternate policy grant. ARN-taking tag operations accept
the stored legacy ARN or its exact canonical alias, resolved to the same real
row in the same account, Region and parent cluster. An alias cannot select a
foreign row or a different resource kind. `ListTagsForResource` still requires
policy Resource `*`, even though its request identifies one actual resource.
Resolving a queue's group does not introduce a separate IAM action on that
reference.
Parent and child checks use one immutable policy snapshot for the request;
separate policy revisions cannot contribute one grant each. Trusted catalog
adapters must supply `AuthorizeSnapshot`; compound requests fail closed without
a valid evaluator. The standalone server supplies the shared identity snapshot.

Local identity-policy denials retain the simulator's HTTP 403 boundary. The
PCS API reference also defines a service `AccessDeniedException` with HTTP 400;
those additional service-side failure conditions are not all reproduced here.

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

## Interrupted association writes

`CreateQueue`, `UpdateQueue` and `DeleteComputeNodeGroup` use the original
request context when waiting for their write transaction and check it again
before commit. Cancellation or an elapsed deadline before commit refuses the
write and rolls back staged resource, association and sequence changes. An
interruption after commit does not undo a committed write: read the current
resource before retrying. Generic catalog preflight lookups retain their
existing behavior, so this does not promise prompt cancellation at every HTTP
stage.
