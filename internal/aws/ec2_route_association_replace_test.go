package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func replaceAssociationQuery(old, table string) url.Values {
	return url.Values{"Action": {ec2ReplaceNativeRouteTable}, "AssociationId": {old}, "RouteTableId": {table}}
}

func replaceTestAssociation(t *testing.T, s *StackStore, region, subnet, table string) EC2NetworkResource {
	t.Helper()
	r, err := s.associateNativeRouteTable(t.Context(), region, ec2NativeRouteAssociationQuery{table: table, subnet: subnet}, routeAssocAdminFixture)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func replaceTestTable(t *testing.T, s *StackStore, region, vpc string) EC2NetworkResource {
	t.Helper()
	r, err := s.createNativeRouteTable(t.Context(), region, ec2NativeRouteTableQuery{vpc: vpc})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func replaceTestInventory(t *testing.T, db *sql.DB) map[[2]string][4]string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT region,id,stack_id,kind,document,CAST(revision AS text) FROM aws_ec2_network_resources ORDER BY region,id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	image := make(map[[2]string][4]string)
	for rows.Next() {
		var key [2]string
		var fields [4]string
		if err := rows.Scan(&key[0], &key[1], &fields[0], &fields[1], &fields[2], &fields[3]); err != nil {
			t.Fatal(err)
		}
		image[key] = fields
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return image
}

func replaceTestReceipt(t *testing.T, body, old string) string {
	t.Helper()
	var r ec2NativeRouteAssociationResponse
	if err := xml.Unmarshal([]byte(body), &r); err != nil || r.XMLName.Local != ec2ReplaceNativeRouteTable+"Response" ||
		r.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || r.RequestID == "" ||
		r.AssociationID != "" || r.Return != nil || r.AssociationState == nil || r.AssociationState.State != "associated" ||
		!ec2NativeRouteAssociationID.MatchString(r.NewAssociationID) || r.NewAssociationID == old {
		t.Fatalf("invalid replacement receipt: %v / %s", err, body)
	}
	return r.NewAssociationID
}

func TestEC2NativeRouteAssociationReplacementExactAuthorityAndReadback(t *testing.T) {
	const region, other = routeAssocTestRegion, "ap-northeast-1"
	f := newEC2VPCFixture(t)
	s := f.stacks
	vpc, q := routeAssocFixture(t, s, region, "10.90.0.0/16", "10.90.1.0/24")
	target := replaceTestTable(t, s, region, vpc)
	old := replaceTestAssociation(t, s, region, q.subnet, q.table)
	want := replaceAssociationQuery(old.ID, target.ID)
	db := f.store.Database()
	const missing = "rtb-00000000000000000"
	_, foreign := routeAssocFixture(t, s, region, "10.91.0.0/16", "10.91.1.0/24")
	stack, err := compileStack(region, "legacy-replace-fixture", `{"Resources":{
	  "V":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.92.0.0/16"}},
	  "S":{"Type":"AWS::EC2::Subnet","Properties":{"CidrBlock":"10.92.1.0/24","VpcId":{"Ref":"V"}}},
	  "T":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"V"}}},
	  "A":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"SubnetId":{"Ref":"S"},"RouteTableId":{"Ref":"T"}}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(stack); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, r := range stack.Resources {
		ids[r.LogicalID] = r.PhysicalID
	}
	grants := []string{networkARN(region, "route-table", q.table), networkARN(region, "subnet", q.subnet),
		networkARN(region, "route-table", target.ID)}
	policy := func(arns []string) {
		t.Helper()
		authorized, _ := json.Marshal(arns)
		doc, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
			{"Effect":"Allow","Action":%q,"Resource":%s},{"Effect":"Allow","Action":"ec2:DescribeRouteTables","Resource":"*"}]}`, replaceNativeAssociationIAM, authorized)))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set("association-replacer", doc); err != nil {
			t.Fatal(err)
		}
	}
	before := replaceTestInventory(t, db)
	refuse := func(principal, region string, ctx context.Context, values url.Values, status int, code string) {
		t.Helper()
		body := f.expect(f.call(principal, region, ctx, values), status, code)
		routeAssocResilienceNoSuccess(t, body)
		if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatalf("%s changed the durable network inventory", code)
		}
	}
	for _, omit := range grants {
		policy(slices.DeleteFunc(slices.Clone(grants), func(arn string) bool { return arn == omit }))
		refuse("association-replacer", region, nil, want, 403, "UnauthorizedOperation")
		dry := replaceAssociationQuery(old.ID, target.ID)
		dry.Set("DryRun", "true")
		refuse("association-replacer", region, nil, dry, 403, "UnauthorizedOperation")
	}
	policy(append(slices.Clone(grants), networkARN(region, "route-table", missing)))
	dry := replaceAssociationQuery(old.ID, missing)
	dry.Set("DryRun", "true")
	refuse("association-replacer", region, nil, dry, 400, "DryRunOperation")
	refuse("association-replacer", region, nil, replaceAssociationQuery(old.ID, missing), 400, "InvalidRouteTableID.NotFound")
	scope := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	refuse("association-replacer", region, scope, want, 403, "UnauthorizedOperation")
	refuse("association-replacer", other, nil, want, 403, "UnauthorizedOperation")
	refuse("", other, nil, want, 400, "InvalidAssociationID.NotFound")
	refuse("", region, nil, replaceAssociationQuery(old.ID, q.table), 400, "InvalidParameterValue")
	refuse("", region, nil, replaceAssociationQuery(old.ID, foreign.table), 400, "InvalidParameterValue")
	refuse("", region, nil, replaceAssociationQuery(old.ID, ids["T"]), 400, "UnsupportedOperation")
	refuse("", region, nil, replaceAssociationQuery(ids["A"], target.ID), 400, "InvalidAssociationID.NotFound")
	refuse("association-replacer", region, nil, replaceAssociationQuery(ids["A"], target.ID), 403, "UnauthorizedOperation")
	newID := replaceTestReceipt(t, f.expect(f.call("association-replacer", region, nil, want), 200, ""), old.ID)
	after := replaceTestInventory(t, db)
	delete(before, [2]string{region, old.ID})
	next, exists := after[[2]string{region, newID}]
	delete(after, [2]string{region, newID})
	if !exists || next[0] != ec2NativeOwner || next[1] != ec2Assoc || next[3] != "1" || !reflect.DeepEqual(before, after) {
		t.Fatal("replacement did not keep every other network row unchanged")
	}
	for _, table := range []string{q.table, target.ID} {
		query := url.Values{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {table}}
		body := f.expect(f.call("association-replacer", region, nil, query), 200, "")
		var reply ec2DescribeRouteTablesResponse
		if err := xml.Unmarshal([]byte(body), &reply); err != nil || len(reply.Tables) != 1 || reply.Tables[0].ID != table {
			t.Fatalf("bad canonical readback: %v / %s", err, body)
		}
		got := reply.Tables[0].Associations
		if table == q.table && len(got) != 0 || table == target.ID &&
			(len(got) != 1 || got[0].ID != newID || got[0].Subnet != q.subnet || got[0].Main) {
			t.Fatalf("replacement not reflected in the same read table: %+v", reply)
		}
	}
	body := f.expect(f.call("", region, nil, routeAssocResilienceRemove(old.ID)), 400, "InvalidAssociationID.NotFound")
	routeAssocResilienceNoSuccess(t, body)
	f.expect(f.call("", region, nil, routeAssocResilienceRemove(newID)), 200, "")
}

