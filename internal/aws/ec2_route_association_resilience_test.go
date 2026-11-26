package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func routeAssocResilienceExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("prepare association storage fixture: %v", err)
	}
}

func routeAssocResilienceAssociate(q ec2NativeRouteAssociationQuery) url.Values {
	return url.Values{"Action": {ec2AssociateNativeRouteTable}, "RouteTableId": {q.table}, "SubnetId": {q.subnet}}
}

func routeAssocResilienceRemove(id string) url.Values {
	return url.Values{"Action": {ec2DisassociateNativeRouteTable}, "AssociationId": {id}}
}

func routeAssocResilienceNoSuccess(t *testing.T, body string) {
	t.Helper()
	for _, marker := range []string{"<associationId>", "<associationState>", "<return>", "<routeTableSet>", "<vpcSet>"} {
		if strings.Contains(body, marker) {
			t.Fatalf("failed EC2 request returned a partial success: %s", body)
		}
	}
}

// An unavailable or broken subnet witness must never persist an association.
// Restore the same row, then demonstrate that the same native pair is usable.
func TestEC2NativeRouteAssociationRejectsBrokenSubnetWitness(t *testing.T) {
	const region = routeAssocTestRegion
	f := newEC2VPCFixture(t)
	_, q := routeAssocFixture(t, f.stacks, region, "10.100.0.0/16", "10.100.1.0/24")
	db := f.store.Database()
	empty := map[string][]ec2RouteAssociationRead{}
	call := func(principal string, q ec2NativeRouteAssociationQuery, status int, code string) {
		routeAssocResilienceNoSuccess(t, f.expect(f.call(principal, region, nil, routeAssocResilienceAssociate(q)), status, code))
		routeAssocExpectProjection(t, f.stacks, region, empty)
	}
	call("unregistered-association-reader", q, 403, "UnauthorizedOperation")
	missing := q
	missing.subnet = "subnet-00000000"
	call("", missing, 400, "InvalidSubnetID.NotFound")

	var original string
	if err := db.QueryRowContext(t.Context(), "SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?", region, q.subnet).Scan(&original); err != nil {
		t.Fatal(err)
	}
	setDocument := func(document string) {
		routeAssocResilienceExec(t, db, "UPDATE aws_ec2_network_resources SET document=? WHERE region=? AND id=?", document, region, q.subnet)
	}
	setDocument("{}")
	call("", q, 500, "InternalError")
	routeAssocResilienceExec(t, db, "UPDATE aws_ec2_network_resources SET document=json_set(?,'$.CIDR','10.100.1.5/24') WHERE region=? AND id=?", original, region, q.subnet)
	call("", q, 500, "InternalError")
	setDocument(original)
	body := f.expect(f.call("", region, nil, routeAssocResilienceAssociate(q)), 200, "")
	var out ec2NativeRouteAssociationResponse
	if err := xml.Unmarshal([]byte(body), &out); err != nil || !ec2NativeRouteAssociationID.MatchString(out.AssociationID) {
		t.Fatalf("restored native pair was not associated: %s / %v", body, err)
	}
	routeAssocExpectProjection(t, f.stacks, region, map[string][]ec2RouteAssociationRead{
		q.table: {{ID: out.AssociationID, Table: q.table, Subnet: q.subnet}},
	})
	f.expect(f.call("", region, nil, routeAssocResilienceRemove(out.AssociationID)), 200, "")
	routeAssocExpectProjection(t, f.stacks, region, empty)
}

