package aws

import (
	"database/sql"
	"encoding/xml"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const subnetLineRegion = "us-east-1"

func nativeSubnetLineTopology(t *testing.T) (ec2VPCFixture, describedVPC, ec2SubnetRead) {
	t.Helper()
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.114.0.0/16"}})
	made, err := f.stacks.createNativeSubnet(t.Context(), subnetLineRegion,
		ec2SubnetMutation{vpc: vpc.ID, cidr: "10.114.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	return f, vpc, made
}

func nativeSubnetLineExec(t *testing.T, db *sql.DB, text string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), text, args...); err != nil {
		t.Fatal(err)
	}
}

func nativeSubnetLineInventory(t *testing.T, f ec2VPCFixture) []ec2SubnetRead {
	t.Helper()
	body := f.expect(f.call("", subnetLineRegion, nil, url.Values{"Action": {"DescribeSubnets"}}), 200, "")
	var doc ec2DescribeSubnetsResponse
	if err := xml.Unmarshal([]byte(body), &doc); err != nil || doc.RequestID == "" {
		t.Fatalf("DescribeSubnets had no native acknowledgement: %v / %s", err, body)
	}
	return doc.Subnets.Items
}

func nativeSubnetLineServerFailure(t *testing.T, f ec2VPCFixture, input url.Values) {
	t.Helper()
	body := f.expect(f.call("", subnetLineRegion, nil, input), 500, "InternalError")
	for _, escaped := range []string{"fixture storage fault", "SQL logic", "sql: ", "<subnetSet>", "<vpcSet>", "<return>true</return>"} {
		if strings.Contains(body, escaped) {
			t.Fatalf("storage failure exposed internals or success (%s): %s", escaped, body)
		}
	}
}

func TestEC2NativeSubnetLineRegressionRejectsUnacknowledgedWrites(t *testing.T) {
	f, vpc, subnet := nativeSubnetLineTopology(t)
	db := f.store.Database()
	create := url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.114.2.0/24"}}
	remove := url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}
	before := nativeSubnetLineInventory(t, f)
	for _, test := range []struct {
		name, trigger string
		form          url.Values
	}{
		{"insert aborted", `BEFORE INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet'
			BEGIN SELECT RAISE(ABORT,'fixture storage fault'); END`, create},
		{"saved row differs from admitted subnet", `AFTER INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::Subnet'
			BEGIN UPDATE aws_ec2_network_resources SET document=json_set(document,'$.AvailabilityZone','us-east-1c')
			WHERE region=NEW.region AND id=NEW.id; END`, create},
		{"delete aborted", `BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::Subnet'
			BEGIN SELECT RAISE(ABORT,'fixture storage fault'); END`, remove},
		{"delete reports no affected row", `BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::Subnet'
			BEGIN SELECT RAISE(IGNORE); END`, remove},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := f
			f.t = t
			nativeSubnetLineExec(t, db, "CREATE TRIGGER subnet_commit_witness "+test.trigger)
			defer nativeSubnetLineExec(t, db, "DROP TRIGGER subnet_commit_witness")
			nativeSubnetLineServerFailure(t, f, test.form)
			if after := nativeSubnetLineInventory(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("refused mutation changed persisted state: %+v → %+v", before, after)
			}
		})
	}
	f.expect(f.call("", subnetLineRegion, nil, remove), 200, "")
	if got := nativeSubnetLineInventory(t, f); len(got) != 0 {
		t.Fatalf("valid delete after the fault was cleared: %+v", got)
	}
}

