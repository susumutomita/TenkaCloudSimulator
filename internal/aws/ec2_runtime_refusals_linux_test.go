//go:build linux

package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func runtimeResource(profile string) StackResource {
	return StackResource{LogicalID: "Instance", Type: "AWS::EC2::Instance", Properties: map[string]any{"ImageId": "ami-pinned", "InstanceType": "t3.micro", "IamInstanceProfile": profile, "SubnetId": "subnet-test", "SecurityGroupIds": []any{"sg-test"}, "UserData": base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\ntrue\n"))}}
}

func runtimeState(t *testing.T, b *EC2VMBackend) []string {
	t.Helper()
	rows, e := b.db.Query(`SELECT state FROM aws_ec2_instance_runtime ORDER BY instance`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			t.Fatal(e)
		}
		states = append(states, s)
	}
	return states
}

func TestEC2InstanceRuntimeRefusesIncompleteConfiguration(t *testing.T) {
	b, _, _, _, _, _, ctx := vmBackendFixture(t)
	ok := func(*EC2VMProbeLaunch) error { return nil }
	valid := EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: ok}
	unbounded := context.Background()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	tooLong, cancelLong := context.WithDeadline(context.Background(), time.Now().Add(48*time.Hour))
	defer cancelLong()
	for name, change := range map[string]func(*EC2InstanceRuntimeConfig){
		"no lifetime":           func(c *EC2InstanceRuntimeConfig) { c.Lifetime = nil },
		"no bundle":             func(c *EC2InstanceRuntimeConfig) { c.Bundle = nil },
		"no pinned images":      func(c *EC2InstanceRuntimeConfig) { c.ImageIDs = nil },
		"no ready registration": func(c *EC2InstanceRuntimeConfig) { c.OnReady = nil },
		"unbounded lifetime":    func(c *EC2InstanceRuntimeConfig) { c.Lifetime = unbounded },
		"expired lifetime":      func(c *EC2InstanceRuntimeConfig) { c.Lifetime = expired },
		"lifetime beyond max":   func(c *EC2InstanceRuntimeConfig) { c.Lifetime = tooLong },
		"wrong session image":   func(c *EC2InstanceRuntimeConfig) { c.SessionImageSHA256 = strings.Repeat("0", 64) },
	} {
		config := valid
		change(&config)
		if r, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, config); e == nil || r != nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, e := NewEC2InstanceRuntime(nil, b.networkLease.store, valid); e == nil {
		t.Fatal("runtime without database accepted")
	}
	if _, e := NewEC2InstanceRuntime(b.db, nil, valid); e == nil {
		t.Fatal("runtime without network store accepted")
	}
	// The runtime keeps its own copy of the pinned image list.
	images := []string{"ami-pinned"}
	config := valid
	config.ImageIDs = images
	r, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, config)
	if e != nil {
		t.Fatal(e)
	}
	images[0] = "ami-swapped"
	if _, e = r.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, runtimeResource("Profile")); e != nil {
		t.Fatal("caller mutation changed the pinned image list:", e)
	}
}

