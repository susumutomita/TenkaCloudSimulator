//go:build linux

package aws

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type canonicalCommandPeer struct {
	status  map[string]any
	starts  atomic.Int32
	entered chan struct{}
	block   bool
	release <-chan struct{}
}

func (p *canonicalCommandPeer) client(t *testing.T, ctx context.Context) *qga.Client {
	t.Helper()
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer guest.Close()
		reader := bufio.NewReader(guest)
		marker, err := reader.ReadByte()
		if err != nil || marker != 0xff {
			return
		}
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var r struct {
				Execute   string          `json:"execute"`
				Arguments json.RawMessage `json:"arguments"`
				ID        int64           `json:"id"`
			}
			if json.Unmarshal(line, &r) != nil {
				return
			}
			var result any
			switch r.Execute {
			case "guest-sync-delimited":
				var a struct {
					ID int64 `json:"id"`
				}
				if json.Unmarshal(r.Arguments, &a) != nil {
					return
				}
				if _, err = guest.Write([]byte{0xff}); err != nil {
					return
				}
				result = a.ID
			case "guest-exec":
				p.starts.Add(1)
				if p.entered != nil {
					close(p.entered)
					p.entered = nil
				}
				if p.release != nil {
					<-p.release
				}
				if p.block {
					_, _ = io.Copy(io.Discard, reader)
					return
				}
				result = map[string]int{"pid": int(p.starts.Load())}
			case "guest-exec-status":
				result = map[string]any{"exited": true, "exitcode": 0, "out-data": "b2sK"}
				if p.status != nil {
					result = p.status
				}
			default:
				return
			}
			reply, _ := json.Marshal(map[string]any{"id": r.ID, "return": result})
			if _, err = guest.Write(append(reply, '\n')); err != nil {
				return
			}
		}
	}()
	client, err := qga.New(ctx, host, qga.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = guest.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("memory QGA peer pending")
		}
	})
	return client
}
func canonicalCommandFixture(t *testing.T, peer *canonicalCommandPeer) (*CanonicalSSMCommandBackend, *SSMCommandStore, SSMCommandAuthority, EC2GuestLease, *EC2VMProbeLaunch, context.Context) {
	t.Helper()
	b, s, c, _, _, _, ctx := vmBackendFixture(t)
	b.ops.agent = func(ctx context.Context, _ *guestvm.Prepared) (io.Closer, error) { return peer.client(t, ctx), nil }
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
	plan := EC2InstancePlan{Region: owner.Region, StackID: owner.Deployment, InstanceID: owner.Instance, canonical: "exact-command-fixture"}
	runtime := &EC2InstanceRuntime{db: b.db, retained: map[string]*ec2InstanceRuntimeEntry{owner.Instance: {plan: plan, backend: b, receipt: receipt}}}
	if _, err = b.db.Exec(`CREATE TABLE aws_ec2_instance_runtime(region TEXT,stack_id TEXT,instance TEXT,plan TEXT,state TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Exec(`INSERT INTO aws_ec2_instance_runtime VALUES(?,?,?,?,'ready')`, owner.Region, owner.Deployment, owner.Instance, plan.canonical); err != nil {
		t.Fatal(err)
	}
	store, backend, err := NewCanonicalSSMCommandStore(runtime)
	if err != nil {
		t.Fatal(err)
	}
	authority := SSMCommandAuthority{Account: owner.Account, Region: owner.Region, Principal: "operator-generation", Deployment: owner.Deployment}
	return backend, store, authority, lease, receipt, ctx
}
func waitCanonicalCommand(t *testing.T, b *CanonicalSSMCommandBackend, i guestcommand.DurableIntent, ctx context.Context) {
	t.Helper()
	b.mu.Lock()
	job := b.jobs[i.Token]
	b.mu.Unlock()
	if job == nil {
		t.Fatal("missing retained worker")
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		t.Fatal("worker did not finish", ctx.Err())
	}
}
func TestCanonicalCommandMemorySequentialAndDuplicate(t *testing.T) {
	peer := &canonicalCommandPeer{}
	b, s, a, lease, _, ctx := canonicalCommandFixture(t, peer)
	for round := 0; round < 2; round++ {
		target, err := b.ResolveCommandTarget(ctx, lease.Owner.Region, lease.Owner.Instance)
		if err != nil || target != lease {
			t.Fatal("wrong retained target", err)
		}
		command, err := s.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
		if err != nil {
			t.Fatal(err)
		}
		intent := command.Targets[0].Intent
		waitCanonicalCommand(t, b, intent, ctx)
		result, err := b.Observe(ctx, intent)
		if err != nil || result.State != "Success" || result.Stdout != "ok\n" {
			t.Fatal(result, err)
		}
		if _, err = s.Reconcile(ctx, a, command.CommandId, lease.Owner.Instance); err != nil {
			t.Fatal(err)
		}
		if err = b.Dispatch(ctx, intent); err != nil {
			t.Fatal(err)
		}
		conflict := intent
		conflict.ScriptJSON = `["different"]`
		if err = b.Dispatch(ctx, conflict); err == nil {
			t.Fatal("conflicting token")
		}
		if peer.starts.Load() != int32(round+1) {
			t.Fatal("duplicate execution")
		}
	}
}
func TestCanonicalCommandMemoryRestartReservationAndCommitDrift(t *testing.T) {
	for _, mode := range []string{"restart", "drift"} {
		t.Run(mode, func(t *testing.T) {
			peer := &canonicalCommandPeer{}
			b, s, a, lease, _, ctx := canonicalCommandFixture(t, peer)
			// Authoritative store record committed without dispatch, simulating lost
			// continuation. The concrete backend must never adopt/replay its reservation.
			dormant := &SSMCommandStore{db: s.db, backend: &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }}}
			command, err := dormant.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
			if err != nil {
				t.Fatal(err)
			}
			i := command.Targets[0].Intent
			if mode == "restart" {
				if _, err = s.db.Exec(`INSERT INTO aws_canonical_commands VALUES(?,?,?)`, i.Token, ssmEncode(i), ssmEncode(unknownCommand(i, 0))); err != nil {
					t.Fatal(err)
				}
				restarted := &CanonicalSSMCommandBackend{runtime: b.runtime, jobs: map[string]*canonicalCommandJob{}}
				if err = restarted.Dispatch(ctx, i); err != nil {
					t.Fatal(err)
				}
				o, err := restarted.Observe(ctx, i)
				if err != nil || o.State != "Unknown" || !o.CleanupRequired {
					t.Fatal(o, err)
				}
			} else {
				if _, err = s.db.Exec(`CREATE TRIGGER corrupt_command AFTER INSERT ON aws_canonical_commands BEGIN UPDATE aws_canonical_commands SET intent='{}' WHERE token=NEW.token; END`); err != nil {
					t.Fatal(err)
				}
				if err = b.Dispatch(ctx, i); err == nil {
					t.Fatal("drifted commit dispatched")
				}
			}
			if peer.starts.Load() != 0 {
				t.Fatal("effect before valid durable reservation")
			}
		})
	}
}
func TestCanonicalCommandMemorySealDuringSubmission(t *testing.T) {
	entered := make(chan struct{})
	peer := &canonicalCommandPeer{entered: entered, block: true}
	b, s, a, lease, receipt, ctx := canonicalCommandFixture(t, peer)
	command, err := s.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("no actual memory guest-exec")
	}
	closed := make(chan error, 1)
	go func() { closed <- receipt.resource.Close() }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("close did not interrupt and join submit")
	}
	i := command.Targets[0].Intent
	waitCanonicalCommand(t, b, i, ctx)
	o, err := b.Observe(ctx, i)
	if err != nil || o.State != "Unknown" || !o.CleanupRequired {
		t.Fatal("in-flight effect falsely settled", o, err)
	}
	if _, err = receipt.commandOwner(); err == nil {
		t.Fatal("late driver registration after seal")
	}
	if peer.starts.Load() != 1 {
		t.Fatal("automatic retry")
	}
}
func TestCanonicalCommandMemoryShutdownAndRejectedTarget(t *testing.T) {
	peer := &canonicalCommandPeer{}
	b, _, _, lease, receipt, ctx := canonicalCommandFixture(t, peer)
	if _, err := b.ResolveCommandTarget(ctx, "foreign", lease.Owner.Instance); err == nil {
		t.Fatal("foreign target")
	}
	if err := b.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Dispatch(ctx, guestcommand.DurableIntent{}); !errors.Is(err, ErrSSMCommandUnknown) {
		t.Fatal("shutdown admission", err)
	}
	_ = receipt.resource.Close()
	if _, err := receipt.commandOwner(); err == nil {
		t.Fatal("late registration")
	}
	if peer.starts.Load() != 0 {
		t.Fatal("unexpected effect")
	}
}

func TestCanonicalCommandMemoryTerminalDriftAndLeaseBinding(t *testing.T) {
	peer := &canonicalCommandPeer{}
	b, s, a, lease, _, ctx := canonicalCommandFixture(t, peer)
	dormant := &SSMCommandStore{db: s.db, backend: &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }}}
	command, err := dormant.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
	if err != nil {
		t.Fatal(err)
	}
	i := command.Targets[0].Intent
	forged := i
	forged.LeaseFence = "foreign-fence"
	if err = b.Dispatch(ctx, forged); err == nil || peer.starts.Load() != 0 {
		t.Fatal("foreign lease fence accepted")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER corrupt_terminal AFTER UPDATE OF observation ON aws_canonical_commands WHEN NEW.observation LIKE '%"State":"Success"%' BEGIN UPDATE aws_canonical_commands SET observation='{}' WHERE token=NEW.token; END`); err != nil {
		t.Fatal(err)
	}
	if err = b.Dispatch(ctx, i); err != nil {
		t.Fatal(err)
	}
	waitCanonicalCommand(t, b, i, ctx)
	o, err := b.Observe(ctx, i)
	if o.State != "Unknown" || !o.CleanupRequired {
		t.Fatal("terminal drift became success", o, err)
	}
	if peer.starts.Load() != 1 {
		t.Fatal("redispatched after failed terminal commit")
	}
	// A trigger rollback may preserve InProgress; a failed worker still reports
	// Unknown rather than treating its historical row as live execution evidence.
	b.mu.Lock()
	job := b.jobs[i.Token]
	b.mu.Unlock()
	if job.err == nil {
		t.Fatal("terminal drift unobserved")
	}
}

