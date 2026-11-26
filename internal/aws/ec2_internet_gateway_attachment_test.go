package aws

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func nativeGatewayTestCreate(t *testing.T, f ec2VPCFixture) string {
	t.Helper()
	out := internetGatewayTestXML(t, f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2CreateInternetGateway}}), 200, ""), ec2CreateInternetGateway)
	if out.Gateway == nil || !ec2InternetGatewayID.MatchString(out.Gateway.ID) || len(out.Gateway.Attachments) != 0 {
		t.Fatalf("new gateway is not detached: %+v", out)
	}
	return out.Gateway.ID
}

func nativeGatewayLinkQuery(action, gateway, vpc string) url.Values {
	return url.Values{"Action": {action}, "InternetGatewayId": {gateway}, "VpcId": {vpc}}
}

func nativeGatewayTestChange(t *testing.T, f ec2VPCFixture, actor, action, gateway, vpc string) {
	t.Helper()
	out := internetGatewayTestXML(t, f.expect(f.call(actor, igwRegion, nil, nativeGatewayLinkQuery(action, gateway, vpc)), 200, ""), action)
	if out.Return == nil || !*out.Return || out.Gateway != nil || out.Gateways != nil {
		t.Fatalf("invalid mutation acknowledgement: %+v", out)
	}
}

func nativeGatewayTestExact(t *testing.T, f ec2VPCFixture, gatewayID string) ec2InternetGateway {
	t.Helper()
	out := internetGatewayTestXML(t, f.expect(f.call("", igwRegion, nil, url.Values{
		"Action": {ec2DescribeInternetGateway}, "InternetGatewayId.1": {gatewayID},
	}), 200, ""), ec2DescribeInternetGateway)
	if out.Gateways == nil || len(out.Gateways.Items) != 1 || out.Gateways.Items[0].ID != gatewayID || out.NextToken != "" {
		t.Fatalf("missing exact gateway: %+v", out)
	}
	return out.Gateways.Items[0]
}

