package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const naclRegressionRegion = "us-east-1"

func naclRegressionExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func naclRegressionQuery(t *testing.T, f ec2VPCFixture, fields url.Values) ec2NetworkACLResponse {
	t.Helper()
	body := f.expect(f.call("", naclRegressionRegion, nil, fields), 200, "")
	var result ec2NetworkACLResponse
	if err := xml.Unmarshal([]byte(body), &result); err != nil || result.XMLName.Local != fields.Get("Action")+"Response" ||
		result.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || result.RequestID == "" {
		t.Fatalf("invalid native EC2 acknowledgement: %v / %s", err, body)
	}
	return result
}

func TestEC2NetworkACLNativeQueryRejectsOversizedScopeWithoutPartialState(t *testing.T) {
	f := newEC2VPCFixture(t)
	a := f.create("", url.Values{"CidrBlock": {"10.52.0.0/16"}})
	b := f.create("", url.Values{"CidrBlock": {"10.53.0.0/16"}})
	makeACL := func(vpc, token string) string {
		t.Helper()
		out := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc}, "ClientToken": {token}})
		if out.NetworkACL == nil || out.NetworkACL.VPCID != vpc || out.ClientToken != token {
			t.Fatalf("lost exact VPC or create token: %+v", out)
		}
		return out.NetworkACL.ID
	}
	aid, bid := makeACL(a.ID, "query-scope-a"), makeACL(b.ID, "query-scope-b")
	list := url.Values{"Action": {"DescribeNetworkAcls"}, "DryRun": {"false"}}
	before := naclRegressionQuery(t, f, list).NetworkACLs
	for _, selector := range []struct{ field, prefix string }{{"NetworkAclId.", "acl-"}, {"Filter.1.Value.", "vpc-"}} {
		args := url.Values{"Action": {"DescribeNetworkAcls"}}
		if selector.prefix == "vpc-" {
			args.Set("Filter.1.Name", "vpc-id")
		}
		for i := 1; i <= 51; i++ {
			args.Set(fmt.Sprintf("%s%d", selector.field, i), fmt.Sprintf("%s%017x", selector.prefix, i))
		}
		body := f.expect(f.call("", naclRegressionRegion, nil, args), 400, "InvalidParameterValue")
		if strings.Contains(body, "<networkAclSet>") || !strings.Contains(body, "at most 50 values") {
			t.Fatalf("oversized selector produced state or wrong classification: %s", body)
		}
	}
	args := url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {aid}, "NetworkAclId.2": {bid},
		"Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {b.ID}}
	out := naclRegressionQuery(t, f, args).NetworkACLs
	if out == nil || len(out.Items) != 1 || out.Items[0].ID != bid || out.Items[0].VPCID != b.ID {
		t.Fatalf("both selectors must apply without reading the other VPC: %+v", out)
	}
	fields := url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {a.ID}, "ClientToken": {"query-scope-recover"},
		"TagSpecification.1.ResourceType": {"network-acl"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"\uffff"}}
	f.expect(f.call("", naclRegressionRegion, nil, fields), 400, "InvalidParameterValue")
	if after := naclRegressionQuery(t, f, list).NetworkACLs; !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected query changed saved ACLs: %+v / %+v", before, after)
	}
	makeACL(a.ID, fields.Get("ClientToken"))
}

