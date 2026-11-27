package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func networkACLEntryForm(action, id string, number int, egress bool) url.Values {
	fields := url.Values{
		"Action": {action}, "NetworkAclId": {id}, "RuleNumber": {strconv.Itoa(number)},
		"Egress": {strconv.FormatBool(egress)},
	}
	if action != ec2DeleteNetworkACLEntry {
		fields.Set("CidrBlock", "192.0.2.81/24")
		fields.Set("Protocol", "6")
		fields.Set("RuleAction", "allow")
		fields.Set("PortRange.From", "443")
		fields.Set("PortRange.To", "443")
	}
	return fields
}

func networkACLEntryACK(t *testing.T, f ec2VPCFixture, actor string, fields url.Values) {
	t.Helper()
	body := f.expect(f.call(actor, naclRegressionRegion, nil, fields), 200, "")
	var out ec2NetworkACLResponse
	if err := xml.Unmarshal([]byte(body), &out); err != nil || out.XMLName.Local != fields.Get("Action")+"Response" ||
		out.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || out.RequestID == "" ||
		out.Return == nil || !*out.Return || out.NetworkACL != nil || out.NetworkACLs != nil || out.ClientToken != "" {
		t.Fatalf("bad mutation acknowledgement: %s / %v", body, err)
	}
}

