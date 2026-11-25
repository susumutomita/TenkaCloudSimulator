package aws

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Check recovery cases the writer itself refuses to create. Start from its real
// saved graph; change only the witness named in each subtest.
func checkEC2RouteGraph(t *testing.T, g *Gateway, stack StackRecord, ids map[string]string) {
	t.Helper()
	tx, err := g.Stacks.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := networkList(t.Context(), tx, stack.Region, stack.ID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ec2RouteClaims(t.Context(), tx, stack.Region, stack.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	type graph struct {
		Stack  StackRecord
		Rows   []EC2NetworkResource
		Claims map[string]string
	}
	original, err := json.Marshal(graph{stack, rows, claims})
	if err != nil {
		t.Fatal(err)
	}
	record := func(d *graph, name string) *StackResource {
		for i := range d.Stack.Resources {
			if d.Stack.Resources[i].LogicalID == name {
				return &d.Stack.Resources[i]
			}
		}
		panic(name)
	}
	row := func(d *graph, name string) *EC2NetworkResource {
		for i := range d.Rows {
			if d.Rows[i].ID == ids[name] {
				return &d.Rows[i]
			}
		}
		panic(name)
	}
	duplicate := func(d *graph, name string) {
		m := *record(d, name)
		r := *row(d, name)
		m.LogicalID += "Copy"
		copy, err := compileNetworkResource(stack.Region, stack.ID, m.LogicalID, m.Type, m.Properties)
		if err != nil {
			t.Fatal(err)
		}
		r.ID = copy.ID
		m.PhysicalID = copy.ID
		d.Stack.Resources = append(d.Stack.Resources, m)
		d.Rows = append(d.Rows, r)
		d.Claims[copy.ID] = r.Kind
	}
	for _, tc := range []struct {
		name   string
		change func(*graph)
		valid  bool
	}{
		{"unrelated group is not a route", func(d *graph) {
			d.Rows = append(d.Rows, EC2NetworkResource{Kind: ec2SecurityGroupKind})
			d.Stack.Resources = append(d.Stack.Resources, StackResource{Type: ec2SecurityGroupKind})
		}, true},
		{"duplicate physical row", func(d *graph) { d.Rows = append(d.Rows, *row(d, "Table")) }, false},
		{"orphan typed claim", func(d *graph) { d.Claims["unused-table"] = ec2RT }, false},
		{"duplicate declaration", func(d *graph) { d.Stack.Resources = append(d.Stack.Resources, *record(d, "Table")) }, false},
		{"changed saved destination", func(d *graph) { row(d, "Default").CIDR = "10.4.0.0/16" }, false},
		{"association target is a VPC not a table", func(d *graph) {
			record(d, "Assoc").Properties["RouteTableId"] = ids["Vpc"]
			row(d, "Assoc").RouteTableID = ids["Vpc"]
		}, false},
		{"changed saved parent", func(d *graph) { row(d, "Table").VPCID = ids["Other"] }, false},
		{"changed saved tags", func(d *graph) { row(d, "Table").Tags["Name"] = "changed" }, false},
		{"unrepresentable XML tag", func(d *graph) {
			record(d, "Table").Properties["Tags"] = []any{map[string]any{"Key": "x", "Value": "\x00"}}
			row(d, "Table").Tags = map[string]string{"x": "\x00"}
		}, false},
		{"duplicate gateway attachment", func(d *graph) { duplicate(d, "Attach") }, false},
		{"network row without declaration", func(d *graph) {
			for i, m := range d.Stack.Resources {
				if m.LogicalID == "Empty" {
					d.Stack.Resources = append(d.Stack.Resources[:i], d.Stack.Resources[i+1:]...)
					break
				}
			}
		}, false},
		{"duplicate default", func(d *graph) { duplicate(d, "Default") }, false},
		{"duplicate association", func(d *graph) { duplicate(d, "Assoc") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d graph
			if err := json.Unmarshal(original, &d); err != nil {
				t.Fatal(err)
			}
			tc.change(&d)
			got, err := projectEC2RouteTables(d.Stack, d.Rows, d.Claims)
			if tc.valid {
				if err != nil || len(got) != 2 {
					t.Fatalf("unrelated records changed table read: %v %+v", err, got)
				}
			} else if !errors.Is(err, errEC2RouteSnapshot) || len(got) != 0 {
				t.Fatalf("damaged graph was not rejected atomically: %v %+v", err, got)
			}
		})
	}
}

func TestEC2RouteTablesIncompleteStore(t *testing.T) {
	store, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.Database()
	reader := &StackStore{db: db}
	const region = "us-east-1"
	reject := func() {
		t.Helper()
		got, err := reader.describeEC2RouteTables(t.Context(), region)
		if err == nil || len(got) != 0 {
			t.Fatalf("unavailable or damaged graph was published: %v %+v", err, got)
		}
	}
	reject() // New database without a network schema.
	if _, err = NewStackStore(db); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err = db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO aws_ec2_network_resources VALUES(?,?,?,?,?,?)", region, "table", "orphan", ec2RT, "{}", 1)
	reject()
	exec("INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)", region, "net", "orphan", "{}")
	reject()
	rec, _ := json.Marshal(StackRecord{ID: "orphan", Name: "net", Region: region})
	exec("UPDATE aws_cfn_stacks SET document=? WHERE id='orphan'", string(rec))
	reject()
	// A stable typed table cannot share its physical identity with a subnet.
	r, _ := json.Marshal(EC2NetworkResource{Kind: ec2RT, ID: "table", StackID: "orphan", Region: region, Revision: 1})
	exec("UPDATE aws_ec2_network_resources SET document=? WHERE id='table'", string(r))
	ns := stackResourceNamespace(region, ec2RT)
	exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", ns, ec2RT, "table", "orphan")
	exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", ns, ec2RTSubnet, "table", "orphan")
	reject()
	exec("DELETE FROM aws_cfn_ownership WHERE kind=?", ec2RTSubnet)
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1000)
       INSERT INTO aws_cfn_ownership SELECT ?,?,printf('sub-%d',i),'orphan' FROM n`, ns, ec2RTSubnet)
	reject()
	// Reject an over-bound owner set without silently truncating (raw orphan
	// rows cannot disappear through a join to the stack/ownership tables).
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1000)
       INSERT INTO aws_ec2_network_resources SELECT ?,printf('rt-%d',i),printf('owner-%d',i),?,'{}',1 FROM n`, region, ec2RT)
	reject()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reject()
}

