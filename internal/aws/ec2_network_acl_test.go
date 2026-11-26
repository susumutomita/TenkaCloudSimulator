package aws

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func mustNetworkACLRequest(t *testing.T, action string, fields url.Values) ec2NetworkACLRequest {
	t.Helper()
	q, f := parseEC2NetworkACLRequest(action, fields)
	if f != nil {
		t.Fatal(f)
	}
	return q
}

func expectNetworkACLError(t *testing.T, err error, code string) {
	t.Helper()
	var f *ec2Failure
	if !errors.As(err, &f) || f.code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func openNetworkACLState(t *testing.T, path string) (*core.Store, *StackStore) {
	t.Helper()
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stacks, err := NewStackStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err := initEC2NetworkACLReplay(store.Database()); err != nil {
		t.Fatal(err)
	}
	return store, stacks
}

func newNetworkACLParent(t *testing.T, s *StackStore, region, cidr string) EC2NetworkResource {
	t.Helper()
	r, err := s.createNativeVPC(context.Background(), region, cidr, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEC2NetworkACLQueryBoundsAndAuthority(t *testing.T) {
	const vpc = "vpc-0123456789abcdefa"
	const acl = "acl-0123456789abcdefa"
	const region = "us-east-1"
	q := mustNetworkACLRequest(t, "CreateNetworkAcl", url.Values{
		"VpcId": {vpc}, "ClientToken": {"a-b_c"}, "DryRun": {"false"},
		"TagSpecification.1.ResourceType": {"network-acl"}, "TagSpecification.1.Tag.1.Key": {"Name"},
		"TagSpecification.1.Tag.1.Value": {"学習 & control"}, "TagSpecification.1.Tag.2.Key": {"Empty"},
	})
	if q.vpcID != vpc || q.clientToken != "a-b_c" || len(q.tags) != 2 || q.tags["Name"] != "学習 & control" || q.tags["Empty"] != "" {
		t.Fatalf("create did not preserve inputs: %+v", q)
	}
	want := [][2]string{
		{"ec2:CreateNetworkAcl", "arn:aws:ec2:us-east-1:000000000000:vpc/" + vpc},
		{"ec2:CreateNetworkAcl", "arn:aws:ec2:us-east-1:000000000000:network-acl/*"},
		{"ec2:CreateTags", "arn:aws:ec2:us-east-1:000000000000:network-acl/*"},
	}
	if got := q.permissions("CreateNetworkAcl", region); !reflect.DeepEqual(got, want) {
		t.Fatalf("create authority: %v", got)
	}
	list := mustNetworkACLRequest(t, "DescribeNetworkAcls", url.Values{
		"NetworkAclId.1": {acl}, "NetworkAclId.2": {"acl-00000000"},
		"Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {vpc},
	})
	if !reflect.DeepEqual(list.ids, []string{"acl-00000000", acl}) || !reflect.DeepEqual(list.filterVPCs, []string{vpc}) ||
		!reflect.DeepEqual(list.permissions("DescribeNetworkAcls", region), [][2]string{{"ec2:DescribeNetworkAcls", "*"}}) {
		t.Fatalf("list contract: %+v", list)
	}
	del := mustNetworkACLRequest(t, "DeleteNetworkAcl", url.Values{"NetworkAclId": {acl}})
	if del.aclID != acl || !reflect.DeepEqual(del.permissions("DeleteNetworkAcl", region), [][2]string{
		{"ec2:DeleteNetworkAcl", "arn:aws:ec2:us-east-1:000000000000:network-acl/" + acl},
	}) {
		t.Fatalf("delete authority: %+v", del)
	}

	for _, c := range []struct {
		action string
		fields url.Values
		code   string
	}{
		{"CreateNetworkAcl", url.Values{}, "MissingParameter"},
		{"CreateNetworkAcl", url.Values{"VpcId": {acl}}, "InvalidVpcID.Malformed"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc, vpc}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "ClientToken": {strings.Repeat("a", 65)}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "ClientToken": {"\n"}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "ClientToken": {"日"}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "DryRun": {"true"}}, "UnsupportedOperation"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "SubnetId": {"subnet-01234567"}}, "UnsupportedOperation"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"vpc"}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "TagSpecification.1.Tag.1.Key": {"Name"}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"network-acl"}, "TagSpecification.1.Tag.2.Key": {"Name"}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"network-acl"}, "TagSpecification.1.Tag.1.Key": {"Name\x00"}}, "InvalidParameterValue"},
		{"CreateNetworkAcl", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"network-acl"}, "TagSpecification.1.Tag.1.Key": {"AWS:Name"}}, "InvalidParameterValue"},
		{"DescribeNetworkAcls", url.Values{"NetworkAclId.1": {vpc}}, "InvalidNetworkAclId.Malformed"},
		{"DescribeNetworkAcls", url.Values{"NetworkAclId.2": {acl}}, "InvalidParameterValue"},
		{"DescribeNetworkAcls", url.Values{"NetworkAclId.1": {acl}, "NetworkAclId.2": {acl}}, "InvalidParameterValue"},
		{"DescribeNetworkAcls", url.Values{"Filter.1.Name": {"entry.rule-number"}}, "UnsupportedOperation"},
		{"DescribeNetworkAcls", url.Values{"Filter.1.Name": {"vpc-id"}}, "InvalidParameterValue"},
		{"DescribeNetworkAcls", url.Values{"Filter.1.Value.1": {vpc}}, "InvalidParameterValue"},
		{"DescribeNetworkAcls", url.Values{"Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {acl}}, "InvalidVpcID.Malformed"},
		{"DescribeNetworkAcls", url.Values{"NextToken": {"anything"}}, "UnsupportedOperation"},
		{"DeleteNetworkAcl", url.Values{}, "MissingParameter"},
		{"DeleteNetworkAcl", url.Values{"NetworkAclId": {vpc}}, "InvalidNetworkAclId.Malformed"},
		{"CreateNetworkAclEntry", url.Values{}, "UnsupportedOperation"},
	} {
		t.Run(c.action+"_"+c.code, func(t *testing.T) {
			_, f := parseEC2NetworkACLRequest(c.action, c.fields)
			if f == nil || f.code != c.code {
				t.Fatalf("want %s, got %v", c.code, f)
			}
		})
	}
}

func TestEC2NetworkACLPersistedDetachedLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nacl.sqlite")
	store, s := openNetworkACLState(t, path)
	const region, otherRegion = "us-east-1", "ap-northeast-1"
	vpc := newNetworkACLParent(t, s, region, "10.25.0.0/16")
	second := newNetworkACLParent(t, s, region, "10.27.0.0/16")
	other := newNetworkACLParent(t, s, otherRegion, "10.26.0.0/16")
	fixture := ec2VPCFixture{t: t, store: store}
	fixture.cloudFormationVPC(region, "arn:aws:cloudformation:us-east-1:000000000000:stack/nacl-parent/test", "vpc-0000000000000000a", "10.28.0.0/16")
	q := mustNetworkACLRequest(t, "CreateNetworkAcl", url.Values{
		"VpcId": {vpc.ID}, "ClientToken": {"fixture-stable"},
		"TagSpecification.1.ResourceType": {"network-acl"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"detached"},
	})
	denied := q
	denied.authorizeCreateID = func(id string) error {
		if !ec2NetworkACLIDPattern.MatchString(id) {
			t.Fatalf("resource authorization must use the actual ACL ID, got %q", id)
		}
		return &ec2Failure{"UnauthorizedOperation", "fixture denial", 403}
	}
	_, err := s.createNativeNetworkACL(ctx, region, denied)
	expectNetworkACLError(t, err, "UnauthorizedOperation")
	if rows, err := s.describeNativeNetworkACLs(ctx, region, ec2NetworkACLRequest{}); err != nil || len(rows) != 0 {
		t.Fatalf("denied concrete ACL created state: %+v / %v", rows, err)
	}
	_, err = s.createNativeNetworkACL(ctx, otherRegion, q)
	expectNetworkACLError(t, err, "InvalidVpcID.NotFound")
	foreign := q
	foreign.vpcID = "vpc-0000000000000000a"
	_, err = s.createNativeNetworkACL(ctx, region, foreign)
	expectNetworkACLError(t, err, "UnsupportedOperation")
	created, err := s.executeEC2NetworkACL(ctx, "CreateNetworkAcl", region, q)
	if err != nil {
		t.Fatal(err)
	}
	if created.NetworkACL.Mode != "detached-config-only" || created.NetworkACL.Default || created.NetworkACL.VPCID != vpc.ID ||
		!ec2NetworkACLIDPattern.MatchString(created.NetworkACL.ID) || len(created.NetworkACL.Entries) != 2 ||
		created.NetworkACL.Entries[0].Egress || !created.NetworkACL.Entries[1].Egress || created.ClientToken != q.clientToken {
		t.Fatalf("create contract: %+v / %+v", created, created.NetworkACL)
	}
	encoded, err := xml.Marshal(created)
	if err != nil || !strings.Contains(string(encoded), "<associationSet></associationSet>") ||
		!strings.Contains(string(encoded), "<simulatorNetworkAclMode>detached-config-only</simulatorNetworkAclMode>") {
		t.Fatalf("detached wire: %s, %v", encoded, err)
	}
	id := created.NetworkACL.ID
	_, err = s.createNativeNetworkACL(ctx, region, denied)
	expectNetworkACLError(t, err, "UnauthorizedOperation")
	expectNetworkACLError(t, s.deleteNativeVPC(ctx, region, vpc.ID), "DependencyViolation")
	different := q
	different.vpcID = second.ID
	_, err = s.createNativeNetworkACL(ctx, region, different)
	expectNetworkACLError(t, err, "IdempotentParameterMismatch")
	different = q
	different.tags = map[string]string{"Name": "different"}
	_, err = s.createNativeNetworkACL(ctx, region, different)
	expectNetworkACLError(t, err, "IdempotentParameterMismatch")
	different = q
	different.vpcID = other.ID
	r, err := s.createNativeNetworkACL(ctx, otherRegion, different)
	if err != nil || r.ID == id {
		t.Fatalf("token must be scoped by region: %+v / %v", r, err)
	}
	_, err = s.describeNativeNetworkACLs(ctx, otherRegion, ec2NetworkACLRequest{ids: []string{id}})
	expectNetworkACLError(t, err, "InvalidNetworkAclID.NotFound")

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = openNetworkACLState(t, path)
	list, err := s.executeEC2NetworkACL(ctx, "DescribeNetworkAcls", region, ec2NetworkACLRequest{filterVPCs: []string{vpc.ID}})
	if err != nil || list == nil || list.NetworkACLs == nil || !reflect.DeepEqual(list.NetworkACLs.Items, []ec2NetworkACLRead{*created.NetworkACL}) {
		t.Fatalf("restart readback: %+v / %v", list, err)
	}
	miss, err := s.describeNativeNetworkACLs(ctx, region, ec2NetworkACLRequest{filterVPCs: []string{second.ID}})
	if err != nil || len(miss) != 0 {
		t.Fatalf("filter readback: %+v / %v", miss, err)
	}
	replayed, err := s.createNativeNetworkACL(ctx, region, q)
	if err != nil || replayed.ID != id || replayed.Revision != 1 {
		t.Fatalf("restart replay: %+v / %v", replayed, err)
	}
	deleted, err := s.executeEC2NetworkACL(ctx, "DeleteNetworkAcl", region, ec2NetworkACLRequest{aclID: id})
	if err != nil || deleted.Return == nil || !*deleted.Return {
		t.Fatalf("delete: %+v / %v", deleted, err)
	}
	_, err = s.createNativeNetworkACL(ctx, region, q)
	expectNetworkACLError(t, err, "InvalidNetworkAclID.NotFound")
	expectNetworkACLError(t, s.deleteNativeNetworkACL(ctx, region, id), "InvalidNetworkAclID.NotFound")
	if empty, err := s.describeNativeNetworkACLs(ctx, region, ec2NetworkACLRequest{}); err != nil || len(empty) != 0 {
		t.Fatalf("stale or recreated ACL: %+v / %v", empty, err)
	}
	if err := s.deleteNativeVPC(ctx, region, vpc.ID); err != nil {
		t.Fatalf("parent cleanup: %v", err)
	}
}

