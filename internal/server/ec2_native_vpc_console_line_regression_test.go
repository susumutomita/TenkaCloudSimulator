package server

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const vpcConsoleRegion = "us-east-1"

type vpcConsoleRegression struct {
	t        *testing.T
	path     string
	access   *Access
	operator string
	cookie   *http.Cookie
}

func newVPCConsoleRegression(t *testing.T) vpcConsoleRegression {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vpc-console.db")
	handler, closeState, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeState(); err != nil {
			t.Error(err)
		}
	})
	operator := strings.Repeat("c", 64)
	access, err := NewAccess(handler, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	return vpcConsoleRegression{t, path, access, operator, browserSignIn(t, access, operator, nil)}
}

func (f vpcConsoleRegression) request(cookie *http.Cookie, region, service, action string, fields url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	values := url.Values{"Action": {action}, "Version": {"2016-11-15"}}
	if service == "cloudformation" {
		values.Set("Version", "2010-05-15")
	}
	for key, vals := range fields {
		values[key] = vals
	}
	return browserRequest(f.access, "POST", "/", values.Encode(), "", cookie, map[string]string{
		"Content-Type":        "application/x-www-form-urlencoded",
		"X-Simulator-Service": service, "X-Simulator-Region": region,
	})
}

func (f vpcConsoleRegression) ec2(action string, fields url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.request(f.cookie, vpcConsoleRegion, "ec2", action, fields)
}

func vpcConsoleXML(t *testing.T, w *httptest.ResponseRecorder, reply string, target any) {
	t.Helper()
	var root struct {
		XMLName   xml.Name
		RequestID string `xml:"requestId"`
	}
	if w.Code != http.StatusOK || xml.Unmarshal(w.Body.Bytes(), &root) != nil || root.XMLName.Local != reply {
		t.Fatalf("%s: status %d, body %s", reply, w.Code, w.Body.String())
	}
	if !strings.HasPrefix(reply, "CreateStack") && !strings.HasPrefix(reply, "DeleteStack") && !strings.HasPrefix(reply, "DescribeStack") &&
		(root.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || root.RequestID == "") {
		t.Fatalf("%s lost native EC2 namespace or request ID", reply)
	}
	if err := xml.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("%s body did not decode: %v", reply, err)
	}
}

func vpcConsoleFailure(t *testing.T, w *httptest.ResponseRecorder, status int, code string, noOutput ...string) {
	t.Helper()
	var result struct {
		XMLName xml.Name
		Code    string `xml:"Errors>Error>Code"`
		CFNCode string `xml:"Error>Code"`
	}
	if w.Code != status || xml.Unmarshal(w.Body.Bytes(), &result) != nil ||
		(result.Code != code && result.CFNCode != code) {
		t.Fatalf("want status %d / %s, got %d / %s", status, code, w.Code, w.Body.String())
	}
	for _, forbidden := range noOutput {
		if forbidden != "" && strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("refusal exposed partial data or internal diagnostics %q: %s", forbidden, w.Body.String())
		}
	}
}

type vpcConsoleItem struct {
	VPCID      string   `xml:"vpcId"`
	SubnetID   string   `xml:"subnetId"`
	GroupID    string   `xml:"groupId"`
	ACLID      string   `xml:"networkAclId"`
	Owner      string   `xml:"simulatorManagedBy"`
	CIDR       string   `xml:"cidrBlock"`
	Zone       string   `xml:"availabilityZone"`
	GroupMode  string   `xml:"simulatorSecurityGroupMode"`
	ACLMode    string   `xml:"simulatorNetworkAclMode"`
	ACLDefault bool     `xml:"default"`
	ACLSubnets []string `xml:"associationSet>item>subnetId"`
}