// Faults here are owned SQLite test triggers. Use the real Query handler and
// persistence path; do not accept a fake success or a partial committed write.
func TestEC2NativeRouteAssociationRollsBackUnconfirmedWrites(t *testing.T) {
	const region = routeAssocTestRegion
	f := newEC2VPCFixture(t)
	_, q := routeAssocFixture(t, f.stacks, region, "10.101.0.0/16", "10.101.1.0/24")
	db := f.store.Database()
	empty := map[string][]ec2RouteAssociationRead{}
	for _, trigger := range []string{
		`CREATE TRIGGER association_fault BEFORE INSERT ON aws_ec2_network_resources
			WHEN NEW.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN SELECT RAISE(ABORT,'fixture rejects insert'); END`,
		`CREATE TRIGGER association_fault AFTER INSERT ON aws_ec2_network_resources
			WHEN NEW.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN
			UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Description','fixture readback mismatch')
				WHERE region=NEW.region AND id=NEW.id; END`,
	} {
		routeAssocResilienceExec(t, db, trigger)
		body := f.expect(f.call("", region, nil, routeAssocResilienceAssociate(q)), 500, "InternalError")
		routeAssocResilienceNoSuccess(t, body)
		if strings.Contains(body, "fixture") {
			t.Fatalf("response exposed a storage diagnostic: %s", body)
		}
		routeAssocExpectProjection(t, f.stacks, region, empty)
		routeAssocResilienceExec(t, db, "DROP TRIGGER association_fault")
	}

	body := f.expect(f.call("", region, nil, routeAssocResilienceAssociate(q)), 200, "")
	var a ec2NativeRouteAssociationResponse
	if err := xml.Unmarshal([]byte(body), &a); err != nil || !ec2NativeRouteAssociationID.MatchString(a.AssociationID) {
		t.Fatalf("initial native association missing: %s / %v", body, err)
	}
	want := map[string][]ec2RouteAssociationRead{q.table: {{ID: a.AssociationID, Table: q.table, Subnet: q.subnet}}}
	routeAssocExpectProjection(t, f.stacks, region, want)
	for _, trigger := range []string{
		`CREATE TRIGGER association_fault BEFORE DELETE ON aws_ec2_network_resources
			WHEN OLD.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN SELECT RAISE(ABORT,'fixture rejects delete'); END`,
		`CREATE TRIGGER association_fault AFTER DELETE ON aws_ec2_network_resources
			WHEN OLD.kind='AWS::EC2::SubnetRouteTableAssociation' BEGIN
			INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision); END`,
	} {
		routeAssocResilienceExec(t, db, trigger)
		body := f.expect(f.call("", region, nil, routeAssocResilienceRemove(a.AssociationID)), 500, "InternalError")
		routeAssocResilienceNoSuccess(t, body)
		if strings.Contains(body, "fixture") {
			t.Fatalf("response exposed a storage diagnostic: %s", body)
		}
		routeAssocExpectProjection(t, f.stacks, region, want)
		routeAssocResilienceExec(t, db, "DROP TRIGGER association_fault")
	}
	f.expect(f.call("", region, nil, routeAssocResilienceRemove(a.AssociationID)), 200, "")
	routeAssocExpectProjection(t, f.stacks, region, empty)
}

