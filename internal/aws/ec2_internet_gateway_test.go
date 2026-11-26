package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const igwRegion = "us-east-1"
const igwOther = "ap-northeast-1"
const igwXML = "http://ec2.amazonaws.com/doc/2016-11-15/"

func internetGatewayTestStack(t *testing.T, f ec2VPCFixture) (StackRecord, string, string, string) {
	t.Helper()
	stack, err := compileStack(igwRegion, "igw-contract", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.51.0.0/16"}},
		"Gateway":{"Type":"AWS::EC2::InternetGateway","Properties":{"Tags":[{"Key":"Name","Value":"stack gateway"}]}},
		"Detached":{"Type":"AWS::EC2::InternetGateway"},
		"Attach":{"Type":"AWS::EC2::VPCGatewayAttachment","Properties":{"VpcId":{"Ref":"Vpc"},"InternetGatewayId":{"Ref":"Gateway"}}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, record := range stack.Resources {
		ids[record.LogicalID] = record.PhysicalID
	}
	return stack, ids["Gateway"], ids["Detached"], ids["Vpc"]
}

func internetGatewayTestXML(t *testing.T, raw, action string) ec2InternetGatewayResponse {
	t.Helper()
	var out ec2InternetGatewayResponse
	if err := xml.Unmarshal([]byte(raw), &out); err != nil || out.XMLName.Space != igwXML ||
		out.XMLName.Local != action+"Response" || out.RequestID == "" {
		t.Fatalf("unexpected %s response: %v %s", action, err, raw)
	}
	return out
}

func TestEC2InternetGatewayWireAndAuthority(t *testing.T) {
	f := newEC2VPCFixture(t)
	_, attached, detached, vpc := internetGatewayTestStack(t, f)
	_ = f.create("", url.Values{"CidrBlock": {"10.45.0.0/16"}})
	actions := func(as, region, action string, fields url.Values) ec2InternetGatewayResponse {
		t.Helper()
		if fields == nil {
			fields = url.Values{}
		}
		fields.Set("Action", action)
		return internetGatewayTestXML(t, f.expect(f.call(as, region, nil, fields), 200, ""), action)
	}
	inventory := func(as, region string) map[string]ec2InternetGateway {
		t.Helper()
		out := actions(as, region, ec2DescribeInternetGateway, nil)
		if out.Gateways == nil || out.Gateway != nil || out.Return != nil {
			t.Fatalf("invalid DescribeInternetGateways shape: %+v", out)
		}
		byID := map[string]ec2InternetGateway{}
		var prev string
		for _, gw := range out.Gateways.Items {
			if gw.ID <= prev {
				t.Fatalf("unordered or duplicate gateways: %+v", out.Gateways)
			}
			byID[gw.ID] = gw
			prev = gw.ID
		}
		return byID
	}
	before := inventory("", igwRegion)
	if len(before) != 2 || before[detached].ManagedBy != ec2ManagedByCloudFormation || len(before[detached].Attachments) != 0 ||
		before[attached].ManagedBy != ec2ManagedByCloudFormation || before[attached].OwnerID != "000000000000" ||
		!reflect.DeepEqual(before[attached].Attachments, []ec2GatewayAttachment{{vpc, "available"}}) ||
		!reflect.DeepEqual(before[attached].Tags, []ec2Tag{{"Name", "stack gateway"}}) {
		t.Fatalf("bad stack readback: %+v", before)
	}
	policy := func(rules string) {
		t.Helper()
		doc, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + rules + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set("gateway-builder", doc); err != nil {
			t.Fatal(err)
		}
	}
	regional := networkARN(igwRegion, ec2InternetGatewayARNKind, "*")
	canRead := `{"Effect":"Allow","Action":"ec2:DescribeInternetGateways","Resource":"*"}`
	canCreate := fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateInternetGateway","Resource":%q}`, regional)
	canTag := fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, regional)
	tagged := url.Values{"Action": {ec2CreateInternetGateway}, "DryRun": {"false"},
		"TagSpecification.1.ResourceType": {"internet-gateway"}, "TagSpecification.1.Tag.1.Key": {"Name"},
		"TagSpecification.1.Tag.1.Value": {"練習 & <local>"}}
	policy(canCreate + "," + canRead)
	f.expect(f.call("gateway-builder", igwRegion, nil, tagged), 403, "UnauthorizedOperation")
	exactGenerated := networkARN(igwRegion, ec2InternetGatewayARNKind, "igw-"+strings.Repeat("?", 17))
	policy(canCreate + "," + canRead + "," + canTag +
		fmt.Sprintf(`,{"Effect":"Deny","Action":"ec2:CreateInternetGateway","Resource":%q}`, exactGenerated))
	f.expect(f.call("gateway-builder", igwRegion, nil, tagged), 403, "UnauthorizedOperation")
	policy(canCreate + "," + canRead + "," + canTag)
	scope := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: igwRegion, instance: "i-00000000"})
	f.expect(f.call("gateway-builder", igwRegion, scope, tagged), 403, "UnauthorizedOperation")
	f.expect(f.call("gateway-builder", igwRegion, scope, url.Values{"Action": {ec2DescribeInternetGateway}}), 403, "UnauthorizedOperation")
	if got := inventory("gateway-builder", igwRegion); !reflect.DeepEqual(got, before) {
		t.Fatalf("refusals changed inventory: %+v", got)
	}

	created := actions("gateway-builder", igwRegion, ec2CreateInternetGateway, tagged).Gateway
	if created == nil || !ec2InternetGatewayID.MatchString(created.ID) || created.ManagedBy != ec2ManagedByEC2 ||
		created.OwnerID != "000000000000" || len(created.Attachments) != 0 ||
		!reflect.DeepEqual(created.Tags, []ec2Tag{{"Name", "練習 & <local>"}}) {
		t.Fatalf("invalid CreateInternetGateway protocol: %+v", created)
	}
	after := inventory("", igwRegion)
	want := maps.Clone(before)
	want[created.ID] = *created
	if !reflect.DeepEqual(after, want) || len(f.describe(igwRegion)) != 2 {
		t.Fatalf("native/CFN readback or VPC discovery changed: %+v", after)
	}
	exact := actions("", igwRegion, ec2DescribeInternetGateway, url.Values{"InternetGatewayId.1": {created.ID}})
	if len(exact.Gateways.Items) != 1 || !reflect.DeepEqual(exact.Gateways.Items[0], *created) || len(inventory("", igwOther)) != 0 {
		t.Fatalf("exact current region selector failed: %+v", exact)
	}

	del := func(id string) url.Values {
		return url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {id}}
	}
	f.expect(f.call("gateway-builder", igwRegion, nil, del(created.ID)), 403, "UnauthorizedOperation")
	f.expect(f.call("", igwOther, nil, del(created.ID)), 400, "InvalidInternetGatewayID.NotFound")
	for _, cf := range []string{attached, detached} {
		f.expect(f.call("", igwRegion, nil, del(cf)), 400, "UnsupportedOperation")
	}
	for _, action := range []string{"AttachInternetGateway", "DetachInternetGateway"} {
		f.expect(f.call("", igwRegion, nil, url.Values{"Action": {action}, "InternetGatewayId": {created.ID}, "VpcId": {vpc}}), 400, "UnsupportedOperation")
	}
	if !reflect.DeepEqual(inventory("", igwRegion), after) {
		t.Fatal("refused action changed the live inventory")
	}
	deleteARN := networkARN(igwRegion, ec2InternetGatewayARNKind, created.ID)
	policy(fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:DeleteInternetGateway","Resource":%q}`, deleteARN))
	f.expect(f.call("gateway-builder", igwRegion, nil, url.Values{"Action": {ec2DescribeInternetGateway}}), 403, "UnauthorizedOperation")
	reply := actions("gateway-builder", igwRegion, ec2DeleteInternetGateway, del(created.ID))
	if reply.Return == nil || !*reply.Return || !reflect.DeepEqual(before, inventory("", igwRegion)) {
		t.Fatalf("cleanup did not preserve CFN gateways: %+v", reply)
	}
	f.expect(f.call("", igwRegion, nil, del(created.ID)), 400, "InvalidInternetGatewayID.NotFound")
}

