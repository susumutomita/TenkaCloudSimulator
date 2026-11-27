package catalog

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
)

// RDSPrivatePostgresRefs are configuration references, not validated network
// ownership, an address, or backend authority. The EC2 provider must resolve
// them using the same SQLite transaction that would commit the RDS write.
type RDSPrivatePostgresRefs struct {
	Region                      string
	SubnetIDs, SecurityGroupIDs []string
	// Internal catalog provenance, not an input to the native EC2 validator.
	group           privateSubnetGroupIdentity
	groupGeneration int64
}

// Creation sequence distinguishes groups even if their names, ARNs and subnet
// lists match. Generation changes independently on updates to the same group.
type privateSubnetGroupIdentity struct {
	arn string
	seq int64
}

// Only the concrete Linux/amd64 provider composition implements this
// package-private contract. There is no request-selectable callback or target.
type privateRDSLifecycle interface {
	lock(region string, kind Kind) func()
	reserve(tx *sql.Tx, r row) (bool, error)
	startCommitted(ctx context.Context, r row)
	preflightModify(q *request, proposed row) error
	retire(r row) error
}

func (q *request) retireOwnedPrivateRDS(r row) error {
	if q.e.ownedPrivateRDS == nil || r.Service != "rds" || r.Kind != "dbInstance" {
		return nil
	}
	return q.e.ownedPrivateRDS.retire(r)
}

func (q *request) checkRDSPrivatePostgresTopology(tx *sql.Tx, r row) error {
	validate := q.e.cfg.ValidateRDSPrivatePostgres
	if validate == nil || !privatePostgresShape(r) ||
		(q.op.Name != "CreateDBInstance" && q.op.Name != "ModifyDBInstance") {
		return nil
	}
	return validatePrivatePostgresRefs(q.ctx, tx, r, validate)
}

func privatePostgresShape(r row) bool {
	engine, _ := r.Document["Engine"].(string)
	public, specified := r.Document["PubliclyAccessible"].(bool)
	_, inCluster := r.Document["DBClusterIdentifier"]
	return r.Service == "rds" && r.Kind == "dbInstance" && engine == "postgres" && specified && !public && !inCluster
}

func validatePrivatePostgresRefs(ctx context.Context, tx *sql.Tx, r row, validate func(context.Context, *sql.Tx, RDSPrivatePostgresRefs) error) error {
	refs, err := resolvePrivatePostgresRefs(tx, r)
	if err != nil {
		return err
	}
	return validate(ctx, tx, refs)
}

func resolvePrivatePostgresRefs(tx *sql.Tx, r row) (RDSPrivatePostgresRefs, error) {
	ref, _ := r.Document["DBSubnetGroup"].(map[string]any)
	name, _ := ref["DBSubnetGroupName"].(string)
	groups, groupOK := exactTopologyIDs(r.Document["VpcSecurityGroups"], "VpcSecurityGroupId", rdsSecurityPattern, 1, 5)
	if name == "default" || !rdsReferencePattern.MatchString(name) || !groupOK {
		return RDSPrivatePostgresRefs{}, invalid("Private PostgreSQL metadata requires an explicit existing DB subnet group and one to five distinct VPC security groups.")
	}
	storedGroup, err := find(tx, "rds", r.Region, "dbSubnetGroup", "", name)
	if errors.Is(err, sql.ErrNoRows) {
		return RDSPrivatePostgresRefs{}, Failure("InvalidVPCNetworkStateFault", "The configured private PostgreSQL DB subnet group is not present in this Region.")
	}
	if err != nil {
		return RDSPrivatePostgresRefs{}, err
	}
	subnets, subnetOK := exactTopologyIDs(storedGroup.Document["Subnets"], "SubnetIdentifier", rdsSubnetPattern, 2, 20)
	if !subnetOK || storedGroup.ID != name || storedGroup.Name != name || storedGroup.Seq <= 0 || storedGroup.Generation <= 0 {
		return RDSPrivatePostgresRefs{}, Failure("InvalidVPCNetworkStateFault", "The stored private PostgreSQL DB subnet group cannot be resolved.")
	}
	return RDSPrivatePostgresRefs{
		Region: r.Region, SubnetIDs: subnets, SecurityGroupIDs: groups,
		group: privateSubnetGroupIdentity{arn: storedGroup.ARN, seq: storedGroup.Seq}, groupGeneration: storedGroup.Generation,
	}, nil
}

func exactTopologyIDs(value any, field string, pattern *regexp.Regexp, min, max int) ([]string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) < min || len(items) > max {
		return nil, false
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		members, _ := item.(map[string]any)
		id, _ := members[field].(string)
		if !pattern.MatchString(id) || slices.Contains(ids, id) {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}
