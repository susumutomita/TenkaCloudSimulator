package aws

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
)

type attachmentMemoryStream struct {
	closed        chan struct{}
	once          sync.Once
	reads, writes atomic.Int32
	closeEntered  chan struct{}
	closeRelease  chan struct{}
	closeErr      error
}

func (s *attachmentMemoryStream) Read([]byte) (int, error) {
	s.reads.Add(1)
	<-s.closed
	return 0, io.EOF
}
func (s *attachmentMemoryStream) Write(p []byte) (int, error) { s.writes.Add(1); return len(p), nil }
func (s *attachmentMemoryStream) Close() error {
	s.once.Do(func() {
		close(s.closed)
		if s.closeEntered != nil {
			close(s.closeEntered)
		}
	})
	if s.closeRelease != nil {
		<-s.closeRelease
	}
	return s.closeErr
}

type attachmentMemoryResource struct {
	stream     *attachmentMemoryStream
	issuance   EC2GuestIssuance
	descriptor guesttcp.OwnedProbeDescriptor
	onStream   func()
}

func (r *attachmentMemoryResource) Stream() io.ReadWriteCloser {
	if r.onStream != nil {
		r.onStream()
	}
	return r.stream
}
func (r *attachmentMemoryResource) Transact(_ context.Context, _ guesttcp.OwnedProbeAcceptance, f func() error) error {
	return f()
}
func (r *attachmentMemoryResource) PersistentDescriptor() guesttcp.OwnedProbeDescriptor {
	return r.descriptor
}
func (r *attachmentMemoryResource) EC2Issuance() EC2GuestIssuance { return r.issuance }

type attachmentPassive struct {
	ctx    context.Context
	cancel context.CancelFunc
	closes atomic.Int32
}

