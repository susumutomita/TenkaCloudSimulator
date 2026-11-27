package aws

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func nativeRouteTableTestStore(t *testing.T, path string) (*core.Store, *StackStore) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err := initEC2NativeRouteTableReplay(db.Database()); err != nil {
		t.Fatal(err)
	}
	return db, store
}

func wantNativeRouteTableError(t *testing.T, err error, code string) {
	t.Helper()
	var f *ec2Failure
	if !errors.As(err, &f) || f.code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func nativeRouteTableTestVPC(t *testing.T, store *StackStore, region, cidr string) string {
	t.Helper()
	vpc, err := store.createNativeVPC(t.Context(), region, cidr, nil)
	if err != nil {
		t.Fatal(err)
	}
	return vpc.ID
}

func nativeRouteTableListForTest(t *testing.T, store *StackStore, region string) ([]EC2NetworkResource, error) {
	t.Helper()
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return listNativeRouteTables(t.Context(), tx, region)
}

func TestEC2NativeRouteTableMutationQuery(t *testing.T) {
	const region, vpc, id = "us-east-1", "vpc-0123456789abcdefa", "rtb-0123456789abcdefa"
	create := url.Values{
		"VpcId": {vpc}, "DryRun": {"false"}, "ClientToken": {"Sample-Fixture"},
		"TagSpecification.1.ResourceType": {"route-table"},
		"TagSpecification.1.Tag.1.Key":    {"Name"},
		"TagSpecification.1.Tag.1.Value":  {"学習 & <local>"},
		"TagSpecification.1.Tag.2.Key":    {"Empty"},
	}
	q, failure := parseEC2NativeRouteTableMutation("CreateRouteTable", create)
	if failure != nil || q.vpc != vpc || q.clientToken != "Sample-Fixture" || !reflect.DeepEqual(q.tags, map[string]string{"Name": "学習 & <local>", "Empty": ""}) {
		t.Fatalf("create parse: %+v / %v", q, failure)
	}
	parentARN, familyARN := networkARN(region, "vpc", vpc), networkARN(region, "route-table", "*")
	if got := q.permissions("CreateRouteTable", region); !reflect.DeepEqual(got, [][2]string{
		{"ec2:CreateRouteTable", parentARN}, {"ec2:CreateRouteTable", familyARN}, {"ec2:CreateTags", familyARN},
	}) {
		t.Fatalf("parent and family authorization: %v", got)
	}
	exact := networkARN(region, "route-table", id)
	if got := q.createPermissions(region, id); !reflect.DeepEqual(got, [][2]string{{"ec2:CreateRouteTable", exact}, {"ec2:CreateTags", exact}}) {
		t.Fatalf("assigned route table authorization: %v", got)
	}
	del, failure := parseEC2NativeRouteTableMutation("DeleteRouteTable", url.Values{"RouteTableId": {id}})
	if failure != nil || !reflect.DeepEqual(del.permissions("DeleteRouteTable", region), [][2]string{{"ec2:DeleteRouteTable", exact}}) {
		t.Fatalf("delete parse or permission: %+v / %v", del, failure)
	}
	for _, tc := range []struct {
		action, code string
		fields       url.Values
	}{
		{"CreateRouteTable", "MissingParameter", nil},
		{"CreateRouteTable", "InvalidVpcID.Malformed", url.Values{"VpcId": {id}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc, vpc}}},
		{"CreateRouteTable", "UnsupportedOperation", url.Values{"VpcId": {vpc}, "DryRun": {"true"}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc}, "ClientToken": {"\x01"}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc}, "TagSpecification.1.Tag.1.Key": {"Name"}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"vpc"}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"route-table"}, "TagSpecification.1.Tag.2.Key": {"Name"}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"route-table"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"a\x01"}}},
		{"CreateRouteTable", "InvalidParameterValue", url.Values{"VpcId": {vpc}, "TagSpecification.1.ResourceType": {"route-table"}, "TagSpecification.1.Tag.1.Key": {"aws:fixture"}}},
		{"CreateRouteTable", "UnsupportedOperation", url.Values{"VpcId": {vpc}, "GatewayId": {"local"}}},
		{"DeleteRouteTable", "MissingParameter", nil},
		{"DeleteRouteTable", "InvalidRouteTableID.Malformed", url.Values{"RouteTableId": {vpc}}},
		{"DeleteRouteTable", "UnsupportedOperation", url.Values{"RouteTableId": {id}, "ClientToken": {"NoTokenForDelete"}}},
		{"AssociateRouteTable", "UnsupportedOperation", url.Values{"RouteTableId": {id}}},
	} {
		_, bad := parseEC2NativeRouteTableMutation(tc.action, tc.fields)
		if bad == nil || bad.code != tc.code {
			t.Errorf("%s %v: want %s, got %v", tc.action, tc.fields, tc.code, bad)
		}
	}
}