func describeExactNetworkACL(t *testing.T, f ec2VPCFixture, acl, vpc string) ec2NetworkACLRead {
	t.Helper()
	out := naclRegressionQuery(t, f, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {acl},
		"Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {vpc}})
	if out.NetworkACLs == nil || len(out.NetworkACLs.Items) != 1 {
		t.Fatalf("expected scoped ACL, got %+v", out)
	}
	r := out.NetworkACLs.Items[0]
	if r.ID != acl || r.VPCID != vpc || r.Default || r.Mode != "detached-config-only" {
		t.Fatalf("wrong ACL scope: %+v", r)
	}
	return r
}

func TestEC2NetworkACLEntryScopedLifecycleAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acl-rules.sqlite")
	open := func() ec2VPCFixture {
		store, stacks := openNetworkACLState(t, path)
		return ec2VPCFixture{t, &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}, stacks, store}
	}
	f := open()
	vpc := f.create("", url.Values{"CidrBlock": {"10.60.0.0/16"}}).ID
	peer := f.create("", url.Values{"CidrBlock": {"10.61.0.0/16"}}).ID
	create := url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}, "ClientToken": {"acl-entries-fixture"}}
	acl := *naclRegressionQuery(t, f, create).NetworkACL
	unchanged := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {peer}}).NetworkACL
	exact := networkARN(naclRegressionRegion, "network-acl", acl.ID)
	for name, statements := range map[string]string{
		"acl-entries": fmt.Sprintf(`{"Effect":"Allow","Action":["ec2:CreateNetworkAclEntry","ec2:ReplaceNetworkAclEntry","ec2:DeleteNetworkAclEntry"],"Resource":%q}`, exact),
		"acl-read":    `{"Effect":"Allow","Action":"ec2:DescribeNetworkAcls","Resource":"*"}`,
		"acl-denied": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:*","Resource":"*"},`+
			`{"Effect":"Deny","Action":"ec2:CreateNetworkAclEntry","Resource":%q}`, exact),
	} {
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + statements + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(name, policy); err != nil {
			t.Fatal(err)
		}
	}
	before := replaceTestInventory(t, f.store.Database())
	add := networkACLEntryForm(ec2CreateNetworkACLEntry, acl.ID, 150, false)
	bound := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: naclRegressionRegion, instance: "i-00000000"})
	f.expect(f.call("acl-read", naclRegressionRegion, nil, add), 403, "UnauthorizedOperation")
	f.expect(f.call("acl-denied", naclRegressionRegion, nil, add), 403, "UnauthorizedOperation")
	f.expect(f.call("", naclRegressionRegion, bound, add), 403, "UnauthorizedOperation")
	f.expect(f.call("acl-entries", "ap-northeast-1", nil, add), 403, "UnauthorizedOperation")
	f.expect(f.call("", "ap-northeast-1", nil, add), 400, "InvalidNetworkAclID.NotFound")
	f.expect(f.call("acl-entries", naclRegressionRegion, nil, networkACLEntryForm(ec2CreateNetworkACLEntry, unchanged.ID, 150, false)), 403, "UnauthorizedOperation")
	f.expect(f.call("", naclRegressionRegion, nil, networkACLEntryForm(ec2ReplaceNetworkACLEntry, acl.ID, 150, false)), 400, "InvalidNetworkAclEntry.NotFound")
	f.expect(f.call("", naclRegressionRegion, nil, networkACLEntryForm(ec2DeleteNetworkACLEntry, acl.ID, 150, true)), 400, "InvalidNetworkAclEntry.NotFound")
	if !reflect.DeepEqual(before, replaceTestInventory(t, f.store.Database())) {
		t.Fatal("rejected entry requests changed persisted rows")
	}

	networkACLEntryACK(t, f, "acl-entries", add)
	f.expect(f.call("acl-entries", naclRegressionRegion, nil, add), 400, "NetworkAclEntryAlreadyExists")
	egress := networkACLEntryForm(ec2CreateNetworkACLEntry, acl.ID, 150, true)
	egress.Set("Protocol", "-1")
	egress.Del("PortRange.From")
	egress.Del("PortRange.To")
	egress.Set("RuleAction", "deny")
	networkACLEntryACK(t, f, "acl-entries", egress)
	networkACLEntryACK(t, f, "acl-entries", networkACLEntryForm(ec2CreateNetworkACLEntry, acl.ID, 100, false))
	replace := networkACLEntryForm(ec2ReplaceNetworkACLEntry, acl.ID, 150, false)
	replace.Set("Protocol", "17")
	replace.Set("CidrBlock", "198.51.100.157/24")
	replace.Set("PortRange.From", "53")
	replace.Set("PortRange.To", "54")
	networkACLEntryACK(t, f, "acl-entries", replace)
	changed := replaceTestInventory(t, f.store.Database())
	networkACLEntryACK(t, f, "acl-entries", replace)
	if !reflect.DeepEqual(changed, replaceTestInventory(t, f.store.Database())) {
		t.Fatal("identical replace created a new persisted generation")
	}
	key := [2]string{naclRegressionRegion, acl.ID}
	var stored EC2NetworkResource
	const docField = 2 // replaceTestInventory: {stack_id, kind, document, revision}
	if err := json.Unmarshal([]byte(changed[key][docField]), &stored); err != nil || stored.Revision != 5 ||
		stored.NativeACLRules == nil || stored.NativeACLRules.Updates != 4 || len(stored.NativeACLRules.Entries) != 3 {
		t.Fatalf("wrong persisted ACL generation: %+v / %v", stored, err)
	}
	delete(changed, key)
	delete(before, key)
	if !reflect.DeepEqual(before, changed) {
		t.Fatal("mutations affected another network resource")
	}
	want := []ec2NetworkACLEntry{
		{Rule: 100, Protocol: "6", Action: "allow", CIDR: "192.0.2.0/24", PortRange: &ec2NetworkACLPortRange{443, 443}},
		{Rule: 150, Protocol: "17", Action: "allow", CIDR: "198.51.100.0/24", PortRange: &ec2NetworkACLPortRange{53, 54}},
		acl.Entries[0],
		{Rule: 150, Protocol: "-1", Action: "deny", Egress: true, CIDR: "192.0.2.0/24"},
		acl.Entries[1],
	}
	if got := describeExactNetworkACL(t, f, acl.ID, vpc).Entries; !reflect.DeepEqual(got, want) {
		t.Fatalf("rule readback not canonical: %+v", got)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	if got := describeExactNetworkACL(t, f, acl.ID, vpc).Entries; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules did not survive restart: %+v", got)
	}
	if replay := naclRegressionQuery(t, f, create).NetworkACL; replay == nil || replay.ID != acl.ID || !reflect.DeepEqual(replay.Entries, want) {
		t.Fatalf("existing create token created a different ACL or reset rules: %+v", replay)
	}
	networkACLEntryACK(t, f, "acl-entries", networkACLEntryForm(ec2DeleteNetworkACLEntry, acl.ID, 150, true))
	if got := describeExactNetworkACL(t, f, acl.ID, vpc).Entries; !reflect.DeepEqual(got, append(want[:3:3], want[4])) {
		t.Fatalf("deleting one direction changed the other: %+v", got)
	}
	if got := describeExactNetworkACL(t, f, unchanged.ID, peer); !reflect.DeepEqual(got, *unchanged) {
		t.Fatalf("peer ACL changed: %+v", got)
	}
	naclRegressionQuery(t, f, url.Values{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {acl.ID}})
	f.expect(f.call("", naclRegressionRegion, nil, add), 400, "InvalidNetworkAclID.NotFound")
	f.expect(f.call("", naclRegressionRegion, nil, create), 400, "InvalidNetworkAclID.NotFound")
	naclRegressionQuery(t, f, url.Values{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {unchanged.ID}})
	for _, id := range []string{vpc, peer} {
		f.expect(f.call("", naclRegressionRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {id}}), 200, "")
	}
}