func TestCanonicalCommandMemoryPendingObservationCanProgress(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	peer := &canonicalCommandPeer{entered: entered, release: release}
	b, s, a, lease, _, ctx := canonicalCommandFixture(t, peer)
	command, err := s.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("no pending submission")
	}
	i := command.Targets[0].Intent
	o, err := b.Observe(ctx, i)
	if err != nil || o.State != "Unknown" || o.CleanupRequired {
		t.Fatal("pending response falsely orphaned", o, err)
	}
	if _, err = s.Reconcile(ctx, a, command.CommandId, lease.Owner.Instance); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitCanonicalCommand(t, b, i, ctx)
	complete, err := s.Reconcile(ctx, a, command.CommandId, lease.Owner.Instance)
	if err != nil || complete.Targets[0].Observation.State != "Success" {
		t.Fatal("pending record could not advance", complete, err)
	}
}

func waitCanonicalReservation(t *testing.T, b *CanonicalSSMCommandBackend, i guestcommand.DurableIntent, ctx context.Context) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var raw string
		if b.runtime.db.QueryRowContext(ctx, `SELECT intent FROM aws_canonical_commands WHERE token=?`, i.Token).Scan(&raw) == nil && raw == ssmEncode(i) {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("durable reservation not reached")
		}
	}
}
func TestCanonicalCommandMemoryReservationPublicationGap(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate-and-observe", true: "shutdown"}[shutdown], func(t *testing.T) {
			peer := &canonicalCommandPeer{}
			b, s, a, lease, _, ctx := canonicalCommandFixture(t, peer)
			dormant := &SSMCommandStore{db: s.db, backend: &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }}}
			command, err := dormant.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
			if err != nil {
				t.Fatal(err)
			}
			i := command.Targets[0].Intent
			// target() acquires this actor lock only AFTER committing the reservation.
			// Observe and duplicate reservation checks do not require it.
			b.runtime.mu.Lock()
			locked := true
			defer func() {
				if locked {
					b.runtime.mu.Unlock()
				}
			}()
			dispatched := make(chan error, 1)
			go func() { dispatched <- b.Dispatch(ctx, i) }()
			waitCanonicalReservation(t, b, i, ctx)
			o, err := b.Observe(ctx, i)
			if err != nil || o.State != "Unknown" || o.CleanupRequired {
				t.Fatal("live publication gap misclassified", o, err)
			}
			if !shutdown {
				if err = b.Dispatch(ctx, i); err != nil {
					t.Fatal("overlapping exact duplicate", err)
				}
				o, err = b.Observe(ctx, i)
				if err != nil || o.CleanupRequired {
					t.Fatal("duplicate erased live pending marker", o, err)
				}
			} else {
				stopCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
				if err = b.Shutdown(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("shutdown escaped accepted dispatch", err)
				}
			}
			b.runtime.mu.Unlock()
			locked = false
			err = <-dispatched
			if shutdown {
				if err == nil || peer.starts.Load() != 0 {
					t.Fatal("late worker after shutdown", err)
				}
				if err = b.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
				o, err = b.Observe(ctx, i)
				if err != nil || !o.CleanupRequired {
					t.Fatal("lost continuation not quarantined", o, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				waitCanonicalCommand(t, b, i, ctx)
				if peer.starts.Load() != 1 {
					t.Fatal("duplicate effect")
				}
			}
		})
	}
}

