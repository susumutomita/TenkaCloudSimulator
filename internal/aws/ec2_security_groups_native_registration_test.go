package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestEC2NativeSGGatewayAdmissionAndMetadataReadback(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	set := func(statements string) {
		t.Helper()
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + statements + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set("sg-editor", p); err != nil {
			t.Fatal(err)
		}
	}
	first := f.create("", url.Values{"CidrBlock": {"10.55.0.0/16"}})
	other := f.create("", url.Values{"CidrBlock": {"10.56.0.0/16"}})
	vpc, sg := networkARN(region, "vpc", first.ID), networkARN(region, "security-group", "*")
	allow := fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateSecurityGroup","Resource":[%q,%q]}`, vpc, sg)
	create := url.Values{"Action": {"CreateSecurityGroup"}, "GroupName": {"native group"}, "GroupDescription": {"Metadata only"}, "VpcId": {first.ID}}
	set(fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateSecurityGroup","Resource":%q}`, sg))
	f.expect(f.call("sg-editor", region, nil, create), 403, "UnauthorizedOperation")
	set(fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateSecurityGroup","Resource":%q}`, vpc))
	f.expect(f.call("sg-editor", region, nil, create), 403, "UnauthorizedOperation")
	exact := networkARN(region, "security-group", "sg-"+strings.Repeat("?", 17))
	set(allow + fmt.Sprintf(`,{"Effect":"Deny","Action":"ec2:CreateSecurityGroup","Resource":%q}`, exact))
	f.expect(f.call("sg-editor", region, nil, create), 403, "UnauthorizedOperation")
	set(allow)
	create.Set("VpcId", other.ID)
	f.expect(f.call("sg-editor", region, nil, create), 403, "UnauthorizedOperation")
	create.Set("VpcId", first.ID)
	create.Set("TagSpecification.1.ResourceType", "security-group")
	create.Set("TagSpecification.1.Tag.1.Key", "Name")
	create.Set("TagSpecification.1.Tag.1.Value", "学習")
	f.expect(f.call("sg-editor", region, nil, create), 403, "UnauthorizedOperation")
	describe := url.Values{"Action": {"DescribeSecurityGroups"}}
	if body := f.expect(f.call("", region, nil, describe), 200, ""); !strings.Contains(body, "<securityGroupInfo></securityGroupInfo>") {
		t.Fatal("refused requests changed metadata")
	}
	set(allow + fmt.Sprintf(`,{"Effect":"Allow","Action":"ec2:CreateTags","Resource":%q}`, sg))
	w := f.expect(f.call("sg-editor", region, nil, create), 200, "")
	var made ec2NativeSecurityGroupMutationResponse
	if err := xml.Unmarshal([]byte(w), &made); err != nil || made.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" ||
		made.XMLName.Local != "CreateSecurityGroupResponse" || made.RequestID == "" ||
		!ec2NativeSecurityGroupID.MatchString(made.GroupID) || made.ARN != networkARN(region, "security-group", made.GroupID) {
		t.Fatalf("unexpected create %s / %v", w, err)
	}
	if groups := f.describe(region); len(groups) != 2 {
		t.Fatalf("native SG disturbed normal VPC discovery: %+v", groups)
	}
	describe.Set("GroupId.1", made.GroupID)
	f.expect(f.call("sg-editor", region, nil, describe), 403, "UnauthorizedOperation")
	got := f.expect(f.call("", region, nil, describe), 200, "")
	if !strings.Contains(got, "<groupName>native group</groupName>") || !strings.Contains(got, "<simulatorSecurityGroupMode>detached-config-only</simulatorSecurityGroupMode>") ||
		strings.Contains(got, "<ipPermissionsEgress>") {
		t.Fatalf("readback made a false policy claim: %s", got)
	}
	bound := context.WithValue(context.Background(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	f.expect(f.call("sg-editor", region, bound, create), 403, "UnauthorizedOperation")
	f.expect(f.call("", region, nil, url.Values{"Action": {"AuthorizeSecurityGroupIngress"}}), 400, "UnsupportedOperation")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {first.ID}}), 400, "DependencyViolation")
	remove := url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {made.GroupID}}
	f.expect(f.call("sg-editor", region, nil, remove), 403, "UnauthorizedOperation")
	set(fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:DeleteSecurityGroup","Resource":%q}`, made.ARN))
	f.expect(f.call("sg-editor", region, nil, remove), 200, "")
	f.expect(f.call("", region, nil, describe), 400, "InvalidGroup.NotFound")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {first.ID}}), 200, "")
}