type vpcConsoleReply struct {
	VPC      vpcConsoleItem   `xml:"vpc"`
	Subnet   vpcConsoleItem   `xml:"subnet"`
	Group    string           `xml:"groupId"`
	ACL      vpcConsoleItem   `xml:"networkAcl"`
	VPCs     []vpcConsoleItem `xml:"vpcSet>item"`
	Subnets  []vpcConsoleItem `xml:"subnetSet>item"`
	Groups   []vpcConsoleItem `xml:"securityGroupInfo>item"`
	ACLs     []vpcConsoleItem `xml:"networkAclSet>item"`
	Returned bool             `xml:"return"`
}

func (f vpcConsoleRegression) network(action string, fields url.Values) vpcConsoleReply {
	f.t.Helper()
	var r vpcConsoleReply
	vpcConsoleXML(f.t, f.ec2(action, fields), action+"Response", &r)
	return r
}

func (f vpcConsoleRegression) createVPC(region, cidr string) string {
	f.t.Helper()
	var r vpcConsoleReply
	vpcConsoleXML(f.t, f.request(f.cookie, region, "ec2", "CreateVpc", url.Values{"CidrBlock": {cidr}}), "CreateVpcResponse", &r)
	if !strings.HasPrefix(r.VPC.VPCID, "vpc-") || r.VPC.Owner != "ec2" || r.VPC.CIDR != cidr {
		f.t.Fatalf("bad native VPC projection: %+v", r.VPC)
	}
	return r.VPC.VPCID
}

func (f vpcConsoleRegression) children(vpc string) (subnet, group, acl string) {
	f.t.Helper()
	s := f.network("CreateSubnet", url.Values{"VpcId": {vpc}, "CidrBlock": {"10.48.2.8/24"}, "AvailabilityZone": {"us-east-1b"}}).Subnet
	g := f.network("CreateSecurityGroup", url.Values{"VpcId": {vpc}, "GroupName": {"console-children"}, "GroupDescription": {"Detached console group"}}).Group
	n := f.network("CreateNetworkAcl", url.Values{"VpcId": {vpc}, "ClientToken": {"console-children"}}).ACL
	if !strings.HasPrefix(s.SubnetID, "subnet-") || !strings.HasPrefix(g, "sg-") || !strings.HasPrefix(n.ACLID, "acl-") ||
		s.VPCID != vpc || s.Owner != "ec2" || s.Zone != "us-east-1b" || s.CIDR != "10.48.2.0/24" || n.VPCID != vpc || n.ACLMode != "detached-config-only" {
		f.t.Fatalf("unexpected native child projections: %+v %q %+v", s, g, n)
	}
	return s.SubnetID, g, n.ACLID
}

const vpcConsoleStackName = "console-ownership-fixture"
const vpcConsoleTemplate = `{"Resources":{
  "Net":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.48.0.0/16"}},
  "Segment":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Net"},"CidrBlock":"10.48.2.0/24","AvailabilityZone":"us-east-1c"}},
  "Firewall":{"Type":"AWS::EC2::SecurityGroup","Properties":{"VpcId":{"Ref":"Net"},"GroupDescription":"CloudFormation fixture"}},
  "Routes":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"Net"}}},
  "Binding":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"Routes"},"SubnetId":{"Ref":"Segment"}}}
},"Outputs":{"VPC":{"Value":{"Ref":"Net"}},"Subnet":{"Value":{"Ref":"Segment"}},"Group":{"Value":{"Ref":"Firewall"}},"RouteTable":{"Value":{"Ref":"Routes"}}}}`

func (f vpcConsoleRegression) stack(action string, fields url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	if fields == nil {
		fields = url.Values{}
	}
	fields.Set("StackName", vpcConsoleStackName)
	return f.request(f.cookie, vpcConsoleRegion, "cloudformation", action, fields)
}

