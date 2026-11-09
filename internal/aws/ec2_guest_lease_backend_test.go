package aws

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
)

// queuedLeaseBackend is a deterministic state-machine model, NOT durable backend
// implementation or containment evidence. Observations read independently applied
// work; they cannot infer completion by echoing the caller's requested action.
type queuedLeaseBackend struct {
	mu              sync.Mutex
	requests        map[uint64]guestlease.Intent
	dispatches      map[uint64]int
	fence, lease    string
	completed       uint64
	state           guestlease.State
	running         bool
	block           uint64
	entered, resume chan struct{}
}

func newQueuedLeaseBackend() *queuedLeaseBackend {
	return &queuedLeaseBackend{requests: map[uint64]guestlease.Intent{}, dispatches: map[uint64]int{}, entered: make(chan struct{}), resume: make(chan struct{})}
}
func (b *queuedLeaseBackend) Dispatch(ctx context.Context, i guestlease.Intent) (guestlease.Disposition, error) {
	b.mu.Lock()
	if (b.fence != "" && b.fence != i.Fence) || i.Operation < 1 || i.Operation > 3 {
		b.mu.Unlock()
		return guestlease.Rejected, nil
	}
	if old, ok := b.requests[i.Operation]; ok && old != i {
		b.mu.Unlock()
		return guestlease.Rejected, nil
	}
	if i.Operation > 1 && (i.Lease != b.lease || b.completed < i.Operation-1) {
		b.mu.Unlock()
		return guestlease.Rejected, nil
	}
	b.fence = i.Fence
	b.requests[i.Operation] = i
	b.dispatches[i.Operation]++
	blocked := b.block == i.Operation
	b.mu.Unlock()
	if blocked {
		close(b.entered)
		select {
		case <-b.resume:
		case <-ctx.Done():
			return guestlease.Uncertain, ctx.Err()
		}
	}
	if e := b.complete(i.Operation); e != nil {
		return guestlease.Uncertain, e
	}
	return guestlease.Accepted, nil
}
func (b *queuedLeaseBackend) complete(op uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.requests[op]
	if !ok {
		return errors.New("no dispatched intent")
	}
	if op <= b.completed {
		return nil
	} // Includes stale in-flight continuations.
	if op != b.completed+1 {
		return errors.New("out of order work")
	}
	switch i.Action {
	case guestlease.Acquire:
		if op != 1 || i.Lease != "" {
			return errors.New("invalid acquisition")
		}
		b.lease = "lease-" + i.Fence
		b.running = true
		b.state = guestlease.Acquired
	case guestlease.Contain:
		if op != 2 || i.Lease != b.lease {
			return errors.New("invalid containment")
		}
		b.running = false
		b.state = guestlease.Contained
	case guestlease.Release:
		if op != 3 || i.Lease != b.lease || b.running {
			return errors.New("invalid release")
		}
		b.state = guestlease.Released
	default:
		return errors.New("invalid action")
	}
	b.completed = op
	return nil
}
func (b *queuedLeaseBackend) Observe(_ context.Context, i guestlease.Intent) (guestlease.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stored, ok := b.requests[i.Operation]
	if !ok || stored != i || b.fence != i.Fence {
		return guestlease.Observation{}, errors.New("unrecognized request")
	}
	state := guestlease.Unknown
	if b.completed == i.Operation {
		state = b.state
	}
	return guestlease.Observation{Intent: stored, Lease: b.lease, State: state}, nil
}
func (b *queuedLeaseBackend) counts() (uint64, bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, v := range b.dispatches {
		n += v
	}
	return b.completed, b.running, n
}

