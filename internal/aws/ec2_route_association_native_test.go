package aws

import (
	"encoding/xml"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const routeAssocTestRegion = "us-east-1"

var routeAssocAdminFixture = ec2NativeRouteAssociationAuthority{
	check: func(_, _ string) bool { return true }, revealMissingID: true,
}

func TestEC2NativeRouteAssociationQuery(t *testing.T) {
	const table, subnet, assoc = "rtb-0123456789abcdef0", "subnet-0123456789abcdef0", "rtbassoc-0123456789abcdef0"
	for _, tc := range []struct {
		name, action, want string
		form               url.Values
	}{
		{"associate", ec2AssociateNativeRouteTable, "", url.Values{"RouteTableId": {table}, "SubnetId": {subnet}, "DryRun": {"false"}}},
		{"disassociate", ec2DisassociateNativeRouteTable, "", url.Values{"AssociationId": {assoc}}},
		{"missing subnet", ec2AssociateNativeRouteTable, "MissingParameter", url.Values{"RouteTableId": {table}}},
		{"missing association", ec2DisassociateNativeRouteTable, "MissingParameter", nil},
		{"invalid table", ec2AssociateNativeRouteTable, "InvalidRouteTableID.Malformed", url.Values{"RouteTableId": {subnet}, "SubnetId": {subnet}}},
		{"invalid subnet", ec2AssociateNativeRouteTable, "InvalidParameterValue", url.Values{"RouteTableId": {table}, "SubnetId": {assoc}}},
		{"invalid association", ec2DisassociateNativeRouteTable, "InvalidParameterValue", url.Values{"AssociationId": {subnet}}},
		{"duplicate", ec2DisassociateNativeRouteTable, "InvalidParameterValue", url.Values{"AssociationId": {assoc, assoc}}},
		{"replace", ec2ReplaceNativeRouteTable, "", url.Values{"AssociationId": {assoc}, "RouteTableId": {table}}},
		{"permission-only dry run", ec2ReplaceNativeRouteTable, "", url.Values{"AssociationId": {assoc}, "RouteTableId": {table}, "DryRun": {"true"}}},
		{"invalid dry run", ec2ReplaceNativeRouteTable, "InvalidParameterValue", url.Values{"AssociationId": {assoc}, "RouteTableId": {table}, "DryRun": {"True"}}},
		{"missing replacement table", ec2ReplaceNativeRouteTable, "MissingParameter", url.Values{"AssociationId": {assoc}}},
		{"invalid replacement table", ec2ReplaceNativeRouteTable, "InvalidRouteTableID.Malformed", url.Values{"AssociationId": {assoc}, "RouteTableId": {subnet}}},
		{"invalid replacement association", ec2ReplaceNativeRouteTable, "InvalidParameterValue", url.Values{"AssociationId": {subnet}, "RouteTableId": {table}}},
		{"replace rejects subnet overrides", ec2ReplaceNativeRouteTable, "UnsupportedOperation", url.Values{"AssociationId": {assoc}, "RouteTableId": {table}, "SubnetId": {subnet}}},
		{"replace rejects gateway requests", ec2ReplaceNativeRouteTable, "UnsupportedOperation", url.Values{"AssociationId": {assoc}, "RouteTableId": {table}, "GatewayId": {"igw-0123456789abcdef0"}}},
		{"no gateway", ec2AssociateNativeRouteTable, "UnsupportedOperation", url.Values{"RouteTableId": {table}, "GatewayId": {"igw-0123456789abcdef0"}}},
		{"no client token", ec2AssociateNativeRouteTable, "UnsupportedOperation", url.Values{"RouteTableId": {table}, "SubnetId": {subnet}, "ClientToken": {"fixture"}}},
		{"no dry run fiction", ec2DisassociateNativeRouteTable, "UnsupportedOperation", url.Values{"AssociationId": {assoc}, "DryRun": {"true"}}},
		{"reject wrong members", ec2DisassociateNativeRouteTable, "UnsupportedOperation", url.Values{"AssociationId": {assoc}, "RouteTableId": {table}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := parseEC2NativeRouteAssociation(tc.action, tc.form)
			if tc.want != "" {
				wantNativeRouteTableError(t, err, tc.want)
			} else if err != nil || q.table != tc.form.Get("RouteTableId") || q.subnet != tc.form.Get("SubnetId") || q.association != tc.form.Get("AssociationId") || q.dryRun != (tc.form.Get("DryRun") == "true") {
				t.Fatalf("wrong typed query: %+v %v", q, err)
			}
		})
	}
}

func routeAssocFixture(t *testing.T, s *StackStore, region, parent, child string) (string, ec2NativeRouteAssociationQuery) {
	t.Helper()
	vpc := nativeRouteTableTestVPC(t, s, region, parent)
	n, err := s.createNativeSubnet(t.Context(), region, ec2SubnetMutation{vpc: vpc, cidr: child})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := s.createNativeRouteTable(t.Context(), region, ec2NativeRouteTableQuery{vpc: vpc})
	if err != nil {
		t.Fatal(err)
	}
	return vpc, ec2NativeRouteAssociationQuery{table: rt.ID, subnet: n.ID}
}

func routeAssocSnapshot(t *testing.T, s *StackStore, region string) (map[string][]ec2RouteAssociationRead, int, error) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return nativeRouteAssociationProjection(t.Context(), tx, region)
}

