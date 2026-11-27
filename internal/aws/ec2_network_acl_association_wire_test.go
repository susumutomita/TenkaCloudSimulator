package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func queryNetworkACLBinding(f ec2VPCFixture, subnet string) []ec2NetworkACLRead {
	f.t.Helper()
	return naclRegressionQuery(f.t, f, url.Values{
		"Action": {"DescribeNetworkAcls"}, "Filter.1.Name": {"association.subnet-id"}, "Filter.1.Value.1": {subnet},
	}).NetworkACLs.Items
}

func requireNetworkACLBinding(f ec2VPCFixture, subnet, acl, mode string) ec2NetworkACLAssociationRead {
	f.t.Helper()
	rows := queryNetworkACLBinding(f, subnet)
	if len(rows) != 1 || rows[0].ID != acl || rows[0].Mode != mode || rows[0].Default != (mode == "default-config-only") {
		f.t.Fatalf("unexpected scoped ACL binding: %+v", rows)
	}
	for _, b := range rows[0].Associations.Items {
		if b.SubnetID == subnet && b.ACLID == acl && ec2NetworkACLAssociationIDPattern.MatchString(b.ID) {
			return b
		}
	}
	f.t.Fatal("selected subnet was not associated to the confirmed ACL")
	return ec2NetworkACLAssociationRead{}
}

func changeNetworkACLBinding(f ec2VPCFixture, actor, association, acl string) string {
	f.t.Helper()
	body := f.expect(f.call(actor, naclRegressionRegion, nil, aclBindingQuery(association, acl)), 200, "")
	var r ec2ReplaceNetworkACLAssociationResponse
	if err := xml.Unmarshal([]byte(body), &r); err != nil || r.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" ||
		r.RequestID == "" || !ec2NetworkACLAssociationIDPattern.MatchString(r.NewAssociationID) {
		f.t.Fatalf("invalid Query acknowledgement: %s (%v)", body, err)
	}
	return r.NewAssociationID
}

func aclBindingQuery(association, acl string) url.Values {
	return url.Values{"Action": {ec2ReplaceNetworkACLAssociation}, "AssociationId": {association}, "NetworkAclId": {acl}}
}

func createNetworkACLSubnet(f ec2VPCFixture, vpc, cidr string) string {
	f.t.Helper()
	var out ec2CreateSubnetResponse
	body := f.expect(f.call("", naclRegressionRegion, nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc}, "CidrBlock": {cidr}}), 200, "")
	if err := xml.Unmarshal([]byte(body), &out); err != nil || out.Subnet.VPC != vpc || !ec2SubnetIDPattern.MatchString(out.Subnet.ID) {
		f.t.Fatalf("native subnet not saved: %+v / %v", out, err)
	}
	return out.Subnet.ID
}