func TestEC2NetworkACLReplaySameTokenIsAtomicAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nacl.sqlite")
	_, first := openNetworkACLState(t, path)
	_, second := openNetworkACLState(t, path)
	vpc := newNetworkACLParent(t, first, "us-east-1", "10.30.0.0/16")
	q := mustNetworkACLRequest(t, "CreateNetworkAcl", url.Values{"VpcId": {vpc.ID}, "ClientToken": {"fixture-concurrent"}})
	type result struct {
		id  string
		err error
	}
	replies := make(chan result, 2)
	start := make(chan struct{})
	for _, s := range []*StackStore{first, second} {
		go func(s *StackStore) {
			<-start
			r, err := s.createNativeNetworkACL(ctx, "us-east-1", q)
			replies <- result{r.ID, err}
		}(s)
	}
	close(start)
	a, b := <-replies, <-replies
	if a.err != nil || b.err != nil || a.id == "" || a.id != b.id {
		t.Fatalf("concurrent retries did not share one object: %+v / %+v", a, b)
	}
	rows, err := second.describeNativeNetworkACLs(ctx, "us-east-1", ec2NetworkACLRequest{})
	if err != nil || len(rows) != 1 || rows[0].ID != a.id {
		t.Fatalf("atomic readback: %+v / %v", rows, err)
	}
}

