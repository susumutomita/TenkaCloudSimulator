package aws

import (
	"errors"
	"strings"
	"testing"
)

func networkBase() map[string]map[string]any {
	ingress := []any{map[string]any{"IpProtocol": "tcp", "FromPort": "80", "ToPort": 8080, "CidrIp": "203.0.113.7/24", "Description": "web"}}
	return map[string]map[string]any{
		"AWS::EC2::VPC":                         {"CidrBlock": "10.1.0.0/16", "EnableDnsHostnames": "true", "EnableDnsSupport": true, "Tags": []any{map[string]any{"Key": "Name", "Value": "vpc"}}},
		"AWS::EC2::Subnet":                      {"VpcId": "vpc-1", "CidrBlock": "10.1.1.0/24", "AvailabilityZone": "us-east-1b", "MapPublicIpOnLaunch": "false"},
		"AWS::EC2::InternetGateway":             {},
		"AWS::EC2::VPCGatewayAttachment":        {"VpcId": "vpc-1", "InternetGatewayId": "igw-1"},
		"AWS::EC2::RouteTable":                  {"VpcId": "vpc-1"},
		"AWS::EC2::Route":                       {"RouteTableId": "rtb-1", "GatewayId": "igw-1", "DestinationCidrBlock": "0.0.0.0/0"},
		"AWS::EC2::SubnetRouteTableAssociation": {"RouteTableId": "rtb-1", "SubnetId": "subnet-1"},
		"AWS::EC2::SecurityGroup":               {"VpcId": "vpc-1", "GroupDescription": "web", "SecurityGroupIngress": ingress},
	}
}

func TestCompileNetworkResourceAcceptsCanonicalProperties(t *testing.T) {
	for kind, props := range networkBase() {
		r, err := compileNetworkResource("us-east-1", "stack", "Logical", kind, props)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if r.Kind != kind || r.ID == "" || r.Revision != 1 {
			t.Fatalf("%s compiled to %+v", kind, r)
		}
		again, _ := compileNetworkResource("us-east-1", "stack", "Logical", kind, props)
		if again.ID != r.ID {
			t.Fatalf("%s physical ID is not deterministic", kind)
		}
	}
	vpc, _ := compileNetworkResource("us-east-1", "stack", "V", "AWS::EC2::VPC", networkBase()["AWS::EC2::VPC"])
	if !vpc.DNSHostnames || !vpc.DNSSupport || vpc.Tags["Name"] != "vpc" {
		t.Fatalf("VPC booleans/tags not decoded: %+v", vpc)
	}
	subnet, _ := compileNetworkResource("us-east-1", "stack", "S", "AWS::EC2::Subnet", networkBase()["AWS::EC2::Subnet"])
	if subnet.AvailabilityZone != "us-east-1b" || subnet.PublicIP {
		t.Fatalf("subnet not decoded: %+v", subnet)
	}
	sg, _ := compileNetworkResource("us-east-1", "stack", "G", "AWS::EC2::SecurityGroup", networkBase()["AWS::EC2::SecurityGroup"])
	if len(sg.Ingress) != 1 || sg.Ingress[0] != (EC2IngressRule{"tcp", 80, 8080, "203.0.113.0/24"}) {
		t.Fatalf("ingress not canonicalized: %+v", sg.Ingress)
	}
	defaults, _ := compileNetworkResource("us-east-1", "stack", "D", "AWS::EC2::VPC", map[string]any{"CidrBlock": "10.2.0.0/16"})
	if defaults.DNSHostnames || !defaults.DNSSupport {
		t.Fatalf("VPC defaults wrong: %+v", defaults)
	}
}

