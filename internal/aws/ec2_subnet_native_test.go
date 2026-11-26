package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Exercise native Query/XML and the actual shared SQLite network store, not a
// separate topology model or an independently provisioned/cloud subnet.
func TestEC2NativeSubnetLifecycle(t *testing.T) {
	state, _ := s3TestState(t)
	stacks, err := NewStackStore(state.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(state)}
	const east, west = "us-east-1", "us-west-2"
	const admin = "simulator-admin"
	query := func(region, actor, service, action string, fields url.Values, status int, code string, handoff bool) []byte {
		t.Helper()
		if fields == nil {
			fields = url.Values{}
		}
		version := "2016-11-15"
		if service == "cloudformation" {
			version = "2010-05-15"
		}
		fields.Set("Action", action)
		fields.Set("Version", version)
		r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", service)
		r.Header.Set("X-Simulator-Region", region)
		r.Header.Set("X-Simulator-Principal", actor)
		if handoff {
			r = r.WithContext(context.WithValue(r.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-01000000"}))
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), "<Code>"+code+"</Code>") {
			t.Fatalf("%s/%s expected %d %s, got %d %s", service, action, status, code, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	ec2 := func(region, actor, action string, fields url.Values, status int, code string) []byte {
		t.Helper()
		return query(region, actor, "ec2", action, fields, status, code, false)
	}
	var vpc ec2CreateVpcResponse
	if err := xml.Unmarshal(ec2(east, admin, "CreateVpc", url.Values{"CidrBlock": {"10.70.0.0/16"}}, 200, ""), &vpc); err != nil || vpc.VPC.ID == "" {
		t.Fatal("missing native VPC")
	}
	const cfnTemplate = `{"Resources":{"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.72.0.0/16"}},"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.72.1.0/24"}}}}`
	query(east, admin, "cloudformation", "CreateStack", url.Values{"StackName": {"cfn-ownership"}, "TemplateBody": {cfnTemplate}}, 200, "", false)
	describe := func(region, actor string) []ec2SubnetRead {
		t.Helper()
		var doc ec2DescribeSubnetsResponse
		if err := xml.Unmarshal(ec2(region, actor, "DescribeSubnets", nil, 200, ""), &doc); err != nil || doc.XMLNS != "http://ec2.amazonaws.com/doc/2016-11-15/" || doc.RequestID == "" {
			t.Fatalf("unexpected DescribeSubnets XML: %v", err)
		}
		return doc.Subnets.Items
	}
	before := describe(east, admin)
	if len(before) != 1 || before[0].ManagedBy != "" || before[0].CIDR != "10.72.1.0/24" {
		t.Fatal("existing CloudFormation subnet missing")
	}
	policy := func(actor, document string) {
		t.Helper()
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":` + document + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Identities.Set(actor, p); err != nil {
			t.Fatal(err)
		}
	}
	vpcARN := networkARN(east, "vpc", vpc.VPC.ID)
	subnetARN := networkARN(east, "subnet", "*")
	policy("vpc-only", fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:CreateSubnet","Resource":%q}]`, vpcARN))
	policy("subnet-only", fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:CreateSubnet","Resource":%q}]`, subnetARN))
	// A denial that matches an allocated subnet ID (but not the wildcard
	// precheck) must still win over the broad create allow.
	concreteSubnetARN := networkARN(east, "subnet", "subnet-"+strings.Repeat("?", 17))
	policy("allocation-denied", fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:CreateSubnet","Resource":[%q,%q]},{"Effect":"Deny","Action":"ec2:CreateSubnet","Resource":%q}]`, vpcARN, subnetARN, concreteSubnetARN))
	policy("builder", fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:CreateSubnet","Resource":[%q,%q]},{"Effect":"Allow","Action":"ec2:DescribeSubnets","Resource":"*"}]`, vpcARN, subnetARN))
	create := func(vpcID, cidr, az string) url.Values {
		p := url.Values{"VpcId": {vpcID}, "CidrBlock": {cidr}}
		if az != "" {
			p.Set("AvailabilityZone", az)
		}
		return p
	}
	for _, actor := range []string{"vpc-only", "subnet-only", "unregistered", "allocation-denied"} {
		ec2(east, actor, "CreateSubnet", create(vpc.VPC.ID, "10.70.1.0/24", ""), 403, "UnauthorizedOperation")
	}
	query(east, admin, "ec2", "CreateSubnet", create(vpc.VPC.ID, "10.70.1.0/24", ""), 403, "UnauthorizedOperation", true)
	for _, test := range []struct{ name, region, vpc, cidr, az, code string }{
		{"foreign region", west, vpc.VPC.ID, "10.70.1.0/24", "", "InvalidVpcID.NotFound"},
		{"outside VPC", east, vpc.VPC.ID, "10.73.1.0/24", "", "InvalidSubnet.Range"},
		{"other zone", east, vpc.VPC.ID, "10.70.1.0/24", west + "a", "InvalidParameterValue"},
		{"stack VPC", east, before[0].VPC, "10.72.2.0/24", "", "UnsupportedOperation"},
		{"missing VPC", east, "vpc-01000000", "10.70.1.0/24", "", "InvalidVpcID.NotFound"},
		{"invalid VPC", east, "vpc-invalid", "10.70.1.0/24", "", "InvalidVpcID.Malformed"},
		{"IPv6 rejected", east, vpc.VPC.ID, "::/24", "", "InvalidSubnet.Range"},
		{"no CIDR", east, vpc.VPC.ID, "", "", "MissingParameter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ec2(test.region, admin, "CreateSubnet", create(test.vpc, test.cidr, test.az), 400, test.code)
		})
	}
	for field, value := range map[string]string{"DryRun": "true", "TagSpecification.1.ResourceType": "subnet", "Ipv6CidrBlock": "::/64"} {
		p := create(vpc.VPC.ID, "10.70.1.0/24", "")
		p.Set(field, value)
		ec2(east, admin, "CreateSubnet", p, 400, "UnsupportedOperation")
	}
	e := create(vpc.VPC.ID, "10.70.1.0/24", "")
	e.Add("VpcId", vpc.VPC.ID)
	ec2(east, admin, "CreateSubnet", e, 400, "InvalidParameterValue")
	if len(describe(east, "builder")) != 1 {
		t.Fatal("refused request mutated state")
	}
	var created ec2CreateSubnetResponse
	p := create(vpc.VPC.ID, "10.70.1.13/24", east+"b")
	p.Set("DryRun", "false")
	if err := xml.Unmarshal(ec2(east, "builder", "CreateSubnet", p, 200, ""), &created); err != nil {
		t.Fatal(err)
	}
	a := created.Subnet
	if !ec2SubnetIDPattern.MatchString(a.ID) || a.VPC != vpc.VPC.ID || a.CIDR != "10.70.1.0/24" || a.AZ != east+"b" || a.ManagedBy != "ec2" || a.State != "available" || created.RequestID == "" {
		t.Fatal("incorrect native creation receipt")
	}
	p = create(vpc.VPC.ID, "10.70.2.0/24", "")
	var second ec2CreateSubnetResponse
	if err := xml.Unmarshal(ec2(east, "builder", "CreateSubnet", p, 200, ""), &second); err != nil || second.Subnet.AZ != east+"a" || second.Subnet.ID == a.ID {
		t.Fatal("default zone/second subnet not saved")
	}
	ec2(east, "builder", "CreateSubnet", create(vpc.VPC.ID, "10.70.1.0/25", ""), 400, "InvalidSubnet.Conflict")
	ec2(east, admin, "DescribeVpcs", nil, 200, "") // Adding a native child must not invalidate VPC discovery.
	list := describe(east, "builder")
	if len(list) != 3 || len(describe(west, admin)) != 0 {
		t.Fatal("same-region native and stack children not both reported")
	}
	for _, want := range []ec2SubnetRead{a, second.Subnet, before[0]} {
		seen := false
		for _, row := range list {
			seen = seen || row.ID == want.ID && row.VPC == want.VPC && row.AZ == want.AZ && row.CIDR == want.CIDR
		}
		if !seen {
			t.Fatalf("saved row not observed: %s", want.ID)
		}
	}
	deleteByID := func(region, actor, id string, status int, code string) []byte {
		return ec2(region, actor, "DeleteSubnet", url.Values{"SubnetId": {id}}, status, code)
	}
	deleteByID(east, "builder", a.ID, 403, "UnauthorizedOperation")
	deleteByID(west, admin, a.ID, 400, "InvalidSubnetID.NotFound")
	deleteByID(east, admin, before[0].ID, 400, "UnsupportedOperation")
	deleteByID(east, admin, "", 400, "MissingParameter")
	deleteByID(east, admin, "subnet-invalid", 400, "InvalidSubnetID.Malformed")
	ec2(east, admin, "DeleteVpc", url.Values{"VpcId": {vpc.VPC.ID}}, 400, "DependencyViolation")
	ec2(east, "vpc-only", "DescribeSubnets", nil, 403, "UnauthorizedOperation")
	query(east, admin, "ec2", "DescribeSubnets", nil, 403, "UnauthorizedOperation", true)
	policy("exact-delete", fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:DeleteSubnet","Resource":%q}]`, networkARN(east, "subnet", a.ID)))
	deleteByID(east, "exact-delete", second.Subnet.ID, 403, "UnauthorizedOperation")
	var removed ec2DeleteSubnetResponse
	if err := xml.Unmarshal(deleteByID(east, "exact-delete", a.ID, 200, ""), &removed); err != nil || !removed.Return || removed.RequestID == "" {
		t.Fatal("missing successful delete acknowledgement")
	}
	deleteByID(east, admin, a.ID, 400, "InvalidSubnetID.NotFound")
	deleteByID(east, admin, second.Subnet.ID, 200, "")
	ec2(east, admin, "DeleteVpc", url.Values{"VpcId": {vpc.VPC.ID}}, 200, "")
	query(east, admin, "cloudformation", "DeleteStack", url.Values{"StackName": {"cfn-ownership"}}, 200, "", false)
	if len(describe(east, admin)) != 0 {
		t.Fatal("cleanup left a subnet")
	}
}
