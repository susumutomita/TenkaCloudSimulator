//go:build linux

package aws

import (
	"strings"
	"testing"
)

func TestEC2InstanceReadyPublicationFailureSealsRetainedLaunch(t *testing.T) {
	b, _, _, process, control, launches, ctx := vmBackendFixture(t)
	iam, err := NewIAMRoleStore(b.db)
	if err != nil {
		t.Fatal(err)
	}
	profile := profileCreate(t, iam, "PublicationFaultProfile")
	var registered *EC2VMProbeLaunch
	registrations := 0
	r, err := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{
		Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory,
		ImageIDs: []string{"ami-pinned"},
		OnReady: func(receipt *EC2VMProbeLaunch) error {
			registrations++
			registered = receipt
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.operations = b.ops
	t.Cleanup(func() {
		if err := r.Shutdown(ctx); err != nil {
			t.Error("mock runtime shutdown:", err)
		}
	})
	plan, err := r.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, runtimeResource(profile.InstanceProfileName))
	if err != nil {
		t.Fatal(err)
	}
	// Fail only the final publication. Reservation and sealing remain writable,
	// so the failure cannot be confused with OnReady refusal or database loss.
	const fault = "injected EC2 ready publication failure"
	_, err = b.db.Exec(`CREATE TRIGGER reject_ec2_ready_publication
		BEFORE UPDATE OF state ON aws_ec2_instance_runtime
		WHEN NEW.state = 'ready'
		BEGIN SELECT RAISE(ABORT, 'injected EC2 ready publication failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := r.Launch(ctx, plan)
	if err == nil || !strings.Contains(err.Error(), fault) {
		t.Fatalf("expected injected publication failure, got %v", err)
	}
	if registrations != 1 || launches.Load() != 1 || receipt == nil || receipt != registered {
		t.Fatalf("lost retained launch: registrations=%d launches=%d receipt=%p registered=%p", registrations, launches.Load(), receipt, registered)
	}
	if receipt.Ready() {
		t.Fatal("failed publication left a ready receipt")
	}
	if states := runtimeState(t, b); len(states) != 1 || states[0] != "seal_pending" {
		t.Fatalf("failed publication was not durably sealed: %v", states)
	}
	if _, err := r.Launch(ctx, plan); err == nil {
		t.Fatal("sealed reservation accepted a retry")
	}
	if registrations != 1 || launches.Load() != 1 {
		t.Fatalf("retry repeated effects: registrations=%d launches=%d", registrations, launches.Load())
	}
	if err := r.WaitCleanup(ctx, plan.Region, plan.StackID, plan.InstanceID); err != nil {
		t.Fatal("retained mock cleanup:", err)
	}
	select {
	case <-process.closed:
	default:
		t.Fatal("mock process stream remained open after cleanup")
	}
	if !control.closed.Load() {
		t.Fatal("mock control remained open after cleanup")
	}
}
