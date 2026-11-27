package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const repairRegion = "us-east-1"
const repairVPC = "vpc-00000000000000171"
const repairCIDR = "10.190.0.0/16"

func legacyMainRepairFixture(t *testing.T) (ec2VPCFixture, EC2NetworkResource) {
	t.Helper()
	f := newEC2VPCFixture(t)
	ctx := t.Context()
	tx, err := f.store.Database().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := createEC2Network(ctx, tx, EC2NetworkResource{ID: repairVPC, Kind: ec2RTVPC, Region: repairRegion,
		StackID: ec2NativeOwner, CIDR: repairCIDR, DNSSupport: true, Tags: map[string]string{"Name": "Legacy VPC"}, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	custom, err := f.stacks.createNativeRouteTable(ctx, repairRegion, ec2NativeRouteTableQuery{vpc: repairVPC,
		tags: map[string]string{"Name": "Legacy custom table"}, clientToken: "retained-legacy-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	return f, custom
}

func TestEC2NativeMainRepairRollbackAfterStagedGraph(t *testing.T) {
	f, custom := legacyMainRepairFixture(t)
	db, ctx := f.store.Database(), t.Context()
	digest := strings.Repeat("d", 64)
	before := replaceTestInventory(t, db)
	plan, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC)
	if err != nil || plan.Status != "ready" || plan.ExtraPersistedRows != 1 || !slices.Equal(plan.CustomRouteTableIDs, []string{custom.ID}) {
		t.Fatal(plan, err)
	}
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("planning changed saved rows")
	}
	for _, cancelLate := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel-%t", cancelLate), func(t *testing.T) {
			late := errors.New("late stopped-copy witness refused")
			{ // A genuinely staged transaction with observable pending writes.
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelLinearizable})
				if err != nil {
					t.Fatal(err)
				}
				selected, err := inspectNativeMainRepair(ctx, tx, repairRegion, repairVPC)
				if err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				var observed bool
				verifyThenRefuse := func() error {
					parent, e := networkRead(ctx, tx, repairRegion, repairVPC)
					main, m := networkRead(ctx, tx, repairRegion, plan.MainRouteTableID)
					child, c := networkRead(ctx, tx, repairRegion, custom.ID)
					observed = e == nil && m == nil && c == nil && validNativeVPCMainRepair(parent) &&
						isNativeMainTable(main) && main.Revision == 1 && child.Revision == 2 && child.CIDR == repairCIDR
					if cancelLate {
						cancel()
						return ctx.Err()
					}
					return late
				}
				_, err = applyNativeMainRepairSelection(ctx, tx, selected, digest, verifyThenRefuse)
				_ = tx.Rollback()
				if !observed || (!cancelLate && !errors.Is(err, late)) || (cancelLate && !errors.Is(err, context.Canceled)) {
					t.Fatal(observed, err)
				}
			}
			if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
				t.Fatal("a refused staged transaction left durable changes")
			}
		})
	}
	allow := func() error { return nil }
	result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, digest, allow, allow)
	if err != nil || result.Status != "applied" || result.VPCRevision != 2 || result.PlanDigest != digest {
		t.Fatal(result, err)
	}
	if got := nativeMainRouteWire(t, f, repairRegion, url.Values{}); len(got) != 2 || !slices.Equal(got[0].Routes, []ec2RouteRead{nativeLocalRouteFixture(repairCIDR)}) ||
		!slices.Equal(got[1].Routes, got[0].Routes) {
		t.Fatal(got)
	}
	current, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC)
	if err != nil || current.Status != "already-repaired" || current.PlanDigest != digest || current.ExtraPersistedRows != 0 {
		t.Fatal(current, err)
	}
	f.expect(f.call("", repairRegion, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {result.MainRouteTableID}}), 400, "DependencyViolation")
	f.expect(f.call("", repairRegion, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {custom.ID}}), 200, "")
	f.expect(f.call("", repairRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {repairVPC}}), 200, "")
	if after := replaceTestInventory(t, db); len(after) != 0 {
		t.Fatal("repaired VPC cleanup left route resources", len(after))
	}
}

