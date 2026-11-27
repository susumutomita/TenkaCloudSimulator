package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func nativeDefaultRouteTestTable(t *testing.T, f ec2VPCFixture, vpc string) string {
	t.Helper()
	var r ec2NativeRouteTableMutationResponse
	raw := f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"CreateRouteTable"}, "VpcId": {vpc}}), 200, "")
	if err := xml.Unmarshal([]byte(raw), &r); err != nil || r.Table == nil || r.Table.VPC != vpc {
		t.Fatalf("native route table fixture: %s (%v)", raw, err)
	}
	return r.Table.ID
}

func defaultRouteQuery(action, table, cidr, gateway string) url.Values {
	q := url.Values{"Action": {action}, "RouteTableId": {table}, "DestinationCidrBlock": {cidr}}
	if gateway != "" {
		q.Set("GatewayId", gateway)
	}
	return q
}

func expectNativeDefaultRoute(t *testing.T, f ec2VPCFixture, principal, region, action, table, destination, gateway string) {
	t.Helper()
	body := f.expect(f.call(principal, region, nil, defaultRouteQuery(action, table, destination, gateway)), 200, "")
	var r ec2NativeDefaultRouteResponse
	if err := xml.Unmarshal([]byte(body), &r); err != nil || r.XMLName.Local != action+"Response" ||
		r.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || r.RequestID == "" || !r.Return ||
		!strings.Contains(body, "<return>true</return>") {
		t.Fatalf("incorrect route acknowledgment: %s (%v)", body, err)
	}
}

func expectExactNativeDefaultRoute(t *testing.T, f ec2VPCFixture, selection url.Values, table, vpc, gateway string) {
	t.Helper()
	selection.Set("Action", "DescribeRouteTables")
	raw := f.expect(f.call("", igwRegion, nil, selection), 200, "")
	var r ec2DescribeRouteTablesResponse
	if err := xml.Unmarshal([]byte(raw), &r); err != nil || r.RequestID == "" || len(r.Tables) != 1 ||
		r.Tables[0].ID != table || r.Tables[0].VPC != vpc {
		t.Fatalf("incorrect exact route table readback: %s (%v)", raw, err)
	}
	var want []ec2RouteRead
	if gateway != "" {
		want = []ec2RouteRead{{CIDR: ec2NativeDefaultCIDR, Origin: "CreateRoute", State: "active", Gateway: gateway}}
	}
	want = append(want, expectsNativeLocalRoute(t, f, igwRegion, vpc))
	if !slices.Equal(r.Tables[0].Routes, want) {
		t.Fatalf("want route %+v, got %+v", want, r.Tables[0].Routes)
	}
}

