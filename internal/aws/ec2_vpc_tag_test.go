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

const vpcTagRegion = "us-east-1"

func vpcTagForm(action, id, key string, values ...string) url.Values {
	q := url.Values{"Action": {action}, "ResourceId.1": {id}, "Tag.1.Key": {key}}
	if len(values) != 0 {
		q["Tag.1.Value"] = values
	}
	return q
}

func assertVPCTagACK(t *testing.T, f ec2VPCFixture, actor string, q url.Values) {
	t.Helper()
	body := f.expect(f.call(actor, vpcTagRegion, nil, q), 200, "")
	var ack ec2VPCTagResponse
	if err := xml.Unmarshal([]byte(body), &ack); err != nil || ack.XMLName.Local != q.Get("Action")+"Response" ||
		ack.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || ack.RequestID == "" || !ack.Return || strings.Contains(body, "<tagSet>") {
		t.Fatal(body, err)
	}
}

func assertVPCTags(t *testing.T, f ec2VPCFixture, id string, want []ec2Tag) {
	t.Helper()
	for _, record := range f.describe(vpcTagRegion) {
		if record.ID != id {
			continue
		}
		got := []ec2Tag{}
		for _, tag := range record.Tags {
			got = append(got, ec2Tag{tag.Key, tag.Value})
		}
		if record.ManagedBy != ec2ManagedByEC2 || !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected native VPC tag readback: %+v", record)
		}
		return
	}
	t.Fatal("expected scoped VPC in DescribeVpcs")
}

func vpcTagFileFixture(t *testing.T, path string) ec2VPCFixture {
	t.Helper()
	store, stacks := nativeRouteTableTestStore(t, path)
	return ec2VPCFixture{t, &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}, stacks, store}
}

func TestEC2VPCTagWireOwnershipAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpc-tags.db")
	f := vpcTagFileFixture(t, path)
	vpc := f.create("", url.Values{"CidrBlock": {"10.77.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"},
		"TagSpecification.1.Tag.1.Key": {"Keep"}, "TagSpecification.1.Tag.1.Value": {"unaltered"}})
	peer := f.create("", url.Values{"CidrBlock": {"10.78.0.0/16"}}).ID
	const stack = "arn:aws:cloudformation:us-east-1:000000000000:stack/vpc-tag-fixture/owned"
	const foreign = "vpc-00000000000000077"
	f.cloudFormationVPC(vpcTagRegion, stack, foreign, "10.79.0.0/16")
	arn := networkARN(vpcTagRegion, "vpc", vpc.ID)
	for actor, conditions := range map[string]string{
		"tag-writer":  fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, arn),
		"tag-remover": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:DeleteTags","Resource":%q}`, arn),
		"tag-reader":  `{"Effect":"Allow","Action":"ec2:DescribeVpcs","Resource":"*"}`,
		"tag-denied": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:*","Resource":"*"},`+
			`{"Effect":"Deny","Action":"ec2:CreateTags","Resource":%q}`, arn),
	} {
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + conditions + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(actor, policy); err != nil {
			t.Fatal(err)
		}
	}
	before := replaceTestInventory(t, f.store.Database())
	add := vpcTagForm(ec2CreateVPCTag, vpc.ID, "Name", "演習 & <test>")
	remove := vpcTagForm(ec2DeleteVPCTag, vpc.ID, "Name")
	for _, actor := range []string{"tag-reader", "tag-denied", "tag-remover"} {
		f.expect(f.call(actor, vpcTagRegion, nil, add), 403, "UnauthorizedOperation")
	}
	f.expect(f.call("tag-writer", vpcTagRegion, nil, remove), 403, "UnauthorizedOperation")
	f.expect(f.call("tag-writer", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, peer, "Name", "fixture")), 403, "UnauthorizedOperation")
	f.expect(f.call("tag-writer", "ap-northeast-1", nil, add), 403, "UnauthorizedOperation")
	f.expect(f.call("", "ap-northeast-1", nil, add), 400, "InvalidVpcID.NotFound")
	f.expect(f.call("", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, foreign, "Name", "fixture")), 400, "UnsupportedOperation")
	bound := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: vpcTagRegion, instance: "i-00000000"})
	f.expect(f.call("", vpcTagRegion, bound, add), 403, "UnauthorizedOperation")
	f.expect(f.call("", vpcTagRegion, bound, remove), 403, "UnauthorizedOperation")
	if !reflect.DeepEqual(before, replaceTestInventory(t, f.store.Database())) {
		t.Fatal("denied or unavailable scope changed persisted state")
	}

	assertVPCTagACK(t, f, "tag-writer", add)
	assertVPCTagACK(t, f, "tag-writer", vpcTagForm(ec2CreateVPCTag, vpc.ID, "name", ""))
	want := []ec2Tag{{"Keep", "unaltered"}, {"Name", "演習 & <test>"}, {"name", ""}}
	assertVPCTags(t, f, vpc.ID, want)
	after := replaceTestInventory(t, f.store.Database())
	assertVPCTagACK(t, f, "tag-writer", add)
	assertVPCTagACK(t, f, "tag-remover", vpcTagForm(ec2DeleteVPCTag, vpc.ID, "Name", "")) // exact empty is not key-only
	assertVPCTagACK(t, f, "tag-remover", vpcTagForm(ec2DeleteVPCTag, vpc.ID, "NotPresent"))
	if !reflect.DeepEqual(after, replaceTestInventory(t, f.store.Database())) {
		t.Fatal("identical upsert or nonmatching delete changed the document")
	}
	key := [2]string{vpcTagRegion, vpc.ID}
	var source, saved EC2NetworkResource
	if json.Unmarshal([]byte(before[key][2]), &source) != nil || json.Unmarshal([]byte(after[key][2]), &saved) != nil {
		t.Fatal("invalid inventory document")
	}
	source.Tags["Name"], source.Tags["name"] = "演習 & <test>", ""
	if !reflect.DeepEqual(source, saved) {
		t.Fatal("tag write changed the VPC topology, DNS or revision")
	}
	delete(before, key)
	delete(after, key)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("tag write changed a sibling resource")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f = vpcTagFileFixture(t, path)
	assertVPCTags(t, f, vpc.ID, want)
	assertVPCTagACK(t, f, "tag-writer", vpcTagForm(ec2CreateVPCTag, vpc.ID, "Name", "Rename"))
	assertVPCTags(t, f, vpc.ID, []ec2Tag{{"Keep", "unaltered"}, {"Name", "Rename"}, {"name", ""}})
	assertVPCTagACK(t, f, "tag-remover", vpcTagForm(ec2DeleteVPCTag, vpc.ID, "name", ""))
	assertVPCTagACK(t, f, "tag-remover", remove)
	assertVPCTags(t, f, vpc.ID, []ec2Tag{{"Keep", "unaltered"}})
	for _, id := range []string{vpc.ID, peer} {
		f.expect(f.call("", vpcTagRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {id}}), 200, "")
	}
	f.expect(f.call("", vpcTagRegion, nil, add), 400, "InvalidVpcID.NotFound")
}

