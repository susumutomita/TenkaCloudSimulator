package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestEC2NativeRouteTableGatewayMaintainsLegacyReadback(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region, other = "us-east-1", "ap-northeast-1"
	parent := f.create("", url.Values{"CidrBlock": {"10.48.0.0/16"}})
	stack, err := compileStack(region, "native-compare", `{"Resources":{"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.49.0.0/16"}},"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"},"Tags":[{"Key":"Name","Value":"legacy table"}]}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	list := func(principal, scope string) []ec2RouteTableRead {
		t.Helper()
		body := f.expect(f.call(principal, scope, nil, url.Values{"Action": {"DescribeRouteTables"}}), 200, "")
		var out ec2DescribeRouteTablesResponse
		if err := xml.Unmarshal([]byte(body), &out); err != nil || out.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || out.RequestID == "" {
			t.Fatalf("DescribeRouteTables protocol: %v / %s", err, body)
		}
		return out.Tables
	}
	baseline := list("", region)
	if len(baseline) != 1 || len(baseline[0].Tags) != 1 || baseline[0].Tags[0].Value != "legacy table" {
		t.Fatalf("expected surviving legacy table only; got %+v", baseline)
	}
	policy := func(statements string) {
		t.Helper()
		doc, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + statements + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.g.Identities.Set("route-editor", doc); err != nil {
			t.Fatal(err)
		}
	}
	create := url.Values{"Action": {"CreateRouteTable"}, "VpcId": {parent.ID}, "ClientToken": {"bounded-fixture"},
		"TagSpecification.1.ResourceType": {"route-table"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"ローカル ＆ <保存>"}}
	scope := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	parentARN, familyARN := networkARN(region, "vpc", parent.ID), networkARN(region, "route-table", "*")
	both := fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateRouteTable","Resource":[%q,%q]}`, parentARN, familyARN)
	tags := fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, familyARN)
	policy(fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateRouteTable","Resource":%q},%s`, familyARN, tags))
	f.expect(f.call("route-editor", region, nil, create), 403, "UnauthorizedOperation")
	policy(both)
	f.expect(f.call("route-editor", region, nil, create), 403, "UnauthorizedOperation")
	exactMask := networkARN(region, "route-table", "rtb-"+strings.Repeat("?", 17))
	policy(both + "," + tags + fmt.Sprintf(`,{"Effect":"Deny","Action":"ec2:CreateRouteTable","Resource":%q}`, exactMask))
	f.expect(f.call("route-editor", region, nil, create), 403, "UnauthorizedOperation")
	if got := list("", region); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("denials altered native or legacy state: %+v", got)
	}
	policy(both + "," + tags)
	f.expect(f.call("route-editor", region, scope, create), 403, "UnauthorizedOperation")
	var made ec2NativeRouteTableMutationResponse
	body := f.expect(f.call("route-editor", region, nil, create), 200, "")
	if err := xml.Unmarshal([]byte(body), &made); err != nil || made.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" ||
		made.XMLName.Local != "CreateRouteTableResponse" || made.RequestID == "" || made.Table == nil ||
		!ec2NativeRouteTableID.MatchString(made.Table.ID) || made.ClientToken != create.Get("ClientToken") || made.Table.VPC != parent.ID ||
		len(made.Table.Routes) != 0 || len(made.Table.Associations) != 0 || len(made.Table.Tags) != 1 || made.Table.Tags[0].Value != create.Get("TagSpecification.1.Tag.1.Value") {
		t.Fatalf("CreateRouteTable protocol: %v / %s", err, body)
	}
	target := made.Table.ID
	if got := f.describe(region); len(got) != 2 {
		t.Fatalf("native table made unrelated VPC discovery fail: %+v", got)
	}
	policy(both + "," + tags + `,{"Effect":"Allow","Action":"ec2:DescribeRouteTables","Resource":"*"}`)
	got := list("route-editor", region)
	if len(got) != 2 || got[0].ID >= got[1].ID {
		t.Fatalf("combined regional list: %+v", got)
	}
	by := map[string]ec2RouteTableRead{got[0].ID: got[0], got[1].ID: got[1]}
	if !reflect.DeepEqual(by[baseline[0].ID], baseline[0]) || !reflect.DeepEqual(by[target], *made.Table) {
		t.Fatalf("legacy or native projection changed: %+v", by)
	}
	if len(list("", other)) != 0 {
		t.Fatal("other region had a route table")
	}
	for _, bad := range []url.Values{
		{"Action": {"ReplaceRoute"}, "RouteTableId": {target}},
		{"Action": {"DescribeRouteTables"}, "Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {parent.ID}},
	} {
		f.expect(f.call("", region, nil, bad), 400, "UnsupportedOperation")
	}
	f.expect(f.call("", region, nil, url.Values{"Action": {"ReplaceRouteTableAssociation"}, "RouteTableId": {target}}), 400, "MissingParameter")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeRouteTables"}, "RouteTableId.1": {target}, "MaxResults": {"10"}}), 400, "InvalidParameterCombination")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {parent.ID}}), 400, "DependencyViolation")
	del := url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {target}}
	f.expect(f.call("route-editor", region, nil, del), 403, "UnauthorizedOperation")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteRouteTable"}, "RouteTableId": {baseline[0].ID}}), 400, "UnsupportedOperation")
	f.expect(f.call("", other, nil, del), 400, "InvalidRouteTableID.NotFound")
	policy(fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:DeleteRouteTable","Resource":%q}`, networkARN(region, "route-table", target)))
	f.expect(f.call("route-editor", region, scope, del), 403, "UnauthorizedOperation")
	body = f.expect(f.call("route-editor", region, nil, del), 200, "")
	var removed ec2NativeRouteTableMutationResponse
	if err := xml.Unmarshal([]byte(body), &removed); err != nil || removed.XMLName.Local != "DeleteRouteTableResponse" ||
		removed.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || removed.RequestID == "" || removed.Return == nil || !*removed.Return {
		t.Fatalf("DeleteRouteTable protocol: %v / %s", err, body)
	}
	f.expect(f.call("", region, nil, create), 400, "InvalidRouteTableID.NotFound")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {parent.ID}}), 200, "")
	if got := list("", region); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("cleanup changed the legacy projection: %+v", got)
	}
}
