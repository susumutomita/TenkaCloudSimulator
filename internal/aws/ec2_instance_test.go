package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const ec2TestNamespace = "http://ec2.amazonaws.com/doc/2016-11-15/"

type describedSubnet struct {
	ID        string `xml:"subnetId"`
	VPC       string `xml:"vpcId"`
	CIDR      string `xml:"cidrBlock"`
	State     string `xml:"state"`
	Zone      string `xml:"availabilityZone"`
	ManagedBy string `xml:"simulatorManagedBy"`
	Tags      []struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	} `xml:"tagSet>item"`
}

type describedInstance struct {
	ID        string `xml:"instanceId"`
	Image     string `xml:"imageId"`
	Type      string `xml:"instanceType"`
	Subnet    string `xml:"subnetId"`
	VPC       string `xml:"vpcId"`
	Runtime   string `xml:"simulatorRuntimeState"`
	ManagedBy string `xml:"simulatorManagedBy"`
	Groups    []struct {
		ID string `xml:"groupId"`
	} `xml:"groupSet>item"`
	Tags []struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	} `xml:"tagSet>item"`
}

func (f ec2VPCFixture) decode(body, root string, out any) {
	f.t.Helper()
	var name struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(body), &name); err != nil || name.XMLName.Local != root || name.XMLName.Space != ec2TestNamespace || !strings.Contains(body, "<requestId>ec2-local-") {
		f.t.Fatalf("invalid %s envelope %v: %s", root, err, body)
	}
	if err := xml.Unmarshal([]byte(body), out); err != nil {
		f.t.Fatalf("invalid %s body %v: %s", root, err, body)
	}
}

func (f ec2VPCFixture) subnet(principal, region string, values url.Values) describedSubnet {
	f.t.Helper()
	values.Set("Action", "CreateSubnet")
	var out struct {
		Subnet describedSubnet `xml:"subnet"`
	}
	f.decode(f.expect(f.call(principal, region, nil, values), 200, ""), "CreateSubnetResponse", &out)
	if !regexp.MustCompile(`^subnet-[0-9a-f]{17}$`).MatchString(out.Subnet.ID) || out.Subnet.ManagedBy != "ec2" || out.Subnet.State != "available" {
		f.t.Fatalf("unexpected subnet identity: %+v", out.Subnet)
	}
	return out.Subnet
}

