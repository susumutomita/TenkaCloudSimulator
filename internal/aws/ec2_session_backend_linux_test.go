//go:build linux

package aws

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func canonicalSessionFixture(t *testing.T) (*CanonicalSSMSessionBackend, SSMSessionTarget, *EC2VMProbeLaunch, context.Context) {
	t.Helper()
	b, s, c, _, _, _, ctx := vmBackendFixture(t)
	info, err := b.bundle.Info(b.layout.BaseDisk)
	if err != nil {
		t.Fatal(err)
	}
	b.prepare = func(ctx context.Context, i guestlease.Intent) (*guestvm.Prepared, error) {
		return guestvm.PrepareWithNetworkAndSession(ctx, b.directory, b.bundle, b.layout, i, b.userdata, b.network, info.SHA256)
	}
	lease, err := s.Acquire(ctx, leaseOwner())
	if err != nil {
		t.Fatal(err)
	}
	lease, err = s.Reconcile(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	passiveCtx, cancel := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	t.Cleanup(cancel)
	receipt, err := c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, &attachmentPassive{ctx: passiveCtx, cancel: cancel}, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipt.Seal(ctx); _ = receipt.WaitCleanup(ctx) })
	owner := lease.Owner
	plan := EC2InstancePlan{Region: owner.Region, StackID: owner.Deployment, InstanceID: owner.Instance, canonical: "fixture-exact-plan"}
	runtime := &EC2InstanceRuntime{db: b.db, config: EC2InstanceRuntimeConfig{SessionImageSHA256: info.SHA256}, retained: map[string]*ec2InstanceRuntimeEntry{owner.Instance: {plan: plan, backend: b, receipt: receipt}}}
	if _, err = b.db.Exec(`CREATE TABLE aws_ec2_instance_runtime(region TEXT,stack_id TEXT,instance TEXT,plan TEXT,state TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Exec(`INSERT INTO aws_ec2_instance_runtime VALUES(?,?,?,?,'ready')`, owner.Region, owner.Deployment, owner.Instance, plan.canonical); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewCanonicalSSMSessionBackend(runtime)
	if err != nil {
		t.Fatal(err)
	}
	target, err := adapter.ResolveSessionTarget(ctx, owner.Region, owner.Instance)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, target, receipt, ctx
}
func TestCanonicalSessionAdapterMemoryHelloAndInvalidation(t *testing.T) {
	b, target, receipt, ctx := canonicalSessionFixture(t)
	same, err := NewCanonicalSSMSessionBackend(b.runtime)
	if err != nil || same != b {
		t.Fatal("duplicate runtime session owner")
	}
	var calls atomic.Int32
	peerDone := make(chan struct{})
	b.dial = func(_ context.Context, network, path string) (net.Conn, error) {
		calls.Add(1)
		p := b.runtime.retained[target.InstanceID].backend.Prepared()
		if network != "unix" || path != p.SessionSocket() || path == p.QGASocket() {
			t.Fatal("wrong retained endpoint")
		}
		host, guest := net.Pipe()
		go func() {
			defer close(peerDone)
			defer guest.Close()
			_ = guestsession.NewCodec(guest).Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(p.Record().UUID)})
			var one [1]byte
			_, _ = guest.Read(one[:])
		}()
		return host, nil
	}
	driver, err := b.OpenSessionChannel(ctx, target)
	if err != nil || driver == nil {
		t.Fatal(err)
	}
	if _, err = b.OpenSessionChannel(ctx, target); err == nil || calls.Load() != 1 {
		t.Fatal("channel replay")
	}
	// Actual owner closure reaches resource signal, without receipt.Close.
	receipt.coordinator.mu.Lock()
	entry := receipt.coordinator.entries[receipt.resource.issuance.Lease.Owner]
	receipt.coordinator.mu.Unlock()
	entry.mu.Lock()
	owner := entry.owner
	entry.mu.Unlock()
	owner.Revoke()
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("owner did not close session channel")
	}
	if _, err = b.ResolveSessionTarget(ctx, target.Region, target.InstanceID); err == nil {
		t.Fatal("dead owner resolved")
	}
}
func TestCanonicalSessionAdapterMemoryRejectsDriftAndWrongHello(t *testing.T) {
	for _, mode := range []string{"database", "target", "hello", "dial-error"} {
		t.Run(mode, func(t *testing.T) {
			b, target, _, ctx := canonicalSessionFixture(t)
			var calls atomic.Int32
			b.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
				calls.Add(1)
				if mode == "dial-error" {
					return nil, errors.New("synthetic unavailable")
				}
				host, guest := net.Pipe()
				go func() {
					defer guest.Close()
					_ = guestsession.NewCodec(guest).Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte("11111111-1111-4111-8111-111111111111")})
				}()
				return host, nil
			}
			if mode == "database" {
				_, _ = b.runtime.db.Exec(`UPDATE aws_ec2_instance_runtime SET plan='drift'`)
			}
			if mode == "target" {
				target.StackID = "foreign"
			}
			if d, err := b.OpenSessionChannel(ctx, target); err == nil || d != nil {
				t.Fatal("invalid channel accepted")
			}
			first := calls.Load()
			if mode == "database" || mode == "target" {
				if first != 0 {
					t.Fatal("prevalidation dialed")
				}
			} else {
				if first != 1 {
					t.Fatal("missing bounded attempt")
				}
				_, _ = b.OpenSessionChannel(ctx, target)
				if calls.Load() != first {
					t.Fatal("ambiguous attempt retried")
				}
			}
		})
	}
}

type canonicalBlockedClose struct {
	net.Conn
	entered, release chan struct{}
	failure          error
}

func (c *canonicalBlockedClose) Close() error {
	close(c.entered)
	<-c.release
	_ = c.Conn.Close()
	return c.failure
}
func TestCanonicalSessionAdapterMemoryCleanupDebt(t *testing.T) {
	host, peer := net.Pipe()
	defer peer.Close()
	failure := errors.New("synthetic close failure")
	raw := &canonicalBlockedClose{Conn: host, entered: make(chan struct{}), release: make(chan struct{}), failure: failure}
	_, cancel := context.WithCancel(context.Background())
	conn := &canonicalSessionConn{Conn: raw, valid: func() bool { return true }, cancel: cancel, stopOwner: func() bool { return true }, done: make(chan struct{})}
	done := make(chan struct{})
	close(done)
	backend := &CanonicalSSMSessionBackend{attempts: map[*ec2InstanceRuntimeEntry]*canonicalSessionAttempt{{}: {done: done, conn: conn}}}
	go conn.Close()
	<-raw.entered
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if err := backend.WaitCleanup(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked close lost debt", err)
	}
	close(raw.release)
	<-conn.done
	if err := backend.WaitCleanup(context.Background()); !errors.Is(err, failure) {
		t.Fatal("failed close accepted", err)
	}
	backend.attempts = map[*ec2InstanceRuntimeEntry]*canonicalSessionAttempt{{}: {done: done, err: errors.New("unknown acquisition")}}
	if err := backend.WaitCleanup(context.Background()); err == nil {
		t.Fatal("unknown constructor released")
	}
}

func TestCanonicalSessionAdapterMemoryPositiveExitFreshGeneration(t *testing.T) {
	b, target, _, ctx := canonicalSessionFixture(t)
	var calls atomic.Int32
	b.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		host, guest := net.Pipe()
		uuid := b.runtime.retained[target.InstanceID].backend.Prepared().Record().UUID
		go func() {
			defer guest.Close()
			codec := guestsession.NewCodec(guest)
			if codec.Write(guestsession.Frame{Kind: guestsession.Hello, Payload: []byte(uuid)}) != nil {
				return
			}
			start, err := codec.Read()
			if err != nil || start.Kind != guestsession.Start {
				return
			}
			if codec.Write(guestsession.Frame{Kind: guestsession.Ready, Session: start.Session}) != nil {
				return
			}
			closeFrame, err := codec.Read()
			if err != nil || closeFrame.Kind != guestsession.Close {
				return
			}
			_ = codec.Write(guestsession.Frame{Kind: guestsession.Exit, Session: start.Session, Payload: make([]byte, 4)})
		}()
		return host, nil
	}
	for i := 1; i <= 8; i++ {
		d, err := b.OpenSessionChannel(ctx, target)
		if err != nil {
			t.Fatalf("generation %d: %v", i, err)
		}
		if err = d.Open(ctx, guestsession.ID{byte(i)}, 24, 80); err != nil {
			t.Fatal(err)
		}
		if err = d.Terminate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = d.WaitCleanup(ctx); err != nil || !d.ExitAcknowledged() {
			t.Fatal("missing positive release", err)
		}
	}
	if _, err := b.OpenSessionChannel(ctx, target); err == nil || calls.Load() != 8 {
		t.Fatal("generation cap bypassed")
	}
	if err := b.WaitCleanup(ctx); err != nil {
		t.Fatal("joined generation history", err)
	}
}

func TestCanonicalSessionAdapterMemoryShutdownFencesLateOpen(t *testing.T) {
	b, target, receipt, ctx := canonicalSessionFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	b.dial = func(context.Context, string, string) (net.Conn, error) {
		close(entered)
		<-release
		return nil, errors.New("synthetic delayed acquisition")
	}
	opened := make(chan error, 1)
	go func() { _, err := b.OpenSessionChannel(ctx, target); opened <- err }()
	<-entered
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := b.WaitCleanup(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pending Open escaped snapshot", err)
	}
	// Admission was sealed before snapshot, even while the earlier dial remains.
	if _, err := b.OpenSessionChannel(ctx, target); err == nil {
		t.Fatal("late Open accepted")
	}
	close(release)
	if err := <-opened; err == nil {
		t.Fatal("delayed acquisition accepted")
	}
	if err := b.WaitCleanup(ctx); err == nil {
		t.Fatal("unknown acquisition released")
	}
	_ = receipt.Close()
}