func TestEC2NetworkACLInconsistentLocalStateCannotReadOrDelete(t *testing.T) {
	cases := []string{"malformed JSON", "unexpected subnet", "too many tags", "invalid tag text", "reserved tag", "foreign child claim", "foreign parent claim", "malformed parent"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEC2VPCFixture(t)
			db := f.store.Database()
			vpc := f.create("", url.Values{"CidrBlock": {"10.54.0.0/16"}})
			second := f.create("", url.Values{"CidrBlock": {"10.55.0.0/16"}})
			create := func(id string) string {
				out := naclRegressionQuery(t, f, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {id}})
				if out.NetworkACL == nil {
					t.Fatal("missing created ACL")
				}
				return out.NetworkACL.ID
			}
			id, healthy := create(vpc.ID), create(second.ID)
			var edit string
			var value any
			switch name {
			case "malformed JSON":
				edit, value = "document=?", "["
			case "unexpected subnet":
				edit, value = "document=json_set(document,'$.SubnetID',?)", "subnet-01234567"
			case "too many tags":
				keys := make([]string, 51)
				for i := range keys {
					keys[i] = fmt.Sprintf(`"name%02d":"value"`, i)
				}
				edit, value = "document=json_set(document,'$.Tags',json(?))", "{"+strings.Join(keys, ",")+"}"
			case "invalid tag text":
				edit, value = "document=json_set(document,'$.Tags',json(?))", `{"Name":"\u0001"}`
			case "reserved tag":
				edit, value = "document=json_set(document,'$.Tags',json(?))", `{"AWS:hidden":"value"}`
			case "malformed parent":
				naclRegressionExec(t, db, `UPDATE aws_ec2_network_resources SET document='[' WHERE region=? AND id=?`, naclRegressionRegion, vpc.ID)
			default:
				const stack = "arn:aws:cloudformation:us-east-1:000000000000:stack/nacl-claim/test"
				f.cloudFormationVPC(naclRegressionRegion, stack, "vpc-0123456789abcdeff", "10.56.0.0/16")
				kind, claimed := ec2NetworkACLKind, id
				if name == "foreign parent claim" {
					kind, claimed = ec2NetworkACLVPCType, vpc.ID
				}
				naclRegressionExec(t, db, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(naclRegressionRegion, kind), kind, claimed, stack)
			}
			if edit != "" {
				naclRegressionExec(t, db, `UPDATE aws_ec2_network_resources SET `+edit+` WHERE region=? AND id=?`, value, naclRegressionRegion, id)
			}
			for _, args := range []url.Values{{"Action": {"DescribeNetworkAcls"}}, {"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {id}}} {
				body := f.expect(f.call("", naclRegressionRegion, nil, args), 500, "InternalError")
				if strings.Contains(body, "<networkAclSet>") || strings.Contains(body, "<return>true</return>") {
					t.Fatalf("local inconsistency appeared successful: %s", body)
				}
			}
			if name == "malformed parent" || name == "foreign parent claim" {
				f.expect(f.call("", naclRegressionRegion, nil, url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc.ID}}), 500, "InternalError")
			}
			var remaining int
			if err := db.QueryRow(`SELECT count(*) FROM aws_ec2_network_resources WHERE region=? AND kind=?`, naclRegressionRegion, ec2NetworkACLKind).Scan(&remaining); err != nil || remaining != 2 {
				t.Fatalf("validation destroyed or created resources: %d / %v", remaining, err)
			}
			out := naclRegressionQuery(t, f, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {healthy}})
			if out.NetworkACLs == nil || len(out.NetworkACLs.Items) != 1 || out.NetworkACLs.Items[0].ID != healthy {
				t.Fatalf("invalid neighbour affected an explicitly selected healthy row: %+v", out)
			}
		})
	}
}

func TestEC2NetworkACLCreateDeleteRequirePersistentReadback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nacl.sqlite")
	store, stacks := openNetworkACLState(t, path)
	ctx, db := t.Context(), store.Database()
	vpc := newNetworkACLParent(t, stacks, naclRegressionRegion, "10.57.0.0/16")
	q := ec2NetworkACLRequest{vpcID: vpc.ID, clientToken: "verify-local-receipt", tags: map[string]string{"Name": "retained"}}
	triggers := []string{
		`BEFORE INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::NetworkAcl' BEGIN SELECT RAISE(ABORT,'test fixture unavailable'); END`,
		`BEFORE INSERT ON aws_ec2_network_acl_replay BEGIN SELECT RAISE(IGNORE); END`,
		`AFTER INSERT ON aws_ec2_network_acl_replay BEGIN DELETE FROM aws_ec2_network_acl_replay WHERE token_hash=NEW.token_hash; END`,
		`AFTER INSERT ON aws_ec2_network_acl_replay BEGIN UPDATE aws_ec2_network_acl_replay SET request_hash=zeroblob(32) WHERE token_hash=NEW.token_hash; END`,
	}
	for _, trigger := range triggers {
		naclRegressionExec(t, db, "CREATE TRIGGER nacl_create_fixture "+trigger)
		if result, err := stacks.executeEC2NetworkACL(ctx, "CreateNetworkAcl", naclRegressionRegion, q); err == nil || result != nil {
			t.Fatalf("unverified create appeared successful: %+v / %v", result, err)
		}
		naclRegressionExec(t, db, `DROP TRIGGER nacl_create_fixture`)
		var receiptCount int
		if err := db.QueryRow(`SELECT count(*) FROM aws_ec2_network_acl_replay`).Scan(&receiptCount); err != nil || receiptCount != 0 {
			t.Fatalf("partial create receipt: %d / %v", receiptCount, err)
		}
		if rows, err := stacks.describeNativeNetworkACLs(ctx, naclRegressionRegion, ec2NetworkACLRequest{}); err != nil || len(rows) != 0 {
			t.Fatalf("partial create resource: %+v / %v", rows, err)
		}
	}
	created, err := stacks.createNativeNetworkACL(ctx, naclRegressionRegion, q)
	if err != nil {
		t.Fatal(err)
	}
	// Triggers emulate a storage engine reporting a SQL error, or reporting a
	// single changed row without the required absence. Only this fixture owns
	// the temporary database, not a production or another tenant's database.
	for _, trigger := range []string{
		`BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::NetworkAcl' BEGIN SELECT RAISE(ABORT,'test fixture unavailable'); END`,
		`AFTER DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::NetworkAcl' BEGIN INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision); END`,
	} {
		naclRegressionExec(t, db, "CREATE TRIGGER nacl_delete_fixture "+trigger)
		if result, err := stacks.executeEC2NetworkACL(ctx, "DeleteNetworkAcl", naclRegressionRegion, ec2NetworkACLRequest{aclID: created.ID}); err == nil || result != nil {
			t.Fatalf("unverified delete appeared successful: %+v / %v", result, err)
		}
		naclRegressionExec(t, db, `DROP TRIGGER nacl_delete_fixture`)
		replay, err := stacks.createNativeNetworkACL(ctx, naclRegressionRegion, q)
		if err != nil || !reflect.DeepEqual(replay, created) {
			t.Fatalf("delete failure lost resource or replay witness: %+v / %v", replay, err)
		}
	}
	if err := stacks.deleteNativeNetworkACL(ctx, naclRegressionRegion, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, fresh := openNetworkACLState(t, path)
	_, err = fresh.createNativeNetworkACL(ctx, naclRegressionRegion, q)
	expectNetworkACLError(t, err, "InvalidNetworkAclID.NotFound")
	if rows, err := fresh.describeNativeNetworkACLs(ctx, naclRegressionRegion, ec2NetworkACLRequest{}); err != nil || len(rows) != 0 {
		t.Fatalf("retired token resurrected resource after restart: %+v / %v", rows, err)
	}
	if err := fresh.deleteNativeVPC(ctx, naclRegressionRegion, vpc.ID); err != nil {
		t.Fatalf("the deleted resource still blocks VPC cleanup: %v", err)
	}
}