func TestEC2NativeInternetGatewayAttachmentWire(t *testing.T) {
	f := newEC2VPCFixture(t)
	_, stackGateway, _, stackVPC := internetGatewayTestStack(t, f)
	stackBefore := nativeGatewayTestExact(t, f, stackGateway)
	vpc := f.create("", url.Values{"CidrBlock": {"10.51.0.0/16"}}).ID
	spareVPC := f.create("", url.Values{"CidrBlock": {"10.52.0.0/16"}}).ID
	gateway, spare := nativeGatewayTestCreate(t, f), nativeGatewayTestCreate(t, f)

	put := func(allow []string, deny string) {
		t.Helper()
		doc := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Action":["ec2:AttachInternetGateway","ec2:DetachInternetGateway"],"Resource":%q}]`, allow[0])
		doc = strings.TrimSuffix(doc, "]")
		for _, arn := range allow[1:] {
			doc += fmt.Sprintf(`,{"Effect":"Allow","Action":["ec2:AttachInternetGateway","ec2:DetachInternetGateway"],"Resource":%q}`, arn)
		}
		if deny != "" {
			doc += fmt.Sprintf(`,{"Effect":"Deny","Action":"ec2:DetachInternetGateway","Resource":%q}`, deny)
		}
		policy, err := core.ParsePolicy([]byte(doc + "]}"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set("native-link-editor", policy); err != nil {
			t.Fatal(err)
		}
	}
	gatewayARN, vpcARN := networkARN(igwRegion, ec2InternetGatewayARNKind, gateway), networkARN(igwRegion, "vpc", vpc)
	put([]string{gatewayARN}, "")
	for _, dry := range []string{"false", "true"} {
		query := nativeGatewayLinkQuery(ec2AttachInternetGateway, gateway, vpc)
		query.Set("DryRun", dry)
		f.expect(f.call("native-link-editor", igwRegion, nil, query), 403, "UnauthorizedOperation")
	}
	put([]string{gatewayARN, vpcARN}, "")
	query := nativeGatewayLinkQuery(ec2AttachInternetGateway, gateway, vpc)
	query.Set("DryRun", "true")
	f.expect(f.call("native-link-editor", igwRegion, nil, query), 400, "DryRunOperation")
	before := nativeGatewayTestExact(t, f, gateway)
	if len(before.Attachments) != 0 {
		t.Fatal("DryRun or unauthorized request created a link")
	}
	for _, tc := range []struct {
		name, action, g, v, code string
	}{
		{"foreign gateway", ec2AttachInternetGateway, stackGateway, vpc, "UnsupportedOperation"},
		{"foreign VPC", ec2AttachInternetGateway, gateway, stackVPC, "UnsupportedOperation"},
		{"missing VPC", ec2AttachInternetGateway, gateway, "vpc-00000000", "InvalidVpcID.NotFound"},
		{"missing gateway", ec2AttachInternetGateway, "igw-00000000", vpc, "InvalidInternetGatewayID.NotFound"},
		{"detached gateway", ec2DetachInternetGateway, gateway, vpc, "Gateway.NotAttached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.expect(f.call("", igwRegion, nil, nativeGatewayLinkQuery(tc.action, tc.g, tc.v)), 400, tc.code)
		})
	}
	f.expect(f.call("", igwOther, nil, nativeGatewayLinkQuery(ec2AttachInternetGateway, gateway, vpc)), 400, "InvalidInternetGatewayID.NotFound")

	nativeGatewayTestChange(t, f, "native-link-editor", ec2AttachInternetGateway, gateway, vpc)
	expected := before
	expected.Attachments = []ec2GatewayAttachment{{VPCID: vpc, State: "available"}}
	if got := nativeGatewayTestExact(t, f, gateway); !reflect.DeepEqual(got, expected) {
		t.Fatalf("attached state differs: %+v", got)
	}
	if got := f.describe(igwRegion); len(got) != 3 {
		t.Fatalf("DescribeVpcs lost parents after attach: %+v", got)
	}
	for _, pair := range [][2]string{{gateway, vpc}, {gateway, spareVPC}, {spare, vpc}} {
		f.expect(f.call("", igwRegion, nil, nativeGatewayLinkQuery(ec2AttachInternetGateway, pair[0], pair[1])), 400, "Resource.AlreadyAssociated")
	}
	f.expect(f.call("", igwRegion, nil, nativeGatewayLinkQuery(ec2DetachInternetGateway, gateway, spareVPC)), 400, "Gateway.NotAttached")
	f.expect(f.call("", igwRegion, nil, nativeGatewayLinkQuery(ec2DetachInternetGateway, stackGateway, stackVPC)), 400, "UnsupportedOperation")
	for _, del := range []url.Values{
		{"Action": {"DeleteVpc"}, "VpcId": {vpc}},
		{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {gateway}},
	} {
		f.expect(f.call("", igwRegion, nil, del), 400, "DependencyViolation")
	}
	dryDetach := nativeGatewayLinkQuery(ec2DetachInternetGateway, gateway, vpc)
	dryDetach.Set("DryRun", "true")
	put([]string{gatewayARN, vpcARN}, vpcARN)
	f.expect(f.call("native-link-editor", igwRegion, nil, dryDetach), 403, "UnauthorizedOperation")
	f.expect(f.call("native-link-editor", igwRegion, nil, nativeGatewayLinkQuery(ec2DetachInternetGateway, gateway, vpc)), 403, "UnauthorizedOperation")
	put([]string{gatewayARN, vpcARN}, "")
	f.expect(f.call("native-link-editor", igwRegion, nil, dryDetach), 400, "DryRunOperation")
	if got := nativeGatewayTestExact(t, f, gateway); !reflect.DeepEqual(got, expected) {
		t.Fatalf("refused operations changed the link: %+v", got)
	}

	nativeGatewayTestChange(t, f, "native-link-editor", ec2DetachInternetGateway, gateway, vpc)
	if got := nativeGatewayTestExact(t, f, gateway); !reflect.DeepEqual(got, before) {
		t.Fatalf("detach altered parents: %+v", got)
	}
	if got := nativeGatewayTestExact(t, f, stackGateway); !reflect.DeepEqual(got, stackBefore) {
		t.Fatalf("native operations changed the CFN link: %+v", got)
	}
	nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, gateway, spareVPC)
	nativeGatewayTestChange(t, f, "", ec2DetachInternetGateway, gateway, spareVPC)
	for _, id := range []string{gateway, spare} {
		f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {id}}), 200, "")
	}
	for _, id := range []string{vpc, spareVPC} {
		f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {id}}), 200, "")
	}
}

