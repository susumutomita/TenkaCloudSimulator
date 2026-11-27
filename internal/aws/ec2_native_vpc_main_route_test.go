package aws

import (
	"crypto/sha256"
	"encoding/xml"
	"net/url"
	"reflect"
	"testing"
)

func nativeLocalRouteFixture(cidr string) ec2RouteRead {
	return ec2RouteRead{CIDR: cidr, Gateway: "local", Origin: "CreateRouteTable", State: "active"}
}

func expectsNativeLocalRoute(t *testing.T, f ec2VPCFixture, region, vpc string) ec2RouteRead {
	t.Helper()
	for _, parent := range f.describe(region) {
		if parent.ID == vpc {
			return nativeLocalRouteFixture(parent.CIDR)
		}
	}
	t.Fatalf("missing native VPC %s from independent VPC discovery", vpc)
	return ec2RouteRead{}
}

func nativeMainRouteWire(t *testing.T, f ec2VPCFixture, region string, params url.Values) []ec2RouteTableRead {
	t.Helper()
	params.Set("Action", "DescribeRouteTables")
	body := f.expect(f.call("", region, nil, params), 200, "")
	var response ec2DescribeRouteTablesResponse
	if err := xml.Unmarshal([]byte(body), &response); err != nil || response.RequestID == "" || response.XMLNS != "http://ec2.amazonaws.com/doc/2016-11-15/" {
		t.Fatalf("invalid route-table read-back: %v %s", err, body)
	}
	return response.Tables
}

func TestEC2NativeVPCMainRouteFreshDefaults(t *testing.T) {
	const region, vpcCIDR = subnetLineRegion, "10.207.0.0/16"
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {vpcCIDR}}).ID
	main := nativeMainTableID(region, vpc)
	mainAssociation := nativeMainAssociationID(region, vpc)
	local := []ec2RouteRead{{CIDR: vpcCIDR, Gateway: "local", Origin: "CreateRouteTable", State: "active"}}
	want := []ec2RouteTableRead{{ID: main, VPC: vpc, Owner: "000000000000", Routes: local,
		Associations: []ec2RouteAssociationRead{{ID: mainAssociation, Table: main, Main: true,
			State: &ec2NativeRouteAssociationStatus{State: "associated"}}}}}
	if got := nativeMainRouteWire(t, f, region, url.Values{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh VPC has no exact persisted main slot: %+v", got)
	}
	if got := nativeMainRouteWire(t, f, region, url.Values{"RouteTableId.1": {main}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("exact read differs from unfiltered read: %+v", got)
	}
	subnet, err := f.stacks.createNativeSubnet(t.Context(), region, ec2SubnetMutation{vpc: vpc, cidr: "10.207.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	filter := url.Values{"Filter.1.Name": {"association.subnet-id"}, "Filter.1.Value.1": {subnet.ID}}
	if got := nativeMainRouteWire(t, f, region, filter); len(got) != 0 {
		t.Fatal("implicit main relationship was passed off as an explicit subnet association")
	}
	for _, tc := range []struct{ kind, id string }{{"DeleteRouteTable", main}, {"DeleteVpc", vpc}} {
		key := "RouteTableId"
		if tc.kind == "DeleteVpc" {
			key = "VpcId"
		}
		f.expect(f.call("", region, nil, url.Values{"Action": {tc.kind}, key: {tc.id}}), 400, "DependencyViolation")
	}
	params := url.Values{"Action": {"CreateRouteTable"}, "VpcId": {vpc}, "ClientToken": {"main-slot-native-custom"}}
	var customID string
	for i := 0; i < 2; i++ {
		body := f.expect(f.call("", region, nil, params), 200, "")
		var reply ec2NativeRouteTableMutationResponse
		if err := xml.Unmarshal([]byte(body), &reply); err != nil || reply.Table == nil || reply.Table.ID == "" ||
			!reflect.DeepEqual(reply.Table.Routes, local) || len(reply.Table.Associations) != 0 || i == 1 && reply.Table.ID != customID {
			t.Fatalf("create/retry did not reflect the same automatic local route: %v %s", err, body)
		}
		customID = reply.Table.ID
	}
	// The automatic route is not an explicit deletion target. It does not
	// prevent deleting an otherwise empty custom table or consume its quota.
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteRoute"}, "RouteTableId": {customID}, "DestinationCidrBlock": {vpcCIDR}}), 400, "InvalidParameterValue")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {customID}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 200, "")
	if got := nativeMainRouteWire(t, f, region, url.Values{}); len(got) != 0 || len(replaceTestInventory(t, f.store.Database())) != 0 {
		t.Fatalf("main table survived parent cleanup: %+v", got)
	}
}

func TestEC2NativeVPCMainRouteAtomicAndFailClosed(t *testing.T) {
	const region = subnetLineRegion
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	nativeSubnetLineExec(t, db, `CREATE TRIGGER deny_main_write BEFORE INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::RouteTable'
		BEGIN SELECT RAISE(ABORT,'fixture main write failed'); END`)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.206.0.0/16"}})
	if len(replaceTestInventory(t, db)) != 0 {
		t.Fatal("failed default table creation left a parent VPC")
	}
	nativeSubnetLineExec(t, db, "DROP TRIGGER deny_main_write")
	vpc := f.create("", url.Values{"CidrBlock": {"10.206.0.0/16"}}).ID
	main := nativeMainTableID(region, vpc)
	before := replaceTestInventory(t, db)
	nativeSubnetLineExec(t, db, `CREATE TRIGGER deny_main_delete BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::RouteTable'
		BEGIN SELECT RAISE(IGNORE); END`)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}})
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("unacknowledged main deletion changed its VPC or main table")
	}
	nativeSubnetLineExec(t, db, "DROP TRIGGER deny_main_delete")
	nativeSubnetLineExec(t, db, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, main)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeRouteTables"}})
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 500, "InternalError")
	if got := len(replaceTestInventory(t, db)); got != 1 {
		t.Fatalf("invalid stored main evidence was repaired or deleted by a read: %d resources", got)
	}
}

