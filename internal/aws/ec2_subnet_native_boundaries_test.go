package aws

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEC2NativeSubnetBoundaries(t *testing.T) {
	state, _ := s3TestState(t)
	store, err := NewStackStore(state.Database())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const region = "us-east-1"
	vpc, err := store.createNativeVPC(ctx, region, "10.102.0.0/16", nil)
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := store.createNativeSubnet(ctx, region, ec2SubnetMutation{vpc: vpc.ID, cidr: "10.102.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(r EC2NetworkResource, insert bool) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := networkWrite(ctx, tx, r, insert); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	readAll := func(action string) {
		t.Helper()
		if _, err := store.ec2Read(ctx, region, action); err == nil {
			t.Fatalf("%s converted corrupt ownership into an apparently valid snapshot", action)
		}
	}
	code := func(err error, want string) {
		t.Helper()
		var f *ec2Failure
		if !errors.As(err, &f) || f.code != want {
			t.Fatalf("got %v, want %s", err, want)
		}
	}
	// A trusted topology provider may write a dependency before another scoped
	// request arrives. The subnet must survive, not orphan native graph state.
	witness := EC2NetworkResource{Region: region, ID: "rtbassoc-" + strings.Repeat("8", 17), Kind: "AWS::EC2::SubnetRouteTableAssociation", StackID: ec2NativeOwner, VPCID: vpc.ID, SubnetID: subnet.ID, Revision: 1}
	mutate(witness, true)
	code(store.deleteNativeSubnet(ctx, region, subnet.ID), "DependencyViolation")
	if report, err := store.ec2Read(ctx, region, "DescribeSubnets"); err != nil || len(report.Subnets.Items) != 1 {
		t.Fatal("dependency refusal lost the subnet")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, witness.ID); err != nil {
		t.Fatal(err)
	}

	// A damaged row must fail discovery, not display a usable native VPC or
	// incorrectly accept removal on the basis of a partial/inconsistent read.
	row := EC2NetworkResource{Region: region, ID: subnet.ID, Kind: ec2NativeSubnetKind, StackID: ec2NativeOwner, VPCID: vpc.ID, CIDR: subnet.CIDR, AvailabilityZone: region + "a", Revision: 2}
	for _, invalid := range []func(*EC2NetworkResource){
		func(r *EC2NetworkResource) { r.AvailabilityZone = "us-west-2a" },
		func(r *EC2NetworkResource) { r.CIDR = "10.102.1.15/24" },
		func(r *EC2NetworkResource) { r.VPCID = "vpc-" + strings.Repeat("2", 17) },
	} {
		bad := row
		invalid(&bad)
		mutate(bad, false)
		readAll("DescribeSubnets")
		readAll("DescribeVpcs")
		if err := store.deleteNativeSubnet(ctx, region, subnet.ID); err == nil {
			t.Fatal("removed invalid persisted row")
		}
		row.Revision++
		mutate(row, false)
		row.Revision++
	}

	// An explicit stack claim of the same physical ID is corrupt state. APIs
	// refuse rather than allowing a native mutation to adopt stack ownership.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, region, "test-claim", "stack-claim", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(region, ec2NativeSubnetKind), "AWS::EC2::VPC", vpc.ID, "stack-claim"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createNativeSubnet(ctx, region, ec2SubnetMutation{vpc: vpc.ID, cidr: "10.102.2.0/24"}); err == nil {
		t.Fatal("adopted a stack-claimed native VPC")
	}
	readAll("DescribeSubnets")
	readAll("DescribeVpcs")
	if _, err := store.db.ExecContext(ctx, `DELETE FROM aws_cfn_ownership WHERE stack_id='stack-claim'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM aws_cfn_stacks WHERE id='stack-claim'`); err != nil {
		t.Fatal(err)
	}

	// Whole-store quota also includes networks in other regions. Populate the
	// remaining slots with typed fixtures in the shared store, not a shadow quota.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Each row is a bounded typed VPC fixture. The source of truth and count
	// still come from the actual shared SQLite table.
	if _, err := tx.ExecContext(ctx, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<997)
INSERT INTO aws_ec2_network_resources(region,id,stack_id,kind,document,revision)
SELECT 'us-west-2',printf('vpc-%017x',i),'ec2:native','AWS::EC2::VPC',
 json_object('Region','us-west-2','ID',printf('vpc-%017x',i),'StackID','ec2:native','Kind','AWS::EC2::VPC','CIDR','10.200.0.0/16','Revision',1),1 FROM n`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = store.createNativeSubnet(ctx, region, ec2SubnetMutation{vpc: vpc.ID, cidr: "10.102.2.0/24"})
	code(err, "SubnetLimitExceeded")
	if report, err := store.ec2Read(ctx, region, "DescribeSubnets"); err != nil || len(report.Subnets.Items) != 1 {
		t.Fatal("quota failure mutated native state")
	}

	// Cancellation before the SQLite transaction exposes no success receipt.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.createNativeSubnet(canceled, region, ec2SubnetMutation{vpc: vpc.ID, cidr: "10.102.2.0/24"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("create cancellation: %v", err)
	}
	if err := store.deleteNativeSubnet(canceled, region, subnet.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete cancellation: %v", err)
	}
	w := httptest.NewRecorder()
	(&Gateway{}).failEC2Mutation(w, context.Canceled)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "nothing was committed") {
		t.Fatal("unexpected cancellation receipt")
	}
	if err := store.deleteNativeSubnet(ctx, region, subnet.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.deleteNativeVPC(ctx, region, vpc.ID); err != nil {
		t.Fatal(err)
	}
}
