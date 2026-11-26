package aws

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
)

type igwLinkFaultFixture struct {
	f                  ec2VPCFixture
	gateway, vpc, link string
}

func newIGWLinkFaultFixture(t *testing.T, attached bool) igwLinkFaultFixture {
	t.Helper()
	f := newEC2VPCFixture(t)
	v := f.create("", url.Values{"CidrBlock": {"10.81.0.0/16"}}).ID
	g := nativeGatewayTestCreate(t, f)
	if attached {
		nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, g, v)
	}
	return igwLinkFaultFixture{f: f, gateway: g, vpc: v, link: nativeInternetGatewayLinkID(g)}
}

func (x igwLinkFaultFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := x.f.stacks.db.Exec(sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Compare authoritative network rows, stack claims, and declarations.
// Comparing only a published view would miss a shadow write after refusal.
func (x igwLinkFaultFixture) image(t *testing.T) []string {
	t.Helper()
	db := x.f.stacks.db
	var out []string
	for _, query := range []string{
		"SELECT json_array(region,id,stack_id,kind,revision,document) FROM aws_ec2_network_resources ORDER BY region,id",
		"SELECT json_array(namespace,kind,physical_id,stack_id) FROM aws_cfn_ownership ORDER BY namespace,kind,physical_id",
		"SELECT json_array(region,name,id,document) FROM aws_cfn_stacks ORDER BY region,id",
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out = append(out, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (x igwLinkFaultFixture) errorResponse(t *testing.T, ctx context.Context, action, code string, fields url.Values) {
	t.Helper()
	fields.Set("Action", action)
	status := 400
	if code == "InternalError" {
		status = 500
	}
	raw := x.f.expect(x.f.call("", igwRegion, ctx, fields), status, code)
	if strings.Contains(raw, "<return>") || strings.Contains(raw, "<internetGatewaySet>") {
		t.Fatalf("%s published partial data or acknowledged a rejected write: %s", action, raw)
	}
}

func (x igwLinkFaultFixture) linkError(t *testing.T, action, code string) {
	t.Helper()
	x.errorResponse(t, nil, action, code, url.Values{"InternetGatewayId": {x.gateway}, "VpcId": {x.vpc}})
}

func (x igwLinkFaultFixture) assertAttachment(t *testing.T, wantVPC string) {
	t.Helper()
	r := nativeGatewayTestExact(t, x.f, x.gateway)
	if wantVPC == "" {
		if len(r.Attachments) != 0 {
			t.Fatalf("a detached gateway acquired a link: %+v", r)
		}
		return
	}
	if !slices.Equal(r.Attachments, []ec2GatewayAttachment{{VPCID: wantVPC, State: "available"}}) {
		t.Fatalf("the original attachment was not preserved: %+v", r)
	}
}

func (x igwLinkFaultFixture) replaceDocument(t *testing.T, id, expression string) func() {
	t.Helper()
	var old string
	if err := x.f.stacks.db.QueryRow("SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, id).Scan(&old); err != nil {
		t.Fatal(err)
	}
	// expression is a constant owned by the test fixture, not an API input.
	x.exec(t, "UPDATE aws_ec2_network_resources SET document="+expression+" WHERE region=? AND id=?", igwRegion, id)
	return func() {
		x.exec(t, "UPDATE aws_ec2_network_resources SET document=? WHERE region=? AND id=?", old, igwRegion, id)
	}
}

func TestEC2NativeInternetGatewayAttachmentDoesNotTrustDamagedInventory(t *testing.T) {
	// Each fixture starts with a real, healthy attachment. Simulate only
	// durable-store faults in this isolated DB, not inputs to an external API.
	for _, fault := range []string{
		"invalid gateway document", "invalid VPC document", "unsupported gateway fields", "invalid gateway tags",
		"unsupported VPC settings", "competing link", "orphan stack gateway", "foreign link owner",
		"native relation claimed by stack", "native relation declared by stack",
	} {
		t.Run(fault, func(t *testing.T) {
			x := newIGWLinkFaultFixture(t, true)
			dropRow := func(id string) {
				x.exec(t, "DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, id)
			}
			var restore func()
			switch fault {
			case "invalid gateway document":
				restore = x.replaceDocument(t, x.gateway, `json_set(document,'$.Revision',json('{}'))`)
			case "invalid VPC document":
				restore = x.replaceDocument(t, x.vpc, `json_set(document,'$.CIDR',json('{}'))`)
			case "unsupported gateway fields":
				restore = x.replaceDocument(t, x.gateway, `json_set(document,'$.PublicIP',json('true'))`)
			case "invalid gateway tags":
				restore = x.replaceDocument(t, x.gateway, `json_set(document,'$.Tags',json('{"aws:private":"not a native tag"}'))`)
			case "unsupported VPC settings":
				restore = x.replaceDocument(t, x.vpc, `json_set(document,'$.RouteEnabled',json('true'))`)
			case "competing link":
				second := nativeGatewayTestCreate(t, x.f)
				doubled := nativeInternetGatewayLinkID(second)
				x.f.writeNetwork(EC2NetworkResource{ID: doubled, Kind: ec2RTAttachment, Region: igwRegion,
					StackID: ec2NativeOwner, GatewayID: second, VPCID: x.vpc, Revision: 1})
				restore = func() {
					dropRow(doubled)
					if attached := nativeGatewayTestExact(t, x.f, second).Attachments; len(attached) != 0 {
						t.Fatal("refusal moved the other gateway")
					}
				}
			case "orphan stack gateway":
				const orphan = "igw-10203040506070809"
				x.f.writeNetwork(EC2NetworkResource{ID: orphan, Kind: ec2RTGateway, Region: igwRegion,
					StackID: "arn:aws:cloudformation:us-east-1:000000000000:stack/missing/fixture", Revision: 1})
				restore = func() { dropRow(orphan) }
			case "foreign link owner":
				stack, _, _, _ := internetGatewayTestStack(t, x.f)
				changeOwner := func(owner string) {
					x.exec(t, "UPDATE aws_ec2_network_resources SET stack_id=?,document=json_set(document,'$.StackID',?) WHERE region=? AND id=?",
						owner, owner, igwRegion, x.link)
				}
				changeOwner(stack.ID)
				restore = func() { changeOwner(ec2NativeOwner) }
			case "native relation claimed by stack":
				stack, _, _, _ := internetGatewayTestStack(t, x.f)
				ns := stackResourceNamespace(igwRegion, ec2RTAttachment)
				x.exec(t, "INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", ns, ec2RTAttachment, x.link, stack.ID)
				restore = func() {
					x.exec(t, "DELETE FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?", ns, ec2RTAttachment, x.link)
				}
			case "native relation declared by stack":
				stack, _, _, _ := internetGatewayTestStack(t, x.f)
				var raw string
				if err := x.f.stacks.db.QueryRow("SELECT document FROM aws_cfn_stacks WHERE id=?", stack.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				for _, member := range stack.Resources {
					if member.Type == ec2RTAttachment {
						x.exec(t, "UPDATE aws_cfn_stacks SET document=replace(document,?,?) WHERE id=?", member.PhysicalID, x.link, stack.ID)
					}
				}
				restore = func() {
					x.exec(t, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", raw, stack.ID)
				}
			default:
				t.Fatalf("missing fault fixture for %s", fault)
			}
			image := x.image(t)
			x.errorResponse(t, nil, ec2DescribeInternetGateway, "InternalError", url.Values{"InternetGatewayId.1": {x.gateway}})
			x.linkError(t, ec2DetachInternetGateway, "InternalError")
			if fault == "native relation claimed by stack" || fault == "native relation declared by stack" ||
				fault == "unsupported VPC settings" {
				x.errorResponse(t, nil, "DescribeVpcs", "InternalError", url.Values{})
			}
			if fault == "unsupported gateway fields" || fault == "invalid gateway tags" {
				x.errorResponse(t, nil, ec2DeleteInternetGateway, "InternalError", url.Values{"InternetGatewayId": {x.gateway}})
			}
			if !slices.Equal(x.image(t), image) {
				t.Fatal("read or rejected detach changed saved state")
			}
			restore()
			x.assertAttachment(t, x.vpc)
			nativeGatewayTestChange(t, x.f, "", ec2DetachInternetGateway, x.gateway, x.vpc)
			x.assertAttachment(t, "")
		})
	}
}

func TestEC2NativeInternetGatewayAttachmentRollsBackSQLiteRefusal(t *testing.T) {
	for _, tc := range []struct{ name, when, body string }{
		{"statement abort", "BEFORE DELETE", "SELECT RAISE(ABORT,'synthetic write refused');"},
		{"zero affected rows", "BEFORE DELETE", "SELECT RAISE(IGNORE);"},
		{"regional readback corrupted", "AFTER DELETE", `UPDATE aws_ec2_network_resources
			SET document=json_set(document,'$.Tags',json('{"aws:reserved":"invalid"}')) WHERE id=SIDE_GATEWAY;`},
		{"reciprocal VPC readback disagrees", "AFTER DELETE", `INSERT INTO aws_ec2_network_resources(region,id,stack_id,kind,document,revision)
			VALUES(OLD.region,SIDE_LINK,OLD.stack_id,OLD.kind,json_set(OLD.document,'$.ID',SIDE_LINK,'$.GatewayID',SIDE_GATEWAY),OLD.revision);`},
		{"parent claimed during final readback", "AFTER DELETE", `INSERT INTO aws_cfn_ownership(namespace,kind,physical_id,stack_id)
			VALUES(NETWORK_NAMESPACE,'AWS::EC2::VPC',json_extract(OLD.document,'$.VPCID'),STACK_ID);`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newIGWLinkFaultFixture(t, true)
			spare := nativeGatewayTestCreate(t, x.f)
			// The fixture owner has no gateway or attachment. An unexpected
			// claim against the VPC is discovered by its final native check.
			stack, err := compileStack(igwRegion, "igw-final-claim-fixture", `{"Resources":{
				"Reports":{"Type":"AWS::S3::Bucket","Properties":{"BucketName":"igw-rollback-test-reports"}}
			}}`, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := x.f.stacks.Create(stack); err != nil {
				t.Fatal(err)
			}
			image := x.image(t)
			quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
			body := strings.NewReplacer(
				"SIDE_GATEWAY", quote(spare), "SIDE_LINK", quote(nativeInternetGatewayLinkID(spare)),
				"NETWORK_NAMESPACE", quote(stackResourceNamespace(igwRegion, ec2RTAttachment)), "STACK_ID", quote(stack.ID),
			).Replace(tc.body)
			x.exec(t, "CREATE TRIGGER igw_write_fault "+tc.when+" ON aws_ec2_network_resources WHEN OLD.id="+quote(x.link)+" BEGIN "+body+" END")
			x.linkError(t, ec2DetachInternetGateway, "InternalError")
			if !slices.Equal(x.image(t), image) {
				t.Fatal("failed detach persisted a side effect")
			}
			x.assertAttachment(t, x.vpc)
			x.exec(t, "DROP TRIGGER igw_write_fault")
			nativeGatewayTestChange(t, x.f, "", ec2DetachInternetGateway, x.gateway, x.vpc)
			x.assertAttachment(t, "")
		})
	}
}

func TestEC2NativeInternetGatewayAttachmentRefusesUnavailableStorage(t *testing.T) {
	t.Run("canonical data unavailable", func(t *testing.T) {
		for _, table := range []string{"aws_cfn_ownership", "aws_cfn_stacks"} {
			t.Run(table, func(t *testing.T) {
				x := newIGWLinkFaultFixture(t, false)
				saved := x.image(t)
				x.exec(t, "ALTER TABLE "+table+" RENAME TO fixture_unavailable")
				x.linkError(t, ec2AttachInternetGateway, "InternalError")
				x.errorResponse(t, nil, ec2DescribeInternetGateway, "InternalError", url.Values{})
				x.errorResponse(t, nil, ec2CreateInternetGateway, "InternalError", url.Values{})
				x.errorResponse(t, nil, ec2DeleteInternetGateway, "InternalError", url.Values{"InternetGatewayId": {x.gateway}})
				x.exec(t, "ALTER TABLE fixture_unavailable RENAME TO "+table)
				if !slices.Equal(x.image(t), saved) {
					t.Fatal("a call committed without verifying authoritative ownership")
				}
				nativeGatewayTestChange(t, x.f, "", ec2AttachInternetGateway, x.gateway, x.vpc)
				x.assertAttachment(t, x.vpc)
			})
		}
	})
	t.Run("cancel before store admission", func(t *testing.T) {
		x := newIGWLinkFaultFixture(t, false)
		saved := x.image(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, tc := range []struct {
			action string
			fields url.Values
		}{
			{ec2AttachInternetGateway, url.Values{"InternetGatewayId": {x.gateway}, "VpcId": {x.vpc}}},
			{ec2DescribeInternetGateway, url.Values{}},
			{ec2CreateInternetGateway, url.Values{}},
			{ec2DeleteInternetGateway, url.Values{"InternetGatewayId": {x.gateway}}},
		} {
			x.errorResponse(t, ctx, tc.action, "InternalError", tc.fields)
		}
		if !slices.Equal(x.image(t), saved) {
			t.Fatal("canceled operations changed durable state")
		}
		x.assertAttachment(t, "")
	})
	t.Run("insert and cleanup statements fail", func(t *testing.T) {
		x := newIGWLinkFaultFixture(t, false)
		saved := x.image(t)
		x.exec(t, `CREATE TRIGGER igw_abort_insert BEFORE INSERT ON aws_ec2_network_resources
			WHEN NEW.kind='AWS::EC2::VPCGatewayAttachment' BEGIN SELECT RAISE(ABORT,'fixture store refused insert'); END`)
		x.linkError(t, ec2AttachInternetGateway, "InternalError")
		x.exec(t, "DROP TRIGGER igw_abort_insert")
		x.exec(t, `CREATE TRIGGER igw_abort_delete BEFORE DELETE ON aws_ec2_network_resources
			WHEN OLD.kind='AWS::EC2::InternetGateway' BEGIN SELECT RAISE(ABORT,'fixture store refused cleanup'); END`)
		x.errorResponse(t, nil, ec2DeleteInternetGateway, "InternalError", url.Values{"InternetGatewayId": {x.gateway}})
		x.exec(t, "DROP TRIGGER igw_abort_delete")
		if !slices.Equal(x.image(t), saved) {
			t.Fatal("SQLite write failure changed a parent or left a relation")
		}
		nativeGatewayTestChange(t, x.f, "", ec2AttachInternetGateway, x.gateway, x.vpc)
		nativeGatewayTestChange(t, x.f, "", ec2DetachInternetGateway, x.gateway, x.vpc)
	})
}

func TestEC2NativeInternetGatewayAttachmentQuotaAndRecovery(t *testing.T) {
	x := newIGWLinkFaultFixture(t, false)
	_, cfnGateway, _, cfnVPC := internetGatewayTestStack(t, x.f)
	db := x.f.stacks.db
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_ec2_network_resources").Scan(&count); err != nil {
		t.Fatal(err)
	}
	makeGateway := func(id string) EC2NetworkResource {
		return EC2NetworkResource{ID: id, Kind: ec2RTGateway, Region: igwRegion, StackID: ec2NativeOwner, Revision: 1}
	}
	const disposable = "igw-00000000000000000"
	for i := range ec2GatewayListLimit - count {
		// A valid, full inventory, created directly in this disposable fixture.
		if err := networkWrite(t.Context(), tx, makeGateway(fmt.Sprintf("igw-%017x", i)), true); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	saved := x.image(t)
	x.linkError(t, ec2AttachInternetGateway, "ResourceLimitExceeded")
	x.errorResponse(t, nil, ec2CreateInternetGateway, "InternetGatewayLimitExceeded", url.Values{})
	x.assertAttachment(t, "")
	if !slices.Equal(x.image(t), saved) {
		t.Fatal("quota refusal modified a full inventory")
	}

	// Loading inconsistent previous state must not publish a partial regional
	// inventory, even when the requested ID is not the excess row.
	const excess = "igw-fffffffffffffffff"
	x.f.writeNetwork(makeGateway(excess))
	x.errorResponse(t, nil, ec2DescribeInternetGateway, "InternalError", url.Values{"InternetGatewayId.1": {x.gateway}})
	x.linkError(t, ec2AttachInternetGateway, "InternalError")
	x.exec(t, "DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, excess)
	x.f.expect(x.f.call("", igwRegion, nil, url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {disposable}}), 200, "")
	nativeGatewayTestChange(t, x.f, "", ec2AttachInternetGateway, x.gateway, x.vpc)
	x.assertAttachment(t, x.vpc)
	if stackSide := nativeGatewayTestExact(t, x.f, cfnGateway); !slices.Equal(stackSide.Attachments, []ec2GatewayAttachment{{VPCID: cfnVPC, State: "available"}}) {
		t.Fatalf("quota or cleanup disturbed the stack gateway: %+v", stackSide)
	}
	nativeGatewayTestChange(t, x.f, "", ec2DetachInternetGateway, x.gateway, x.vpc)
	x.assertAttachment(t, "")
}