func TestEC2NativeDefaultRouteWireAndScopedAuthority(t *testing.T) {
	f := newEC2VPCFixture(t)
	_, foreignGateway, _, foreignVPC := internetGatewayTestStack(t, f)
	vpc := f.create("", url.Values{"CidrBlock": {"10.83.0.0/16"}}).ID
	secondVPC := f.create("", url.Values{"CidrBlock": {"10.84.0.0/16"}}).ID
	table, alternate := nativeDefaultRouteTestTable(t, f, vpc), nativeDefaultRouteTestTable(t, f, secondVPC)
	gateway, other := nativeGatewayTestCreate(t, f), nativeGatewayTestCreate(t, f)
	nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, other, secondVPC)

	// A legal non-network stack may use the same literal internal identifier.
	bucket := nativeDefaultRouteID(table)
	stack, err := compileStack(igwRegion, "route-name-coexistence", fmt.Sprintf(`{"Resources":{
		"Reports":{"Type":"AWS::S3::Bucket","Properties":{"BucketName":%q}}
	}}`, bucket), nil, "")
	if err != nil || len(stack.Resources) != 1 || stack.Resources[0].PhysicalID != bucket {
		t.Fatalf("invalid S3 coexistence fixture: %v %+v", err, stack)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	legacyTable, err := compileStack(igwRegion, "route-cfn-refusal", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.85.0.0/16"}},
		"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"}}}
	}}`, nil, "")
	if err != nil || f.stacks.Create(legacyTable) != nil {
		t.Fatalf("invalid CFN table fixture: %v", err)
	}
	var foreignTable string
	for _, member := range legacyTable.Resources {
		if member.Type == ec2RT {
			foreignTable = member.PhysicalID
		}
	}

	const editor = "native-default-route-editor"
	setPolicy := func(resource, denied string) {
		t.Helper()
		body := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
			{"Effect":"Allow","Action":["ec2:CreateRoute","ec2:DeleteRoute"],"Resource":%q},
			{"Effect":"Allow","Action":"ec2:DescribeRouteTables","Resource":"*"}%s]}`, resource, denied)
		p, err := core.ParsePolicy([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(editor, p); err != nil {
			t.Fatal(err)
		}
	}
	tableARN := networkARN(igwRegion, "route-table", table)
	create := defaultRouteQuery(ec2CreateNativeDefaultRoute, table, ec2NativeDefaultCIDR, gateway)
	erase := defaultRouteQuery(ec2DeleteNativeDefaultRoute, table, ec2NativeDefaultCIDR, "")
	exact := url.Values{"RouteTableId.1": {table}}
	ctx := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: igwRegion, instance: "i-test"})
	setPolicy(networkARN(igwRegion, "route-table", alternate), "")
	f.expect(f.call(editor, igwRegion, nil, create), 403, "UnauthorizedOperation")
	create.Set("DryRun", "true")
	f.expect(f.call(editor, igwRegion, nil, create), 403, "UnauthorizedOperation")
	setPolicy(tableARN, "")
	f.expect(f.call(editor, igwRegion, ctx, create), 403, "UnauthorizedOperation")
	// This is permission-only: a detached target isn't resolved or attached.
	f.expect(f.call(editor, igwRegion, nil, create), 400, "DryRunOperation")
	create.Del("DryRun")
	f.expect(f.call(editor, igwRegion, nil, create), 400, "Gateway.NotAttached")
	expectExactNativeDefaultRoute(t, f, exact, table, vpc, "")

	for _, tc := range []struct{ table, gateway, region, code string }{
		{table, other, igwRegion, "Gateway.NotAttached"},
		{table, foreignGateway, igwRegion, "UnsupportedOperation"},
		{foreignTable, gateway, igwRegion, "UnsupportedOperation"},
		{table, "igw-00000000", igwRegion, "InvalidInternetGatewayID.NotFound"},
		{table, gateway, igwOther, "InvalidRouteTableID.NotFound"},
	} {
		q := defaultRouteQuery(ec2CreateNativeDefaultRoute, tc.table, ec2NativeDefaultCIDR, tc.gateway)
		f.expect(f.call("", tc.region, nil, q), 400, tc.code)
	}
	nativeGatewayTestChange(t, f, "", ec2AttachInternetGateway, gateway, vpc)
	expectNativeDefaultRoute(t, f, editor, igwRegion, ec2CreateNativeDefaultRoute, table, "192.0.2.18/0", gateway)
	f.expect(f.call(editor, igwRegion, nil, create), 400, "RouteAlreadyExists")
	expectExactNativeDefaultRoute(t, f, exact, table, vpc, gateway)
	expectExactNativeDefaultRoute(t, f, url.Values{"RouteTableId.1": {alternate}}, alternate, secondVPC, "")
	if v := nativeGatewayTestExact(t, f, gateway); len(v.Attachments) != 1 || v.Attachments[0].VPCID != vpc || len(f.describe(igwRegion)) != 4 {
		t.Fatal("coherent route invalidated another native projection")
	}
	if v := nativeGatewayTestExact(t, f, foreignGateway); len(v.Attachments) != 1 || v.Attachments[0].VPCID != foreignVPC {
		t.Fatal("route changed unrelated CFN relationships")
	}
	for _, q := range []url.Values{
		{"Action": {"DeleteRouteTable"}, "RouteTableId": {table}},
		nativeGatewayLinkQuery(ec2DetachInternetGateway, gateway, vpc),
	} {
		f.expect(f.call("", igwRegion, nil, q), 400, "DependencyViolation")
	}
	erase.Set("DryRun", "true")
	setPolicy(tableARN, fmt.Sprintf(`,{"Effect":"Deny","Action":"ec2:DeleteRoute","Resource":%q}`, tableARN))
	f.expect(f.call(editor, igwRegion, nil, erase), 403, "UnauthorizedOperation")
	setPolicy(tableARN, "")
	f.expect(f.call(editor, igwRegion, nil, erase), 400, "DryRunOperation")
	expectExactNativeDefaultRoute(t, f, exact, table, vpc, gateway)
	expectNativeDefaultRoute(t, f, editor, igwRegion, ec2DeleteNativeDefaultRoute, table, ec2NativeDefaultCIDR, "")
	erase.Del("DryRun")
	f.expect(f.call(editor, igwRegion, nil, erase), 400, "InvalidRoute.NotFound")
	expectExactNativeDefaultRoute(t, f, exact, table, vpc, "")
	nativeGatewayTestChange(t, f, "", ec2DetachInternetGateway, gateway, vpc)
	f.expect(f.call("", igwRegion, nil, url.Values{"Action": {ec2DeleteInternetGateway}, "InternetGatewayId": {gateway}}), 200, "")
	f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {table}}), 200, "")
	f.expect(f.call("", igwRegion, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 200, "")
	f.expect(f.call("", igwRegion, nil, exact), 400, "InvalidRouteTableID.NotFound")
	var owner string
	if err := f.stacks.db.QueryRow("SELECT stack_id FROM aws_cfn_ownership WHERE namespace='' AND physical_id=?", bucket).Scan(&owner); err != nil || owner != stack.ID {
		t.Fatalf("route changed a legal non-network name: %q (%v)", owner, err)
	}
}