func TestEC2NativeMainRepairRefusesUnwitnessedState(t *testing.T) {
	for _, scenario := range []string{"trigger", "foreign-claim", "typed-declaration", "retired-main", "unknown-old-fields", "quota", "missing-vpc"} {
		t.Run(scenario, func(t *testing.T) {
			f, custom := legacyMainRepairFixture(t)
			db, ctx := f.store.Database(), t.Context()
			main, selected := nativeMainTableID(repairRegion, repairVPC), repairVPC
			switch scenario {
			case "trigger":
				nativeSubnetLineExec(t, db, `CREATE TRIGGER local_network_stop AFTER UPDATE ON aws_ec2_network_resources
					BEGIN SELECT RAISE(ABORT,'operator schema is outside the supported repair'); END;`)
			case "foreign-claim", "typed-declaration":
				stack := subnetSettingsEvidenceStack(t, f)
				if scenario == "foreign-claim" {
					nativeSubnetLineExec(t, db, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(repairRegion, ec2RT), ec2RT, main, stack.ID)
				} else {
					nativeSubnetLineExec(t, db, `UPDATE aws_cfn_stacks SET document=json_insert(document,'$.Resources[#]',
						json_object('LogicalID','ReservedMain','Type',?, 'PhysicalID',?)) WHERE id=?`, ec2RT, main, stack.ID)
				}
			case "retired-main":
				hash := make([]byte, 32)
				nativeSubnetLineExec(t, db, `INSERT INTO aws_ec2_native_route_table_replay VALUES(?,?,?,?)`, repairRegion, hash, hash, main)
			case "unknown-old-fields":
				nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_insert(document,'$.OlderPrivateProperty',1) WHERE region=? AND id=?`, repairRegion, custom.ID)
			case "quota":
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for i := range ec2NativeRouteTableRowLimit - 2 {
					if err := networkWrite(ctx, tx, EC2NetworkResource{ID: fmt.Sprintf("vpc-%017x", i), Kind: ec2RTVPC,
						Region: "ap-northeast-1", StackID: ec2NativeOwner, CIDR: "10.0.0.0/16", Revision: 1}, true); err != nil {
						t.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "missing-vpc":
				selected = "vpc-00000000000000172"
			}
			before := replaceTestInventory(t, db)
			if ready, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, selected); err == nil {
				t.Fatal("plan accepted unsupported state", ready)
			}
			var finalCalled bool
			if result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, selected, strings.Repeat("9", 64),
				func() error { return nil }, func() error { finalCalled = true; return nil }); err == nil {
				t.Fatal("apply accepted unsupported state", result)
			}
			if finalCalled || !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
				t.Fatal("a refused precondition reached mutation verification or changed the database")
			}
		})
	}
}

func TestEC2NativeMainRepairPreservesDependentRowsOnSchemaRefusal(t *testing.T) {
	f, _ := legacyMainRepairFixture(t)
	db, ctx := f.store.Database(), t.Context()
	nativeSubnetLineExec(t, db, `CREATE UNIQUE INDEX repair_fixture_network_generation ON aws_ec2_network_resources(region,id,revision);`)
	nativeSubnetLineExec(t, db, `CREATE TABLE repair_fixture_retained(
		region TEXT NOT NULL, id TEXT NOT NULL, generation INTEGER NOT NULL,
		FOREIGN KEY(region,id,generation) REFERENCES aws_ec2_network_resources(region,id,revision) ON UPDATE CASCADE);`)
	nativeSubnetLineExec(t, db, `INSERT INTO repair_fixture_retained VALUES(?,?,1)`, repairRegion, repairVPC)
	before := replaceTestInventory(t, db)
	refusal := "unsupported dependent network foreign key"
	if plan, err := PlanEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatal(plan, err)
	}
	if result, err := ApplyEC2NativeMainRouteRepair(ctx, db, repairRegion, repairVPC, strings.Repeat("2", 64),
		func() error { return nil }, func() error { t.Fatal("unexpected commit stage"); return nil }); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatal(result, err)
	}
	var savedRegion, id string
	var generation int
	if err := db.QueryRowContext(ctx, `SELECT region,id,generation FROM repair_fixture_retained`).Scan(&savedRegion, &id, &generation); err != nil ||
		savedRegion != repairRegion || id != repairVPC || generation != 1 || !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("unsupported schema or network rows changed", savedRegion, id, generation, err)
	}
}
