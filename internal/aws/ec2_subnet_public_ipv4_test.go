package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func subnetPublicIPv4Query(subnet string, on bool) url.Values {
	return url.Values{"Action": {ec2ModifyNativeSubnet}, "SubnetId": {subnet},
		"MapPublicIpOnLaunch.Value": {strconv.FormatBool(on)}}
}

func requireSubnetPublicIPv4(t *testing.T, rows []ec2SubnetRead, id string, want *bool) {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			if !reflect.DeepEqual(want, row.PublicIPv4) {
				t.Fatalf("wrong IPv4 preference for %s: want %+v got %+v", id, want, row.PublicIPv4)
			}
			return
		}
	}
	t.Fatalf("subnet %s was absent from readback", id)
}

func TestEC2NativeSubnetPublicIPv4QueryAndOwnership(t *testing.T) {
	const east, west = subnetLineRegion, "us-west-2"
	f, vpc, child := nativeSubnetLineTopology(t)
	db := f.store.Database()
	neighbor, err := f.stacks.createNativeSubnet(t.Context(), east, ec2SubnetMutation{vpc: vpc.ID, cidr: "10.114.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := compileStack(east, "subnet-settings-stack", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.125.0.0/16"}},
		"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.125.1.0/24","MapPublicIpOnLaunch":true}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	var stackSubnet string
	for _, member := range stack.Resources {
		if member.Type == ec2NativeSubnetKind {
			stackSubnet = member.PhysicalID
		}
	}
	if stackSubnet == "" {
		t.Fatal("missing stack fixture subnet")
	}
	off, on := false, true
	requireSubnetPublicIPv4(t, []ec2SubnetRead{child}, child.ID, &off)
	requireSubnetPublicIPv4(t, nativeSubnetLineInventory(t, f), stackSubnet, nil)
	policy := func(name, statements string) {
		t.Helper()
		doc, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":%s}`, statements)))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(name, doc); err != nil {
			t.Fatal(err)
		}
	}
	const action = "ec2:ModifySubnetAttribute"
	target := networkARN(east, "subnet", child.ID)
	policy("subnet-reader", `[{"Effect":"Allow","Action":"ec2:DescribeSubnets","Resource":"*"}]`)
	policy("subnet-vpc-only", fmt.Sprintf(`[{"Effect":"Allow","Action":%q,"Resource":%q}]`, action, networkARN(east, "vpc", vpc.ID)))
	policy("subnet-setter", fmt.Sprintf(`[{"Effect":"Allow","Action":%q,"Resource":%q}]`, action, target))
	policy("subnet-explicit-deny", fmt.Sprintf(`[{"Effect":"Allow","Action":%q,"Resource":%q},{"Effect":"Deny","Action":%q,"Resource":%q}]`,
		action, networkARN(east, "subnet", "*"), action, target))
	before := replaceTestInventory(t, db)
	refuse := func(region, identity string, scope context.Context, q url.Values, status int, code string) {
		t.Helper()
		body := f.expect(f.call(identity, region, scope, q), status, code)
		routeAssocResilienceNoSuccess(t, body)
		if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatalf("refusal %s changed the persisted inventory", code)
		}
	}
	for _, identity := range []string{"subnet-reader", "subnet-vpc-only", "subnet-explicit-deny"} {
		refuse(east, identity, nil, subnetPublicIPv4Query(child.ID, true), 403, "UnauthorizedOperation")
	}
	refuse(east, "subnet-setter", nil, subnetPublicIPv4Query(neighbor.ID, true), 403, "UnauthorizedOperation")
	refuse(west, "subnet-setter", nil, subnetPublicIPv4Query(child.ID, true), 403, "UnauthorizedOperation")
	refuse(west, "", nil, subnetPublicIPv4Query(child.ID, true), 400, "InvalidSubnetID.NotFound")
	refuse(east, "", nil, subnetPublicIPv4Query(stackSubnet, false), 400, "UnsupportedOperation")
	scope := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: east, instance: "i-00000000"})
	refuse(east, "", scope, subnetPublicIPv4Query(child.ID, true), 403, "UnauthorizedOperation")
	for _, denied := range []struct{ key, value, code string }{
		{"MapPublicIpOnLaunch.Value", "", "MissingParameter"},
		{"MapPublicIpOnLaunch.Value", "TRUE", "InvalidParameterValue"},
		{"SubnetId", "bad-subnet", "InvalidSubnetID.Malformed"},
		{"DryRun", "true", "UnsupportedOperation"},
		{"AssignIpv6AddressOnCreation.Value", "true", "UnsupportedOperation"},
	} {
		form := subnetPublicIPv4Query(child.ID, true)
		if denied.value == "" {
			form.Del(denied.key)
		} else {
			form.Set(denied.key, denied.value)
		}
		refuse(east, "", nil, form, 400, denied.code)
	}
	dup := subnetPublicIPv4Query(child.ID, true)
	dup.Add("MapPublicIpOnLaunch.Value", "false")
	refuse(east, "", nil, dup, 400, "InvalidParameterValue")

	// The scope is not inferred from just the indexed native owner. In this
	// disposable database a residual CFN claim or declaration also refuses.
	namespace := stackResourceNamespace(east, ec2NativeSubnetKind)
	nativeSubnetLineExec(t, db, "INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", namespace, ec2NativeSubnetKind, child.ID, stack.ID)
	refuse(east, "", nil, subnetPublicIPv4Query(child.ID, true), 500, "InternalError")
	nativeSubnetLineExec(t, db, "DELETE FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?", namespace, child.ID)
	savedStack, err := json.Marshal(stack)
	if err != nil {
		t.Fatal(err)
	}
	retained := stack
	retained.Resources = append(retained.Resources, StackResource{
		Type: ec2NativeSubnetKind, LogicalID: "RetainedDeclaration", PhysicalID: child.ID,
	})
	withDeclaration, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE region=? AND id=?", string(withDeclaration), east, stack.ID)
	refuse(east, "", nil, subnetPublicIPv4Query(child.ID, true), 500, "InternalError")
	nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE region=? AND id=?", string(savedStack), east, stack.ID)

	write := func(enabled bool) {
		t.Helper()
		body := f.expect(f.call("subnet-setter", east, nil, subnetPublicIPv4Query(child.ID, enabled)), 200, "")
		var receipt ec2ModifySubnetResponse
		if err := xml.Unmarshal([]byte(body), &receipt); err != nil || receipt.XMLNS != "http://ec2.amazonaws.com/doc/2016-11-15/" ||
			receipt.RequestID == "" || !receipt.Return {
			t.Fatalf("no typed settings receipt: %v / %s", err, body)
		}
	}
	write(true)
	read := nativeSubnetLineInventory(t, f)
	requireSubnetPublicIPv4(t, read, child.ID, &on)
	requireSubnetPublicIPv4(t, read, neighbor.ID, &off)
	requireSubnetPublicIPv4(t, read, stackSubnet, nil)
	after := replaceTestInventory(t, db)
	key := [2]string{east, child.ID}
	want, ok := before[key]
	got, exists := after[key]
	var original, updated EC2NetworkResource
	if !ok || !exists || json.Unmarshal([]byte(want[2]), &original) != nil || json.Unmarshal([]byte(got[2]), &updated) != nil ||
		original.PublicIP || !updated.PublicIP || updated.Revision != original.Revision+1 {
		t.Fatal("did not persist exact bool and revision")
	}
	original.PublicIP, original.Revision = true, updated.Revision
	if !reflect.DeepEqual(original, updated) {
		t.Fatal("modified settings changed other target fields")
	}
	write(true)
	if !reflect.DeepEqual(after, replaceTestInventory(t, db)) {
		t.Fatal("repeating the same value changed persisted rows")
	}
	delete(before, key)
	delete(after, key)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("settings update changed unrelated rows")
	}
	write(false)
	requireSubnetPublicIPv4(t, nativeSubnetLineInventory(t, f), child.ID, &off)
}