func TestEC2VPCTagValidationLimitsAndRollback(t *testing.T) {
	f := newEC2VPCFixture(t)
	items := url.Values{"CidrBlock": {"10.81.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}}
	for i := 1; i <= 50; i++ {
		items.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i), fmt.Sprintf("tag-%02d", i))
	}
	id := f.create("", items).ID
	db := f.store.Database()
	before := replaceTestInventory(t, db)
	for _, tc := range []struct {
		key, value, reason string
	}{
		{"ResourceId.1", "sg-00000000", "InvalidVpcID.Malformed"}, {"ResourceId.2", id, "UnsupportedOperation"},
		{"Tag.2.Key", "other", "UnsupportedOperation"}, {"Tag.1.Key", "", "InvalidParameterValue"},
		{"Tag.1.Key", "AWS:system", "InvalidParameterValue"}, {"Tag.1.Key", strings.Repeat("日", 43), "InvalidParameterValue"},
		{"Tag.1.Value", strings.Repeat("日", 86), "InvalidParameterValue"}, {"Tag.1.Value", "hi\x00", "InvalidParameterValue"},
		{"Tag.1.Value", "\xff", "InvalidParameterValue"}, {"DryRun", "true", "UnsupportedOperation"},
	} {
		q := vpcTagForm(ec2CreateVPCTag, id, "Name", "fixture")
		q.Set(tc.key, tc.value)
		f.expect(f.call("", vpcTagRegion, nil, q), 400, tc.reason)
	}
	q := vpcTagForm(ec2CreateVPCTag, id, "Name")
	f.expect(f.call("", vpcTagRegion, nil, q), 400, "MissingParameter")
	q = vpcTagForm(ec2CreateVPCTag, id, "Name", "one", "two")
	f.expect(f.call("", vpcTagRegion, nil, q), 400, "InvalidParameterValue")
	q = url.Values{"Action": {ec2DeleteVPCTag}, "ResourceId.1": {id}}
	f.expect(f.call("", vpcTagRegion, nil, q), 400, "MissingParameter") // never strip all
	f.expect(f.call("", vpcTagRegion, nil, vpcTagForm(ec2DeleteVPCTag, id, "aws:system")), 400, "InvalidParameterValue")
	f.expect(f.call("", vpcTagRegion, nil, vpcTagForm(ec2CreateVPCTag, id, "new", "fixture")), 400, "TagLimitExceeded")
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("rejected query changed state")
	}
	update := vpcTagForm(ec2CreateVPCTag, id, "tag-01", strings.Repeat("x", 256))
	for _, trigger := range []string{
		`BEFORE UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::VPC' BEGIN SELECT RAISE(IGNORE); END`,
		`AFTER UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::VPC' BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Tags."tag-01"','changed') WHERE id=NEW.id AND region=NEW.region; END`,
	} {
		nativeSubnetLineExec(t, db, "CREATE TRIGGER vpc_tag_fixture "+trigger)
		f.expect(f.call("", vpcTagRegion, nil, update), 500, "InternalError")
		if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatal("failed acknowledgement changed state")
		}
		nativeSubnetLineExec(t, db, "DROP TRIGGER vpc_tag_fixture")
	}
	assertVPCTagACK(t, f, "", update) // replace at the quota is allowed
	assertVPCTagACK(t, f, "", vpcTagForm(ec2DeleteVPCTag, id, "tag-50"))
	assertVPCTagACK(t, f, "", vpcTagForm(ec2CreateVPCTag, id, strings.Repeat("k", 128), ""))
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Unknown',true) WHERE region=? AND id=?`, vpcTagRegion, id)
	before = replaceTestInventory(t, db)
	f.expect(f.call("", vpcTagRegion, nil, update), 500, "InternalError") // even a no-op doesn't accept a foreign shape
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("tag path rewrote an unknown document")
	}
}

func TestEC2VPCTagSerializesWithDNSAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpc-tag-concurrency.db")
	first, second := vpcTagFileFixture(t, path), vpcTagFileFixture(t, path)
	vpc := first.create("", url.Values{"CidrBlock": {"10.82.0.0/16"}}).ID
	queries := []struct {
		f ec2VPCFixture
		q url.Values
	}{
		{first, vpcTagForm(ec2CreateVPCTag, vpc, "First", "a")},
		{second, vpcTagForm(ec2CreateVPCTag, vpc, "Second", "b")},
		{second, modifyDNSForm(vpc, vpcDNSHostnames, true)},
	}
	start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, len(queries))
	for _, item := range queries {
		go func() {
			<-start
			done <- item.f.call("", vpcTagRegion, nil, item.q)
		}()
	}
	close(start)
	for range queries {
		first.expect(<-done, 200, "")
	}
	assertVPCTags(t, first, vpc, []ec2Tag{{"First", "a"}, {"Second", "b"}})
	assertDNSAttribute(t, second, "", vpc, vpcDNSHostnames, true)
	image := replaceTestInventory(t, first.store.Database())
	var saved EC2NetworkResource
	if err := json.Unmarshal([]byte(image[[2]string{vpcTagRegion, vpc}][2]), &saved); err != nil || saved.Revision != 2 || saved.NativeDNSUpdates != 1 {
		t.Fatalf("only the DNS writer should advance revision: %+v %v", saved, err)
	}
}

func TestEC2VPCTagSharesExactLegacyRepairAndCloudFormationGuards(t *testing.T) {
	f, _ := legacyMainRepairFixture(t)
	db := f.store.Database()
	q := vpcTagForm(ec2CreateVPCTag, repairVPC, "Name", "Repaired fixture")
	const stack = "arn:aws:cloudformation:us-east-1:000000000000:stack/vpc-tag-claim/fixture"
	f.cloudFormationVPC(repairRegion, stack, "vpc-00000000000000078", "10.83.0.0/16")
	namespace := stackResourceNamespace(repairRegion, ec2RTVPC)
	nativeSubnetLineExec(t, db, `INSERT INTO aws_cfn_ownership VALUES (?,?,?,?)`, namespace, ec2RTVPC, repairVPC, stack)
	f.expect(f.call("", repairRegion, nil, q), 500, "InternalError")
	nativeSubnetLineExec(t, db, `DELETE FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?`, namespace, repairVPC)
	var original string
	if err := db.QueryRow(`SELECT document FROM aws_cfn_stacks WHERE id=?`, stack).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var record StackRecord
	if err := json.Unmarshal([]byte(original), &record); err != nil {
		t.Fatal(err)
	}
	record.Resources = append(record.Resources, StackResource{Type: ec2RTVPC, PhysicalID: repairVPC})
	conflict, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	nativeSubnetLineExec(t, db, `UPDATE aws_cfn_stacks SET document=? WHERE id=?`, string(conflict), stack)
	f.expect(f.call("", repairRegion, nil, q), 500, "InternalError")
	nativeSubnetLineExec(t, db, `UPDATE aws_cfn_stacks SET document=? WHERE id=?`, original, stack)
	assertVPCTags(t, f, repairVPC, []ec2Tag{{"Name", "Legacy VPC"}})
	assertVPCTagACK(t, f, "", q)
	allow := func() error { return nil }
	plan, err := ApplyEC2NativeMainRouteRepair(t.Context(), db, repairRegion, repairVPC, strings.Repeat("a", 64), allow, allow)
	if err != nil || plan.Status != "applied" || plan.VPCRevision != 2 {
		t.Fatal(plan, err)
	}
	assertVPCTags(t, f, repairVPC, []ec2Tag{{"Name", "Repaired fixture"}})
	assertVPCTagACK(t, f, "", vpcTagForm(ec2CreateVPCTag, repairVPC, "Keep", "ok"))
	verified, err := PlanEC2NativeMainRouteRepair(t.Context(), db, repairRegion, repairVPC)
	if err != nil || verified.Status != "already-repaired" || verified.PlanDigest != plan.PlanDigest || verified.VPCRevision != 2 {
		t.Fatal(verified, err)
	}
}
