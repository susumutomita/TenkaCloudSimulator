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

func TestEC2NetworkACLGatewayEnforcesAdmissionAndReadback(t *testing.T) {
	f := newEC2VPCFixture(t)
	const region = "us-east-1"
	set := func(statements string) {
		t.Helper()
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":` + statements + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.g.Identities.Set("acl-editor", policy); err != nil {
			t.Fatal(err)
		}
	}
	vpc := f.create("", url.Values{"CidrBlock": {"10.36.0.0/16"}})
	aclScope, vpcScope := networkARN(region, "network-acl", "*"), networkARN(region, "vpc", vpc.ID)
	create := url.Values{"Action": {"CreateNetworkAcl"}, "VpcId": {vpc.ID}, "ClientToken": {"fixture-acl-auth"}}
	describe := url.Values{"Action": {"DescribeNetworkAcls"}}
	set(fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:CreateNetworkAcl","Resource":%q}]`, aclScope))
	f.expect(f.call("acl-editor", region, nil, create), 403, "UnauthorizedOperation")
	set(fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:CreateNetworkAcl","Resource":%q}]`, vpcScope))
	f.expect(f.call("acl-editor", region, nil, create), 403, "UnauthorizedOperation")
	allow := fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:CreateNetworkAcl","Resource":[%q,%q]}`, vpcScope, aclScope)
	set("[" + allow + "]")
	create.Set("TagSpecification.1.ResourceType", "network-acl")
	create.Set("TagSpecification.1.Tag.1.Key", "Name")
	f.expect(f.call("acl-editor", region, nil, create), 403, "UnauthorizedOperation")
	create.Del("TagSpecification.1.ResourceType")
	create.Del("TagSpecification.1.Tag.1.Key")
	if body := f.expect(f.call("", region, nil, describe), 200, ""); !strings.Contains(body, "<networkAclSet></networkAclSet>") {
		t.Fatal("failed admissions altered persisted state")
	}
	raw := f.expect(f.call("acl-editor", region, nil, create), 200, "")
	var created ec2NetworkACLResponse
	if err := xml.Unmarshal([]byte(raw), &created); err != nil || created.XMLName.Local != "CreateNetworkAclResponse" ||
		created.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || created.RequestID == "" ||
		created.NetworkACL == nil || created.NetworkACL.VPCID != vpc.ID || created.NetworkACL.Mode != "detached-config-only" {
		t.Fatalf("unexpected EC2 Query response: %+v / %v", created, err)
	}
	id := created.NetworkACL.ID
	if listed := f.describe(region); len(listed) != 1 || listed[0].ID != vpc.ID {
		t.Fatalf("native ACL affects VPC discovery: %+v", listed)
	}
	f.expect(f.call("acl-editor", region, nil, describe), 403, "UnauthorizedOperation")
	f.expect(f.call("acl-editor", region, nil, url.Values{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {id}}), 403, "UnauthorizedOperation")
	aclARN := networkARN(region, "network-acl", id)
	exactDeny := fmt.Sprintf(`{"Effect":"Deny","Action":"ec2:CreateNetworkAcl","Resource":%q}`, aclARN)
	set("[" + allow + "," + exactDeny + "]")
	f.expect(f.call("acl-editor", region, nil, create), 403, "UnauthorizedOperation")
	set(fmt.Sprintf(`[{"Effect":"Allow","Action":"ec2:DescribeNetworkAcls","Resource":"*"},{"Effect":"Allow","Action":"ec2:DeleteNetworkAcl","Resource":%q}]`, aclARN))
	if body := f.expect(f.call("acl-editor", region, nil, describe), 200, ""); strings.Count(body, "<networkAclId>"+id+"</networkAclId>") != 1 {
		t.Fatal("bound Describe did not show exactly one ACL")
	}
	bound := context.WithValue(context.Background(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-00000000"})
	f.expect(f.call("acl-editor", region, bound, describe), 403, "UnauthorizedOperation")
	f.expect(f.call("", region, nil, url.Values{"Action": {"CreateNetworkAclEntry"}}), 400, "MissingParameter")
	f.expect(f.call("", region, nil, url.Values{"Action": {"ReplaceNetworkAclAssociation"}}), 400, "UnsupportedOperation")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 400, "DependencyViolation")
	f.expect(f.call("acl-editor", region, nil, url.Values{"Action": {"DeleteNetworkAcl"}, "NetworkAclId": {id}}), 200, "")
	f.expect(f.call("", region, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc.ID}}), 200, "")
}