func TestEC2NativeSubnetLineRegressionRejectsUnreadableSibling(t *testing.T) {
	f, vpc, subnet := nativeSubnetLineTopology(t)
	db := f.store.Database()
	peer := f.create("", url.Values{"CidrBlock": {"10.115.0.0/16"}})
	before := nativeSubnetLineInventory(t, f)
	// The target's relational revision and JSON identity must agree before
	// a delete or an operation accepting that target as a subnet parent.
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=2 WHERE region=? AND id=?`, subnetLineRegion, subnet.ID)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}})
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=1 WHERE region=? AND id=?`, subnetLineRegion, subnet.ID)
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=2 WHERE region=? AND id=?`, subnetLineRegion, vpc.ID)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.114.2.0/24"}})
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET revision=1 WHERE region=? AND id=?`, subnetLineRegion, vpc.ID)

	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document='{' WHERE region=? AND id=?`, subnetLineRegion, peer.ID)
	for _, input := range []url.Values{
		{"Action": {"DescribeSubnets"}}, {"Action": {"DescribeVpcs"}},
		{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.114.2.0/24"}},
		{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}},
		{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}},
	} {
		nativeSubnetLineServerFailure(t, f, input)
	}
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_object('ID',id,'Region',region,
		'StackID',stack_id,'Kind',kind,'Revision',revision,'CIDR',?,'DNSSupport',json('true')) WHERE region=? AND id=?`,
		peer.CIDR, subnetLineRegion, peer.ID)
	if got := nativeSubnetLineInventory(t, f); !reflect.DeepEqual(before, got) {
		t.Fatalf("failed graph operation changed the valid sibling %+v", got)
	}
	if vpcs := f.describe(subnetLineRegion); len(vpcs) != 2 {
		t.Fatalf("recovered VPC inventory: %+v", vpcs)
	}
}

func TestEC2NativeSubnetLineRegressionFencesTypedChildAndParent(t *testing.T) {
	f, vpc, subnet := nativeSubnetLineTopology(t)
	db := f.store.Database()
	before := nativeSubnetLineInventory(t, f)
	const claim = "arn:aws:cloudformation:us-east-1:000000000000:stack/foreign-claim/test"
	nativeSubnetLineExec(t, db, `INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, subnetLineRegion, "foreign-claim", claim, "{}")
	// Even a claim for another network kind in the same typed namespace
	// cannot be mistaken for a legitimate transfer of a native subnet.
	nativeSubnetLineExec(t, db, `INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(subnetLineRegion, ec2NativeSubnetKind), ec2RT, subnet.ID, claim)
	for _, input := range []url.Values{
		{"Action": {"DescribeVpcs"}}, {"Action": {"DescribeSubnets"}},
		{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}},
	} {
		nativeSubnetLineServerFailure(t, f, input)
	}
	nativeSubnetLineExec(t, db, `DELETE FROM aws_cfn_ownership WHERE stack_id=?`, claim)

	// Parent host bits cannot be silently normalized during saved-state
	// admission, unlike a new request's explicitly normalized prefix.
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.CIDR',?) WHERE region=? AND id=?`,
		"10.114.0.9/16", subnetLineRegion, vpc.ID)
	for _, input := range []url.Values{
		{"Action": {"DescribeVpcs"}}, {"Action": {"DescribeSubnets"}},
		{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.114.2.0/24"}},
	} {
		nativeSubnetLineServerFailure(t, f, input)
	}
	nativeSubnetLineExec(t, db, `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.CIDR',?) WHERE region=? AND id=?`,
		vpc.CIDR, subnetLineRegion, vpc.ID)
	if got := nativeSubnetLineInventory(t, f); !reflect.DeepEqual(before, got) {
		t.Fatalf("bad claims or parent corrupted/replaced a saved child: %+v", got)
	}
}

func TestEC2NativeSubnetLineRegressionVpcInventoryRejectsInvalidChild(t *testing.T) {
	f, vpc, _ := nativeSubnetLineTopology(t)
	db := f.store.Database()
	for _, fixture := range []struct {
		name, id, kind, description string
	}{
		{"known group with missing typed metadata", "sg-00000000000000003", ec2SecurityGroupKind, "known fixture"},
		{"known ACL with unsupported persisted field", "acl-00000000000000003", ec2NetworkACLKind, "invalid detached fixture"},
		{"unrecognized kind", "unrecognized-00000000000000003", "UnsupportedNetworkFixture", ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			f := f
			f.t = t
			f.writeNetwork(EC2NetworkResource{Region: subnetLineRegion, ID: fixture.id, Kind: fixture.kind,
				StackID: ec2NativeOwner, VPCID: vpc.ID, Description: fixture.description, Revision: 1})
			defer nativeSubnetLineExec(t, db, `DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?`, subnetLineRegion, fixture.id)
			nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeVpcs"}})
		})
	}
	if got := f.describe(subnetLineRegion); len(got) != 1 || got[0].ID != vpc.ID {
		t.Fatalf("valid local topology not restored: %+v", got)
	}
}

