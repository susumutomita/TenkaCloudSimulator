package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestAutoScalingPlansStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	decode := func(t *testing.T, raw string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(raw), v); err != nil {
			t.Fatal(err)
		}
	}
	var source ASPApplicationSource
	decode(t, aspTags("shop"), &source)
	var read, write, narrowed ASPInstruction
	decode(t, aspInstructionJSON("orders", autoScalingReadDimension, 5, 50, ""), &read)
	decode(t, aspInstructionJSON("orders", autoScalingWriteDimension, 1, 20, ""), &write)
	decode(t, aspInstructionJSON("orders", autoScalingReadDimension, 2, 4, ""), &narrowed)
	build := func(t *testing.T, db *sql.DB, planned bool) *AutoScalingPlansStore {
		t.Helper()
		dynamo, err := NewDynamoDBStore(db)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = dynamo.CreateTable(DynamoDBTable{Region: "us-east-1", Name: "orders", KeySchema: []DynamoDBKeyElement{{"id", "HASH"}}, Definitions: map[string]string{"id": "S"}, BillingMode: "PROVISIONED", ReadUnits: 1, WriteUnits: 100}); err != nil {
			t.Fatal(err)
		}
		plans, err := NewAutoScalingPlansStore(db, dynamo)
		if err != nil {
			t.Fatal(err)
		}
		if planned {
			if err = plans.Create(context.Background(), "us-east-1", "shop", source, []ASPInstruction{read, write}); err != nil {
				t.Fatal(err)
			}
		}
		return plans
	}
	steps := []struct {
		name    string
		planned bool
		op      func(ctx context.Context, s *AutoScalingPlansStore) error
	}{
		{"create bounds table capacity", false, func(ctx context.Context, s *AutoScalingPlansStore) error {
			return s.Create(ctx, "us-east-1", "shop", source, []ASPInstruction{read, write})
		}},
		{"update instructions", true, func(ctx context.Context, s *AutoScalingPlansStore) error {
			return s.Update(ctx, "us-east-1", "shop", nil, []ASPInstruction{narrowed})
		}},
		{"describe plans and resources", true, func(ctx context.Context, s *AutoScalingPlansStore) error {
			plans, _, err := s.Plans(ctx, "us-east-1", ASPPlanQuery{Limit: 10})
			if err != nil {
				return err
			}
			if len(plans) != 1 {
				t.Fatalf("plans %v", plans)
			}
			resources, _, err := s.Resources(ctx, "us-east-1", "shop", "", 10)
			if err == nil && len(resources) != 2 {
				t.Fatalf("resources %v", resources)
			}
			return err
		}},
		{"delete", true, func(ctx context.Context, s *AutoScalingPlansStore) error { return s.Delete(ctx, "us-east-1", "shop") }},
	}
	for _, step := range steps {
		step := step
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
				s := build(t, db, step.planned)
				return func(ctx context.Context) error { return step.op(ctx, s) }
			})
		})
	}
}
