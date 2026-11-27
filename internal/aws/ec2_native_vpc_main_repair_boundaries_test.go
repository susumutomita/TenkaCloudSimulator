package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestEC2NativeMainRepairRejectsLegacyDriftBeforeWrite(t *testing.T) {
	for _, scenario := range []string{"schema-version", "missing-stack-index", "vpc-generation", "unexpected-json", "reserved-row", "regional-orphan"} {
		t.Run(scenario, func(t *testing.T) {
			f, custom := legacyMainRepairFixture(t)
			db, ctx := f.store.Database(), t.Context()
			var want string
			switch scenario {
			case "schema-version":
				nativeSubnetLineExec(t, db, `PRAGMA user_version=2;`)
				want = "expected existing version 1"
			case "missing-stack-index":
				nativeSubnetLineExec(t, db, `ALTER TABLE aws_cfn_stacks RENAME TO unavailable_fixture_stack_index;`)
				want = "no such table: aws_cfn_stacks"
			case "vpc-generation":
				nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=2,document=json_set(document,'$.Revision',2) WHERE region=? AND id=?`, repairRegion, repairVPC)
				want = "unsupported native VPC generation"
			case "unexpected-json":
				nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_insert(document,'$.UnrecognizedGenerationField',1) WHERE region=? AND id=?`, repairRegion, repairVPC)
				want = "not the exact supported schema"
			case "reserved-row":
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err := networkWrite(ctx, tx, EC2NetworkResource{ID: nativeMainTableID(repairRegion, repairVPC),
					Kind: ec2RT, StackID: ec2NativeOwner, Region: repairRegion, VPCID: repairVPC, Revision: 1}, true); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				want = "a reserved identity already exists"
			case "regional-orphan":
				nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.VPCID','vpc-00000000000000479') WHERE region=? AND id=?`, repairRegion, custom.ID)
				want = "verify regional route table inventory"
			}
			before := replaceTestInventory(t, db)
			check := func(plan EC2NativeMainRepairPlan, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), want) || plan.Status == "applied" {
					t.Fatal(want, plan, err)
				}
			}
			check(PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC))
			check(ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, strings.Repeat("b", 64),
				func() error { return nil }, func() error { t.Fatal("invalid old state reached commit admission"); return nil }))
			if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
				t.Fatal("repair rewrote unwitnessed legacy state")
			}
		})
	}
}

func TestEC2NativeMainRepairWriteDenialsRollBackActualSQLite(t *testing.T) {
	for _, failure := range []string{"parent-update", "custom-update", "main-insertion", "unbounded-unrelated-row"} {
		t.Run(failure, func(t *testing.T) {
			f, custom := legacyMainRepairFixture(t)
			db, ctx := f.store.Database(), t.Context()
			var want string
			switch failure {
			case "parent-update", "custom-update":
				id := repairVPC
				if failure == "custom-update" {
					id = custom.ID
				}
				// A local operator's incompatible additional constraint admits
				// all existing rows but rejects one real versioned UPDATE.
				nativeSubnetLineExec(t, db, fmt.Sprintf(`ALTER TABLE aws_ec2_network_resources ADD COLUMN repair_fixture_guard INTEGER NOT NULL DEFAULT 0 CHECK(id <> '%s' OR revision=1);`, id))
				want = "CHECK constraint failed"
			case "main-insertion":
				// Older reads and planned updates accept the existing rows;
				// SQLite refuses the later table INSERT on this private schema.
				nativeSubnetLineExec(t, db, `ALTER TABLE aws_ec2_network_resources ADD COLUMN repair_fixture_other INTEGER NOT NULL DEFAULT 0;`)
				want = "values were supplied"
			case "unbounded-unrelated-row":
				nativeSubnetLineExec(t, db, `INSERT INTO aws_ec2_network_resources VALUES(?,?,?,?,?,?)`, "ap-northeast-1", "vpc-00000000000000172",
					ec2NativeOwner, ec2RTVPC, strings.Repeat("x", (128<<10)+1), 1)
				want = "out-of-bounds network row census"
			}
			before := replaceTestInventory(t, db)
			plan, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC)
			if err != nil || plan.Status != "ready" {
				t.Fatal(plan, err)
			}
			if outcome, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, strings.Repeat("3", 64),
				func() error { return nil }, func() error { t.Fatal("a failed SQL operation reached final approval"); return nil }); err == nil || !strings.Contains(err.Error(), want) || outcome.Status == "applied" {
				t.Fatal(want, outcome, err)
			}
			if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
				t.Fatal("a real SQLite failure left earlier repair writes durable")
			}
		})
	}
}