func TestEC2NativeSubnetLineRegressionUnavailableOwnershipDoesNotCommitVpc(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.114.0.0/16"}})
	db := f.store.Database()
	nativeSubnetLineExec(t, db, `ALTER TABLE aws_cfn_ownership RENAME TO temporarily_unavailable_ownership`)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.115.0.0/16"}})
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}})
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.114.1.0/24"}})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Isolate the native discovery step from the earlier CFN enumeration.
	got, readErr := nativeVPCs(t.Context(), tx, subnetLineRegion)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if readErr == nil || got != nil {
		t.Fatalf("native discovery hid unavailable ownership: %+v / %v", got, readErr)
	}
	nativeSubnetLineExec(t, db, `ALTER TABLE temporarily_unavailable_ownership RENAME TO aws_cfn_ownership`)
	if got := f.describe(subnetLineRegion); len(got) != 1 || got[0].ID != vpc.ID {
		t.Fatalf("ownership failure lost or created a VPC: %+v", got)
	}
	if got := nativeSubnetLineInventory(t, f); len(got) != 0 {
		t.Fatalf("ownership failure saved an unverified subnet: %+v", got)
	}
}

func TestEC2NativeSubnetLineRegressionSharedVpcStorageAcknowledgements(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.114.0.0/16"}})
	db := f.store.Database()
	create := url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.115.0.0/16"}}
	remove := url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}
	for _, test := range []struct {
		name, trigger string
		form          url.Values
	}{
		{"VPC insert not acknowledged", `BEFORE INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::VPC'
			BEGIN SELECT RAISE(IGNORE); END`, create},
		{"VPC delete SQLite write failed", `BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::VPC'
			BEGIN SELECT RAISE(ABORT,'fixture storage fault'); END`, remove},
		{"VPC delete not acknowledged", `BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::VPC'
			BEGIN SELECT RAISE(IGNORE); END`, remove},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := f
			f.t = t
			nativeSubnetLineExec(t, db, "CREATE TRIGGER vpc_commit_witness "+test.trigger)
			defer nativeSubnetLineExec(t, db, "DROP TRIGGER vpc_commit_witness")
			nativeSubnetLineServerFailure(t, f, test.form)
			if got := f.describe(subnetLineRegion); len(got) != 1 || got[0].ID != vpc.ID {
				t.Fatalf("VPC storage failure changed identity or count: %+v", got)
			}
		})
	}
	nativeSubnetLineExec(t, db, `ALTER TABLE aws_ec2_network_resources RENAME TO temporarily_unavailable_network`)
	nativeSubnetLineServerFailure(t, f, create)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeVpcs"}})
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeSubnets"}})
	nativeSubnetLineExec(t, db, `ALTER TABLE temporarily_unavailable_network RENAME TO aws_ec2_network_resources`)
	if got := f.describe(subnetLineRegion); len(got) != 1 || got[0].ID != vpc.ID {
		t.Fatalf("unavailable table changed identity or count: %+v", got)
	}
}

func TestEC2NativeSubnetLineRegressionRejectsOvergrownSavedInventory(t *testing.T) {
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	// The storage fixture represents a damaged or incompatible saved state.
	// Normal creation has a different admission test and cannot reach this size.
	nativeSubnetLineExec(t, db, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1001)
	INSERT INTO aws_ec2_network_resources
	SELECT 'us-east-1',printf('vpc-%017x',i),'ec2:native','AWS::EC2::VPC',
	 json_object('Region','us-east-1','ID',printf('vpc-%017x',i),'StackID','ec2:native','Kind','AWS::EC2::VPC','CIDR','10.114.0.0/16','Revision',1),1 FROM n`)
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeVpcs"}})
	nativeSubnetLineServerFailure(t, f, url.Values{"Action": {"DescribeSubnets"}})
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM aws_ec2_network_resources`).Scan(&count); err != nil || count != 1001 {
		t.Fatalf("read mutated out-of-bounds saved state: %d / %v", count, err)
	}
}