func TestEC2NativeInternetGatewayAttachmentIsAnEC2NetworkIdentity(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.62.0.0/16"}}).ID
	gateway := nativeGatewayTestCreate(t, f)
	bucketName := nativeInternetGatewayLinkID(gateway)
	stack, err := compileStack(igwRegion, "named-bucket-gateway-coexistence", fmt.Sprintf(`{"Resources":{
		"Reports":{"Type":"AWS::S3::Bucket","Properties":{"BucketName":%q}}
	}}`, bucketName), nil, "")
	if err != nil || len(stack.Resources) != 1 || stack.Resources[0].PhysicalID != bucketName {
		t.Fatalf("ordinary S3 fixture could not own its independently scoped name: %v %+v", err, stack.Resources)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	if got := nativeGatewayTestExact(t, f, gateway); len(got.Attachments) != 0 {
		t.Fatalf("unattached inventory differs: %+v", got)
	}
	nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, gateway, vpc)
	if got := nativeGatewayTestExact(t, f, gateway); !reflect.DeepEqual(got.Attachments, []ec2GatewayAttachment{{VPCID: vpc, State: "available"}}) {
		t.Fatalf("non-network identity prevented valid attachment: %+v", got)
	}
	if len(f.describe(igwRegion)) != 1 {
		t.Fatal("unrelated typed stack member prevented VPC readback")
	}
	nativeGatewayTestChange(t, f, "", ec2DetachInternetGateway, gateway, vpc)
	f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {gateway}}), 200, "")
	var recorded string
	if err := f.stacks.db.QueryRow("SELECT stack_id FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?", "", bucketName).Scan(&recorded); err != nil || recorded != stack.ID {
		t.Fatalf("gateway lifecycle changed the unrelated owner: %q (%v)", recorded, err)
	}
}

func TestEC2NativeInternetGatewayAttachmentInvalidFields(t *testing.T) {
	f := newEC2VPCFixture(t)
	for _, tc := range []struct{ form, code string }{
		{"Action=AttachInternetGateway&InternetGatewayId=igw-00000001", "MissingParameter"},
		{"Action=AttachInternetGateway&VpcId=vpc-00000001", "MissingParameter"},
		{"Action=AttachInternetGateway&InternetGatewayId=igw-123456789&VpcId=vpc-00000001", "InvalidInternetGatewayId.Malformed"},
		{"Action=AttachInternetGateway&InternetGatewayId=igw-00000001&VpcId=vpc-123456789", "InvalidVpcID.Malformed"},
		{"Action=DetachInternetGateway&InternetGatewayId=igw-00000001&VpcId=vpc-00000001&DryRun=maybe", "InvalidParameterValue"},
		{"Action=DetachInternetGateway&InternetGatewayId=igw-00000001&VpcId=vpc-00000001&ClientToken=unused", "UnsupportedOperation"},
		{"Action=DetachInternetGateway&InternetGatewayId=igw-00000001&VpcId=vpc-00000001&VpcId=vpc-00000002", "InvalidParameterValue"},
	} {
		form, err := url.ParseQuery(tc.form)
		if err != nil {
			t.Fatal(err)
		}
		f.expect(f.call("", igwRegion, nil, form), 400, tc.code)
	}
}

func TestEC2NativeInternetGatewayAttachmentAtomicReadback(t *testing.T) {
	for _, fault := range []string{"before-endpoint-claim", "before-endpoint-declaration", "insert-alters-parent", "delete-restores-link", "wrong-typed-link", "unsupported-native-route"} {
		t.Run(fault, func(t *testing.T) {
			f := newEC2VPCFixture(t)
			vpc := f.create("", url.Values{"CidrBlock": {"10.63.0.0/16"}}).ID
			gateway := nativeGatewayTestCreate(t, f)
			linkID := nativeInternetGatewayLinkID(gateway)
			link := nativeGatewayLinkQuery(ec2AttachInternetGateway, gateway, vpc)
			unlink := nativeGatewayLinkQuery(ec2DetachInternetGateway, gateway, vpc)
			db := f.stacks.db
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			rows := func(want int) {
				t.Helper()
				var got int
				if err := db.QueryRow("SELECT count(*) FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, linkID).Scan(&got); err != nil || got != want {
					t.Fatalf("want durable link rows %d, got %d (%v)", want, got, err)
				}
			}
			switch fault {
			case "before-endpoint-claim":
				stack, _, _, _ := internetGatewayTestStack(t, f)
				exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(igwRegion, ec2RTVPC), ec2RTVPC, vpc, stack.ID)
				f.expect(f.call("", igwRegion, nil, link), 500, "InternalError")
				rows(0)
			case "before-endpoint-declaration":
				stack, _, _, cfnVPC := internetGatewayTestStack(t, f)
				exec("UPDATE aws_cfn_stacks SET document=replace(document,?,?) WHERE id=?", cfnVPC, vpc, stack.ID)
				f.expect(f.call("", igwRegion, nil, link), 500, "InternalError")
				rows(0)
			case "insert-alters-parent":
				exec(`CREATE TRIGGER break_link AFTER INSERT ON aws_ec2_network_resources WHEN NEW.id GLOB 'igwattach-*'
					BEGIN DELETE FROM aws_ec2_network_resources WHERE region=NEW.region AND id=json_extract(NEW.document,'$.VPCID'); END`)
				f.expect(f.call("", igwRegion, nil, link), 500, "InternalError")
				rows(0)
				if got := f.describe(igwRegion); len(got) != 1 || got[0].ID != vpc {
					t.Fatalf("rollback lost the original VPC: %+v", got)
				}
			case "delete-restores-link":
				nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, gateway, vpc)
				exec(`CREATE TRIGGER restore_link AFTER DELETE ON aws_ec2_network_resources WHEN OLD.id GLOB 'igwattach-*'
					BEGIN INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision); END`)
				f.expect(f.call("", igwRegion, nil, unlink), 500, "InternalError")
				rows(1)
				if got := nativeGatewayTestExact(t, f, gateway); len(got.Attachments) != 1 || got.Attachments[0].VPCID != vpc {
					t.Fatalf("failed delete removed link: %+v", got)
				}
				exec("DROP TRIGGER restore_link")
				nativeGatewayTestChange(t, f, "", ec2DetachInternetGateway, gateway, vpc)
				rows(0)
			case "wrong-typed-link", "unsupported-native-route":
				nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, gateway, vpc)
				if fault == "wrong-typed-link" {
					exec(`UPDATE aws_ec2_network_resources
						SET kind=?,document=json_set(document,'$.Kind',?,'$.VPCID','') WHERE region=? AND id=?`, ec2RTVPC, ec2RTVPC, igwRegion, linkID)
					f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 500, "InternalError")
				} else {
					f.writeNetwork(EC2NetworkResource{ID: "route-00000000", Kind: ec2Route, Region: igwRegion, StackID: ec2NativeOwner,
						GatewayID: gateway, VPCID: vpc, RouteEnabled: true, Revision: 1})
				}
				f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2DescribeInternetGateway}, "InternetGatewayId.1": {gateway}}), 500, "InternalError")
				f.expect(f.call("", igwRegion, nil, unlink), 500, "InternalError")
				rows(1)
			}
		})
	}
}

