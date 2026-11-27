package aws

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const sgRuleFailureRegion = "us-east-1"

func sgRuleLineInventory(t *testing.T, f ec2VPCFixture) []ec2SecurityGroupRead {
	t.Helper()
	var out ec2DescribeSecurityGroupsResponse
	body := f.expect(f.call("", sgRuleFailureRegion, nil, url.Values{"Action": {"DescribeSecurityGroups"}}), http.StatusOK, "")
	if err := xml.Unmarshal([]byte(body), &out); err != nil || out.RequestID == "" {
		t.Fatalf("invalid group inventory (%v): %s", err, body)
	}
	return out.Groups.Items
}

func sgRuleLineExec(t *testing.T, f ec2VPCFixture, statement string, args ...any) {
	t.Helper()
	if _, err := f.store.Database().Exec(statement, args...); err != nil {
		t.Fatalf("fixture SQL failed: %v", err)
	}
}

func sgRuleLineNoResult(t *testing.T, f ec2VPCFixture, query url.Values, ids ...string) {
	t.Helper()
	body := f.expect(f.call("", sgRuleFailureRegion, nil, query), http.StatusInternalServerError, "InternalError")
	for _, marker := range append([]string{"<return>", "<securityGroupInfo>"}, ids...) {
		if strings.Contains(body, marker) {
			t.Fatalf("failed %s published a receipt or partial state: %s", query.Get("Action"), body)
		}
	}
}

func TestEC2NativeSGRuleCanonicalQueryAndOrdering(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.123.0.0/16"}})
	group := createSGForRuleTest(t, f, vpc.ID, "rule ordering")
	const fromKey = "IpPermissions.1.FromPort"
	const toKey = "IpPermissions.1.ToPort"
	for _, tc := range []struct{ field, value, code string }{
		{"DryRun", "true", "UnsupportedOperation"},
		{"GroupId", "not-a-group", "InvalidGroupId.Malformed"},
		{fromKey, "0", "InvalidParameterValue"},
		{fromKey, "01", "InvalidParameterValue"},
		{fromKey, "invalid", "InvalidParameterValue"},
		{fromKey, "2", "InvalidParameterValue"},
		{toKey, "65536", "InvalidParameterValue"},
	} {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			f := f
			f.t = t
			query := sgRuleRequest(ec2AuthorizeNativeSGIngress, group, "10.123.1.0/24", 1)
			query.Set(tc.field, tc.value)
			f.expect(f.call("", sgRuleFailureRegion, nil, query), http.StatusBadRequest, tc.code)
		})
	}
	before := sgRuleLineInventory(t, f)
	if len(before) != 1 || len(before[0].Ingress) != 0 || before[0].Egress != nil {
		t.Fatalf("invalid requests changed rule state: %+v", before)
	}

	add := func(from, to, cidr string) {
		t.Helper()
		query := sgRuleRequest(ec2AuthorizeNativeSGIngress, group, cidr, 1)
		query.Set("DryRun", "false") // Explicit false must be equivalent to absence.
		query.Set(fromKey, from)
		query.Set(toKey, to)
		f.expect(f.call("", sgRuleFailureRegion, nil, query), http.StatusOK, "")
	}
	// Unsorted insertion, shared start and end, and both inclusive port edges.
	add("65535", "65535", "10.123.0.0/16")
	add("1", "2", "10.123.2.0/24")
	add("1", "1", "10.123.3.0/24")
	add("1", "2", "10.123.1.0/24")
	want := []ec2SecurityGroupPermission{
		{Protocol: "tcp", FromPort: 1, ToPort: 1, Ranges: []ec2SecurityGroupIPv4Range{{CIDR: "10.123.3.0/24"}}},
		{Protocol: "tcp", FromPort: 1, ToPort: 2, Ranges: []ec2SecurityGroupIPv4Range{{CIDR: "10.123.1.0/24"}}},
		{Protocol: "tcp", FromPort: 1, ToPort: 2, Ranges: []ec2SecurityGroupIPv4Range{{CIDR: "10.123.2.0/24"}}},
		{Protocol: "tcp", FromPort: 65535, ToPort: 65535, Ranges: []ec2SecurityGroupIPv4Range{{CIDR: "10.123.0.0/16"}}},
	}
	got := sgRuleLineInventory(t, f)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Ingress, want) || got[0].Egress != nil {
		t.Fatalf("unexpected range ordering or default egress: %+v", got)
	}
}

