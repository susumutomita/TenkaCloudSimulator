package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type ec2VPCFixture struct {
	t      *testing.T
	g      *Gateway
	stacks *StackStore
	store  *core.Store
}

func newEC2VPCFixture(t *testing.T) ec2VPCFixture {
	t.Helper()
	store, _ := s3TestState(t)
	stacks, err := NewStackStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	return ec2VPCFixture{t, &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}, stacks, store}
}

func (f ec2VPCFixture) call(principal, region string, ctx context.Context, values url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	if values.Get("Version") == "" {
		values.Set("Version", "2016-11-15")
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(values.Encode()))
	if ctx != nil {
		r = r.WithContext(ctx)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Service", "ec2")
	r.Header.Set("X-Simulator-Region", region)
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	return w
}

func (f ec2VPCFixture) expect(w *httptest.ResponseRecorder, status int, code string) string {
	f.t.Helper()
	body := w.Body.String()
	if w.Code != status || (code != "" && !strings.Contains(body, "<Code>"+code+"</Code>")) {
		f.t.Fatalf("want %d %s, got %d %s", status, code, w.Code, body)
	}
	return body
}

type describedVPC struct {
	ID        string `xml:"vpcId"`
	CIDR      string `xml:"cidrBlock"`
	ManagedBy string `xml:"simulatorManagedBy"`
	Tags      []struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	} `xml:"tagSet>item"`
}

func (f ec2VPCFixture) describe(region string) []describedVPC {
	f.t.Helper()
	var out struct {
		XMLName xml.Name
		VPCs    []describedVPC `xml:"vpcSet>item"`
	}
	body := f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeVpcs"}}), 200, "")
	if err := xml.Unmarshal([]byte(body), &out); err != nil || out.XMLName.Local != "DescribeVpcsResponse" || out.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" {
		f.t.Fatalf("invalid DescribeVpcs response %v: %s", err, body)
	}
	return out.VPCs
}

func (f ec2VPCFixture) create(principal string, values url.Values) describedVPC {
	f.t.Helper()
	values.Set("Action", "CreateVpc")
	body := f.expect(f.call(principal, "us-east-1", nil, values), 200, "")
	var out struct {
		XMLName   xml.Name
		RequestID string       `xml:"requestId"`
		VPC       describedVPC `xml:"vpc"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil || out.XMLName.Local != "CreateVpcResponse" || out.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || out.RequestID == "" {
		f.t.Fatalf("invalid CreateVpc response %v: %s", err, body)
	}
	if !regexp.MustCompile(`^vpc-[0-9a-f]{17}$`).MatchString(out.VPC.ID) || out.VPC.ManagedBy != "ec2" {
		f.t.Fatalf("unexpected VPC identity: %+v", out.VPC)
	}
	return out.VPC
}

func (f ec2VPCFixture) cloudFormationVPC(region, stack, id, cidr string) {
	f.t.Helper()
	db := f.store.Database()
	record := StackRecord{ID: stack, Name: "network", Region: region, Resources: []StackResource{{Type: "AWS::EC2::VPC", PhysicalID: id}}}
	data, _ := json.Marshal(record)
	if _, err := db.Exec(`INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, record.Region, record.Name, record.ID, string(data)); err != nil {
		f.t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(region, "AWS::EC2::VPC"), "AWS::EC2::VPC", id, stack); err != nil {
		f.t.Fatal(err)
	}
	f.writeNetwork(EC2NetworkResource{ID: id, Kind: "AWS::EC2::VPC", Region: region, StackID: stack, CIDR: cidr, Revision: 1})
}

