package aws

import (
	"context"
	"encoding/json"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEC2ReadOwnedStateAndIsolation(t *testing.T) {
	store, _ := s3TestState(t)
	stacks, e := NewStackStore(store.Database())
	if e != nil {
		t.Fatal(e)
	}
	g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	db := store.Database()
	record := StackRecord{ID: "stack-one", Name: "one", Region: "us-east-1", Resources: []StackResource{{Type: "AWS::EC2::VPC", PhysicalID: "vpc-one"}, {Type: "AWS::EC2::Instance", PhysicalID: "i-one", Properties: map[string]any{"ImageId": "ami-local", "InstanceType": "t3.micro", "SubnetId": "subnet-one", "SecurityGroupIds": []any{"sg-one"}, "UserData": "DO-NOT-EXPOSE"}}}}
	data, _ := json.Marshal(record)
	if _, e = db.Exec(`INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)`, record.Region, record.Name, record.ID, string(data)); e != nil {
		t.Fatal(e)
	}
	for _, r := range record.Resources {
		if _, e = db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(record.Region, r.Type), r.Type, r.PhysicalID, record.ID); e != nil {
			t.Fatal(e)
		}
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []EC2NetworkResource{{ID: "vpc-one", Kind: "AWS::EC2::VPC", Region: record.Region, StackID: record.ID, CIDR: "10.0.0.0/16", Revision: 1}, {ID: "subnet-one", Kind: "AWS::EC2::Subnet", Region: record.Region, StackID: record.ID, VPCID: "vpc-one", Revision: 1}, {ID: "sg-one", Kind: "AWS::EC2::SecurityGroup", Region: record.Region, StackID: record.ID, VPCID: "vpc-one", Revision: 1}} {
		if e = networkWrite(context.Background(), tx, r, true); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	call := func(action, region, principal string, extra url.Values, want int) string {
		t.Helper()
		v := url.Values{"Action": {action}, "Version": {"2016-11-15"}}
		for k, x := range extra {
			v[k] = x
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "ec2")
		r.Header.Set("X-Simulator-Region", region)
		r.Header.Set("X-Simulator-Principal", principal)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got %d: %s", action, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if _, err := stacks.ec2Read(context.Background(), "us-east-1", "DescribeInstances"); err != nil {
		t.Fatal(err)
	}
	body := call("DescribeInstances", "us-east-1", "", nil, 200)
	for _, want := range []string{"<instanceId>i-one</instanceId>", "<vpcId>vpc-one</vpcId>", "<simulatorRuntimeState>unknown</simulatorRuntimeState>"} {
		if !strings.Contains(body, want) {
			t.Fatal(body)
		}
	}
	for _, secret := range []string{"DO-NOT-EXPOSE", "UserData", "<instanceState>", "stack-one"} {
		if strings.Contains(body, secret) {
			t.Fatal("leaked or fabricated field", body)
		}
	}
	if body = call("DescribeVpcs", "us-east-1", "", nil, 200); !strings.Contains(body, "<cidrBlock>10.0.0.0/16</cidrBlock>") {
		t.Fatal(body)
	}
	if body = call("DescribeInstances", "ap-northeast-1", "", nil, 200); strings.Contains(body, "i-one") {
		t.Fatal("region leak")
	}
	call("DescribeInstances", "us-east-1", "missing", nil, 403)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	call("DescribeInstances", "us-east-1", "reader", nil, 200)
	call("DescribeVpcs", "us-east-1", "reader", nil, 403)
	policy, err = core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:*","Resource":"*"},{"Effect":"Deny","Action":"ec2:DescribeInstances","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	call("DescribeInstances", "us-east-1", "reader", nil, 403)
	if _, err = db.Exec(`CREATE TABLE aws_ec2_instance_runtime(region TEXT,stack_id TEXT,instance TEXT,plan TEXT,state TEXT); INSERT INTO aws_ec2_instance_runtime VALUES('us-east-1','stack-one','i-one','SECRET-PLAN','ready')`); err != nil {
		t.Fatal(err)
	}
	body = call("DescribeInstances", "us-east-1", "", nil, 200)
	if !strings.Contains(body, "<simulatorRuntimeState>ready</simulatorRuntimeState>") || strings.Contains(body, "SECRET-PLAN") || strings.Contains(body, "running") {
		t.Fatal(body)
	}

	call("RunInstances", "us-east-1", "", nil, 400)
	call("DescribeVpcs", "us-east-1", "", url.Values{"Filter.1.Name": {"vpc-id"}}, 400)
	call("DescribeVpcs", "us-east-1", "", url.Values{"Version": {"bad"}}, 400)
	call("DescribeVpcs", "us-east-1", "", url.Values{"Action": {"DescribeVpcs", "DescribeInstances"}}, 400)
	if _, e = db.Exec(`UPDATE aws_ec2_network_resources SET stack_id='different' WHERE id='subnet-one'`); e != nil {
		t.Fatal(e)
	}
	call("DescribeInstances", "us-east-1", "", nil, 500)
}

func TestEC2ReadWireGuards(t *testing.T) {
	store, _ := s3TestState(t)
	stacks, err := NewStackStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	for _, tc := range []struct {
		name, method, path, body, header, value string
		status                                  int
	}{
		{"session", "POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "X-Amz-Security-Token", "secret", 400},
		{"region", "POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "X-Simulator-Region", "invalid", 400},
		{"real-credentials", "POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "Authorization", "AWS4-HMAC-SHA256 Credential=AKIAREAL/20261113/us-east-1/ec2/aws4_request", 403},
		{"scope", "POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "Authorization", "AWS4-HMAC-SHA256 Credential=TCSIM/20261113/ap-northeast-1/ec2/aws4_request", 400},
		{"get", "GET", "/", "Action=DescribeVpcs&Version=2016-11-15", "", "", 400},
		{"query", "POST", "/?other=1", "Action=DescribeVpcs&Version=2016-11-15", "", "", 400},
		{"malformed", "POST", "/", "Action=%xx", "", "", 400},
		{"large", "POST", "/", strings.Repeat("a", 8193), "", "", 400},
		{"empty", "POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("X-Simulator-Service", "ec2")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("X-Simulator-Region", "us-east-1")
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
