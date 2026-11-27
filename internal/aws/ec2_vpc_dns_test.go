package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func describeDNSForm(id, attr string) url.Values {
	return url.Values{"Action": {ec2DescribeVPCAttribute}, "VpcId": {id}, "Attribute": {attr}}
}
func modifyDNSForm(id, attr string, on bool) url.Values {
	return url.Values{"Action": {ec2ModifyVPCAttribute}, "VpcId": {id}, "Enable" + strings.TrimPrefix(attr, "enable") + ".Value": {strconv.FormatBool(on)}}
}
func assertDNSAttribute(t *testing.T, f ec2VPCFixture, actor, id, name string, want bool) {
	t.Helper()
	body := f.expect(f.call(actor, "us-east-1", nil, describeDNSForm(id, name)), 200, "")
	var out ec2VPCAttributeResponse
	if err := xml.Unmarshal([]byte(body), &out); err != nil || out.XMLName.Local != "DescribeVpcAttributeResponse" ||
		out.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || out.RequestID == "" || out.VpcID != id ||
		out.Return || (out.Support != nil) != (name == vpcDNSSupport) || (out.Hostnames != nil) != (name == vpcDNSHostnames) ||
		out.Support != nil && out.Support.Value != want || out.Hostnames != nil && out.Hostnames.Value != want {
		t.Fatalf("bad %s response: %s (%v)", name, body, err)
	}
}

