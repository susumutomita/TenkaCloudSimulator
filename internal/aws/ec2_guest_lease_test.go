package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
)

type leaseBackendDouble struct {
	dispatch func(guestlease.Intent) (guestlease.Disposition, error)
	observe  func(guestlease.Intent) (guestlease.Observation, error)
}

func (b *leaseBackendDouble) Dispatch(_ context.Context, i guestlease.Intent) (guestlease.Disposition, error) {
	return b.dispatch(i)
}
func (b *leaseBackendDouble) Observe(_ context.Context, i guestlease.Intent) (guestlease.Observation, error) {
	return b.observe(i)
}
func leaseOwner() EC2GuestOwner {
	return EC2GuestOwner{Account: "000000000000", Region: "us-east-1", Instance: "i-test", Deployment: "stack:incarnation-1"}
}
func leaseFixture(t *testing.T) (*EC2GuestLeaseStore, *leaseBackendDouble, *sql.DB) {
	t.Helper()
	db, e := core.New(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := &leaseBackendDouble{dispatch: func(guestlease.Intent) (guestlease.Disposition, error) { return guestlease.Accepted, nil }}
	b.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		state := guestlease.Acquired
		if i.Action == guestlease.Contain {
			state = guestlease.Contained
		}
		if i.Action == guestlease.Release {
			state = guestlease.Released
		}
		return guestlease.Observation{Intent: i, Lease: "opaque-backend-lease", State: state}, nil
	}
	s, e := NewEC2GuestLeaseStore(db.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	return s, b, db.Database()
}
func TestEC2GuestLeaseDurableIntentAndRestart(t *testing.T) {
	s, b, db := leaseFixture(t)
	ctx := context.Background()
	owner := leaseOwner()
	calls := 0
	b.dispatch = func(i guestlease.Intent) (guestlease.Disposition, error) {
		calls++
		got, e := s.Get(ctx, owner)
		if e != nil || got.Intent != i {
			t.Fatalf("dispatch without committed intent: %+v %v", got, e)
		}
		return guestlease.Accepted, nil
	}
	r, e := s.Acquire(ctx, owner)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"held", "contained", "released"} {
		// Reconstruct the registry at every dispatch/observation boundary.
		s, e = NewEC2GuestLeaseStore(db, b)
		if e != nil {
			t.Fatal(e)
		}
		r, e = s.Get(ctx, owner)
		if e != nil {
			t.Fatal(e)
		}
		r, e = s.Reconcile(ctx, r)
		if e != nil || r.State != want {
			t.Fatalf("%s: %+v %v", want, r, e)
		}
		s, e = NewEC2GuestLeaseStore(db, b)
		if e != nil {
			t.Fatal(e)
		}
		if want != "released" {
			r, e = s.Advance(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	if _, e = s.Acquire(ctx, owner); e == nil {
		t.Fatal("reused retained tombstone")
	}
	if _, e = s.Advance(ctx, r); e == nil {
		t.Fatal("redispatched released lease")
	}
}
func TestEC2GuestLeaseUncertainAcquireNeverReplayed(t *testing.T) {
	for _, dispatchErr := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New("closed transport")} {
		t.Run(fmt.Sprint(dispatchErr), func(t *testing.T) {
			s, b, db := leaseFixture(t)
			calls := 0
			b.dispatch = func(guestlease.Intent) (guestlease.Disposition, error) {
				calls++
				return guestlease.Uncertain, dispatchErr
			}
			r, e := s.Acquire(context.Background(), leaseOwner())
			if e == nil {
				t.Fatal("uncertainty accepted")
			}
			s, e = NewEC2GuestLeaseStore(db, b)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Acquire(context.Background(), leaseOwner()); e == nil {
				t.Fatal("replayed")
			}
			if _, e = s.Advance(context.Background(), r); e == nil {
				t.Fatal("advanced pending")
			}
			if calls != 1 {
				t.Fatal(calls)
			}
			r, e = s.Reconcile(context.Background(), r)
			if e != nil || r.State != "held" {
				t.Fatal(r, e)
			}
		})
	}
}
func TestEC2GuestLeaseFencesAndNoUnconfirmedCleanup(t *testing.T) {
	s, b, _ := leaseFixture(t)
	ctx := context.Background()
	r, e := s.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*EC2GuestLease){
		func(r *EC2GuestLease) { r.Owner.Account = "111111111111" }, func(r *EC2GuestLease) { r.Owner.Region = "us-west-2" }, func(r *EC2GuestLease) { r.Owner.Instance = "i-other" }, func(r *EC2GuestLease) { r.Owner.Deployment = "stack:new" }, func(r *EC2GuestLease) { r.Intent.Operation++ }, func(r *EC2GuestLease) { r.Intent.Fence = "wrong" },
	} {
		wrong := r
		mutate(&wrong)
		if _, e = s.Reconcile(ctx, wrong); e == nil {
			t.Fatal("accepted wrong authority", wrong)
		}
	}
	oldObserve := b.observe
	b.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		o, _ := oldObserve(i)
		o.Intent.Operation++
		return o, nil
	}
	if _, e = s.Reconcile(ctx, r); e == nil {
		t.Fatal("accepted stale observation")
	}
	b.observe = oldObserve
	r, e = s.Reconcile(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	held := r
	r, e = s.Advance(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if r.State != "contain_pending" {
		t.Fatal("ack confirmed containment")
	}
	b.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		o, _ := oldObserve(i)
		o.State = guestlease.Released
		return o, nil
	}
	if _, e = s.Reconcile(ctx, r); e == nil {
		t.Fatal("release bypassed containment")
	}
	b.observe = oldObserve
	r, e = s.Reconcile(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.Advance(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if r.State != "release_pending" {
		t.Fatal("ack confirmed release")
	}
	b.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		o, _ := oldObserve(i)
		o.Lease = "other-guest"
		return o, nil
	}
	if _, e = s.Reconcile(ctx, r); e == nil {
		t.Fatal("cross-guest cleanup")
	}
	b.observe = oldObserve
	r, e = s.Reconcile(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, held); e == nil {
		t.Fatal("late old operation advanced current lease")
	}
}
func TestEC2GuestLeaseIgnoredAndFaultedWrites(t *testing.T) {
	for _, fault := range []string{"RAISE(IGNORE)", "RAISE(ABORT,'fault')"} {
		t.Run(fault, func(t *testing.T) {
			s, b, db := leaseFixture(t)
			calls := 0
			b.dispatch = func(guestlease.Intent) (guestlease.Disposition, error) { calls++; return guestlease.Accepted, nil }
			profileSQL(t, db, `CREATE TRIGGER lease_fault BEFORE INSERT ON aws_ec2_guest_leases BEGIN SELECT `+fault+`; END`)
			if _, e := s.Acquire(context.Background(), leaseOwner()); e == nil {
				t.Fatal("ignored insert succeeded")
			}
			if calls != 0 {
				t.Fatal("dispatched failed write")
			}
			profileSQL(t, db, `DROP TRIGGER lease_fault`)
			r, e := s.Acquire(context.Background(), leaseOwner())
			if e != nil {
				t.Fatal(e)
			}
			profileSQL(t, db, `CREATE TRIGGER lease_fault BEFORE UPDATE ON aws_ec2_guest_leases BEGIN SELECT `+fault+`; END`)
			if _, e = s.Reconcile(context.Background(), r); e == nil {
				t.Fatal("ignored final ack succeeded")
			}
			got, e := s.Get(context.Background(), leaseOwner())
			if e != nil || got != r {
				t.Fatal("lost pending obligation", got, e)
			}
			profileSQL(t, db, `DROP TRIGGER lease_fault`)
			r, e = s.Reconcile(context.Background(), r)
			if e != nil {
				t.Fatal(e)
			}
			profileSQL(t, db, `CREATE TRIGGER lease_fault BEFORE UPDATE ON aws_ec2_guest_leases BEGIN SELECT `+fault+`; END`)
			if _, e = s.Advance(context.Background(), r); e == nil {
				t.Fatal("ignored intent succeeded")
			}
			if calls != 1 {
				t.Fatal("dispatched ignored cleanup intent")
			}
		})
	}
}
func TestEC2GuestLeaseReadbackRejectsTriggerMutation(t *testing.T) {
	s, b, db := leaseFixture(t)
	calls := 0
	b.dispatch = func(guestlease.Intent) (guestlease.Disposition, error) { calls++; return guestlease.Accepted, nil }
	profileSQL(t, db, `CREATE TRIGGER lease_mutate AFTER INSERT ON aws_ec2_guest_leases BEGIN UPDATE aws_ec2_guest_leases SET record='{}'; END`)
	if _, e := s.Acquire(context.Background(), leaseOwner()); e == nil {
		t.Fatal("mutation acknowledged")
	}
	if calls != 0 {
		t.Fatal("dispatch")
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM aws_ec2_guest_leases`).Scan(&count); e != nil || count != 0 {
		t.Fatal("not rolled back", count, e)
	}
}
func TestEC2GuestLeaseProfileIncarnation(t *testing.T) {
	s, _, db := leaseFixture(t)
	iam, e := NewIAMRoleStore(db)
	if e != nil {
		t.Fatal(e)
	}
	p := profileCreate(t, iam, "Profile")
	o := leaseOwner()
	o.ProfileName = p.InstanceProfileName
	o.ProfileID = "AIPA0000000000000000"
	if _, e = s.Acquire(context.Background(), o); e == nil {
		t.Fatal("substituted profile")
	}
	o.ProfileID = p.InstanceProfileID
	o.ProfileName = "profile"
	if _, e = s.Acquire(context.Background(), o); e == nil {
		t.Fatal("noncanonical profile")
	}
	o.ProfileName = p.InstanceProfileName
	if _, e = s.Acquire(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	o.ProfileID = "AIPA0000000000000000"
	if _, e = s.Get(context.Background(), o); e == nil {
		t.Fatal("owner substitution")
	}
}
func TestEC2GuestLeaseBoundedConcurrentAdmission(t *testing.T) {
	s, b, db := leaseFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	calls := 0
	b.dispatch = func(guestlease.Intent) (guestlease.Disposition, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return guestlease.Rejected, nil
	}
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Acquire(ctx, leaseOwner()) }()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatal("duplicate admission", calls)
	}
	// Fill the quota with retained rows; no backend invocation may exceed it.
	for n := 1; n < guestLeaseLimit; n++ {
		o := leaseOwner()
		o.Instance = fmt.Sprintf("i-%d", n)
		if _, e := s.Acquire(ctx, o); e != nil {
			t.Fatal(e)
		}
	}
	o := leaseOwner()
	o.Instance = "i-overflow"
	if _, e := s.Acquire(ctx, o); e == nil {
		t.Fatal("unbounded")
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM aws_ec2_guest_leases`).Scan(&count); e != nil || count != guestLeaseLimit {
		t.Fatal(count, e)
	}
	if calls != guestLeaseLimit {
		t.Fatal(calls)
	}
}

func TestEC2GuestLeaseCrashReopenAndBackendAlias(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	b := &leaseBackendDouble{dispatch: func(guestlease.Intent) (guestlease.Disposition, error) {
		calls++
		return guestlease.Uncertain, errors.New("crash after backend accepted")
	}}
	b.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		return guestlease.Observation{Intent: i, Lease: "immutable-guest", State: guestlease.Acquired}, nil
	}
	s, e := NewEC2GuestLeaseStore(db.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Acquire(ctx, leaseOwner())
	if e == nil {
		t.Fatal("uncertain success")
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e = NewEC2GuestLeaseStore(db.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(ctx, leaseOwner())
	if e != nil || got != r {
		t.Fatal(got, e)
	}
	if _, e = s.Acquire(ctx, leaseOwner()); e == nil {
		t.Fatal("restart redispatch")
	}
	if _, e = s.Reconcile(ctx, got); e != nil {
		t.Fatal(e)
	}
	other := leaseOwner()
	other.Instance = "i-other"
	r, e = s.Acquire(ctx, other)
	if e == nil {
		t.Fatal("expected uncertainty")
	}
	if _, e = s.Reconcile(ctx, r); e == nil {
		t.Fatal("same backend guest bound to two owners")
	}
	got, e = s.Get(ctx, other)
	if e != nil || got.State != "acquire_pending" {
		t.Fatal(got, e)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestEC2GuestLeaseRejectedAcknowledgementFaultStaysPending(t *testing.T) {
	s, b, db := leaseFixture(t)
	ctx := context.Background()
	b.dispatch = func(guestlease.Intent) (guestlease.Disposition, error) { return guestlease.Rejected, nil }
	profileSQL(t, db, `CREATE TRIGGER lease_ack BEFORE UPDATE ON aws_ec2_guest_leases BEGIN SELECT RAISE(IGNORE); END`)
	if _, e := s.Acquire(ctx, leaseOwner()); e == nil {
		t.Fatal("ignored rejection acknowledged")
	}
	r, e := s.Get(ctx, leaseOwner())
	if e != nil || r.State != "acquire_pending" {
		t.Fatal(r, e)
	}
}

func TestEC2GuestLeaseRecoveryEnumerationFailsClosed(t *testing.T) {
	s, _, db := leaseFixture(t)
	ctx := context.Background()
	o := leaseOwner()
	r, e := s.Acquire(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	records, e := s.List(ctx, o.Account, o.Region, "", 10)
	if e != nil || len(records) != 1 || records[0] != r {
		t.Fatal(records, e)
	}
	records, e = s.List(ctx, o.Account, o.Region, o.Instance, 10)
	if e != nil || len(records) != 0 {
		t.Fatal(records, e)
	}
	if _, e = s.List(ctx, o.Account, o.Region, "", 101); e == nil {
		t.Fatal("unbounded page")
	}
	profileSQL(t, db, `UPDATE aws_ec2_guest_leases SET record='{}'`)
	if _, e = s.Get(ctx, o); e == nil {
		t.Fatal("invalid persisted state")
	}
	if _, e = s.List(ctx, o.Account, o.Region, "", 10); e == nil {
		t.Fatal("invalid recovery enumeration")
	}
}