func TestEC2NativeSGRuleInternalBoundariesAndCancellation(t *testing.T) {
	f := newEC2VPCFixture(t)
	const cidr = "10.124.1.0/24"
	const parent = "10.124.0.0/16"
	vpc := f.create("", url.Values{"CidrBlock": {parent}})
	group := createSGForRuleTest(t, f, vpc.ID, "bounded rule")
	ctx := t.Context()
	valid := ec2NativeSGRule{from: 443, to: 443, cidr: cidr}
	wrongDirection := valid
	wrongDirection.egress = true
	if out, err := f.stacks.executeEC2NativeSGRuleMutation(ctx, ec2AuthorizeNativeSGIngress, sgRuleFailureRegion, ec2NativeSecurityGroupQuery{id: group, rule: wrongDirection}); out != nil {
		t.Fatalf("mismatched direction received a receipt: %+v", out)
	} else {
		wantNativeSGError(t, err, "InvalidParameterValue")
	}
	if _, err := parseEC2NativeSGRuleQuery("DescribeSecurityGroups", nil); err == nil || err.code != "UnsupportedOperation" {
		t.Fatalf("rule parser accepted a non-rule action: %v", err)
	}
	if _, err := parseEC2NativeSGQuery("DescribeSecurityGroupRules", nil); err == nil || err.code != "UnsupportedOperation" {
		t.Fatalf("native group parser claimed unimplemented discovery: %v", err)
	}
	if _, err := f.stacks.executeEC2NativeSGMutation(ctx, "DescribeSecurityGroups", sgRuleFailureRegion, ec2NativeSecurityGroupQuery{}); err == nil {
		t.Fatal("native group mutation accepted a read")
	} else {
		wantNativeSGError(t, err, "UnsupportedOperation")
	}
	for _, broken := range []string{"10.124.1.2/16", "::/16", "10.124.0.0/12", "not-a-prefix"} {
		if valid.within(broken) {
			t.Fatalf("valid rule accepted malformed parent %s", broken)
		}
	}
	invalid := ec2NativeSGRule{from: 443, to: 0, cidr: cidr}
	if !valid.within(parent) || invalid.within(parent) {
		t.Fatal("rule and parent must both be canonical")
	}
	wantNativeSGError(t, f.stacks.changeEC2NativeSGRule(ctx, sgRuleFailureRegion, group, invalid, true), "InvalidParameterValue")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for name, err := range map[string]error{
		"add rule":     f.stacks.changeEC2NativeSGRule(canceled, sgRuleFailureRegion, group, valid, true),
		"delete group": f.stacks.deleteEC2NativeSecurityGroup(canceled, sgRuleFailureRegion, group),
	} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s must not succeed after cancellation: %v", name, err)
		}
	}
	if _, err := f.stacks.createEC2NativeSecurityGroup(canceled, sgRuleFailureRegion, ec2NativeSecurityGroupQuery{vpc: vpc.ID, name: "canceled", description: "never committed"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled group create returned %v", err)
	}
	got := sgRuleLineInventory(t, f)
	if len(got) != 1 || got[0].ID != group || len(got[0].Ingress) != 0 || got[0].Egress != nil {
		t.Fatalf("canceled or rejected calls changed persisted state: %+v", got)
	}
}