func (f vpcConsoleRegression) stackOutputs() map[string]string {
	f.t.Helper()
	var state struct {
		Status string `xml:"DescribeStacksResult>Stacks>member>StackStatus"`
		Items  []struct {
			Key   string `xml:"OutputKey"`
			Value string `xml:"OutputValue"`
		} `xml:"DescribeStacksResult>Stacks>member>Outputs>member"`
	}
	vpcConsoleXML(f.t, f.stack("DescribeStacks", nil), "DescribeStacksResponse", &state)
	result := map[string]string{}
	for _, item := range state.Items {
		result[item.Key] = item.Value
	}
	if state.Status != "CREATE_COMPLETE" || len(result) != 4 {
		f.t.Fatalf("CloudFormation did not retain its resources: %+v", state)
	}
	for key, prefix := range map[string]string{"VPC": "vpc-", "Subnet": "subnet-", "Group": "sg-", "RouteTable": "rtb-"} {
		if !strings.HasPrefix(result[key], prefix) {
			f.t.Fatalf("missing actual %s identity: %+v", key, result)
		}
	}
	return result
}

func (f vpcConsoleRegression) createStack() map[string]string {
	f.t.Helper()
	vpcConsoleXML(f.t, f.stack("CreateStack", url.Values{"TemplateBody": {vpcConsoleTemplate}}), "CreateStackResponse", &struct{}{})
	return f.stackOutputs()
}