func TestEC2NetworkACLEntryQueryRejectsUnsupportedAndReservedInputs(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.62.0.0/16"}}).ID
	acl := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}}).NetworkACL.ID
	before := replaceTestInventory(t, f.store.Database())
	for _, test := range []struct {
		key, value, code string
	}{
		{"NetworkAclId", "vpc-01234567", "InvalidNetworkAclId.Malformed"},
		{"Egress", "", "MissingParameter"}, {"Egress", "TRUE", "InvalidParameterValue"},
		{"RuleNumber", "0", "InvalidParameterValue"}, {"RuleNumber", "32767", "InvalidParameterValue"},
		{"RuleNumber", "999999", "InvalidParameterValue"}, {"RuleNumber", "+100", "InvalidParameterValue"},
		{"CidrBlock", "", "MissingParameter"}, {"CidrBlock", "2001:db8::/32", "InvalidParameterValue"},
		{"Ipv6CidrBlock", "2001:db8::/32", "UnsupportedOperation"}, {"Protocol", "1", "UnsupportedOperation"},
		{"Protocol", "-1", "UnsupportedOperation"}, {"Icmp.Type", "8", "UnsupportedOperation"},
		{"RuleAction", "accept", "InvalidParameterValue"}, {"PortRange.From", "", "MissingParameter"},
		{"PortRange.From", "444", "InvalidParameterValue"}, {"PortRange.To", "65536", "InvalidParameterValue"},
		{"DryRun", "true", "UnsupportedOperation"}, {"VpcId", vpc, "UnsupportedOperation"},
		{"PortRange.from", "443", "UnsupportedOperation"},
	} {
		t.Run(test.key+"/"+test.value, func(t *testing.T) {
			f.t = t
			q := networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 100, false)
			if test.value == "" {
				q.Del(test.key)
			} else {
				q.Set(test.key, test.value)
			}
			f.expect(f.call("", naclRegressionRegion, nil, q), 400, test.code)
		})
	}
	f.t = t
	q := networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 100, false)
	q.Add("RuleNumber", "100")
	f.expect(f.call("", naclRegressionRegion, nil, q), 400, "InvalidParameterValue")
	for _, action := range []string{ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry} {
		f.expect(f.call("", naclRegressionRegion, nil, networkACLEntryForm(action, acl, 32767, false)), 400, "InvalidParameterValue")
	}
	q = networkACLEntryForm(ec2DeleteNetworkACLEntry, acl, 100, true)
	q.Set("CidrBlock", "192.0.2.0/24")
	f.expect(f.call("", naclRegressionRegion, nil, q), 400, "UnsupportedOperation")
	if !reflect.DeepEqual(before, replaceTestInventory(t, f.store.Database())) {
		t.Fatal("invalid request wrote ACL state")
	}
	f.expect(f.call("", naclRegressionRegion, nil, url.Values{"Action": {"ReplaceNetworkAclAssociation"}}), 400, "MissingParameter")
}