func TestEC2InstancePrepareRefusesUnboundedProperties(t *testing.T) {
	b, _, _, _, _, _, ctx := vmBackendFixture(t)
	r, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: func(*EC2VMProbeLaunch) error { return nil }})
	if e != nil {
		t.Fatal(e)
	}
	stack := StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}
	plan, e := r.Prepare(ctx, stack, runtimeResource("Profile"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(plan.InstanceID, "i-") || len(plan.InstanceID) != 19 || plan.InstanceType != "t3.micro" || string(plan.UserData) != "#!/bin/bash\ntrue\n" {
		t.Fatalf("unexpected canonical plan %+v", plan)
	}
	again := runtimeResource("Profile")
	again.PhysicalID = plan.InstanceID
	if replay, e := r.Prepare(ctx, stack, again); e != nil || replay.InstanceID != plan.InstanceID {
		t.Fatal("deterministic physical ID not accepted on replay", e)
	}
	small := runtimeResource("Profile")
	small.Properties["InstanceType"] = "t3.small"
	if _, e = r.Prepare(ctx, stack, small); e != nil {
		t.Fatal(e)
	}
	bigUserData := base64.StdEncoding.EncodeToString(make([]byte, 16385))
	sixGroups := []any{"sg-1", "sg-2", "sg-3", "sg-4", "sg-5", "sg-6"}
	cases := map[string]func(*StackResource){
		"wrong resource type":    func(s *StackResource) { s.Type = "AWS::EC2::Volume" },
		"unsupported property":   func(s *StackResource) { s.Properties["KeyName"] = "key" },
		"missing image":          func(s *StackResource) { delete(s.Properties, "ImageId") },
		"unpinned image":         func(s *StackResource) { s.Properties["ImageId"] = "ami-remote" },
		"non-string type":        func(s *StackResource) { s.Properties["InstanceType"] = 3 },
		"unsupported type":       func(s *StackResource) { s.Properties["InstanceType"] = "m5.24xlarge" },
		"missing subnet":         func(s *StackResource) { s.Properties["SubnetId"] = "" },
		"missing profile":        func(s *StackResource) { delete(s.Properties, "IamInstanceProfile") },
		"missing user data":      func(s *StackResource) { delete(s.Properties, "UserData") },
		"non-base64 user data":   func(s *StackResource) { s.Properties["UserData"] = "not base64!" },
		"oversized user data":    func(s *StackResource) { s.Properties["UserData"] = bigUserData },
		"security groups scalar": func(s *StackResource) { s.Properties["SecurityGroupIds"] = "sg-test" },
		"no security groups":     func(s *StackResource) { s.Properties["SecurityGroupIds"] = []any{} },
		"too many groups":        func(s *StackResource) { s.Properties["SecurityGroupIds"] = sixGroups },
		"duplicate group":        func(s *StackResource) { s.Properties["SecurityGroupIds"] = []any{"sg-test", "sg-test"} },
		"non-string group":       func(s *StackResource) { s.Properties["SecurityGroupIds"] = []any{7} },
		"foreign physical ID":    func(s *StackResource) { s.PhysicalID = "i-0123456789abcdef0" },
	}
	for name, change := range cases {
		resource := runtimeResource("Profile")
		change(&resource)
		if p, e := r.Prepare(ctx, stack, resource); e == nil || p != nil {
			t.Fatalf("%s accepted", name)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = r.Prepare(cancelled, stack, runtimeResource("Profile")); e == nil {
		t.Fatal("cancelled preparation accepted")
	}
	var nilRuntime *EC2InstanceRuntime
	if _, e = nilRuntime.Prepare(ctx, stack, runtimeResource("Profile")); e == nil {
		t.Fatal("nil runtime prepared a plan")
	}
	if got := runtimeState(t, b); len(got) != 0 {
		t.Fatalf("preparation wrote durable state: %v", got)
	}
}

func TestEC2InstanceLaunchFailuresSealTheReservation(t *testing.T) {
	t.Run("unknown instance profile", func(t *testing.T) {
		b, _, _, _, _, calls, ctx := vmBackendFixture(t)
		r, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: func(*EC2VMProbeLaunch) error { return nil }})
		if e != nil {
			t.Fatal(e)
		}
		r.operations = b.ops
		if _, e = NewIAMRoleStore(b.db); e != nil {
			t.Fatal(e)
		}
		plan, e := r.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, runtimeResource("MissingProfile"))
		if e != nil {
			t.Fatal(e)
		}
		if receipt, e := r.Launch(ctx, plan); e == nil || receipt != nil || calls.Load() != 0 {
			t.Fatal("launch without a resolvable instance profile", receipt, e, calls.Load())
		}
		if got := runtimeState(t, b); len(got) != 1 || got[0] != "seal_pending" {
			t.Fatalf("failed launch not sealed: %v", got)
		}
		if _, e = r.Launch(ctx, plan); e == nil {
			t.Fatal("sealed reservation relaunched")
		}
		if e = r.Shutdown(ctx); e != nil && !errors.Is(e, ErrEC2GuestAttachmentPending) {
			t.Fatal(e)
		}
	})
	t.Run("ready registration refused", func(t *testing.T) {
		b, _, _, _, _, calls, ctx := vmBackendFixture(t)
		iam, e := NewIAMRoleStore(b.db)
		if e != nil {
			t.Fatal(e)
		}
		profile := profileCreate(t, iam, "RefusedProfile")
		refused := errors.New("scorer refused registration")
		var registered *EC2VMProbeLaunch
		r, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: func(receipt *EC2VMProbeLaunch) error { registered = receipt; return refused }})
		if e != nil {
			t.Fatal(e)
		}
		r.operations = b.ops
		plan, e := r.Prepare(ctx, StackRecord{ID: b.owner.Deployment, Region: b.owner.Region}, runtimeResource(profile.InstanceProfileName))
		if e != nil {
			t.Fatal(e)
		}
		receipt, e := r.Launch(ctx, plan)
		if !errors.Is(e, refused) || receipt == nil || receipt != registered || calls.Load() != 1 {
			t.Fatal("refused registration not surfaced", receipt, e, calls.Load())
		}
		if receipt.Ready() {
			t.Fatal("receipt still ready after its registration was refused")
		}
		if got := runtimeState(t, b); len(got) != 1 || got[0] != "seal_pending" {
			t.Fatalf("refused registration published state %v", got)
		}
		if e = r.Shutdown(ctx); e != nil {
			t.Fatal(e)
		}
	})
}

