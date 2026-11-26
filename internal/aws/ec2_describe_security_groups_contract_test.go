package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// The ordinary native Query route reads the provider-owned SQLite state that
// CloudFormation actually creates. This test starts neither a guest nor a
// packet path and does not grant a public rule mutation.
func TestEC2DescribeSecurityGroupsContract(t *testing.T) {
	const (
		east        = "us-east-1"
		west        = "us-west-2"
		empty       = "ap-northeast-1"
		vpcKind     = "AWS::EC2::VPC"
		description = "web <local> & owned\r\n\t南風🌧\ufffd"
		tag         = "local\t\r\n南風"
		data        = `{"Resources":{
			"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.21.0.0/16"}},
			"App":{"Type":"AWS::EC2::SecurityGroup","Properties":{
				"VpcId":{"Ref":"Vpc"},"GroupDescription":"web <local> & owned\r\n\t南風🌧\ufffd",
				"Tags":[{"Key":"Z","Value":""},{"Key":"A","Value":"local\t\r\n南風"}],
				"SecurityGroupIngress":[{"IpProtocol":"tcp","FromPort":8000,"ToPort":8001,"CidrIp":"192.0.2.129/25"}]}},
			"Empty":{"Type":"AWS::EC2::SecurityGroup","Properties":{
				"VpcId":{"Ref":"Vpc"},"GroupDescription":"no rules"}}
		}}`
	)
	state, _ := s3TestState(t)
	db := state.Database()
	stacks, err := NewStackStore(db)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(state)}

	type fixture struct {
		record         StackRecord
		vpc, app, none string
	}
	create := func(t *testing.T, region, name, template string) fixture {
		t.Helper()
		record, err := compileStack(region, name, template, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := stacks.Create(record); err != nil {
			t.Fatal(err)
		}
		var f fixture
		f.record = record
		for _, item := range record.Resources {
			switch item.LogicalID {
			case "Vpc":
				f.vpc = item.PhysicalID
			case "App":
				f.app = item.PhysicalID
			case "Empty":
				f.none = item.PhysicalID
			}
		}
		if f.vpc == "" || f.app == "" || f.none == "" {
			t.Fatal("fixture did not create expected identifiers")
		}
		return f
	}
	a, b := create(t, east, "native-sg-east", data), create(t, west, "native-sg-west", data)

	const reader = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeSecurityGroups","Resource":"*"}]}`
	const denial = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:*","Resource":"*"},{"Effect":"Deny","Action":"ec2:DescribeSecurityGroups","Resource":"*"}]}`
	for principal, raw := range map[string]string{"sg-reader": reader, "sg-denied": denial} {
		policy, err := core.ParsePolicy([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Identities.Set(principal, policy); err != nil {
			t.Fatal(err)
		}
	}
	scopedPolicy, err := core.ParsePolicy([]byte(reader))
	if err != nil {
		t.Fatal(err)
	}
	call := func(t *testing.T, region, principal string, params url.Values, instanceScope bool, status int) string {
		t.Helper()
		form := url.Values{"Action": {"DescribeSecurityGroups"}, "Version": {"2016-11-15"}}
		for key, values := range params {
			form[key] = values
		}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Simulator-Service", "ec2")
		req.Header.Set("X-Simulator-Region", region)
		req.Header.Set("X-Simulator-Principal", principal)
		if instanceScope {
			ctx := context.WithValue(req.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-01234567"})
			req = req.WithContext(context.WithValue(ctx, sdkPolicyContextKey{}, scopedPolicy))
		}
		response := httptest.NewRecorder()
		g.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("DescribeSecurityGroups HTTP %d, want %d: %s", response.Code, status, response.Body.String())
		}
		return response.Body.String()
	}
	// Decode independently of the production Go projection structs.
	type wireTag struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	}
	type rule struct {
		Protocol string   `xml:"ipProtocol"`
		FromPort int      `xml:"fromPort"`
		ToPort   int      `xml:"toPort"`
		CIDRs    []string `xml:"ipRanges>item>cidrIp"`
	}
	type group struct {
		Owner       string    `xml:"ownerId"`
		ID          string    `xml:"groupId"`
		Description string    `xml:"groupDescription"`
		VPC         string    `xml:"vpcId"`
		Rules       []rule    `xml:"ipPermissions>item"`
		Tags        []wireTag `xml:"tagSet>item"`
	}
	decode := func(t *testing.T, body string) []group {
		t.Helper()
		var result struct {
			Root xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeSecurityGroupsResponse"`
			ID   string   `xml:"requestId"`
			Set  *struct {
				Items []group `xml:"item"`
			} `xml:"securityGroupInfo"`
		}
		if err := xml.Unmarshal([]byte(body), &result); err != nil || result.ID == "" || result.Set == nil {
			t.Fatalf("invalid Query/XML envelope: %v; %s", err, body)
		}
		for _, unsupported := range []string{"<groupName>", "<ipPermissionsEgress>", "<ipv6Ranges>", "<subnetSet>", "<vpcSet>", "<reservationSet>"} {
			if strings.Contains(body, unsupported) {
				t.Fatalf("fabricated field %s: %s", unsupported, body)
			}
		}
		return result.Set.Items
	}
	assertRows := func(t *testing.T, f fixture, ports [2]int, cidr string) {
		t.Helper()
		rows := decode(t, call(t, f.record.Region, "sg-reader", nil, false, http.StatusOK))
		if len(rows) != 2 || rows[0].ID >= rows[1].ID {
			t.Fatalf("expected exactly two deterministically ordered groups: %#v", rows)
		}
		byID := map[string]group{rows[0].ID: rows[0], rows[1].ID: rows[1]}
		for _, id := range []string{f.app, f.none} {
			if row := byID[id]; row.ID != id || row.VPC != f.vpc || row.Owner != "000000000000" {
				t.Fatalf("incorrect local group binding: %#v", row)
			}
		}
		app := byID[f.app]
		if app.Description != description || !slices.Equal(app.Tags, []wireTag{{"A", tag}, {"Z", ""}}) ||
			len(app.Rules) != 1 ||
			app.Rules[0].Protocol != "tcp" || app.Rules[0].FromPort != ports[0] || app.Rules[0].ToPort != ports[1] ||
			!slices.Equal(app.Rules[0].CIDRs, []string{cidr}) {
			t.Fatalf("stored ingress or metadata not preserved: %#v", app)
		}
		if none := byID[f.none]; none.Description != "no rules" || len(none.Rules) != 0 || len(none.Tags) != 0 {
			t.Fatalf("empty group contains fabricated state: %#v", none)
		}
	}
	assertRows(t, a, [2]int{8000, 8001}, "192.0.2.128/25")
	assertRows(t, b, [2]int{8000, 8001}, "192.0.2.128/25")
	if rows := decode(t, call(t, empty, "sg-reader", nil, false, http.StatusOK)); len(rows) != 0 {
		t.Fatalf("empty region: %#v", rows)
	}
	for _, field := range []string{"DryRun", "Filter.1.Name", "GroupName.1", "MaxResults", "NextToken"} {
		if body := call(t, east, "sg-reader", url.Values{field: {"ignored"}}, false, http.StatusBadRequest); !strings.Contains(body, "<Code>UnsupportedOperation</Code>") {
			t.Fatalf("unsupported %s accepted or misclassified: %s", field, body)
		}
	}
	if body := call(t, east, "sg-reader", url.Values{"GroupId.1": {"ignored"}}, false, http.StatusBadRequest); !strings.Contains(body, "<Code>InvalidGroupId.Malformed</Code>") {
		t.Fatalf("typed ID selection admitted malformed ID: %s", body)
	}
	for _, denied := range []struct {
		principal string
		scoped    bool
	}{{"missing", false}, {"sg-denied", false}, {"sg-reader", true}} {
		if body := call(t, east, denied.principal, nil, denied.scoped, http.StatusForbidden); strings.Contains(body, a.app) || strings.Contains(body, "<securityGroupInfo>") {
			t.Fatalf("unauthorized result leaked a record: %s", body)
		}
	}
	// Some existing CloudFormation rows can contain JSON-valid text that
	// cannot round-trip through XML 1.0. Reject the entire read rather than
	// silently replacing characters or returning two colliding tag keys.
	for _, legacy := range []struct{ name, from, to string }{
		{"description", "web <local>", `web\u0000 <local>`},
		{"tag-key", `"Key":"A"`, `"Key":"A\u0001"`},
		{"tag-value", `"Value":"local`, `"Value":"local\ufffe`},
	} {
		t.Run("legacy-"+legacy.name, func(t *testing.T) {
			stored := create(t, east, "legacy-"+legacy.name, strings.Replace(data, legacy.from, legacy.to, 1))
			snapshot := func() string {
				t.Helper()
				var doc string
				if err := db.QueryRow(`SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, east, stored.app).Scan(&doc); err != nil {
					t.Fatal(err)
				}
				return doc
			}
			before := snapshot()
			body := call(t, east, "sg-reader", nil, false, http.StatusInternalServerError)
			if !strings.Contains(body, "<Code>InternalError</Code>") ||
				strings.Contains(body, "<securityGroupInfo>") || strings.Contains(body, stored.app) || strings.Contains(body, a.app) {
				t.Fatalf("invalid XML data produced a partial or misleading read: %s", body)
			}
			if after := snapshot(); after != before {
				t.Fatal("DescribeSecurityGroups modified stored legacy state")
			}
			if err := stacks.Delete(east, stored.record.Name); err != nil {
				t.Fatal(err)
			}
			assertRows(t, a, [2]int{8000, 8001}, "192.0.2.128/25")
		})
	}
	network, err := NewEC2NetworkStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// The established trusted owner API can revise stored ingress independently
	// of the template. Describe must see that revision, not replay creation.
	if err := network.ChangeIngress(context.Background(), east, a.record.ID, a.app, []EC2IngressRule{{"tcp", 9443, 9443, "198.51.100.2/24"}},
		func(action, resource string) error {
			if resource != networkARN(east, "security-group", a.app) ||
				(action != "ec2:AuthorizeSecurityGroupIngress" && action != "ec2:RevokeSecurityGroupIngress") {
				return fmt.Errorf("unexpected fixture authority: %s %s", action, resource)
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	assertRows(t, a, [2]int{9443, 9443}, "198.51.100.0/24")
	assertRows(t, b, [2]int{8000, 8001}, "192.0.2.128/25")

	// No surviving group can be exposed without the parent claim. Failure of
	// one group must not serialize the other group's private metadata either.
	namespace := stackResourceNamespace(east, vpcKind)
	if _, err := db.Exec("DELETE FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=?", namespace, vpcKind, a.vpc); err != nil {
		t.Fatal(err)
	}
	body := call(t, east, "sg-reader", nil, false, http.StatusInternalServerError)
	if strings.Contains(body, a.app) || strings.Contains(body, a.none) || strings.Contains(body, "<securityGroupInfo>") {
		t.Fatalf("broken ownership leaked a partial result: %s", body)
	}
	if _, err := db.Exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", namespace, vpcKind, a.vpc, a.record.ID); err != nil {
		t.Fatal(err)
	}
	if err := stacks.Delete(east, a.record.Name); err != nil {
		t.Fatal(err)
	}
	if rows := decode(t, call(t, east, "sg-reader", nil, false, http.StatusOK)); len(rows) != 0 {
		t.Fatalf("deleted stack still visible: %#v", rows)
	}
	assertRows(t, b, [2]int{8000, 8001}, "192.0.2.128/25")
}