func (p *attachmentPassive) Lifetime() context.Context { return p.ctx }
func (p *attachmentPassive) Inject([]byte) error       { return nil }
func (p *attachmentPassive) ReadFrame(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, guesttcp.ErrClosed
	}
}
func (p *attachmentPassive) Close() error { p.closes.Add(1); p.cancel(); return nil }
func attachmentFixture(t *testing.T) (*EC2GuestAttachmentCoordinator, EC2GuestLease, *attachmentMemoryResource, *attachmentPassive, context.Context) {
	t.Helper()
	db, e := core.New(filepath.Join(t.TempDir(), "attachment.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	store, e := NewEC2GuestLeaseStore(db.Database(), newQueuedLeaseBackend())
	if e != nil {
		t.Fatal(e)
	}
	iam, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	profile := profileCreate(t, iam, "AttachmentProfile")
	owner := leaseOwner()
	owner.ProfileName = profile.InstanceProfileName
	owner.ProfileID = profile.InstanceProfileID
	lease, e := store.Acquire(context.Background(), owner)
	if e != nil {
		t.Fatal(e)
	}
	lease, e = store.Reconcile(context.Background(), lease)
	if e != nil {
		t.Fatal(e)
	}
	c, e := NewEC2GuestAttachmentCoordinator(store, strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	passiveCtx, passiveCancel := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	p := &attachmentPassive{ctx: passiveCtx, cancel: passiveCancel}
	t.Cleanup(passiveCancel)
	nic := guestnet.ProbeNIC{LocalIP: netip.MustParseAddr("198.51.100.2"), PeerIP: netip.MustParseAddr("10.0.1.4"), LocalMAC: [6]byte{2, 0, 0, 0, 0, 1}, PeerMAC: [6]byte{2, 0, 0, 0, 0, 2}, ServicePorts: []uint16{80}}
	binding := guesttcp.HTTPProbeScopeBinding{Owner: [32]byte{1}, Deployment: [32]byte{2}, Lease: [32]byte{3}, BackendIncarnation: [32]byte{4}}
	descriptor := guesttcp.OwnedProbeDescriptor{Config: guesttcp.OwnedProbeConfig{NIC: nic, Paths: []string{"/healthz"}, EvidenceLimit: 2}, Binding: binding, Services: []guesttcp.HTTPProbeService{{Name: "web", Port: 80, Path: "/healthz"}}, Passive: guestnet.TCPRoute{LocalIP: netip.MustParseAddr("169.254.169.254"), PeerIP: nic.PeerIP, LocalMAC: nic.LocalMAC, PeerMAC: nic.PeerMAC, LocalPort: 80, AnyPeerPort: true}}
	r := &attachmentMemoryResource{stream: &attachmentMemoryStream{closed: make(chan struct{})}, issuance: EC2GuestIssuance{Lease: lease, BackendIncarnation: binding.BackendIncarnation, PolicyGeneration: strings.Repeat("b", 64)}, descriptor: descriptor}
	return c, lease, r, p, ctx
}
func TestEC2GuestAttachmentJournalBeforeIOAndSeal(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	r.onStream = func() {
		var state string
		if e := c.store.db.QueryRow(`SELECT state FROM aws_ec2_guest_attachments`).Scan(&state); e != nil || state != "preparing" {
			t.Errorf("resource consumed before durable intent: %s %v", state, e)
		}
	}
	receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	scope := receipt.Scope()
	if scope == nil {
		t.Fatal("missing scope")
	}
	// Mutation of supplier slices after capture does not mutate the journal.
	r.descriptor.Config.NIC.ServicePorts[0] = 443
	if _, e = c.store.Advance(ctx, lease); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatalf("plain store bypass: %v", e)
	}
	other, e := NewEC2GuestLeaseStore(c.store.db, newQueuedLeaseBackend())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Advance(ctx, lease); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatalf("aliased store bypass: %v", e)
	}
	pending, e := c.Advance(ctx, lease)
	if e != nil || pending.State != "contain_pending" {
		t.Fatal(pending, e)
	}
	if receipt.Scope() != scope {
		t.Fatal("retained scope lost")
	}
	if e = c.ObserveCleanup(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = c.store.db.QueryRow(`SELECT state FROM aws_ec2_guest_attachments`).Scan(&state); e != nil || state != "locally_drained" {
		t.Fatal(state, e)
	}
	// Operation termination remains a failure even though cleanup succeeded.
	if operationErr := receipt.entry.attachment.WaitContext(ctx); operationErr == nil {
		t.Fatal("shutdown lost terminal operation outcome")
	}
	// Local drain is not backend observation: native lease remains pending.
	got, e := c.store.Get(ctx, lease.Owner)
	if e != nil || got.State != "contain_pending" {
		t.Fatal(got, e)
	}
}
func TestEC2GuestAttachmentSealBeforeOwnerRegistration(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	r.onStream = func() { close(entered); <-release }
	type result struct {
		r *EC2GuestAttachmentReceipt
		e error
	}
	done := make(chan result, 1)
	go func() {
		receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
		done <- result{receipt, e}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, e := c.Advance(ctx, lease)
	if e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	got := <-done
	if got.e == nil || got.r.Scope() != nil {
		t.Fatal("sealed constructor published", got.e)
	}
	if r.stream.reads.Load() != 0 || r.stream.writes.Load() != 0 || p.closes.Load() != 0 {
		t.Fatal("transport started or caller service consumed after seal")
	}
	if e = c.ObserveCleanup(ctx, got.r); e != nil {
		t.Fatal(e)
	}
}
func TestEC2GuestAttachmentRestartCannotAdopt(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _, _ = c.Seal(context.Background(), receipt); _ = c.ObserveCleanup(ctx, receipt) }()
	restarted, e := NewEC2GuestAttachmentCoordinator(c.store, strings.Repeat("c", 64))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.Advance(ctx, lease); !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("restart adopted authority", e)
	}
	if _, e = restarted.Attach(ctx, ctx, lease, r, p, time.Now().Add(time.Second)); e == nil {
		t.Fatal("restart reconstructed capability")
	}
}
func TestEC2GuestAttachmentWrongAssociationUnconsumed(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	r.issuance.Lease.Owner.Deployment = "other"
	r.onStream = func() { t.Error("invalid association consumed resource") }
	if receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(time.Second)); e == nil || receipt != nil {
		t.Fatal("mismatched provenance", e)
	}
	if p.closes.Load() != 0 {
		t.Fatal("caller service consumed")
	}
}