func TestEC2NativeSGRuleStorageFaultsDoNotAcknowledgeOrPublishPartialState(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.125.0.0/16"}})
	group := createSGForRuleTest(t, f, vpc.ID, "storage fixture")
	peer := createSGForRuleTest(t, f, vpc.ID, "storage peer")
	add := func(id string, port int) url.Values {
		return sgRuleRequest(ec2AuthorizeNativeSGIngress, id, "10.125.1.0/24", port)
	}
	f.expect(f.call("", sgRuleFailureRegion, nil, add(group, 91)), http.StatusOK, "")
	f.expect(f.call("", sgRuleFailureRegion, nil, add(peer, 92)), http.StatusOK, "")
	good := sgRuleLineInventory(t, f)
	assertRecovered := func(t *testing.T) {
		t.Helper()
		if rows := sgRuleLineInventory(t, f); !reflect.DeepEqual(rows, good) {
			t.Fatalf("recovered inventory differs: %+v, want %+v", rows, good)
		}
		if err := f.store.ForeignKeyCheck(); err != nil {
			t.Fatal(err)
		}
	}
	const table = "aws_ec2_native_security_group_rules"
	const retained = "sg_rule_fixture_retained"
	for _, fault := range []struct{ name, ddl string }{
		{"write abort", `CREATE TRIGGER sg_rule_fixture_write BEFORE INSERT ON ` + table + `
			WHEN NEW.from_port=93 BEGIN SELECT RAISE(ABORT,'fixture insert unavailable'); END`},
		{"post-write corrupt readback", `CREATE TRIGGER sg_rule_fixture_write AFTER INSERT ON ` + table + `
			WHEN NEW.from_port=93 BEGIN UPDATE ` + table + ` SET cidr='10.126.1.0/24'
			WHERE region=NEW.region AND group_id=NEW.group_id AND egress=NEW.egress AND from_port=NEW.from_port; END`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := f
			f.t = t
			sgRuleLineExec(t, f, fault.ddl)
			sgRuleLineNoResult(t, f, add(group, 93), group, peer)
			sgRuleLineExec(t, f, "DROP TRIGGER sg_rule_fixture_write")
			assertRecovered(t)
		})
	}

	// Simulate an unavailable or malformed schema only in this test database.
	// The real Gateway must not report success, return a healthy peer alone,
	// or commit either half of a group mutation based on incomplete state.
	sgRuleLineExec(t, f, "ALTER TABLE "+table+" RENAME TO "+retained)
	inventory := url.Values{"Action": {"DescribeSecurityGroups"}}
	sgRuleLineNoResult(t, f, inventory, group, peer)
	sgRuleLineNoResult(t, f, add(group, 93), group, peer)
	sgRuleLineNoResult(t, f, url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {group}}, group, peer)
	sgRuleLineNoResult(t, f, url.Values{"Action": {"CreateSecurityGroup"}, "VpcId": {vpc.ID},
		"GroupName": {"missing registry"}, "GroupDescription": {"must roll back"}}, group, peer)
	for _, ordinal := range []string{"'bad persisted value'", "2"} {
		sgRuleLineExec(t, f, "CREATE VIEW "+table+" AS SELECT region,group_id,"+
			"CASE WHEN from_port=91 THEN "+ordinal+" ELSE egress END AS egress,from_port,to_port,cidr FROM "+retained)
		sgRuleLineNoResult(t, f, inventory, group, peer)
		sgRuleLineNoResult(t, f, add(group, 93), group, peer)
		sgRuleLineExec(t, f, "DROP VIEW "+table)
	}
	sgRuleLineExec(t, f, "ALTER TABLE "+retained+" RENAME TO "+table)
	assertRecovered(t)
	var nativeRows, typedRows int
	db := f.store.Database()
	if err := db.QueryRow(`SELECT count(*) FROM aws_ec2_network_resources WHERE kind=? AND stack_id=?`, ec2SecurityGroupKind, ec2NativeOwner).Scan(&nativeRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM aws_ec2_native_security_groups").Scan(&typedRows); err != nil {
		t.Fatal(err)
	}
	if nativeRows != len(good) || typedRows != len(good) {
		t.Fatalf("half of a failed group write survived: shared=%d typed=%d", nativeRows, typedRows)
	}
}

func TestEC2NativeSGRuleParentCIDRMustRemainCanonical(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.127.0.0/16"}})
	group := createSGForRuleTest(t, f, vpc.ID, "parent fixture")
	before := sgRuleLineInventory(t, f)
	const target = " WHERE region=? AND id=?"
	var document string
	if err := f.store.Database().QueryRow("SELECT document FROM aws_ec2_network_resources"+target, sgRuleFailureRegion, vpc.ID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	sgRuleLineExec(t, f, "UPDATE aws_ec2_network_resources SET document=json_set(document,'$.CIDR',?)"+target, "10.127.1.1/16", sgRuleFailureRegion, vpc.ID)
	sgRuleLineNoResult(t, f, url.Values{"Action": {"DescribeSecurityGroups"}}, group)
	sgRuleLineNoResult(t, f, sgRuleRequest(ec2AuthorizeNativeSGEgress, group, "10.127.1.0/24", 443), group)
	sgRuleLineExec(t, f, "UPDATE aws_ec2_network_resources SET document=?"+target, document, sgRuleFailureRegion, vpc.ID)
	if after := sgRuleLineInventory(t, f); !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid parent changed group state: %+v != %+v", after, before)
	}
}
