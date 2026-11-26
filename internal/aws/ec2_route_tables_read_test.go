package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Exercise the public EC2 Query handler against actual CloudFormation-created
// resources and the normal SQLite store, not a second fixture registry.
func TestEC2DescribeRouteTablesReadback(t *testing.T) {
	const east, west = "us-east-1", "us-west-2"
	path := filepath.Join(t.TempDir(), "routes.db")
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	open := func(store *core.Store) *Gateway {
		t.Helper()
		stacks, err := NewStackStore(store.Database())
		if err != nil {
			t.Fatal(err)
		}
		return &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	}
	g := open(store)
	callVia := func(g *Gateway, region, principal string, extra url.Values, scoped bool, code int) string {
		t.Helper()
		v := url.Values{"Version": {"2016-11-15"}, "Action": {"DescribeRouteTables"}}
		for k, entries := range extra {
			v[k] = entries
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "ec2")
		r.Header.Set("X-Simulator-Region", region)
		r.Header.Set("X-Simulator-Principal", principal)
		if scoped {
			r = r.WithContext(context.WithValue(r.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-bound"}))
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("%d instead of %d: %s", w.Code, code, w.Body.String())
		}
		return w.Body.String()
	}
	call := func(region, principal string, extra url.Values, scoped bool, code int) string {
		t.Helper()
		return callVia(g, region, principal, extra, scoped, code)
	}
	read := func(region, principal string) []ec2RouteTableRead {
		t.Helper()
		body := call(region, principal, nil, false, 200)
		var result ec2DescribeRouteTablesResponse
		if err := xml.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
		if result.XMLNS != "http://ec2.amazonaws.com/doc/2016-11-15/" || result.RequestID == "" ||
			!strings.Contains(body, "<routeTableSet>") || strings.Contains(body, "<vpcSet>") || strings.Contains(body, "<securityGroupInfo>") {
			t.Fatalf("wrong wire envelope: %s", body)
		}
		return result.Tables
	}
	template := `{"Resources":{
        "Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.21.0.0/16"}},
        "Other":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.22.0.0/16"}},
        "Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.21.0.0/24"}},
        "OtherSubnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Other"},"CidrBlock":"10.22.0.0/24"}},
        "Gateway":{"Type":"AWS::EC2::InternetGateway","Properties":{}},
        "Attach":{"Type":"AWS::EC2::VPCGatewayAttachment","Properties":{"VpcId":{"Ref":"Vpc"},"InternetGatewayId":{"Ref":"Gateway"}}},
        "Table":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Vpc"},"Tags":[{"Key":"Name","Value":"<one> & two"},{"Key":"Empty","Value":""}]}},
        "Empty":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Other"}}},
        "Default":{"Type":"AWS::EC2::Route","DependsOn":"Attach","Properties":{"RouteTableId":{"Ref":"Table"},"GatewayId":{"Ref":"Gateway"},"DestinationCidrBlock":"0.0.0.0/0"}},
        "Assoc":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"SubnetId":{"Ref":"Subnet"},"RouteTableId":{"Ref":"Table"}}}
    }}`
	create := func(region, name string) (StackRecord, map[string]string) {
		t.Helper()
		stack, err := compileStack(region, name, template, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Stacks.Create(stack); err != nil {
			t.Fatal(err)
		}
		ids := map[string]string{}
		for _, r := range stack.Resources {
			ids[r.LogicalID] = r.PhysicalID
		}
		return stack, ids
	}
	policy := func(name, statements string) {
		t.Helper()
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":` + statements + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Identities.Set(name, p); err != nil {
			t.Fatal(err)
		}
	}
	policy("reader", `[{"Effect":"Allow","Action":"ec2:DescribeRouteTables","Resource":"*"}]`)
	policy("denied", `[{"Effect":"Allow","Action":"ec2:*","Resource":"*"},{"Effect":"Deny","Action":"ec2:DescribeRouteTables","Resource":"*"}]`)
	nativeVPC, err := g.Stacks.createNativeVPC(t.Context(), east, "10.50.0.0/16", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(read(east, "reader")) != 0 {
		t.Fatalf("synthesized table for EC2-owned VPC %s", nativeVPC.ID)
	}
	stack, ids := create(east, "east-net")
	_, westIDs := create(west, "west-net")
	verify := func(region string, ids map[string]string, route bool) {
		t.Helper()
		tables := read(region, "reader")
		if len(tables) != 2 || tables[0].ID >= tables[1].ID {
			t.Fatalf("count/order: %+v", tables)
		}
		for _, r := range tables {
			if r.Owner != "000000000000" {
				t.Fatal(r)
			}
			switch r.ID {
			case ids["Empty"]:
				if r.VPC != ids["Other"] || len(r.Associations) != 0 || len(r.Routes) != 0 {
					t.Fatalf("implicit default/local route or wrong VPC: %+v", r)
				}
			case ids["Table"]:
				if r.VPC != ids["Vpc"] || !slices.Equal(r.Tags, []ec2Tag{{Key: "Empty", Value: ""}, {Key: "Name", Value: "<one> & two"}}) ||
					!slices.Equal(r.Associations, []ec2RouteAssociationRead{{ID: ids["Assoc"], Table: ids["Table"], Subnet: ids["Subnet"], Main: false}}) {
					t.Fatalf("metadata: %+v", r)
				}
				var routes []ec2RouteRead
				if route {
					routes = []ec2RouteRead{{CIDR: "0.0.0.0/0", Gateway: ids["Gateway"], State: "active", Origin: "CreateRoute"}}
				}
				if !slices.Equal(r.Routes, routes) {
					t.Fatalf("route enable/readback: %+v want %+v", r.Routes, routes)
				}
			default:
				t.Fatalf("another region or owner: %+v", r)
			}
		}
	}
	verify(east, ids, true)
	verify(west, westIDs, true)
	// Pin the native wire spellings independently of the decoder/encoder types.
	body := call(east, "reader", nil, false, 200)
	for _, field := range []string{"<DescribeRouteTablesResponse ", "<requestId>", "<routeTableSet><item>", "<routeTableId>" + ids["Table"] + "</routeTableId>", "<vpcId>" + ids["Vpc"] + "</vpcId>", "<ownerId>000000000000</ownerId>", "<routeSet><item>", "<destinationCidrBlock>0.0.0.0/0</destinationCidrBlock>", "<gatewayId>" + ids["Gateway"] + "</gatewayId>", "<origin>CreateRoute</origin>", "<state>active</state>", "<associationSet><item>", "<routeTableAssociationId>" + ids["Assoc"] + "</routeTableAssociationId>", "<subnetId>" + ids["Subnet"] + "</subnetId>", "<main>false</main>"} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing wire field %q: %s", field, body)
		}
	}
	// The team isolation boundary is its own database, not an identity in this DB.
	other, err := core.New(filepath.Join(t.TempDir(), "team-two.db"))
	if err != nil {
		t.Fatal(err)
	}
	team := open(other)
	empty := callVia(team, east, "", nil, false, 200)
	if strings.Contains(empty, "<item>") || !strings.Contains(empty, "<routeTableSet></routeTableSet>") {
		t.Fatal(empty)
	}
	callVia(team, east, "reader", nil, false, 403)
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	exact := url.Values{"RouteTableId.1": {ids["Table"]}}
	exactTables := call(east, "reader", exact, false, 200)
	if !strings.Contains(exactTables, ids["Table"]) || !strings.Contains(exactTables, ids["Assoc"]) || strings.Contains(exactTables, ids["Empty"]) {
		t.Fatal(exactTables)
	}
	call(west, "reader", exact, false, 400)
	call(east, "denied", exact, false, 403)
	call(east, "reader", exact, true, 403)
	for _, q := range []url.Values{{"RouteTableId.1": {ids["Table"]}, "RouteTableId.2": {ids["Empty"]}}, {"Filter.1.Name": {"vpc-id"}}, {"MaxResults": {"5"}}, {"NextToken": {"none"}}, {"DryRun": {"false"}}} {
		if b := call(east, "reader", q, false, 400); !strings.Contains(b, "UnsupportedOperation") || strings.Contains(b, "routeTableSet") {
			t.Fatal(b)
		}
	}
	for _, p := range []string{"unknown", "denied"} {
		if b := call(east, p, nil, false, 403); strings.Contains(b, ids["Table"]) || !strings.Contains(b, "UnauthorizedOperation") {
			t.Fatal(b)
		}
	}
	call(east, "reader", nil, true, 403)
	filter := ec2TestSubnetRouteFilter(ids["Subnet"])
	filtered := call(east, "reader", filter, false, 200)
	if !strings.Contains(filtered, ids["Table"]) || strings.Contains(filtered, ids["Empty"]) {
		t.Fatal(filtered)
	}
	for _, region := range []string{west} {
		empty := call(region, "reader", filter, false, 200)
		if !strings.Contains(empty, "<routeTableSet></routeTableSet>") {
			t.Fatal(empty)
		}
	}
	call(east, "denied", filter, false, 403)
	call(east, "reader", filter, true, 403)
	change := func(enabled bool) {
		t.Helper()
		network, err := NewEC2NetworkStore(store.Database())
		if err != nil {
			t.Fatal(err)
		}
		action := "ec2:DeleteRoute"
		if enabled {
			action = "ec2:CreateRoute"
		}
		err = network.ChangeDefaultRoute(t.Context(), east, stack.ID, ids["Default"], enabled, func(a, arn string) error {
			if a != action || arn != networkARN(east, "route-table", ids["Table"]) {
				return fmt.Errorf("incorrect target %s %s", a, arn)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	change(false)
	verify(east, ids, false)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	g = open(store)
	verify(east, ids, false)
	verify(west, westIDs, true)
	change(true)
	verify(east, ids, true)
	checkEC2RouteGraph(t, g, stack, ids)
	// A valid-looking relation with the wrong VPC must not be partially exposed,
	// even if both the declaration and its saved subnet ID match each other.
	records, err := g.Stacks.Records(east, stack.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatal(records)
	}
	current := records[0]
	saved, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range current.Resources {
		if r.PhysicalID == ids["Assoc"] {
			current.Resources[i].Properties["SubnetId"] = ids["OtherSubnet"]
		}
	}
	wrong, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	modify := func(q string, args ...any) {
		t.Helper()
		r, e := store.Database().Exec(q, args...)
		if e != nil {
			t.Fatal(e)
		}
		n, e := r.RowsAffected()
		if e != nil || n != 1 {
			t.Fatalf("fixture update: %d, %v", n, e)
		}
	}
	modify("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(wrong), stack.ID)
	modify("UPDATE aws_ec2_network_resources SET document=json_set(document,'$.SubnetID',?) WHERE id=?", ids["OtherSubnet"], ids["Assoc"])
	mustReject := func() {
		t.Helper()
		// A selector that cannot match in this region must not hide damaged
		// bindings, compute a partial list, or turn store errors into empty.
		for _, q := range []url.Values{nil, ec2TestSubnetRouteFilter(westIDs["Subnet"])} {
			b := call(east, "reader", q, false, 500)
			if !strings.Contains(b, "InternalError") || strings.Contains(b, "routeTableSet") || strings.Contains(b, ids["Table"]) {
				t.Fatal(b)
			}
		}
		verify(west, westIDs, true)
	}
	mustReject()
	modify("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(saved), stack.ID)
	modify("UPDATE aws_ec2_network_resources SET document=json_set(document,'$.SubnetID',?) WHERE id=?", ids["Subnet"], ids["Assoc"])
	verify(east, ids, true)
	var savedRoute string
	if err := store.Database().QueryRow("SELECT document FROM aws_ec2_network_resources WHERE id=?", ids["Default"]).Scan(&savedRoute); err != nil {
		t.Fatal(err)
	}
	tamperRoute := func(expr string) {
		t.Helper()
		modify("UPDATE aws_ec2_network_resources SET document="+expr+" WHERE id=?", ids["Default"])
		mustReject()
		modify("UPDATE aws_ec2_network_resources SET document=? WHERE id=?", savedRoute, ids["Default"])
	}
	tamperRoute("json_remove(document,'$.RouteEnabled')")
	tamperRoute("json_set(document,'$.RouteEnabled',null)")
	tamperRoute("json_set(document,'$.RouteEnabled','false')")
	tamperRoute("json_set(document,'$.routeenabled',json('false'))")
	if !strings.Contains(savedRoute, `"RouteEnabled":true`) {
		t.Fatal("fixture must have an enabled route")
	}
	tamperRoute(`replace(document,'"RouteEnabled":true','"RouteEnabled":true,"RouteEnabled":null')`)
	verify(east, ids, true)
	// A missing typed entry may not silently remove a child from an otherwise valid table.
	modify("DELETE FROM aws_cfn_ownership WHERE kind=? AND physical_id=?", ec2Assoc, ids["Assoc"])
	mustReject()
	modify("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(east, ec2Assoc), ec2Assoc, ids["Assoc"], stack.ID)
	if err := g.Stacks.Delete(east, stack.Name); err != nil {
		t.Fatal(err)
	}
	if len(read(east, "reader")) != 0 {
		t.Fatal("returned deleted route table")
	}
	verify(west, westIDs, true)
}