func TestEC2NativeDefaultRouteRefusesUnsupportedQueryShapes(t *testing.T) {
	f := newEC2VPCFixture(t)
	for _, tc := range []struct{ form, code string }{
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0", "MissingParameter"},
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&GatewayId=local", "UnsupportedOperation"},
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&GatewayId=igw-invalid", "InvalidInternetGatewayId.Malformed"},
		{"Action=CreateRoute&RouteTableId=wrong&DestinationCidrBlock=0.0.0.0/0&GatewayId=igw-01234567", "InvalidRouteTableId.Malformed"},
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=::/0&GatewayId=igw-01234567", "InvalidParameterValue"},
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=192.0.2.0/33&GatewayId=igw-01234567", "InvalidParameterValue"},
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationIpv6CidrBlock=::/0&GatewayId=igw-01234567", "UnsupportedOperation"},
		{"Action=CreateRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&GatewayId=igw-01234567&NatGatewayId=nat-01234567", "UnsupportedOperation"},
		{"Action=DeleteRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&DryRun=maybe", "InvalidParameterValue"},
		{"Action=DeleteRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&DestinationCidrBlock=0.0.0.0/0", "InvalidParameterValue"},
		{"Action=DeleteRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&GatewayId=igw-01234567", "UnsupportedOperation"},
		{"Action=DeleteRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=192.0.2.18/0", "InvalidRoute.NotFound"},
		{"Action=ReplaceRoute&RouteTableId=rtb-01234567&DestinationCidrBlock=0.0.0.0/0&GatewayId=igw-01234567", "UnsupportedOperation"},
		{"Action=DescribeRouteTables&RouteTableId.1=rtb-01234567&RouteTableId.2=rtb-01234568", "UnsupportedOperation"},
		{"Action=DescribeRouteTables&RouteTableId.1=bad", "InvalidRouteTableId.Malformed"},
	} {
		q, err := url.ParseQuery(tc.form)
		if err != nil {
			t.Fatal(err)
		}
		f.expect(f.call("", igwRegion, nil, q), 400, tc.code)
	}
}