func TestEC2NativeVPCMainRouteLegacyAndCFNUnchanged(t *testing.T) {
	const region = subnetLineRegion
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	vpc := EC2NetworkResource{ID: "vpc-00000000000000001", Kind: ec2RTVPC, Region: region, StackID: ec2NativeOwner,
		CIDR: "10.205.0.0/16", Revision: 1}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := createEC2Network(t.Context(), tx, vpc); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	custom, err := f.stacks.createNativeRouteTable(t.Context(), region, ec2NativeRouteTableQuery{vpc: vpc.ID})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := compileStack(region, "legacy-cfn-table", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.209.0.0/16"}},
		"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"}}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	before := replaceTestInventory(t, db)
	if got := nativeMainRouteWire(t, f, region, url.Values{}); len(got) != 2 ||
		len(got[0].Routes)+len(got[1].Routes)+len(got[0].Associations)+len(got[1].Associations) != 0 {
		t.Fatalf("legacy or CFN observations changed without an explicit upgrade: %+v", got)
	}
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("describe silently wrote legacy resources")
	}
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {custom.ID}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 200, "")
}

func TestEC2NativeVPCMainRouteRetainedReferencesFenceDelete(t *testing.T) {
	const region = subnetLineRegion
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	vpc := f.create("", url.Values{"CidrBlock": {"10.203.0.0/16"}}).ID
	peer := f.create("", url.Values{"CidrBlock": {"10.204.0.0/16"}}).ID
	peerSubnet, err := f.stacks.createNativeSubnet(t.Context(), region, ec2SubnetMutation{vpc: peer, cidr: "10.204.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	main := nativeMainTableID(region, vpc)
	remove := url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}

	// Model a mismatched saved ownership witness directly in a disposable DB;
	// no public API grants this attachment and no simulated request targets it.
	const referenceID = "rtbassoc-00000000000000001"
	ref := EC2NetworkResource{ID: referenceID, Kind: ec2Assoc, Region: region, StackID: ec2NativeOwner,
		VPCID: peer, SubnetID: peerSubnet.ID, RouteTableID: main, Revision: 1}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := networkWrite(t.Context(), tx, ref, true); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := replaceTestInventory(t, db)
	f.expect(f.call("", region, nil, remove), 400, "DependencyViolation")
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("deletion changed a parent, its main, or the retained relation")
	}
	// A storage fault can introduce the same typed reference at the final
	// write. The stored field need not use the encoder's normal casing.
	nativeSubnetLineExec(t, db, `CREATE TABLE late_reference_fixture AS SELECT region,id,stack_id,kind,
		json_set(json_remove(document,'$.RouteTableID'),'$.routeTableId',json_extract(document,'$.RouteTableID')),revision
		FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, referenceID)
	nativeSubnetLineExec(t, db, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, referenceID)
	before = replaceTestInventory(t, db)
	nativeSubnetLineExec(t, db, `CREATE TRIGGER late_reference_witness AFTER DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::VPC'
		BEGIN INSERT INTO aws_ec2_network_resources SELECT * FROM late_reference_fixture; END`)
	nativeSubnetLineServerFailure(t, f, remove)
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("late typed reference did not roll back the paired removal")
	}
	nativeSubnetLineExec(t, db, "DROP TRIGGER late_reference_witness")
	nativeSubnetLineExec(t, db, "DROP TABLE late_reference_fixture")

	// The last SQLite write must not leave a previously deleted system child.
	nativeSubnetLineExec(t, db, `CREATE TABLE retained_main_fixture AS SELECT * FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, main)
	nativeSubnetLineExec(t, db, `CREATE TRIGGER late_main_witness AFTER DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::VPC'
		BEGIN INSERT INTO aws_ec2_network_resources SELECT * FROM retained_main_fixture; END`)
	nativeSubnetLineServerFailure(t, f, remove)
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("late recovery of a removed main did not roll back the parent pair")
	}
	nativeSubnetLineExec(t, db, "DROP TRIGGER late_main_witness")
	nativeSubnetLineExec(t, db, "DROP TABLE retained_main_fixture")
	f.expect(f.call("", region, nil, remove), 200, "")
	if tables := nativeMainRouteWire(t, f, region, url.Values{}); len(tables) != 1 || tables[0].VPC != peer {
		t.Fatal("exact cleanup of the requested pair did not preserve the peer VPC")
	}
}