func TestEC2NativeMainRepairReusesNoExpiredWitnessOrWritableGeneration(t *testing.T) {
	f, _ := legacyMainRepairFixture(t)
	db, ctx := f.store.Database(), t.Context()
	before := replaceTestInventory(t, db)
	digest := strings.Repeat("4", 64)
	good := func() error { return nil }
	if result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, "not-a-witness", good, good); err == nil || result.Status != "" {
		t.Fatal(result, err)
	}
	if result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, digest, nil, good); err == nil || result.Status != "" {
		t.Fatal(result, err)
	}
	if plan, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, "vpc-*"); err == nil || plan.Status != "" {
		t.Fatal(plan, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if plan, err := PlanEC2NativeMainRouteRepair(canceled, db, repairRegion, repairVPC); !errors.Is(err, context.Canceled) || plan.Status != "" {
		t.Fatal(plan, err)
	}
	if result, err := ApplyEC2NativeMainRouteRepair(canceled, db, repairRegion, repairVPC, digest, good, good); !errors.Is(err, context.Canceled) || result.Status != "" {
		t.Fatal(result, err)
	}
	failure := errors.New("the stopped source digest changed at admission")
	if result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, digest, func() error { return failure }, good); !errors.Is(err, failure) || result.Status != "" {
		t.Fatal(result, err)
	}
	within, stop := context.WithCancel(ctx)
	defer stop()
	if result, err := ApplyEC2NativeMainRouteRepair(within, db, repairRegion, repairVPC, digest, good,
		func() error { stop(); return nil }); result.Status != "" || !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("a rejected source or canceled commit left durable changes")
	}
	if applied, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, digest, good, good); err != nil || applied.Status != "applied" {
		t.Fatal(applied, err)
	}
	retained := replaceTestInventory(t, db)
	if result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, digest, good, good); err == nil || result.Status != "" || !strings.Contains(err.Error(), "did not select an unmarked legacy VPC") {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(retained, replaceTestInventory(t, db)) {
		t.Fatal("the lower-level writer retried a marked VPC")
	}
}

func TestEC2NativeMainRepairReadbackRefusesDamagedMainEvidence(t *testing.T) {
	for _, damage := range []string{"wrong-slot", "noncanonical-cidr", "claimed-table", "occupied-association-id"} {
		t.Run(damage, func(t *testing.T) {
			f := newEC2VPCFixture(t)
			db, ctx := f.store.Database(), t.Context()
			id := f.create("", url.Values{"CidrBlock": {repairCIDR}}).ID
			main := nativeMainTableID(repairRegion, id)
			primary := nativeMainAssociationID(repairRegion, id)
			switch damage {
			case "wrong-slot":
				nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.MainRouteTableID','rtb-fffffffffffffffff') WHERE region=? AND id=?`, repairRegion, id)
			case "noncanonical-cidr":
				nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.CIDR','10.190.0.19/16') WHERE region=? AND id=?`, repairRegion, id)
			case "claimed-table":
				stack := subnetSettingsEvidenceStack(t, f)
				nativeSubnetLineExec(t, db, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(repairRegion, ec2RT), ec2RT, main, stack.ID)
			case "occupied-association-id":
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err := networkWrite(ctx, tx, EC2NetworkResource{ID: primary, Kind: ec2Assoc, StackID: ec2NativeOwner,
					Region: repairRegion, VPCID: id, RouteTableID: main, Revision: 1}, true); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			before := replaceTestInventory(t, db)
			if plan, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, id); err == nil || plan.Status != "" {
				t.Fatal("replanning accepted damaged current ownership", plan, err)
			}
			nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {main}})
			if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
				t.Fatal("plan or discovery rewrote a damaged main witness")
			}
		})
	}
}

func TestEC2NativeMainRepairRefusesIncompleteOrRetiredPreparedState(t *testing.T) {
	for _, scenario := range []string{"incomplete-custom-set", "late-retired-slot", "late-declared-slot"} {
		t.Run(scenario, func(t *testing.T) {
			f, _ := legacyMainRepairFixture(t)
			db, ctx := f.store.Database(), t.Context()
			stack := subnetSettingsEvidenceStack(t, f)
			before := replaceTestInventory(t, db)
			originalStack := subnetSettingsEvidenceDocument(t, f, stack.ID)
			tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelLinearizable})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			planned, err := inspectNativeMainRepair(ctx, tx, repairRegion, repairVPC)
			if err != nil || planned.plan.Status != "ready" || len(planned.tables) != 1 {
				t.Fatal(planned.plan, err)
			}
			var want string
			switch scenario {
			case "incomplete-custom-set":
				// The final production verifier must not trust a partial
				// prepared set, even if its staged byte census agrees.
				planned.tables = nil
				want = "final ownership/projection was not verified"
			case "late-retired-slot":
				hash := make([]byte, 32)
				if _, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_route_table_replay VALUES(?,?,?,?)`, repairRegion, hash, hash,
					planned.plan.MainRouteTableID); err != nil {
					t.Fatal(err)
				}
			case "late-declared-slot":
				if _, err := tx.ExecContext(ctx, `UPDATE aws_cfn_stacks SET document=json_insert(document,'$.Resources[#]',
					json_object('LogicalID','LateReservedMain','Type',?, 'PhysicalID',?)) WHERE id=?`, ec2RT, planned.plan.MainRouteTableID, stack.ID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := applyNativeMainRepairSelection(ctx, tx, planned, strings.Repeat("5", 64),
				func() error { t.Fatal("invalid staged evidence reached commit"); return nil })
			if result.Status != "" || err == nil || want != "" && !strings.Contains(err.Error(), want) || want == "" && !errors.Is(err, errEC2NativeRouteTableState) {
				t.Fatal(result, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var retired bool
			if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ec2_native_route_table_replay WHERE region=? AND route_table_id=?)`,
				repairRegion, planned.plan.MainRouteTableID).Scan(&retired); err != nil || retired {
				t.Fatal(retired, err)
			}
			if !reflect.DeepEqual(before, replaceTestInventory(t, db)) || originalStack != subnetSettingsEvidenceDocument(t, f, stack.ID) {
				t.Fatal("a late rejected condition left state changed")
			}
		})
	}
}
