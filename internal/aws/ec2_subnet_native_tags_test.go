package aws

import (
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestEC2NativeSubnetTagOnCreate(t *testing.T) {
	const region = "us-east-1"
	const admin = core.Administrator
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() (*core.Store, *Gateway) {
		t.Helper()
		store, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		stacks, err := NewStackStore(store.Database())
		if err != nil {
			t.Fatal(err)
		}
		return store, &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	}
	store, gateway := open()
	rawQuery := func(region, service, actor, action string, form url.Values, status int, code string) []byte {
		t.Helper()
		if form == nil {
			form = url.Values{}
		}
		form.Set("Action", action)
		version := "2016-11-15"
		if service == "cloudformation" {
			version = "2010-05-15"
		}
		form.Set("Version", version)
		request := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("X-Simulator-Service", service)
		request.Header.Set("X-Simulator-Region", region)
		request.Header.Set("X-Simulator-Principal", actor)
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, request)
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), "<Code>"+code+"</Code>") {
			t.Fatalf("%s as %s: got %d %s, want %d %s", action, actor, w.Code, w.Body.String(), status, code)
		}
		return w.Body.Bytes()
	}
	query := func(actor, action string, form url.Values, status int, code string) []byte {
		t.Helper()
		return rawQuery(region, "ec2", actor, action, form, status, code)
	}
	var vpc ec2CreateVpcResponse
	if err := xml.Unmarshal(query(admin, "CreateVpc", url.Values{"CidrBlock": {"10.95.0.0/16"}}, 200, ""), &vpc); err != nil || vpc.VPC.ID == "" {
		t.Fatalf("invalid VPC receipt: %v", err)
	}
	const template = `{"Resources":{"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.96.0.0/16"}},"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.96.1.0/24","Tags":[{"Key":"Name","Value":"stack-owned"}]}}}}`
	rawQuery(region, "cloudformation", admin, "CreateStack", url.Values{"StackName": {"subnet-tag-stack"}, "TemplateBody": {template}}, 200, "")
	describe := func(r string) []ec2SubnetRead {
		t.Helper()
		var doc ec2DescribeSubnetsResponse
		if err := xml.Unmarshal(rawQuery(r, "ec2", admin, "DescribeSubnets", nil, 200, ""), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Subnets.Items
	}
	cfn := describe(region)
	if len(cfn) != 1 || !reflect.DeepEqual(cfn[0].Tags, []ec2Tag{{"Name", "stack-owned"}}) {
		t.Fatalf("expected unchanged CFN tag readback, got %+v", cfn)
	}
	policy := func(actor, extra string) {
		t.Helper()
		vpcARN := networkARN(region, "vpc", vpc.VPC.ID)
		subnetARN := networkARN(region, "subnet", "*")
		raw := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateSubnet","Resource":[%q,%q]}%s]}`, vpcARN, subnetARN, extra)
		p, err := core.ParsePolicy([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := gateway.Identities.Set(actor, p); err != nil {
			t.Fatal(err)
		}
	}
	tagAllow := fmt.Sprintf(`,{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, networkARN(region, "subnet", "*"))
	policy("subnet-no-tags", "")
	policy("subnet-tag-builder", tagAllow)
	policy("subnet-tag-deny-exact", tagAllow+fmt.Sprintf(`,{"Effect":"Deny","Action":"ec2:CreateTags","Resource":%q}`, networkARN(region, "subnet", "subnet-"+strings.Repeat("?", 17))))
	form := func(parent, cidr string, withName bool) url.Values {
		args := url.Values{"VpcId": {parent}, "CidrBlock": {cidr}}
		if withName {
			for k, v := range map[string]string{
				"TagSpecification.1.ResourceType": "subnet",
				"TagSpecification.1.Tag.1.Key":    "Name",
				"TagSpecification.1.Tag.1.Value":  "lab <private> & 東京",
				"TagSpecification.1.Tag.2.Key":    "Stage",
			} {
				args.Set(k, v)
			}
		}
		return args
	}
	query("subnet-no-tags", "CreateSubnet", form(vpc.VPC.ID, "10.95.1.0/24", true), 403, "UnauthorizedOperation")
	query("subnet-tag-deny-exact", "CreateSubnet", form(vpc.VPC.ID, "10.95.1.0/24", true), 403, "UnauthorizedOperation")
	query(admin, "CreateSubnet", form(cfn[0].VPC, "10.96.2.0/24", true), 400, "UnsupportedOperation")
	for _, invalid := range []struct{ key, value, code string }{
		{"Name", "not-an-api-field", "UnsupportedOperation"},
		{"TagSpecification.1.ResourceType", "vpc", "InvalidParameterValue"},
		{"TagSpecification.2.ResourceType", "subnet", "UnsupportedOperation"},
		{"TagSpecification.1.Tag.2.Key", "Name", "InvalidParameterValue"},
		{"TagSpecification.1.Tag.2.Key", "AWS:reserved", "InvalidParameterValue"},
		{"TagSpecification.1.Tag.4.Key", "gap", "InvalidParameterValue"},
		{"TagSpecification.1.Tag.51.Key", "over-bound", "InvalidParameterValue"},
		{"TagSpecification.1.Tag.2.Value", strings.Repeat("a", 257), "InvalidParameterValue"},
		{"TagSpecification.1.Tag.2.Value", "\x00", "InvalidParameterValue"},
	} {
		args := form(vpc.VPC.ID, "10.95.1.0/24", true)
		args.Set(invalid.key, invalid.value)
		query(admin, "CreateSubnet", args, 400, invalid.code)
	}
	args := form(vpc.VPC.ID, "10.95.1.0/24", true)
	args.Add("TagSpecification.1.Tag.1.Key", "Name")
	query(admin, "CreateSubnet", args, 400, "InvalidParameterValue")
	args = form(vpc.VPC.ID, "10.95.1.0/24", true)
	args.Del("TagSpecification.1.ResourceType")
	query(admin, "CreateSubnet", args, 400, "InvalidParameterValue")
	args = form(vpc.VPC.ID, "10.95.1.0/24", false)
	args.Set("TagSpecification.1.ResourceType", "subnet")
	query(admin, "CreateSubnet", args, 400, "InvalidParameterValue")
	if got := describe(region); !reflect.DeepEqual(got, cfn) {
		t.Fatal("a rejected create altered durable state")
	}
	save := func(actor, block string, tagged bool) ec2SubnetRead {
		t.Helper()
		var doc ec2CreateSubnetResponse
		if err := xml.Unmarshal(query(actor, "CreateSubnet", form(vpc.VPC.ID, block, tagged), 200, ""), &doc); err != nil || !ec2SubnetIDPattern.MatchString(doc.Subnet.ID) {
			t.Fatalf("invalid subnet receipt: %v", err)
		}
		return doc.Subnet
	}
	tagged := save("subnet-tag-builder", "10.95.1.0/24", true)
	bare := save("subnet-no-tags", "10.95.2.0/24", false)
	tags := []ec2Tag{{"Name", "lab <private> & 東京"}, {"Stage", ""}}
	if !reflect.DeepEqual(tagged.Tags, tags) || len(bare.Tags) != 0 {
		t.Fatalf("incorrect creation tag receipt: tagged=%+v bare=%+v", tagged.Tags, bare.Tags)
	}
	query(admin, "ModifySubnetAttribute", url.Values{"SubnetId": {tagged.ID}, "MapPublicIpOnLaunch.Value": {"true"}}, 200, "")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, gateway = open()
	rows := describe(region)
	if len(rows) != 3 || len(describe("us-west-2")) != 0 {
		t.Fatalf("incorrect durable regional inventory: %+v", rows)
	}
	for _, row := range rows {
		switch row.ID {
		case tagged.ID:
			if !reflect.DeepEqual(row.Tags, tags) || row.VPC != vpc.VPC.ID || row.PublicIPv4 == nil || !*row.PublicIPv4 {
				t.Fatalf("wrong saved native row: %+v", row)
			}
		case bare.ID:
			if len(row.Tags) != 0 {
				t.Fatal("untagged subnet received unexpected tags")
			}
		case cfn[0].ID:
			if !reflect.DeepEqual(row, cfn[0]) {
				t.Fatalf("CFN row changed: %+v", row)
			}
		default:
			t.Fatalf("unexpected subnet %s after reopen", row.ID)
		}
	}
	for _, id := range []string{tagged.ID, bare.ID} {
		query(admin, "DeleteSubnet", url.Values{"SubnetId": {id}}, 200, "")
	}
	if got := describe(region); !reflect.DeepEqual(got, cfn) {
		t.Fatal("native delete changed CFN subnet state")
	}
	rawQuery(region, "cloudformation", admin, "DeleteStack", url.Values{"StackName": {"subnet-tag-stack"}}, 200, "")
	query(admin, "DeleteVpc", url.Values{"VpcId": {vpc.VPC.ID}}, 200, "")
}