func TestEC2NativeVPCMainRouteRejectsDeclarationsAndReplayAdoption(t *testing.T) {
	const region = subnetLineRegion
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	vpc := f.create("", url.Values{"CidrBlock": {"10.202.0.0/16"}}).ID
	main := nativeMainTableID(region, vpc)
	stack := subnetSettingsEvidenceStack(t, f)
	document := subnetSettingsEvidenceDocument(t, f, stack.ID)
	before := replaceTestInventory(t, db)
	create := url.Values{"Action": {"CreateRouteTable"}, "VpcId": {vpc}}
	nativeSubnetLineExec(t, db, `UPDATE aws_cfn_stacks SET document=json_insert(document,'$.Resources[#]',
		json_object('LogicalID','RetainedMain','Type',?, 'PhysicalID',?)) WHERE id=?`, ec2RT, main, stack.ID)
	for _, q := range []url.Values{{"Action": {"DescribeVpcs"}}, {"Action": {"DescribeRouteTables"}, "RouteTableId.1": {main}}, create} {
		nativeSubnetLineServerFailure(t, f, q)
	}
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("a declaration lacking its secondary claim allowed a resource change")
	}
	nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", document, stack.ID)

	const token = "corrupt-local-replay-fixture"
	hash := sha256.Sum256([]byte(token))
	create.Set("ClientToken", token)
	parsed, failure := parseEC2NativeRouteTableMutation("CreateRouteTable", create)
	if failure != nil {
		t.Fatal(failure)
	}
	request, err := nativeRouteTableRequestHash(parsed)
	if err != nil {
		t.Fatal(err)
	}
	nativeSubnetLineExec(t, db, `INSERT INTO aws_ec2_native_route_table_replay VALUES(?,?,?,?)`, region, hash[:], request[:], main)
	nativeSubnetLineServerFailure(t, f, create)
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("a corrupt create replay modified the system main")
	}
	nativeSubnetLineExec(t, db, `DELETE FROM aws_ec2_native_route_table_replay WHERE region=? AND token_hash=?`, region, hash[:])
	f.expect(f.call("", region, nil, create), 200, "")
}
