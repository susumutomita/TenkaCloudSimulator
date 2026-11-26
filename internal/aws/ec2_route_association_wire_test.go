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

// Exercise actual registration, authorization and one shared SQLite snapshot,
// not just the association's independent store methods.
func TestEC2NativeRouteAssociationGatewayReadbackAndAuthority(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region, other, editor = "us-east-1", "ap-northeast-1", "association-editor"
	const namespace = "http://ec2.amazonaws.com/doc/2016-11-15/"
	parent, q := routeAssocFixture(t, f.stacks, region, "10.61.0.0/16", "10.61.1.0/24")
	legacy, err := compileStack(region, "association-cfn-continuity", `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.61.0.0/16"}},
		"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.61.1.0/24"}},
		"Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"}}},
		"Association":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"SubnetId":{"Ref":"Subnet"},"RouteTableId":{"Ref":"Table"}}}
	}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(legacy); err != nil {
		t.Fatal(err)
	}

	call := func(principal, scope, action string, kv ...string) string {
		t.Helper()
		values := url.Values{"Action": {action}}
		for i := 0; i < len(kv); i += 2 {
			values.Set(kv[i], kv[i+1])
		}
		return f.expect(f.call(principal, scope, nil, values), 200, "")
	}
	list := func(principal, scope string) map[string]ec2RouteTableRead {
		t.Helper()
		body := call(principal, scope, "DescribeRouteTables")
		var out ec2DescribeRouteTablesResponse
		if err := xml.Unmarshal([]byte(body), &out); err != nil ||
			out.XMLName.Space != namespace || out.RequestID == "" {
			t.Fatalf("DescribeRouteTables result: %v / %s", err, body)
		}
		rows := make(map[string]ec2RouteTableRead, len(out.Tables))
		for _, table := range out.Tables {
			rows[table.ID] = table
		}
		return rows
	}
	baseline := list("", region)
	var cfn ec2RouteTableRead
	for id, table := range baseline {
		if id != q.table {
			cfn = table
		}
	}
	if len(baseline) != 2 || cfn.ID == "" || len(cfn.Associations) != 1 ||
		len(baseline[q.table].Associations) != 0 {
		t.Fatalf("baseline did not contain exactly one CFN association and detached native table: %+v", baseline)
	}

	const readGrant = `{"Effect":"Allow","Action":"ec2:DescribeRouteTables","Resource":"*"}`
	grant := func(effect, arn string) string {
		return fmt.Sprintf(`{"Effect":%q,"Action":["ec2:AssociateRouteTable","ec2:DisassociateRouteTable"],"Resource":%q}`, effect, arn)
	}
	setPolicy := func(entries ...string) {
		t.Helper()
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + strings.Join(entries, ",") + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set(editor, policy); err != nil {
			t.Fatal(err)
		}
	}
	tableARN, subnetARN := networkARN(region, "route-table", q.table), networkARN(region, "subnet", q.subnet)
	tableGrant, subnetGrant := grant("Allow", tableARN), grant("Allow", subnetARN)
	associate := url.Values{"Action": {"AssociateRouteTable"}, "RouteTableId": {q.table}, "SubnetId": {q.subnet}}
	f.expect(f.call(editor, region, nil, associate), 403, "UnauthorizedOperation")
	setPolicy(readGrant, tableGrant)
	f.expect(f.call(editor, region, nil, associate), 403, "UnauthorizedOperation")
	setPolicy(readGrant, subnetGrant)
	f.expect(f.call(editor, region, nil, associate), 403, "UnauthorizedOperation")
	setPolicy(readGrant, tableGrant, subnetGrant, grant("Deny", subnetARN))
	f.expect(f.call(editor, region, nil, associate), 403, "UnauthorizedOperation")
	setPolicy(readGrant, tableGrant, subnetGrant)
	instance := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	f.expect(f.call(core.Administrator, region, instance, associate), 403, "UnauthorizedOperation")
	if got := list(editor, region); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("refused edits changed regional inventory: %+v", got)
	}

	var id string
	for i := 0; i < 2; i++ {
		body := f.expect(f.call(editor, region, nil, associate), 200, "")
		var receipt ec2NativeRouteAssociationResponse
		if err := xml.Unmarshal([]byte(body), &receipt); err != nil || receipt.XMLName != (xml.Name{Space: namespace, Local: "AssociateRouteTableResponse"}) ||
			receipt.RequestID == "" || receipt.Return != nil || !ec2NativeRouteAssociationID.MatchString(receipt.AssociationID) ||
			receipt.AssociationState == nil || receipt.AssociationState.State != "associated" ||
			i > 0 && id != receipt.AssociationID {
			t.Fatalf("invalid or unstable associate acknowledgment: %v / %s", err, body)
		}
		id = receipt.AssociationID
	}
	got := list(editor, region)
	wantAssociation := ec2RouteAssociationRead{ID: id, Table: q.table, Subnet: q.subnet, Main: false}
	if len(got) != 2 || !reflect.DeepEqual(got[cfn.ID], cfn) ||
		!reflect.DeepEqual(got[q.table].Associations, []ec2RouteAssociationRead{wantAssociation}) || len(got[q.table].Routes) != 0 ||
		len(list(editor, other)) != 0 || len(f.describe(region)) != 2 {
		t.Fatalf("incorrect bound readback, legacy continuity, or region: %+v", got)
	}

	remove := func(target string) url.Values {
		return url.Values{"Action": {"DisassociateRouteTable"}, "AssociationId": {target}}
	}
	setPolicy(readGrant, tableGrant)
	lookup := func(scope, id string) string {
		t.Helper()
		return f.expect(f.call(editor, scope, nil, remove(id)), 403, "UnauthorizedOperation")
	}
	absent := lookup(region, "rtbassoc-00000000")
	for _, existing := range []string{lookup(region, id), lookup(other, id), lookup(region, cfn.Associations[0].ID)} {
		if existing != absent {
			t.Fatal("unresolved/unauthorized association returned distinguishable detail")
		}
	}

	// A malformed synthetic datastore record cannot establish a trustworthy pair.
	db := f.store.Database()
	var saved string
	if err := db.QueryRow(`SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, region, id).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	writeDocument := func(document string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE aws_ec2_network_resources SET document=? WHERE region=? AND id=?`, document, region, id); err != nil {
			t.Fatal(err)
		}
	}
	writeDocument("{}")
	if corrupt := lookup(region, id); corrupt != absent {
		t.Fatal("invalid saved identity returned detail without establishing a trusted pair")
	}
	f.expect(f.call(core.Administrator, region, nil, remove(id)), 500, "InternalError")
	for _, action := range []string{"DescribeRouteTables", "DescribeVpcs"} {
		body := f.expect(f.call(core.Administrator, region, nil, url.Values{"Action": {action}}), 500, "InternalError")
		if strings.Contains(body, cfn.ID) || strings.Contains(body, id) || strings.Contains(body, parent) {
			t.Fatal("inconsistent regional state exposed a partial snapshot")
		}
	}
	writeDocument(saved)

	setPolicy(readGrant, tableGrant, subnetGrant, grant("Deny", subnetARN))
	if lookup(region, id) != absent {
		t.Fatal("explicit subnet deny returned pair detail")
	}
	setPolicy(readGrant, tableGrant, subnetGrant)
	body := f.expect(f.call(editor, region, nil, remove(id)), 200, "")
	var receipt ec2NativeRouteAssociationResponse
	if err := xml.Unmarshal([]byte(body), &receipt); err != nil || receipt.XMLName != (xml.Name{Space: namespace, Local: "DisassociateRouteTableResponse"}) ||
		receipt.RequestID == "" || receipt.Return == nil || !*receipt.Return || receipt.AssociationID != "" || receipt.AssociationState != nil {
		t.Fatalf("invalid disassociate acknowledgment: %v / %s", err, body)
	}
	if got := list(editor, region); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("removal did not restore exact independent snapshots: %+v", got)
	}
	f.expect(f.call(core.Administrator, region, nil, remove(id)), 400, "InvalidAssociationID.NotFound")
}
