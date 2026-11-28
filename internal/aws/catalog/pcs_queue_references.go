package catalog

import (
	"database/sql"
	"errors"
)

// PCS uses compute node group IDs, rather than the ARN references used by
// the generic catalog. Resolve them in the queue's persisted parent scope.
// The caller repeats this check in the same transaction as each mutation.
func (q *request) checkPCSQueueReferences(db querier, res *Resource, document map[string]any) error {
	if q.b.spec.Model != "pcs" || res.Kind != "queue" {
		return nil
	}
	parent, _ := document["clusterId"].(string)
	configurations, _ := document["computeNodeGroupConfigurations"].([]any)
	for _, entry := range configurations {
		configuration, _ := entry.(map[string]any)
		if id, _ := configuration["computeNodeGroupId"].(string); id == "" {
			return invalid("The simulator requires computeNodeGroupId in each queue configuration.")
		}
	}
	for _, id := range refValues(document, "computeNodeGroupConfigurations[].computeNodeGroupId") {
		found, err := find(db, "pcs", q.region, "computeNodeGroup", parent, id)
		if errors.Is(err, sql.ErrNoRows) || err == nil && found.ID != id {
			return &fault{kind: "notfound", message: "The referenced compute node group does not exist in this cluster."}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Both this predicate and the final generation check run in the deleting
// transaction. No authorized queue creation/update can leave a dangling ID.
func pcsCheckDeleteComputeNodeGroup(db querier, group row) error {
	queues, err := listKind(db, "pcs", "queue")
	if err != nil {
		return err
	}
	for _, queue := range queues {
		if queue.Region != group.Region || queue.Parent != group.Parent {
			continue
		}
		for _, id := range refValues(queue.Document, "computeNodeGroupConfigurations[].computeNodeGroupId") {
			if id == group.ID {
				return &fault{kind: "conflict", message: "The compute node group is associated with a queue; remove its queue associations first."}
			}
		}
	}
	return nil
}

// Only the reviewed PCS association mutations opt into this final context
// fence. An error causes their existing deferred transaction rollback.
func (q *request) pcsAssociationContext() error {
	if q.b.spec.Model == "pcs" && (q.op.Name == "CreateQueue" || q.op.Name == "UpdateQueue" || q.op.Name == "DeleteComputeNodeGroup") && q.ctx != nil {
		return q.ctx.Err()
	}
	return nil
}
