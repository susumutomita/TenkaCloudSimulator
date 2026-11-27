package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func assertSubnetTags(t *testing.T, f ec2VPCFixture, subnet ec2SubnetRead, tags []ec2Tag) {
	t.Helper()
	subnet.Tags = tags
	for _, row := range nativeSubnetLineInventory(t, f) {
		if row.ID != subnet.ID {
			continue
		}
		if !reflect.DeepEqual(subnet, row) {
			t.Fatalf("unexpected subnet readback: %+v", row)
		}
		return
	}
	t.Fatal("verified subnet absent")
}

func createTagFixtureSubnet(t *testing.T, f ec2VPCFixture, vpc, cidr string) ec2SubnetRead {
	t.Helper()
	body := f.expect(f.call("", vpcTagRegion, nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc},
		"CidrBlock": {cidr}}), 200, "")
	var response ec2CreateSubnetResponse
	if err := xml.Unmarshal([]byte(body), &response); err != nil || response.Subnet.ID == "" {
		t.Fatal(body, err)
	}
	return response.Subnet
}

func TestEC2NativeSubnetTagsXMLOwnershipAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subnet-tags.sqlite")
	f := vpcTagFileFixture(t, path)
	vpc := f.create("", url.Values{"CidrBlock": {"10.84.0.0/16"}}).ID
	sub := createTagFixtureSubnet(t, f, vpc, "10.84.1.0/24")
	peer := createTagFixtureSubnet(t, f, vpc, "10.84.2.0/24")
	stack, err := compileStack(vpcTagRegion, "subnet-tag-stack", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.85.0.0/16"}},
		"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.85.1.0/24","Tags":[{"Key":"Name","Value":"Stack fixture"}]}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	var foreign string
	for _, resource := range stack.Resources {
		if resource.Type == ec2NativeSubnetKind {
			foreign = resource.PhysicalID
		}
	}
	if foreign == "" {
		t.Fatal("missing foreign subnet fixture")
	}
	exact := networkARN(vpcTagRegion, "subnet", sub.ID)
	for actor, statement := range map[string]string{
		"subnet-tag-writer":   fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, exact),
		"subnet-tag-remover":  fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:DeleteTags","Resource":%q}`, exact),
		"subnet-tag-vpc-only": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, networkARN(vpcTagRegion, "vpc", vpc)),
		"subnet-tag-deny": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"*"},`+
			`{"Effect":"Deny","Action":"ec2:CreateTags","Resource":%q}`, exact),
	} {
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + statement + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(actor, policy); err != nil {
			t.Fatal(err)
		}
	}
	db := f.store.Database()
	f.expect(f.call("", vpcTagRegion, nil, modifyDNSForm(vpc, vpcDNSHostnames, true)), 200, "")
	before := replaceTestInventory(t, db)
	add := vpcTagForm(ec2CreateVPCTag, sub.ID, "Name", "研修 & <fixture>")
	remove := vpcTagForm(ec2DeleteVPCTag, sub.ID, "Name")
	for _, actor := range []string{"subnet-tag-vpc-only", "subnet-tag-remover", "subnet-tag-deny"} {
		f.expect(f.call(actor, vpcTagRegion, nil, add), 403, "UnauthorizedOperation")
	}
	f.expect(f.call("subnet-tag-writer", vpcTagRegion, nil, remove), 403, "UnauthorizedOperation")
	f.expect(f.call("subnet-tag-writer", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, peer.ID, "Name", "other")), 403, "UnauthorizedOperation")
	f.expect(f.call("subnet-tag-writer", "ap-northeast-1", nil, add), 403, "UnauthorizedOperation")
	f.expect(f.call("", "ap-northeast-1", nil, add), 400, "InvalidSubnetID.NotFound")
	f.expect(f.call("", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, foreign, "Name", "other")), 400, "UnsupportedOperation")
	f.expect(f.call("", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, "subnet-invalid", "Name", "other")), 400, "InvalidSubnetID.Malformed")
	bound := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: vpcTagRegion, instance: "i-00000000"})
	f.expect(f.call("", vpcTagRegion, bound, add), 403, "UnauthorizedOperation")
	f.expect(f.call("", vpcTagRegion, bound, remove), 403, "UnauthorizedOperation")
	both := vpcTagForm(ec2CreateVPCTag, sub.ID, "Name", "other")
	both.Set("ResourceId.2", vpc)
	f.expect(f.call("", vpcTagRegion, nil, both), 400, "UnsupportedOperation")
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("a refused tag request changed the network inventory")
	}

	assertVPCTagACK(t, f, "subnet-tag-writer", add)
	assertVPCTagACK(t, f, "subnet-tag-writer", vpcTagForm(ec2CreateVPCTag, sub.ID, "name", ""))
	assertVPCTagACK(t, f, "subnet-tag-remover", vpcTagForm(ec2DeleteVPCTag, sub.ID, "Name", ""))
	want := []ec2Tag{{"Name", "研修 & <fixture>"}, {"name", ""}}
	assertSubnetTags(t, f, sub, want)
	assertSubnetTags(t, f, peer, nil)
	assertDNSAttribute(t, f, "", vpc, vpcDNSHostnames, true)
	after := replaceTestInventory(t, db)
	assertVPCTagACK(t, f, "subnet-tag-writer", add)
	if !reflect.DeepEqual(after, replaceTestInventory(t, db)) {
		t.Fatal("identical upsert modified the subnet document")
	}
	key := [2]string{vpcTagRegion, sub.ID}
	var old, current EC2NetworkResource
	if json.Unmarshal([]byte(before[key][2]), &old) != nil || json.Unmarshal([]byte(after[key][2]), &current) != nil {
		t.Fatal("wrong inventory fixture")
	}
	old.Tags = map[string]string{"Name": "研修 & <fixture>", "name": ""}
	if !reflect.DeepEqual(old, current) {
		t.Fatal("tag write changed the subnet's topology, settings, or revision")
	}
	delete(before, key)
	delete(after, key)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("tag write changed another network row, VPC DNS, or route metadata")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f = vpcTagFileFixture(t, path)
	assertSubnetTags(t, f, sub, want)
	assertVPCTagACK(t, f, "subnet-tag-writer", vpcTagForm(ec2CreateVPCTag, sub.ID, "Name", "Renamed"))
	assertVPCTagACK(t, f, "subnet-tag-remover", remove)
	assertSubnetTags(t, f, sub, []ec2Tag{{"name", ""}})
	// The existing dependent-row protection and same-row cleanup apply to a
	// tagged subnet. Its remaining tag disappears with that one deleted row.
	f.expect(f.call("", vpcTagRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 400, "DependencyViolation")
	for _, child := range []ec2SubnetRead{sub, peer} {
		f.expect(f.call("", vpcTagRegion, nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {child.ID}}), 200, "")
	}
	f.expect(f.call("", vpcTagRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 200, "")
	f.expect(f.call("", vpcTagRegion, nil, add), 400, "InvalidSubnetID.NotFound")
}

func TestEC2NativeSubnetTagQuotaReadbackAndRollback(t *testing.T) {
	f, vpc, sub := nativeSubnetLineTopology(t)
	db := f.store.Database()
	for i := 1; i <= 50; i++ {
		assertVPCTagACK(t, f, "", vpcTagForm(ec2CreateVPCTag, sub.ID, fmt.Sprintf("key-%02d", i), ""))
	}
	before := replaceTestInventory(t, db)
	for _, q := range []url.Values{
		vpcTagForm(ec2CreateVPCTag, sub.ID, "AWS:fixture", "prop"),
		vpcTagForm(ec2CreateVPCTag, sub.ID, "key-01", "invalid\x00"),
		vpcTagForm(ec2CreateVPCTag, sub.ID, strings.Repeat("日", 43), ""),
	} {
		f.expect(f.call("", vpcTagRegion, nil, q), 400, "InvalidParameterValue")
	}
	f.expect(f.call("", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, sub.ID, "new", "")), 400, "TagLimitExceeded")
	f.expect(f.call("", vpcTagRegion, nil, url.Values{"Action": {ec2DeleteVPCTag}, "ResourceId.1": {sub.ID}}), 400, "MissingParameter")
	for _, trigger := range []string{
		`BEFORE UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet' BEGIN SELECT RAISE(IGNORE); END`,
		`AFTER UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet'
		 BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Tags."key-01"','unexpected') WHERE region=NEW.region AND id=NEW.id; END`,
		`AFTER UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet'
		 BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Tags.Name','unexpected')
		 WHERE region=NEW.region AND id=json_extract(NEW.document,'$.VPCID'); END`,
	} {
		nativeSubnetLineExec(t, db, "CREATE TRIGGER subnet_tag_fixture "+trigger)
		change := vpcTagForm(ec2CreateVPCTag, sub.ID, "key-01", "set")
		f.expect(f.call("", vpcTagRegion, nil, change), 500, "InternalError")
		if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatal("a failed tag write changed a subnet or its parent")
		}
		nativeSubnetLineExec(t, db, "DROP TRIGGER subnet_tag_fixture")
	}
	assertVPCTagACK(t, f, "", vpcTagForm(ec2CreateVPCTag, sub.ID, "key-01", strings.Repeat("a", 256)))
	assertVPCTagACK(t, f, "", vpcTagForm(ec2DeleteVPCTag, sub.ID, "key-50", ""))
	assertVPCTagACK(t, f, "", vpcTagForm(ec2CreateVPCTag, sub.ID, strings.Repeat("x", 128), ""))
	// A retained ownership index or declaration on *either* end protects the
	// target. These inconsistent rows are created solely in this fixture DB.
	stack, err := compileStack(vpcTagRegion, "subnet-tag-retained", `{"Resources":{"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.88.0.0/16"}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(stack)
	if err != nil {
		t.Fatal(err)
	}
	change := vpcTagForm(ec2CreateVPCTag, sub.ID, "key-01", "new")
	for _, claim := range []StackResource{{Type: ec2NativeSubnetKind, PhysicalID: sub.ID}, {Type: ec2RTVPC, PhysicalID: vpc.ID}} {
		namespace := stackResourceNamespace(vpcTagRegion, claim.Type)
		nativeSubnetLineExec(t, db, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, namespace, claim.Type, claim.PhysicalID, stack.ID)
		f.expect(f.call("", vpcTagRegion, nil, change), 500, "InternalError")
		nativeSubnetLineExec(t, db, `DELETE FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?`, namespace, claim.Type, claim.PhysicalID)
		declared := stack
		declared.Resources = append(declared.Resources, claim)
		value, err := json.Marshal(declared)
		if err != nil {
			t.Fatal(err)
		}
		nativeSubnetLineExec(t, db, `UPDATE aws_cfn_stacks SET document=? WHERE id=?`, string(value), stack.ID)
		f.expect(f.call("", vpcTagRegion, nil, change), 500, "InternalError")
		nativeSubnetLineExec(t, db, `UPDATE aws_cfn_stacks SET document=? WHERE id=?`, string(original), stack.ID)
	}
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Unknown',1) WHERE region=? AND id=?`, vpcTagRegion, sub.ID)
	f.expect(f.call("", vpcTagRegion, nil, change), 500, "InternalError")
}

func TestEC2NativeSubnetTagsSerializeWithPublicIPv4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subnet-tags-concurrent.sqlite")
	first, second := vpcTagFileFixture(t, path), vpcTagFileFixture(t, path)
	vpc := first.create("", url.Values{"CidrBlock": {"10.89.0.0/16"}}).ID
	sub := createTagFixtureSubnet(t, first, vpc, "10.89.1.0/24")
	start := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() {
		<-start
		done <- first.call("", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, sub.ID, "Name", "Both saved"))
	}()
	go func() {
		<-start
		done <- second.call("", vpcTagRegion, nil, subnetPublicIPv4Query(sub.ID, true))
	}()
	close(start)
	first.expect(<-done, 200, "")
	first.expect(<-done, 200, "")
	on := true
	sub.PublicIPv4 = &on
	assertSubnetTags(t, first, sub, []ec2Tag{{"Name", "Both saved"}})
	image := replaceTestInventory(t, second.store.Database())
	if image[[2]string{vpcTagRegion, sub.ID}][3] != "2" {
		t.Fatal("only the setting write should advance the subnet revision")
	}
}