func TestEC2NativeRouteTableMetadataRestartAndCleanup(t *testing.T) {
	ctx := t.Context()
	const region, other = "us-east-1", "ap-northeast-1"
	path := filepath.Join(t.TempDir(), "owner.sqlite")
	db, store := nativeRouteTableTestStore(t, path)
	parent := nativeRouteTableTestVPC(t, store, region, "10.23.0.0/16")
	otherParent := nativeRouteTableTestVPC(t, store, other, "10.23.0.0/16")
	stack, err := compileStack(region, "cfn-fenced", `{"Resources":{"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.24.0.0/16"}},"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"}}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(stack); err != nil {
		t.Fatal(err)
	}
	var stackVPC, stackRT string
	for _, item := range stack.Resources {
		switch item.Type {
		case ec2RT:
			stackRT = item.PhysicalID
		case ec2RTVPC:
			stackVPC = item.PhysicalID
		}
	}
	if stackVPC == "" || stackRT == "" {
		t.Fatalf("could not build CloudFormation-owned inputs: %+v", stack.Resources)
	}
	query := ec2NativeRouteTableQuery{vpc: parent, clientToken: "stable-fixture", tags: map[string]string{"Name": "独立 metadata"}}
	_, err = store.createNativeRouteTable(ctx, other, query)
	wantNativeRouteTableError(t, err, "InvalidVpcID.NotFound")
	refused := query
	refused.vpc = stackVPC
	_, err = store.createNativeRouteTable(ctx, region, refused)
	wantNativeRouteTableError(t, err, "UnsupportedOperation")
	wantNativeRouteTableError(t, store.deleteNativeRouteTable(ctx, region, stackRT), "UnsupportedOperation")

	reply, err := store.executeEC2NativeRouteTableMutation(ctx, "CreateRouteTable", region, query)
	if err != nil || reply.Table == nil || !ec2NativeRouteTableID.MatchString(reply.Table.ID) ||
		reply.Table.VPC != parent || !reflect.DeepEqual(reply.Table.Tags, []ec2Tag{{Key: "Name", Value: "独立 metadata"}}) ||
		reply.ClientToken != query.clientToken || !slices.Equal(reply.Table.Routes, []ec2RouteRead{nativeLocalRouteFixture("10.23.0.0/16")}) || len(reply.Table.Associations) != 0 {
		t.Fatalf("detached create: %+v / %v", reply, err)
	}
	id := reply.Table.ID
	wantNativeRouteTableError(t, store.deleteNativeRouteTable(ctx, other, id), "InvalidRouteTableID.NotFound")
	wantNativeRouteTableError(t, store.deleteNativeVPC(ctx, region, parent), "DependencyViolation")
	eastSecond, err := store.createNativeRouteTable(ctx, region, ec2NativeRouteTableQuery{vpc: parent})
	if err != nil || eastSecond.ID == id {
		t.Fatalf("explicitly untracked create: %+v / %v", eastSecond, err)
	}
	otherQuery := query
	otherQuery.vpc = otherParent
	otherTable, err := store.createNativeRouteTable(ctx, other, otherQuery)
	if err != nil || otherTable.ID == id {
		t.Fatalf("region-separated token: %+v / %v", otherTable, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, store = nativeRouteTableTestStore(t, path)
	rows, err := nativeRouteTableListForTest(t, store, region)
	if err != nil || len(rows) != 3 || rows[0].ID >= rows[1].ID || rows[1].ID >= rows[2].ID ||
		!slices.ContainsFunc(rows, func(row EC2NetworkResource) bool {
			return row.ID == nativeMainTableID(region, parent) && row.CIDR == "10.23.0.0/16"
		}) {
		t.Fatalf("same-region restart read: %+v / %v", rows, err)
	}
	replay, err := store.createNativeRouteTable(ctx, region, query)
	if err != nil || replay.ID != id {
		t.Fatalf("replay after restart: %+v / %v", replay, err)
	}
	changed := query
	changed.tags = map[string]string{"Name": "different"}
	_, err = store.createNativeRouteTable(ctx, region, changed)
	wantNativeRouteTableError(t, err, "IdempotentParameterMismatch")
	denied := query
	denied.authorizeCreatedID = func(exact string) error {
		if exact != id {
			t.Fatalf("wrong replay authorization target: %s", exact)
		}
		return &ec2Failure{"UnauthorizedOperation", "fixture denied the existing id", 403}
	}
	_, err = store.createNativeRouteTable(ctx, region, denied)
	wantNativeRouteTableError(t, err, "UnauthorizedOperation")

	// A legitimate later referencing operation must supply its own topology
	// contract. This storage witness only exercises the delete-side fence.
	f := ec2VPCFixture{t: t, store: db}
	const dependency = "rtbassoc-00000000000000001"
	f.writeNetwork(EC2NetworkResource{
		Region: region, ID: dependency, Kind: ec2Assoc, StackID: ec2NativeOwner,
		VPCID: parent, RouteTableID: id, Revision: 1,
	})
	wantNativeRouteTableError(t, store.deleteNativeRouteTable(ctx, region, id), "DependencyViolation")
	if _, err := db.Database().Exec(`DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, dependency); err != nil {
		t.Fatal(err)
	}
	removed, err := store.executeEC2NativeRouteTableMutation(ctx, "DeleteRouteTable", region, ec2NativeRouteTableQuery{id: id})
	if err != nil || removed.Return == nil || !*removed.Return {
		t.Fatalf("delete: %+v / %v", removed, err)
	}
	_, err = store.createNativeRouteTable(ctx, region, query)
	wantNativeRouteTableError(t, err, "InvalidRouteTableID.NotFound")
	rows, err = nativeRouteTableListForTest(t, store, region)
	if err != nil || len(rows) != 2 || !slices.ContainsFunc(rows, func(row EC2NetworkResource) bool { return row.ID == eastSecond.ID }) ||
		!slices.ContainsFunc(rows, func(row EC2NetworkResource) bool { return row.ID == nativeMainTableID(region, parent) }) {
		t.Fatalf("cleanup touched a sibling: %+v / %v", rows, err)
	}
	for _, existing := range []struct{ region, child, parent string }{{region, eastSecond.ID, parent}, {other, otherTable.ID, otherParent}} {
		if err := store.deleteNativeRouteTable(ctx, existing.region, existing.child); err != nil {
			t.Fatal(err)
		}
		if err := store.deleteNativeVPC(ctx, existing.region, existing.parent); err != nil {
			t.Fatal(err)
		}
	}
	cfn, err := store.describeEC2RouteTables(ctx, region)
	if err != nil || len(cfn) != 1 || cfn[0].ID != stackRT {
		t.Fatalf("native operations changed legacy table: %+v / %v", cfn, err)
	}
	if err := db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestEC2NativeRouteTableTwoConnectionReplayAndRollback(t *testing.T) {
	const region = "us-east-1"
	path := filepath.Join(t.TempDir(), "two-connections.sqlite")
	db, one := nativeRouteTableTestStore(t, path)
	_, two := nativeRouteTableTestStore(t, path)
	parent := nativeRouteTableTestVPC(t, one, region, "10.26.0.0/16")
	q := ec2NativeRouteTableQuery{vpc: parent, clientToken: "one-nonsecret-token"}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Database().Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER native_rtb_receipt_drop BEFORE INSERT ON aws_ec2_native_route_table_replay BEGIN SELECT RAISE(IGNORE); END`)
	if _, err := one.createNativeRouteTable(t.Context(), region, q); !errors.Is(err, errEC2NativeRouteTableState) {
		t.Fatalf("receipt-fault error: %v", err)
	}
	if rows, err := nativeRouteTableListForTest(t, one, region); err != nil || len(rows) != 1 || rows[0].ID != nativeMainTableID(region, parent) {
		t.Fatalf("failed receipt left a partial table: %+v / %v", rows, err)
	}
	exec(`DROP TRIGGER native_rtb_receipt_drop`)
	type result struct {
		r   EC2NetworkResource
		err error
	}
	reply := make(chan result, 2)
	for _, store := range []*StackStore{one, two} {
		go func(s *StackStore) {
			r, err := s.createNativeRouteTable(t.Context(), region, q)
			reply <- result{r, err}
		}(store)
	}
	a, b := <-reply, <-reply
	if a.err != nil || b.err != nil || a.r.ID == "" || a.r.ID != b.r.ID {
		t.Fatalf("one token did not converge across connections: %+v / %+v", a, b)
	}
	id := a.r.ID
	var count, tokenLength, requestLength int
	if err := db.Database().QueryRow(`SELECT count(*),length(token_hash),length(request_hash) FROM aws_ec2_native_route_table_replay`).Scan(&count, &tokenLength, &requestLength); err != nil ||
		count != 1 || tokenLength != 32 || requestLength != 32 {
		t.Fatalf("expected one digest-only receipt: %d %d %d / %v", count, tokenLength, requestLength, err)
	}
	exec(`CREATE TRIGGER native_rtb_delete_drop BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::RouteTable' BEGIN SELECT RAISE(IGNORE); END`)
	if err := one.deleteNativeRouteTable(t.Context(), region, id); !errors.Is(err, errEC2NativeRouteTableState) {
		t.Fatalf("delete fault was acknowledged: %v", err)
	}
	if rows, err := nativeRouteTableListForTest(t, one, region); err != nil || len(rows) != 2 ||
		!slices.ContainsFunc(rows, func(row EC2NetworkResource) bool { return row.ID == id }) ||
		!slices.ContainsFunc(rows, func(row EC2NetworkResource) bool { return row.ID == nativeMainTableID(region, parent) }) {
		t.Fatalf("failed delete corrupted saved state: %+v / %v", rows, err)
	}
	exec(`DROP TRIGGER native_rtb_delete_drop`)
	exec(`UPDATE aws_ec2_network_resources SET document=json_set(document,'$.RouteEnabled',json('true')) WHERE region=? AND id=?`, region, id)
	if rows, err := nativeRouteTableListForTest(t, one, region); !errors.Is(err, errEC2NativeRouteTableState) || rows != nil {
		t.Fatalf("a malformed detached table escaped readback: %+v / %v", rows, err)
	}
	exec(`UPDATE aws_ec2_network_resources SET document=json_set(document,'$.RouteEnabled',json('false')) WHERE region=? AND id=?`, region, id)
	if err := one.deleteNativeRouteTable(t.Context(), region, id); err != nil {
		t.Fatal(err)
	}
	tx, err := one.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := networkRead(t.Context(), tx, region, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted network row persisted: %v", err)
	}
}
