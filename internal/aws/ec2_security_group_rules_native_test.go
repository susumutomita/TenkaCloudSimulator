package aws

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func sgRuleRequest(action, id, ipv4 string, port int) url.Values {
	return url.Values{
		"Action": {action}, "GroupId": {id},
		"IpPermissions.1.IpProtocol": {"tcp"}, "IpPermissions.1.FromPort": {fmt.Sprint(port)},
		"IpPermissions.1.ToPort": {fmt.Sprint(port)}, "IpPermissions.1.IpRanges.1.CidrIp": {ipv4},
	}
}

func createSGForRuleTest(t *testing.T, f ec2VPCFixture, vpc, name string) string {
	t.Helper()
	created := f.expect(f.call("", "us-east-1", nil, url.Values{
		"Action": {"CreateSecurityGroup"}, "VpcId": {vpc}, "GroupName": {name}, "GroupDescription": {"Detached rule fixture"},
	}), 200, "")
	var result ec2NativeSecurityGroupMutationResponse
	if err := xml.Unmarshal([]byte(created), &result); err != nil || result.GroupID == "" {
		t.Fatalf("create did not return a group: %v", err)
	}
	return result.GroupID
}

func TestEC2NativeSGRuleGatewayRoundTrip(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	vpc := f.create("", url.Values{"CidrBlock": {"10.95.0.0/16"}})
	first := createSGForRuleTest(t, f, vpc.ID, "rule fixture")
	peer := createSGForRuleTest(t, f, vpc.ID, "peer fixture")
	const editor = "sg-rule-editor"
	set := func(allowed ...string) {
		t.Helper()
		actions := make([]string, len(allowed))
		for i, a := range allowed {
			actions[i] = fmt.Sprintf("%q", "ec2:"+a)
		}
		p, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{
			"Effect":"Allow","Action":[%s],"Resource":%q}]}`, strings.Join(actions, ","), networkARN(region, "security-group", first))))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(editor, p); err != nil {
			t.Fatal(err)
		}
	}
	f.expect(f.call(editor, region, nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, first, "10.95.0.0/24", 443)), 403, "UnauthorizedOperation")
	set(ec2AuthorizeNativeSGIngress)
	f.expect(f.call(editor, region, nil, sgRuleRequest(ec2AuthorizeNativeSGEgress, first, "10.95.0.0/24", 443)), 403, "UnauthorizedOperation")
	f.expect(f.call(editor, region, nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, peer, "10.95.0.0/24", 443)), 403, "UnauthorizedOperation")
	f.expect(f.call(editor, "ap-northeast-1", nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, first, "10.95.0.0/24", 443)), 403, "UnauthorizedOperation")
	f.expect(f.call("", "ap-northeast-1", nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, first, "10.95.0.0/24", 443)), 400, "InvalidGroup.NotFound")
	instance := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	f.expect(f.call("", region, instance, sgRuleRequest(ec2AuthorizeNativeSGIngress, first, "10.95.0.0/24", 443)), 403, "UnauthorizedOperation")

	const ip = "10.95.3.4/32"
	check := func(in, eg bool) {
		t.Helper()
		query := url.Values{"Action": {"DescribeSecurityGroups"}, "GroupId.1": {first}, "GroupId.2": {peer}}
		body := f.expect(f.call("", region, nil, query), 200, "")
		var out ec2DescribeSecurityGroupsResponse
		if xml.Unmarshal([]byte(body), &out) != nil || len(out.Groups.Items) != 2 {
			t.Fatal("invalid rule readback")
		}
		for _, g := range out.Groups.Items {
			if g.ID == peer {
				if len(g.Ingress) != 0 || g.Egress != nil {
					t.Fatal("rule leaked into peer")
				}
				continue
			}
			if (len(g.Ingress) == 1) != in || (g.Egress != nil) != eg || g.Mode != "detached-config-only" {
				t.Fatalf("wrong rule direction on readback: %v", g)
			}
			want := ec2SecurityGroupPermission{Protocol: "tcp", FromPort: 443, ToPort: 443, Ranges: []ec2SecurityGroupIPv4Range{{CIDR: ip}}}
			if in && !reflect.DeepEqual(g.Ingress[0], want) || eg && !reflect.DeepEqual(g.Egress.Items, []ec2SecurityGroupPermission{want}) {
				t.Fatalf("wrong tuple: %v", g)
			}
		}
	}
	change := func(action string) {
		t.Helper()
		body := f.expect(f.call(editor, region, nil, sgRuleRequest(action, first, ip, 443)), 200, "")
		var ack ec2NativeSGRuleMutationResponse
		if xml.Unmarshal([]byte(body), &ack) != nil || !ack.Return || ack.RequestID == "" ||
			ack.XMLName.Local != action+"Response" || strings.Contains(body, "<groupId>") || strings.Contains(body, "<tagSet>") {
			t.Fatal("invalid EC2 rule mutation receipt")
		}
	}
	check(false, false)
	change(ec2AuthorizeNativeSGIngress)
	check(true, false)
	f.expect(f.call(editor, region, nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, first, ip, 443)), 400, "InvalidPermission.Duplicate")
	f.expect(f.call("", region, nil, sgRuleRequest(ec2RevokeNativeSGEgress, first, ip, 443)), 400, "InvalidPermission.NotFound")
	f.expect(f.call(editor, region, nil, sgRuleRequest(ec2RevokeNativeSGIngress, first, ip, 443)), 403, "UnauthorizedOperation")
	check(true, false)
	set(ec2AuthorizeNativeSGEgress, ec2RevokeNativeSGIngress, ec2RevokeNativeSGEgress)
	change(ec2AuthorizeNativeSGEgress)
	check(true, true)
	change(ec2RevokeNativeSGIngress)
	check(false, true)
	change(ec2RevokeNativeSGEgress)
	check(false, false)
	unknown := sgRuleRequest(ec2AuthorizeNativeSGIngress, first, ip, 443)
	unknown.Add("IpPermissions.1.IpRanges.2.CidrIp", "10.95.0.0/24")
	f.expect(f.call("", region, nil, unknown), 400, "UnsupportedOperation")
	unknown = sgRuleRequest(ec2AuthorizeNativeSGIngress, first, ip, 443)
	unknown.Add("GroupId", first)
	f.expect(f.call("", region, nil, unknown), 400, "InvalidParameterValue")
	for _, cidr := range []string{"192.0.2.0/24", "10.95.3.4/24", "0.0.0.0/0", "::1/128"} {
		f.expect(f.call("", region, nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, first, cidr, 443)), 400, "InvalidParameterValue")
	}
	unknown = sgRuleRequest(ec2AuthorizeNativeSGIngress, first, ip, 443)
	unknown.Set("IpPermissions.1.IpProtocol", "udp")
	f.expect(f.call("", region, nil, unknown), 400, "UnsupportedOperation")
	check(false, false)
}

func TestEC2NativeSGRuleBoundRestartAndAtomicity(t *testing.T) {
	ctx := t.Context()
	const region = "us-east-1"
	path := t.TempDir() + "/sg-rules.sqlite"
	db, s := openNativeSGFixture(t, path)
	vpc := newNativeSGVPC(t, s, region, "10.96.0.0/20")
	makeGroup := func(name string) ec2NativeSecurityGroup {
		t.Helper()
		group, err := s.createEC2NativeSecurityGroup(ctx, region, ec2NativeSecurityGroupQuery{vpc: vpc, name: name, description: "Visible only"})
		if err != nil {
			t.Fatal(err)
		}
		return group
	}
	first, second := makeGroup("first"), makeGroup("second")
	inbound := ec2NativeSGRule{from: 443, to: 443, cidr: "10.96.1.2/32"}
	outbound := ec2NativeSGRule{egress: true, from: 53, to: 53, cidr: "10.96.2.0/24"}
	set := func(id string, r ec2NativeSGRule, add bool) {
		t.Helper()
		if err := s.changeEC2NativeSGRule(ctx, region, id, r, add); err != nil {
			t.Fatal(err)
		}
	}
	set(first.ID, inbound, true)
	set(first.ID, outbound, true)
	set(second.ID, outbound, true)
	check := func(id string, want ...ec2NativeSGRule) {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		got, err := readEC2NativeSecurityGroup(ctx, tx, region, id)
		if err != nil || !slices.Equal(got.rules, want) {
			t.Fatalf("tuple snapshot %v, want %v: %v", got.rules, want, err)
		}
	}
	if _, err := db.Database().Exec(`CREATE TRIGGER alter_rule_fixture AFTER INSERT ON aws_ec2_native_security_group_rules
		WHEN NEW.from_port=444 BEGIN DELETE FROM aws_ec2_native_security_group_rules WHERE region=NEW.region AND group_id=NEW.group_id AND egress=1; END`); err != nil {
		t.Fatal(err)
	}
	if err := s.changeEC2NativeSGRule(ctx, region, first.ID, ec2NativeSGRule{from: 444, to: 444, cidr: inbound.cidr}, true); !errors.Is(err, errEC2NativeSecurityGroupState) {
		t.Fatal("readback acknowledged a changed peer direction", err)
	}
	check(first.ID, inbound, outbound)
	check(second.ID, outbound)
	if _, err := db.Database().Exec(`CREATE TRIGGER ignored_rule_remove BEFORE DELETE ON aws_ec2_native_security_group_rules
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.changeEC2NativeSGRule(ctx, region, first.ID, inbound, false); !errors.Is(err, errEC2NativeSecurityGroupState) {
		t.Fatal("readback acknowledged ignored revoke", err)
	}
	check(first.ID, inbound, outbound)
	if _, err := db.Database().Exec("DROP TRIGGER ignored_rule_remove"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = openNativeSGFixture(t, path)
	check(first.ID, inbound, outbound)
	check(second.ID, outbound)
	for port := 1024; port < 1024+ec2NativeSGRuleLimit-1; port++ {
		set(first.ID, ec2NativeSGRule{from: port, to: port, cidr: inbound.cidr}, true)
	}
	wantNativeSGError(t,
		s.changeEC2NativeSGRule(ctx, region, first.ID, ec2NativeSGRule{from: 8192, to: 8192, cidr: inbound.cidr}, true),
		"RulesPerSecurityGroupLimitExceeded")
	extra := ec2NativeSGRule{egress: true, from: 999, to: 999, cidr: outbound.cidr}
	set(first.ID, extra, true) // inbound quota does not alter outbound.
	set(first.ID, extra, false)
	set(second.ID, outbound, false)
	check(second.ID)
	if err := s.deleteEC2NativeSecurityGroup(ctx, region, first.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM aws_ec2_native_security_group_rules").Scan(&count); err != nil || count != 0 {
		t.Fatal("group delete left rule rows", err)
	}
	check(second.ID)
}