func TestEC2NativeInternetGatewayAttachmentSerializesSharedVPC(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.65.0.0/16"}}).ID
	a, b := nativeGatewayTestCreate(t, f), nativeGatewayTestCreate(t, f)
	start, done := make(chan struct{}), make(chan error, 2)
	for _, id := range []string{a, b} {
		go func() {
			<-start
			done <- f.stacks.changeNativeInternetGatewayAttachment(t.Context(), igwRegion, id, vpc, true)
		}()
	}
	close(start)
	accepted := 0
	for range 2 {
		err := <-done
		if err == nil {
			accepted++
			continue
		}
		var failure *ec2Failure
		if !errors.As(err, &failure) || failure.code != "Resource.AlreadyAssociated" {
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d links to one VPC", accepted)
	}
	if got := len(nativeGatewayTestExact(t, f, a).Attachments) + len(nativeGatewayTestExact(t, f, b).Attachments); got != 1 {
		t.Fatalf("expected one saved link, got %d", got)
	}
}

func TestEC2NativeInternetGatewayAttachmentRespectsPrivateTopology(t *testing.T) {
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.66.0.0/16"}}).ID
	gateway := nativeGatewayTestCreate(t, f)
	refs := catalog.RDSPrivatePostgresRefs{Region: igwRegion}
	for i, az := range []string{"us-east-1a", "us-east-1b"} {
		raw := f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc},
			"CidrBlock": {fmt.Sprintf("10.66.%d.0/24", i+1)}, "AvailabilityZone": {az}}), 200, "")
		var out struct {
			ID string `xml:"subnet>subnetId"`
		}
		if err := xml.Unmarshal([]byte(raw), &out); err != nil || out.ID == "" {
			t.Fatalf("subnet: %v", err)
		}
		refs.SubnetIDs = append(refs.SubnetIDs, out.ID)
	}
	raw := f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"CreateSecurityGroup"}, "VpcId": {vpc},
		"GroupName": {"isolation-check"}, "GroupDescription": {"local topology admission"}}), 200, "")
	var out struct {
		ID string `xml:"groupId"`
	}
	if err := xml.Unmarshal([]byte(raw), &out); err != nil || out.ID == "" {
		t.Fatalf("security group: %v", err)
	}
	refs.SecurityGroupIDs = []string{out.ID}
	current := func() error {
		t.Helper()
		ctx := t.Context()
		tx, err := f.stacks.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return ValidateRDSPrivatePostgresTopology(ctx, tx, refs)
	}
	if err := current(); err != nil {
		t.Fatalf("private baseline refused: %v", err)
	}
	nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, gateway, vpc)
	if err := current(); err == nil || !strings.Contains(err.Error(), "in one isolated VPC") {
		t.Fatalf("attached VPC is not this private topology: %v", err)
	}
	nativeGatewayTestChange(t, f, "", ec2DetachInternetGateway, gateway, vpc)
	if err := current(); err != nil {
		t.Fatalf("detach did not restore private topology eligibility: %v", err)
	}
}