func routeAssocExpectProjection(t *testing.T, s *StackStore, region string, want map[string][]ec2RouteAssociationRead) {
	t.Helper()
	got, n, err := routeAssocSnapshot(t, s, region)
	count := 0
	for _, rows := range want {
		count += len(rows)
	}
	if err != nil || n != count || !reflect.DeepEqual(got, want) {
		t.Fatalf("count=%d snapshot=%+v err=%v; want=%+v", n, got, err, want)
	}
}

func TestEC2NativeRouteAssociationLifecycleAndCleanup(t *testing.T) {
	const east, west = routeAssocTestRegion, "us-west-2"
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "associations.db")
	db, s := nativeRouteTableTestStore(t, path)
	parent, q := routeAssocFixture(t, s, east, "10.77.0.0/16", "10.77.1.0/24")
	_, other := routeAssocFixture(t, s, east, "10.78.0.0/16", "10.78.1.0/24")
	_, peer := routeAssocFixture(t, s, west, "10.77.0.0/16", "10.77.1.0/24")
	alternate, err := s.createNativeRouteTable(ctx, east, ec2NativeRouteTableQuery{vpc: parent})
	if err != nil {
		t.Fatal(err)
	}
	a := ec2NativeRouteAssociationAuthority{check: func(action, arn string) bool {
		return (action == "ec2:AssociateRouteTable" || action == "ec2:DisassociateRouteTable") &&
			(arn == networkARN(east, "route-table", q.table) || arn == networkARN(east, "subnet", q.subnet))
	}}
	result, err := s.executeEC2NativeRouteAssociation(ctx, ec2AssociateNativeRouteTable, east, q, a)
	if err != nil || result == nil || !ec2NativeRouteAssociationID.MatchString(result.AssociationID) ||
		result.AssociationState == nil || result.AssociationState.State != "associated" || result.Return != nil {
		t.Fatalf("wrong persisted association acknowledgment: %+v %v", result, err)
	}
	xmlBytes, err := xml.Marshal(result)
	if err != nil || !strings.Contains(string(xmlBytes), "<associationState><state>associated</state></associationState>") {
		t.Fatalf("wrong association response fields: %s %v", xmlBytes, err)
	}
	id := result.AssociationID
	westResult, err := s.associateNativeRouteTable(ctx, west, peer, routeAssocAdminFixture)
	if err != nil {
		t.Fatal(err)
	}
	eastWant := map[string][]ec2RouteAssociationRead{q.table: {{ID: id, Table: q.table, Subnet: q.subnet}}}
	westWant := map[string][]ec2RouteAssociationRead{peer.table: {{ID: westResult.ID, Table: peer.table, Subnet: peer.subnet}}}
	routeAssocExpectProjection(t, s, east, eastWant)
	routeAssocExpectProjection(t, s, west, westWant)
	_, err = s.associateNativeRouteTable(ctx, east, ec2NativeRouteAssociationQuery{table: alternate.ID, subnet: q.subnet}, routeAssocAdminFixture)
	wantNativeRouteTableError(t, err, "Resource.AlreadyAssociated")
	_, err = s.associateNativeRouteTable(ctx, east, ec2NativeRouteAssociationQuery{table: other.table, subnet: q.subnet}, routeAssocAdminFixture)
	wantNativeRouteTableError(t, err, "InvalidParameterValue")
	_, err = s.associateNativeRouteTable(ctx, west, q, routeAssocAdminFixture)
	wantNativeRouteTableError(t, err, "InvalidRouteTableID.NotFound")
	wantNativeRouteTableError(t, s.deleteNativeRouteTable(ctx, east, q.table), "DependencyViolation")
	wantNativeRouteTableError(t, s.deleteNativeSubnet(ctx, east, q.subnet), "DependencyViolation")
	wantNativeRouteTableError(t, s.deleteNativeVPC(ctx, east, parent), "DependencyViolation")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = nativeRouteTableTestStore(t, path)
	r, err := s.associateNativeRouteTable(ctx, east, q, a)
	if err != nil || r.ID != id {
		t.Fatalf("same exact pair must preserve its ID across store reopen: %+v %v", r, err)
	}
	removed, err := s.executeEC2NativeRouteAssociation(ctx, ec2DisassociateNativeRouteTable, east, ec2NativeRouteAssociationQuery{association: id}, a)
	if err != nil || removed == nil || removed.Return == nil || !*removed.Return || removed.AssociationState != nil {
		t.Fatalf("wrong explicit removal receipt: %+v %v", removed, err)
	}
	routeAssocExpectProjection(t, s, east, map[string][]ec2RouteAssociationRead{})
	routeAssocExpectProjection(t, s, west, westWant)
	wantNativeRouteTableError(t, s.disassociateNativeRouteTable(ctx, east, id, a), "UnauthorizedOperation")
	wantNativeRouteTableError(t, s.disassociateNativeRouteTable(ctx, east, id, routeAssocAdminFixture), "InvalidAssociationID.NotFound")
	fresh, err := s.associateNativeRouteTable(ctx, east, q, a)
	if err != nil || fresh.ID == id {
		t.Fatalf("a new binding after deletion is not the retired receipt: %+v %v", fresh, err)
	}
	if err := s.disassociateNativeRouteTable(ctx, east, fresh.ID, a); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{s.deleteNativeRouteTable(ctx, east, q.table), s.deleteNativeSubnet(ctx, east, q.subnet),
		s.deleteNativeRouteTable(ctx, east, alternate.ID), s.deleteNativeVPC(ctx, east, parent)} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestEC2NativeRouteAssociationDenialsAndLegacy(t *testing.T) {
	const region = routeAssocTestRegion
	ctx := t.Context()
	_, s := nativeRouteTableTestStore(t, ":memory:")
	_, q := routeAssocFixture(t, s, region, "10.77.0.0/16", "10.77.1.0/24")
	legacy, err := compileStack(region, "route-association-legacy", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.83.0.0/16"}},
		"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.83.1.0/24"}},
		"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"}}},
		"Association":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"Table"},"SubnetId":{"Ref":"Subnet"}}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(legacy); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, x := range legacy.Resources {
		ids[x.LogicalID] = x.PhysicalID
	}
	legacyQ := ec2NativeRouteAssociationQuery{table: ids["Table"], subnet: ids["Subnet"]}
	_, err = s.associateNativeRouteTable(ctx, region, legacyQ, routeAssocAdminFixture)
	wantNativeRouteTableError(t, err, "UnsupportedOperation")
	_, err = s.associateNativeRouteTable(ctx, region, ec2NativeRouteAssociationQuery{table: q.table, subnet: ids["Subnet"]}, routeAssocAdminFixture)
	wantNativeRouteTableError(t, err, "UnsupportedOperation")
	wantNativeRouteTableError(t, s.disassociateNativeRouteTable(ctx, region, ids["Association"], routeAssocAdminFixture), "InvalidAssociationID.NotFound")
	onlyTable := ec2NativeRouteAssociationAuthority{check: func(action, resource string) bool {
		return strings.HasSuffix(action, "AssociateRouteTable") && resource == networkARN(region, "route-table", q.table)
	}}
	_, err = s.associateNativeRouteTable(ctx, region, q, onlyTable)
	wantNativeRouteTableError(t, err, "UnauthorizedOperation")
	_, err = s.associateNativeRouteTable(ctx, region, q, ec2NativeRouteAssociationAuthority{revealMissingID: true})
	wantNativeRouteTableError(t, err, "UnauthorizedOperation")
	routeAssocExpectProjection(t, s, region, map[string][]ec2RouteAssociationRead{})
	r, err := s.associateNativeRouteTable(ctx, region, q, routeAssocAdminFixture)
	if err != nil {
		t.Fatal(err)
	}
	missing := "rtbassoc-00000000000000000"
	for _, id := range []string{r.ID, ids["Association"], missing} {
		err := s.disassociateNativeRouteTable(ctx, region, id, onlyTable)
		wantNativeRouteTableError(t, err, "UnauthorizedOperation")
		if !reflect.DeepEqual(err, deniedNativeRouteAssociation()) {
			t.Fatalf("inconsistent negative result: %v", err)
		}
	}
	routeAssocExpectProjection(t, s, region, map[string][]ec2RouteAssociationRead{
		q.table: {{ID: r.ID, Table: q.table, Subnet: q.subnet}},
	})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	old, err := networkRead(ctx, tx, region, ids["Association"])
	if err != nil || old.StackID != legacy.ID || old.SubnetID != ids["Subnet"] || old.RouteTableID != ids["Table"] {
		t.Fatalf("legacy relationship changed: %+v %v", old, err)
	}
}

