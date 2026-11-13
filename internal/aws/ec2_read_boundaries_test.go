package aws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func ec2ReadBoundaryCall(g *Gateway, action string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader("Action="+action+"&Version=2016-11-15"))
	r.Header.Set("X-Simulator-Service", "ec2")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestEC2ReadStoreAndHeaderFailures(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Security-Token", "X-Amz-Target"} {
		t.Run("duplicate-"+header, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeVpcs&Version=2016-11-15"))
			r.Header.Set("X-Simulator-Service", "ec2")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Del(header)
			r.Header.Add(header, "ec2")
			r.Header.Add(header, "ec2")
			w := httptest.NewRecorder()
			(&Gateway{}).ServeHTTP(w, r)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "Duplicate headers are unsupported") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if w := ec2ReadBoundaryCall(&Gateway{}, "DescribeVpcs"); w.Code != 500 || !strings.Contains(w.Body.String(), "Discovery stores are unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	store, _ := s3TestState(t)
	stacks, err := NewStackStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
	if err = store.Database().Close(); err != nil {
		t.Fatal(err)
	}
	if w := ec2ReadBoundaryCall(g, "DescribeVpcs"); w.Code != 500 || strings.Contains(w.Body.String(), "database is closed") {
		t.Fatalf("unavailable database leaked or succeeded: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeVpcs&Version=2016-11-15"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Service", "ec2")
	r.Header.Set("X-Simulator-Principal", "reader")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "Identity state unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestEC2ReadCorruptOwnershipRejectsResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*StackRecord)
	}{
		{"wrong-stack", func(s *StackRecord) { s.ID = "other" }},
		{"wrong-region", func(s *StackRecord) { s.Region = "ap-northeast-1" }},
		{"missing-resource", func(s *StackRecord) { s.Resources = nil }},
		{"duplicate-resource", func(s *StackRecord) { s.Resources = append(s.Resources, s.Resources[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := s3TestState(t)
			db := store.Database()
			stacks, err := NewStackStore(db)
			if err != nil {
				t.Fatal(err)
			}
			record := StackRecord{ID: "stack-one", Name: "one", Region: "us-east-1", Resources: []StackResource{{Type: "AWS::EC2::VPC", PhysicalID: "vpc-one"}}}
			doc, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO aws_cfn_stacks VALUES('us-east-1','one','stack-one',?)`, string(doc)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace("us-east-1", "AWS::EC2::VPC"), "AWS::EC2::VPC", "vpc-one", "stack-one"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = networkWrite(context.Background(), tx, EC2NetworkResource{ID: "vpc-one", Kind: "AWS::EC2::VPC", Region: "us-east-1", StackID: "stack-one", CIDR: "10.0.0.0/16", Revision: 1}, true); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			g := &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
			if valid := ec2ReadBoundaryCall(g, "DescribeVpcs"); valid.Code != 200 || !strings.Contains(valid.Body.String(), "<vpcId>vpc-one</vpcId>") {
				t.Fatalf("invalid baseline fixture: %d %s", valid.Code, valid.Body.String())
			}
			tc.mutate(&record)
			doc, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE aws_cfn_stacks SET document=? WHERE id='stack-one'`, string(doc)); err != nil {
				t.Fatal(err)
			}
			w := ec2ReadBoundaryCall(g, "DescribeVpcs")
			if w.Code != 500 || strings.Contains(w.Body.String(), "<vpcSet>") || strings.Contains(w.Body.String(), "vpc-one") {
				t.Fatalf("corrupt state succeeded or leaked partial data: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEC2ReadTagsStableOrder(t *testing.T) {
	tags := ec2ReadTags(map[string]string{"z": "last", "a": "first"})
	if len(tags) != 2 || tags[0].Key != "a" || tags[0].Value != "first" || tags[1].Key != "z" || tags[1].Value != "last" {
		t.Fatalf("unstable tags: %+v", tags)
	}
}

func TestEC2ReadRejectsUnprojectableInstances(t *testing.T) {
	for _, tc := range []struct {
		name  string
		props map[string]any
	}{
		{"invalid-tags", map[string]any{"SubnetId": "subnet-one", "Tags": true, "SecurityGroupIds": []any{}}},
		{"invalid-groups", map[string]any{"SubnetId": "subnet-one", "SecurityGroupIds": "sg-one"}},
		{"missing-group", map[string]any{"SubnetId": "subnet-one", "SecurityGroupIds": []any{"sg-missing"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := s3TestState(t)
			db := store.Database()
			stacks, err := NewStackStore(db)
			if err != nil {
				t.Fatal(err)
			}
			record := StackRecord{ID: "stack-one", Name: "one", Region: "us-east-1", Resources: []StackResource{{Type: "AWS::EC2::Instance", PhysicalID: "i-one", Properties: tc.props}}}
			doc, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO aws_cfn_stacks VALUES('us-east-1','one','stack-one',?)`, string(doc)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace("us-east-1", "AWS::EC2::Instance"), "AWS::EC2::Instance", "i-one", "stack-one"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = networkWrite(context.Background(), tx, EC2NetworkResource{ID: "subnet-one", Kind: "AWS::EC2::Subnet", Region: "us-east-1", StackID: "stack-one", VPCID: "vpc-one", Revision: 1}, true); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			w := ec2ReadBoundaryCall(&Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}, "DescribeInstances")
			if w.Code != 500 || strings.Contains(w.Body.String(), "<reservationSet>") || strings.Contains(w.Body.String(), "i-one") {
				t.Fatalf("invalid instance returned partial data: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