// These fixtures use only net.Pipe QGA peers and SQLite. No VM, socket or shell.
func TestCanonicalCommandOutputBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    map[string]any
		state     string
		exit      int
		truncated bool
	}{
		{"nonzero", map[string]any{"exited": true, "exitcode": 7}, "Failed", 7, false},
		{"signal", map[string]any{"exited": true, "signal": 15}, "Failed", 143, false},
		{"invalid-utf8", map[string]any{"exited": true, "exitcode": 0, "out-data": base64.StdEncoding.EncodeToString([]byte{255})}, "Unknown", 0, false},
		{"truncated-unicode", map[string]any{"exited": true, "exitcode": 0, "out-data": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("雪", ssmCommandOutput))), "err-data": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", ssmCommandOutput+1)))}, "Failed", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &canonicalCommandPeer{status: tc.status}
			b, s, a, lease, _, ctx := canonicalCommandFixture(t, peer)
			command, err := s.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
			if err != nil {
				t.Fatal(err)
			}
			i := command.Targets[0].Intent
			waitCanonicalCommand(t, b, i, ctx)
			o, err := b.Observe(ctx, i)
			if err != nil || o.State != tc.state || o.ExitCode != tc.exit || o.OutputTruncated != tc.truncated {
				t.Fatalf("observation=%+v err=%v", o, err)
			}
			if tc.truncated && (len(o.Stdout) > ssmCommandOutput || len(o.Stderr) != ssmCommandOutput || o.Stdout != strings.Repeat("雪", ssmCommandOutput/3) || o.Stderr != strings.Repeat("x", ssmCommandOutput)) {
				t.Fatal("unsafe UTF8 truncation", len(o.Stdout), len(o.Stderr))
			}
			if tc.state == "Unknown" && !o.CleanupRequired {
				t.Fatal("invalid output implied cleanup")
			}
			if peer.starts.Load() != 1 {
				t.Fatal("unexpected replay")
			}
		})
	}
}
func TestCanonicalCommandPersistRejectsEvidenceMutation(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt-old", "noncanonical-old", "changed-start", "terminal-conflict", "terminal-repeat", "update-refused"} {
		t.Run(mode, func(t *testing.T) {
			b, s, a, lease, _, ctx := canonicalCommandFixture(t, &canonicalCommandPeer{})
			dormant := &SSMCommandStore{db: s.db, backend: &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }}}
			command, err := dormant.Send(ctx, a, ssmCommandRequestFixture([]EC2GuestLease{lease}), []EC2GuestLease{lease})
			if err != nil {
				t.Fatal(err)
			}
			i := command.Targets[0].Intent
			old := guestcommand.DurableObservation{Intent: i, State: "InProgress", StartedAt: i.RequestedAt}
			next := guestcommand.DurableObservation{Intent: i, State: "Success", StartedAt: i.RequestedAt, FinishedAt: i.RequestedAt + 1}
			raw := ssmEncode(old)
			switch mode {
			case "corrupt-old":
				raw = "{}"
			case "noncanonical-old":
				raw = " " + raw
			case "changed-start":
				next.StartedAt++
				next.FinishedAt++
			case "terminal-conflict", "terminal-repeat":
				old = next
				raw = ssmEncode(old)
				if mode == "terminal-conflict" {
					next.Stdout = "different"
				}
			}
			if mode != "missing" {
				if _, err = s.db.Exec(`INSERT INTO aws_canonical_commands VALUES(?,?,?)`, i.Token, ssmEncode(i), raw); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "update-refused" {
				if _, err = s.db.Exec(`CREATE TRIGGER refuse_observation BEFORE UPDATE ON aws_canonical_commands BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			}
			err = b.persist(ctx, next)
			if mode == "terminal-repeat" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("mutated evidence accepted")
			}
			if mode != "missing" {
				var after string
				if err = s.db.QueryRow(`SELECT observation FROM aws_canonical_commands WHERE token=?`, i.Token).Scan(&after); err != nil || after != raw {
					t.Fatal("failure changed retained observation", err)
				}
			}
		})
	}
}

func TestCanonicalCommandShutdownRetainsErrorsAndWaits(t *testing.T) {
	if err := (&CanonicalSSMCommandBackend{}).Shutdown(nil); !errors.Is(err, ErrSSMCommandInvalid) {
		t.Fatal(err)
	}
	for _, mode := range []string{"active-call", "owner-pending", "worker-pending", "joined-errors"} {
		t.Run(mode, func(t *testing.T) {
			ownerErr, workerErr := errors.New("retained owner failure"), errors.New("retained worker failure")
			owner := &canonicalCommandOwner{closeDone: make(chan struct{}), closeErr: ownerErr}
			// Represent an already requested seal without invoking a fake physical driver.
			owner.closeOnce.Do(func() {})
			job := &canonicalCommandJob{owner: owner, done: make(chan struct{}), err: workerErr}
			b := &CanonicalSSMCommandBackend{jobs: map[string]*canonicalCommandJob{"retained": job}}
			if mode == "active-call" {
				b.calls = 1
			}
			if mode != "owner-pending" {
				close(owner.closeDone)
			}
			if mode != "worker-pending" {
				close(job.done)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := b.Shutdown(ctx)
			if !b.closed {
				t.Fatal("shutdown left admission open")
			}
			if mode == "joined-errors" {
				if !errors.Is(err, ownerErr) || !errors.Is(err, workerErr) {
					t.Fatal("lost cleanup errors", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("pending work reported settled", err)
			}
			if mode == "worker-pending" && !errors.Is(err, ownerErr) {
				t.Fatal("lost observed owner failure", err)
			}
		})
	}
}