// A declaration alone is deliberately not an enumeration source. Never claim
// visibility or detection of an empty table whose final two witnesses are gone.
func TestEC2RouteTablesBothWitnessesGone(t *testing.T) {
	const region = "us-east-1"
	s, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := NewStackStore(s.Database())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := compileStack(region, "empty-table", `{"Resources":{"V":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.0.0.0/16"}},"T":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"V"}}}}}`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Create(stack); err != nil {
		t.Fatal(err)
	}
	got, err := st.describeEC2RouteTables(t.Context(), region)
	if err != nil || len(got) != 1 {
		t.Fatalf("fixture: %+v %v", got, err)
	}
	id := got[0].ID
	change := func(q string) {
		t.Helper()
		r, e := s.Database().Exec(q, id)
		if e != nil {
			t.Fatal(e)
		}
		n, e := r.RowsAffected()
		if e != nil || n != 1 {
			t.Fatalf("%s: %d %v", q, n, e)
		}
	}
	change("DELETE FROM aws_ec2_network_resources WHERE id=?")
	if got, err = st.describeEC2RouteTables(t.Context(), region); err == nil || len(got) != 0 {
		t.Fatalf("remaining typed claim did not detect missing row: %+v %v", got, err)
	}
	change("DELETE FROM aws_cfn_ownership WHERE physical_id=?")
	if got, err = st.describeEC2RouteTables(t.Context(), region); err != nil || len(got) != 0 {
		t.Fatalf("invented row or detection without witnesses: %+v %v", got, err)
	}
}
