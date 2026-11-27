package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
)

func aclBindingTx(t *testing.T, db *sql.DB, run func(context.Context, *sql.Tx) error) error {
	t.Helper()
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := run(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func storeACLBindingFixture(t *testing.T, s *StackStore, region, vpc, id, cidr string) EC2NetworkResource {
	t.Helper()
	row := EC2NetworkResource{ID: id, Kind: ec2NativeSubnetKind, Region: region, StackID: ec2NativeOwner,
		VPCID: vpc, CIDR: cidr, AvailabilityZone: region + "a", Revision: 1}
	if err := aclBindingTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := nativeSubnetReadback(ctx, tx, row); err != nil {
			return err
		}
		if err := createEC2Network(ctx, tx, row); err != nil {
			return err
		}
		return createNativeSubnetACLAssociation(ctx, tx, row)
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

func readACLBindingFixture(t *testing.T, s *StackStore, vpc EC2NetworkResource) (nativeACLDefault, map[string][]ec2NetworkACLAssociationRead) {
	t.Helper()
	var d nativeACLDefault
	var byACL map[string][]ec2NetworkACLAssociationRead
	if err := aclBindingTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
		declared, err := nativeACLDeclarations(ctx, tx, vpc.Region)
		if err != nil {
			return err
		}
		d, byACL, err = nativeNetworkACLAssociationProjection(ctx, tx, vpc.Region, vpc.ID, declared)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return d, byACL
}

func installDefaultACLFixture(t *testing.T, s *StackStore, vpc EC2NetworkResource) {
	t.Helper()
	if err := aclBindingTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
		d, found, err := rawNativeACLDefault(ctx, tx, vpc.Region, vpc.ID)
		if err != nil {
			return err
		}
		if !found {
			return createNativeNetworkACLDefault(ctx, tx, vpc)
		}
		declared, err := nativeACLDeclarations(ctx, tx, vpc.Region)
		if err != nil {
			return err
		}
		return validateNativeACLDefault(ctx, tx, d, declared)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEC2NetworkACLAssociationLeafQuery(t *testing.T) {
	query := url.Values{"AssociationId": {"aclassoc-01234567"}, "NetworkAclId": {"acl-2abc4567"}, "DryRun": {"false"}}
	got, failure := parseEC2NetworkACLAssociation(query)
	if failure != nil || got.association != query.Get("AssociationId") || got.acl != query.Get("NetworkAclId") {
		t.Fatalf("invalid existing-API query shape: %+v / %v", got, failure)
	}
	for name, patch := range map[string]url.Values{
		"missing":                      {"AssociationId": {""}},
		"other association namespace":  {"AssociationId": {"rtbassoc-01234567"}},
		"other ACL resource namespace": {"NetworkAclId": {"subnet-01234567"}},
		"extra":                        {"SubnetId": {"subnet-01234567"}},
		"duplicate":                    {"AssociationId": {"aclassoc-01234567", "aclassoc-12345678"}},
		"dry run unsupported":          {"DryRun": {"true"}},
	} {
		t.Run(name, func(t *testing.T) {
			fields := url.Values{}
			for k, v := range query {
				fields[k] = v
			}
			for k, v := range patch {
				fields[k] = v
			}
			if _, failure := parseEC2NetworkACLAssociation(fields); failure == nil {
				t.Fatal("unsupported shape admitted")
			}
		})
	}
	wire, err := xml.Marshal(ec2ReplaceNetworkACLAssociationResponse{XMLNS: "http://ec2.amazonaws.com/doc/2016-11-15/", RequestID: "fixture", NewAssociationID: "aclassoc-01234567"})
	if want := "<ReplaceNetworkAclAssociationResponse xmlns=\"http://ec2.amazonaws.com/doc/2016-11-15/\"><requestId>fixture</requestId><newAssociationId>aclassoc-01234567</newAssociationId></ReplaceNetworkAclAssociationResponse>"; err != nil || string(wire) != want {
		t.Fatalf("wire changed: %s / %v", wire, err)
	}
}

func TestEC2NetworkACLAssociationLeafPersistenceAuthorizationAndRollback(t *testing.T) {
	const region = "us-east-1"
	path := filepath.Join(t.TempDir(), "acl-binding.sqlite")
	store, s := openNetworkACLState(t, path)
	if err := initEC2NativeNetworkACLAssociations(s.db); err != nil {
		t.Fatal(err)
	}
	vpc := newNetworkACLParent(t, s, region, "10.78.0.0/16")
	installDefaultACLFixture(t, s, vpc)
	subnet := storeACLBindingFixture(t, s, region, vpc.ID, "subnet-01234567", "10.78.1.0/24")
	peer := storeACLBindingFixture(t, s, region, vpc.ID, "subnet-12345678", "10.78.2.0/24")
	custom, err := s.createNativeNetworkACL(t.Context(), region, ec2NetworkACLRequest{vpcID: vpc.ID})
	if err != nil {
		t.Fatal(err)
	}
	otherVPC := newNetworkACLParent(t, s, region, "10.79.0.0/16")
	foreign, err := s.createNativeNetworkACL(t.Context(), region, ec2NetworkACLRequest{vpcID: otherVPC.ID})
	if err != nil {
		t.Fatal(err)
	}
	d, byACL := readACLBindingFixture(t, s, vpc)
	initial := nativeACLBindingID(region, subnet.ID, 1)
	if d.vpcID != vpc.ID || d.id != nativeDefaultACLID(region, vpc.ID) || !reflect.DeepEqual(byACL[d.id],
		[]ec2NetworkACLAssociationRead{{initial, d.id, subnet.ID}, {nativeACLBindingID(region, peer.ID, 1), d.id, peer.ID}}) {
		t.Fatalf("stored default does not own both associations: %+v", byACL)
	}
	if entries := nativeDefaultNetworkACLEntries(); len(entries) != 4 || entries[0].Rule != 100 || entries[1].Rule != 32767 ||
		entries[2].Action != "allow" || !entries[3].Egress || entries[3].Action != "deny" {
		t.Fatalf("unexpected immutable IPv4 defaults: %+v", entries)
	}
	admin := ec2NetworkACLAssociationAuthority{check: func(string, string) bool { return true }, revealMissingID: true}
	q := ec2NetworkACLAssociationQuery{initial, custom.ID}
	subnetARN, aclARN := networkARN(region, "subnet", subnet.ID), networkARN(region, "network-acl", custom.ID)
	exact := func(resources ...string) ec2NetworkACLAssociationAuthority {
		return ec2NetworkACLAssociationAuthority{check: func(action, arn string) bool {
			if action != "ec2:ReplaceNetworkAclAssociation" {
				return false
			}
			for _, res := range resources {
				if res == arn {
					return true
				}
			}
			return false
		}}
	}
	for _, a := range []ec2NetworkACLAssociationAuthority{{}, exact(subnetARN), exact(aclARN), exact(aclARN, networkARN(region, "vpc", vpc.ID))} {
		_, err := s.replaceNativeNetworkACLAssociation(t.Context(), region, q, a)
		expectNetworkACLError(t, err, "UnauthorizedOperation")
	}
	_, err = s.replaceNativeNetworkACLAssociation(t.Context(), "ap-northeast-1", q, admin)
	expectNetworkACLError(t, err, "InvalidAssociationID.NotFound")
	_, err = s.replaceNativeNetworkACLAssociation(t.Context(), region, ec2NetworkACLAssociationQuery{initial, foreign.ID}, admin)
	expectNetworkACLError(t, err, "InvalidParameterValue")
	naclRegressionExec(t, s.db, `CREATE TRIGGER acl_fixture_extra AFTER UPDATE ON aws_ec2_native_acl_associations
		BEGIN UPDATE aws_ec2_network_resources SET document=document WHERE region=NEW.region AND id=NEW.subnet_id; END`)
	if result, err := s.executeEC2NetworkACLAssociation(t.Context(), region, q, admin); result != nil || err == nil {
		t.Fatalf("unexpected extra write acknowledged: %+v / %v", result, err)
	}
	naclRegressionExec(t, s.db, `DROP TRIGGER acl_fixture_extra`)
	if _, observed := readACLBindingFixture(t, s, vpc); !reflect.DeepEqual(byACL, observed) {
		t.Fatalf("rejected update changed durable association state: %+v", observed)
	}
	changed, err := s.replaceNativeNetworkACLAssociation(t.Context(), region, q, exact(subnetARN, aclARN))
	if err != nil || changed == initial || changed != nativeACLBindingID(region, subnet.ID, 2) {
		t.Fatalf("native change did not return expected persisted identity: %s / %v", changed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, restarted := openNetworkACLState(t, path)
	if err := initEC2NativeNetworkACLAssociations(restarted.db); err != nil {
		t.Fatal(err)
	}
	if persisted, observed := readACLBindingFixture(t, restarted, vpc); persisted != d ||
		!reflect.DeepEqual(observed[custom.ID], []ec2NetworkACLAssociationRead{{changed, custom.ID, subnet.ID}}) ||
		!reflect.DeepEqual(observed[d.id], byACL[d.id][1:]) {
		t.Fatalf("subnet bindings did not reopen: %+v", observed)
	}
	for _, retry := range []ec2NetworkACLAssociationQuery{q, {changed, custom.ID}} {
		if got, err := restarted.replaceNativeNetworkACLAssociation(t.Context(), region, retry, admin); err != nil || got != changed {
			t.Fatalf("repeat advanced the generation: %s / %v", got, err)
		}
	}
	expectNetworkACLError(t, aclBindingTx(t, restarted.db, func(ctx context.Context, tx *sql.Tx) error {
		return rejectAttachedNativeACL(ctx, tx, region, custom.ID)
	}), "DependencyViolation")
	newID, err := restarted.replaceNativeNetworkACLAssociation(t.Context(), region, ec2NetworkACLAssociationQuery{changed, d.id}, admin)
	if err != nil || newID != nativeACLBindingID(region, subnet.ID, 3) || newID == initial {
		t.Fatalf("return to default: %s / %v", newID, err)
	}
	_, err = restarted.replaceNativeNetworkACLAssociation(t.Context(), region, q, admin)
	expectNetworkACLError(t, err, "InvalidAssociationID.NotFound")
	if err := restarted.deleteNativeNetworkACL(t.Context(), region, custom.ID); err != nil {
		t.Fatal(err)
	}
	// Existing core state still has the same VPC and subnet documents.
	if err := aclBindingTx(t, restarted.db, func(ctx context.Context, tx *sql.Tx) error {
		for _, expected := range []EC2NetworkResource{vpc, subnet, peer} {
			actual, err := networkRead(ctx, tx, region, expected.ID)
			if err != nil || !reflect.DeepEqual(expected, actual) {
				t.Fatalf("unrelated network row changed: %s / %v", expected.ID, err)
			}
		}
		return removeNativeNetworkACLDefault(ctx, tx, vpc)
	}); err == nil {
		t.Fatal("default ACL retired while subnets are present")
	}
}

func TestEC2NetworkACLAssociationLeafLeavesLegacyUntouched(t *testing.T) {
	const region = "us-east-1"
	_, s := openNetworkACLState(t, filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err := initEC2NativeNetworkACLAssociations(s.db); err != nil {
		t.Fatal(err)
	}
	// Reproduce the genuine persisted predecessor shape: native VPC and
	// verified main route together, with no enrollment in either ACL table.
	vpc := EC2NetworkResource{ID: "vpc-03030303", Kind: ec2NetworkACLVPCType, Region: region,
		StackID: ec2NativeOwner, CIDR: "10.82.0.0/16", DNSSupport: true, Revision: 1}
	vpc.MainRouteTableID = nativeMainTableID(region, vpc.ID)
	if err := aclBindingTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
		if err := createEC2Network(ctx, tx, vpc); err != nil {
			return err
		}
		return createNativeVPCMainTable(ctx, tx, vpc)
	}); err != nil {
		t.Fatal(err)
	}
	subnet := storeACLBindingFixture(t, s, region, vpc.ID, "subnet-03030303", "10.82.1.0/24")
	if d, byACL := readACLBindingFixture(t, s, vpc); d.id != "" || len(byACL) != 0 {
		t.Fatalf("legacy read invented a default ACL or association: %+v / %+v", d, byACL)
	}
	if err := aclBindingTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
		return createNativeNetworkACLDefault(ctx, tx, vpc)
	}); err == nil {
		t.Fatal("ordinary default creation partially adopted an existing subnet")
	}
	if d, byACL := readACLBindingFixture(t, s, vpc); d.id != "" || len(byACL) != 0 {
		t.Fatal("failed enrollment changed legacy state")
	}
	if err := s.deleteNativeSubnet(t.Context(), region, subnet.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteNativeVPC(t.Context(), region, vpc.ID); err != nil {
		t.Fatal(err)
	}
}
