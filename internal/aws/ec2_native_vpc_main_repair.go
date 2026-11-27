package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
)

const EC2NativeMainRepairFormat = "ec2-native-main-route/v1"

var (
	nativeMainRepairDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nativeMainRepairVPC    = regexp.MustCompile(`^vpc-[0-9a-f]{17}$`)
	nativeMainRepairRegion = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
	errNativeMainRepair    = errors.New("legacy native main route repair refused")
)

type nativeVPCMainRepairMarker struct {
	Version    int
	PlanDigest string
}

func validNativeVPCMainRepair(v EC2NetworkResource) bool {
	m := v.MainRouteRepair
	return m != nil && m.Version == 1 && nativeMainRepairDigest.MatchString(m.PlanDigest) && v.Kind == ec2RTVPC &&
		v.StackID == ec2NativeOwner && v.Revision == 2 && nativeMainRepairVPC.MatchString(v.ID) &&
		v.MainRouteTableID == nativeMainTableID(v.Region, v.ID)
}

// EC2NativeMainRepairPlan describes exactly one stopped local state scope.
// It is not an AWS IAM operation or a public/server mutation surface.
type EC2NativeMainRepairPlan struct {
	Format              string   `json:"format"`
	Status              string   `json:"status"`
	Region              string   `json:"region"`
	VPCID               string   `json:"vpcId"`
	VPCRevision         int64    `json:"vpcRevision"`
	MainRouteTableID    string   `json:"mainRouteTableId"`
	MainAssociationID   string   `json:"mainAssociationId"`
	CustomRouteTableIDs []string `json:"customRouteTableIds"`
	ExtraPersistedRows  int      `json:"extraPersistedRows"`
	PlanDigest          string   `json:"planDigest,omitempty"`
}

type nativeMainRepairSelection struct {
	plan   EC2NativeMainRepairPlan
	vpc    EC2NetworkResource
	tables []EC2NetworkResource
}

func ValidateEC2NativeMainRepairScope(region, vpc string) error {
	if len(region) > 64 || !nativeMainRepairRegion.MatchString(region) || !nativeMainRepairVPC.MatchString(vpc) {
		return fmt.Errorf("%w: choose one exact region and native VPC ID", errNativeMainRepair)
	}
	return nil
}

