package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Exercise the wire boundaries against the real Gateway/SQLite, not just
// the parser. Explicit IDs must all exist, independently of the VPC filter.
func TestEC2NativeSGRegressionSelectorsAndTags(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	a := f.create("", url.Values{"CidrBlock": {"10.73.0.0/16"}})
	b := f.create("", url.Values{"CidrBlock": {"10.74.0.0/16"}})
	create := func(vpc, name string) url.Values {
		return url.Values{
			"Action": {"CreateSecurityGroup"}, "GroupName": {name}, "GroupDescription": {"test only"}, "VpcId": {vpc},
		}
	}
	for _, tc := range []struct {
		name   string
		extras url.Values
	}{
		{"tag without resource type", url.Values{"TagSpecification.1.Tag.1.Key": {"Name"}}},
		{"tag key missing", url.Values{"TagSpecification.1.ResourceType": {"security-group"}, "TagSpecification.1.Tag.1.Value": {"test"}}},
		{"tag index gap", url.Values{"TagSpecification.1.ResourceType": {"security-group"}, "TagSpecification.1.Tag.2.Key": {"Name"}}},
		{"empty group", url.Values{"GroupName": {"   "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := create(a.ID, "before-validation")
			for key, value := range tc.extras {
				req[key] = value
			}
			code := "InvalidParameterValue"
			if tc.name == "empty group" {
				code = "MissingParameter"
			}
			f.expect(f.call("", region, nil, req), http.StatusBadRequest, code)
		})
	}
	absent := url.Values{"Action": {"DeleteSecurityGroup"}}
	f.expect(f.call("", region, nil, absent), http.StatusBadRequest, "MissingParameter")
	if groups, err := allNativeSGs(t, f.stacks, region); err != nil || len(groups) != 0 {
		t.Fatalf("invalid requests changed state: %+v / %v", groups, err)
	}

	save := func(form url.Values) ec2NativeSecurityGroupMutationResponse {
		t.Helper()
		body := f.expect(f.call("", region, nil, form), http.StatusOK, "")
		var created ec2NativeSecurityGroupMutationResponse
		if err := xml.Unmarshal([]byte(body), &created); err != nil || !created.Return || !ec2NativeSecurityGroupID.MatchString(created.GroupID) {
			t.Fatalf("bad creation response: %s (%v)", body, err)
		}
		return created
	}
	req := create(a.ID, strings.Repeat("x", 255))
	req.Set("TagSpecification.1.ResourceType", "security-group")
	req.Set("TagSpecification.1.Tag.1.Key", "empty")
	one := save(req)
	if len(one.Tags) != 1 || one.Tags[0] != (ec2Tag{"empty", ""}) {
		t.Fatalf("omitted tag value was not saved as empty: %+v", one.Tags)
	}
	two := save(create(b.ID, "other resource"))
	describe := func(form url.Values) []ec2SecurityGroupRead {
		t.Helper()
		form.Set("Action", "DescribeSecurityGroups")
		body := f.expect(f.call("", region, nil, form), http.StatusOK, "")
		var result ec2DescribeSecurityGroupsResponse
		if err := xml.Unmarshal([]byte(body), &result); err != nil || result.RequestID == "" {
			t.Fatalf("bad read response: %s (%v)", body, err)
		}
		return result.Groups.Items
	}
	req = url.Values{"GroupId.1": {two.GroupID}, "GroupId.2": {one.GroupID}, "Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {a.ID}}
	if rows := describe(req); len(rows) != 1 || rows[0].ID != one.GroupID || rows[0].Name != strings.Repeat("x", 255) ||
		len(rows[0].Tags) != 1 || rows[0].Tags[0] != (ec2Tag{"empty", ""}) {
		t.Fatalf("ID/VPC intersection or tag readback: %+v", rows)
	}
	req.Set("Filter.1.Value.2", b.ID)
	if rows := describe(req); len(rows) != 2 || rows[0].ID >= rows[1].ID {
		t.Fatalf("multi-VPC results must be distinct and sorted: %+v", rows)
	}
	req.Set("GroupId.2", "sg-12345678")
	body := f.expect(f.call("", region, nil, req), http.StatusBadRequest, "InvalidGroup.NotFound")
	if strings.Contains(body, two.GroupID) || strings.Contains(body, "<securityGroupInfo>") {
		t.Fatalf("unknown exact ID returned partial data: %s", body)
	}
	req.Set("GroupId.2", two.GroupID)
	f.expect(f.call("", region, nil, req), http.StatusBadRequest, "InvalidParameterValue")
	req.Del("GroupId.1")
	req.Del("GroupId.2")
	req.Set("Filter.1.Value.2", one.GroupID)
	f.expect(f.call("", region, nil, req), http.StatusBadRequest, "InvalidVpcID.Malformed")

	limit := url.Values{"Action": {"DescribeSecurityGroups"}, "Filter.1.Name": {"vpc-id"}}
	for n := 1; n <= 50; n++ {
		limit.Set(fmt.Sprintf("Filter.1.Value.%d", n), fmt.Sprintf("vpc-%08x", n))
	}
	if rows := describe(limit); len(rows) != 0 {
		t.Fatalf("unmatched VPC filters returned groups: %+v", rows)
	}
	limit.Set("Filter.1.Value.51", "vpc-12345678")
	f.expect(f.call("", region, nil, limit), http.StatusBadRequest, "InvalidParameterValue")
	for _, id := range []string{one.GroupID, two.GroupID} {
		f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {id}}), http.StatusOK, "")
	}
	for _, id := range []string{a.ID, b.ID} {
		f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {id}}), http.StatusOK, "")
	}
	if err := f.store.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}

