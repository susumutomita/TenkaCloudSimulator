package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

type nativeACLCohortView struct {
	defaultACL nativeACLDefault
	byACL      map[string][]ec2NetworkACLAssociationRead
}

// Cache verified cohorts and one regional subnet inventory in this same
// transaction. A regional Describe cannot multiply the 1,000-resource bound
// by scanning the same network snapshot for each ACL.
type nativeACLReadSnapshot struct {
	ctx      context.Context
	tx       *sql.Tx
	region   string
	declared map[string]bool
	subnets  map[string][]string
	cohorts  map[string]nativeACLCohortView
}

func newNativeACLReadSnapshot(ctx context.Context, tx *sql.Tx, region string) (nativeACLReadSnapshot, error) {
	declared, err := nativeACLDeclarations(ctx, tx, region)
	return nativeACLReadSnapshot{ctx: ctx, tx: tx, region: region, declared: declared, cohorts: map[string]nativeACLCohortView{}}, err
}

func (view *nativeACLReadSnapshot) cohort(vpc string) (nativeACLCohortView, error) {
	if found, ok := view.cohorts[vpc]; ok {
		return found, nil
	}
	d, byACL, err := nativeACLProjectionFromSnapshot(view.ctx, view.tx, view.region, vpc, view.declared, &view.subnets)
	if err != nil {
		return nativeACLCohortView{}, fmt.Errorf("%w: %w", errNetworkACLState, err)
	}
	c := nativeACLCohortView{d, byACL}
	view.cohorts[vpc] = c
	return c, nil
}

// A custom ACL must not be reported as detached if a stale relationship from
// any other cohort still references it. Validate the indexed count against the
// typed join in the same transaction before projecting or mutating the ACL.
func verifyNativeACLCustomReferences(ctx context.Context, tx *sql.Tx, region, id string, expected int) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_native_acl_associations
		WHERE region=? AND custom_acl_id=?`, region, id).Scan(&count); err != nil {
		return err
	}
	if count != expected {
		return errNetworkACLState
	}
	return nil
}

func (view *nativeACLReadSnapshot) customAssociations(r EC2NetworkResource) ([]ec2NetworkACLAssociationRead, error) {
	if err := nativeACLUnclaimed(view.ctx, view.tx, view.region, view.declared, r.VPCID, r.ID); err != nil {
		return nil, fmt.Errorf("%w: %w", errNetworkACLState, err)
	}
	cohort, err := view.cohort(r.VPCID)
	if err != nil {
		return nil, err
	}
	refs := cohort.byACL[r.ID]
	if err := verifyNativeACLCustomReferences(view.ctx, view.tx, view.region, r.ID, len(refs)); err != nil {
		return nil, err
	}
	return refs, nil
}

func validateDetachedNativeACLForWrite(ctx context.Context, tx *sql.Tx, acl EC2NetworkResource) error {
	view, err := newNativeACLReadSnapshot(ctx, tx, acl.Region)
	if err != nil {
		return err
	}
	if _, err := view.customAssociations(acl); err != nil {
		return err
	}
	return rejectAttachedNativeACL(ctx, tx, acl.Region, acl.ID)
}

func (view *nativeACLReadSnapshot) record(id string) (ec2NetworkACLRead, error) {
	var vpc string
	err := view.tx.QueryRowContext(view.ctx, `SELECT vpc_id FROM aws_ec2_native_acl_defaults WHERE region=? AND acl_id=?`, view.region, id).Scan(&vpc)
	if err == nil {
		cohort, err := view.cohort(vpc)
		if err != nil {
			return ec2NetworkACLRead{}, err
		}
		d := cohort.defaultACL
		if d.id != id {
			return ec2NetworkACLRead{}, errNetworkACLState
		}
		return ec2NetworkACLRead{ID: d.id, VPCID: d.vpcID, OwnerID: "000000000000", Default: true,
			Entries: nativeDefaultNetworkACLEntries(), Associations: ec2NetworkACLAssociationSet{Items: cohort.byACL[id]},
			Mode: "default-config-only"}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ec2NetworkACLRead{}, err
	}
	r, err := readNativeNetworkACL(view.ctx, view.tx, view.region, id)
	if err != nil {
		return ec2NetworkACLRead{}, err
	}
	refs, err := view.customAssociations(r)
	if err != nil {
		return ec2NetworkACLRead{}, err
	}
	record := ec2NetworkACLRecord(r)
	if len(refs) > 0 {
		record.Mode = "attached-config-only"
		record.Associations.Items = refs
	}
	return record, nil
}

func (view *nativeACLReadSnapshot) subnetACL(id string) (string, error) {
	r, err := networkRead(view.ctx, view.tx, view.region, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (r.Kind != ec2NativeSubnetKind || r.StackID != ec2NativeOwner) {
		return "", checkNativeACLSubnetBindingAbsent(view.ctx, view.tx, EC2NetworkResource{Region: view.region, ID: id})
	}
	if err != nil {
		return "", err
	}
	if _, err := nativeSubnetReadback(view.ctx, view.tx, r); err != nil {
		return "", err
	}
	c, err := view.cohort(r.VPCID)
	if err != nil {
		return "", err
	}
	if c.defaultACL.id == "" {
		return "", checkNativeACLSubnetBindingAbsent(view.ctx, view.tx, r)
	}
	for acl, refs := range c.byACL {
		for _, item := range refs {
			if item.SubnetID == r.ID {
				return acl, nil
			}
		}
	}
	return "", errNetworkACLState
}

func (view *nativeACLReadSnapshot) listIDs() ([]string, error) {
	ids, err := listNativeNetworkACLIDs(view.ctx, view.tx, view.region)
	if err != nil {
		return nil, err
	}
	rows, err := view.tx.QueryContext(view.ctx, `SELECT acl_id FROM aws_ec2_native_acl_defaults WHERE region=? ORDER BY acl_id LIMIT ?`,
		view.region, ec2NetworkACLRowLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if len(ids) >= ec2NetworkACLRowLimit {
			return nil, errNetworkACLState
		}
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		return nil, errNetworkACLState
	}
	return ids, nil
}

func (s *StackStore) describeNativeNetworkACLs(ctx context.Context, region string, q ec2NetworkACLRequest) ([]ec2NetworkACLRead, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	view, err := newNativeACLReadSnapshot(ctx, tx, region)
	if err != nil {
		return nil, err
	}
	ids := q.ids
	var subnetACL string
	if q.filterSubnet != "" {
		subnetACL, err = view.subnetACL(q.filterSubnet)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 && subnetACL != "" {
			ids = []string{subnetACL}
		}
	} else if len(ids) == 0 {
		ids, err = view.listIDs()
		if err != nil {
			return nil, err
		}
	}
	out := make([]ec2NetworkACLRead, 0, len(ids))
	for _, id := range ids {
		r, err := view.record(id)
		if err != nil {
			return nil, err
		}
		if (q.filterSubnet == "" || r.ID == subnetACL) && (len(q.filterVPCs) == 0 || slices.Contains(q.filterVPCs, r.VPCID)) {
			out = append(out, r)
		}
	}
	return out, tx.Commit()
}
