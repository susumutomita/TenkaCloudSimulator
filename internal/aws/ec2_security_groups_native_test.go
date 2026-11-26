package aws

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func nativeSGQueryFixture(t *testing.T, action string, form url.Values) ec2NativeSecurityGroupQuery {
	t.Helper()
	q, err := parseEC2NativeSGQuery(action, form)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func wantNativeSGError(t *testing.T, err error, code string) {
	t.Helper()
	var failure *ec2Failure
	if !errors.As(err, &failure) || failure.code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func openNativeSGFixture(t *testing.T, path string) (*core.Store, *StackStore) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err := initEC2NativeSecurityGroups(db.Database()); err != nil {
		t.Fatal(err)
	}
	return db, s
}

func newNativeSGVPC(t *testing.T, s *StackStore, region string, network string) string {
	t.Helper()
	v, err := s.createNativeVPC(context.Background(), region, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func allNativeSGs(t *testing.T, s *StackStore, region string) ([]ec2NativeSecurityGroup, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return listEC2NativeSecurityGroups(ctx, tx, region)
}

func TestEC2NativeSGQueryBoundaries(t *testing.T) {
	const region, vpc, group = "us-east-1", "vpc-0123456789abcdefa", "sg-0123456789abcdefa"
	form := url.Values{
		"GroupName": {"web + app  "}, "GroupDescription": {"Web & App"}, "VpcId": {vpc}, "DryRun": {"false"},
		"TagSpecification.1.ResourceType": {"security-group"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"学習"},
	}
	q := nativeSGQueryFixture(t, "CreateSecurityGroup", form)
	if q.name != "web + app" || q.description != "Web & App" || q.vpc != vpc || q.tags["Name"] != "学習" {
		t.Fatalf("metadata normalization: %+v", q)
	}
	want := [][2]string{
		{"ec2:CreateSecurityGroup", networkARN(region, "vpc", vpc)},
		{"ec2:CreateSecurityGroup", networkARN(region, "security-group", "*")},
		{"ec2:CreateTags", networkARN(region, "security-group", "*")},
	}
	if got := q.permissions("CreateSecurityGroup", region); !reflect.DeepEqual(got, want) {
		t.Fatalf("create policy checks: %v", got)
	}
	d := nativeSGQueryFixture(t, "DeleteSecurityGroup", url.Values{"GroupId": {group}})
	if got := d.permissions("DeleteSecurityGroup", region); !reflect.DeepEqual(got, [][2]string{{"ec2:DeleteSecurityGroup", networkARN(region, "security-group", group)}}) {
		t.Fatalf("delete policy checks: %v", got)
	}
	des := nativeSGQueryFixture(t, "DescribeSecurityGroups", url.Values{"GroupId.1": {group}, "Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {vpc}})
	if got := des.permissions("DescribeSecurityGroups", region); !reflect.DeepEqual(got, [][2]string{{"ec2:DescribeSecurityGroups", "*"}}) {
		t.Fatalf("describe policy checks: %v", got)
	}
	rows, err := des.filterSecurityGroups([]ec2SecurityGroupRead{{ID: group, VPC: vpc}, {ID: "sg-01234567", VPC: vpc}})
	if err != nil || len(rows) != 1 || rows[0].ID != group {
		t.Fatalf("exact list: %+v / %v", rows, err)
	}
	_, err = des.filterSecurityGroups([]ec2SecurityGroupRead{{ID: "sg-01234567", VPC: vpc}})
	wantNativeSGError(t, err, "InvalidGroup.NotFound")
	for _, c := range []struct {
		key, value, code string
	}{
		{"GroupName", "DEFAULT  ", "InvalidGroup.Reserved"},
		{"GroupName", "sg-Test", "InvalidParameterValue"},
		{"GroupName", "a" + strings.Repeat(" ", 255), "InvalidParameterValue"},
		{"GroupDescription", "test 💡", "InvalidParameterValue"},
		{"VpcId", "", "MissingParameter"},
		{"VpcId", group, "InvalidVpcID.Malformed"},
		{"TagSpecification.1.ResourceType", "vpc", "InvalidParameterValue"},
		{"TagSpecification.1.Tag.1.Key", "aws:test", "InvalidParameterValue"},
		{"TagSpecification.1.Tag.1.Value", "x\x01", "InvalidParameterValue"},
		{"DryRun", "true", "UnsupportedOperation"},
		{"ClientToken", "unused", "UnsupportedOperation"},
		{"IpPermissions.1.IpProtocol", "tcp", "UnsupportedOperation"},
	} {
		t.Run(c.key+"_"+c.code, func(t *testing.T) {
			next, _ := url.ParseQuery(form.Encode())
			next.Set(c.key, c.value)
			_, f := parseEC2NativeSGQuery("CreateSecurityGroup", next)
			if f == nil || f.code != c.code {
				t.Fatalf("want %s, got %v", c.code, f)
			}
		})
	}
	for _, x := range []struct {
		action string
		form   url.Values
		code   string
	}{
		{"DeleteSecurityGroup", url.Values{"GroupName": {"web"}}, "UnsupportedOperation"},
		{"DeleteSecurityGroup", url.Values{"GroupId": {vpc}}, "InvalidGroupId.Malformed"},
		{"DescribeSecurityGroups", url.Values{"GroupId.1": {group, group}}, "InvalidParameterValue"},
		{"DescribeSecurityGroups", url.Values{"GroupId.2": {group}}, "InvalidParameterValue"},
		{"DescribeSecurityGroups", url.Values{"Filter.1.Value.1": {vpc}}, "InvalidParameterValue"},
		{"DescribeSecurityGroups", url.Values{"Filter.1.Name": {"group-name"}}, "UnsupportedOperation"},
		{"DescribeSecurityGroups", url.Values{"NextToken": {"local"}}, "UnsupportedOperation"},
	} {
		_, err := parseEC2NativeSGQuery(x.action, x.form)
		if err == nil || err.code != x.code {
			t.Fatalf("want %s, got %v for %v", x.code, err, x.form)
		}
	}
}

func TestEC2NativeSGPersistedLifecycleAndOwnership(t *testing.T) {
	ctx := context.Background()
	const region, other = "us-east-1", "ap-northeast-1"
	path := filepath.Join(t.TempDir(), "owned.sqlite")
	db, s := openNativeSGFixture(t, path)
	one := newNativeSGVPC(t, s, region, "10.30.0.0/16")
	two := newNativeSGVPC(t, s, region, "10.31.0.0/16")
	cfnVPC := "vpc-0000000000000000a"
	f := ec2VPCFixture{t: t, store: db}
	f.cloudFormationVPC(region, "arn:aws:cloudformation:us-east-1:000000000000:stack/sg-owner/test", cfnVPC, "10.32.0.0/16")
	q := nativeSGQueryFixture(t, "CreateSecurityGroup", url.Values{
		"GroupName": {"Web Group  "}, "GroupDescription": {"Persisted description"}, "VpcId": {one},
		"TagSpecification.1.ResourceType": {"security-group"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"application"},
	})
	_, err := s.createEC2NativeSecurityGroup(ctx, other, q)
	wantNativeSGError(t, err, "InvalidVpcID.NotFound")
	bad := q
	bad.vpc = cfnVPC
	_, err = s.createEC2NativeSecurityGroup(ctx, region, bad)
	wantNativeSGError(t, err, "UnsupportedOperation")
	bad = q
	bad.authorizeCreatedID = func(id string) error {
		if !ec2NativeSecurityGroupID.MatchString(id) {
			t.Fatalf("expected a concrete SG ID, got %q", id)
		}
		return &ec2Failure{"UnauthorizedOperation", "fixture denies assigned group", 403}
	}
	_, err = s.createEC2NativeSecurityGroup(ctx, region, bad)
	wantNativeSGError(t, err, "UnauthorizedOperation")
	initial, err := s.executeEC2NativeSGMutation(ctx, "CreateSecurityGroup", region, q)
	if err != nil || initial.GroupID == "" || initial.ARN != networkARN(region, "security-group", initial.GroupID) || len(initial.Tags) != 1 {
		t.Fatalf("create: %+v / %v", initial, err)
	}
	_, err = s.createEC2NativeSecurityGroup(ctx, region, bad)
	wantNativeSGError(t, err, "UnauthorizedOperation") // not a duplicate-name oracle for an ungranted create
	same := q
	same.name = "wEb GrouP"
	_, err = s.createEC2NativeSecurityGroup(ctx, region, same)
	wantNativeSGError(t, err, "InvalidGroup.Duplicate")
	same.vpc = two
	second, err := s.createEC2NativeSecurityGroup(ctx, region, same)
	if err != nil || second.ID == initial.GroupID {
		t.Fatalf("names should be per VPC: %+v / %v", second, err)
	}
	wantNativeSGError(t, s.deleteNativeVPC(ctx, region, one), "DependencyViolation")
	wantNativeSGError(t, s.deleteEC2NativeSecurityGroup(ctx, other, initial.GroupID), "InvalidGroup.NotFound")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = openNativeSGFixture(t, path)
	list, err := allNativeSGs(t, s, region)
	if err != nil || len(list) != 2 || list[0].ID >= list[1].ID {
		t.Fatalf("durable list: %+v / %v", list, err)
	}
	byID := map[string]ec2NativeSecurityGroup{list[0].ID: list[0], list[1].ID: list[1]}
	g := byID[initial.GroupID]
	if g.Name != "Web Group" || g.VPCID != one || g.Description != q.description || g.Tags["Name"] != "application" || len(g.Ingress) != 0 {
		t.Fatalf("stored detached metadata mismatch: %+v", g)
	}
	del := ec2NativeSecurityGroupQuery{id: initial.GroupID}
	response, err := s.executeEC2NativeSGMutation(ctx, "DeleteSecurityGroup", region, del)
	if err != nil || !response.Return || response.GroupID != initial.GroupID {
		t.Fatalf("delete: %+v / %v", response, err)
	}
	wantNativeSGError(t, s.deleteEC2NativeSecurityGroup(ctx, region, initial.GroupID), "InvalidGroup.NotFound")
	recreated, err := s.createEC2NativeSecurityGroup(ctx, region, q)
	if err != nil || recreated.ID == initial.GroupID {
		t.Fatalf("deleted unique name wasn't released: %+v / %v", recreated, err)
	}
	for _, id := range []string{recreated.ID, second.ID} {
		if err := s.deleteEC2NativeSecurityGroup(ctx, region, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{one, two} {
		if err := s.deleteNativeVPC(ctx, region, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestEC2NativeSGAtomicDuplicateAndRollback(t *testing.T) {
	ctx := context.Background()
	const region = "us-east-1"
	path := filepath.Join(t.TempDir(), "race.sqlite")
	db, first := openNativeSGFixture(t, path)
	_, second := openNativeSGFixture(t, path)
	vpc := newNativeSGVPC(t, first, region, "10.33.0.0/16")
	q := nativeSGQueryFixture(t, "CreateSecurityGroup", url.Values{"GroupName": {"web"}, "GroupDescription": {"test"}, "VpcId": {vpc}})
	sql := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Database().Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`CREATE TRIGGER test_native_sg_insert_fault BEFORE INSERT ON aws_ec2_native_security_groups BEGIN SELECT RAISE(IGNORE); END`)
	if _, err := first.createEC2NativeSecurityGroup(ctx, region, q); !errors.Is(err, errEC2NativeSecurityGroupState) {
		t.Fatalf("insert fault: %v", err)
	}
	if groups, err := allNativeSGs(t, first, region); err != nil || len(groups) != 0 {
		t.Fatalf("partial create: %+v / %v", groups, err)
	}
	sql(`DROP TRIGGER test_native_sg_insert_fault`)
	ch := make(chan error, 2)
	go func() { _, err := first.createEC2NativeSecurityGroup(ctx, region, q); ch <- err }()
	go func() { _, err := second.createEC2NativeSecurityGroup(ctx, region, q); ch <- err }()
	a, b := <-ch, <-ch
	if a != nil && b != nil || a == nil && b == nil {
		t.Fatalf("exactly one create must win: %v / %v", a, b)
	}
	if a == nil {
		a = b
	}
	wantNativeSGError(t, a, "InvalidGroup.Duplicate")
	groups, err := allNativeSGs(t, first, region)
	if err != nil || len(groups) != 1 {
		t.Fatalf("duplicate race: %+v / %v", groups, err)
	}
	id := groups[0].ID
	sql(`CREATE TRIGGER test_native_sg_delete_fault BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::SecurityGroup' BEGIN SELECT RAISE(IGNORE); END`)
	if err := first.deleteEC2NativeSecurityGroup(ctx, region, id); !errors.Is(err, errEC2NativeSecurityGroupState) {
		t.Fatalf("delete fault: %v", err)
	}
	if groups, err := allNativeSGs(t, first, region); err != nil || len(groups) != 1 || groups[0].ID != id {
		t.Fatalf("partial delete: %+v / %v", groups, err)
	}
	sql(`DROP TRIGGER test_native_sg_delete_fault`)
	sql(`DELETE FROM aws_ec2_native_security_groups WHERE region=? AND id=?`, region, id)
	if groups, err := allNativeSGs(t, first, region); !errors.Is(err, errEC2NativeSecurityGroupState) || groups != nil {
		t.Fatalf("inconsistent stored SG must fail whole listing: %+v / %v", groups, err)
	}
}