func TestEC2NetworkACLAssociationQueryLifecycleAndRestart(t *testing.T) {
	region := naclRegressionRegion
	path := filepath.Join(t.TempDir(), "acl-query.sqlite")
	open := func() ec2VPCFixture {
		store, s := openNetworkACLState(t, path)
		return ec2VPCFixture{t, &Gateway{Stacks: s, Identities: core.NewIdentities(store)}, s, store}
	}
	f := open()
	vpc := f.create("", url.Values{"CidrBlock": {"10.88.0.0/16"}}).ID
	d := nativeDefaultACLID(region, vpc)
	emptyDefault := naclRegressionQuery(t, f, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {d}}).NetworkACLs.Items
	if len(emptyDefault) != 1 || !emptyDefault[0].Default || len(emptyDefault[0].Associations.Items) != 0 ||
		!reflect.DeepEqual(emptyDefault[0].Entries, nativeDefaultNetworkACLEntries()) {
		t.Fatalf("new VPC did not store the true default ACL: %+v", emptyDefault)
	}
	subnet := createNetworkACLSubnet(f, vpc, "10.88.1.0/24")
	peer := createNetworkACLSubnet(f, vpc, "10.88.2.0/24")
	other := f.create("", url.Values{"CidrBlock": {"10.89.0.0/16"}}).ID
	foreignSubnet := createNetworkACLSubnet(f, other, "10.89.1.0/24")
	foreignDefault := nativeDefaultACLID(region, other)
	foreignInitial := requireNetworkACLBinding(f, foreignSubnet, foreignDefault, "default-config-only")
	foreignACL := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {other}}).NetworkACL.ID
	foreignAttached := changeNetworkACLBinding(f, "", foreignInitial.ID, foreignACL)
	create := url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}, "ClientToken": {"fixture-native-attachment"}}
	acl := naclRegressionQuery(t, f, create).NetworkACL.ID
	initial := requireNetworkACLBinding(f, subnet, d, "default-config-only")
	peerInitial := requireNetworkACLBinding(f, peer, d, "default-config-only")
	before := replaceTestInventory(t, f.store.Database())
	for i, grants := range [][]string{{networkARN(region, "network-acl", acl)}, {networkARN(region, "subnet", subnet)},
		{networkARN(region, "network-acl", acl), networkARN(region, "subnet", subnet)}} {
		stmt := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:ReplaceNetworkAclAssociation","Resource":[%q]}]}`, strings.Join(grants, "\",\""))
		if len(grants) == 2 {
			stmt = fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:ReplaceNetworkAclAssociation","Resource":[%q,%q]}]}`, grants[0], grants[1])
		}
		p, err := core.ParsePolicy([]byte(stmt))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set("acl-config-editor", p); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			f.expect(f.call("acl-config-editor", region, nil, aclBindingQuery(initial.ID, acl)), 403, "UnauthorizedOperation")
		}
	}
	denied := f.expect(f.call("acl-config-editor", region, nil, aclBindingQuery("aclassoc-00000000", acl)), 403, "UnauthorizedOperation")
	if strings.Contains(denied, subnet) || strings.Contains(denied, acl) {
		t.Fatal("unresolved association disclosed an endpoint")
	}
	bound := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	f.expect(f.call("", region, bound, aclBindingQuery(initial.ID, acl)), 403, "UnauthorizedOperation")
	f.expect(f.call("", "ap-northeast-1", nil, aclBindingQuery(initial.ID, acl)), 400, "InvalidAssociationID.NotFound")
	for _, otherACL := range []string{foreignACL, foreignDefault} {
		f.expect(f.call("", region, nil, aclBindingQuery(initial.ID, otherACL)), 400, "InvalidParameterValue")
	}
	if requireNetworkACLBinding(f, foreignSubnet, foreignACL, "attached-config-only").ID != foreignAttached {
		t.Fatal("cross-VPC attachment or refusal changed the foreign relationship")
	}
	f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeNetworkAcls"}, "Filter.1.Name": {"association.subnet-id"}, "Filter.1.Value.1": {subnet}, "Filter.1.Value.2": {peer}}), 400, "InvalidParameterValue")
	if actual := requireNetworkACLBinding(f, subnet, d, "default-config-only"); actual != initial {
		t.Fatal("denials changed saved state")
	}
	attached := changeNetworkACLBinding(f, "acl-config-editor", initial.ID, acl)
	if attached == initial.ID || requireNetworkACLBinding(f, subnet, acl, "attached-config-only").ID != attached ||
		requireNetworkACLBinding(f, peer, d, "default-config-only") != peerInitial {
		t.Fatal("confirmed binding changed the wrong subnet")
	}
	if !reflect.DeepEqual(before, replaceTestInventory(t, f.store.Database())) {
		t.Fatal("an ACL association touched network/route/settings rows")
	}
	f.expect(f.call("acl-config-editor", region, nil, create), 403, "UnauthorizedOperation")
	f.expect(f.call("", region, nil, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {other}, "ClientToken": {create.Get("ClientToken")}}),
		400, "IdempotentParameterMismatch")
	requireAttachedACLCreateReplay(f, create, acl, attached)
	for _, req := range []url.Values{{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {acl}},
		networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 100, false)} {
		f.expect(f.call("", region, nil, req), 400, "DependencyViolation")
	}
	for _, req := range []url.Values{{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {d}},
		networkACLEntryForm(ec2CreateNetworkACLEntry, d, 100, false)} {
		f.expect(f.call("", region, nil, req), 400, "UnsupportedOperation")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	if requireNetworkACLBinding(f, subnet, acl, "attached-config-only").ID != attached ||
		changeNetworkACLBinding(f, "", initial.ID, acl) != attached {
		t.Fatal("saved binding or idempotent retry did not survive restart")
	}
	requireAttachedACLCreateReplay(f, create, acl, attached)
	routeTable := naclRegressionQueryRouteTable(t, f, vpc)
	f.expect(f.call("", region, nil, url.Values{"Action": {"AssociateRouteTable"}, "RouteTableId": {routeTable}, "SubnetId": {subnet}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet}}), 400, "DependencyViolation")
	if requireNetworkACLBinding(f, subnet, acl, "attached-config-only").ID != attached {
		t.Fatal("route-table dependency removed the saved ACL association")
	}
	var wire struct {
		IDs []string `xml:"routeTableSet>item>associationSet>item>routeTableAssociationId"`
	}
	body := f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {routeTable}}), 200, "")
	if err := xml.Unmarshal([]byte(body), &wire); err != nil || len(wire.IDs) != 1 {
		t.Fatalf("missing explicit route relation: %s / %v", body, err)
	}
	f.expect(f.call("", region, nil, url.Values{"Action": {"DisassociateRouteTable"}, "AssociationId": {wire.IDs[0]}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {routeTable}}), 200, "")
	restored := changeNetworkACLBinding(f, "", attached, d)
	if restored == initial.ID || restored == attached || requireNetworkACLBinding(f, subnet, d, "default-config-only").ID != restored {
		t.Fatal("default restoration did not create a new durable identity")
	}
	f.expect(f.call("", region, nil, aclBindingQuery(initial.ID, acl)), 400, "InvalidAssociationID.NotFound")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {acl}}), 200, "")
	f.expect(f.call("", region, nil, create), 400, "InvalidNetworkAclID.NotFound")
	for _, id := range []string{subnet, peer} {
		f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {id}}), 200, "")
	}
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {d}}), 400, "InvalidNetworkAclID.NotFound")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {foreignSubnet}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {foreignACL}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {other}}), 200, "")
	if got := naclRegressionQuery(t, f, url.Values{"Action": {"DescribeNetworkAcls"}}).NetworkACLs.Items; len(got) != 0 {
		t.Fatal("network ACL cleanup left resources")
	}
}