func TestEC2NetworkACLEntryLimitAndTransactionalRefusals(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.63.0.0/16"}}).ID
	acl := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}}).NetworkACL.ID
	for i := ec2NetworkACLRuleLimit; i > 0; i-- {
		networkACLEntryACK(t, f, "", networkACLEntryForm(ec2CreateNetworkACLEntry, acl, i, false))
	}
	add := networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 21, false)
	f.expect(f.call("", naclRegressionRegion, nil, add), 400, "NetworkAclEntryLimitExceeded")
	networkACLEntryACK(t, f, "", networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 21, true))
	q := networkACLEntryForm(ec2ReplaceNetworkACLEntry, acl, 20, false)
	q.Set("PortRange.From", "0")
	q.Set("PortRange.To", "65535")
	networkACLEntryACK(t, f, "", q)
	entries := describeExactNetworkACL(t, f, acl, vpc).Entries
	if len(entries) != ec2NetworkACLRuleLimit+3 || entries[19].PortRange == nil || entries[19].PortRange.From != 0 ||
		entries[19].PortRange.To != 65535 || entries[20].Rule != 32767 || entries[21].Rule != 21 || !entries[21].Egress {
		t.Fatalf("rule bound or order differs: %+v", entries)
	}
	before := replaceTestInventory(t, f.store.Database())
	db := f.store.Database()
	for _, trigger := range []string{
		`BEFORE UPDATE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::NetworkAcl' BEGIN SELECT RAISE(IGNORE); END`,
		`AFTER UPDATE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::NetworkAcl' BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.NativeACLRules.Updates',1) WHERE id=NEW.id AND region=NEW.region; END`,
	} {
		naclRegressionExec(t, db, "CREATE TRIGGER nacl_rule_fixture "+trigger)
		networkACLEntryACK(t, f, "", q) // identical replace must not reach the write trigger
		for _, mutation := range []url.Values{networkACLEntryForm(ec2DeleteNetworkACLEntry, acl, 20, false),
			networkACLEntryForm(ec2ReplaceNetworkACLEntry, acl, 20, false), networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 22, true)} {
			body := f.expect(f.call("", naclRegressionRegion, nil, mutation), 500, "InternalError")
			if strings.Contains(body, "<return>true</return>") || !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
				t.Fatal("failed entry transaction acknowledged or persisted changes")
			}
		}
		naclRegressionExec(t, db, "DROP TRIGGER nacl_rule_fixture")
	}
	networkACLEntryACK(t, f, "", networkACLEntryForm(ec2DeleteNetworkACLEntry, acl, 20, false))
	networkACLEntryACK(t, f, "", add)
}

func TestEC2NetworkACLEntryRejectsForeignAndInconsistentOwnership(t *testing.T) {
	f := newEC2VPCFixture(t)
	region := naclRegressionRegion
	vpc := f.create("", url.Values{"CidrBlock": {"10.64.0.0/16"}}).ID
	acl := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}}).NetworkACL.ID
	const stack = "arn:aws:cloudformation:us-east-1:000000000000:stack/nacl-entry-claim/fixture"
	const cfvpc, foreign = "vpc-0123456789abcdeff", "acl-01234567"
	f.cloudFormationVPC(region, stack, cfvpc, "10.65.0.0/16")
	f.writeNetwork(EC2NetworkResource{ID: foreign, Kind: ec2NetworkACLKind, Region: region, StackID: stack, VPCID: cfvpc, Revision: 1})
	db := f.store.Database()
	unchanged := func(q url.Values, status int, code string) {
		t.Helper()
		before := replaceTestInventory(t, db)
		f.expect(f.call("", region, nil, q), status, code)
		if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatalf("%s altered owned data", code)
		}
	}
	unchanged(networkACLEntryForm(ec2CreateNetworkACLEntry, foreign, 100, false), 400, "InvalidNetworkAclID.NotFound")
	add := networkACLEntryForm(ec2CreateNetworkACLEntry, acl, 100, false)
	var original string
	if err := db.QueryRow(`SELECT document FROM aws_cfn_stacks WHERE id=?`, stack).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, claim := range []StackResource{{Type: ec2NetworkACLVPCType, PhysicalID: vpc}, {Type: ec2NetworkACLKind, PhysicalID: acl}} {
		namespace := stackResourceNamespace(region, claim.Type)
		naclRegressionExec(t, db, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, namespace, claim.Type, claim.PhysicalID, stack)
		unchanged(add, 500, "InternalError")
		naclRegressionExec(t, db, `DELETE FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?`, namespace, claim.Type, claim.PhysicalID)
		var row StackRecord
		if err := json.Unmarshal([]byte(original), &row); err != nil {
			t.Fatal(err)
		}
		row.Resources = append(row.Resources, claim)
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		naclRegressionExec(t, db, `UPDATE aws_cfn_stacks SET document=? WHERE id=?`, string(raw), stack)
		unchanged(add, 500, "InternalError")
		naclRegressionExec(t, db, `UPDATE aws_cfn_stacks SET document=? WHERE id=?`, original, stack)
	}
	networkACLEntryACK(t, f, "", add)
	naclRegressionExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.NativeACLRules.Entries[0].Rule',32767) WHERE id=? AND region=?`, acl, region)
	unchanged(networkACLEntryForm(ec2DeleteNetworkACLEntry, acl, 100, false), 500, "InternalError")
	unchanged(url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {acl}}, 500, "InternalError")
}