func TestEC2InternetGatewayAdmission(t *testing.T) {
	f := newEC2VPCFixture(t)
	for _, tc := range []struct {
		name, input, code string
	}{
		{"dry run create", "Action=CreateInternetGateway&DryRun=true", "UnsupportedOperation"},
		{"dry run describe", "Action=DescribeInternetGateways&DryRun=true", "UnsupportedOperation"},
		{"arbitrary selector", "Action=DescribeInternetGateways&Filter.1.Name=owner-id&Filter.1.Value.1=000000000000", "UnsupportedOperation"},
		{"pagination", "Action=DescribeInternetGateways&MaxResults=5", "UnsupportedOperation"},
		{"gap in selector", "Action=DescribeInternetGateways&InternetGatewayId.2=igw-00000001", "InvalidParameterValue"},
		{"duplicate", "Action=DescribeInternetGateways&InternetGatewayId.1=igw-00000001&InternetGatewayId.2=igw-00000001", "InvalidParameterValue"},
		{"more than eight", "Action=DescribeInternetGateways&InternetGatewayId.1=igw-00000001&InternetGatewayId.2=igw-00000002&InternetGatewayId.3=igw-00000003&InternetGatewayId.4=igw-00000004&InternetGatewayId.5=igw-00000005&InternetGatewayId.6=igw-00000006&InternetGatewayId.7=igw-00000007&InternetGatewayId.8=igw-00000008&InternetGatewayId.9=igw-00000009", "InvalidParameterValue"},
		{"missing selector", "Action=DescribeInternetGateways&InternetGatewayId.1=igw-00000000", "InvalidInternetGatewayID.NotFound"},
		{"unsupported attaching member", "Action=CreateInternetGateway&VpcId=vpc-00000001", "UnsupportedOperation"},
		{"wrong resource type", "Action=CreateInternetGateway&TagSpecification.1.ResourceType=vpc", "InvalidParameterValue"},
		{"no type", "Action=CreateInternetGateway&TagSpecification.1.Tag.1.Key=Name", "InvalidParameterValue"},
		{"gap in tags", "Action=CreateInternetGateway&TagSpecification.1.ResourceType=internet-gateway&TagSpecification.1.Tag.2.Key=Name", "InvalidParameterValue"},
		{"invalid tag", "Action=CreateInternetGateway&TagSpecification.1.ResourceType=internet-gateway&TagSpecification.1.Tag.1.Key=aws%3AName", "InvalidParameterValue"},
		{"invalid XML", "Action=CreateInternetGateway&TagSpecification.1.ResourceType=internet-gateway&TagSpecification.1.Tag.1.Key=Name&TagSpecification.1.Tag.1.Value=%00", "InvalidParameterValue"},
		{"duplicate parameter", "Action=DeleteInternetGateway&InternetGatewayId=igw-00000001&InternetGatewayId=igw-00000002", "InvalidParameterValue"},
		{"missing ID", "Action=DeleteInternetGateway", "MissingParameter"},
		{"bad ID", "Action=DeleteInternetGateway&InternetGatewayId=igw-000000001", "InvalidInternetGatewayId.Malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form, err := url.ParseQuery(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			f.expect(f.call("", igwRegion, nil, form), 400, tc.code)
		})
	}
	raw := f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2DescribeInternetGateway}}), 200, "")
	if out := internetGatewayTestXML(t, raw, ec2DescribeInternetGateway); out.Gateways == nil || len(out.Gateways.Items) != 0 {
		t.Fatalf("defensive admissions left state: %s", raw)
	}
}