func requireAttachedACLCreateReplay(f ec2VPCFixture, create url.Values, acl, association string) {
	f.t.Helper()
	readOne := func() ec2NetworkACLRead {
		out := naclRegressionQuery(f.t, f, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {acl}})
		if out.NetworkACLs == nil || len(out.NetworkACLs.Items) != 1 {
			f.t.Fatal("attached ACL readback missing")
		}
		return out.NetworkACLs.Items[0]
	}
	total := func() int {
		var n int
		if err := f.store.Database().QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			f.t.Fatal(err)
		}
		return n
	}
	before := readOne()
	writes := total()
	replay := naclRegressionQuery(f.t, f, create)
	if replay.ClientToken != create.Get("ClientToken") || replay.NetworkACL == nil || replay.NetworkACL.ID != acl ||
		replay.NetworkACL.Mode != "attached-config-only" || len(replay.NetworkACL.Associations.Items) != 1 ||
		replay.NetworkACL.Associations.Items[0].ID != association || !reflect.DeepEqual(*replay.NetworkACL, before) {
		f.t.Fatal("token replay did not return the same current attached ACL")
	}
	if !reflect.DeepEqual(readOne(), before) || total() != writes {
		f.t.Fatal("token replay mutated durable ACL, association or receipt state")
	}
}

func naclRegressionQueryRouteTable(t *testing.T, f ec2VPCFixture, vpc string) string {
	t.Helper()
	var row struct {
		ID string `xml:"routeTable>routeTableId"`
	}
	body := f.expect(f.call("", naclRegressionRegion, nil, url.Values{"Action": {"CreateRouteTable"}, "VpcId": {vpc}}), 200, "")
	if err := xml.Unmarshal([]byte(body), &row); err != nil || !ec2NativeRouteTableID.MatchString(row.ID) {
		t.Fatalf("invalid route table: %s / %v", body, err)
	}
	return row.ID
}