func TestEC2NativeDefaultRouteInnocentStorageDriftFailsClosed(t *testing.T) {
	for _, fault := range []string{"legacy JSON shape", "missing attachment", "foreign native ID claim", "declared foreign network ID", "wrong kind", "foreign row owner"} {
		t.Run(fault, func(t *testing.T) {
			x := newIGWLinkFaultFixture(t, true)
			table := nativeDefaultRouteTestTable(t, x.f, x.vpc)
			route := nativeDefaultRouteID(table)
			expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, table, ec2NativeDefaultCIDR, x.gateway)
			var restore func()
			switch fault {
			case "legacy JSON shape":
				restore = x.replaceDocument(t, route, `json_set(json_remove(document,'$.RouteTableID'),'$.routetableid',json_extract(document,'$.RouteTableID'))`)
			case "missing attachment":
				var prior EC2NetworkResource
				tx, err := x.f.stacks.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				prior, err = networkRead(t.Context(), tx, igwRegion, x.link)
				_ = tx.Rollback()
				if err != nil {
					t.Fatal(err)
				}
				x.exec(t, "DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", igwRegion, x.link)
				restore = func() { x.f.writeNetwork(prior) }
			case "foreign native ID claim":
				stack, _, _, _ := internetGatewayTestStack(t, x.f)
				ns := stackResourceNamespace(igwRegion, ec2Route)
				x.exec(t, "INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", ns, ec2Route, route, stack.ID)
				restore = func() { x.exec(t, "DELETE FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?", ns, route) }
			case "declared foreign network ID":
				stack, _, _, _ := internetGatewayTestStack(t, x.f)
				var old string
				if err := x.f.stacks.db.QueryRow("SELECT document FROM aws_cfn_stacks WHERE id=?", stack.ID).Scan(&old); err != nil {
					t.Fatal(err)
				}
				for _, r := range stack.Resources {
					if r.Type == ec2RTAttachment {
						x.exec(t, "UPDATE aws_cfn_stacks SET document=replace(document,?,?) WHERE id=?", r.PhysicalID, route, stack.ID)
					}
				}
				restore = func() { x.exec(t, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", old, stack.ID) }
			case "wrong kind":
				rewrite := func(kind string) {
					x.exec(t, "UPDATE aws_ec2_network_resources SET kind=?,document=json_set(document,'$.Kind',?) WHERE region=? AND id=?",
						kind, kind, igwRegion, route)
				}
				rewrite(ec2RT)
				restore = func() { rewrite(ec2Route) }
			case "foreign row owner":
				stack, _, _, _ := internetGatewayTestStack(t, x.f)
				rewrite := func(owner string) {
					x.exec(t, "UPDATE aws_ec2_network_resources SET stack_id=?,document=json_set(document,'$.StackID',?) WHERE region=? AND id=?",
						owner, owner, igwRegion, route)
				}
				rewrite(stack.ID)
				restore = func() { rewrite(ec2NativeOwner) }
			}
			image := x.image(t)
			for _, q := range []url.Values{
				{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {table}},
				{"Action": {ec2DescribeInternetGateway}, "InternetGatewayId.1": {x.gateway}},
				defaultRouteQuery(ec2DeleteNativeDefaultRoute, table, ec2NativeDefaultCIDR, ""),
				{"Action": {"DeleteRouteTable"}, "RouteTableId": {table}},
				nativeGatewayLinkQuery(ec2DetachInternetGateway, x.gateway, x.vpc),
			} {
				raw := x.f.expect(x.f.call("", igwRegion, nil, q), 500, "InternalError")
				if strings.Contains(raw, "<return>") || strings.Contains(raw, "<routeTableSet>") || strings.Contains(raw, "<internetGatewaySet>") {
					t.Fatalf("failed query published a partial result: %s", raw)
				}
			}
			if !slices.Equal(image, x.image(t)) {
				t.Fatal("a refused operation changed durable rows, claims or declarations")
			}
			restore()
			expectExactNativeDefaultRoute(t, x.f, url.Values{"RouteTableId.1": {table}}, table, x.vpc, x.gateway)
			expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2DeleteNativeDefaultRoute, table, ec2NativeDefaultCIDR, "")
			nativeGatewayTestChange(t, x.f, "", ec2DetachInternetGateway, x.gateway, x.vpc)
		})
	}
}