func TestCompileNetworkResourceRefusesUnboundedProperties(t *testing.T) {
	rule := func(change func(map[string]any)) []any {
		r := map[string]any{"IpProtocol": "tcp", "FromPort": 80, "ToPort": 80, "CidrIp": "0.0.0.0/0"}
		change(r)
		return []any{r}
	}
	manyTags := []any{}
	for i := 0; i < 51; i++ {
		manyTags = append(manyTags, map[string]any{"Key": strings.Repeat("k", i+1), "Value": "v"})
	}
	manyRules := []any{}
	for i := 0; i < 61; i++ {
		manyRules = append(manyRules, map[string]any{"IpProtocol": "tcp", "FromPort": 80, "ToPort": 80, "CidrIp": "0.0.0.0/0"})
	}
	cases := []struct {
		name, kind, key string
		value           any
	}{
		{"unknown kind", "AWS::EC2::NatGateway", "", nil},
		{"unsupported property", "AWS::EC2::VPC", "InstanceTenancy", "default"},
		{"non-string CIDR", "AWS::EC2::VPC", "CidrBlock", 10},
		{"IPv6 CIDR", "AWS::EC2::VPC", "CidrBlock", "fd00::/48"},
		{"too large VPC", "AWS::EC2::VPC", "CidrBlock", "10.0.0.0/8"},
		{"non-boolean DNS", "AWS::EC2::VPC", "EnableDnsSupport", "yes"},
		{"hostnames without DNS", "AWS::EC2::VPC", "EnableDnsSupport", "false"},
		{"tags not a list", "AWS::EC2::VPC", "Tags", "Name=vpc"},
		{"too many tags", "AWS::EC2::VPC", "Tags", manyTags},
		{"reserved tag", "AWS::EC2::VPC", "Tags", []any{map[string]any{"Key": "aws:owner", "Value": "x"}}},
		{"tag without value", "AWS::EC2::VPC", "Tags", []any{map[string]any{"Key": "Name"}}},
		{"duplicate tag", "AWS::EC2::VPC", "Tags", []any{map[string]any{"Key": "a", "Value": "1"}, map[string]any{"Key": "a", "Value": "2"}}},
		{"subnet without VPC", "AWS::EC2::Subnet", "VpcId", ""},
		{"subnet bad CIDR", "AWS::EC2::Subnet", "CidrBlock", "10.1.1.0/30"},
		{"subnet bad public flag", "AWS::EC2::Subnet", "MapPublicIpOnLaunch", 1},
		{"remote availability zone", "AWS::EC2::Subnet", "AvailabilityZone", "us-west-2a"},
		{"non-string availability zone", "AWS::EC2::Subnet", "AvailabilityZone", 1},
		{"attachment without VPC", "AWS::EC2::VPCGatewayAttachment", "VpcId", nil},
		{"attachment without gateway", "AWS::EC2::VPCGatewayAttachment", "InternetGatewayId", strings.Repeat("g", 129)},
		{"route table without VPC", "AWS::EC2::RouteTable", "VpcId", 3},
		{"route without table", "AWS::EC2::Route", "RouteTableId", ""},
		{"route without gateway", "AWS::EC2::Route", "GatewayId", ""},
		{"route bad destination", "AWS::EC2::Route", "DestinationCidrBlock", "bad"},
		{"non-default route", "AWS::EC2::Route", "DestinationCidrBlock", "10.0.0.0/8"},
		{"association without subnet", "AWS::EC2::SubnetRouteTableAssociation", "SubnetId", ""},
		{"association without table", "AWS::EC2::SubnetRouteTableAssociation", "RouteTableId", ""},
		{"group without VPC", "AWS::EC2::SecurityGroup", "VpcId", ""},
		{"group without description", "AWS::EC2::SecurityGroup", "GroupDescription", ""},
		{"ingress not a list", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", "all"},
		{"too many ingress rules", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", manyRules},
		{"unsupported ingress field", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["SourceSecurityGroupId"] = "sg-1" })},
		{"udp ingress", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["IpProtocol"] = "udp" })},
		{"non-numeric from port", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["FromPort"] = "http" })},
		{"inverted port range", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["FromPort"] = 90 })},
		{"port beyond range", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["ToPort"] = 70000 })},
		{"ingress bad CIDR", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["CidrIp"] = "::/0" })},
		{"ingress bad description", "AWS::EC2::SecurityGroup", "SecurityGroupIngress", rule(func(r map[string]any) { r["Description"] = 5 })},
	}
	for _, tc := range cases {
		props := map[string]any{}
		for k, v := range networkBase()[tc.kind] {
			props[k] = v
		}
		if tc.key != "" {
			if tc.value == nil {
				delete(props, tc.key)
			} else {
				props[tc.key] = tc.value
			}
		}
		if _, err := compileNetworkResource("us-east-1", "stack", "Logical", tc.kind, props); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
}

// Every action/resource pair a network resource needs is individually required:
// denying any single one refuses the create or delete.
func TestStackNetworkPermissionsRequireEveryActionResourcePair(t *testing.T) {
	record, err := compileStack("us-east-1", "perm", networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range record.Resources {
		if !stackNetworkKind(r.Type) {
			continue
		}
		for _, deleting := range []bool{false, true} {
			var needed []string
			if err := stackNetworkPermissions(record, r, deleting, func(action, resource string) error {
				needed = append(needed, action+" "+resource)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(needed) == 0 {
				t.Fatalf("%s needs no permission", r.Type)
			}
			for _, denied := range needed {
				err := stackNetworkPermissions(record, r, deleting, func(action, resource string) error {
					if action+" "+resource == denied {
						return errors.New("denied")
					}
					return nil
				})
				if err == nil {
					t.Fatalf("%s (deleting=%v) admitted without %s", r.Type, deleting, denied)
				}
				checked++
			}
		}
	}
	if checked < 16 {
		t.Fatalf("only %d permission pairs checked", checked)
	}
}