func TestEC2NetworkACLAssociationQueryRollbackAcrossLifecycle(t *testing.T) {
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	region := naclRegressionRegion
	naclRegressionExec(t, db, `CREATE TRIGGER deny_test_default BEFORE INSERT ON aws_ec2_native_acl_defaults
		BEGIN SELECT RAISE(ABORT,'fixture default unavailable'); END`)
	f.expect(f.call("", region, nil, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.95.0.0/16"}}), 500, "InternalError")
	if len(replaceTestInventory(t, db)) != 0 {
		t.Fatal("default creation failure left a VPC or route table")
	}
	naclRegressionExec(t, db, `DROP TRIGGER deny_test_default`)
	vpc := f.create("", url.Values{"CidrBlock": {"10.95.0.0/16"}}).ID
	d := nativeDefaultACLID(region, vpc)
	before := replaceTestInventory(t, db)
	naclRegressionExec(t, db, `CREATE TRIGGER deny_test_initial_binding BEFORE INSERT ON aws_ec2_native_acl_associations
		BEGIN SELECT RAISE(ABORT,'fixture initial binding unavailable'); END`)
	f.expect(f.call("", region, nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc}, "CidrBlock": {"10.95.1.0/24"}}), 500, "InternalError")
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("binding creation failure left a native subnet")
	}
	naclRegressionExec(t, db, `DROP TRIGGER deny_test_initial_binding`)
	subnet := createNetworkACLSubnet(f, vpc, "10.95.1.0/24")
	b := requireNetworkACLBinding(f, subnet, d, "default-config-only")
	naclRegressionExec(t, db, `CREATE TRIGGER deny_test_subnet_delete BEFORE DELETE ON aws_ec2_network_resources
		WHEN OLD.kind='AWS::EC2::Subnet' BEGIN SELECT RAISE(ABORT,'fixture subnet delete unavailable'); END`)
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet}}), 500, "InternalError")
	if requireNetworkACLBinding(f, subnet, d, "default-config-only") != b {
		t.Fatal("subnet deletion failure retired its saved association")
	}
	naclRegressionExec(t, db, `DROP TRIGGER deny_test_subnet_delete`)
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet}}), 200, "")
	naclRegressionExec(t, db, `CREATE TRIGGER deny_test_vpc_delete BEFORE DELETE ON aws_ec2_network_resources
		WHEN OLD.kind='AWS::EC2::VPC' BEGIN SELECT RAISE(ABORT,'fixture VPC delete unavailable'); END`)
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 500, "InternalError")
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("VPC deletion failure retired its main route")
	}
	out := naclRegressionQuery(t, f, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {d}}).NetworkACLs.Items
	if len(out) != 1 || !out[0].Default || len(out[0].Associations.Items) != 0 {
		t.Fatal("VPC deletion failure retired its default")
	}
	naclRegressionExec(t, db, `DROP TRIGGER deny_test_vpc_delete`)
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 200, "")
	if len(replaceTestInventory(t, db)) != 0 {
		t.Fatal("clean VPC teardown left durable network resources")
	}
}

func TestEC2NetworkACLAssociationQueryNeverProjectsPartialTypedBinding(t *testing.T) {
	f := newEC2VPCFixture(t)
	region := naclRegressionRegion
	db := f.store.Database()
	vpc := f.create("", url.Values{"CidrBlock": {"10.96.0.0/16"}}).ID
	subnet := createNetworkACLSubnet(f, vpc, "10.96.1.0/24")
	d := nativeDefaultACLID(region, vpc)
	custom := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}}).NetworkACL.ID
	b := requireNetworkACLBinding(f, subnet, d, "default-config-only")
	naclRegressionExec(t, db, `DELETE FROM aws_ec2_native_acl_associations WHERE region=? AND subnet_id=?`, region, subnet)
	for _, q := range []url.Values{
		{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {d}},
		{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {custom}},
		{"Action": {"DescribeNetworkAcls"}, "Filter.1.Name": {"association.subnet-id"}, "Filter.1.Value.1": {subnet}},
		{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {custom}},
	} {
		body := f.expect(f.call("", region, nil, q), 500, "InternalError")
		if strings.Contains(body, "<networkAclSet>") || strings.Contains(body, "<return>true</return>") {
			t.Fatal("partial typed association produced a successful configuration response")
		}
	}
	naclRegressionExec(t, db, `INSERT INTO aws_ec2_native_acl_associations
		(region,subnet_id,vpc_id,association_id,generation) VALUES(?,?,?,?,1)`, region, subnet, vpc, b.ID)
	if requireNetworkACLBinding(f, subnet, d, "default-config-only") != b {
		t.Fatal("exact fixture repair did not recover readback")
	}
}
