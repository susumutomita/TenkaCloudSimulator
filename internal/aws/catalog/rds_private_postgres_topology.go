package catalog

import (
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
}

func (q *request) checkRDSPrivatePostgresTopology(tx *sql.Tx, r row) error {
	validate := q.e.cfg.ValidateRDSPrivatePostgres
	if validate == nil || r.Service != "rds" || r.Kind != "dbInstance" ||
		(q.op.Name != "CreateDBInstance" && q.op.Name != "ModifyDBInstance") {
		return nil
	}
	// Omission is not explicit private intent. Other existing configuration
	// records stay outside this optional slice, without making a runtime claim.
	engine, _ := r.Document["Engine"].(string)
	public, specified := r.Document["PubliclyAccessible"].(bool)
	_, inCluster := r.Document["DBClusterIdentifier"]
	if engine != "postgres" || !specified || public || inCluster {
		return nil
	}
	ref, _ := r.Document["DBSubnetGroup"].(map[string]any)
	name, _ := ref["DBSubnetGroupName"].(string)
	groups, groupOK := exactTopologyIDs(r.Document["VpcSecurityGroups"], "VpcSecurityGroupId", rdsSecurityPattern, 1, 5)
	if name == "default" || !rdsReferencePattern.MatchString(name) || !groupOK {
		return invalid("Private PostgreSQL metadata requires an explicit existing DB subnet group and one to five distinct VPC security groups.")
	}
	storedGroup, err := find(tx, "rds", r.Region, "dbSubnetGroup", "", name)
	if errors.Is(err, sql.ErrNoRows) {
		return Failure("InvalidVPCNetworkStateFault", "The configured private PostgreSQL DB subnet group is not present in this Region.")
	}
	if err != nil {
		return err
	}
	subnets, subnetOK := exactTopologyIDs(storedGroup.Document["Subnets"], "SubnetIdentifier", rdsSubnetPattern, 2, 20)
	if !subnetOK || storedGroup.ID != name || storedGroup.Name != name || storedGroup.Seq <= 0 || storedGroup.Generation <= 0 {
		return Failure("InvalidVPCNetworkStateFault", "The stored private PostgreSQL DB subnet group cannot be resolved.")
	}
	return validate(q.ctx, tx, RDSPrivatePostgresRefs{Region: r.Region, SubnetIDs: subnets, SecurityGroupIDs: groups})
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
