//go:build linux

package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

// EC2VMBackend is a single-instance canonical VM backend. Its immutable config
// is provider-selected, not guest input. Reopening the DB does not adopt a VM:
// any persisted reservation without this actor's exact handle stays Unknown.
// One instance accepts one fence forever; no pruning/retry/identity reuse exists.
type EC2VMBackend struct {
	networkLease *EC2NetworkLease
	sealOnce     sync.Once
	ops          vmNICOperations
	prepare      func(context.Context, guestlease.Intent) (*guestvm.Prepared, error)
	cancel       context.CancelFunc
	lifetime     context.Context
	db           *sql.DB
	owner        EC2GuestOwner
	bundle       *guestbundle.Bundle
	layout       guestvm.Layout
	directory    string
	userdata     []byte
	network      guestseed.IPv4Network
	descriptor   guesttcp.OwnedProbeDescriptor
	mu           sync.Mutex
	intent       guestlease.Intent
	prepared     *guestvm.Prepared
	process      vmNICProcess
	control      vmNICControl
	state        guestlease.State
	consumed     bool
	running      bool
}

// NewEC2VMBackend does not start a guest. Dispatch is the explicit real operation.
// Trusted deployment must own this actor exclusively; a DB path is not authority.
func NewEC2VMBackend(lifetime context.Context, db *sql.DB, owner EC2GuestOwner, bundle *guestbundle.Bundle, layout guestvm.Layout, directory string, userdata []byte, network guestseed.IPv4Network, descriptor guesttcp.OwnedProbeDescriptor) (*EC2VMBackend, error) {
	if lifetime == nil || db == nil || bundle == nil || !validGuestOwner(owner) {
		return nil, errGuestLease
	}
	deadline, ok := lifetime.Deadline()
	if !ok || !time.Now().Before(deadline) {
		return nil, errGuestLease
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_vm_backend_intents(account TEXT NOT NULL,region TEXT NOT NULL,instance TEXT NOT NULL,intent TEXT NOT NULL,prepared TEXT,PRIMARY KEY(account,region,instance))`)
	if err != nil {
		return nil, err
	}
	owned, cancel := context.WithDeadline(lifetime, deadline)
	b := &EC2VMBackend{lifetime: owned, cancel: cancel, db: db, owner: owner, bundle: bundle, layout: layout, directory: directory, userdata: append([]byte(nil), userdata...), network: network, descriptor: cloneVMDescriptor(descriptor), ops: canonicalVMNICOperations()}
	b.prepare = func(ctx context.Context, i guestlease.Intent) (*guestvm.Prepared, error) {
		return guestvm.PrepareWithNetwork(ctx, b.directory, b.bundle, b.layout, i, b.userdata, b.network)
	}
	return b, nil
}
func (b *EC2VMBackend) Dispatch(ctx context.Context, i guestlease.Intent) (guestlease.Disposition, error) {
	if ctx == nil || !guestLeaseToken.MatchString(i.Fence) {
		return guestlease.Rejected, errGuestLease
	}
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return guestlease.Uncertain, ErrEC2GuestAttachmentPending
	}
	// Identical requests never execute twice, including after uncertain effects.
	if b.intent == i {
		b.mu.Unlock()
		return guestlease.Uncertain, nil
	}
	acquire := i.Action == guestlease.Acquire && i.Operation == 1 && i.Lease == ""
	if !acquire && (b.prepared == nil || i.Fence != b.intent.Fence || i.Lease != b.prepared.Record().Lease || i.Operation != b.intent.Operation+1 || !((i.Action == guestlease.Contain && i.Operation == 2 && b.state == guestlease.Acquired) || (i.Action == guestlease.Release && i.Operation == 3 && b.state == guestlease.Contained))) {
		b.mu.Unlock()
		return guestlease.Rejected, errGuestLease
	}
	if acquire && b.intent.Fence != "" {
		b.mu.Unlock()
		return guestlease.Rejected, errGuestLease
	}
	raw, _ := json.Marshal(i)
	old, _ := json.Marshal(b.intent)
	var result sql.Result
	var err error
	if acquire {
		result, err = b.db.ExecContext(ctx, `INSERT INTO aws_ec2_vm_backend_intents(account,region,instance,intent) SELECT ?,?,?,? WHERE (SELECT count(*) FROM aws_ec2_vm_backend_intents)<?`, b.owner.Account, b.owner.Region, b.owner.Instance, string(raw), guestLeaseLimit)
	} else {
		result, err = b.db.ExecContext(ctx, `UPDATE aws_ec2_vm_backend_intents SET intent=? WHERE account=? AND region=? AND instance=? AND intent=?`, string(raw), b.owner.Account, b.owner.Region, b.owner.Instance, string(old))
	}
	if err != nil {
		b.mu.Unlock()
		return guestlease.Uncertain, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		b.mu.Unlock()
		return guestlease.Uncertain, errors.Join(err, errGuestLease)
	}
	b.intent = i
	b.running = true
	b.state = guestlease.Unknown
	process, control := b.process, b.control
	b.mu.Unlock()
	if acquire {
		prepared, e := b.prepare(ctx, i)
		if e != nil {
			return b.finish(i, guestlease.Unknown, e)
		}
		if !prepared.MatchesNIC(b.descriptor.Config.NIC, b.descriptor.ARP) {
			return b.finish(i, guestlease.Unknown, errGuestLease)
		}
		rawPrepared, encodeErr := json.Marshal(prepared.Record())
		if encodeErr != nil {
			return b.finish(i, guestlease.Unknown, encodeErr)
		}
		persisted, writeErr := b.db.ExecContext(ctx, `UPDATE aws_ec2_vm_backend_intents SET prepared=? WHERE account=? AND region=? AND instance=? AND intent=? AND prepared IS NULL`, string(rawPrepared), b.owner.Account, b.owner.Region, b.owner.Instance, string(raw))
		if writeErr != nil {
			return b.finish(i, guestlease.Unknown, writeErr)
		}
		count, writeErr := persisted.RowsAffected()
		if writeErr != nil || count != 1 {
			return b.finish(i, guestlease.Unknown, errors.Join(writeErr, errGuestLease))
		}
		var verified string
		if writeErr = b.db.QueryRowContext(ctx, `SELECT prepared FROM aws_ec2_vm_backend_intents WHERE account=? AND region=? AND instance=? AND intent=?`, b.owner.Account, b.owner.Region, b.owner.Instance, string(raw)).Scan(&verified); writeErr != nil || verified != string(rawPrepared) {
			return b.finish(i, guestlease.Unknown, errors.Join(writeErr, errGuestLease))
		}
		// Register preparation before launch and retain every returned process before
		// a readiness wait. There is never a restart fallback or second launch.
		b.mu.Lock()
		b.prepared = prepared
		b.mu.Unlock()
		process, e = b.ops.launch(b.lifetime, prepared, b.descriptor.Config.NIC.PeerMAC)
		b.mu.Lock()
		b.process = process
		b.mu.Unlock()
		if e != nil {
			if process != nil {
				_ = process.Close()
			}
			return b.finish(i, guestlease.Unknown, e)
		}
		if process == nil {
			return b.finish(i, guestlease.Unknown, errGuestLease)
		}
		if e = process.WaitReady(ctx); e != nil {
			_ = process.Close()
			return b.finish(i, guestlease.Unknown, e)
		}
		control, e = b.ops.control(ctx, prepared)
		b.mu.Lock()
		b.control = control
		b.mu.Unlock()
		if e != nil {
			if control != nil {
				_ = control.Close()
			}
			_ = process.Close()
			return b.finish(i, guestlease.Unknown, e)
		}
		if control == nil {
			_ = process.Close()
			return b.finish(i, guestlease.Unknown, errGuestLease)
		}
		status, e := control.QueryStatus(ctx)
		if e != nil || status.Running || (status.State != "prelaunch" && status.State != "paused") {
			_ = process.Close()
			_ = control.Close()
			return b.finish(i, guestlease.Unknown, errors.Join(e, qmp.ErrNotPaused))
		}
		return b.finish(i, guestlease.Acquired, nil)
	}
	if process == nil {
		return b.finish(i, guestlease.Unknown, errGuestLease)
	}
	err = process.Close()
	if control != nil {
		err = errors.Join(err, control.Close())
	}
	err = errors.Join(err, process.WaitCleanup(ctx))
	if err != nil {
		return b.finish(i, guestlease.Unknown, err)
	}
	state := guestlease.Contained
	if i.Action == guestlease.Release {
		state = guestlease.Released
	}
	return b.finish(i, state, nil)
}
func (b *EC2VMBackend) finish(i guestlease.Intent, state guestlease.State, err error) (guestlease.Disposition, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.intent != i {
		return guestlease.Uncertain, errGuestLease
	}
	b.running = false
	b.state = state
	if err != nil {
		return guestlease.Uncertain, err
	}
	return guestlease.Accepted, nil
}
func (b *EC2VMBackend) Observe(ctx context.Context, i guestlease.Intent) (guestlease.Observation, error) {
	if ctx == nil || ctx.Err() != nil {
		return guestlease.Observation{}, errGuestLease
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.intent != i || b.running || b.prepared == nil || b.process == nil || b.state == guestlease.Unknown {
		return guestlease.Observation{Intent: i, State: guestlease.Unknown}, nil
	}
	return guestlease.Observation{Intent: i, Lease: b.prepared.Record().Lease, State: b.state}, nil
}

// takeAcquired transfers the already launched, verified, paused VM once. The
// caller cannot inject a process or acquire capability by supplying matching IDs.
func (b *EC2VMBackend) takeAcquired(expected EC2GuestLease, p *guestvm.Prepared) (vmNICProcess, vmNICControl, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lifetime.Err() != nil || b.owner != expected.Owner || b.prepared != p || b.state != guestlease.Acquired || b.running || b.consumed || b.process == nil || b.control == nil || b.intent.Fence != expected.Intent.Fence || p.Record().Lease != expected.Intent.Lease {
		return nil, nil, errGuestLease
	}
	b.consumed = true
	return b.process, b.control, nil
}
func (b *EC2VMBackend) Prepared() *guestvm.Prepared {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prepared
}

// SealNIC closes admission and the same retained physical stream without taking
// a network callback gate. It is safe as the network actor's pre-join seal hook.
// It requests cleanup; only subsequent observed joins can establish completion.
func (b *EC2VMBackend) SealNIC() {
	if b == nil {
		return
	}
	b.cancel()
	b.sealOnce.Do(func() {
		go func() {
			b.mu.Lock()
			process, control := b.process, b.control
			b.mu.Unlock()
			if process != nil {
				_ = process.Close()
			}
			if control != nil {
				_ = control.Close()
			}
		}()
	})
}