func TestEC2InstanceRuntimeSealRequiresTheRetainedOwner(t *testing.T) {
	b, _, _, _, _, _, ctx := vmBackendFixture(t)
	r, e := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory, ImageIDs: []string{"ami-pinned"}, OnReady: func(*EC2VMProbeLaunch) error { return nil }})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Seal(ctx, b.owner.Region, b.owner.Deployment, "i-unknown"); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("unknown instance sealed", e)
	}
	if e = r.WaitCleanup(ctx, b.owner.Region, b.owner.Deployment, "i-unknown"); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("unknown instance reported settled", e)
	}
	r.retained["i-retained"] = &ec2InstanceRuntimeEntry{plan: EC2InstancePlan{Region: b.owner.Region, StackID: b.owner.Deployment, InstanceID: "i-retained"}, done: make(chan struct{})}
	if e = r.Seal(ctx, "eu-west-1", b.owner.Deployment, "i-retained"); !errors.Is(e, ErrEC2GuestAttachmentPending) || r.retained["i-retained"].sealed {
		t.Fatal("foreign region sealed a retained instance", e)
	}
	if e = r.Seal(ctx, b.owner.Region, "other-stack", "i-retained"); !errors.Is(e, ErrEC2GuestAttachmentPending) || r.retained["i-retained"].sealed {
		t.Fatal("foreign stack sealed a retained instance", e)
	}
	waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if e = r.WaitCleanup(waiting, b.owner.Region, b.owner.Deployment, "i-retained"); !errors.Is(e, context.DeadlineExceeded) || !r.retained["i-retained"].sealed {
		t.Fatal("in-flight launch reported settled before it finished", e)
	}
	close(r.retained["i-retained"].done)
	if e = r.WaitCleanup(ctx, b.owner.Region, b.owner.Deployment, "i-retained"); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("launch without a backend reported clean", e)
	}
	r.retained["i-retained"].backend = b
	if e = r.WaitCleanup(ctx, b.owner.Region, b.owner.Deployment, "i-retained"); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("backend without a process reported clean", e)
	}
	stuck, cancelStuck := context.WithCancel(ctx)
	cancelStuck()
	delete(r.retained, "i-retained")
	r.retained["i-stuck"] = &ec2InstanceRuntimeEntry{plan: EC2InstancePlan{Region: b.owner.Region, StackID: b.owner.Deployment, InstanceID: "i-stuck"}, done: make(chan struct{})}
	r.launches = 1
	if e = r.Shutdown(stuck); !errors.Is(e, context.Canceled) {
		t.Fatal("shutdown ignored an in-flight launch", e)
	}
	var nilRuntime *EC2InstanceRuntime
	if e = nilRuntime.Shutdown(ctx); e == nil {
		t.Fatal("nil runtime shutdown reported clean")
	}
}

func TestInstanceNICDescriptorRequiresIPv4SubnetAndGateway(t *testing.T) {
	owner := EC2GuestOwner{Account: "000000000000", Deployment: "stack", Instance: "i-0123456789abcdef0"}
	d, seed, e := instanceNICDescriptor(owner, EC2NetworkSnapshot{SubnetCIDR: "10.0.1.0/24", GatewayIPv4: "10.0.1.1"})
	if e != nil {
		t.Fatal(e)
	}
	if d.Config.NIC.PeerIP.String() != "10.0.1.4" || seed.Address != "10.0.1.4/24" || seed.Gateway != "10.0.1.1" || len(d.Services) != 2 {
		t.Fatalf("unexpected descriptor %+v %+v", d.Config.NIC, seed)
	}
	again, _, _ := instanceNICDescriptor(owner, EC2NetworkSnapshot{SubnetCIDR: "10.0.1.0/24", GatewayIPv4: "10.0.1.1"})
	if again.Config.NIC.PeerMAC != d.Config.NIC.PeerMAC {
		t.Fatal("guest MAC is not deterministic per owner")
	}
	for _, s := range []EC2NetworkSnapshot{{SubnetCIDR: "bad", GatewayIPv4: "10.0.1.1"}, {SubnetCIDR: "fd00::/64", GatewayIPv4: "10.0.1.1"}, {SubnetCIDR: "10.0.1.0/24", GatewayIPv4: "gateway"}, {SubnetCIDR: "10.0.1.0/30", GatewayIPv4: "10.0.1.1"}} {
		if _, _, e := instanceNICDescriptor(owner, s); e == nil {
			t.Fatalf("invalid network %+v accepted", s)
		}
	}
}