func TestEC2VPCDNSQuerySettingsAndOwnership(t *testing.T) {
	f := newEC2VPCFixture(t)
	const east = "us-east-1"
	one := f.create("", url.Values{"CidrBlock": {"10.201.0.0/16"}, "TagSpecification.1.ResourceType": {"vpc"}, "TagSpecification.1.Tag.1.Key": {"Name"}, "TagSpecification.1.Tag.1.Value": {"keep"}}).ID
	two := f.create("", url.Values{"CidrBlock": {"10.202.0.0/16"}}).ID
	stack, err := compileStack(east, "dns-evidence", `{"Resources":{"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.183.0.0/16","EnableDnsHostnames":true}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	foreign := stack.Resources[0].PhysicalID
	arn := networkARN(east, "vpc", one)
	for actor, stmt := range map[string]string{
		"dns-writer": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:ModifyVpcAttribute","Resource":%q}`, arn),
		"dns-reader": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:DescribeVpcAttribute","Resource":%q}`, arn),
		"dns-denied": fmt.Sprintf(`{"Effect":"Allow","Action":"ec2:*","Resource":"*"},{"Effect":"Deny","Action":"ec2:ModifyVpcAttribute","Resource":%q}`, arn),
	} {
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[` + stmt + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.g.Identities.Set(actor, p); err != nil {
			t.Fatal(err)
		}
	}
	assertDNSAttribute(t, f, "dns-reader", one, vpcDNSSupport, true)
	assertDNSAttribute(t, f, "dns-reader", one, vpcDNSHostnames, false)
	assertDNSAttribute(t, f, "", foreign, vpcDNSHostnames, true)

	db := f.store.Database()
	before := replaceTestInventory(t, db)
	refuse := func(actor, region string, ctx context.Context, q url.Values, status int, code string) {
		t.Helper()
		res := f.expect(f.call(actor, region, ctx, q), status, code)
		if strings.Contains(res, "<return>") || strings.Contains(res, "<enableDns") {
			t.Fatalf("error returned settings: %s", res)
		}
		if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
			t.Fatalf("%s changed persisted rows", code)
		}
	}
	refuse("dns-reader", east, nil, modifyDNSForm(one, vpcDNSHostnames, true), 403, "UnauthorizedOperation")
	refuse("dns-writer", east, nil, describeDNSForm(one, vpcDNSSupport), 403, "UnauthorizedOperation")
	refuse("dns-writer", east, nil, modifyDNSForm(two, vpcDNSHostnames, true), 403, "UnauthorizedOperation")
	refuse("dns-denied", east, nil, modifyDNSForm(one, vpcDNSHostnames, true), 403, "UnauthorizedOperation")
	refuse("dns-reader", "us-west-2", nil, describeDNSForm(one, vpcDNSHostnames), 403, "UnauthorizedOperation")
	refuse("", "us-west-2", nil, describeDNSForm(one, vpcDNSHostnames), 400, "InvalidVpcID.NotFound")
	refuse("", east, nil, modifyDNSForm(foreign, vpcDNSHostnames, false), 400, "UnsupportedOperation")
	scope := context.WithValue(t.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: east, instance: "i-00000000"})
	refuse("", east, scope, describeDNSForm(one, vpcDNSHostnames), 403, "UnauthorizedOperation")

	both := modifyDNSForm(one, vpcDNSSupport, false)
	both.Set("EnableDnsHostnames.Value", "false")
	refuse("", east, nil, both, 400, "InvalidParameterCombination")
	q := describeDNSForm(one, "enableNetworkAddressUsageMetrics")
	refuse("", east, nil, q, 400, "UnsupportedOperation")
	q = describeDNSForm(one, vpcDNSSupport)
	q.Add("VpcId", two)
	refuse("", east, nil, q, 400, "InvalidParameterValue")
	q = modifyDNSForm(one, vpcDNSSupport, true)
	q.Set("EnableDnsSupport.Value", "1")
	refuse("", east, nil, q, 400, "InvalidParameterValue")
	q.Del("EnableDnsSupport.Value")
	refuse("", east, nil, q, 400, "MissingParameter")
	q.Set("EnableNetworkAddressUsageMetrics.Value", "true")
	refuse("", east, nil, q, 400, "UnsupportedOperation")
	refuse("", east, nil, describeDNSForm("not-a-vpc", vpcDNSSupport), 400, "InvalidVpcID.Malformed")
	q = describeDNSForm(one, vpcDNSSupport)
	q.Set("DryRun", "true")
	refuse("dns-reader", east, nil, q, 412, "DryRunOperation")
	refuse("dns-writer", east, nil, q, 403, "UnauthorizedOperation")

	// A native row must also be absent from both independent stack
	// ownership witnesses, not merely have an "ec2:native" owner column.
	nativeSubnetLineExec(t, db, "INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(east, ec2RTVPC), ec2RTVPC, one, stack.ID)
	refuse("", east, nil, modifyDNSForm(one, vpcDNSHostnames, true), 500, "InternalError")
	nativeSubnetLineExec(t, db, "DELETE FROM aws_cfn_ownership WHERE namespace=? AND physical_id=?", stackResourceNamespace(east, ec2RTVPC), one)
	doc := subnetSettingsEvidenceDocument(t, f, stack.ID)
	st := stack
	st.Resources = append(st.Resources, StackResource{Type: ec2RTVPC, LogicalID: "Pending", PhysicalID: one})
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(raw), stack.ID)
	refuse("", east, nil, modifyDNSForm(one, vpcDNSHostnames, true), 500, "InternalError")
	nativeSubnetLineExec(t, db, "UPDATE aws_cfn_stacks SET document=? WHERE id=?", doc, stack.ID)

	write := func(name string, on bool) {
		t.Helper()
		body := f.expect(f.call("dns-writer", east, nil, modifyDNSForm(one, name, on)), 200, "")
		var out ec2VPCAttributeResponse
		if err := xml.Unmarshal([]byte(body), &out); err != nil || out.XMLName.Local != "ModifyVpcAttributeResponse" || out.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || out.RequestID == "" || !out.Return || out.VpcID != "" {
			t.Fatal(body, err)
		}
	}
	write(vpcDNSSupport, false)
	assertDNSAttribute(t, f, "dns-reader", one, vpcDNSSupport, false)
	after := replaceTestInventory(t, db)
	key := [2]string{east, one}
	original, updated := before[key], after[key]
	var was, now EC2NetworkResource
	if json.Unmarshal([]byte(original[2]), &was) != nil || json.Unmarshal([]byte(updated[2]), &now) != nil {
		t.Fatal("bad resource document")
	}
	was.DNSSupport = false
	was.NativeDNSUpdates++
	was.Revision++
	if !reflect.DeepEqual(was, now) {
		t.Fatal("changed fields outside selected DNS setting and its revision")
	}
	delete(before, key)
	delete(after, key)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed unrelated resources")
	}
	before = replaceTestInventory(t, db)
	refuse("dns-writer", east, nil, modifyDNSForm(one, vpcDNSHostnames, true), 400, "InvalidParameterCombination")
	write(vpcDNSSupport, false)
	if !reflect.DeepEqual(before, replaceTestInventory(t, db)) {
		t.Fatal("idempotent write changed revision")
	}
	write(vpcDNSSupport, true)
	write(vpcDNSHostnames, true)
	assertDNSAttribute(t, f, "dns-reader", one, vpcDNSHostnames, true)
	assertDNSAttribute(t, f, "", two, vpcDNSHostnames, false)
	assertDNSAttribute(t, f, "", foreign, vpcDNSHostnames, true)
	f.expect(f.call("", east, nil, url.Values{"Action": {"DescribeRouteTables"}}), 200, "")
	f.expect(f.call("", east, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {one}}), 200, "")
	f.expect(f.call("", east, nil, describeDNSForm(one, vpcDNSHostnames)), 400, "InvalidVpcID.NotFound")
}

func TestEC2VPCDNSRestartRollbackAndLegacyRepair(t *testing.T) {
	const east = "us-east-1"
	file := filepath.Join(t.TempDir(), "dns.db")
	db, stacks := nativeRouteTableTestStore(t, file)
	f := ec2VPCFixture{t, &Gateway{Stacks: stacks, Identities: core.NewIdentities(db)}, stacks, db}
	vpc := f.create("", url.Values{"CidrBlock": {"10.167.0.0/16"}}).ID
	sqlDB := db.Database()
	old := replaceTestInventory(t, sqlDB)
	nativeSubnetLineExec(t, sqlDB, `CREATE TRIGGER dns_refuse BEFORE UPDATE ON aws_ec2_network_resources WHEN NEW.kind='AWS::EC2::VPC' BEGIN SELECT RAISE(IGNORE); END`)
	f.expect(f.call("", east, nil, modifyDNSForm(vpc, vpcDNSHostnames, true)), 500, "InternalError")
	if !reflect.DeepEqual(old, replaceTestInventory(t, sqlDB)) {
		t.Fatal("failed write changed resources")
	}
	nativeSubnetLineExec(t, sqlDB, "DROP TRIGGER dns_refuse")
	f.expect(f.call("", east, nil, modifyDNSForm(vpc, vpcDNSHostnames, true)), 200, "")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, stacks = nativeRouteTableTestStore(t, file)
	f = ec2VPCFixture{t, &Gateway{Stacks: stacks, Identities: core.NewIdentities(db)}, stacks, db}
	assertDNSAttribute(t, f, "", vpc, vpcDNSHostnames, true)
	assertDNSAttribute(t, f, "", vpc, vpcDNSSupport, true)
	f.expect(f.call("", east, nil, url.Values{"Action": {"DeleteVpc"}, "VpcId": {vpc}}), 200, "")

	g, _ := legacyMainRepairFixture(t)
	g.expect(g.call("", repairRegion, nil, modifyDNSForm(repairVPC, vpcDNSHostnames, true)), 200, "")
	allow := func() error { return nil }
	p, err := ApplyEC2NativeMainRouteRepair(t.Context(), g.store.Database(), repairRegion, repairVPC, strings.Repeat("e", 64), allow, allow)
	if err != nil || p.Status != "applied" || p.VPCRevision != 3 {
		t.Fatal(p, err)
	}
	g.expect(g.call("", repairRegion, nil, modifyDNSForm(repairVPC, vpcDNSHostnames, false)), 200, "")
	assertDNSAttribute(t, g, "", repairVPC, vpcDNSHostnames, false)
	g.expect(g.call("", repairRegion, nil, url.Values{"Action": {"DescribeRouteTables"}}), 200, "")
}