func TestEC2NativeVPCConsoleKeepsCloudFormationAndNativeOwnershipSeparate(t *testing.T) {
	f := newVPCConsoleRegression(t)
	cfn := f.createStack()
	native := f.createVPC(vpcConsoleRegion, "10.48.0.0/16") // Same CIDR; still a distinct owner.
	other := f.createVPC("us-west-2", "10.48.0.0/16")
	subnet, group, acl := f.children(native)
	learner := browserSignIn(t, f.access, participantCredential(t, f.access, f.operator), nil)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ec2:DescribeVpcs","ec2:DescribeSubnets","ec2:DescribeSecurityGroups","ec2:DescribeRouteTables","ec2:DescribeNetworkAcls","cloudformation:DescribeStacks"],"Resource":"*"}]}`
	if w := browserRequest(f.access, "PUT", "/_simulator/v1/iam/principals/reader/policy", policy, "", f.cookie, map[string]string{"Content-Type": "application/json"}); w.Code != http.StatusOK {
		t.Fatalf("install participant read policy: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		name, action, idKey, id string
	}{
		{"VPC", "DeleteVpc", "VpcId", cfn["VPC"]},
		{"Subnet", "DeleteSubnet", "SubnetId", cfn["Subnet"]},
		{"SecurityGroup", "DeleteSecurityGroup", "GroupId", cfn["Group"]},
	} {
		t.Run("CloudFormation owns "+tc.name, func(t *testing.T) {
			vpcConsoleFailure(t, f.ec2(tc.action, url.Values{tc.idKey: {tc.id}}), 400, "UnsupportedOperation", "<return>true")
		})
	}
	for _, tc := range []struct {
		op     string
		fields url.Values
	}{
		{"CreateSubnet", url.Values{"VpcId": {cfn["VPC"]}, "CidrBlock": {"10.48.3.0/24"}}},
		{"CreateSecurityGroup", url.Values{"VpcId": {cfn["VPC"]}, "GroupName": {"no-adoption"}, "GroupDescription": {"Do not adopt"}}},
		{"CreateNetworkAcl", url.Values{"VpcId": {cfn["VPC"]}}},
	} {
		vpcConsoleFailure(t, f.ec2(tc.op, tc.fields), 400, "UnsupportedOperation", tc.op+"Response")
	}
	vpcConsoleFailure(t, f.request(learner, vpcConsoleRegion, "ec2", "DeleteSubnet", url.Values{"SubnetId": {subnet}}),
		403, "UnauthorizedOperation", "<return>true")
	vpcConsoleFailure(t, f.request(learner, vpcConsoleRegion, "cloudformation", "DescribeStacks", url.Values{"StackName": {vpcConsoleStackName}}),
		403, "AccessDenied", cfn["VPC"], cfn["Subnet"])

	var vpcs, subnets, groups, acls vpcConsoleReply
	vpcConsoleXML(t, f.request(learner, vpcConsoleRegion, "ec2", "DescribeVpcs", nil), "DescribeVpcsResponse", &vpcs)
	owners := map[string]string{}
	for _, v := range vpcs.VPCs {
		owners[v.VPCID] = v.Owner
	}
	if len(vpcs.VPCs) != 2 || owners[native] != "ec2" || owners[cfn["VPC"]] != "cloudformation" || owners[other] != "" {
		t.Fatalf("same-CIDR or cross-region data misprojected: %+v", vpcs.VPCs)
	}
	vpcConsoleXML(t, f.request(learner, vpcConsoleRegion, "ec2", "DescribeSubnets", nil), "DescribeSubnetsResponse", &subnets)
	byID := map[string]vpcConsoleItem{}
	for _, s := range subnets.Subnets {
		byID[s.SubnetID] = s
	}
	if len(subnets.Subnets) != 2 || byID[subnet].VPCID != native || byID[subnet].Owner != "ec2" ||
		byID[cfn["Subnet"]].VPCID != cfn["VPC"] || byID[cfn["Subnet"]].Owner == "ec2" ||
		byID[cfn["Subnet"]].Zone != "us-east-1c" || !sort.SliceIsSorted(subnets.Subnets, func(i, j int) bool {
		return subnets.Subnets[i].SubnetID < subnets.Subnets[j].SubnetID
	}) {
		t.Fatalf("mixed native and CFN subnets lost ownership: %+v", subnets.Subnets)
	}
	groups = f.network("DescribeSecurityGroups", url.Values{"Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {native}})
	if len(groups.Groups) != 1 || groups.Groups[0].GroupID != group || groups.Groups[0].GroupMode != "detached-config-only" {
		t.Fatalf("native group leaked from or into another owner: %+v", groups.Groups)
	}
	acls = f.network("DescribeNetworkAcls", url.Values{"Filter.1.Name": {"vpc-id"}, "Filter.1.Value.1": {native}})
	var defaultACL, customACL vpcConsoleItem
	for _, item := range acls.ACLs {
		if item.ACLDefault {
			defaultACL = item
		} else {
			customACL = item
		}
	}
	if len(acls.ACLs) != 2 || customACL.ACLID != acl || customACL.ACLMode != "detached-config-only" ||
		len(customACL.ACLSubnets) != 0 || defaultACL.ACLMode != "default-config-only" ||
		!reflect.DeepEqual(defaultACL.ACLSubnets, []string{subnet}) {
		t.Fatalf("expected exact detached ACL and associated default: %+v", acls.ACLs)
	}
	var routes struct {
		Routes []struct {
			ID    string `xml:"routeTableId"`
			VPCID string `xml:"vpcId"`
			Links []struct {
				Subnet string `xml:"subnetId"`
				Main   bool   `xml:"main"`
			} `xml:"associationSet>item"`
			Paths []struct {
				CIDR    string `xml:"destinationCidrBlock"`
				Gateway string `xml:"gatewayId"`
			} `xml:"routeSet>item"`
		} `xml:"routeTableSet>item"`
	}
	vpcConsoleXML(t, f.ec2("DescribeRouteTables", nil), "DescribeRouteTablesResponse", &routes)
	var seenCFN, seenMain bool
	for _, table := range routes.Routes {
		switch table.VPCID {
		case cfn["VPC"]:
			seenCFN = table.ID == cfn["RouteTable"] && len(table.Links) == 1 && !table.Links[0].Main && table.Links[0].Subnet == cfn["Subnet"]
		case native:
			seenMain = strings.HasPrefix(table.ID, "rtb-") && len(table.Links) == 1 && table.Links[0].Main && table.Links[0].Subnet == "" &&
				len(table.Paths) == 1 && table.Paths[0].CIDR == "10.48.0.0/16" && table.Paths[0].Gateway == "local"
		}
	}
	if len(routes.Routes) != 2 || !seenCFN || !seenMain {
		t.Fatalf("mixed route discovery did not preserve CFN and native ownership: %+v", routes.Routes)
	}

	vpcConsoleXML(t, f.stack("DeleteStack", nil), "DeleteStackResponse", &struct{}{})
	if seen := f.network("DescribeSubnets", nil).Subnets; len(seen) != 1 || seen[0].SubnetID != subnet {
		t.Fatalf("CloudFormation stack cleanup removed a native subnet or retained its own: %+v", seen)
	}
	vpcConsoleFailure(t, f.ec2("DeleteVpc", url.Values{"VpcId": {native}}), 400, "DependencyViolation", "<return>true")
	for _, tc := range []struct{ action, field, id string }{
		{"DeleteNetworkAcl", "NetworkAclId", acl}, {"DeleteSecurityGroup", "GroupId", group},
		{"DeleteSubnet", "SubnetId", subnet}, {"DeleteVpc", "VpcId", native},
	} {
		if !f.network(tc.action, url.Values{tc.field: {tc.id}}).Returned {
			t.Fatalf("%s did not acknowledge the scoped cleanup", tc.action)
		}
	}
	var west vpcConsoleReply
	vpcConsoleXML(t, f.request(f.cookie, "us-west-2", "ec2", "DescribeVpcs", nil), "DescribeVpcsResponse", &west)
	if len(west.VPCs) != 1 || west.VPCs[0].VPCID != other {
		t.Fatal("cleanup reached another region")
	}
}

func (f vpcConsoleRegression) state() *sql.DB {
	f.t.Helper()
	db, err := sql.Open("sqlite3", "file:"+f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		if err := db.Close(); err != nil {
			f.t.Error(err)
		}
	})
	return db
}

func vpcConsoleReplaceDoc(t *testing.T, db *sql.DB, id, payload string) {
	t.Helper()
	result, err := db.Exec(`UPDATE aws_ec2_network_resources SET document=? WHERE region=? AND id=?`, payload, vpcConsoleRegion, id)
	if err != nil {
		t.Fatal(err)
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		t.Fatalf("fixture update affected %d rows, want one: %v", n, err)
	}
}

func TestEC2NativeVPCConsoleRejectsPartialInventoryAndRecovers(t *testing.T) {
	f := newVPCConsoleRegression(t)
	cfn := f.createStack()
	native := f.createVPC(vpcConsoleRegion, "10.48.0.0/16")
	subnet, group, acl := f.children(native)
	db := f.state()

	if got := f.network("DescribeVpcs", nil).VPCs; len(got) != 2 {
		t.Fatalf("healthy mixed starting inventory: %+v", got)
	}
	for _, tc := range []struct {
		label, id, attr, value, describe, element string
	}{
		{"native subnet", subnet, "AvailabilityZone", "us-west-2a", "DescribeSubnets", "subnetSet"},
		{"detached group", group, "SubnetID", subnet, "DescribeSecurityGroups", "securityGroupInfo"},
		{"detached ACL", acl, "RouteTableID", cfn["RouteTable"], "DescribeNetworkAcls", "networkAclSet"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var raw string
			if err := db.QueryRow(`SELECT document FROM aws_ec2_network_resources WHERE region=? AND id=?`, vpcConsoleRegion, tc.id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			payload[tc.attr] = tc.value
			altered, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			vpcConsoleReplaceDoc(t, db, tc.id, string(altered))
			t.Cleanup(func() { vpcConsoleReplaceDoc(t, db, tc.id, raw) })
			vpcConsoleFailure(t, f.ec2("DescribeVpcs", nil), 500, "InternalError", "vpcSet", cfn["VPC"], native)
			vpcConsoleFailure(t, f.ec2(tc.describe, nil), 500, "InternalError", tc.element, tc.id)
			if got := f.stackOutputs(); got["Subnet"] != cfn["Subnet"] || got["VPC"] != cfn["VPC"] {
				t.Fatalf("failed EC2 projection altered unrelated stack ownership: %+v", got)
			}
		})
		if got := f.network("DescribeVpcs", nil).VPCs; len(got) != 2 {
			t.Fatalf("repaired %s did not restore the full list: %+v", tc.label, got)
		}
	}
}
