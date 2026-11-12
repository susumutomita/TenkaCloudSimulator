package aws

import (
	"context"
	"database/sql"
	"testing"
)

// faultStacks prepares the native stores a stack template writes into.
func faultStacks(t *testing.T, db *sql.DB) *StackStore {
	t.Helper()
	if _, err := NewS3Store(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewParameterStore(db); err != nil {
		t.Fatal(err)
	}
	stacks, err := NewStackStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return stacks
}

func TestStackStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	templates := []struct {
		name, body, capability string
		params                 map[string]string
	}{
		{"api gateway graph", apiStackTemplate, "", nil},
		{"ssm parameter with events", eventTemplate, "", nil},
		{"iam role and instance profile", cfnProfileTemplate, "CAPABILITY_NAMED_IAM", nil},
		{"s3 bucket", s3StackTemplate, "", nil},
		{"battle network", networkTemplate(t), "", map[string]string{"NamePrefix": "tc-hello-team"}},
	}
	for _, tpl := range templates {
		tpl := tpl
		record, err := compileStack("us-east-1", "faults", tpl.body, tpl.params, tpl.capability)
		if err != nil {
			t.Fatal(tpl.name, err)
		}
		t.Run(tpl.name+"/create", func(t *testing.T) {
			t.Parallel()
			sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
				stacks := faultStacks(t, db)
				return func(context.Context) error { return stacks.Create(record) }
			})
		})
		t.Run(tpl.name+"/delete", func(t *testing.T) {
			t.Parallel()
			sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
				stacks := faultStacks(t, db)
				if err := stacks.Create(record); err != nil {
					t.Fatal(err)
				}
				return func(context.Context) error {
					rows, err := stacks.Records(record.Region, record.Name)
					if err != nil {
						return err
					}
					if len(rows) != 1 || rows[0].ID != record.ID {
						t.Fatalf("created stack listed as %v", rows)
					}
					return stacks.Delete(record.Region, record.ID)
				}
			})
		})
	}
}