func TestEC2NativeSubnetPublicIPv4RollbackAndRestart(t *testing.T) {
	const region = subnetLineRegion
	dbFile := filepath.Join(t.TempDir(), "subnet-public-ipv4.db")
	db, stacks := nativeRouteTableTestStore(t, dbFile)
	vpc := nativeRouteTableTestVPC(t, stacks, region, "10.211.0.0/16")
	subnet, err := stacks.createNativeSubnet(t.Context(), region, ec2SubnetMutation{vpc: vpc, cidr: "10.211.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	sqldb := db.Database()
	for _, trigger := range []string{
		`BEFORE UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet' BEGIN SELECT RAISE(IGNORE); END`,
		`AFTER UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet'
		  BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.PublicIP',json('false'))
		  WHERE region=NEW.region AND id=NEW.id; END`,
		`AFTER UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet'
		  BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.CIDR','10.211.0.0/17')
		  WHERE region=NEW.region AND id=json_extract(NEW.document,'$.VPCID'); END`,
	} {
		before := replaceTestInventory(t, sqldb)
		nativeSubnetLineExec(t, sqldb, "CREATE TRIGGER subnet_settings_receipt "+trigger)
		if err := stacks.modifyNativeSubnetPublicIPv4(t.Context(), region, subnet.ID, true); err == nil {
			t.Fatal("unacknowledged or inconsistent write returned success")
		}
		if !reflect.DeepEqual(before, replaceTestInventory(t, sqldb)) {
			t.Fatal("SQLite failure left a partial update")
		}
		nativeSubnetLineExec(t, sqldb, "DROP TRIGGER subnet_settings_receipt")
	}
	if err := stacks.modifyNativeSubnetPublicIPv4(t.Context(), region, subnet.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := nativeRouteTableTestStore(t, dbFile)
	out, err := reopened.ec2Read(t.Context(), region, "DescribeSubnets")
	if err != nil || out.Subnets == nil {
		t.Fatalf("reopened persisted IPv4 settings unavailable: %v", err)
	}
	on := true
	requireSubnetPublicIPv4(t, out.Subnets.Items, subnet.ID, &on)
	if err := reopened.deleteNativeSubnet(t.Context(), region, subnet.ID); err != nil {
		t.Fatal(err)
	}
}

func subnetSettingsEvidenceStack(t *testing.T, f ec2VPCFixture) StackRecord {
	t.Helper()
	stack, err := compileStack(subnetLineRegion, "settings-evidence-stack", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.227.0.0/16"}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	return stack
}

func subnetSettingsEvidenceDocument(t *testing.T, f ec2VPCFixture, stackID string) string {
	t.Helper()
	var document string
	if err := f.store.Database().QueryRowContext(t.Context(), "SELECT document FROM aws_cfn_stacks WHERE id=?", stackID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestEC2NativeSubnetPublicIPv4RejectsUnavailableEvidence(t *testing.T) {
	f, _, child := nativeSubnetLineTopology(t)
	stack := subnetSettingsEvidenceStack(t, f)
	db := f.store.Database()
	form := subnetPublicIPv4Query(child.ID, true)
	baseline := replaceTestInventory(t, db)
	document := subnetSettingsEvidenceDocument(t, f, stack.ID)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.stacks.modifyNativeSubnetPublicIPv4(canceled, subnetLineRegion, child.ID, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled settings write: %v", err)
	}
	if !reflect.DeepEqual(baseline, replaceTestInventory(t, db)) {
		t.Fatal("canceled settings write changed persisted resources")
	}

	t.Run("indexed child disagrees with saved revision", func(t *testing.T) {
		f := f
		f.t = t
		nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=revision+1 WHERE region=? AND id=?`, subnetLineRegion, child.ID)
		defer nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=revision-1 WHERE region=? AND id=?`, subnetLineRegion, child.ID)
		damaged := replaceTestInventory(t, db)
		nativeSubnetLineServerFailure(t, f, form)
		nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeSubnets"}})
		if !reflect.DeepEqual(damaged, replaceTestInventory(t, db)) {
			t.Fatal("refusal rewrote the existing child revision disagreement")
		}
	})
	t.Run("regional stack document is unreadable", func(t *testing.T) {
		f := f
		f.t = t
		const unreadable = "{"
		nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", unreadable, stack.ID)
		defer nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", document, stack.ID)
		if err := f.stacks.modifyNativeSubnetPublicIPv4(t.Context(), subnetLineRegion, child.ID, true); !errors.Is(err, errEC2InternetGatewayState) {
			t.Fatalf("expected regional declaration evidence failure, got %v", err)
		}
		nativeSubnetLineServerFailure(t, f, form)
		if subnetSettingsEvidenceDocument(t, f, stack.ID) != unreadable || !reflect.DeepEqual(baseline, replaceTestInventory(t, db)) {
			t.Fatal("refusal changed the stack document or native network")
		}
	})
	t.Run("stack inventory temporarily unavailable", func(t *testing.T) {
		f := f
		f.t = t
		nativeSubnetLineExec(t, db, "ALTER TABLE aws_cfn_stacks RENAME TO held_stack_inventory")
		defer nativeSubnetLineExec(t, db, "ALTER TABLE held_stack_inventory RENAME TO aws_cfn_stacks")
		nativeSubnetLineServerFailure(t, f, form)
		nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeSubnets"}})
		if !reflect.DeepEqual(baseline, replaceTestInventory(t, db)) {
			t.Fatal("unavailable ownership evidence changed the native network")
		}
	})
	f.expect(f.call("", subnetLineRegion, nil, form), 200, "")
	on := true
	requireSubnetPublicIPv4(t, nativeSubnetLineInventory(t, f), child.ID, &on)
}

func TestEC2NativeSubnetPublicIPv4RollsBackLateOwnershipEvidence(t *testing.T) {
	f, vpc, child := nativeSubnetLineTopology(t)
	stack := subnetSettingsEvidenceStack(t, f)
	db := f.store.Database()
	baseline := replaceTestInventory(t, db)
	document := subnetSettingsEvidenceDocument(t, f, stack.ID)
	claims := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND physical_id IN (?,?)`,
			stackResourceNamespace(subnetLineRegion, ec2NativeSubnetKind), child.ID, vpc.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if claims(t) != 0 {
		t.Fatal("native baseline unexpectedly owned by a stack")
	}
	form := subnetPublicIPv4Query(child.ID, true)
	for _, tc := range []struct{ name, fault, cause string }{
		{"claim appears during write", `INSERT INTO aws_cfn_ownership(namespace,kind,physical_id,stack_id)
			SELECT 'ec2-network:' || NEW.region,NEW.kind,NEW.id,id FROM aws_cfn_stacks
			WHERE region=NEW.region AND name='settings-evidence-stack';`, "native subnet or VPC has a CloudFormation ownership claim"},
		{"parent declaration appears during write", `UPDATE aws_cfn_stacks SET document=json_insert(document,'$.Resources[#]',
			json_object('LogicalID','RetainedParent','Type','AWS::EC2::VPC','PhysicalID',json_extract(NEW.document,'$.VPCID')))
			WHERE region=NEW.region AND name='settings-evidence-stack';`, "native subnet settings conflict with a CloudFormation declaration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := f
			f.t = t
			// Model an inconsistent storage acknowledgement inside the same
			// SQLite transaction, never a request-controlled ownership change.
			nativeSubnetLineExec(t, db, `CREATE TRIGGER late_subnet_evidence AFTER UPDATE OF revision ON aws_ec2_network_resources
				WHEN NEW.kind='AWS::EC2::Subnet' BEGIN `+tc.fault+` END`)
			defer nativeSubnetLineExec(t, db, "DROP TRIGGER late_subnet_evidence")
			if err := f.stacks.modifyNativeSubnetPublicIPv4(t.Context(), subnetLineRegion, child.ID, true); err == nil || err.Error() != tc.cause {
				t.Fatalf("write did not reach the expected late evidence guard: %v", err)
			}
			nativeSubnetLineServerFailure(t, f, form)
			if claims(t) != 0 || subnetSettingsEvidenceDocument(t, f, stack.ID) != document || !reflect.DeepEqual(baseline, replaceTestInventory(t, db)) {
				t.Fatal("rejected write left a preference, revision, topology, claim or declaration behind")
			}
		})
	}
	f.expect(f.call("", subnetLineRegion, nil, form), 200, "")
	on := true
	requireSubnetPublicIPv4(t, nativeSubnetLineInventory(t, f), child.ID, &on)
}