func (f ec2VPCFixture) writeNetwork(r EC2NetworkResource) {
	f.t.Helper()
	tx, err := f.store.Database().Begin()
	if err != nil {
		f.t.Fatal(err)
	}
	if err = networkWrite(context.Background(), tx, r, true); err != nil {
		f.t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

func TestEC2CreateDescribeDeleteNativeVPCBesideCloudFormationVPC(t *testing.T) {
	f := newEC2VPCFixture(t)
	f.cloudFormationVPC("us-east-1", "arn:aws:cloudformation:us-east-1:000000000000:stack/network/one", "vpc-0000000000000000c", "10.1.0.0/16")
	created := f.create("", url.Values{"CidrBlock": {"10.20.30.40/16"},
		"TagSpecification.1.ResourceType": {"vpc"},
		"TagSpecification.1.Tag.1.Key":    {"Name"}, "TagSpecification.1.Tag.1.Value": {"学習 VPC"},
		"TagSpecification.1.Tag.2.Key": {"empty"}, "InstanceTenancy": {"default"}, "DryRun": {"false"}})
	if created.CIDR != "10.20.0.0/16" || len(created.Tags) != 2 || created.Tags[0].Key != "Name" || created.Tags[0].Value != "学習 VPC" || created.Tags[1].Key != "empty" || created.Tags[1].Value != "" {
		t.Fatalf("CreateVpc did not canonicalize CIDR or keep tags: %+v", created)
	}
	untagged := f.create("", url.Values{"CidrBlock": {"192.168.0.0/28"}})
	listed := f.describe("us-east-1")
	if len(listed) != 3 {
		t.Fatalf("DescribeVpcs must return both owners: %+v", listed)
	}
	byID := map[string]describedVPC{}
	for i, v := range listed {
		byID[v.ID] = v
		if i > 0 && listed[i-1].ID >= v.ID {
			t.Fatalf("DescribeVpcs must be ordered by ID: %+v", listed)
		}
	}
	if byID[created.ID].ManagedBy != "ec2" || byID[created.ID].CIDR != "10.20.0.0/16" || byID[untagged.ID].ManagedBy != "ec2" || len(byID[untagged.ID].Tags) != 0 || byID["vpc-0000000000000000c"].ManagedBy != "cloudformation" {
		t.Fatalf("unexpected ownership or state: %+v", byID)
	}
	if other := f.describe("ap-northeast-1"); len(other) != 0 {
		t.Fatalf("region leak: %+v", other)
	}
	f.expect(f.call("", "ap-northeast-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {created.ID}}), 400, "InvalidVpcID.NotFound")

	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {created.ID}}), 200, "")
	if !strings.Contains(body, "<DeleteVpcResponse xmlns=\"http://ec2.amazonaws.com/doc/2016-11-15/\">") || !strings.Contains(body, "<return>true</return>") {
		t.Fatal(body)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {created.ID}}), 400, "InvalidVpcID.NotFound")
	if after := f.describe("us-east-1"); len(after) != 2 || after[0].ID == created.ID || after[1].ID == created.ID {
		t.Fatalf("deleted VPC is still listed: %+v", after)
	}
}

func TestEC2CloudFormationVPCCannotBeDeletedOrAdoptedNatively(t *testing.T) {
	f := newEC2VPCFixture(t)
	stack := "arn:aws:cloudformation:us-east-1:000000000000:stack/network/two"
	f.cloudFormationVPC("us-east-1", stack, "vpc-0000000000000000d", "10.2.0.0/16")
	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {"vpc-0000000000000000d"}}), 400, "UnsupportedOperation")
	if !strings.Contains(body, "Delete its stack instead") {
		t.Fatal(body)
	}
	native := f.create("", url.Values{"CidrBlock": {"10.3.0.0/16"}})
	// A stack subnet cannot attach to an EC2-owned VPC: ownership is never adopted.
	tx, err := f.store.Database().Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = createEC2Network(context.Background(), tx, EC2NetworkResource{ID: "subnet-0000000000000000e", Kind: "AWS::EC2::Subnet", Region: "us-east-1", StackID: stack, VPCID: native.ID, CIDR: "10.3.1.0/24"})
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("a CloudFormation subnet adopted an EC2-owned VPC")
	}
	if listed := f.describe("us-east-1"); len(listed) != 2 {
		t.Fatalf("refused operations changed state: %+v", listed)
	}
}