func TestEC2NativeRouteAssociationConcurrentAndRollback(t *testing.T) {
	const region = routeAssocTestRegion
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "concurrent-assoc.db")
	_, first := nativeRouteTableTestStore(t, path)
	_, second := nativeRouteTableTestStore(t, path)
	v, q := routeAssocFixture(t, first, region, "10.77.0.0/16", "10.77.1.0/24")
	other, err := first.createNativeRouteTable(ctx, region, ec2NativeRouteTableQuery{vpc: v})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		row EC2NetworkResource
		err error
	}
	ready := make(chan struct{})
	done := make(chan result, 2)
	for i, x := range []*StackStore{first, second} {
		target := q
		if i == 1 {
			target.table = other.ID
		}
		go func(db *StackStore, q ec2NativeRouteAssociationQuery) {
			<-ready
			r, err := db.associateNativeRouteTable(ctx, region, q, routeAssocAdminFixture)
			done <- result{r, err}
		}(x, target)
	}
	close(ready)
	a, b := <-done, <-done
	if a.err != nil {
		a, b = b, a
	}
	if a.err != nil || a.row.ID == "" {
		t.Fatalf("neither exact candidate committed: %+v %+v", a, b)
	}
	wantNativeRouteTableError(t, b.err, "Resource.AlreadyAssociated")
	want := map[string][]ec2RouteAssociationRead{a.row.RouteTableID: {{ID: a.row.ID, Table: a.row.RouteTableID, Subnet: q.subnet}}}
	routeAssocExpectProjection(t, second, region, want)
	if _, err := first.db.ExecContext(ctx, `CREATE TRIGGER suppress_native_assoc_delete
		BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::SubnetRouteTableAssociation'
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := first.disassociateNativeRouteTable(ctx, region, a.row.ID, routeAssocAdminFixture); !errors.Is(err, errEC2NativeRouteAssociationState) {
		t.Fatalf("a suppressed delete must not be reported as success: %v", err)
	}
	routeAssocExpectProjection(t, first, region, want)
	if _, err := first.db.ExecContext(ctx, `DROP TRIGGER suppress_native_assoc_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.VPCID','vpc-00000000000000000') WHERE region=? AND id=?`, region, a.row.ID); err != nil {
		t.Fatal(err)
	}
	if got, n, err := routeAssocSnapshot(t, first, region); got != nil || n != 0 || !errors.Is(err, errEC2NativeRouteAssociationState) {
		t.Fatalf("inconsistent saved scope must fail closed, not return an empty success: %v %d %v", got, n, err)
	}
}