func TestEC2NativeRouteAssociationReplacementRollsBackBothSides(t *testing.T) {
	const region = routeAssocTestRegion
	f := newEC2VPCFixture(t)
	vpc, q := routeAssocFixture(t, f.stacks, region, "10.93.0.0/16", "10.93.1.0/24")
	target := replaceTestTable(t, f.stacks, region, vpc)
	old := replaceTestAssociation(t, f.stacks, region, q.subnet, q.table)
	query := replaceAssociationQuery(old.ID, target.ID)
	db := f.store.Database()
	before := replaceTestInventory(t, db)
	for _, stmt := range []string{
		`CREATE TRIGGER replacement_fault BEFORE DELETE ON aws_ec2_network_resources
		WHEN OLD.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN SELECT RAISE(IGNORE); END`,
		`CREATE TRIGGER replacement_fault BEFORE INSERT ON aws_ec2_network_resources
		WHEN NEW.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN SELECT RAISE(ABORT,'local rollback fixture'); END`,
		`CREATE TRIGGER replacement_fault AFTER INSERT ON aws_ec2_network_resources
		WHEN NEW.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN
		  UPDATE aws_ec2_network_resources SET document=document||' ' WHERE region=NEW.region AND id=NEW.id;
		END`,
		`CREATE TRIGGER replacement_fault AFTER DELETE ON aws_ec2_network_resources
		WHEN OLD.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN
		  INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision);
		END`,
		`CREATE TRIGGER replacement_fault AFTER INSERT ON aws_ec2_network_resources
		WHEN NEW.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN
		  UPDATE aws_ec2_network_resources SET document=document||' ' WHERE region=NEW.region AND id=json_extract(NEW.document,'$.VPCID');
		END`,
	} {
		routeAssocResilienceExec(t, db, stmt)
		body := f.expect(f.call("", region, nil, query), 500, "InternalError")
		routeAssocResilienceNoSuccess(t, body)
		if strings.Contains(body, "rollback fixture") || !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatalf("failure exposed diagnostics or committed a partial mutation: %s", body)
		}
		routeAssocResilienceExec(t, db, "DROP TRIGGER replacement_fault")
	}
	// A saved CFN declaration without its corresponding claim must still
	// fail closed. An unrelated type with the same legal name does not own EC2.
	stack, err := compileStack(region, "replacement-declaration-only", `{"Resources":{
	  "Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.96.0.0/16"}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	stack.Resources = append(stack.Resources, StackResource{LogicalID: "RetainedTable", Type: ec2RT, PhysicalID: target.ID})
	raw, _ := json.Marshal(stack)
	routeAssocResilienceExec(t, db, "INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)", region, stack.Name, stack.ID, string(raw))
	routeAssocResilienceNoSuccess(t, f.expect(f.call("", region, nil, query), 500, "InternalError"))
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("surviving declaration allowed an association mutation")
	}
	stack.Resources[1].Type = "AWS::S3::Bucket"
	raw, _ = json.Marshal(stack)
	routeAssocResilienceExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE region=? AND id=?", string(raw), region, stack.ID)
	id := replaceTestReceipt(t, f.expect(f.call("", region, nil, query), 200, ""), old.ID)
	f.expect(f.call("", region, nil, routeAssocResilienceRemove(id)), 200, "")
}

func TestEC2NativeRouteAssociationReplacementConcurrentStoreAndRestart(t *testing.T) {
	const region = routeAssocTestRegion
	path := filepath.Join(t.TempDir(), "replace-association.db")
	db1, s1 := nativeRouteTableTestStore(t, path)
	db2, s2 := nativeRouteTableTestStore(t, path)
	vpc, q := routeAssocFixture(t, s1, region, "10.94.0.0/16", "10.94.1.0/24")
	sourceSibling, err := s1.createNativeSubnet(t.Context(), region, ec2SubnetMutation{vpc: vpc, cidr: "10.94.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	unchanged := replaceTestAssociation(t, s1, region, sourceSibling.ID, q.table)
	old := replaceTestAssociation(t, s1, region, q.subnet, q.table)
	first, second := replaceTestTable(t, s1, region, vpc), replaceTestTable(t, s1, region, vpc)
	type result struct {
		row EC2NetworkResource
		err error
	}
	ready, finished := make(chan struct{}), make(chan result, 2)
	for i, store := range []*StackStore{s1, s2} {
		table := []string{first.ID, second.ID}[i]
		go func() {
			<-ready
			row, err := store.replaceNativeRouteTableAssociation(t.Context(), region,
				ec2NativeRouteAssociationQuery{table: table, association: old.ID}, routeAssocAdminFixture)
			finished <- result{row, err}
		}()
	}
	close(ready)
	a, b := <-finished, <-finished
	if a.err != nil {
		a, b = b, a
	}
	if a.err != nil || a.row.ID == old.ID || !ec2NativeRouteAssociationID.MatchString(a.row.ID) || a.row.SubnetID != q.subnet {
		t.Fatalf("no exact replacement survived the race: %+v / %+v", a, b)
	}
	wantNativeRouteTableError(t, b.err, "InvalidAssociationID.NotFound")
	for _, db := range []*core.Store{db1, db2} {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	_, reopened := nativeRouteTableTestStore(t, path)
	routeAssocExpectProjection(t, reopened, region, map[string][]ec2RouteAssociationRead{
		q.table:            {{ID: unchanged.ID, Table: q.table, Subnet: sourceSibling.ID}},
		a.row.RouteTableID: {{ID: a.row.ID, Table: a.row.RouteTableID, Subnet: q.subnet}},
	})
	wantNativeRouteTableError(t, reopened.disassociateNativeRouteTable(t.Context(), region, old.ID, routeAssocAdminFixture), "InvalidAssociationID.NotFound")
	if err := reopened.disassociateNativeRouteTable(t.Context(), region, a.row.ID, routeAssocAdminFixture); err != nil {
		t.Fatal(err)
	}
}

func TestEC2NativeRouteAssociationReplacementReusesOneFullQuotaSlot(t *testing.T) {
	const east, west = routeAssocTestRegion, "us-west-2"
	store, s := nativeRouteTableTestStore(t, ":memory:")
	ctx := t.Context()
	vpc, q := routeAssocFixture(t, s, east, "10.88.0.0/16", "10.88.1.0/24")
	target := replaceTestTable(t, s, east, vpc)
	old := replaceTestAssociation(t, s, east, q.subnet, q.table)
	westVPC := nativeRouteTableTestVPC(t, s, west, "10.89.0.0/16")
	tx, err := store.Database().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_ec2_network_resources").Scan(&before); err != nil {
		t.Fatal(err)
	}
	// The other region contains valid detached EC2 route tables. Seed the
	// ordinary cap efficiently instead of issuing 994 API requests.
	for n := range ec2NativeRouteTableRowLimit - before {
		row := EC2NetworkResource{Region: west, ID: fmt.Sprintf("rtb-%017x", n+1), Kind: ec2RT,
			StackID: ec2NativeOwner, Revision: 1, VPCID: westVPC}
		if err := networkWrite(ctx, tx, row, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = s.createNativeRouteTable(ctx, east, ec2NativeRouteTableQuery{vpc: vpc})
	wantNativeRouteTableError(t, err, "RouteTableLimitExceeded")
	oldImage := replaceTestInventory(t, store.Database())
	newRow, err := s.replaceNativeRouteTableAssociation(ctx, east, ec2NativeRouteAssociationQuery{
		table: target.ID, association: old.ID}, routeAssocAdminFixture)
	if err != nil || newRow.ID == old.ID || newRow.RouteTableID != target.ID || newRow.SubnetID != q.subnet {
		t.Fatalf("replacement did not reuse incumbent slot: %+v / %v", newRow, err)
	}
	gotImage := replaceTestInventory(t, store.Database())
	if len(oldImage) != ec2NativeRouteTableRowLimit || len(gotImage) != len(oldImage) {
		t.Fatal("resource count changed during a full quota replacement")
	}
	delete(oldImage, [2]string{east, old.ID})
	delete(gotImage, [2]string{east, newRow.ID})
	if !reflect.DeepEqual(oldImage, gotImage) {
		t.Fatal("unrelated regional state changed")
	}
}