func TestEC2GuestLeaseDelayedAcquireCannotReviveReleasedGuest(t *testing.T) {
	db, e := core.New(filepath.Join(t.TempDir(), "queued.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	b := newQueuedLeaseBackend()
	b.block = 1
	s, e := NewEC2GuestLeaseStore(db.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	// Bound the concurrent lifecycle, not SQLite/WASM cold initialization.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := s.Acquire(ctx, leaseOwner()); done <- e }()
	select {
	case <-b.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r, e := s.Get(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reconcile(ctx, r); e == nil {
		t.Fatal("queued request alone became acquisition evidence")
	}
	// The backend worker completes acquisition while its Dispatch response is
	// still blocked. Later operations can safely fence that old continuation.
	if e = b.complete(1); e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"held", "contained", "released"} {
		r, e = s.Reconcile(ctx, r)
		if e != nil || r.State != want {
			t.Fatal(r, e)
		}
		if want != "released" {
			r, e = s.Advance(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	close(b.resume)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	completed, running, calls := b.counts()
	if completed != 3 || running || calls != 3 {
		t.Fatal(completed, running, calls)
	}
	got, e := s.Get(ctx, leaseOwner())
	if e != nil || got != r {
		t.Fatal("old dispatch changed durable record", got, e)
	}
	b.mu.Lock()
	old := b.requests[1]
	b.block = 0
	b.mu.Unlock()
	conflict := old
	conflict.Action = guestlease.Release
	if d, e := b.Dispatch(ctx, conflict); e != nil || d != guestlease.Rejected {
		t.Fatal("conflicting request accepted", d, e)
	}
	if d, e := b.Dispatch(ctx, old); e != nil || d != guestlease.Accepted {
		t.Fatal(d, e)
	}
	completed, running, _ = b.counts()
	if completed != 3 || running {
		t.Fatal("identical late request revived guest")
	}
}

func TestEC2GuestLeaseConcurrentSnapshotsAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	b := newQueuedLeaseBackend()
	a, e := NewEC2GuestLeaseStore(first.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	z, e := NewEC2GuestLeaseStore(second.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	r, e := a.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	compete := func(snapshot EC2GuestLease, reconcile bool) EC2GuestLease {
		t.Helper()
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, s := range []*EC2GuestLeaseStore{a, z} {
			go func(s *EC2GuestLeaseStore) {
				<-start
				var e error
				if reconcile {
					_, e = s.Reconcile(ctx, snapshot)
				} else {
					_, e = s.Advance(ctx, snapshot)
				}
				results <- e
			}(s)
		}
		close(start)
		wins := 0
		for n := 0; n < 2; n++ {
			if <-results == nil {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("wanted one committed transition, got %d", wins)
		}
		got, e := a.Get(ctx, leaseOwner())
		if e != nil {
			t.Fatal(e)
		}
		return got
	}
	r = compete(r, true)
	r = compete(r, false)
	r = compete(r, true)
	r = compete(r, false)
	r = compete(r, true)
	if r.State != "released" {
		t.Fatal(r)
	}
	_, running, calls := b.counts()
	if running || calls != 3 {
		t.Fatal(running, calls)
	}
}

func TestEC2GuestLeaseEveryAcknowledgementFaultRetainsObligation(t *testing.T) {
	for _, phase := range []string{"held", "contained", "released"} {
		for _, fault := range []string{"RAISE(IGNORE)", "RAISE(ABORT,'ack fault')"} {
			t.Run(phase+fault, func(t *testing.T) {
				ctx := context.Background()
				db, e := core.New(filepath.Join(t.TempDir(), "ack.db"))
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				b := newQueuedLeaseBackend()
				s, e := NewEC2GuestLeaseStore(db.Database(), b)
				if e != nil {
					t.Fatal(e)
				}
				pending, e := s.Acquire(ctx, leaseOwner())
				if e != nil {
					t.Fatal(e)
				}
				for _, want := range []string{"held", "contained", "released"} {
					if want == phase {
						break
					}
					r, e := s.Reconcile(ctx, pending)
					if e != nil {
						t.Fatal(e)
					}
					pending, e = s.Advance(ctx, r)
					if e != nil {
						t.Fatal(e)
					}
				}
				_, _, before := b.counts()
				profileSQL(t, db.Database(), `CREATE TRIGGER ack_fault BEFORE UPDATE ON aws_ec2_guest_leases BEGIN SELECT `+fault+`; END`)
				if _, e = s.Reconcile(ctx, pending); e == nil {
					t.Fatal("faulted acknowledgement accepted")
				}
				got, e := s.Get(ctx, leaseOwner())
				if e != nil || got != pending {
					t.Fatal("lost pending obligation", got, e)
				}
				profileSQL(t, db.Database(), `DROP TRIGGER ack_fault`)
				got, e = s.Reconcile(ctx, pending)
				if e != nil || got.State != phase {
					t.Fatal(got, e)
				}
				_, _, after := b.counts()
				if after != before {
					t.Fatal("reconciliation redispatched work")
				}
			})
		}
	}
}

func TestEC2GuestLeaseProfileRecreationCannotReplaceCleanupAuthority(t *testing.T) {
	ctx := context.Background()
	db, e := core.New(filepath.Join(t.TempDir(), "profile.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	iam, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	p := profileCreate(t, iam, "Profile")
	role := profileRole(t, iam, "Role")
	if e = iam.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
		t.Fatal(e)
	}
	b := newQueuedLeaseBackend()
	s, e := NewEC2GuestLeaseStore(db.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	o := leaseOwner()
	o.ProfileName = p.InstanceProfileName
	o.ProfileID = p.InstanceProfileID
	r, e := s.Acquire(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	if e = iam.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, role.RoleName, role.RoleID, false); e != nil {
		t.Fatal(e)
	}
	if e = iam.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e != nil {
		t.Fatal(e)
	}
	replacement := profileCreate(t, iam, "Profile")
	if replacement.InstanceProfileID == p.InstanceProfileID {
		t.Fatal("reused incarnation")
	}
	if e = iam.ChangeInstanceProfileRole(ctx, replacement.InstanceProfileName, replacement.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
		t.Fatal(e)
	}
	substituted := r
	substituted.Owner.ProfileID = replacement.InstanceProfileID
	if _, e = s.Reconcile(ctx, substituted); e == nil {
		t.Fatal("replacement incarnation adopted obligation")
	}
	for _, want := range []string{"held", "contained", "released"} {
		r, e = s.Reconcile(ctx, r)
		if e != nil || r.State != want {
			t.Fatal("old incarnation cannot clean up", r, e)
		}
		if want != "released" {
			r, e = s.Advance(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
}