// A denial on only concrete CreateTags IDs must also be evaluated after the
// ID is allocated. The same identity can create the same untagged group.
func TestEC2NativeSGRegressionAssignedTagGrant(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	v := f.create("", url.Values{"CidrBlock": {"10.75.0.0/16"}})
	wild := networkARN(region, "security-group", "*")
	assigned := networkARN(region, "security-group", "sg-"+strings.Repeat("?", 17))
	raw := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"ec2:CreateSecurityGroup","Resource":[%q,%q]},
		{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q},
		{"Effect":"Deny","Action":"ec2:CreateTags","Resource":%q}]}`,
		networkARN(region, "vpc", v.ID), wild, wild, assigned)
	p, err := core.ParsePolicy([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.g.Identities.Set("restricted-tag-editor", p); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"Action": {"CreateSecurityGroup"}, "VpcId": {v.ID}, "GroupName": {"no-tag-on-create"}, "GroupDescription": {"test only"},
		"TagSpecification.1.ResourceType": {"security-group"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"fixture"},
	}
	body := f.expect(f.call("restricted-tag-editor", region, nil, form), http.StatusForbidden, "UnauthorizedOperation")
	if strings.Contains(body, "<groupId>") || !strings.Contains(body, "authorized to create this security group") {
		t.Fatalf("expected assigned ID denial before any state change: %s", body)
	}
	if groups, err := allNativeSGs(t, f.stacks, region); err != nil || len(groups) != 0 {
		t.Fatalf("denied tag-on-create left state: %+v / %v", groups, err)
	}
	for k := range form {
		if strings.HasPrefix(k, "TagSpecification.") {
			form.Del(k)
		}
	}
	body = f.expect(f.call("restricted-tag-editor", region, nil, form), http.StatusOK, "")
	var group ec2NativeSecurityGroupMutationResponse
	if err := xml.Unmarshal([]byte(body), &group); err != nil || group.GroupID == "" || len(group.Tags) != 0 {
		t.Fatalf("untagged create: %+v / %v", group, err)
	}
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {group.GroupID}}), http.StatusOK, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {v.ID}}), http.StatusOK, "")
}

// SG ownership is derived from both the network row and the typed name row.
// Historical or damaged local DB state must never silently become editable.
func TestEC2NativeSGRegressionOwnershipAndInvalidState(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	const vpcKind = "AWS::EC2::VPC"
	ctx := context.Background()
	const legacyTemplate = `{"Resources":{
		"VPC":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.76.0.0/16"}},
		"SG":{"Type":"AWS::EC2::SecurityGroup","Properties":{"VpcId":{"Ref":"VPC"},"GroupDescription":"legacy SG"}}
	}}`
	legacy, err := compileStack(region, "sg-legacy-owner", legacyTemplate, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stacks.Create(legacy); err != nil {
		t.Fatal(err)
	}
	var legacyGroup, legacyVPC string
	for _, resource := range legacy.Resources {
		switch resource.Type {
		case ec2SecurityGroupKind:
			legacyGroup = resource.PhysicalID
		case vpcKind:
			legacyVPC = resource.PhysicalID
		}
	}
	if legacyGroup == "" || legacyVPC == "" {
		t.Fatal("legacy fixture incomplete")
	}
	remove := func(id string) url.Values {
		return url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {id}}
	}
	f.expect(f.call("", region, nil, remove(legacyGroup)), http.StatusBadRequest, "UnsupportedOperation")
	one := newNativeSGVPC(t, f.stacks, region, "10.77.0.0/16")
	two := newNativeSGVPC(t, f.stacks, region, "10.78.0.0/16")
	made, err := f.stacks.createEC2NativeSecurityGroup(ctx, region, ec2NativeSecurityGroupQuery{
		name: "kept", description: "native SG", vpc: one,
	})
	if err != nil {
		t.Fatal(err)
	}
	db := f.store.Database()
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.Exec(stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	desc := url.Values{"Action": {"DescribeSecurityGroups"}}
	healthy := func() {
		t.Helper()
		body := f.expect(f.call("", region, nil, desc), http.StatusOK, "")
		for _, id := range []string{made.ID, legacyGroup} {
			if !strings.Contains(body, "<groupId>"+id+"</groupId>") {
				t.Fatalf("independent native and legacy rows must both remain: %s", body)
			}
		}
	}
	failClosed := func(t *testing.T) {
		t.Helper()
		for _, call := range []url.Values{desc, remove(made.ID)} {
			body := f.expect(f.call("", region, nil, call), http.StatusInternalServerError, "InternalError")
			if strings.Contains(body, "<securityGroupInfo>") || strings.Contains(body, "<return>") ||
				strings.Contains(body, legacyGroup) || strings.Contains(body, made.ID) {
				t.Fatalf("inconsistent native state returned partial read or successful deletion: %s", body)
			}
		}
	}
	healthy()
	var pristine string
	if err := db.QueryRow(`SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, made.ID).Scan(&pristine); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*EC2NetworkResource)
	}{
		{"forbidden native policy", func(r *EC2NetworkResource) {
			r.Ingress = []EC2IngressRule{{"tcp", 443, 443, "192.0.2.0/24"}}
		}},
		{"reserved persisted tag", func(r *EC2NetworkResource) { r.Tags = map[string]string{"aws:pinned": "fixture"} }},
		{"invalid XML text in persisted tag", func(r *EC2NetworkResource) { r.Tags = map[string]string{"test": "bad\x00text"} }},
		{"too many persisted tags", func(r *EC2NetworkResource) {
			r.Tags = map[string]string{}
			for n := 0; n < 51; n++ {
				r.Tags[fmt.Sprintf("tag-%d", n)] = ""
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := made.EC2NetworkResource
			tc.change(&corrupt)
			doc, err := json.Marshal(corrupt)
			if err != nil {
				t.Fatal(err)
			}
			exec(`UPDATE aws_ec2_network_resources SET document=? WHERE region=? AND id=?`, string(doc), region, made.ID)
			failClosed(t)
			exec(`UPDATE aws_ec2_network_resources SET document=? WHERE region=? AND id=?`, pristine, region, made.ID)
			healthy()
		})
	}
	// A typed SG pointing at a different valid VPC still fails closed.
	exec(`UPDATE aws_ec2_native_security_groups SET vpc_id=? WHERE region=? AND id=?`, two, region, made.ID)
	failClosed(t)
	exec(`UPDATE aws_ec2_native_security_groups SET vpc_id=? WHERE region=? AND id=?`, one, region, made.ID)
	// Neither typed child nor native parent may have a conflicting CFN claim.
	for _, claim := range []struct{ kind, id string }{{ec2SecurityGroupKind, made.ID}, {vpcKind, one}} {
		ns := stackResourceNamespace(region, claim.kind)
		exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, ns, claim.kind, claim.id, legacy.ID)
		failClosed(t)
		exec(`DELETE FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?`, ns, claim.kind, claim.id)
		healthy()
	}
	// A conflicting typed name record never adopts an actual legacy row.
	exec(`INSERT INTO aws_ec2_native_security_groups VALUES(?,?,?,?)`, region, legacyGroup, legacyVPC, "misowned")
	f.expect(f.call("", region, nil, remove(legacyGroup)), http.StatusInternalServerError, "InternalError")
	exec(`DELETE FROM aws_ec2_native_security_groups WHERE region=? AND id=?`, region, legacyGroup)
	healthy()
	f.expect(f.call("", region, nil, remove(made.ID)), http.StatusOK, "")
	if err := f.stacks.Delete(region, legacy.Name); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{one, two} {
		if err := f.stacks.deleteNativeVPC(ctx, region, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestEC2NativeSGRegressionTransactionalReadback(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	ctx := context.Background()
	vpc := newNativeSGVPC(t, f.stacks, region, "10.79.0.0/16")
	db := f.store.Database()
	exec := func(stmt string) {
		t.Helper()
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	check := func(ids ...string) {
		t.Helper()
		got, err := allNativeSGs(t, f.stacks, region)
		if err != nil || len(got) != len(ids) {
			t.Fatalf("unexpected persisted state: %+v / %v", got, err)
		}
		var typed, net int
		for _, statement := range []struct {
			count *int
			sql   string
		}{
			{&typed, `SELECT count(*) FROM aws_ec2_native_security_groups WHERE region=?`},
			{&net, `SELECT count(*) FROM aws_ec2_network_resources WHERE region=? AND kind='AWS::EC2::SecurityGroup'`},
		} {
			if err := db.QueryRow(statement.sql, region).Scan(statement.count); err != nil || *statement.count != len(ids) {
				t.Fatalf("partially committed state in %s: %d / %v", statement.sql, *statement.count, err)
			}
		}
		if len(ids) == 1 && (got[0].ID != ids[0] || got[0].Name != "before-readback" || got[0].VPCID != vpc) {
			t.Fatalf("rollback changed stored record: %+v", got[0])
		}
	}
	form := url.Values{"Action": {"CreateSecurityGroup"}, "VpcId": {vpc}, "GroupName": {"before-readback"}, "GroupDescription": {"bounded test metadata"}}
	for _, trigger := range []string{
		`BEFORE INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::SecurityGroup' BEGIN SELECT RAISE(ABORT,'test failed before network write'); END`,
		`BEFORE INSERT ON aws_ec2_native_security_groups BEGIN SELECT RAISE(ABORT,'test failed before typed write'); END`,
		`AFTER INSERT ON aws_ec2_native_security_groups BEGIN UPDATE aws_ec2_native_security_groups SET name='changed-in-storage' WHERE region=NEW.region AND id=NEW.id; END`,
		`AFTER INSERT ON aws_ec2_native_security_groups BEGIN UPDATE aws_ec2_native_security_groups SET name='default' WHERE region=NEW.region AND id=NEW.id; END`,
	} {
		exec("CREATE TRIGGER test_sg_fault " + trigger)
		f.expect(f.call("", region, nil, form), http.StatusInternalServerError, "InternalError")
		exec("DROP TRIGGER test_sg_fault")
		check()
	}
	var group ec2NativeSecurityGroupMutationResponse
	body := f.expect(f.call("", region, nil, form), http.StatusOK, "")
	if err := xml.Unmarshal([]byte(body), &group); err != nil || group.GroupID == "" {
		t.Fatalf("successful retry after rollback: %s (%v)", body, err)
	}
	remove := url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {group.GroupID}}
	for _, trigger := range []string{
		`BEFORE DELETE ON aws_ec2_native_security_groups BEGIN SELECT RAISE(ABORT,'test failed before typed deletion'); END`,
		`BEFORE DELETE ON aws_ec2_native_security_groups BEGIN SELECT RAISE(IGNORE); END`,
		`BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::SecurityGroup' BEGIN SELECT RAISE(ABORT,'test failed before network deletion'); END`,
		`AFTER DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::SecurityGroup'
		  BEGIN INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision); END`,
	} {
		exec("CREATE TRIGGER test_sg_fault " + trigger)
		f.expect(f.call("", region, nil, remove), http.StatusInternalServerError, "InternalError")
		exec("DROP TRIGGER test_sg_fault")
		check(group.GroupID)
	}
	f.expect(f.call("", region, nil, remove), http.StatusOK, "")
	check()
	if err := f.stacks.deleteNativeVPC(ctx, region, vpc); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestEC2NativeSGRegressionSharedNetworkQuota(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region, other = "us-east-1", "us-west-2"
	ctx := context.Background()
	vpc := newNativeSGVPC(t, f.stacks, region, "10.80.0.0/16")
	db := f.store.Database()
	// Fixture for a small database with a full all-region resource quota.
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO aws_ec2_network_resources
		SELECT ?,printf('vpc-%017x',i),?,'AWS::EC2::VPC',
		  json_object('ID',printf('vpc-%017x',i),'Kind','AWS::EC2::VPC','Region',?,'StackID',?,'CIDR','10.81.0.0/16','DNSSupport',true,'Revision',1),1
		FROM n`, ec2NativeSGNetworkLimit-2, other, ec2NativeOwner, other, ec2NativeOwner); err != nil {
		t.Fatal(err)
	}
	q := ec2NativeSecurityGroupQuery{name: "last-slot", description: "quota test", vpc: vpc}
	first, err := f.stacks.createEC2NativeSecurityGroup(ctx, region, q)
	if err != nil {
		t.Fatal(err)
	}
	q.name = "over-limit"
	_, err = f.stacks.createEC2NativeSecurityGroup(ctx, region, q)
	wantNativeSGError(t, err, "SecurityGroupLimitExceeded")
	if rows, err := allNativeSGs(t, f.stacks, region); err != nil || !slices.EqualFunc(rows, []string{first.ID}, func(g ec2NativeSecurityGroup, id string) bool { return g.ID == id }) {
		t.Fatalf("quota failure mutated native SGs: %+v / %v", rows, err)
	}
	if err := f.stacks.deleteEC2NativeSecurityGroup(ctx, region, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := f.stacks.createEC2NativeSecurityGroup(ctx, region, q)
	if err != nil || first.ID == second.ID {
		t.Fatalf("released slot could not be reused with a new ID: %+v / %v", second, err)
	}
	if err := f.stacks.deleteEC2NativeSecurityGroup(ctx, region, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM aws_ec2_network_resources WHERE region=?`, other); err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.deleteNativeVPC(ctx, region, vpc); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}
