package aws

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Immutable user-selected original fixture, not simulator-generated expected
// output. CFN resource fields are checked against AWS's resource specification.
func TestOriginalBattleNetworkInputs(t *testing.T) {
	raw, e := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "4de530625bd2a3c0b5a5881f511061709c94c9eb6884d8d6fd1037f3d9bc6bc5" {
		t.Fatal("original template changed")
	}
	var node yaml.Node
	if e = yaml.Unmarshal(raw, &node); e != nil {
		t.Fatal(e)
	}
	parsed, e := taggedYAML(&node)
	if e != nil {
		t.Fatal(e)
	}
	resources := object(object(parsed)["Resources"])
	expected := map[string]string{"Vpc": "AWS::EC2::VPC", "PublicSubnet": "AWS::EC2::Subnet", "Igw": "AWS::EC2::InternetGateway", "IgwAttach": "AWS::EC2::VPCGatewayAttachment", "Rt": "AWS::EC2::RouteTable", "RtRoute": "AWS::EC2::Route", "RtAssoc": "AWS::EC2::SubnetRouteTableAssociation", "Sg": "AWS::EC2::SecurityGroup"}
	for logical, kind := range expected {
		if stringValue(object(resources[logical])["Type"]) != kind {
			t.Fatal(logical)
		}
	}
	if object(object(resources["Vpc"])["Properties"])["CidrBlock"] != "10.99.0.0/16" {
		t.Fatal("VPC CIDR")
	}
	if object(object(resources["PublicSubnet"])["Properties"])["CidrBlock"] != "10.99.1.0/24" {
		t.Fatal("subnet CIDR")
	}
	if object(object(resources["RtRoute"])["Properties"])["DestinationCidrBlock"] != "0.0.0.0/0" {
		t.Fatal("default route")
	}
	if object(resources["RtRoute"])["DependsOn"] != "IgwAttach" {
		t.Fatal("route attachment dependency")
	}
	// These empty outputs intentionally prohibit scoring merely after deployment.
	outputs := object(object(parsed)["Outputs"])
	for _, key := range []string{"FrontendUrl", "ApiUrl"} {
		if object(outputs[key])["Value"] != "" {
			t.Fatal(key)
		}
	}
}

func TestOriginalBattleFullTemplateCompile(t *testing.T) {
	raw, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if err != nil {
		t.Fatal(err)
	}
	record, err := compileStackWithParameters("us-east-1", "original-battle", string(raw), map[string]string{"NamePrefix": "tc-hello-team", "TenkaCloudAccountId": "000000000000", "ExternalId": "fixture-external-id-only"}, "CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
		values := map[string]string{}
		for key := range keys {
			values[key] = "ami-local-pinned"
		}
		return values, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Resources) != 12 {
		t.Fatalf("resources=%d", len(record.Resources))
	}
	if record.Outputs["FrontendUrl"] != "" || record.Outputs["ApiUrl"] != "" {
		t.Fatal("template endpoint defaults rewritten")
	}
	if record.Outputs["Ec2HostHint"] != EC2InstancePublicDNSName(networkID(record, "Ec2")) {
		t.Fatalf("unexpected local hint %q", record.Outputs["Ec2HostHint"])
	}
	calls := map[string]bool{}
	if err = stackResourcePermissions(record, false, func(action, resource string) error { calls[action+" "+resource] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !calls["ec2:RunInstances "+networkARN(record.Region, "instance", networkID(record, "Ec2"))] {
		t.Fatal("missing RunInstances instance admission")
	}
}