func inspectNativeMainRepair(ctx context.Context, tx *sql.Tx, region, id string) (nativeMainRepairSelection, error) {
	var out nativeMainRepairSelection
	if err := ValidateEC2NativeMainRepairScope(region, id); err != nil {
		return out, err
	}
	var v int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != 1 {
		return out, fmt.Errorf("%w: expected existing version 1 simulator state", errNativeMainRepair)
	}
	// This command changes existing core rows, not operator-installed schema.
	// Do not allow a trigger on those rows to run additional, unplanned writes.
	var triggers bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM sqlite_schema WHERE type='trigger' AND lower(tbl_name)='aws_ec2_network_resources'
		UNION ALL SELECT 1 FROM sqlite_temp_schema WHERE type='trigger' AND lower(tbl_name)='aws_ec2_network_resources')`).Scan(&triggers); err != nil || triggers {
		return out, fmt.Errorf("%w: unexpected network table trigger", errNativeMainRepair)
	}
	// The version-1 SG tables reference immutable (region,id) with no update
	// action. A different DB can report the same user_version yet attach a
	// cascade to document/revision. Never let repair rewrite those dependents.
	var incompatibleReferences bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM sqlite_schema AS s JOIN pragma_foreign_key_list(s.name, 'main') AS fk
		WHERE s.type='table' AND lower(fk."table")='aws_ec2_network_resources'
		AND (lower(coalesce(fk."to",'')) NOT IN ('region','id') OR upper(fk.on_update) NOT IN ('NO ACTION','RESTRICT'))
	)`).Scan(&incompatibleReferences); err != nil || incompatibleReferences {
		return out, fmt.Errorf("%w: unsupported dependent network foreign key", errNativeMainRepair)
	}
	_, declarations, err := regionalGatewayStacks(ctx, tx, region)
	if err != nil {
		return out, err
	}
	vpc, err := nativeRouteTableVPC(ctx, tx, region, id, &declarations)
	if err != nil {
		return out, err
	}
	if vpc.Revision != 1 && !validNativeVPCMainRepair(vpc) {
		return out, fmt.Errorf("%w: unsupported native VPC generation", errNativeMainRepair)
	}
	out.vpc = vpc
	main, assoc := nativeMainTableID(region, id), nativeMainAssociationID(region, id)
	out.plan = EC2NativeMainRepairPlan{Format: EC2NativeMainRepairFormat, Status: "ready", Region: region,
		VPCID: id, VPCRevision: vpc.Revision, MainRouteTableID: main, MainAssociationID: assoc, CustomRouteTableIDs: []string{}}
	if vpc.MainRouteTableID != "" {
		out.plan.Status = "already-native"
		if vpc.MainRouteRepair != nil {
			out.plan.Status, out.plan.PlanDigest = "already-repaired", vpc.MainRouteRepair.PlanDigest
		}
	} else {
		out.plan.ExtraPersistedRows = 1
		for _, reserved := range []string{main, assoc} {
			if _, err := networkRead(ctx, tx, region, reserved); !errors.Is(err, sql.ErrNoRows) {
				return out, fmt.Errorf("%w: a reserved identity already exists", errNativeMainRepair)
			}
			if err := nativeRouteTableUnclaimed(ctx, tx, region, reserved); err != nil {
				return out, err
			}
		}
		reservedOwner := vpc
		reservedOwner.MainRouteTableID = main
		if err := checkNativeVPCMainDeclarations(reservedOwner, declarations); err != nil {
			return out, err
		}
		var retired bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_route_table_replay WHERE region=? AND route_table_id=?)`, region, main).Scan(&retired); err != nil || retired {
			return out, fmt.Errorf("%w: reserved table cannot reuse replay history", errNativeMainRepair)
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); err != nil || count >= ec2NativeRouteTableRowLimit {
			return out, fmt.Errorf("%w: no free slot under the local 1000-row network bound", errNativeMainRepair)
		}
		if err := exactNativeRepairDocument(ctx, tx, vpc); err != nil {
			return out, err
		}
	}
	// Validate all native/CFN route ownership, explicit associations and static
	// route limits together, before accepting any legacy table or empty slot.
	projection, err := describeEC2RouteTablesInSnapshot(ctx, tx, region)
	if err != nil {
		return out, fmt.Errorf("verify regional route table inventory: %w", err)
	}
	for _, observed := range projection {
		if observed.VPC != id || observed.ID == main {
			continue
		}
		table, err := readNativeRouteTableInSnapshot(ctx, tx, region, observed.ID, &declarations)
		if err != nil {
			return out, fmt.Errorf("verify selected saved route table: %w", err)
		}
		if table.VPCID != id {
			return out, fmt.Errorf("%w: a selected route table has no verified native owner", errNativeMainRepair)
		}
		if out.plan.Status == "ready" {
			if table.Revision != 1 || table.CIDR != "" {
				return out, fmt.Errorf("%w: unsupported legacy table generation", errNativeMainRepair)
			}
			if err := exactNativeRepairDocument(ctx, tx, table); err != nil {
				return out, err
			}
		}
		out.plan.CustomRouteTableIDs = append(out.plan.CustomRouteTableIDs, table.ID)
		out.tables = append(out.tables, table)
	}
	slices.Sort(out.plan.CustomRouteTableIDs)
	return out, nil
}

// Do not silently drop unknown/duplicate JSON fields when moving earlier
// documents onto this version. Earlier supported writers emitted this shape.
func exactNativeRepairDocument(ctx context.Context, tx *sql.Tx, row EC2NetworkResource) error {
	var raw string
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, row.Region, row.ID).Scan(&raw); err != nil || raw != string(encoded) {
		return fmt.Errorf("%w: legacy network document is not the exact supported schema", errNativeMainRepair)
	}
	return nil
}

type mainRepairTransactions interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func PlanEC2NativeMainRouteRepair(ctx context.Context, db mainRepairTransactions, region, vpc string) (EC2NativeMainRepairPlan, error) {
	// The CLI opens mode=ro, whose default transaction is deferred. Do not
	// toggle the pinned driver's per-connection query_only reset; the helper
	// uses SELECTs only and never bootstraps schema or commits a write.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EC2NativeMainRepairPlan{}, err
	}
	defer tx.Rollback()
	selection, err := inspectNativeMainRepair(ctx, tx, region, vpc)
	return selection.plan, err
}

type nativeRepairRowKey struct{ Region, ID string }

func nativeRepairRowDigest(kind, owner string, revision int64, document string) [32]byte {
	payload, _ := json.Marshal([4]any{kind, owner, revision, document})
	return sha256.Sum256(payload)
}

func nativeRepairNetworkWitness(ctx context.Context, tx *sql.Tx) (map[nativeRepairRowKey][32]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT region,id,kind,stack_id,revision,document FROM aws_ec2_network_resources LIMIT 1001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[nativeRepairRowKey][32]byte{}
	for rows.Next() {
		var key nativeRepairRowKey
		var kind, owner, document string
		var revision int64
		if err := rows.Scan(&key.Region, &key.ID, &kind, &owner, &revision, &document); err != nil {
			return nil, err
		}
		if _, duplicate := out[key]; duplicate || len(out) == ec2NativeRouteTableRowLimit || len(document) > 128<<10 {
			return nil, fmt.Errorf("%w: out-of-bounds network row census", errNativeMainRepair)
		}
		out[key] = nativeRepairRowDigest(kind, owner, revision, document)
	}
	return out, rows.Err()
}

// ApplyEC2NativeMainRouteRepair is used only by the offline CLI. It requires
// a writable existing DB and caller-held file witnesses before any mutation
// and just before commit. The pinned SQLite driver maps Linearizable to
// BEGIN EXCLUSIVE (the CLI admits only closed rollback-journal state).
func ApplyEC2NativeMainRouteRepair(ctx context.Context, db mainRepairTransactions, region, vpc, planDigest string, before, final func() error) (EC2NativeMainRepairPlan, error) {
	if !nativeMainRepairDigest.MatchString(planDigest) || before == nil || final == nil {
		return EC2NativeMainRepairPlan{}, errNativeMainRepair
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelLinearizable})
	if err != nil {
		return EC2NativeMainRepairPlan{}, err
	}
	defer tx.Rollback()
	if err := before(); err != nil {
		return EC2NativeMainRepairPlan{}, err
	}
	selection, err := inspectNativeMainRepair(ctx, tx, region, vpc)
	if err != nil {
		return EC2NativeMainRepairPlan{}, err
	}
	if selection.plan.Status != "ready" {
		return EC2NativeMainRepairPlan{}, fmt.Errorf("%w: this write did not select an unmarked legacy VPC", errNativeMainRepair)
	}
	return applyNativeMainRepairSelection(ctx, tx, selection, planDigest, final)
}

func applyNativeMainRepairSelection(ctx context.Context, tx *sql.Tx, selected nativeMainRepairSelection, planDigest string, final func() error) (EC2NativeMainRepairPlan, error) {
	var empty EC2NativeMainRepairPlan
	expected, err := nativeRepairNetworkWitness(ctx, tx)
	if err != nil {
		return empty, err
	}
	remember := func(row EC2NetworkResource) error {
		document, err := json.Marshal(row)
		if err == nil {
			expected[nativeRepairRowKey{row.Region, row.ID}] = nativeRepairRowDigest(row.Kind, row.StackID, row.Revision, string(document))
		}
		return err
	}
	parent := selected.vpc
	parent.MainRouteTableID = selected.plan.MainRouteTableID
	parent.MainRouteRepair = &nativeVPCMainRepairMarker{Version: 1, PlanDigest: planDigest}
	parent.Revision++
	if err := networkWrite(ctx, tx, parent, false); err != nil {
		return empty, err
	}
	if err := remember(parent); err != nil {
		return empty, err
	}
	for _, table := range selected.tables {
		table.CIDR = parent.CIDR
		table.Revision++
		if err := networkWrite(ctx, tx, table, false); err != nil {
			return empty, err
		}
		if err := remember(table); err != nil {
			return empty, err
		}
	}
	if err := createNativeVPCMainTable(ctx, tx, parent); err != nil {
		return empty, err
	}
	if err := remember(EC2NetworkResource{ID: parent.MainRouteTableID, Kind: ec2RT, Region: parent.Region,
		StackID: ec2NativeOwner, VPCID: parent.ID, CIDR: parent.CIDR, Revision: 1}); err != nil {
		return empty, err
	}
	if actual, err := nativeRepairNetworkWitness(ctx, tx); err != nil || !maps.Equal(expected, actual) {
		return empty, fmt.Errorf("%w: final network rows differ from the plan", errNativeMainRepair)
	}
	verified, err := inspectNativeMainRepair(ctx, tx, parent.Region, parent.ID)
	if err != nil || verified.plan.Status != "already-repaired" || verified.plan.PlanDigest != planDigest ||
		!slices.Equal(verified.plan.CustomRouteTableIDs, selected.plan.CustomRouteTableIDs) {
		return empty, fmt.Errorf("%w: final ownership/projection was not verified", errNativeMainRepair)
	}
	if err := final(); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	verified.plan.Status, verified.plan.ExtraPersistedRows = "applied", 1
	return verified.plan, nil
}