func (f ec2VPCFixture) subnets(region string) []describedSubnet {
	f.t.Helper()
	var out struct {
		Subnets []describedSubnet `xml:"subnetSet>item"`
	}
	f.decode(f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeSubnets"}}), 200, ""), "DescribeSubnetsResponse", &out)
	return out.Subnets
}

func launchValues(subnet string) url.Values {
	return url.Values{"ImageId": {"ami-0123456789abcdef0"}, "InstanceType": {"t3.micro"}, "MinCount": {"1"}, "MaxCount": {"1"}, "SubnetId": {subnet}}
}

func (f ec2VPCFixture) run(principal, region string, values url.Values) describedInstance {
	f.t.Helper()
	values.Set("Action", "RunInstances")
	body := f.expect(f.call(principal, region, nil, values), 200, "")
	var out struct {
		Owner     string              `xml:"ownerId"`
		Instances []describedInstance `xml:"instancesSet>item"`
	}
	f.decode(body, "RunInstancesResponse", &out)
	if out.Owner != "000000000000" || len(out.Instances) != 1 || !regexp.MustCompile(`^i-[0-9a-f]{17}$`).MatchString(out.Instances[0].ID) {
		f.t.Fatalf("unexpected RunInstances result: %s", body)
	}
	// No guest exists, so no AWS runtime state or address is ever reported.
	for _, fabricated := range []string{"<instanceState>", "running", "pending", "privateIpAddress", "dnsName", "launchTime"} {
		if strings.Contains(body, fabricated) {
			f.t.Fatalf("RunInstances fabricated %s: %s", fabricated, body)
		}
	}
	return out.Instances[0]
}

func (f ec2VPCFixture) instances(region string) []describedInstance {
	f.t.Helper()
	var out struct {
		Reservations []struct {
			Owner     string              `xml:"ownerId"`
			Instances []describedInstance `xml:"instancesSet>item"`
		} `xml:"reservationSet>item"`
	}
	f.decode(f.expect(f.call("", region, nil, url.Values{"Action": {"DescribeInstances"}}), 200, ""), "DescribeInstancesResponse", &out)
	all := []describedInstance{}
	for _, reservation := range out.Reservations {
		if reservation.Owner != "000000000000" || len(reservation.Instances) != 1 {
			f.t.Fatalf("unexpected reservation: %+v", reservation)
		}
		all = append(all, reservation.Instances[0])
	}
	return all
}

func (f ec2VPCFixture) network(region, cidr, subnetCIDR string) (describedVPC, describedSubnet) {
	f.t.Helper()
	values := url.Values{"Action": {"CreateVpc"}, "CidrBlock": {cidr}}
	var out struct {
		VPC describedVPC `xml:"vpc"`
	}
	f.decode(f.expect(f.call("", region, nil, values), 200, ""), "CreateVpcResponse", &out)
	return out.VPC, f.subnet("", region, url.Values{"VpcId": {out.VPC.ID}, "CidrBlock": {subnetCIDR}})
}

// cloudFormationInstance seeds one stack that owns a VPC, subnet, security
// group and instance, as the CloudFormation path stores them.
func (f ec2VPCFixture) cloudFormationInstance(region, stack string) {
	f.t.Helper()
	db := f.store.Database()
	record := StackRecord{ID: stack, Name: "battle", Region: region, Resources: []StackResource{
		{Type: "AWS::EC2::VPC", PhysicalID: "vpc-00000000000000c01"}, {Type: "AWS::EC2::Subnet", PhysicalID: "subnet-00000000000000c01"},
		{Type: "AWS::EC2::Instance", PhysicalID: "i-00000000000000c01", Properties: map[string]any{"ImageId": "ami-local", "InstanceType": "t3.small", "SubnetId": "subnet-00000000000000c01", "SecurityGroupIds": []any{"sg-00000000000000c01"}}}}}
	data, _ := json.Marshal(record)
	if _, err := db.Exec(`INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, record.Region, record.Name, record.ID, string(data)); err != nil {
		f.t.Fatal(err)
	}
	for _, r := range record.Resources {
		if _, err := db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(region, r.Type), r.Type, r.PhysicalID, stack); err != nil {
			f.t.Fatal(err)
		}
	}
	f.writeNetwork(EC2NetworkResource{ID: "vpc-00000000000000c01", Kind: "AWS::EC2::VPC", Region: region, StackID: stack, CIDR: "10.90.0.0/16", Revision: 1})
	f.writeNetwork(EC2NetworkResource{ID: "subnet-00000000000000c01", Kind: "AWS::EC2::Subnet", Region: region, StackID: stack, VPCID: "vpc-00000000000000c01", CIDR: "10.90.1.0/24", AvailabilityZone: region + "c", Revision: 1})
	f.writeNetwork(EC2NetworkResource{ID: "sg-00000000000000c01", Kind: "AWS::EC2::SecurityGroup", Region: region, StackID: stack, VPCID: "vpc-00000000000000c01", Revision: 1})
}

func TestEC2NativeSubnetLifecycleBesideCloudFormationSubnet(t *testing.T) {
	f := newEC2VPCFixture(t)
	stack := "arn:aws:cloudformation:us-east-1:000000000000:stack/battle/one"
	f.cloudFormationInstance("us-east-1", stack)
	vpc := f.create("", url.Values{"CidrBlock": {"10.20.0.0/16"}})
	first := f.subnet("", "us-east-1", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.20.1.77/24"}, "DryRun": {"false"},
		"TagSpecification.1.ResourceType": {"subnet"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"公開サブネット"}})
	if first.VPC != vpc.ID || first.CIDR != "10.20.1.0/24" || first.Zone != "us-east-1a" || len(first.Tags) != 1 || first.Tags[0].Value != "公開サブネット" {
		t.Fatalf("CreateSubnet did not canonicalize the CIDR, default the zone or keep tags: %+v", first)
	}
	second := f.subnet("", "us-east-1", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.20.2.0/28"}, "AvailabilityZone": {"us-east-1b"}})
	if second.Zone != "us-east-1b" || len(second.Tags) != 0 {
		t.Fatalf("unexpected second subnet: %+v", second)
	}
	listed := f.subnets("us-east-1")
	if len(listed) != 3 {
		t.Fatalf("DescribeSubnets must return both owners: %+v", listed)
	}
	owners := map[string]describedSubnet{}
	for i, s := range listed {
		owners[s.ID] = s
		if i > 0 && listed[i-1].ID >= s.ID {
			t.Fatalf("DescribeSubnets must be ordered by ID: %+v", listed)
		}
	}
	if owners[first.ID].ManagedBy != "ec2" || owners[second.ID].Zone != "us-east-1b" || owners["subnet-00000000000000c01"].ManagedBy != "cloudformation" || owners["subnet-00000000000000c01"].Zone != "us-east-1c" {
		t.Fatalf("unexpected subnet owners: %+v", owners)
	}
	if other := f.subnets("ap-northeast-1"); len(other) != 0 {
		t.Fatalf("region leak: %+v", other)
	}
	// A VPC with subnets cannot be deleted; a subnet in another region is absent.
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 400, "DependencyViolation")
	f.expect(f.call("", "ap-northeast-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {first.ID}}), 400, "InvalidSubnetID.NotFound")
	for _, id := range []string{first.ID, second.ID} {
		body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {id}, "DryRun": {"false"}}), 200, "")
		if !strings.Contains(body, `<DeleteSubnetResponse xmlns="`+ec2TestNamespace+`">`) || !strings.Contains(body, "<return>true</return>") {
			t.Fatal(body)
		}
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {first.ID}}), 400, "InvalidSubnetID.NotFound")
	if after := f.subnets("us-east-1"); len(after) != 1 || after[0].ManagedBy != "cloudformation" {
		t.Fatalf("deleted subnets are still listed or the stack subnet was removed: %+v", after)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 200, "")
}

func TestEC2CreateAndDeleteSubnetRejectInvalidUnsupportedAndForeignInput(t *testing.T) {
	f := newEC2VPCFixture(t)
	stack := "arn:aws:cloudformation:us-east-1:000000000000:stack/battle/two"
	f.cloudFormationInstance("us-east-1", stack)
	vpc := f.create("", url.Values{"CidrBlock": {"10.30.0.0/20"}})
	taken := f.subnet("", "us-east-1", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.1.0/24"}})
	for _, tc := range []struct {
		name   string
		values url.Values
		code   string
	}{
		{"missing VPC", url.Values{"CidrBlock": {"10.30.2.0/24"}}, "MissingParameter"},
		{"malformed VPC", url.Values{"VpcId": {taken.ID}, "CidrBlock": {"10.30.2.0/24"}}, "InvalidVpcID.Malformed"},
		{"missing CIDR", url.Values{"VpcId": {vpc.ID}}, "MissingParameter"},
		{"malformed CIDR", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0"}}, "InvalidParameterValue"},
		{"IPv6 CIDR", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"2001:db8::/64"}}, "InvalidParameterValue"},
		{"netmask too small", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/29"}}, "InvalidSubnet.Range"},
		{"larger than the VPC", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.0.0/16"}}, "InvalidSubnet.Range"},
		{"outside the VPC", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.31.0.0/24"}}, "InvalidSubnet.Range"},
		{"overlap", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.1.128/25"}}, "InvalidSubnet.Conflict"},
		{"containing overlap", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.0.0/22"}}, "InvalidSubnet.Conflict"},
		{"unknown zone", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "AvailabilityZone": {"us-east-1z"}}, "InvalidParameterValue"},
		{"zone of another region", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "AvailabilityZone": {"us-west-2a"}}, "InvalidParameterValue"},
		{"empty zone is not defaulted", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "AvailabilityZone": {""}}, "InvalidParameterValue"},
		{"zone ID", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "AvailabilityZoneId": {"use1-az1"}}, "UnsupportedOperation"},
		{"IPv6 member", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "Ipv6CidrBlock": {"2001:db8::/64"}}, "UnsupportedOperation"},
		{"dry run", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "DryRun": {"true"}}, "UnsupportedOperation"},
		{"wrong resource type", url.Values{"VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Key": {"Name"}}, "InvalidParameterValue"},
		{"unknown VPC", url.Values{"VpcId": {"vpc-0000000000000000f"}, "CidrBlock": {"10.30.2.0/24"}}, "InvalidVpcID.NotFound"},
		{"CloudFormation VPC", url.Values{"VpcId": {"vpc-00000000000000c01"}, "CidrBlock": {"10.90.2.0/24"}}, "UnsupportedOperation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values.Set("Action", "CreateSubnet")
			f.expect(f.call("", "us-east-1", nil, tc.values), 400, tc.code)
		})
	}
	f.expect(f.call("", "ap-northeast-1", nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.30.2.0/24"}}), 400, "InvalidVpcID.NotFound")
	for _, tc := range []struct {
		values url.Values
		code   string
	}{
		{url.Values{}, "MissingParameter"},
		{url.Values{"SubnetId": {vpc.ID}}, "InvalidSubnetID.Malformed"},
		{url.Values{"SubnetId": {"subnet-XYZ"}}, "InvalidSubnetID.Malformed"},
		{url.Values{"SubnetId": {taken.ID}, "DryRun": {"true"}}, "UnsupportedOperation"},
		{url.Values{"SubnetId": {taken.ID}, "VpcId": {vpc.ID}}, "UnsupportedOperation"},
		{url.Values{"SubnetId": {"subnet-0000000000000000f"}}, "InvalidSubnetID.NotFound"},
		{url.Values{"SubnetId": {"subnet-00000000000000c01"}}, "UnsupportedOperation"},
	} {
		tc.values.Set("Action", "DeleteSubnet")
		f.expect(f.call("", "us-east-1", nil, tc.values), 400, tc.code)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeSubnets"}, "SubnetId.1": {taken.ID}}), 400, "UnsupportedOperation")
	if listed := f.subnets("us-east-1"); len(listed) != 2 {
		t.Fatalf("rejected requests changed the subnets: %+v", listed)
	}
	// Neither owner adopts the other's network: a stack route table association
	// cannot attach to an EC2-owned subnet.
	tx, err := f.store.Database().Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = createEC2Network(context.Background(), tx, EC2NetworkResource{ID: "rtbassoc-0000000000000000e", Kind: "AWS::EC2::SubnetRouteTableAssociation", Region: "us-east-1", StackID: stack, SubnetID: taken.ID, RouteTableID: "rtb-0000000000000000e"})
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("a CloudFormation association adopted an EC2-owned subnet")
	}
	// A stack instance cannot be placed in an EC2-owned subnet either: the
	// network policy snapshot that every guest launch needs is refused.
	network, err := NewEC2NetworkStore(f.store.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = network.Snapshot(context.Background(), "us-east-1", stack, taken.ID, []string{"sg-00000000000000c01"}); err == nil {
		t.Fatal("a CloudFormation instance was placed in an EC2-owned subnet")
	}
	if _, err = network.Snapshot(context.Background(), "us-east-1", stack, "subnet-00000000000000c01", []string{"sg-00000000000000c01"}); err != nil {
		t.Fatalf("the stack's own subnet must stay usable: %v", err)
	}
}

func TestEC2RunDescribeTerminateNativeInstanceBesideCloudFormationInstance(t *testing.T) {
	f := newEC2VPCFixture(t)
	stack := "arn:aws:cloudformation:us-east-1:000000000000:stack/battle/three"
	f.cloudFormationInstance("us-east-1", stack)
	vpc, subnet := f.network("us-east-1", "10.40.0.0/16", "10.40.1.0/24")
	values := launchValues(subnet.ID)
	values.Set("DryRun", "false")
	values.Set("TagSpecification.1.ResourceType", "instance")
	values.Set("TagSpecification.1.Tag.1.Key", "Name")
	values.Set("TagSpecification.1.Tag.1.Value", "練習用サーバー")
	values.Set("TagSpecification.1.Tag.2.Key", "team")
	launched := f.run("", "us-east-1", values)
	if launched.Image != "ami-0123456789abcdef0" || launched.Type != "t3.micro" || launched.Subnet != subnet.ID || launched.VPC != vpc.ID || launched.Runtime != "not_launched" || launched.ManagedBy != "ec2" || len(launched.Groups) != 0 || len(launched.Tags) != 2 || launched.Tags[0].Value != "練習用サーバー" || launched.Tags[1].Key != "team" {
		t.Fatalf("unexpected launch record: %+v", launched)
	}
	other := launchValues(subnet.ID)
	other.Set("ImageId", "ami-12345678")
	other.Set("InstanceType", "t3.small")
	second := f.run("", "us-east-1", other)
	listed := f.instances("us-east-1")
	if len(listed) != 3 {
		t.Fatalf("DescribeInstances must return both owners: %+v", listed)
	}
	byID := map[string]describedInstance{}
	for i, v := range listed {
		byID[v.ID] = v
		if i > 0 && listed[i-1].ID >= v.ID {
			t.Fatalf("DescribeInstances must be ordered by ID: %+v", listed)
		}
	}
	stackInstance := byID["i-00000000000000c01"]
	if byID[launched.ID].Runtime != "not_launched" || len(byID[launched.ID].Tags) != 2 || byID[second.ID].Type != "t3.small" || byID[second.ID].Image != "ami-12345678" ||
		stackInstance.ManagedBy != "cloudformation" || stackInstance.Runtime != "unknown" || len(stackInstance.Groups) != 1 {
		t.Fatalf("unexpected instances: %+v", byID)
	}
	if elsewhere := f.instances("ap-northeast-1"); len(elsewhere) != 0 {
		t.Fatalf("region leak: %+v", elsewhere)
	}
	// A subnet with launch records cannot be deleted, natively or in SQL.
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}), 400, "DependencyViolation")
	if _, err := f.store.Database().Exec(`DELETE FROM aws_ec2_network_resources WHERE id=?`, subnet.ID); err == nil {
		t.Fatal("the foreign key did not protect a subnet that holds launch records")
	}
	f.expect(f.call("", "ap-northeast-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {launched.ID}}), 400, "InvalidInstanceID.NotFound")
	// The stack's instance is refused, never terminated or adopted.
	refused := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {"i-00000000000000c01"}}), 400, "UnsupportedOperation")
	if !strings.Contains(refused, "Delete its stack instead") {
		t.Fatal(refused)
	}
	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {launched.ID}, "DryRun": {"false"}}), 200, "")
	var terminated struct {
		Changes []struct {
			ID      string `xml:"instanceId"`
			Code    int    `xml:"currentState>code"`
			Current string `xml:"currentState>name"`
		} `xml:"instancesSet>item"`
	}
	f.decode(body, "TerminateInstancesResponse", &terminated)
	if len(terminated.Changes) != 1 || terminated.Changes[0].ID != launched.ID || terminated.Changes[0].Code != 48 || terminated.Changes[0].Current != "terminated" || strings.Contains(body, "previousState") {
		t.Fatalf("unexpected TerminateInstances result: %s", body)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {launched.ID}}), 400, "InvalidInstanceID.NotFound")
	if after := f.instances("us-east-1"); len(after) != 2 || after[0].ID == launched.ID || after[1].ID == launched.ID {
		t.Fatalf("the terminated record is still listed: %+v", after)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {second.ID}}), 200, "")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}), 200, "")
	if after := f.instances("us-east-1"); len(after) != 1 || after[0].ManagedBy != "cloudformation" {
		t.Fatalf("native termination changed the stack instance: %+v", after)
	}
}

func TestEC2RunAndTerminateInstancesRejectInvalidUnsupportedAndForeignInput(t *testing.T) {
	f := newEC2VPCFixture(t)
	f.cloudFormationInstance("us-east-1", "arn:aws:cloudformation:us-east-1:000000000000:stack/battle/four")
	vpc, subnet := f.network("us-east-1", "10.50.0.0/16", "10.50.1.0/24")
	with := func(key string, value ...string) url.Values {
		v := launchValues(subnet.ID)
		if len(value) == 0 {
			v.Del(key)
		} else {
			v[key] = value
		}
		return v
	}
	for _, tc := range []struct {
		name   string
		values url.Values
		code   string
	}{
		{"missing image", with("ImageId"), "MissingParameter"},
		{"malformed image", with("ImageId", "ami-XYZ"), "InvalidAMIID.Malformed"},
		{"image of wrong length", with("ImageId", "ami-0123456789"), "InvalidAMIID.Malformed"},
		{"SSM image alias", with("ImageId", "resolve:ssm:/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"), "InvalidAMIID.Malformed"},
		{"missing minimum", with("MinCount"), "MissingParameter"},
		{"missing maximum", with("MaxCount"), "MissingParameter"},
		{"two instances", with("MaxCount", "2"), "UnsupportedOperation"},
		{"zero instances", with("MinCount", "0"), "UnsupportedOperation"},
		{"missing type", with("InstanceType"), "MissingParameter"},
		{"unsupported type", with("InstanceType", "m5.large"), "InvalidParameterValue"},
		{"missing subnet", with("SubnetId"), "MissingParameter"},
		{"malformed subnet", with("SubnetId", vpc.ID), "InvalidSubnetID.Malformed"},
		{"unknown subnet", with("SubnetId", "subnet-0000000000000000f"), "InvalidSubnetID.NotFound"},
		{"CloudFormation subnet", with("SubnetId", "subnet-00000000000000c01"), "UnsupportedOperation"},
		{"empty client token", with("ClientToken", ""), "InvalidParameterValue"},
		{"long client token", with("ClientToken", strings.Repeat("t", 65)), "InvalidParameterValue"},
		{"client token with a space", with("ClientToken", "two words"), "InvalidParameterValue"},
		{"repeated field", with("ImageId", "ami-12345678", "ami-87654321"), "InvalidParameterValue"},
		{"dry run", with("DryRun", "true"), "UnsupportedOperation"},
		{"key pair", with("KeyName", "my-key"), "UnsupportedOperation"},
		{"security group", with("SecurityGroupId.1", "sg-00000000000000c01"), "UnsupportedOperation"},
		{"user data", with("UserData", "IyEvYmluL3No"), "UnsupportedOperation"},
		{"instance profile", with("IamInstanceProfile.Name", "profile"), "UnsupportedOperation"},
		{"network interface", with("NetworkInterface.1.SubnetId", subnet.ID), "UnsupportedOperation"},
		{"launch template", with("LaunchTemplate.LaunchTemplateName", "template"), "UnsupportedOperation"},
		{"wrong resource type", with("TagSpecification.1.ResourceType", "volume"), "InvalidParameterValue"},
		{"second specification", func() url.Values {
			v := with("TagSpecification.1.ResourceType", "instance")
			v.Set("TagSpecification.2.ResourceType", "volume")
			return v
		}(), "UnsupportedOperation"},
		{"reserved tag prefix", func() url.Values {
			v := with("TagSpecification.1.ResourceType", "instance")
			v.Set("TagSpecification.1.Tag.1.Key", "aws:owner")
			return v
		}(), "InvalidParameterValue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values.Set("Action", "RunInstances")
			f.expect(f.call("", "us-east-1", nil, tc.values), 400, tc.code)
		})
	}
	elsewhere := launchValues(subnet.ID)
	elsewhere.Set("Action", "RunInstances")
	f.expect(f.call("", "ap-northeast-1", nil, elsewhere), 400, "InvalidSubnetID.NotFound")
	for _, tc := range []struct {
		values url.Values
		code   string
	}{
		{url.Values{}, "MissingParameter"},
		{url.Values{"InstanceId.1": {"i-XYZ"}}, "InvalidInstanceID.Malformed"},
		{url.Values{"InstanceId.1": {subnet.ID}}, "InvalidInstanceID.Malformed"},
		{url.Values{"InstanceId.1": {"i-0000000000000000f"}, "InstanceId.2": {"i-0000000000000000e"}}, "UnsupportedOperation"},
		{url.Values{"InstanceId.1": {"i-0000000000000000f"}, "DryRun": {"true"}}, "UnsupportedOperation"},
		{url.Values{"InstanceId.1": {"i-0000000000000000f"}}, "InvalidInstanceID.NotFound"},
		{url.Values{"InstanceId.1": {"i-12345678"}}, "InvalidInstanceID.NotFound"},
	} {
		tc.values.Set("Action", "TerminateInstances")
		f.expect(f.call("", "us-east-1", nil, tc.values), 400, tc.code)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeInstances"}, "InstanceId.1": {"i-0000000000000000f"}}), 400, "UnsupportedOperation")
	if listed := f.instances("us-east-1"); len(listed) != 1 || listed[0].ManagedBy != "cloudformation" {
		t.Fatalf("rejected requests recorded instances: %+v", listed)
	}
}

func TestEC2RunInstancesClientTokenReturnsTheFirstRecordOrRefusesAMismatch(t *testing.T) {
	f := newEC2VPCFixture(t)
	_, subnet := f.network("us-east-1", "10.60.0.0/16", "10.60.1.0/24")
	values := func(image, name string) url.Values {
		v := launchValues(subnet.ID)
		v.Set("ImageId", image)
		v.Set("ClientToken", "launch-token-1")
		v.Set("TagSpecification.1.ResourceType", "instance")
		v.Set("TagSpecification.1.Tag.1.Key", "Name")
		v.Set("TagSpecification.1.Tag.1.Value", name)
		return v
	}
	first := f.run("", "us-east-1", values("ami-0123456789abcdef0", "web"))
	if replay := f.run("", "us-east-1", values("ami-0123456789abcdef0", "web")); replay.ID != first.ID {
		t.Fatalf("a repeated client token recorded a second instance: %s and %s", first.ID, replay.ID)
	}
	// Every stored parameter binds the token: image, tags, type and subnet.
	sibling := f.subnet("", "us-east-1", url.Values{"VpcId": {subnet.VPC}, "CidrBlock": {"10.60.2.0/24"}})
	otherType, otherSubnet, untagged := values("ami-0123456789abcdef0", "web"), values("ami-0123456789abcdef0", "web"), launchValues(subnet.ID)
	otherType.Set("InstanceType", "t3.small")
	otherSubnet.Set("SubnetId", sibling.ID)
	untagged.Set("ClientToken", "launch-token-1")
	for _, changed := range []url.Values{values("ami-12345678", "web"), values("ami-0123456789abcdef0", "api"), otherType, otherSubnet, untagged} {
		changed.Set("Action", "RunInstances")
		f.expect(f.call("", "us-east-1", nil, changed), 400, "IdempotentParameterMismatch")
	}
	if listed := f.instances("us-east-1"); len(listed) != 1 || listed[0].Type != "t3.micro" || listed[0].Subnet != subnet.ID {
		t.Fatalf("a refused replay changed the record: %+v", listed)
	}
	untokened := f.run("", "us-east-1", launchValues(subnet.ID))
	if listed := f.instances("us-east-1"); len(listed) != 2 || untokened.ID == first.ID {
		t.Fatalf("expected exactly the token's record and one untokened record: %+v", listed)
	}
	// The token is scoped to its region and is forgotten with its record.
	_, tokyo := f.network("ap-northeast-1", "10.61.0.0/16", "10.61.1.0/24")
	regional := launchValues(tokyo.ID)
	regional.Set("ClientToken", "launch-token-1")
	if other := f.run("", "ap-northeast-1", regional); other.ID == first.ID {
		t.Fatal("a client token crossed regions")
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {first.ID}}), 200, "")
	if again := f.run("", "us-east-1", values("ami-12345678", "api")); again.ID == first.ID {
		t.Fatal("a terminated record was returned for its old client token")
	}
}

func TestEC2SubnetAndInstanceMutationsRequireIdentityPolicy(t *testing.T) {
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
	allow := func(statements ...string) string {
		return `{"Version":"2012-10-17","Statement":[` + strings.Join(statements, ",") + `]}`
	}
	statement := func(effect, action, resource string) string {
		return `{"Effect":"` + effect + `","Action":"` + action + `","Resource":"` + resource + `"}`
	}
	const arn = "arn:aws:ec2:us-east-1:000000000000:"
	vpc := f.create("", url.Values{"CidrBlock": {"10.70.0.0/16"}})
	subnetValues := func(cidr string, tagged bool) url.Values {
		v := url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {cidr}}
		if tagged {
			v.Set("TagSpecification.1.ResourceType", "subnet")
			v.Set("TagSpecification.1.Tag.1.Key", "Name")
		}
		return v
	}
	f.expect(f.call("missing", "us-east-1", nil, subnetValues("10.70.1.0/24", false)), 403, "UnauthorizedOperation")
	// CreateSubnet needs both the VPC and the new subnet resource.
	set(allow(statement("Allow", "ec2:CreateSubnet", arn+"vpc/"+vpc.ID)))
	f.expect(f.call("learner", "us-east-1", nil, subnetValues("10.70.1.0/24", false)), 403, "UnauthorizedOperation")
	set(allow(statement("Allow", "ec2:CreateSubnet", arn+"subnet/*")))
	f.expect(f.call("learner", "us-east-1", nil, subnetValues("10.70.1.0/24", false)), 403, "UnauthorizedOperation")
	set(allow(statement("Allow", "ec2:CreateSubnet", arn+"vpc/"+vpc.ID), statement("Allow", "ec2:CreateSubnet", arn+"subnet/*")))
	f.expect(f.call("learner", "us-east-1", nil, subnetValues("10.70.1.0/24", true)), 403, "UnauthorizedOperation")
	if listed := f.subnets("us-east-1"); len(listed) != 0 {
		t.Fatalf("denied CreateSubnet changed state: %+v", listed)
	}
	subnet := f.subnet("learner", "us-east-1", subnetValues("10.70.1.0/24", false))
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}), 403, "UnauthorizedOperation")

	launch := func(tagged bool) url.Values {
		v := launchValues(subnet.ID)
		v.Set("Action", "RunInstances")
		if tagged {
			v.Set("TagSpecification.1.ResourceType", "instance")
			v.Set("TagSpecification.1.Tag.1.Key", "Name")
		}
		return v
	}
	instance, image, placed := statement("Allow", "ec2:RunInstances", arn+"instance/*"), statement("Allow", "ec2:RunInstances", "arn:aws:ec2:us-east-1::image/ami-0123456789abcdef0"), statement("Allow", "ec2:RunInstances", arn+"subnet/"+subnet.ID)
	// RunInstances needs the instance, the image and the subnet; tags need CreateTags.
	for _, partial := range [][]string{{image, placed}, {instance, placed}, {instance, image}} {
		set(allow(partial...))
		f.expect(f.call("learner", "us-east-1", nil, launch(false)), 403, "UnauthorizedOperation")
	}
	set(allow(instance, image, placed))
	f.expect(f.call("learner", "us-east-1", nil, launch(true)), 403, "UnauthorizedOperation")
	if listed := f.instances("us-east-1"); len(listed) != 0 {
		t.Fatalf("denied RunInstances recorded an instance: %+v", listed)
	}
	launched := f.run("learner", "us-east-1", launch(false))
	set(allow(instance, image, placed, statement("Allow", "ec2:CreateTags", arn+"instance/*")))
	tagged := f.run("learner", "us-east-1", launch(true))
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {launched.ID}}), 403, "UnauthorizedOperation")
	set(allow(statement("Allow", "ec2:TerminateInstances", arn+"instance/"+launched.ID), statement("Allow", "ec2:DeleteSubnet", arn+"subnet/"+subnet.ID)))
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {tagged.ID}}), 403, "UnauthorizedOperation")
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {launched.ID}}), 200, "")
	set(allow(statement("Allow", "ec2:*", "*"), statement("Deny", "ec2:TerminateInstances", "*"), statement("Deny", "ec2:DeleteSubnet", "*")))
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {tagged.ID}}), 403, "UnauthorizedOperation")
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}), 403, "UnauthorizedOperation")
	if listed := f.instances("us-east-1"); len(listed) != 1 || listed[0].ID != tagged.ID {
		t.Fatalf("authorization did not bound termination: %+v", listed)
	}
	// A console session bound to one instance cannot change subnets or instances.
	scoped := context.WithValue(context.WithValue(context.Background(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{"us-east-1", "i-00000000000000001"}), sdkPolicyContextKey{}, core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ec2:*"}, Resource: core.StringList{"*"}}}})
	for _, values := range []url.Values{subnetValues("10.70.2.0/24", false), {"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}, launch(false), {"Action": {"TerminateInstances"}, "InstanceId.1": {tagged.ID}}} {
		f.expect(f.call("", "us-east-1", scoped, values), 403, "UnauthorizedOperation")
	}
	if subnets, instances := f.subnets("us-east-1"), f.instances("us-east-1"); len(subnets) != 1 || len(instances) != 1 {
		t.Fatalf("an instance console session changed state: %+v %+v", subnets, instances)
	}
	// The bound session still sees only its own instance, never a launch record:
	// not another one, and not even one whose ID it was bound to, because a
	// record has no guest to connect to.
	bound := context.WithValue(context.WithValue(context.Background(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{"us-east-1", tagged.ID}), sdkPolicyContextKey{}, core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ec2:*"}, Resource: core.StringList{"*"}}}})
	for _, session := range []context.Context{scoped, bound} {
		body := f.expect(f.call("", "us-east-1", session, url.Values{"Action": {"DescribeInstances"}}), 200, "")
		if strings.Contains(body, tagged.ID) {
			t.Fatalf("an instance console session listed a launch record: %s", body)
		}
	}
}

func TestEC2NativeSubnetAndInstanceStateFailuresAreNotSuccess(t *testing.T) {
	f := newEC2VPCFixture(t)
	db := f.store.Database()
	vpc, subnet := f.network("us-east-1", "10.80.0.0/16", "10.80.1.0/24")
	launched := f.run("", "us-east-1", launchValues(subnet.ID))
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	terminate := url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {launched.ID}}
	failsClosed := func(reason string) {
		t.Helper()
		for _, values := range []url.Values{{"Action": {"DescribeInstances"}}, terminate} {
			if body := f.expect(f.call("", "us-east-1", nil, values), 500, "InternalError"); strings.Contains(body, launched.ID) {
				t.Fatalf("%s: inconsistent state was reflected: %s", reason, body)
			}
		}
	}
	var document string
	if err := db.QueryRow(`SELECT document FROM aws_ec2_native_instances WHERE id=?`, launched.ID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE aws_ec2_native_instances SET document='{' WHERE id=?`, launched.ID)
	failsClosed("corrupt document")
	exec(`UPDATE aws_ec2_native_instances SET document=? WHERE id=?`, strings.Replace(document, vpc.ID, "vpc-0000000000000000f", 1), launched.ID)
	failsClosed("VPC differs from the subnet's VPC")
	exec(`UPDATE aws_ec2_native_instances SET document=?,client_token='smuggled' WHERE id=?`, document, launched.ID)
	failsClosed("token column differs from the document")
	exec(`UPDATE aws_ec2_native_instances SET client_token=NULL WHERE id=?`, launched.ID)
	// A stack claim on an EC2-owned instance or subnet is never resolved by guessing.
	claimant := StackRecord{ID: "arn:aws:cloudformation:us-east-1:000000000000:stack/x/y", Name: "x", Region: "us-east-1"}
	data, _ := json.Marshal(claimant)
	exec(`INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, claimant.Region, claimant.Name, claimant.ID, string(data))
	exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace("us-east-1", "AWS::EC2::Instance"), "AWS::EC2::Instance", launched.ID, claimant.ID)
	failsClosed("stack claim on the instance")
	exec(`DELETE FROM aws_cfn_ownership WHERE physical_id=?`, launched.ID)
	exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace("us-east-1", "AWS::EC2::Subnet"), "AWS::EC2::Subnet", subnet.ID, claimant.ID)
	for _, values := range []url.Values{{"Action": {"DescribeSubnets"}}, {"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}, launchValues(subnet.ID)} {
		if values.Get("Action") == "" {
			values.Set("Action", "RunInstances")
		}
		f.expect(f.call("", "us-east-1", nil, values), 500, "InternalError")
	}
	exec(`DELETE FROM aws_cfn_ownership WHERE physical_id=?`, subnet.ID)
	if listed := f.instances("us-east-1"); len(listed) != 1 || listed[0].ID != launched.ID {
		t.Fatalf("repaired state is not readable: %+v", listed)
	}
	// A record below a stack-owned subnet was never written by RunInstances: the
	// foreign key admits any network row, so the read must refuse it.
	f.cloudFormationInstance("us-east-1", "arn:aws:cloudformation:us-east-1:000000000000:stack/battle/five")
	foreign, _ := json.Marshal(ec2NativeInstance{ID: "i-0000000000000000d", Region: "us-east-1", SubnetID: "subnet-00000000000000c01", VPCID: "vpc-00000000000000c01", ImageID: "ami-12345678", InstanceType: "t3.micro", Tags: map[string]string{}})
	exec(`INSERT INTO aws_ec2_native_instances VALUES(?,?,?,?,?)`, "us-east-1", "i-0000000000000000d", "subnet-00000000000000c01", nil, string(foreign))
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeInstances"}}), 500, "InternalError")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"TerminateInstances"}, "InstanceId.1": {"i-0000000000000000d"}}), 500, "InternalError")
	exec(`DELETE FROM aws_ec2_native_instances WHERE id='i-0000000000000000d'`)
	// An orphaned subnet is not a place to launch into.
	orphan := EC2NetworkResource{ID: "subnet-0000000000000000b", Kind: "AWS::EC2::Subnet", Region: "us-east-1", StackID: ec2NativeOwner, VPCID: "vpc-0000000000000000b", CIDR: "10.81.1.0/24", Revision: 1}
	f.writeNetwork(orphan)
	orphaned := launchValues(orphan.ID)
	orphaned.Set("Action", "RunInstances")
	f.expect(f.call("", "us-east-1", nil, orphaned), 500, "InternalError")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeSubnets"}}), 500, "InternalError")
	exec(`DELETE FROM aws_ec2_network_resources WHERE id=?`, orphan.ID)

	// The record bound refuses the next launch without changing the existing ones.
	for i := 1; i < ec2NativeInstanceLimit; i++ {
		id := fmt.Sprintf("i-%017x", i)
		raw, _ := json.Marshal(ec2NativeInstance{ID: id, Region: "us-east-1", SubnetID: subnet.ID, VPCID: vpc.ID, ImageID: "ami-12345678", InstanceType: "t3.micro", Tags: map[string]string{}})
		exec(`INSERT INTO aws_ec2_native_instances VALUES(?,?,?,?,?)`, "us-east-1", id, subnet.ID, nil, string(raw))
	}
	full := launchValues(subnet.ID)
	full.Set("Action", "RunInstances")
	f.expect(f.call("", "us-east-1", nil, full), 400, "InstanceLimitExceeded")
	// The bound is one per database, not per region.
	_, tokyo := f.network("ap-northeast-1", "10.82.0.0/16", "10.82.1.0/24")
	elsewhere := launchValues(tokyo.ID)
	elsewhere.Set("Action", "RunInstances")
	f.expect(f.call("", "ap-northeast-1", nil, elsewhere), 400, "InstanceLimitExceeded")
	records := 0
	for _, listed := range f.instances("us-east-1") {
		if listed.ManagedBy == "ec2" {
			records++
		}
	}
	if records != ec2NativeInstanceLimit || len(f.instances("ap-northeast-1")) != 0 {
		t.Fatalf("the bound was not exact: %d", records)
	}
	exec(`DELETE FROM aws_ec2_native_instances`)
	// Subnets share the 1000 network resource bound with every other network row.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 998; i++ {
		r := EC2NetworkResource{ID: fmt.Sprintf("igw-%017x", i), Kind: "AWS::EC2::InternetGateway", Region: "us-west-2", StackID: "arn:aws:cloudformation:us-west-2:000000000000:stack/q/q", Revision: 1}
		if err = networkWrite(context.Background(), tx, r, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.80.2.0/24"}}), 400, "SubnetLimitExceeded")
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, values := range []url.Values{{"Action": {"CreateSubnet"}, "VpcId": {vpc.ID}, "CidrBlock": {"10.80.3.0/24"}}, {"Action": {"DeleteSubnet"}, "SubnetId": {subnet.ID}}, full, terminate} {
		if body := f.expect(f.call("", "us-east-1", nil, values), 500, "InternalError"); strings.Contains(body, "database") || strings.Contains(body, "sqlite") {
			t.Fatalf("storage failure leaked internals: %s", body)
		}
	}
}