func TestEC2GuestAttachmentSharedProfileMutationGuards(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _, _ = c.Seal(context.Background(), receipt); _ = c.ObserveCleanup(ctx, receipt) }()
	iam, e := NewIAMRoleStore(c.store.db)
	if e != nil {
		t.Fatal(e)
	}
	role := profileRole(t, iam, "AttachmentRole")
	if e = iam.DeleteInstanceProfile(ctx, lease.Owner.ProfileName, lease.Owner.ProfileID); iamCode(e) != "DeleteConflict" {
		t.Fatalf("native deletion bypass: %v", e)
	}
	if e = iam.ChangeInstanceProfileRole(ctx, lease.Owner.ProfileName, lease.Owner.ProfileID, role.RoleName, role.RoleID, true); iamCode(e) != "DeleteConflict" {
		t.Fatalf("native membership bypass: %v", e)
	}
	tx, e := c.store.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	e = iamDeleteInstanceProfileTx(ctx, tx, lease.Owner.ProfileName, lease.Owner.ProfileID)
	_ = tx.Rollback()
	if iamCode(e) != "DeleteConflict" {
		t.Fatalf("CFN shared helper bypass: %v", e)
	}
	if _, e = c.Seal(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	if e = c.ObserveCleanup(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	if e = iam.ChangeInstanceProfileRole(ctx, lease.Owner.ProfileName, lease.Owner.ProfileID, role.RoleName, role.RoleID, true); e != nil {
		t.Fatal("drained profile remained locked", e)
	}
}

func TestEC2GuestAttachmentSlowCloseDoesNotHoldDatabase(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	r.stream.closeEntered = make(chan struct{})
	r.stream.closeRelease = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(r.stream.closeRelease) })
	requested := time.Now().Add(3 * time.Second)
	receipt, e := c.Attach(ctx, ctx, lease, r, p, requested)
	if e != nil {
		t.Fatal(e)
	}
	status, e := c.Get(ctx, lease.Owner)
	original, _ := ctx.Deadline()
	if e != nil || !status.Deadline.Equal(requested) || !status.OriginalLifetime.Equal(original) {
		t.Fatal("deadline pin", status, e)
	}
	pending, e := c.Advance(ctx, lease)
	if e != nil || pending.State != "contain_pending" {
		t.Fatal(pending, e)
	}
	select {
	case <-r.stream.closeEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Advance already acknowledged with Close blocked; DB remains usable.
	if _, e = c.store.Get(ctx, lease.Owner); e != nil {
		t.Fatal("database held across Close", e)
	}
	short, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	e = c.ObserveCleanup(short, receipt)
	cancel()
	if !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("blocked close marked drained", e)
	}
	status, e = c.Get(ctx, lease.Owner)
	if e != nil || status.State != "seal_pending" || !status.RecoveryRequired {
		t.Fatal(status, e)
	}
	release.Do(func() { close(r.stream.closeRelease) })
	if e = c.ObserveCleanup(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	if e = c.ObserveCleanup(ctx, receipt); e != nil {
		t.Fatal("idempotent cleanup", e)
	}
	status, e = c.Get(ctx, lease.Owner)
	if e != nil || status.State != "locally_drained" || status.RecoveryRequired {
		t.Fatal(status, e)
	}
}

func TestEC2GuestAttachmentOptInCompatibility(t *testing.T) {
	db, e := core.New(filepath.Join(t.TempDir(), "ordinary.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	store, e := NewEC2GuestLeaseStore(db.Database(), newQueuedLeaseBackend())
	if e != nil {
		t.Fatal(e)
	}
	lease, e := store.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	lease, e = store.Reconcile(ctx, lease)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Advance(ctx, lease); e != nil {
		t.Fatal("feature-off lease blocked", e)
	}
	iam, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	profile := profileCreate(t, iam, "OrdinaryProfile")
	role := profileRole(t, iam, "OrdinaryRole")
	if e = iam.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
		t.Fatal(e)
	}
	if e = iam.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, false); e != nil {
		t.Fatal(e)
	}
	if e = iam.DeleteInstanceProfile(ctx, profile.InstanceProfileName, profile.InstanceProfileID); e != nil {
		t.Fatal("feature-off IAM blocked", e)
	}
}

func TestEC2GuestAttachmentUnrelatedProfileRemainsMutable(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _, _ = c.Seal(context.Background(), receipt); _ = c.ObserveCleanup(ctx, receipt) }()
	iam, e := NewIAMRoleStore(c.store.db)
	if e != nil {
		t.Fatal(e)
	}
	profile := profileCreate(t, iam, "UnrelatedProfile")
	role := profileRole(t, iam, "UnrelatedRole")
	if e = iam.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
		t.Fatal(e)
	}
	if e = iam.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, false); e != nil {
		t.Fatal(e)
	}
	if e = iam.DeleteInstanceProfile(ctx, profile.InstanceProfileName, profile.InstanceProfileID); e != nil {
		t.Fatal("unrelated profile blocked", e)
	}
}

func TestEC2GuestAttachmentFailedCloseRetainsDebt(t *testing.T) {
	c, lease, r, p, ctx := attachmentFixture(t)
	failed := errors.New("memory close failure")
	r.stream.closeErr = failed
	receipt, e := c.Attach(ctx, ctx, lease, r, p, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Seal(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	if e = c.ObserveCleanup(ctx, receipt); !errors.Is(e, failed) || !errors.Is(e, ErrEC2GuestAttachmentPending) {
		t.Fatal("close failure suppressed", e)
	}
	status, e := c.Get(ctx, lease.Owner)
	if e != nil || status.State != "seal_pending" || !status.RecoveryRequired {
		t.Fatal(status, e)
	}
	// Intentional conservative owner/transport debt stays retained until process
	// exit. A second observation cannot retry Close or manufacture a drained row.
	if e = c.ObserveCleanup(ctx, receipt); !errors.Is(e, failed) {
		t.Fatal(e)
	}
}
