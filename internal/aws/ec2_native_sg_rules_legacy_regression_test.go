package aws

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

// The native rule projection shares DescribeSecurityGroups with CloudFormation.
// Neither writer can adopt the other's rows, even during a failed readback.
func TestEC2NativeSGRuleKeepsLegacyReadbackIndependent(t *testing.T) {
	f := newEC2VPCFixture(t)
	const tpl = `{"Resources":{
		"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.128.0.0/16"}},
		"Group":{"Type":"AWS::EC2::SecurityGroup","Properties":{
			"VpcId":{"Ref":"Vpc"},"GroupDescription":"CloudFormation owner",
			"SecurityGroupIngress":[{"IpProtocol":"tcp","FromPort":8443,"ToPort":8443,"CidrIp":"10.128.1.0/24"}]}}
	}}`
	stack, err := compileStack(sgRuleFailureRegion, "sg-rule-legacy-integrity", tpl, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	var legacyVpc, legacy string
	for _, resource := range stack.Resources {
		switch resource.LogicalID {
		case "Vpc":
			legacyVpc = resource.PhysicalID
		case "Group":
			legacy = resource.PhysicalID
		}
	}
	if legacyVpc == "" || legacy == "" {
		t.Fatal("missing legacy ownership fixtures")
	}
	vpc := f.create("", url.Values{"CidrBlock": {"10.129.0.0/16"}})
	group := createSGForRuleTest(t, f, vpc.ID, "native peer")
	f.expect(f.call("", sgRuleFailureRegion, nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, group, "10.129.1.0/24", 443)), http.StatusOK, "")
	query := url.Values{"Action": {"DescribeSecurityGroups"}}
	healthy := sgRuleLineInventory(t, f)
	if len(healthy) != 2 {
		t.Fatalf("both owners must be visible before the fault: %+v", healthy)
	}
	recoverExact := func(t *testing.T) {
		t.Helper()
		if after := sgRuleLineInventory(t, f); !reflect.DeepEqual(after, healthy) {
			t.Fatalf("recovery changed one or both owners: %+v", after)
		}
	}
	f.expect(f.call("", sgRuleFailureRegion, nil, sgRuleRequest(ec2AuthorizeNativeSGIngress, legacy, "10.128.1.0/24", 443)), http.StatusBadRequest, "UnsupportedOperation")
	recoverExact(t)

	const where = " WHERE region=? AND id=?"
	snapshot := func(t *testing.T, id string) string {
		t.Helper()
		var raw string
		err := f.store.Database().QueryRow("SELECT document FROM aws_ec2_network_resources"+where, sgRuleFailureRegion, id).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	overfull := make([]EC2IngressRule, ec2NativeSGRuleLimit+1)
	for n := range overfull {
		overfull[n] = EC2IngressRule{Protocol: "tcp", FromPort: 10000 + n, ToPort: 10000 + n, CIDR: "10.128.1.0/24"}
	}
	rulesJSON, err := json.Marshal(overfull)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []struct {
		name, id, expr string
		args           []any
	}{
		{"stale group metadata", legacy, `json_set(document,'$.Description','not the declared description')`, nil},
		{"unreadable parent", legacyVpc, `json_set(document,'$.ID','wrong saved parent identity')`, nil},
		{"overfull legacy ingress", legacy, `json_set(document,'$.Ingress',json(?))`, []any{string(rulesJSON)}},
		{"invalid legacy ingress", legacy, `json_set(document,'$.Ingress[0].ToPort',0)`, nil},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := f
			f.t = t
			pristine := snapshot(t, fault.id)
			sgRuleLineExec(t, f, "UPDATE aws_ec2_network_resources SET document="+fault.expr+where, append(fault.args, sgRuleFailureRegion, fault.id)...)
			damaged := snapshot(t, fault.id)
			if damaged == pristine {
				t.Fatal("fixture did not change the intended data")
			}
			sgRuleLineNoResult(t, f, query, legacy, group)
			if snapshot(t, fault.id) != damaged {
				t.Fatal("failed discovery rewrote stored records")
			}
			sgRuleLineExec(t, f, "UPDATE aws_ec2_network_resources SET document=?"+where, pristine, sgRuleFailureRegion, fault.id)
			recoverExact(t)
		})
	}
	if err := f.store.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}
