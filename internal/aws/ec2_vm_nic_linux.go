//go:build linux

package aws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestprocess"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

// EC2VMProbeLaunch is a retained startup/cleanup receipt, never a restart token.
// A non-nil receipt on error must be retained: launch or close can be uncertain.
type EC2VMProbeLaunch struct {
	mu          sync.Mutex
	resource    *ec2VMNICResource
	attachment  *EC2GuestAttachmentReceipt
	control     vmNICControl
	agent       io.Closer
	coordinator *EC2GuestAttachmentCoordinator
	cancel      context.CancelFunc
	lifetime    context.Context
	sealed      bool
	ready       bool
	done        chan struct{}
}
type vmNICProcess interface {
	io.ReadWriteCloser
	WaitReady(context.Context) error
	WaitCleanup(context.Context) error
}
type vmNICControl interface {
	Close() error
	QueryStatus(context.Context) (qmp.Status, error)
	Activate(context.Context) (qmp.Status, error)
}

func (r *EC2VMProbeLaunch) Ready() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// This is local admission availability, not a physical guest health probe.
	// Owner-driven shutdown closes the private resource without calling Close
	// on this receipt. Check it independently of the historical startup flag.
	if !r.ready || r.sealed || r.resource == nil || r.resource.sealed.Load() || r.lifetime == nil || r.lifetime.Err() != nil {
		return false
	}
	deadline, ok := r.lifetime.Deadline()
	return ok && time.Now().Before(deadline)
}

// ProbeScope remains discoverable after seal only if startup fully succeeded.
func (r *EC2VMProbeLaunch) ProbeScope() *guesttcp.HTTPProbeOperationScope {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.attachment == nil {
		return nil
	}
	return r.attachment.Scope()
}

// reserveStartup linearizes admission before seal. A reservation already in
// flight when Seal wins is cancelled and retained, never called "not dispatched".
func (r *EC2VMProbeLaunch) reserveStartup(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed || ctx.Err() != nil {
		return guesttcp.ErrClosed
	}
	return nil
}