func TestEC2NetworkACLFailClosedOnIncompleteWriteAndBrokenParent(t *testing.T) {
	ctx := context.Background()
	store, s := openNetworkACLState(t, filepath.Join(t.TempDir(), "nacl.sqlite"))
	vpc := newNetworkACLParent(t, s, "us-east-1", "10.31.0.0/16")
	q := mustNetworkACLRequest(t, "CreateNetworkAcl", url.Values{"VpcId": {vpc.ID}, "ClientToken": {"fixture-rollback"}})
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := store.Database().Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`CREATE TRIGGER test_replay_fault BEFORE INSERT ON aws_ec2_network_acl_replay BEGIN SELECT RAISE(ABORT,'fixture'); END`)
	if _, err := s.createNativeNetworkACL(ctx, "us-east-1", q); err == nil {
		t.Fatal("mutated with no committed replay receipt")
	}
	if rows, err := s.describeNativeNetworkACLs(ctx, "us-east-1", ec2NetworkACLRequest{}); err != nil || len(rows) != 0 {
		t.Fatalf("partial write: %+v / %v", rows, err)
	}
	sql(`DROP TRIGGER test_replay_fault`)
	ok, err := s.createNativeNetworkACL(ctx, "us-east-1", q)
	if err != nil {
		t.Fatal(err)
	}
	sql(fmt.Sprintf(`CREATE TRIGGER test_delete_fault BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='%s' BEGIN SELECT RAISE(IGNORE); END`, ec2NetworkACLKind))
	if err := s.deleteNativeNetworkACL(ctx, "us-east-1", ok.ID); !errors.Is(err, errNetworkACLState) {
		t.Fatalf("delete failure appeared successful: %v", err)
	}
	if rows, err := s.describeNativeNetworkACLs(ctx, "us-east-1", ec2NetworkACLRequest{}); err != nil || len(rows) != 1 || rows[0].ID != ok.ID {
		t.Fatalf("failed delete lost original state: %+v / %v", rows, err)
	}
	sql(`DROP TRIGGER test_delete_fault`)
	sql(`DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?`, "us-east-1", vpc.ID)
	if rows, err := s.describeNativeNetworkACLs(ctx, "us-east-1", ec2NetworkACLRequest{}); !errors.Is(err, errNetworkACLState) || rows != nil {
		t.Fatalf("broken parent appeared as successful read: %+v / %v", rows, err)
	}
	if err := s.deleteNativeNetworkACL(ctx, "us-east-1", ok.ID); !errors.Is(err, errNetworkACLState) {
		t.Fatalf("inconsistent ownership was not preserved: %v", err)
	}
}