func TestEC2NativeDefaultRouteRollsBackSQLiteFailures(t *testing.T) {
	for _, tc := range []struct{ name, action, timing, member, stmt string }{
		{"insert rejected", ec2CreateNativeDefaultRoute, "BEFORE INSERT", "NEW", "SELECT RAISE(IGNORE);"},
		{"insert readback changed", ec2CreateNativeDefaultRoute, "AFTER INSERT", "NEW", "UPDATE aws_ec2_network_resources SET document=json_set(document,'$.RouteEnabled',json('false')) WHERE region=NEW.region AND id=NEW.id;"},
		{"insert lost parent", ec2CreateNativeDefaultRoute, "AFTER INSERT", "NEW", "DELETE FROM aws_ec2_network_resources WHERE region=NEW.region AND id=json_extract(NEW.document,'$.RouteTableID');"},
		{"delete rejected", ec2DeleteNativeDefaultRoute, "BEFORE DELETE", "OLD", "SELECT RAISE(IGNORE);"},
		{"delete reappeared", ec2DeleteNativeDefaultRoute, "AFTER DELETE", "OLD", "INSERT INTO aws_ec2_network_resources VALUES(OLD.region,OLD.id,OLD.stack_id,OLD.kind,OLD.document,OLD.revision);"},
		{"insert changes saved VPC", ec2CreateNativeDefaultRoute, "AFTER INSERT", "NEW", `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Tags',json('{"Name":"unintended"}')) WHERE region=NEW.region AND id=json_extract(NEW.document,'$.VPCID');`},
		{"delete changes saved gateway", ec2DeleteNativeDefaultRoute, "AFTER DELETE", "OLD", `UPDATE aws_ec2_network_resources SET document=json_set(document,'$.Tags',json('{"Name":"unintended"}')) WHERE region=OLD.region AND id=json_extract(OLD.document,'$.GatewayID');`},
		{"delete lost attachment", ec2DeleteNativeDefaultRoute, "AFTER DELETE", "OLD", "DELETE FROM aws_ec2_network_resources WHERE region=OLD.region AND kind='AWS::EC2::VPCGatewayAttachment' AND json_extract(document,'$.GatewayID')=json_extract(OLD.document,'$.GatewayID');"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newIGWLinkFaultFixture(t, true)
			table := nativeDefaultRouteTestTable(t, x.f, x.vpc)
			gateway, before := x.gateway, ""
			if tc.action == ec2DeleteNativeDefaultRoute {
				expectNativeDefaultRoute(t, x.f, "", igwRegion, ec2CreateNativeDefaultRoute, table, ec2NativeDefaultCIDR, gateway)
				before, gateway = gateway, ""
			}
			query := defaultRouteQuery(tc.action, table, ec2NativeDefaultCIDR, gateway)
			x.exec(t, fmt.Sprintf("CREATE TRIGGER route_fault %s ON aws_ec2_network_resources WHEN %s.kind='AWS::EC2::Route' BEGIN %s END", tc.timing, tc.member, tc.stmt))
			orig := x.image(t)
			x.f.expect(x.f.call("", igwRegion, nil, query), 500, "InternalError")
			if !slices.Equal(orig, x.image(t)) {
				t.Fatal("route fault did not roll back the original snapshot")
			}
			x.exec(t, "DROP TRIGGER route_fault")
			expectExactNativeDefaultRoute(t, x.f, url.Values{"RouteTableId.1": {table}}, table, x.vpc, before)
			expectNativeDefaultRoute(t, x.f, "", igwRegion, tc.action, table, ec2NativeDefaultCIDR, gateway)
		})
	}
}
