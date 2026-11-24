package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Scope: AWS EC2 Query 2016-11-15 DescribeSubnets with no optional parameters,
// against existing CFN-owned saved state. This is an in-process wire contract,
// not a networking, VM, default VPC, or full filters/pagination exercise.
func TestEC2DescribeSubnetsContract(t *testing.T) {
	state, _ := s3TestState(t)
	db := state.Database()
	stacks, err := NewStackStore(db)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(state)}
	const east = "us-east-1"
	const west = "us-west-2"
	const empty = "ap-northeast-1"
	const subnetKind = "AWS::EC2::Subnet"
	const vpcKind = "AWS::EC2::VPC"

	// Two independently owned stacks in different regions and an unindexed
	// network-only row, which must not be accepted as a provisioned resource.
	for _, f := range []struct{ region, stack, vpc, subnet, cidr string }{
		{east, "stack-east", "vpc-00000001", "subnet-00000001", "10.1.1.0/24"},
		{west, "stack-west", "vpc-00000002", "subnet-00000002", "10.1.2.0/24"},
	} {
		record := StackRecord{ID: f.stack, Name: f.stack, Region: f.region, Resources: []StackResource{
			{Type: vpcKind, PhysicalID: f.vpc}, {Type: subnetKind, PhysicalID: f.subnet},
		}}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)", f.region, f.stack, f.stack, string(raw)); err != nil {
			t.Fatal(err)
		}
		for _, item := range record.Resources {
			if _, err := db.Exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(f.region, item.Type), item.Type, item.PhysicalID, f.stack); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range []EC2NetworkResource{
			{ID: f.vpc, Kind: vpcKind, Region: f.region, StackID: f.stack, CIDR: "10.1.0.0/16", Revision: 1},
			{ID: f.subnet, Kind: subnetKind, Region: f.region, StackID: f.stack, VPCID: f.vpc, CIDR: f.cidr, Revision: 1},
			{ID: "subnet-unindexed-" + f.region, Kind: subnetKind, Region: f.region, StackID: f.stack, VPCID: f.vpc, CIDR: "10.1.3.0/24", Revision: 1},
		} {
			if err := networkWrite(context.Background(), tx, item, true); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	allowed, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeSubnets","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ principal, action string }{{"subnet-reader", "ec2:DescribeSubnets"}, {"vpc-reader", "ec2:DescribeVpcs"}} {
		policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"` + entry.action + `","Resource":"*"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Identities.Set(entry.principal, policy); err != nil {
			t.Fatal(err)
		}
	}
	call := func(t *testing.T, region, identity string, parameters url.Values, instanceScope bool, status int) string {
		t.Helper()
		values := url.Values{"Action": {"DescribeSubnets"}, "Version": {"2016-11-15"}}
		for key, value := range parameters {
			values[key] = value
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "ec2")
		r.Header.Set("X-Simulator-Region", region)
		r.Header.Set("X-Simulator-Principal", identity)
		if instanceScope {
			ctx := context.WithValue(r.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: region, instance: "i-12345678"})
			r = r.WithContext(context.WithValue(ctx, sdkPolicyContextKey{}, allowed))
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("DescribeSubnets status %d, want %d: %s", w.Code, status, w.Body.String())
		}
		return w.Body.String()
	}
	type subnet struct {
		ID    string `xml:"subnetId"`
		VPC   string `xml:"vpcId"`
		CIDR  string `xml:"cidrBlock"`
		State string `xml:"state"`
	}
	decode := func(t *testing.T, body string) []subnet {
		t.Helper()
		var result struct {
			Root xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeSubnetsResponse"`
			ID   string   `xml:"requestId"`
			Set  *struct {
				Items []subnet `xml:"item"`
			} `xml:"subnetSet"`
		}
		if err := xml.Unmarshal([]byte(body), &result); err != nil || result.ID == "" || result.Set == nil {
			t.Fatalf("invalid AWS Query response envelope: %v, %s", err, body)
		}
		return result.Set.Items
	}
	t.Run("saved-owned-subnets-by-region", func(t *testing.T) {
		for _, tc := range []struct {
			region string
			want   subnet
		}{
			{east, subnet{"subnet-00000001", "vpc-00000001", "10.1.1.0/24", "available"}},
			{west, subnet{"subnet-00000002", "vpc-00000002", "10.1.2.0/24", "available"}},
		} {
			got := decode(t, call(t, tc.region, "subnet-reader", nil, false, 200))
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("%s: got %#v, want %#v", tc.region, got, tc.want)
			}
		}
	})
	t.Run("empty-list-is-explicit", func(t *testing.T) {
		for range 2 {
			if results := decode(t, call(t, empty, "subnet-reader", nil, false, 200)); len(results) != 0 {
				t.Fatalf("unpopulated region returned %v", results)
			}
		}
	})
	t.Run("unsupported-options-remain-explicit", func(t *testing.T) {
		for _, field := range []string{"Filter.1.Name", "SubnetId.1", "DryRun", "MaxResults", "NextToken"} {
			if body := call(t, east, "subnet-reader", url.Values{field: {"fixture"}}, false, 400); !strings.Contains(body, "<Code>UnsupportedOperation</Code>") {
				t.Fatalf("unsupported %s: %s", field, body)
			}
		}
	})
	t.Run("authorization-required", func(t *testing.T) {
		call(t, east, "vpc-reader", nil, false, 403)
	})
	t.Run("instance-console-cannot-read-subnets", func(t *testing.T) {
		call(t, east, "subnet-reader", nil, true, 403)
	})
	t.Run("different-stack-debt-fails-closed", func(t *testing.T) {
		if _, err := db.Exec("UPDATE aws_ec2_network_resources SET stack_id='not-the-owner' WHERE id='subnet-00000001'"); err != nil {
			t.Fatal(err)
		}
		body := call(t, east, "subnet-reader", nil, false, 500)
		if strings.Contains(body, "subnet-00000001") || strings.Contains(body, "not-the-owner") {
			t.Fatal("inconsistent ownership data reflected in response")
		}
	})
}
