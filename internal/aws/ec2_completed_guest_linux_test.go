//go:build linux

package aws

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Runs the real stack store and canonical runtime bookkeeping with the repository's
// inert VM fixture. This is not a QEMU, socket, guest-command or SQL transport test.
func TestCompletedBattleGuestRequiresOwnedStackAndReadyGeneration(t *testing.T) {
	fixture, _, _, _, _, _, ctx := vmBackendFixture(t)
	stacks, err := NewStackStore(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	var source *EC2VMProbeLaunch
	var earlyAllowed bool
	cfg := EC2InstanceRuntimeConfig{
		Lifetime: ctx, Bundle: fixture.bundle, Layout: fixture.layout, Directory: fixture.directory,
		ImageIDs: []string{"ami-local-pinned"}, OnReady: func(receipt *EC2VMProbeLaunch) error {
			source = receipt
			actual, err := source.Issuance()
			if err != nil {
				return err
			}
			current, err := stacks.Records(actual.Lease.Owner.Region, actual.Lease.Owner.Deployment)
			if err != nil || len(current) != 1 || current[0].Status != "CREATE_IN_PROGRESS" {
				t.Fatal("fixture did not observe launch before completed stack", err)
			}
			earlyAllowed = stacks.VerifyCompletedGuest(ctx, source) == nil
			return nil // Publication must not wait for completion from inside OnReady.
		},
	}
	runtime, err := NewEC2InstanceRuntime(fixture.db, fixture.networkLease.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtime.operations = fixture.ops // inert fixture, includes no real physical command endpoint
	if _, _, err = NewCanonicalSSMCommandStore(runtime); err != nil {
		t.Fatal(err)
	}
	if err = stacks.ConfigureInstanceRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if err != nil {
		t.Fatal(err)
	}
	stack, err := compileStackWithParameters("us-east-1", "completed-rds-prerequisite", string(raw),
		map[string]string{"NamePrefix": "tc-hello-team", "TenkaCloudAccountId": "000000000000",
			"ExternalId": "inert-fixture-id"}, "CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
			qualified := map[string]string{}
			for key := range keys {
				qualified[key] = "ami-local-pinned"
			}
			return qualified, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(stack); err != nil {
		t.Fatal(err)
	}
	if source == nil || earlyAllowed {
		t.Fatal("premature receipt accepted or no real lifecycle result")
	}
	assertAdmission := func(want bool) {
		t.Helper()
		if err := stacks.VerifyCompletedGuest(ctx, source); (err == nil) != want {
			t.Fatalf("completed-generation admission: want %v, got %v", want, err)
		}
	}
	assertAdmission(true)

	var canonicalDocument []byte
	if err = fixture.db.QueryRow("SELECT document FROM aws_cfn_stacks WHERE id=?", stack.ID).Scan(&canonicalDocument); err != nil {
		t.Fatal(err)
	}
	execute := func(query string, args ...any) {
		t.Helper()
		n, err := fixture.db.Exec(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := n.RowsAffected(); err != nil || rows != 1 {
			t.Fatal(rows, err)
		}
	}
	var completed StackRecord
	if json.Unmarshal(canonicalDocument, &completed) != nil || completed.Status != "CREATE_COMPLETE" {
		t.Fatal("fixture expected native CREATE_COMPLETE")
	}
	completed.Status = "CREATE_IN_PROGRESS"
	unfinished, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	execute("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(unfinished), stack.ID)
	assertAdmission(false) // VM is ready but CloudFormation finalization did not commit.
	execute("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(canonicalDocument), stack.ID)

	execute("UPDATE aws_ec2_instance_runtime SET state='launch_pending' WHERE stack_id=?", stack.ID)
	assertAdmission(false) // Completed CloudFormation row alone cannot substitute for ready runtime.
	execute("UPDATE aws_ec2_instance_runtime SET state='ready' WHERE stack_id=?", stack.ID)
	execute("UPDATE aws_cfn_ownership SET namespace='different-generation' WHERE stack_id=? AND kind='AWS::EC2::Instance'", stack.ID)
	assertAdmission(false)
	execute("UPDATE aws_cfn_ownership SET namespace=? WHERE stack_id=? AND kind='AWS::EC2::Instance' AND namespace='different-generation'",
		stackResourceNamespace(stack.Region, "AWS::EC2::Instance"), stack.ID)
	assertAdmission(true)

	alias, err := NewStackStore(fixture.db)
	if err != nil || alias.ConfigureInstanceRuntime(runtime) != nil {
		t.Fatal("could not prepare alias store", err)
	}
	if alias.VerifyCompletedGuest(ctx, source) == nil {
		t.Fatal("second store adopted a retained attempt")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if stacks.VerifyCompletedGuest(cancelled, source) == nil {
		t.Fatal("cancelled admission")
	}

	if err = stacks.Delete(stack.Region, stack.ID); err != nil {
		t.Fatal(err)
	}
	assertAdmission(false)
}