func TestEC2NetworkACLBoundsNeverEvictOrReturnTruncatedRows(t *testing.T) {
	f := newEC2VPCFixture(t)
	ctx, db := t.Context(), f.store.Database()
	vpc := f.create("", url.Values{"CidrBlock": {"10.58.0.0/16"}})
	q := ec2NetworkACLRequest{vpcID: vpc.ID, clientToken: "established"}
	first, err := f.stacks.createNativeNetworkACL(ctx, naclRegressionRegion, q)
	if err != nil {
		t.Fatal(err)
	}
	naclRegressionExec(t, db, `WITH RECURSIVE fixture(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM fixture WHERE n<?)
		INSERT INTO aws_ec2_network_acl_replay SELECT ?,CAST(printf('%032d',n) AS BLOB),zeroblob(32),printf('acl-%017x',n) FROM fixture`,
		ec2NetworkACLReplayLimit-1, naclRegressionRegion)
	_, err = f.stacks.createNativeNetworkACL(ctx, naclRegressionRegion, ec2NetworkACLRequest{vpcID: vpc.ID, clientToken: "no-eviction"})
	expectNetworkACLError(t, err, "NetworkAclLimitExceeded")
	replay, err := f.stacks.createNativeNetworkACL(ctx, naclRegressionRegion, q)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("local receipt capacity evicted a live receipt: %+v / %v", replay, err)
	}
	seedRows := `WITH RECURSIVE fixture(n) AS (VALUES(?) UNION ALL SELECT n+1 FROM fixture WHERE n<?), ids(id) AS (SELECT printf('acl-%017x',n) FROM fixture)
		INSERT INTO aws_ec2_network_resources SELECT ?,id,?,?,json_object('ID',id,'Kind',?,'Region',?,'StackID',?,'VPCID',?,'Revision',1),1 FROM ids`
	start := ec2NetworkACLRowLimit - 2
	naclRegressionExec(t, db, seedRows, 1, start, naclRegressionRegion, ec2NativeOwner, ec2NetworkACLKind, ec2NetworkACLKind, naclRegressionRegion, ec2NativeOwner, vpc.ID)
	_, err = f.stacks.createNativeNetworkACL(ctx, naclRegressionRegion, ec2NetworkACLRequest{vpcID: vpc.ID})
	expectNetworkACLError(t, err, "NetworkAclLimitExceeded")
	// A locally restored snapshot outside today's bound must not masquerade as
	// a truncated valid list. Existing ID-specific reads remain exactly scoped.
	naclRegressionExec(t, db, seedRows, start+1, start+2, naclRegressionRegion, ec2NativeOwner, ec2NetworkACLKind, ec2NetworkACLKind, naclRegressionRegion, ec2NativeOwner, vpc.ID)
	body := f.expect(f.call("", naclRegressionRegion, nil, url.Values{"Action": {"DescribeNetworkAcls"}}), 500, "InternalError")
	if strings.Contains(body, "<networkAclSet>") {
		t.Fatal("oversized inventory returned partial network ACLs")
	}
	result := naclRegressionQuery(t, f, url.Values{"Action": {"DescribeNetworkAcls"}, "NetworkAclId.1": {first.ID}})
	if result.NetworkACLs == nil || len(result.NetworkACLs.Items) != 1 || result.NetworkACLs.Items[0].ID != first.ID {
		t.Fatalf("ID scoped read lost the known row: %+v", result)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, action := range []string{"CreateNetworkAcl", "DescribeNetworkAcls", "DeleteNetworkAcl"} {
		if out, err := f.stacks.executeEC2NetworkACL(cancelled, action, naclRegressionRegion, q); !errors.Is(err, context.Canceled) || out != nil {
			t.Fatalf("cancelled %s unexpectedly produced state: %+v / %v", action, out, err)
		}
	}
}
