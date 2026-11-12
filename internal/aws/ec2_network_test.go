package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"go.yaml.in/yaml/v3"
)

func networkTemplate(t *testing.T) string {
	t.Helper()
	raw, e := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var node yaml.Node
	if e = yaml.Unmarshal(raw, &node); e != nil {
		t.Fatal(e)
	}
	v, e := taggedYAML(&node)
	if e != nil {
		t.Fatal(e)
	}
	original := object(v)
	resources := map[string]any{}
	for key, item := range object(original["Resources"]) {
		if stackNetworkKind(stringValue(object(item)["Type"])) {
			resources[key] = item
		}
	}
	// Preserve original network declarations exactly; isolate their acceptance
	// from independently owned EC2/IAM/SSM vertical-slice implementation.
	selected := map[string]any{"Parameters": map[string]any{"NamePrefix": object(original["Parameters"])["NamePrefix"], "AllowedCidr": object(original["Parameters"])["AllowedCidr"]}, "Resources": resources, "Outputs": map[string]any{"Vpc": map[string]any{"Value": map[string]any{"Ref": "Vpc"}}, "Subnet": map[string]any{"Value": map[string]any{"Ref": "PublicSubnet"}}, "SecurityGroup": map[string]any{"Value": map[string]any{"Ref": "Sg"}}}}
	data, e := json.Marshal(selected)
	if e != nil {
		t.Fatal(e)
	}
	return string(data)
}
func networkFixture(t *testing.T) (*StackStore, *EC2NetworkStore, StackRecord) {
	t.Helper()
	db, e := core.New(filepath.Join(t.TempDir(), "network.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	stack, e := NewStackStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	network, e := NewEC2NetworkStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	record, e := compileStack("us-east-1", "network-stack", networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = stack.Create(record); e != nil {
		t.Fatal(e)
	}
	return stack, network, record
}
func networkID(r StackRecord, logical string) string {
	for _, resource := range r.Resources {
		if resource.LogicalID == logical {
			return resource.PhysicalID
		}
	}
	return ""
}
func TestNetworkOriginalResourcesCreateSnapshotCleanup(t *testing.T) {
	stack, network, r := networkFixture(t)
	ctx := context.Background()
	snap, e := network.Snapshot(ctx, r.Region, r.ID, networkID(r, "PublicSubnet"), []string{networkID(r, "Sg")})
	if e != nil {
		t.Fatal(e)
	}
	if snap.VPCCIDR != "10.99.0.0/16" || snap.SubnetCIDR != "10.99.1.0/24" || snap.GatewayIPv4 != "10.99.1.1" || !snap.PublicRoute {
		t.Fatal(snap)
	}
	source := netip.MustParseAddr("192.0.2.10")
	if !snap.AllowsIngress(source, 80) || !snap.AllowsIngress(source, 8080) || snap.AllowsIngress(source, 22) {
		t.Fatal("original SG port contract", snap)
	}
	if r.Outputs["Vpc"] != networkID(r, "Vpc") || r.Outputs["Subnet"] != networkID(r, "PublicSubnet") {
		t.Fatal(r.Outputs)
	}
	if e = stack.Delete(r.Region, r.Name); e != nil {
		t.Fatal(e)
	}
	if _, e = network.Snapshot(ctx, r.Region, r.ID, networkID(r, "PublicSubnet"), []string{networkID(r, "Sg")}); e == nil {
		t.Fatal("deleted network still resolved")
	}
}
func TestNetworkMutationSealsBeforeJoiningCallback(t *testing.T) {
	_, network, r := networkFixture(t)
	ctx := context.Background()
	var sealed atomic.Bool
	lease, e := network.Lease(ctx, r.Region, r.ID, networkID(r, "PublicSubnet"), []string{networkID(r, "Sg")}, func() { sealed.Store(true) })
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	entered := make(chan struct{})
	finish := make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		observed <- lease.Transact(ctx, func(s EC2NetworkSnapshot) error { close(entered); <-finish; return nil })
	}()
	<-entered
	changed := make(chan error, 1)
	go func() {
		changed <- network.ChangeDefaultRoute(ctx, r.Region, r.ID, networkID(r, "RtRoute"), false, func(string, string) error { return nil })
	}()
	deadline := time.After(time.Second)
	for !sealed.Load() {
		select {
		case <-deadline:
			t.Fatal("mutation did not pre-seal actual callback")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case e := <-changed:
		t.Fatal("mutation completed before frame join", e)
	default:
	}
	close(finish)
	if e = <-observed; e != nil {
		t.Fatal(e)
	}
	if e = <-changed; e != nil {
		t.Fatal(e)
	}
	if e = lease.Transact(ctx, func(EC2NetworkSnapshot) error { t.Fatal("sealed lease admitted frame"); return nil }); e == nil {
		t.Fatal("stale lease accepted")
	}
	snap, e := network.Snapshot(ctx, r.Region, r.ID, networkID(r, "PublicSubnet"), []string{networkID(r, "Sg")})
	if e != nil || snap.PublicRoute {
		t.Fatal(snap, e)
	}
}
func TestNetworkDeniedMutationDoesNotSealOrChange(t *testing.T) {
	stack, network, r := networkFixture(t)
	ctx := context.Background()
	var sealed atomic.Bool
	lease, e := network.Lease(ctx, r.Region, r.ID, networkID(r, "PublicSubnet"), []string{networkID(r, "Sg")}, func() { sealed.Store(true) })
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	deny := func(string, string) error { return fmt.Errorf("denied") }
	if e = network.ChangeDefaultRoute(ctx, r.Region, r.ID, networkID(r, "RtRoute"), false, deny); e == nil {
		t.Fatal("denied route succeeded")
	}
	if e = stack.deleteChecked(r.Region, r.Name, func(StackRecord) error { return fmt.Errorf("denied") }); e == nil {
		t.Fatal("denied delete succeeded")
	}
	if sealed.Load() {
		t.Fatal("unauthorized request sealed running NIC")
	}
	if e = lease.Transact(ctx, func(s EC2NetworkSnapshot) error {
		if !s.PublicRoute {
			t.Fatal(s)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestNetworkCIDRValidationAndNoPartialCreate(t *testing.T) {
	for _, change := range []string{"10.98.1.0/24", "10.99.1.999/24"} {
		body := strings.Replace(networkTemplate(t), "10.99.1.0/24", change, 1)
		db, e := core.New(":memory:")
		if e != nil {
			t.Fatal(e)
		}
		s, e := NewStackStore(db.Database())
		if e != nil {
			t.Fatal(e)
		}
		r, e := compileStack("us-east-1", "invalid", body, map[string]string{"NamePrefix": "tc-hello-team"}, "")
		if e == nil {
			e = s.Create(r)
		}
		if e == nil {
			t.Fatal("invalid subnet accepted", change)
		}
		var n int
		if e = db.Database().QueryRow("SELECT count(*) FROM aws_ec2_network_resources").Scan(&n); e != nil || n != 0 {
			t.Fatal(n, e)
		}
		db.Close()
	}
}

// AWS service authorization reference: list_ec2.html; attachment requires both
// gateway and VPC, subnet association uses the selected route table and subnet.
func TestNetworkDependencyPermissions(t *testing.T) {
	cases := []struct {
		kind     string
		props    map[string]any
		action   string
		suffixes []string
	}{
		{"AWS::EC2::VPCGatewayAttachment", map[string]any{"InternetGatewayId": "igw-x", "VpcId": "vpc-x"}, "ec2:DetachInternetGateway", []string{"internet-gateway/igw-x", "vpc/vpc-x"}},
		{"AWS::EC2::SubnetRouteTableAssociation", map[string]any{"RouteTableId": "rtb-x", "SubnetId": "subnet-x"}, "ec2:DisassociateRouteTable", []string{"route-table/rtb-x", "subnet/subnet-x"}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			got := map[string]bool{}
			err := stackNetworkPermissions(StackRecord{Region: "us-east-1"}, StackResource{Type: c.kind, Properties: c.props}, true, func(action, resource string) error { got[action+" "+resource] = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.suffixes) {
				t.Fatalf("unexpected permissions: %v", got)
			}
			for _, suffix := range c.suffixes {
				if !got[c.action+" arn:aws:ec2:us-east-1:000000000000:"+suffix] {
					t.Fatalf("missing dependency %s: %v", suffix, got)
				}
			}
		})
	}
}

func TestNetworkStoredCIDRCorruptionFailsClosed(t *testing.T) {
	_, store, record := networkFixture(t)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	state, err := networkRead(context.Background(), tx, record.Region, networkID(record, "PublicSubnet"))
	if err != nil {
		t.Fatal(err)
	}
	state.CIDR = "not-a-prefix"
	state.Revision++
	if err = networkWrite(context.Background(), tx, state, false); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Snapshot(context.Background(), record.Region, record.ID, state.ID, []string{networkID(record, "Sg")}); err == nil {
		t.Fatal("corrupt subnet accepted")
	}
}

func TestNetworkDocumentedAttributes(t *testing.T) {
	var template map[string]any
	if err := json.Unmarshal([]byte(networkTemplate(t)), &template); err != nil {
		t.Fatal(err)
	}
	template["Outputs"] = map[string]any{
		"VpcCIDR":  map[string]any{"Value": map[string]any{"Fn::GetAtt": []any{"Vpc", "CidrBlock"}}},
		"SubnetID": map[string]any{"Value": map[string]any{"Fn::GetAtt": []any{"PublicSubnet", "SubnetId"}}},
		"GroupID":  map[string]any{"Value": map[string]any{"Fn::GetAtt": []any{"Sg", "GroupId"}}},
	}
	raw, _ := json.Marshal(template)
	record, err := compileStack("us-east-1", "attributes", string(raw), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Outputs["VpcCIDR"] != "10.99.0.0/16" || record.Outputs["SubnetID"] != networkID(record, "PublicSubnet") || record.Outputs["GroupID"] != networkID(record, "Sg") {
		t.Fatal(record.Outputs)
	}
	template["Outputs"] = map[string]any{"Bad": map[string]any{"Value": map[string]any{"Fn::GetAtt": []any{"Vpc", "Ipv6CidrBlocks"}}}}
	raw, _ = json.Marshal(template)
	if _, err = compileStack("us-east-1", "attributes", string(raw), map[string]string{"NamePrefix": "tc-hello-team"}, ""); err == nil {
		t.Fatal("unsupported attribute accepted")
	}
}

func TestNetworkIngressMutationChangesAdmissionAndNeverRevivesLease(t *testing.T) {
	_, store, record := networkFixture(t)
	ctx := context.Background()
	subnet := networkID(record, "PublicSubnet")
	groups := []string{networkID(record, "Sg")}
	var sealed atomic.Bool
	lease, err := store.Lease(ctx, record.Region, record.ID, subnet, groups, func() { sealed.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	allow := func(string, string) error { return nil }
	rules := []EC2IngressRule{{Protocol: "tcp", FromPort: 8080, ToPort: 8080, CIDR: "192.0.2.0/24"}}
	if err = store.ChangeIngress(ctx, record.Region, record.ID, groups[0], rules, allow); err != nil {
		t.Fatal(err)
	}
	if !sealed.Load() || lease.Active() {
		t.Fatal("old NIC remained admitted")
	}
	snapshot, err := store.Snapshot(ctx, record.Region, record.ID, subnet, groups)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.AllowsIngress(netip.MustParseAddr("192.0.2.8"), 8080) || snapshot.AllowsIngress(netip.MustParseAddr("198.51.100.8"), 8080) || snapshot.AllowsIngress(netip.MustParseAddr("192.0.2.8"), 80) {
		t.Fatal("SG change not reflected")
	}
	if err = store.ChangeIngress(ctx, record.Region, record.ID, groups[0], []EC2IngressRule{{Protocol: "tcp", FromPort: 80, ToPort: 8080, CIDR: "0.0.0.0/0"}}, allow); err != nil {
		t.Fatal(err)
	}
	invoked := false
	if err = lease.Transact(ctx, func(EC2NetworkSnapshot) error { invoked = true; return nil }); err == nil || invoked {
		t.Fatal("restored policy revived stale lease")
	}
	fresh, err := store.Lease(ctx, record.Region, record.ID, subnet, groups, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err = fresh.Transact(ctx, func(s EC2NetworkSnapshot) error {
		if !s.AllowsIngress(netip.MustParseAddr("198.51.100.8"), 80) {
			return fmt.Errorf("restored policy denied fresh generation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Snapshot(ctx, record.Region, "foreign-stack", subnet, groups); err == nil {
		t.Fatal("cross-stack snapshot accepted")
	}
}

func TestNetworkFixedPolicyRejectsMutationBeforeSeal(t *testing.T) {
	stack, store, record := networkFixture(t)
	ctx := context.Background()
	subnet := networkID(record, "PublicSubnet")
	groups := []string{networkID(record, "Sg")}
	var sealed atomic.Bool
	lease, err := store.LeaseFixedPolicy(ctx, record.Region, record.ID, subnet, groups, func() { sealed.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	before, err := store.Snapshot(ctx, record.Region, record.ID, subnet, groups)
	if err != nil {
		t.Fatal(err)
	}
	allow := func(string, string) error { return nil }
	if err = store.ChangeDefaultRoute(ctx, record.Region, record.ID, networkID(record, "RtRoute"), false, allow); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("route update: %v", err)
	}
	if err = store.ChangeIngress(ctx, record.Region, record.ID, groups[0], nil, allow); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("SG update: %v", err)
	}
	after, err := store.Snapshot(ctx, record.Region, record.ID, subnet, groups)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Load() || !lease.Active() || after.Seal != before.Seal {
		t.Fatal("unsupported mutation changed state or NIC")
	}
	called := false
	if err = lease.Transact(ctx, func(EC2NetworkSnapshot) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("live admission lost: %v", err)
	}
	if err = stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	if !sealed.Load() || lease.Active() {
		t.Fatal("intentional stack deletion failed to seal")
	}
}
