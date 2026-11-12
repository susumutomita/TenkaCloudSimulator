//go:build linux

package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

func TestCanonicalInstancePlanRejectsForgedMemoryOnly(t *testing.T) {
	b, _, _, _, _, _, ctx := vmBackendFixture(t)
	runtime, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: func(*EC2VMProbeLaunch) error { return nil }})
	if e != nil {
		t.Fatal(e)
	}
	resource := StackResource{LogicalID: "Instance", Type: "AWS::EC2::Instance", Properties: map[string]any{"ImageId": "ami-pinned", "InstanceType": "t3.micro", "IamInstanceProfile": "Profile", "SubnetId": "subnet-test", "SecurityGroupIds": []any{"sg-test"}, "UserData": base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\noriginal bytes\n"))}}
	plan, e := runtime.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, resource)
	if e != nil {
		t.Fatal(e)
	}
	plan.UserData[0] = 'x'
	if _, e = runtime.Launch(ctx, plan); e == nil {
		t.Fatal("mutated issued plan accepted")
	}
	fake := *plan
	fake.runtime = nil
	if _, e = runtime.Launch(ctx, &fake); e == nil {
		t.Fatal("unissued plan accepted")
	}
	var n int
	if e = b.db.QueryRow(`SELECT count(*) FROM aws_ec2_instance_runtime`).Scan(&n); e != nil || n != 0 {
		t.Fatal("invalid plan reserved/started", n, e)
	}
}
func TestCanonicalInstanceRuntimeMemoryLaunchAndSealPublication(t *testing.T) {
	for _, sealAtReady := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "seal-during-registration"}[sealAtReady], func(t *testing.T) {
			b, _, _, _, _, calls, ctx := vmBackendFixture(t)
			iam, e := NewIAMRoleStore(b.db)
			if e != nil {
				t.Fatal(e)
			}
			profile := profileCreate(t, iam, "RuntimeProfile")
			var runtime *EC2InstanceRuntime
			config := EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}}
			readyCalls := 0
			config.OnReady = func(receipt *EC2VMProbeLaunch) error {
				readyCalls++
				if !receipt.Ready() {
					t.Fatal("registered beforeactualready")
				}
				if sealAtReady {
					issuance, err := receipt.Issuance()
					if err != nil {
						return err
					}
					return runtime.Seal(ctx, issuance.Lease.Owner.Region, issuance.Lease.Owner.Deployment, issuance.Lease.Owner.Instance)
				}
				return nil
			}
			runtime, e = NewEC2InstanceRuntime(b.db, b.networkLease.store, config)
			if e != nil {
				t.Fatal(e)
			}
			runtime.operations = b.ops // Private test-only OS boundary: no process/socket/QGA.
			resource := StackResource{LogicalID: "Instance", Type: "AWS::EC2::Instance", Properties: map[string]any{"ImageId": "ami-pinned", "InstanceType": "t3.micro", "IamInstanceProfile": profile.InstanceProfileName, "SubnetId": "subnet-test", "SecurityGroupIds": []any{"sg-test"}, "UserData": base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\nunchanged\n"))}}
			plan, e := runtime.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, resource)
			if e != nil {
				t.Fatal(e)
			}
			receipt, e := runtime.Launch(ctx, plan)
			if receipt == nil || calls.Load() != 1 || readyCalls != 1 {
				t.Fatal("missing coherentstartup", receipt, e, calls.Load(), readyCalls)
			}
			if sealAtReady && e == nil {
				t.Fatal("late ready afterterminalseal")
			}
			if !sealAtReady && e != nil {
				t.Fatal(e)
			}
			var state string
			if err := b.db.QueryRow(`SELECT state FROM aws_ec2_instance_runtime`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if sealAtReady && state != "seal_pending" {
				t.Fatal("late durablepublication", state)
			}
			if !sealAtReady && state != "ready" {
				t.Fatal(state)
			}
			if e = runtime.Shutdown(ctx); e != nil {
				t.Fatal(e)
			}
			if _, e = runtime.Launch(ctx, plan); e == nil {
				t.Fatal("replayed instance startup")
			}
		})
	}
}
func TestCanonicalInstanceAttemptCleanupRequiresSealMemoryOnly(t *testing.T) {
	a := &ec2StackAttempt{done: make(chan struct{})}
	if e := a.WaitCleanup(context.Background()); e == nil {
		t.Fatal("unsealed futurelaunch declared settled")
	}
	if e := a.Seal(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := a.WaitCleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := a.Launch(context.Background()); e == nil {
		t.Fatal("sealed attempt launched")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if e := a.WaitCleanup(cancelled); e == nil || !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal(e)
	}
}

func TestCanonicalInstanceShutdownRejectsPreparedLaunchMemoryOnly(t *testing.T) {
	b, _, _, _, _, calls, ctx := vmBackendFixture(t)
	runtime, err := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: func(*EC2VMProbeLaunch) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	resource := StackResource{LogicalID: "Instance", Type: "AWS::EC2::Instance", Properties: map[string]any{"ImageId": "ami-pinned", "InstanceType": "t3.micro", "IamInstanceProfile": "Profile", "SubnetId": "subnet-test", "SecurityGroupIds": []any{"sg-test"}, "UserData": base64.StdEncoding.EncodeToString([]byte("original"))}}
	plan, err := runtime.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, resource)
	if err != nil {
		t.Fatal(err)
	}
	delayed, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() { close(delayed); <-release; _, err := runtime.Launch(ctx, plan); result <- err }()
	<-delayed
	if err = runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-result; err == nil || calls.Load() != 0 {
		t.Fatal("late prepared launch escaped shutdown", err)
	}
	if _, err = runtime.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, resource); err == nil {
		t.Fatal("post-shutdown plan accepted")
	}
	if _, err = NewCanonicalSSMSessionBackend(runtime); err == nil {
		t.Fatal("post-shutdown adapter accepted")
	}
	var count int
	if err = b.db.QueryRow(`SELECT count(*) FROM aws_ec2_instance_runtime`).Scan(&count); err != nil || count != 0 {
		t.Fatal("shutdown admitted reservation", err, count)
	}
}

func TestEC2RuntimePublicImageParameterUsesFirstPinnedImageOnly(t *testing.T) {
	r := &EC2InstanceRuntime{config: EC2InstanceRuntimeConfig{ImageIDs: []string{"ami-0a1b2c3d4e5f60718", "ami-11111111"}}}
	if v, ok := r.PublicImageParameter("/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-x86_64"); !ok || v != "ami-0a1b2c3d4e5f60718" {
		t.Fatal(v, ok)
	}
	for _, name := range []string{"/aws/service/ami-amazon-linux-latest/", "/aws/service/ami-windows-latest/x", "/tc/ami", ""} {
		if _, ok := r.PublicImageParameter(name); ok {
			t.Fatal("unexpected alias for", name)
		}
	}
	var none *EC2InstanceRuntime
	if _, ok := none.PublicImageParameter("/aws/service/ami-amazon-linux-latest/x"); ok {
		t.Fatal("nil runtime resolved an alias")
	}
}