// Descriptor returns copied immutable policy only after completed startup. It
// supplies correlation, never permission to construct another issuer.
func (r *EC2VMProbeLaunch) Descriptor() (guesttcp.OwnedProbeDescriptor, error) {
	if r == nil {
		return guesttcp.OwnedProbeDescriptor{}, errGuestLease
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.resource == nil {
		return guesttcp.OwnedProbeDescriptor{}, ErrEC2GuestAttachmentPending
	}
	return cloneVMDescriptor(r.resource.descriptor), nil
}

// Issuance is immutable provider provenance from the actual acquired resource,
// including Owner.Instance and Owner.Deployment (the provider's stack binding).
// It is correlation metadata, not a constructor or live authority.
func (r *EC2VMProbeLaunch) Issuance() (EC2GuestIssuance, error) {
	if r == nil {
		return EC2GuestIssuance{}, errGuestLease
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.resource == nil {
		return EC2GuestIssuance{}, ErrEC2GuestAttachmentPending
	}
	return r.resource.issuance, nil
}

// PassiveOwnership is authoritative only after startup returns. A pending
// constructor is not permission for the caller to close the passive service.
func (r *EC2VMProbeLaunch) PassiveOwnership() (known, transferred bool) {
	if r == nil {
		return true, false
	}
	select {
	case <-r.done:
	default:
		return false, false
	}
	r.mu.Lock()
	a := r.attachment
	r.mu.Unlock()
	if a == nil {
		return true, false
	}
	_, transferred, constructing := a.Ownership()
	return !constructing, transferred
}
func (r *EC2VMProbeLaunch) Close() error {
	if r == nil {
		return errGuestLease
	}
	r.mu.Lock()
	r.sealed = true
	cancel, resource, control, agent := r.cancel, r.resource, r.control, r.agent
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var err error
	if resource != nil {
		err = resource.Close()
	}
	if control != nil {
		err = errors.Join(err, control.Close())
	}
	if agent != nil {
		err = errors.Join(err, agent.Close())
	}
	return err
}
func (r *EC2VMProbeLaunch) Seal(ctx context.Context) error {
	err := r.Close()
	r.mu.Lock()
	a := r.attachment
	r.mu.Unlock()
	if a != nil {
		_, e := r.coordinator.Seal(ctx, a)
		err = errors.Join(err, e)
	}
	return err
}
func (r *EC2VMProbeLaunch) WaitCleanup(ctx context.Context) error {
	if r == nil || ctx == nil {
		return errGuestLease
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
	}
	if err := r.Seal(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	resource, a := r.resource, r.attachment
	r.mu.Unlock()
	if resource == nil {
		return ErrEC2GuestAttachmentPending
	}
	if a != nil {
		if err := r.coordinator.ObserveCleanup(ctx, a); err != nil {
			return err
		}
	}
	return resource.launch.WaitCleanup(ctx)
}

// Only this private type implements the provider issuer. Its stream comes from
// canonical guestvm acquisition, not caller IO or a saved database handle.
type ec2VMNICResource struct {
	commandMu   sync.Mutex
	command     *canonicalCommandOwner
	closed      chan struct{}
	closeSignal sync.Once
	network     *EC2NetworkLease
	launch      vmNICProcess
	issuance    EC2GuestIssuance
	descriptor  guesttcp.OwnedProbeDescriptor
	gate        sync.Mutex
	sealed      atomic.Bool
}

func (r *ec2VMNICResource) Stream() io.ReadWriteCloser { return r }
func (r *ec2VMNICResource) Read(p []byte) (int, error) {
	if r.sealed.Load() {
		return 0, guesttcp.ErrClosed
	}
	return r.launch.Read(p)
}
func (r *ec2VMNICResource) Write(p []byte) (int, error) {
	if r.sealed.Load() {
		return 0, guesttcp.ErrClosed
	}
	return r.launch.Write(p)
}
func (r *ec2VMNICResource) Close() error {
	r.sealed.Store(true)
	r.closeSignal.Do(func() {
		if r.closed != nil {
			close(r.closed)
		}
	})
	r.commandMu.Lock()
	command := r.command
	r.commandMu.Unlock()
	if command != nil {
		command.requestSeal()
	}
	streamErr := r.launch.Close()
	var commandErr error
	if command != nil {
		<-command.closeDone
		commandErr = command.closeErr
	}
	return errors.Join(commandErr, streamErr)
}
func (r *ec2VMNICResource) EC2Issuance() EC2GuestIssuance { return r.issuance }
func cloneVMDescriptor(d guesttcp.OwnedProbeDescriptor) guesttcp.OwnedProbeDescriptor {
	d.Config.NIC.ServicePorts = append([]uint16(nil), d.Config.NIC.ServicePorts...)
	d.Config.Paths = append([]string(nil), d.Config.Paths...)
	d.Services = append([]guesttcp.HTTPProbeService(nil), d.Services...)
	if d.ARP != nil {
		a := *d.ARP
		d.ARP = &a
	}
	return d
}
func (r *ec2VMNICResource) PersistentDescriptor() guesttcp.OwnedProbeDescriptor {
	return cloneVMDescriptor(r.descriptor)
}
func (r *ec2VMNICResource) Transact(ctx context.Context, a guesttcp.OwnedProbeAcceptance, apply func() error) error {
	if ctx == nil || apply == nil {
		return errGuestLease
	}
	// Close does not take this gate: it can interrupt blocked frame IO before join.
	r.gate.Lock()
	defer r.gate.Unlock()
	if r.sealed.Load() || ctx.Err() != nil || a.Descriptor == nil || !reflect.DeepEqual(*a.Descriptor, r.descriptor) || !reflect.DeepEqual(a.NIC, r.descriptor.Config.NIC) {
		return errGuestLease
	}
	invoke := func() error { return apply() }
	var err error
	if r.network != nil {
		err = r.network.Transact(ctx, func(snapshot EC2NetworkSnapshot) error {
			if snapshot.StackID != r.issuance.Lease.Owner.Deployment || snapshot.Region != r.issuance.Lease.Owner.Region {
				return errGuestLease
			}
			var port uint16
			if a.Observation != nil {
				port = a.Observation.Route.PeerPort
			} else if packet, e := guestnet.ParseTCPFrame(a.Frame); e == nil {
				if a.Direction == guesttcp.ToGuest {
					port = packet.DestinationPort
				} else {
					port = packet.SourcePort
				}
				// Fixed link-local metadata is private service, not public SG ingress.
				if packet.SourceIP == r.descriptor.Passive.LocalIP || packet.DestinationIP == r.descriptor.Passive.LocalIP {
					return invoke()
				}
			} else {
				return invoke()
			} // Owner independently validates exact trusted ARP bytes.
			if !snapshot.AllowsIngress(r.descriptor.Config.NIC.LocalIP, int(port)) {
				return errGuestLease
			}
			return invoke()
		})
	} else {
		err = errGuestLease
	}
	if r.sealed.Load() {
		return errors.Join(err, guesttcp.ErrClosed)
	}
	return err // The owned coordinator applies original deadline/cancellation checks.
}

// LaunchVMAndAttach is an opt-in canonical VM source path. It performs REAL
// socketpair/process/private-QMP operations and must not run where those actions
// are held. QGA remains at Prepared.QGASocket; no replacement channel is opened.
// The caller supplies trusted prepared assets and policy, never a raw stream.
// The exact acquired lease must already match Prepared's immutable receipt.
// Acquisition starts paused; this opt-in path activates only after attachment,
// then requires the unchanged private QGA handshake before publishing readiness.
type vmNICOperations struct {
	launch  func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error)
	control func(context.Context, *guestvm.Prepared) (vmNICControl, error)
	agent   func(context.Context, *guestvm.Prepared) (io.Closer, error)
}

func canonicalVMNICOperations() vmNICOperations {
	return vmNICOperations{
		launch: func(ctx context.Context, p *guestvm.Prepared, mac [6]byte) (vmNICProcess, error) {
			n, e := p.LaunchPersistentNIC(ctx, mac)
			if n == nil {
				return nil, e
			}
			return n, e
		},
		control: func(ctx context.Context, p *guestvm.Prepared) (vmNICControl, error) {
			conn, e := (&net.Dialer{}).DialContext(ctx, "unix", p.QMPSocket())
			if e != nil {
				return nil, e
			}
			c, e := p.OpenControl(ctx, conn, qmp.Options{})
			if c == nil {
				return nil, e
			}
			return c, e
		},
		agent: func(ctx context.Context, p *guestvm.Prepared) (io.Closer, error) {
			return awaitCanonicalQGA(ctx, p.QGASocket(), qgaBootOperations{
				dial: func(ctx context.Context, path string) (qga.Transport, error) {
					conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
					if conn == nil {
						return nil, err
					}
					return conn, err
				},
				handshake: func(ctx context.Context, t qga.Transport) (io.Closer, error) {
					c, err := qga.New(ctx, t, qga.Options{})
					if c == nil {
						return nil, err
					}
					return c, err
				},
				pause: func(ctx context.Context) error {
					timer := time.NewTimer(100 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timer.C:
						return nil
					}
				},
			})
		},
	}
}
func (c *EC2GuestAttachmentCoordinator) LaunchVMAndAttach(admission, lifetime context.Context, expected EC2GuestLease, prepared *guestvm.Prepared, descriptor guesttcp.OwnedProbeDescriptor, service guesttcp.RevocablePassiveService, deadline time.Time) (*EC2VMProbeLaunch, error) {
	if c == nil || c.store == nil {
		return nil, errGuestLease
	}
	backend, ok := c.store.backend.(*EC2VMBackend)
	if !ok || backend.networkLease == nil || !backend.networkLease.Active() || !reflect.DeepEqual(descriptor, backend.descriptor) {
		return nil, errGuestLease
	}
	// Production uses only the exact backend-owned acquired handle, never launches
	// from a caller supplied held row. The private seams below exist for tests.
	ops := backend.ops
	var control vmNICControl
	ops.launch = func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) {
		process, ownedControl, err := backend.takeAcquired(expected, prepared)
		control = ownedControl
		return process, err
	}
	ops.control = func(context.Context, *guestvm.Prepared) (vmNICControl, error) {
		if control == nil {
			return nil, errGuestLease
		}
		return control, nil
	}
	return c.launchVMAndAttach(admission, lifetime, expected, prepared, descriptor, service, deadline, ops)
}
func (c *EC2GuestAttachmentCoordinator) launchVMAndAttach(admission, lifetime context.Context, expected EC2GuestLease, prepared *guestvm.Prepared, descriptor guesttcp.OwnedProbeDescriptor, service guesttcp.RevocablePassiveService, deadline time.Time, ops vmNICOperations) (*EC2VMProbeLaunch, error) {
	if c == nil || admission == nil || lifetime == nil || prepared == nil || expected.State != "held" || !validGuestRecord(expected) || !prepared.MatchesNIC(descriptor.Config.NIC, descriptor.ARP) {
		return nil, errGuestLease
	}
	originalDeadline, ok := lifetime.Deadline()
	if !ok || !time.Now().Before(originalDeadline) {
		return nil, errGuestLease
	}
	record := prepared.Record()
	if record.Intent.Fence != expected.Intent.Fence || record.Intent.Operation != 1 || record.Intent.Action != expected.Intent.Action || record.Intent.Lease != "" || record.Lease != expected.Intent.Lease {
		return nil, errGuestLease
	}
	if err := c.reserveVMNIC(admission, expected, record); err != nil {
		return nil, err
	}
	owned, cancel := context.WithDeadline(lifetime, originalDeadline)
	result := &EC2VMProbeLaunch{coordinator: c, cancel: cancel, lifetime: owned, done: make(chan struct{})}
	c.mu.Lock()
	if c.vmLaunches == nil {
		c.vmLaunches = make(map[EC2GuestOwner]interface{ Close() error })
	}
	c.vmLaunches[expected.Owner] = result
	c.mu.Unlock()
	defer close(result.done)
	if backend, ok := c.store.backend.(*EC2VMBackend); ok {
		context.AfterFunc(backend.lifetime, cancel)
	}
	// Startup operation context intersects both captured lifetime and original
	// admission deadline; cancelling it does not cancel the persistent owner.
	startDeadline := originalDeadline
	if d, ok := admission.Deadline(); ok && d.Before(startDeadline) {
		startDeadline = d
	}
	startup, stopStartup := context.WithDeadline(admission, startDeadline)
	stopOwned := context.AfterFunc(owned, stopStartup)
	defer func() { stopOwned(); stopStartup() }()
	fail := func(err error) (*EC2VMProbeLaunch, error) { _ = result.Close(); return result, err }
	var incarnation [32]byte
	if _, err := rand.Read(incarnation[:]); err != nil {
		return fail(err)
	}
	descriptor = cloneVMDescriptor(descriptor)
	descriptor.Binding.BackendIncarnation = incarnation
	policy, err := json.Marshal(descriptor)
	if err != nil {
		return fail(err)
	}
	digest := sha256.Sum256(policy)
	launch, err := ops.launch(owned, prepared, descriptor.Config.NIC.PeerMAC)
	if launch != nil {
		resource := &ec2VMNICResource{closed: make(chan struct{}), launch: launch, descriptor: descriptor, issuance: EC2GuestIssuance{Lease: expected, BackendIncarnation: incarnation, PolicyGeneration: hex.EncodeToString(digest[:])}}
		if backend, ok := c.store.backend.(*EC2VMBackend); ok {
			resource.network = backend.networkLease
		}
		result.mu.Lock()
		result.resource = resource
		sealed := result.sealed
		result.mu.Unlock()
		if sealed {
			_ = resource.Close()
			return fail(guesttcp.ErrClosed)
		}
	}
	if err != nil {
		return fail(err)
	}
	if launch == nil {
		return fail(guestprocess.ErrOwnershipUncertain)
	}
	if err = launch.WaitReady(startup); err != nil {
		return fail(err)
	}
	control, err := ops.control(startup, prepared)
	if control != nil {
		result.mu.Lock()
		result.control = control
		sealed := result.sealed
		result.mu.Unlock()
		if sealed {
			_ = control.Close()
			return fail(guesttcp.ErrClosed)
		}
	}
	if err != nil {
		return fail(err)
	}
	if control == nil {
		return fail(errGuestLease)
	}
	status, err := control.QueryStatus(startup)
	if err != nil || status.Running || (status.State != "prelaunch" && status.State != "paused") {
		return fail(errors.Join(err, qmp.ErrNotPaused))
	}
	attachment, err := c.Attach(admission, owned, expected, result.resource, service, deadline)
	result.mu.Lock()
	result.attachment = attachment
	sealed := result.sealed
	result.mu.Unlock()
	if err != nil {
		return fail(err)
	}
	if sealed {
		return fail(guesttcp.ErrClosed)
	}
	if err = result.reserveStartup(startup); err != nil {
		return fail(err)
	}
	if _, err = control.Activate(startup); err != nil {
		return fail(err)
	}
	// QGA uses ONLY the original private Unix channel. Readiness is not inferred
	// from QMP running, a socket path, or successful Ethernet attachment.
	if err = result.reserveStartup(startup); err != nil {
		return fail(err)
	}
	agent, err := ops.agent(startup, prepared)
	if agent != nil {
		result.mu.Lock()
		result.agent = agent
		sealed = result.sealed
		result.mu.Unlock()
		if sealed {
			_ = agent.Close()
			return fail(guesttcp.ErrClosed)
		}
	}
	if err != nil {
		return fail(err)
	}
	if agent == nil {
		return fail(errGuestLease)
	}
	result.mu.Lock()
	sealed = result.sealed || owned.Err() != nil || admission.Err() != nil
	if !sealed {
		result.ready = true
	}
	result.mu.Unlock()
	if sealed {
		return fail(guesttcp.ErrClosed)
	}
	return result, nil
}

func (c *EC2GuestAttachmentCoordinator) reserveVMNIC(ctx context.Context, expected EC2GuestLease, record guestvm.Record) error {
	_, err := c.store.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS aws_ec2_vm_nic_launches(account TEXT NOT NULL,region TEXT NOT NULL,instance TEXT NOT NULL,record TEXT NOT NULL,profile_name TEXT NOT NULL,profile_id TEXT NOT NULL,PRIMARY KEY(account,region,instance))`)
	if err != nil {
		return err
	}
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, _, err := guestLeaseRead(ctx, tx, expected.Owner)
	if err != nil {
		return err
	}
	if current != expected {
		return errGuestLease
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	o := expected.Owner
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_vm_nic_launches(account,region,instance,record,profile_name,profile_id) SELECT ?,?,?,?,?,? WHERE (SELECT count(*) FROM aws_ec2_vm_nic_launches)<?`, o.Account, o.Region, o.Instance, string(raw), o.ProfileName, o.ProfileID, guestLeaseLimit)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errGuestLease
	}
	return tx.Commit()
}