// A malformed or duplicate native row in one's region invalidates the whole
// association snapshot, while an unrelated region remains independently readable.
func TestEC2NativeRouteAssociationRejectsInconsistentRegionalInventory(t *testing.T) {
	const region, peerRegion = routeAssocTestRegion, "ap-northeast-1"
	f := newEC2VPCFixture(t)
	_, q := routeAssocFixture(t, f.stacks, region, "10.102.0.0/16", "10.102.1.0/24")
	_, other := routeAssocFixture(t, f.stacks, region, "10.103.0.0/16", "10.103.1.0/24")
	_, peer := routeAssocFixture(t, f.stacks, peerRegion, "10.102.0.0/16", "10.102.1.0/24")
	add := func(region string, q ec2NativeRouteAssociationQuery) EC2NetworkResource {
		t.Helper()
		a, err := f.stacks.associateNativeRouteTable(t.Context(), region, q, routeAssocAdminFixture)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := add(region, q), add(peerRegion, peer)
	db := f.store.Database()
	want := map[string][]ec2RouteAssociationRead{q.table: {{ID: a.ID, Table: q.table, Subnet: q.subnet}}}
	wantPeer := map[string][]ec2RouteAssociationRead{peer.table: {{ID: b.ID, Table: peer.table, Subnet: peer.subnet}}}
	if len(f.describe(region)) != 2 {
		t.Fatal("expected a healthy native VPC snapshot")
	}
	failClosed := func(wantErr error) {
		t.Helper()
		for _, query := range []url.Values{
			{"Action": {"DescribeRouteTables"}}, {"Action": {"DescribeVpcs"}},
			routeAssocResilienceAssociate(other), routeAssocResilienceRemove(a.ID),
		} {
			body := f.expect(f.call("", region, nil, query), 500, "InternalError")
			routeAssocResilienceNoSuccess(t, body)
			for _, id := range []string{q.table, q.subnet, a.ID} {
				if strings.Contains(body, id) {
					t.Fatalf("inconsistent snapshot exposed an endpoint or receipt: %s", body)
				}
			}
		}
		if got, n, err := routeAssocSnapshot(t, f.stacks, region); got != nil || n != 0 || !errors.Is(err, wantErr) {
			t.Fatalf("expected no partial projection: %+v / %d / %v", got, n, err)
		}
		routeAssocExpectProjection(t, f.stacks, peerRegion, wantPeer)
	}
	routeAssocResilienceExec(t, db, `UPDATE aws_ec2_network_resources SET revision=2, document=json_set(document,'$.Revision',2)
		WHERE region=? AND id=?`, region, a.ID)
	failClosed(errEC2NativeRouteAssociationState)
	routeAssocResilienceExec(t, db, `UPDATE aws_ec2_network_resources SET revision=1, document=json_set(document,'$.Revision',1)
		WHERE region=? AND id=?`, region, a.ID)
	routeAssocExpectProjection(t, f.stacks, region, want)
	const duplicate = "rtbassoc-00000001"
	routeAssocResilienceExec(t, db, `INSERT INTO aws_ec2_network_resources
		SELECT region,?,stack_id,kind,json_set(document,'$.ID',?),revision FROM aws_ec2_network_resources WHERE region=? AND id=?`,
		duplicate, duplicate, region, a.ID)
	failClosed(errEC2NativeRouteAssociationState)
	routeAssocResilienceExec(t, db, "DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", region, duplicate)
	routeAssocExpectProjection(t, f.stacks, region, want)
	// Simulate an inconsistent imported ownership witness for the exact native ID.
	// An existing CFN stack must not be allowed to make the API adopt either side.
	stack, err := compileStack(region, "association-ownership-fixture", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.105.0.0/16"}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	namespace := stackResourceNamespace(region, ec2Assoc)
	routeAssocResilienceExec(t, db, "INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", namespace, ec2Assoc, a.ID, stack.ID)
	failClosed(errEC2NativeRouteTableState)
	routeAssocResilienceExec(t, db, "DELETE FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?", namespace, a.ID)
	routeAssocExpectProjection(t, f.stacks, region, want)
	if len(f.describe(region)) != 3 {
		t.Fatal("restored inventory did not recover the VPC snapshot")
	}
	f.expect(f.call("", region, nil, routeAssocResilienceRemove(a.ID)), 200, "")
	routeAssocExpectProjection(t, f.stacks, region, map[string][]ec2RouteAssociationRead{})
	routeAssocExpectProjection(t, f.stacks, peerRegion, wantPeer)
}

func TestEC2NativeRouteAssociationRejectsUnavailableTransaction(t *testing.T) {
	const region = routeAssocTestRegion
	_, s := nativeRouteTableTestStore(t, ":memory:")
	_, q := routeAssocFixture(t, s, region, "10.104.0.0/16", "10.104.1.0/24")
	ctx := t.Context()
	initial, err := s.associateNativeRouteTable(ctx, region, q, routeAssocAdminFixture)
	if err != nil {
		t.Fatal(err)
	}
	a := ec2NativeRouteAssociationQuery{association: initial.ID}
	wantNativeRouteTableError(t, s.disassociateNativeRouteTable(ctx, region, initial.ID, ec2NativeRouteAssociationAuthority{revealMissingID: true}), "UnauthorizedOperation")
	out, err := s.executeEC2NativeRouteAssociation(ctx, ec2AssociateNativeRouteTable, "invalid-region", q, routeAssocAdminFixture)
	if out != nil {
		t.Fatalf("typed call accepted unsupported region: %+v", out)
	}
	wantNativeRouteTableError(t, err, "InvalidParameterValue")
	out, err = s.executeEC2NativeRouteAssociation(ctx, "ReplaceRouteTableAssociation", region, a, routeAssocAdminFixture)
	if out != nil {
		t.Fatalf("typed call accepted unsupported action: %+v", out)
	}
	wantNativeRouteTableError(t, err, "UnsupportedOperation")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, entry := range []struct {
		action string
		query  ec2NativeRouteAssociationQuery
	}{
		{ec2AssociateNativeRouteTable, q},
		{ec2DisassociateNativeRouteTable, a},
	} {
		out, err := s.executeEC2NativeRouteAssociation(canceled, entry.action, region, entry.query, routeAssocAdminFixture)
		if out != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("%s accepted a canceled transaction: %+v / %v", entry.action, out, err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, n, err := nativeRouteAssociationProjection(canceled, tx, region)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if got != nil || n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled projection returned a live snapshot: %+v / %d / %v", got, n, err)
	}
	want := map[string][]ec2RouteAssociationRead{q.table: {{ID: initial.ID, Table: q.table, Subnet: q.subnet}}}
	routeAssocExpectProjection(t, s, region, want)
	if again, err := s.associateNativeRouteTable(ctx, region, q, routeAssocAdminFixture); err != nil || !reflect.DeepEqual(again, initial) {
		t.Fatalf("fresh retry did not preserve the exact original association: %+v / %v", again, err)
	}
}

func TestEC2NativeRouteAssociationChecksStoreBounds(t *testing.T) {
	const region, peer = routeAssocTestRegion, "ap-northeast-1"
	f := newEC2VPCFixture(t)
	_, q := routeAssocFixture(t, f.stacks, region, "10.106.0.0/16", "10.106.1.0/24")
	db := f.store.Database()
	// Seed ordinary unrelated rows in one SQLite statement. The public write
	// must observe the same global capacity, even with no local association.
	const networkLimit = 1000
	var initial int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM aws_ec2_network_resources").Scan(&initial); err != nil {
		t.Fatal(err)
	}
	routeAssocResilienceExec(t, db, `WITH RECURSIVE fixtures(i) AS (
		VALUES(1) UNION ALL SELECT i+1 FROM fixtures WHERE i<?
	) INSERT INTO aws_ec2_network_resources
	SELECT ?,printf('vpc-%08x',i),?,'AWS::EC2::VPC',
	json_object('ID',printf('vpc-%08x',i),'Region',?,'StackID',?,'Kind','AWS::EC2::VPC','Revision',1,'CIDR','10.250.0.0/16'),1
	FROM fixtures`, networkLimit-initial, peer, ec2NativeOwner, peer, ec2NativeOwner)
	full := f.expect(f.call("", region, nil, routeAssocResilienceAssociate(q)), 400, "ResourceLimitExceeded")
	routeAssocResilienceNoSuccess(t, full)
	empty := map[string][]ec2RouteAssociationRead{}
	routeAssocExpectProjection(t, f.stacks, region, empty)

	routeAssocResilienceExec(t, db, "DELETE FROM aws_ec2_network_resources WHERE region=? AND id='vpc-00000001'", peer)
	body := f.expect(f.call("", region, nil, routeAssocResilienceAssociate(q)), 200, "")
	var a ec2NativeRouteAssociationResponse
	if err := xml.Unmarshal([]byte(body), &a); err != nil || !ec2NativeRouteAssociationID.MatchString(a.AssociationID) {
		t.Fatalf("expected association after one slot was reclaimed: %s / %v", body, err)
	}
	want := map[string][]ec2RouteAssociationRead{q.table: {{ID: a.AssociationID, Table: q.table, Subnet: q.subnet}}}
	routeAssocExpectProjection(t, f.stacks, region, want)
	routeAssocResilienceExec(t, db, "DELETE FROM aws_ec2_network_resources WHERE region=? AND stack_id=?", peer, ec2NativeOwner)

	// An imported overfull native region cannot be truncated to a successful
	// snapshot or used as precedent for editing. Existing healthy state recovers
	// after exactly the synthetic short-ID rows are removed.
	routeAssocResilienceExec(t, db, `WITH RECURSIVE fixtures(i) AS (
		VALUES(1) UNION ALL SELECT i+1 FROM fixtures WHERE i<?
	) INSERT INTO aws_ec2_network_resources
	SELECT region,printf('rtbassoc-%08x',i),stack_id,kind,json_set(document,'$.ID',printf('rtbassoc-%08x',i)),revision
	FROM fixtures,aws_ec2_network_resources WHERE region=? AND id=?`, ec2NativeRouteTableRowLimit, region, a.AssociationID)
	routeAssocResilienceNoSuccess(t, f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeRouteTables"}}), 500, "InternalError"))
	routeAssocResilienceNoSuccess(t, f.expect(f.call("", region, nil, routeAssocResilienceRemove(a.AssociationID)), 500, "InternalError"))
	routeAssocExpectProjection(t, f.stacks, peer, empty)
	routeAssocResilienceExec(t, db, "DELETE FROM aws_ec2_network_resources WHERE region=? AND stack_id=? AND kind=? AND length(id)=?", region, ec2NativeOwner, ec2Assoc, len("rtbassoc-00000001"))
	routeAssocExpectProjection(t, f.stacks, region, want)
	f.expect(f.call("", region, nil, routeAssocResilienceRemove(a.AssociationID)), 200, "")
	routeAssocExpectProjection(t, f.stacks, region, empty)
}