func TestEC2CreateVpcRejectsInvalidAndUnsupportedInput(t *testing.T) {
	f := newEC2VPCFixture(t)
	for _, tc := range []struct {
		name   string
		values url.Values
		code   string
	}{
		{"missing CIDR", url.Values{}, "MissingParameter"},
		{"malformed CIDR", url.Values{"CidrBlock": {"10.0.0.0"}}, "InvalidParameterValue"},
		{"IPv6 CIDR", url.Values{"CidrBlock": {"2001:db8::/56"}}, "InvalidParameterValue"},
		{"too large", url.Values{"CidrBlock": {"10.0.0.0/15"}}, "InvalidVpc.Range"},
		{"too small", url.Values{"CidrBlock": {"10.0.0.0/29"}}, "InvalidVpc.Range"},
		{"repeated field", url.Values{"CidrBlock": {"10.0.0.0/16", "10.1.0.0/16"}}, "InvalidParameterValue"},
		{"IPAM", url.Values{"CidrBlock": {"10.0.0.0/16"}, "Ipv4IpamPoolId": {"ipam-pool-1"}}, "UnsupportedOperation"},
		{"IPv6", url.Values{"CidrBlock": {"10.0.0.0/16"}, "AmazonProvidedIpv6CidrBlock": {"true"}}, "UnsupportedOperation"},
		{"dry run", url.Values{"CidrBlock": {"10.0.0.0/16"}, "DryRun": {"true"}}, "UnsupportedOperation"},
		{"dedicated tenancy", url.Values{"CidrBlock": {"10.0.0.0/16"}, "InstanceTenancy": {"dedicated"}}, "UnsupportedOperation"},
		{"delete field", url.Values{"CidrBlock": {"10.0.0.0/16"}, "VpcId": {"vpc-00000000"}}, "UnsupportedOperation"},
		{"wrong resource type", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"subnet"}, "TagSpecification.1.Tag.1.Key": {"Name"}}, "InvalidParameterValue"},
		{"missing resource type", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.Tag.1.Key": {"Name"}}, "InvalidParameterValue"},
		{"second specification", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.2.ResourceType": {"vpc"}}, "UnsupportedOperation"},
		{"specification index", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.2.ResourceType": {"vpc"}}, "InvalidParameterValue"},
		{"tag gap", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.2.Key": {"Name"}}, "InvalidParameterValue"},
		{"value without key", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Value": {"x"}}, "InvalidParameterValue"},
		{"reserved prefix", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Key": {"aws:owner"}}, "InvalidParameterValue"},
		{"duplicate key", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.2.Key": {"Name"}}, "InvalidParameterValue"},
		{"long value", url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {strings.Repeat("v", 257)}}, "InvalidParameterValue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values.Set("Action", "CreateVpc")
			f.expect(f.call("", "us-east-1", nil, tc.values), 400, tc.code)
		})
	}
	many := url.Values{"CidrBlock": {"10.0.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}}
	for i := 1; i <= 51; i++ {
		many.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i), fmt.Sprintf("k%d", i))
		many.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i), strings.Repeat("v", 256))
	}
	many.Set("Action", "CreateVpc")
	f.expect(f.call("", "us-east-1", nil, many), 400, "InvalidParameterValue")
	many.Del("TagSpecification.1.Tag.51.Key")
	many.Del("TagSpecification.1.Tag.51.Value")
	if created := f.create("", many); len(created.Tags) != 50 {
		t.Fatalf("50 maximum-length tags fit the request bound: %d", len(created.Tags))
	}
	for _, tc := range []struct {
		values url.Values
		code   string
	}{
		{url.Values{}, "MissingParameter"},
		{url.Values{"VpcId": {"subnet-0000000000000000a"}}, "InvalidVpcID.Malformed"},
		{url.Values{"VpcId": {"vpc-XYZ"}}, "InvalidVpcID.Malformed"},
		{url.Values{"VpcId": {"vpc-0000000000000000f"}, "DryRun": {"true"}}, "UnsupportedOperation"},
		{url.Values{"VpcId": {"vpc-0000000000000000f"}, "CidrBlock": {"10.0.0.0/16"}}, "UnsupportedOperation"},
		{url.Values{"VpcId": {"vpc-0000000000000000f"}}, "InvalidVpcID.NotFound"},
	} {
		tc.values.Set("Action", "DeleteVpc")
		f.expect(f.call("", "us-east-1", nil, tc.values), 400, tc.code)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeVpcs"}, "CidrBlock": {"10.0.0.0/16"}}), 400, "UnsupportedOperation")
	if listed := f.describe("us-east-1"); len(listed) != 1 {
		t.Fatalf("rejected requests created VPCs: %+v", listed)
	}
}

func TestEC2VPCMutationsRequireIdentityPolicy(t *testing.T) {
	f := newEC2VPCFixture(t)
	set := func(document string) {
		t.Helper()
		policy, err := core.ParsePolicy([]byte(document))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.g.Identities.Set("learner", policy); err != nil {
			t.Fatal(err)
		}
	}
	tagged := url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.4.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Key": {"Name"}}
	f.expect(f.call("missing", "us-east-1", nil, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.4.0.0/16"}}), 403, "UnauthorizedOperation")
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateVpc","Resource":"arn:aws:ec2:us-east-1:000000000000:vpc/*"}]}`)
	f.expect(f.call("learner", "us-east-1", nil, tagged), 403, "UnauthorizedOperation")
	f.expect(f.call("learner", "ap-northeast-1", nil, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.4.0.0/16"}}), 403, "UnauthorizedOperation")
	if listed := f.describe("us-east-1"); len(listed) != 0 {
		t.Fatalf("denied CreateVpc changed state: %+v", listed)
	}
	untagged := f.create("learner", url.Values{"CidrBlock": {"10.4.0.0/16"}})
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {untagged.ID}}), 403, "UnauthorizedOperation")
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ec2:CreateVpc","ec2:CreateTags"],"Resource":"*"},{"Effect":"Allow","Action":"ec2:DeleteVpc","Resource":"arn:aws:ec2:us-east-1:000000000000:vpc/` + untagged.ID + `"}]}`)
	other := f.create("learner", tagged)
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {other.ID}}), 403, "UnauthorizedOperation")
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {untagged.ID}}), 200, "")
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:*","Resource":"*"},{"Effect":"Deny","Action":"ec2:DeleteVpc","Resource":"*"}]}`)
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {other.ID}}), 403, "UnauthorizedOperation")
	if listed := f.describe("us-east-1"); len(listed) != 1 || listed[0].ID != other.ID {
		t.Fatalf("authorization did not bound deletion: %+v", listed)
	}
	scoped := context.WithValue(context.WithValue(context.Background(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{"us-east-1", "i-00000000000000001"}), sdkPolicyContextKey{}, core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ec2:*"}, Resource: core.StringList{"*"}}}})
	f.expect(f.call("", "us-east-1", scoped, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.5.0.0/16"}}), 403, "UnauthorizedOperation")
	f.expect(f.call("", "us-east-1", scoped, url.Values{"Action": {"DeleteVpc"}, "VpcId": {other.ID}}), 403, "UnauthorizedOperation")
	if listed := f.describe("us-east-1"); len(listed) != 1 {
		t.Fatalf("instance console session mutated VPCs: %+v", listed)
	}
}

func TestEC2NativeVPCStateFailuresAreNotSuccess(t *testing.T) {
	if got := ec2Invalid("InvalidVpcID.NotFound", "missing").Error(); got != "InvalidVpcID.NotFound: missing" {
		t.Fatal(got)
	}
	f := newEC2VPCFixture(t)
	vpc := f.create("", url.Values{"CidrBlock": {"10.6.0.0/16"}})
	// A dependent row defends deletion even though native subnets are not offered.
	f.writeNetwork(EC2NetworkResource{ID: "subnet-0000000000000000a", Kind: "AWS::EC2::Subnet", Region: "us-east-1", StackID: ec2NativeOwner, VPCID: vpc.ID, CIDR: "10.6.1.0/24", Revision: 1})
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 400, "DependencyViolation")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeVpcs"}}), 500, "InternalError")
	db := f.store.Database()
	if _, err := db.Exec(`DELETE FROM aws_ec2_network_resources WHERE id='subnet-0000000000000000a'`); err != nil {
		t.Fatal(err)
	}
	claimant := StackRecord{ID: "arn:aws:cloudformation:us-east-1:000000000000:stack/x/y", Name: "x", Region: "us-east-1"}
	data, _ := json.Marshal(claimant)
	if _, err := db.Exec(`INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, claimant.Region, claimant.Name, claimant.ID, string(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace("us-east-1", "AWS::EC2::VPC"), "AWS::EC2::VPC", vpc.ID, claimant.ID); err != nil {
		t.Fatal(err)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeVpcs"}}), 500, "InternalError")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 500, "InternalError")
	if _, err := db.Exec(`DELETE FROM aws_cfn_ownership WHERE physical_id=?`, vpc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE aws_ec2_network_resources SET revision=7 WHERE id=?`, vpc.ID); err != nil {
		t.Fatal(err)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 500, "InternalError")
	if _, err := db.Exec(`DELETE FROM aws_ec2_native_acl_defaults; DELETE FROM aws_ec2_network_resources`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		r := EC2NetworkResource{ID: fmt.Sprintf("vpc-%017x", i), Kind: "AWS::EC2::VPC", Region: "us-east-1", StackID: "arn:aws:cloudformation:us-east-1:000000000000:stack/q/q", CIDR: "10.7.0.0/16", Revision: 1}
		if err = networkWrite(context.Background(), tx, r, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.8.0.0/16"}}), 400, "VpcLimitExceeded")
	if _, err = db.Exec(`DELETE FROM aws_ec2_network_resources`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateVpc"}, "CidrBlock": {"10.8.0.0/16"}}), 500, "InternalError")
	if strings.Contains(body, "database") || strings.Contains(body, "sqlite") {
		t.Fatalf("storage failure leaked internals: %s", body)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 500, "InternalError")
}
