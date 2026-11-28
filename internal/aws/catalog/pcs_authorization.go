package catalog

import (
	"database/sql"
	"fmt"
	"strings"
)

// PCS's IAM table distinguishes wildcard actions, parent-only actions,
// parent-and-child actions, and tagging a single actual resource.
func (q *request) authorizePCS(resource string, parent *row) (bool, error) {
	if q.b.spec.Model != "pcs" {
		return false, nil
	}
	switch q.op.Name {
	case "CreateCluster", "ListClusters", "ListTagsForResource":
		return true, q.authorize("*")
	case "CreateComputeNodeGroup", "CreateQueue", "ListComputeNodeGroups", "ListQueues":
		arn, err := q.pcsParentARN(parent)
		if err != nil {
			return true, err
		}
		return true, q.authorize(arn)
	case "GetComputeNodeGroup", "UpdateComputeNodeGroup", "DeleteComputeNodeGroup", "GetQueue", "UpdateQueue", "DeleteQueue":
		parentARN, err := q.pcsParentARN(parent)
		if err != nil {
			return true, err
		}
		canonical, err := q.pcsChildARN(resource, parent)
		if err != nil {
			return true, err
		}
		if q.e.cfg.AuthorizeSnapshot == nil {
			return true, fmt.Errorf("PCS compound authorization requires a policy snapshot")
		}
		decide, err := q.e.cfg.AuthorizeSnapshot(q.ctx, q.principal)
		if err != nil {
			return true, err
		}
		if decide == nil {
			return true, fmt.Errorf("PCS compound authorization received an empty policy snapshot")
		}
		action := q.b.spec.IAMPrefix + ":" + q.op.action()
		for _, target := range []string{parentARN, canonical} {
			if err := q.checkDecision(decide(action, target)); err != nil {
				return true, err
			}
		}
		return true, nil
	case "TagResource", "UntagResource":
		// tagged resolves and validates the stored identity first.
		return true, q.authorize(resource)
	}
	return false, nil
}

func (q *request) pcsParentARN(parent *row) (string, error) {
	if parent == nil || parent.Service != "pcs" || parent.Kind != "cluster" || parent.Region != q.region || parent.Parent != "" {
		return "", fmt.Errorf("PCS operation lacks its canonical cluster")
	}
	canonical := q.formatARN(q.b.spec.resource("cluster"), parent.ID, parent.Name, nil)
	if parent.ARN != canonical || parent.Document["id"] != parent.ID || parent.Document["arn"] != parent.ARN {
		return "", fmt.Errorf("PCS cluster identity is inconsistent; saved state preserved")
	}
	return canonical, nil
}

// A previously stored flat ARN is a storage identity, never an additional
// IAM grant. Derive the sole canonical policy target from the actual parent.
func (q *request) pcsChildARN(stored string, parent *row) (string, error) {
	res := q.resource()
	id := stored[strings.LastIndex(stored, "/")+1:]
	canonical := q.formatARN(res, id, id, parent)
	flat := fmt.Sprintf("arn:aws:pcs:%s:%s:%s/%s", q.region, q.e.cfg.Account, strings.ToLower(res.Kind), id)
	if stored != canonical && stored != flat {
		return "", fmt.Errorf("PCS child ARN is inconsistent; saved state preserved")
	}
	return canonical, nil
}

// pcsStoredARN is for ARN-taking tag operations, whose Op has no resource.
func (q *request) pcsStoredARN(r row) (string, error) {
	if r.Region != q.region {
		return "", &fault{kind: "notfound", message: "The PCS resource does not exist in this Region."}
	}
	res := q.b.spec.resource(r.Kind)
	if res == nil || r.Document["id"] != r.ID || r.Document["arn"] != r.ARN {
		return "", fmt.Errorf("PCS stored identity is inconsistent; saved state preserved")
	}
	if r.Kind == "cluster" {
		return q.pcsParentARN(&r)
	}
	if r.Kind != "queue" && r.Kind != "computeNodeGroup" {
		return "", fmt.Errorf("unsupported PCS stored resource kind")
	}
	parent, err := find(q.e.store.db, "pcs", q.region, "cluster", "", r.Parent)
	if err != nil {
		return "", err
	}
	if parent.ID != r.Parent || r.Document["clusterId"] != r.Parent {
		return "", fmt.Errorf("PCS stored parent identity is inconsistent; saved state preserved")
	}
	if _, err := q.pcsParentARN(&parent); err != nil {
		return "", err
	}
	canonical := q.formatARN(res, r.ID, r.Name, &parent)
	flat := fmt.Sprintf("arn:aws:pcs:%s:%s:%s/%s", q.region, q.e.cfg.Account, strings.ToLower(r.Kind), r.ID)
	if r.ARN != canonical && r.ARN != flat {
		return "", fmt.Errorf("PCS stored ARN is inconsistent; saved state preserved")
	}
	return canonical, nil
}

// Only a real row in the exact region, account, parent and kind may be
// addressed through its canonical ARN. This leaves old flat rows untouched.
func (q *request) pcsTagAlias(arn string) (row, error) {
	parts := strings.Split(arn, "/")
	prefix := fmt.Sprintf("arn:aws:pcs:%s:%s:cluster", q.region, q.e.cfg.Account)
	if len(parts) != 4 || parts[0] != prefix {
		return row{}, sql.ErrNoRows
	}
	kind := ""
	switch parts[2] {
	case "queue":
		kind = "queue"
	case "computenodegroup":
		kind = "computeNodeGroup"
	default:
		return row{}, sql.ErrNoRows
	}
	r, err := find(q.e.store.db, "pcs", q.region, kind, parts[1], parts[3])
	if err != nil {
		return row{}, err
	}
	if r.Parent != parts[1] || r.ID != parts[3] {
		return row{}, sql.ErrNoRows
	}
	canonical, err := q.pcsStoredARN(r)
	if err != nil {
		return row{}, err
	}
	if canonical != arn {
		return row{}, sql.ErrNoRows
	}
	return r, nil
}