func TestEC2InternetGatewayFaultAtomicity(t *testing.T) {
	for _, fault := range []string{"alter-insert", "ignore-delete", "restore-delete", "stack-claim", "stack-declaration", "attachment", "route", "route-typed-field", "route-malformed"} {
		t.Run(fault, func(t *testing.T) {
			f := newEC2VPCFixture(t)
			db := f.stacks.db
			exec := func(command string, args ...any) {
				t.Helper()
				if _, err := db.Exec(command, args...); err != nil {
					t.Fatal(err)
				}
			}
			create := url.Values{"Action": {ec2CreateInternetGateway}}
			list := url.Values{"Action": {ec2DescribeInternetGateway}}
			wantRows := func(want int) {
				t.Helper()
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM aws_ec2_network_resources WHERE region=? AND kind=? AND stack_id=?`, igwRegion, ec2RTGateway, ec2NativeOwner).
					Scan(&n); err != nil || n != want {
					t.Fatalf("unexpected durable rows: got %d, want %d (%v)", n, want, err)
				}
			}
			if fault == "alter-insert" {
				exec(`CREATE TRIGGER alter_igw AFTER INSERT ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::InternetGateway'
					BEGIN UPDATE aws_ec2_network_resources SET document=json_set(NEW.document,'$.Tags.Name','changed')
					WHERE region=NEW.region AND id=NEW.id; END`)
				f.expect(f.call("", igwRegion, nil, create), 500, "InternalError")
				wantRows(0)
				return
			}
			g := internetGatewayTestXML(t, f.expect(f.call("", igwRegion, nil, create), 200, ""), ec2CreateInternetGateway).Gateway
			del := url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {g.ID}}
			switch fault {
			case "ignore-delete":
				exec(`CREATE TRIGGER ignore_igw BEFORE DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::InternetGateway'
					BEGIN SELECT RAISE(IGNORE); END`)
			case "restore-delete":
				exec(`CREATE TRIGGER restore_igw AFTER DELETE ON aws_ec2_network_resources WHEN OLD.kind='AWS::EC2::InternetGateway'
					BEGIN INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision); END`)
			case "stack-claim":
				stack, _, _, _ := internetGatewayTestStack(t, f)
				exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(igwRegion, ec2RTGateway), ec2RTGateway, g.ID, stack.ID)
				f.expect(f.call("", igwRegion, nil, list), 500, "InternalError")
			case "stack-declaration":
				stack, _, oldID, _ := internetGatewayTestStack(t, f)
				// A disowned stack declaration is still a conflict for native
				// writes, not authority to adopt or delete the physical ID.
				exec(`UPDATE aws_cfn_stacks SET document=replace(document,?,?) WHERE id=?`, oldID, g.ID, stack.ID)
				f.expect(f.call("", igwRegion, nil, list), 500, "InternalError")
			case "attachment", "route", "route-typed-field", "route-malformed":
				// Model a local database fault, not an exposed operation.
				kind := ec2RTAttachment
				if fault != "attachment" {
					kind = ec2Route
				}
				f.writeNetwork(EC2NetworkResource{ID: "local-dependency", Kind: kind, Region: igwRegion, StackID: ec2NativeOwner, GatewayID: g.ID, Revision: 1})
				if fault == "route-typed-field" {
					exec(`UPDATE aws_ec2_network_resources
						SET document=json_set(json_remove(document,'$.GatewayID'),'$.gatewayId',?) WHERE id='local-dependency'`, g.ID)
				}
				if fault == "route-malformed" {
					exec(`UPDATE aws_ec2_network_resources
						SET document=json_set(document,'$.GatewayID',json('{}')) WHERE id='local-dependency'`)
					f.expect(f.call("", igwRegion, nil, del), 500, "InternalError")
					wantRows(1)
					return
				}
				f.expect(f.call("", igwRegion, nil, list), 500, "InternalError")
				f.expect(f.call("", igwRegion, nil, del), 400, "DependencyViolation")
				wantRows(1)
				return
			}
			f.expect(f.call("", igwRegion, nil, del), 500, "InternalError")
			wantRows(1)
		})
	}
}

func TestEC2InternetGatewayStackReadFailsClosed(t *testing.T) {
	for _, fault := range []string{"missing claim", "missing row", "missing both", "tag drift", "attachment drift"} {
		t.Run(fault, func(t *testing.T) {
			f := newEC2VPCFixture(t)
			stack, g, detached, _ := internetGatewayTestStack(t, f)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := f.stacks.db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "missing both" || fault == "missing claim" {
				exec("DELETE FROM aws_cfn_ownership WHERE stack_id=? AND physical_id=?", stack.ID, g)
			}
			if fault == "missing both" || fault == "missing row" {
				exec("DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, g)
			}
			if fault == "tag drift" {
				exec(`UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Tags.Name','drift') WHERE region=? AND id=?`, igwRegion, g)
			}
			if fault == "attachment drift" {
				exec(`UPDATE aws_ec2_network_resources SET document=json_set(document,'$.GatewayID',?)
					WHERE region=? AND stack_id=? AND kind=?`, detached, igwRegion, stack.ID, ec2RTAttachment)
			}
			for _, query := range []url.Values{{"Action": {ec2DescribeInternetGateway}}, {"Action": {ec2DescribeInternetGateway}, "InternetGatewayId.1": {detached}}} {
				f.expect(f.call("", igwRegion, nil, query), 500, "InternalError")
			}
		})
	}
}

func TestEC2InternetGatewayReadRejectsRouteOnlyDrift(t *testing.T) {
	f := newEC2VPCFixture(t)
	// The typed native route alone, even without visible gateways, is an
	// inconsistent local state. No empty success is allowed.
	f.writeNetwork(EC2NetworkResource{ID: "local-route-only", Kind: ec2Route, Region: igwRegion, StackID: ec2NativeOwner, GatewayID: "igw-00000000", Revision: 1})
	for _, query := range []url.Values{{"Action": {ec2DescribeInternetGateway}}, {"Action": {ec2DescribeInternetGateway}, "InternetGatewayId.1": {"igw-00000001"}}} {
		f.expect(f.call("", igwRegion, nil, query), 500, "InternalError")
		if query.Has("InternetGatewayId.1") {
			f.expect(f.call("", igwOther, nil, query), 400, "InvalidInternetGatewayID.NotFound")
		} else {
			f.expect(f.call("", igwOther, nil, query), 200, "")
		}
	}
}
