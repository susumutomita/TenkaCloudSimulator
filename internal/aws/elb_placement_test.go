package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
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

func TestELBPlacementSharedReleaseAndAtomicReadback(t *testing.T) {
	h := newELBHarness(t)
	ctx := t.Context()
	n, db := h.network, h.store.Database()
	first, second := h.loadBalancer("first"), h.loadBalancer("second")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(albs, refs int) {
		t.Helper()
		var a, r int
		if err := db.QueryRow("SELECT (SELECT count(*) FROM aws_elb_load_balancers),(SELECT count(*) FROM aws_elb_placement_refs)").Scan(&a, &r); err != nil || a != albs || r != refs {
			t.Fatalf("ALBs/refs=%d/%d, want %d/%d: %v", a, r, albs, refs, err)
		}
	}
	count(2, 6)
	exec("CREATE TRIGGER hold_alb BEFORE DELETE ON aws_elb_load_balancers BEGIN SELECT RAISE(IGNORE); END")
	if w := h.call("DeleteLoadBalancer", "LoadBalancerArn", first.LoadBalancerArn); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	count(2, 6)
	exec("DROP TRIGGER hold_alb")
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", first.LoadBalancerArn)
	count(1, 3)
	for _, err := range []error{n.stacks.deleteNativeSubnet(ctx, "us-east-1", n.a), n.stacks.deleteEC2NativeSecurityGroup(ctx, "us-east-1", n.group)} {
		var f *ec2Failure
		if !errors.As(err, &f) || f.code != "DependencyViolation" {
			t.Fatal(err)
		}
	}
	var authentic string
	if err := db.QueryRow("SELECT document FROM aws_elb_load_balancers WHERE arn=?", second.LoadBalancerArn).Scan(&authentic); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*ELBLoadBalancer){
		func(lb *ELBLoadBalancer) { lb.VPCID = "vpc-11223344" },
		func(lb *ELBLoadBalancer) {
			lb.SubnetZones[n.a], lb.SubnetZones[n.b] = lb.SubnetZones[n.b], lb.SubnetZones[n.a]
		},
	} {
		var lb ELBLoadBalancer
		if err := json.Unmarshal([]byte(authentic), &lb); err != nil {
			t.Fatal(err)
		}
		alter(&lb)
		b, err := json.Marshal(lb)
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE aws_elb_load_balancers SET document=? WHERE arn=?", string(b), second.LoadBalancerArn)
		if w := h.call("DescribeLoadBalancers", "Names.member.1", "second"); w.Code != 500 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := h.call("CreateLoadBalancer", append([]string{"Name", "second"}, n.form()...)...); w.Code != 500 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	exec("UPDATE aws_elb_load_balancers SET document=? WHERE arn=?", authentic, second.LoadBalancerArn)
	for _, trigger := range []string{
		"AFTER INSERT ON aws_elb_load_balancers BEGIN UPDATE aws_elb_load_balancers SET document='{}' WHERE arn=new.arn; END",
		"BEFORE INSERT ON aws_elb_placement_refs WHEN (SELECT count(*) FROM aws_elb_placement_refs WHERE alb=new.alb)=1 BEGIN SELECT RAISE(IGNORE); END",
	} {
		exec("CREATE TRIGGER change_placement " + trigger)
		if w := h.call("CreateLoadBalancer", append([]string{"Name", "retry"}, n.form()...)...); w.Code != 500 {
			t.Fatal(w.Code, w.Body.String())
		}
		count(1, 3)
		exec("DROP TRIGGER change_placement")
	}
	h.ok("DeleteLoadBalancer", "LoadBalancerArn", second.LoadBalancerArn)
	count(0, 0)
	if err := n.stacks.deleteNativeSubnet(ctx, "us-east-1", n.a); err != nil {
		t.Fatal(err)
	}
	h.fail("SubnetNotFound", 400, "CreateLoadBalancer", append([]string{"Name", "after-delete"}, n.form()...)...)
	count(0, 0)
}

func TestELBPlacementCancelDuringReferenceInsert(t *testing.T) {
	h := newELBHarness(t)
	db, n := h.store.Database(), h.network
	// WITHOUT ROWID reference tables do not emit SQLite update hooks. An
	// ordinary rowid witness in the same transaction signals the first insert.
	if _, err := db.Exec("CREATE TABLE placement_insert_event(id INTEGER PRIMARY KEY); CREATE TRIGGER placement_insert AFTER INSERT ON aws_elb_placement_refs BEGIN INSERT INTO placement_insert_event(id) VALUES(NULL); END"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	err = conn.Raw(func(raw any) error {
		raw.(driver.Conn).Raw().UpdateHook(func(action sqlite3.AuthorizerActionCode, _, table string, _ int64) {
			if action == sqlite3.AUTH_INSERT && table == "placement_insert_event" && !fired {
				fired = true
				cancel()
			}
		})
		return nil
	})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	lb := ELBLoadBalancer{Name: "cancel", Region: "us-east-1", Scheme: "internal", Subnets: []string{n.a, n.b}, SecurityGroups: []string{n.group}}
	_, err = h.gateway.ELB.CreateLoadBalancer(ctx, lb, func(string, string) error { return nil })
	if err == nil || !fired {
		t.Fatal("late cancellation was not exercised", err)
	}
	var count int
	if err = db.QueryRow("SELECT (SELECT count(*) FROM aws_elb_load_balancers)+(SELECT count(*) FROM aws_elb_placement_refs)+(SELECT count(*) FROM placement_insert_event)").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	h.loadBalancer("cancel")
}

func TestELBPlacementConcurrentNetworkDelete(t *testing.T) {
	for _, resource := range []string{"subnet", "group"} {
		t.Run(resource, func(t *testing.T) {
			h := newELBHarness(t)
			n := h.network
			ctx := t.Context()
			name, region := "race", "us-east-1"
			request := ELBLoadBalancer{Name: name, Region: region, Scheme: "internal", Subnets: []string{n.a, n.b}, SecurityGroups: []string{n.group}}
			start := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				<-start
				if resource == "subnet" {
					result <- n.stacks.deleteNativeSubnet(ctx, region, n.a)
				} else {
					result <- n.stacks.deleteEC2NativeSecurityGroup(ctx, region, n.group)
				}
			}()
			close(start)
			created, createErr := h.gateway.ELB.CreateLoadBalancer(ctx, request, func(string, string) error { return nil })
			deleteErr := <-result
			if createErr == nil {
				var dependency *ec2Failure
				if !errors.As(deleteErr, &dependency) || dependency.code != "DependencyViolation" {
					t.Fatal(deleteErr)
				}
				h.ok("DescribeLoadBalancers", "LoadBalancerArns.member.1", created.ARN)
				return
			}
			want := "SubnetNotFound"
			if resource == "group" {
				want = "InvalidSecurityGroup"
			}
			var failed elbFailure
			if deleteErr != nil || !errors.As(createErr, &failed) || failed.code != want {
				t.Fatal(createErr, deleteErr)
			}
			h.fail("LoadBalancerNotFound", 400, "DescribeLoadBalancers", "Names.member.1", name)
		})
	}
}
