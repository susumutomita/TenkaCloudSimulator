package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

type elbTestNetwork struct {
	vpc, a, b, group string
	stacks           *StackStore
}

func makeELBTestNetwork(t *testing.T, db *sql.DB) elbTestNetwork {
	t.Helper()
	stacks, err := NewStackStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	const region = "us-east-1"
	v, err := stacks.createNativeVPC(ctx, region, "10.90.0.0/16", nil)
	if err != nil {
		t.Fatal(err)
	}
	create := func(cidr, az string) string {
		t.Helper()
		r, err := stacks.createNativeSubnet(ctx, region, ec2SubnetMutation{vpc: v.ID, cidr: cidr, az: az})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	a := create("10.90.1.0/24", region+"a")
	b := create("10.90.2.0/24", region+"b")
	g, err := stacks.createEC2NativeSecurityGroup(ctx, region, ec2NativeSecurityGroupQuery{vpc: v.ID, name: "elb-placement", description: "group for the local load balancer"})
	if err != nil {
		t.Fatal(err)
	}
	return elbTestNetwork{vpc: v.ID, a: a, b: b, group: g.ID, stacks: stacks}
}
func (n elbTestNetwork) form() []string {
	return []string{"Subnets.member.1", n.a, "Subnets.member.2", n.b, "SecurityGroups.member.1", n.group}
}
func TestELBNativePlacementAndLegacyRows(t *testing.T) {
	h := newELBHarness(t)
	n := h.network
	ctx := context.Background()
	const region = "us-east-1"
	db := h.store.Database()
	lb := h.loadBalancer("native")
	check := func(row elbLBWire) {
		t.Helper()
		if row.VPCID != n.vpc || len(row.AvailabilityZones) != 2 || row.AvailabilityZones[0] != (elbAZWire{SubnetID: n.a, ZoneName: region + "a"}) || row.AvailabilityZones[1] != (elbAZWire{SubnetID: n.b, ZoneName: region + "b"}) || len(row.SecurityGroups) != 1 || row.SecurityGroups[0] != n.group {
			t.Fatal(row)
		}
	}
	check(lb)
	check(elbDecode(t, h.ok("DescribeLoadBalancers", "Names.member.1", "native")).LoadBalancers[0])
	for _, err := range []error{n.stacks.deleteNativeSubnet(ctx, region, n.a), n.stacks.deleteEC2NativeSecurityGroup(ctx, region, n.group)} {
		var failure *ec2Failure
		if !errors.As(err, &failure) || failure.code != "DependencyViolation" {
			t.Fatal(err)
		}
	}
	makeArgs := func(name, a, b, g string) []string {
		return []string{"Name", name, "Subnets.member.1", a, "Subnets.member.2", b, "SecurityGroups.member.1", g}
	}
	h.fail("SubnetNotFound", 400, "CreateLoadBalancer", makeArgs("missing", n.a, "subnet-11111111", n.group)...)
	h.fail("InvalidSecurityGroup", 400, "CreateLoadBalancer", makeArgs("unknown-group", n.a, n.b, "sg-11111111")...)
	h.fail("InvalidSecurityGroup", 400, "CreateLoadBalancer", "Name", "no-default", "Subnets.member.1", n.a, "Subnets.member.2", n.b)
	sameAZ, err := n.stacks.createNativeSubnet(ctx, region, ec2SubnetMutation{vpc: n.vpc, cidr: "10.90.3.0/24", az: region + "a"})
	if err != nil {
		t.Fatal(err)
	}
	h.fail("InvalidSubnet", 400, "CreateLoadBalancer", makeArgs("same-az", n.a, sameAZ.ID, n.group)...)
	other := makeELBTestNetwork(t, db)
	h.fail("InvalidSubnet", 400, "CreateLoadBalancer", makeArgs("two-vpcs", n.a, other.b, n.group)...)
	h.fail("InvalidSecurityGroup", 400, "CreateLoadBalancer", makeArgs("wrong-group", n.a, n.b, other.group)...)
	var count int
	if err := db.QueryRow("SELECT count(*) FROM aws_elb_placement_refs").Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	// Reopen the exact historical JSON shape. Same-name retries must never adopt
	// a contemporary EC2 resource, even when a stored string happens to match.
	old := ELBLoadBalancer{ARN: elbLoadBalancerARN(region, "legacy", "aaaaaaaaaaaaaaaa"), Name: "legacy", ID: "aaaaaaaaaaaaaaaa", Region: region, Scheme: "internal", Subnets: []string{n.a, "subnet-deadbeef"}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO aws_elb_load_balancers(arn,region,name,document) VALUES(?,?,?,?)", old.ARN, region, old.Name, string(raw)); err != nil {
		t.Fatal(err)
	}
	retained := elbDecode(t, h.ok("CreateLoadBalancer", "Name", "legacy", "Scheme", "internal", "Subnets.member.1", "subnet-deadbeef", "Subnets.member.2", n.a)).LoadBalancers[0]
	if retained.LoadBalancerArn != old.ARN || retained.VPCID != "" || len(retained.AvailabilityZones) != 2 || retained.AvailabilityZones[0].ZoneName != "" {
		t.Fatal(retained)
	}
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", lb.LoadBalancerArn)
	if err = n.stacks.deleteNativeSubnet(ctx, region, n.a); err != nil {
		t.Fatal(err)
	}
	if err = n.stacks.deleteEC2NativeSecurityGroup(ctx, region, n.group); err != nil {
		t.Fatal(err)
	}
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", old.ARN)
	if err := db.QueryRow("SELECT count(*) FROM aws_elb_placement_refs").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
