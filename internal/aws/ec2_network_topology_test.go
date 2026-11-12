package aws

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"testing"
)

func createNetworkResource(db *sql.DB, r EC2NetworkResource) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = createEC2Network(context.Background(), tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

// buildPublicTopology creates one public subnet routed through an attached
// gateway, plus a second VPC whose resources must never mix with the first.
func buildPublicTopology(t *testing.T, db *sql.DB) *EC2NetworkStore {
	t.Helper()
	store, err := NewEC2NetworkStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []EC2NetworkResource{
		{ID: "vpc-a", Kind: "AWS::EC2::VPC", CIDR: "10.0.0.0/16"},
		{ID: "vpc-b", Kind: "AWS::EC2::VPC", CIDR: "10.1.0.0/16"},
		{ID: "igw-a", Kind: "AWS::EC2::InternetGateway"},
		{ID: "igw-b", Kind: "AWS::EC2::InternetGateway"},
		{ID: "subnet-a", Kind: "AWS::EC2::Subnet", VPCID: "vpc-a", CIDR: "10.0.1.0/24", PublicIP: true},
		{ID: "rtb-a", Kind: "AWS::EC2::RouteTable", VPCID: "vpc-a"},
		{ID: "rtb-b", Kind: "AWS::EC2::RouteTable", VPCID: "vpc-b"},
		{ID: "sg-a", Kind: "AWS::EC2::SecurityGroup", VPCID: "vpc-a", Ingress: []EC2IngressRule{{Protocol: "tcp", FromPort: 80, ToPort: 80, CIDR: "0.0.0.0/0"}}},
		{ID: "sg-b", Kind: "AWS::EC2::SecurityGroup", VPCID: "vpc-b"},
		{ID: "attach-a", Kind: "AWS::EC2::VPCGatewayAttachment", VPCID: "vpc-a", GatewayID: "igw-a"},
		{ID: "route-a", Kind: "AWS::EC2::Route", RouteTableID: "rtb-a", GatewayID: "igw-a", CIDR: "0.0.0.0/0", RouteEnabled: true},
		{ID: "assoc-a", Kind: "AWS::EC2::SubnetRouteTableAssociation", SubnetID: "subnet-a", RouteTableID: "rtb-a"},
	} {
		r.Region, r.StackID = "us-east-1", "stack-a"
		if err := createNetworkResource(db, r); err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
	}
	return store
}

func TestEC2NetworkTopologyRefusesInconsistentResources(t *testing.T) {
	db := newFaultDB(t)
	buildPublicTopology(t, db.DB)
	if err := createNetworkResource(db.DB, EC2NetworkResource{ID: "vpc-foreign", Kind: "AWS::EC2::VPC", Region: "us-east-1", StackID: "stack-b", CIDR: "10.9.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	before := db.dump(t)
	for name, r := range map[string]EC2NetworkResource{
		"subnet outside VPC":        {ID: "subnet-x", Kind: "AWS::EC2::Subnet", VPCID: "vpc-a", CIDR: "10.5.0.0/24"},
		"subnet wider than VPC":     {ID: "subnet-x", Kind: "AWS::EC2::Subnet", VPCID: "vpc-a", CIDR: "10.0.0.0/8"},
		"overlapping subnet":        {ID: "subnet-x", Kind: "AWS::EC2::Subnet", VPCID: "vpc-a", CIDR: "10.0.1.128/25"},
		"foreign-owned VPC":         {ID: "subnet-x", Kind: "AWS::EC2::Subnet", VPCID: "vpc-foreign", CIDR: "10.9.1.0/24"},
		"parent of wrong kind":      {ID: "sg-x", Kind: "AWS::EC2::SecurityGroup", VPCID: "igw-a"},
		"missing parent":            {ID: "rtb-x", Kind: "AWS::EC2::RouteTable", VPCID: "vpc-missing"},
		"attachment to non-IGW":     {ID: "attach-x", Kind: "AWS::EC2::VPCGatewayAttachment", VPCID: "vpc-b", GatewayID: "vpc-a"},
		"VPC already attached":      {ID: "attach-x", Kind: "AWS::EC2::VPCGatewayAttachment", VPCID: "vpc-a", GatewayID: "igw-b"},
		"gateway already attached":  {ID: "attach-x", Kind: "AWS::EC2::VPCGatewayAttachment", VPCID: "vpc-b", GatewayID: "igw-a"},
		"route without attachment":  {ID: "route-x", Kind: "AWS::EC2::Route", RouteTableID: "rtb-b", GatewayID: "igw-b", CIDR: "0.0.0.0/0"},
		"route to non-IGW":          {ID: "route-x", Kind: "AWS::EC2::Route", RouteTableID: "rtb-a", GatewayID: "sg-a", CIDR: "0.0.0.0/0"},
		"route via non-table":       {ID: "route-x", Kind: "AWS::EC2::Route", RouteTableID: "subnet-a", GatewayID: "igw-a", CIDR: "0.0.0.0/0"},
		"duplicate destination":     {ID: "route-x", Kind: "AWS::EC2::Route", RouteTableID: "rtb-a", GatewayID: "igw-a", CIDR: "0.0.0.0/0"},
		"cross-VPC association":     {ID: "assoc-x", Kind: "AWS::EC2::SubnetRouteTableAssociation", SubnetID: "subnet-a", RouteTableID: "rtb-b"},
		"association to non-table":  {ID: "assoc-x", Kind: "AWS::EC2::SubnetRouteTableAssociation", SubnetID: "subnet-a", RouteTableID: "sg-a"},
		"association of non-subnet": {ID: "assoc-x", Kind: "AWS::EC2::SubnetRouteTableAssociation", SubnetID: "rtb-a", RouteTableID: "rtb-a"},
		"subnet already associated": {ID: "assoc-x", Kind: "AWS::EC2::SubnetRouteTableAssociation", SubnetID: "subnet-a", RouteTableID: "rtb-a"},
		"duplicate identifier":      {ID: "vpc-a", Kind: "AWS::EC2::VPC", CIDR: "10.7.0.0/16"},
	} {
		r.Region, r.StackID = "us-east-1", "stack-a"
		if err := createNetworkResource(db.DB, r); err == nil {
			t.Errorf("%s admitted", name)
		}
	}
	if after := db.dump(t); after != before {
		t.Fatalf("refused resources were persisted:\n%s", after)
	}
}

func TestEC2NetworkSnapshotAdmitsOnlyOwnedPublicIngress(t *testing.T) {
	db := newFaultDB(t)
	store := buildPublicTopology(t, db.DB)
	ctx := context.Background()
	snap, err := store.Snapshot(ctx, "us-east-1", "stack-a", "subnet-a", []string{"sg-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.PublicRoute || snap.GatewayIPv4 != "10.0.1.1" || len(snap.Seal) != 64 || snap.VPCCIDR != "10.0.0.0/16" {
		t.Fatalf("snapshot %+v", snap)
	}
	if !snap.AllowsIngress(netip.MustParseAddr("203.0.113.9"), 80) {
		t.Fatal("public HTTP ingress refused")
	}
	for _, c := range []struct {
		addr string
		port int
	}{{"203.0.113.9", 22}, {"203.0.113.9", 0}, {"203.0.113.9", 70000}, {"2001:db8::1", 80}} {
		if snap.AllowsIngress(netip.MustParseAddr(c.addr), c.port) {
			t.Errorf("ingress admitted for %s:%d", c.addr, c.port)
		}
	}
	private := snap
	private.PublicRoute = false
	if private.AllowsIngress(netip.MustParseAddr("203.0.113.9"), 80) {
		t.Fatal("ingress admitted without public route")
	}
	again, err := store.Snapshot(ctx, "us-east-1", "stack-a", "subnet-a", []string{"sg-a"})
	if err != nil || again.Seal != snap.Seal {
		t.Fatal("snapshot seal is not stable", err)
	}
	for name, c := range map[string]struct {
		owner, subnet string
		groups        []string
	}{
		"no owner":            {"", "subnet-a", []string{"sg-a"}},
		"no groups":           {"stack-a", "subnet-a", nil},
		"too many groups":     {"stack-a", "subnet-a", []string{"a", "b", "c", "d", "e", "f"}},
		"unknown subnet":      {"stack-a", "subnet-z", []string{"sg-a"}},
		"subnet of non-owner": {"stack-b", "subnet-a", []string{"sg-a"}},
		"non-subnet":          {"stack-a", "rtb-a", []string{"sg-a"}},
		"other VPC group":     {"stack-a", "subnet-a", []string{"sg-b"}},
		"duplicate group":     {"stack-a", "subnet-a", []string{"sg-a", "sg-a"}},
		"group of wrong kind": {"stack-a", "subnet-a", []string{"rtb-a"}},
	} {
		if _, err := store.Snapshot(ctx, "us-east-1", c.owner, c.subnet, c.groups); err == nil {
			t.Errorf("%s snapshot admitted", name)
		}
	}
	// A subnet whose VPC vanished from durable state cannot be sealed.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = networkWrite(ctx, tx, EC2NetworkResource{ID: "subnet-orphan", Kind: "AWS::EC2::Subnet", Region: "us-east-1", StackID: "stack-a", VPCID: "vpc-gone", CIDR: "10.3.0.0/24", Revision: 1}, true); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Snapshot(ctx, "us-east-1", "stack-a", "subnet-orphan", []string{"sg-a"}); err == nil {
		t.Fatal("orphan subnet sealed")
	}
	// A stored document whose identity disagrees with its row is refused.
	if _, err = db.Exec(`UPDATE aws_ec2_network_resources SET document=replace(document,'"Region":"us-east-1"','"Region":"eu-west-1"') WHERE id='sg-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Snapshot(ctx, "us-east-1", "stack-a", "subnet-a", []string{"sg-a"}); err == nil {
		t.Fatal("tampered network document sealed")
	}
}

func TestEC2NetworkSnapshotReportsStorageFaults(t *testing.T) {
	sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
		store := buildPublicTopology(t, db)
		return func(ctx context.Context) error {
			_, err := store.Snapshot(ctx, "us-east-1", "stack-a", "subnet-a", []string{"sg-a"})
			return err
		}
	})
}

func TestEC2NetworkQuotasBoundDurableState(t *testing.T) {
	db := newFaultDB(t)
	store, err := NewEC2NetworkStore(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if _, err = tx.Exec(`INSERT INTO aws_ec2_network_resources(region,id,stack_id,kind,document,revision) VALUES('us-east-1',?,'stack-a','AWS::EC2::VPC','{}',1)`, fmt.Sprintf("vpc-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = createNetworkResource(db.DB, EC2NetworkResource{ID: "vpc-new", Kind: "AWS::EC2::VPC", Region: "us-east-1", StackID: "stack-b", CIDR: "10.0.0.0/16"}); err == nil {
		t.Fatal("network quota exceeded")
	}
	if _, err = store.Snapshot(context.Background(), "us-east-1", "stack-a", "vpc-0000", []string{"sg"}); err == nil {
		t.Fatal("over-quota stack listed")
	}
}
