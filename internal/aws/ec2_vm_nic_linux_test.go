//go:build linux

package aws

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

type vmFakeProcess struct {
	*attachmentMemoryStream
	ready      func() error
	cleanupErr error
}

func (p *vmFakeProcess) WaitReady(context.Context) error {
	if p.ready != nil {
		return p.ready()
	}
	return nil
}
func (p *vmFakeProcess) WaitCleanup(ctx context.Context) error {
	select {
	case <-p.closed:
		return p.cleanupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type vmFakeControl struct {
	activations     atomic.Int32
	closed          atomic.Bool
	beforeActivate  func()
	activationCheck func() error
}

func (c *vmFakeControl) Close() error { c.closed.Store(true); return nil }
func (c *vmFakeControl) QueryStatus(context.Context) (qmp.Status, error) {
	return qmp.Status{State: "prelaunch"}, nil
}
func (c *vmFakeControl) Activate(ctx context.Context) (qmp.Status, error) {
	if c.beforeActivate != nil {
		c.beforeActivate()
	}
	if ctx.Err() != nil || c.closed.Load() {
		return qmp.Status{}, guesttcp.ErrClosed
	}
	if c.activationCheck != nil {
		if err := c.activationCheck(); err != nil {
			return qmp.Status{}, err
		}
	}
	c.activations.Add(1)
	return qmp.Status{Running: true, State: "running"}, nil
}

type vmFakeAgent struct{}

func (vmFakeAgent) Close() error { return nil }

// Files are inert fake assets. Only extraction/seed creation runs; the private
// operations below replace every process/socket/control operation before Acquire.
func vmBackendFixture(t *testing.T) (*EC2VMBackend, *EC2GuestLeaseStore, *EC2GuestAttachmentCoordinator, *vmFakeProcess, *vmFakeControl, *atomic.Int32, context.Context) {
	t.Helper()
	dir, e := os.MkdirTemp("", "vmnic-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, e := core.New(filepath.Join(dir, "db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	disk := make([]byte, 512)
	copy(disk, "QFI\xfb")
	binary.BigEndian.PutUint32(disk[4:8], 3)
	binary.BigEndian.PutUint32(disk[20:24], 9)
	binary.BigEndian.PutUint64(disk[24:32], 1<<20)
	binary.BigEndian.PutUint32(disk[100:104], 104)
	layout := guestvm.Layout{Emulator: "bin/qemu", Firmware: "firmware/bios", BaseDisk: "images/base.qcow2"}
	manifest := guestbundle.Manifest{OS: runtime.GOOS, Arch: runtime.GOARCH}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	for i, x := range []struct {
		name string
		data []byte
	}{{layout.Emulator, []byte("inert-not-a-program")}, {layout.Firmware, []byte("inert")}, {layout.BaseDisk, disk}} {
		manifest.Files = append(manifest.Files, guestbundle.File{Path: x.name, Size: int64(len(x.data)), SHA256: digest(x.data), Executable: i == 0})
		if e = tw.WriteHeader(&tar.Header{Name: x.name, Size: int64(len(x.data)), Mode: 0600}); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(x.data); e != nil {
			t.Fatal(e)
		}
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = gz.Close(); e != nil {
		t.Fatal(e)
	}
	manifest.ArchiveSHA256 = digest(archive.Bytes())
	bundle, e := guestbundle.Extract(context.Background(), dir, bytes.NewReader(archive.Bytes()), manifest)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { bundle.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	nic := guestnet.ProbeNIC{LocalIP: netip.MustParseAddr("198.51.100.2"), PeerIP: netip.MustParseAddr("10.0.1.4"), LocalMAC: [6]byte{2, 0, 0, 0, 0, 1}, PeerMAC: [6]byte{2, 0, 0, 0, 0, 2}, ServicePorts: []uint16{80, 8080}}
	descriptor := guesttcp.OwnedProbeDescriptor{Config: guesttcp.OwnedProbeConfig{NIC: nic, Paths: []string{"/healthz"}, EvidenceLimit: 2}, Binding: guesttcp.HTTPProbeScopeBinding{Owner: [32]byte{1}, Deployment: [32]byte{2}, Lease: [32]byte{3}, BackendIncarnation: [32]byte{4}}, Services: []guesttcp.HTTPProbeService{{Name: "web", Port: 80, Path: "/healthz"}, {Name: "api", Port: 8080, Path: "/healthz"}}, Passive: guestnet.TCPRoute{LocalIP: netip.MustParseAddr("169.254.169.254"), PeerIP: nic.PeerIP, LocalMAC: nic.LocalMAC, PeerMAC: nic.PeerMAC, LocalPort: 80, AnyPeerPort: true}, ARP: &guestnet.ARPConfig{GuestIP: nic.PeerIP, GatewayIP: netip.MustParseAddr("10.0.1.1"), GuestMAC: nic.PeerMAC, GatewayMAC: nic.LocalMAC}}
	backend, e := NewEC2VMBackend(ctx, db.Database(), leaseOwner(), bundle, layout, dir, []byte("# original fixture bytes\n"), guestseed.IPv4Network{MACAddress: "02:00:00:00:00:02", Address: "10.0.1.4/24", Gateway: "10.0.1.1"}, descriptor)
	if e != nil {
		t.Fatal(e)
	}
	networkStore, e := NewEC2NetworkStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	rows := []EC2NetworkResource{
		{ID: "vpc-test", Kind: "AWS::EC2::VPC", CIDR: "10.0.0.0/16"},
		{ID: "subnet-test", Kind: "AWS::EC2::Subnet", VPCID: "vpc-test", CIDR: "10.0.1.0/24", PublicIP: true},
		{ID: "igw-test", Kind: "AWS::EC2::InternetGateway"},
		{ID: "attach-test", Kind: "AWS::EC2::VPCGatewayAttachment", VPCID: "vpc-test", GatewayID: "igw-test"},
		{ID: "rtb-test", Kind: "AWS::EC2::RouteTable", VPCID: "vpc-test"},
		{ID: "route-test", Kind: "AWS::EC2::Route", RouteTableID: "rtb-test", GatewayID: "igw-test", CIDR: "0.0.0.0/0", RouteEnabled: true},
		{ID: "assoc-test", Kind: "AWS::EC2::SubnetRouteTableAssociation", SubnetID: "subnet-test", RouteTableID: "rtb-test"},
		{ID: "sg-test", Kind: "AWS::EC2::SecurityGroup", VPCID: "vpc-test", Ingress: []EC2IngressRule{{Protocol: "tcp", FromPort: 80, ToPort: 80, CIDR: "0.0.0.0/0"}, {Protocol: "tcp", FromPort: 8080, ToPort: 8080, CIDR: "0.0.0.0/0"}}},
	}
	tx, e := db.Database().BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		row.Region = backend.owner.Region
		row.StackID = backend.owner.Deployment
		row.Revision = 1
		if e = createEC2Network(ctx, tx, row); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	backend.networkLease, e = networkStore.LeaseFixedPolicy(ctx, backend.owner.Region, backend.owner.Deployment, "subnet-test", []string{"sg-test"}, backend.SealNIC)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(backend.networkLease.Close)
	process := &vmFakeProcess{attachmentMemoryStream: &attachmentMemoryStream{closed: make(chan struct{})}}
	control := &vmFakeControl{}
	calls := &atomic.Int32{}
	backend.ops = vmNICOperations{launch: func(_ context.Context, p *guestvm.Prepared, _ [6]byte) (vmNICProcess, error) {
		calls.Add(1)
		var raw string
		if err := backend.db.QueryRow(`SELECT prepared FROM aws_ec2_vm_backend_intents`).Scan(&raw); err != nil || !strings.Contains(raw, p.Record().UUID) {
			t.Error("missing durable association before launch", err)
		}
		return process, nil
	}, control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) { return control, nil }, agent: func(context.Context, *guestvm.Prepared) (io.Closer, error) { return vmFakeAgent{}, nil }}
	store, e := NewEC2GuestLeaseStore(db.Database(), backend)
	if e != nil {
		t.Fatal(e)
	}
	coordinator, e := NewEC2GuestAttachmentCoordinator(store, strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	return backend, store, coordinator, process, control, calls, ctx
}

func TestCanonicalVMBackendMemoryChain(t *testing.T) {
	b, s, c, p, control, calls, ctx := vmBackendFixture(t)
	lease, e := s.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	lease, e = s.Reconcile(ctx, lease)
	if e != nil || lease.State != "held" {
		t.Fatal(lease, e)
	}
	control.activationCheck = func() error {
		c.mu.Lock()
		entry := c.entries[lease.Owner]
		c.mu.Unlock()
		if entry == nil {
			return errors.New("activation before owner")
		}
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if entry.owner == nil || entry.attachment == nil {
			return errors.New("activation before persistent attachment")
		}
		return nil
	}
	passiveCtx, passiveCancel := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	defer passiveCancel()
	passive := &attachmentPassive{ctx: passiveCtx, cancel: passiveCancel}
	receipt, e := c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	if !receipt.Ready() || receipt.ProbeScope() == nil || calls.Load() != 1 || control.activations.Load() != 1 {
		t.Fatal("missing actual source stages")
	}
	if _, e = c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(time.Second)); e == nil {
		t.Fatal("duplicate attachment")
	}
	// Revoke through the actual owner, not receipt.Seal: this is the path
	// taken by automatic owner expiry and must invalidate new resolution.
	c.mu.Lock()
	entry := c.entries[lease.Owner]
	c.mu.Unlock()
	entry.mu.Lock()
	owner := entry.owner
	entry.mu.Unlock()
	owner.Revoke()
	select {
	case <-p.closed:
	case <-ctx.Done():
		t.Fatal("owner-driven close did not reach resource")
	}
	if receipt.Ready() || receipt.ProbeScope() == nil {
		t.Fatal("owner-driven close left readiness or lost historical scope")
	}
	if _, err := receipt.Descriptor(); err != nil {
		t.Fatal("owner close erased historical descriptor", err)
	}
	if e = receipt.Seal(ctx); e != nil {
		t.Fatal(e)
	}
	if receipt.Ready() || receipt.ProbeScope() == nil {
		t.Fatal("lost retained read scope or stillready")
	}
	if e = receipt.WaitCleanup(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case <-p.closed:
	default:
		t.Fatal("process stream notclosed")
	}
	if calls.Load() != 1 {
		t.Fatal("relaunch")
	}
}
func TestCanonicalVMBackendMemoryReplayAndRestart(t *testing.T) {
	b, s, _, _, _, calls, ctx := vmBackendFixture(t)
	pending, e := s.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Dispatch(ctx, pending.Intent); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate launched")
	}
	restarted, e := NewEC2VMBackend(ctx, b.db, b.owner, b.bundle, b.layout, b.directory, b.userdata, b.network, b.descriptor)
	if e != nil {
		t.Fatal(e)
	}
	observed, e := restarted.Observe(ctx, pending.Intent)
	if e != nil || observed.State != guestlease.Unknown {
		t.Fatal("DB adopted guest", observed, e)
	}
	forbidden := atomic.Int32{}
	restarted.ops.launch = func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) {
		forbidden.Add(1)
		return nil, errors.New("must not launch")
	}
	if _, e = restarted.Dispatch(ctx, pending.Intent); e == nil {
		t.Fatal("durable reservation replay accepted")
	}
	if forbidden.Load() != 0 {
		t.Fatal("restart launched")
	}
	_ = b.process.Close()
}

func TestCanonicalVMBackendMemorySealBeforeActivation(t *testing.T) {
	b, s, c, _, control, _, ctx := vmBackendFixture(t)
	lease, e := s.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	lease, e = s.Reconcile(ctx, lease)
	if e != nil {
		t.Fatal(e)
	}
	control.beforeActivate = func() {
		c.mu.Lock()
		pending := c.vmLaunches[lease.Owner]
		c.mu.Unlock()
		if pending == nil {
			t.Fatal("no retained startup owner")
		}
		_ = pending.Close()
	}
	passiveCtx, stop := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	defer stop()
	passive := &attachmentPassive{ctx: passiveCtx, cancel: stop}
	receipt, e := c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(3*time.Second))
	if e == nil || receipt == nil || receipt.Ready() || control.activations.Load() != 0 {
		t.Fatal("sealed activation accepted", e)
	}
	if known, transferred := receipt.PassiveOwnership(); !known || !transferred {
		t.Fatal("lost transferred cleanup debt")
	}
	if e = receipt.WaitCleanup(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestCanonicalVMBackendMemoryCloseFailureRetained(t *testing.T) {
	b, s, c, process, _, _, ctx := vmBackendFixture(t)
	lease, e := s.Acquire(ctx, leaseOwner())
	if e != nil {
		t.Fatal(e)
	}
	lease, e = s.Reconcile(ctx, lease)
	if e != nil {
		t.Fatal(e)
	}
	process.attachmentMemoryStream.closeErr = errors.New("unknown close")
	passiveCtx, stop := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	defer stop()
	passive := &attachmentPassive{ctx: passiveCtx, cancel: stop}
	receipt, e := c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	if e = receipt.Seal(ctx); e == nil {
		t.Fatal("unknownclose accepted")
	}
	if e = receipt.WaitCleanup(ctx); e == nil {
		t.Fatal("unknownclose settled")
	}
	c.mu.Lock()
	retained := c.vmLaunches[lease.Owner]
	c.mu.Unlock()
	if retained != receipt {
		t.Fatal("uncertain receipt discarded")
	}
}

// A non-notifying deadline tests Ready's captured absolute bound without
// scheduler-dependent sleeps or claiming any physical guest health check.
type canonicalSilentDeadline struct {
	context.Context
	deadline time.Time
}

func (c canonicalSilentDeadline) Deadline() (time.Time, bool) { return c.deadline, true }
func TestCanonicalReceiptReadinessLifetimeMemoryOnly(t *testing.T) {
	live, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	r := &EC2VMProbeLaunch{ready: true, lifetime: live, resource: &ec2VMNICResource{}}
	if !r.Ready() {
		t.Fatal("live local receipt rejected")
	}
	cancel()
	if r.Ready() {
		t.Fatal("cancelled lifetime accepted")
	}
	r.lifetime = canonicalSilentDeadline{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if r.Ready() {
		t.Fatal("expired non-notifying lifetime accepted")
	}
	r.lifetime = context.Background()
	if r.Ready() {
		t.Fatal("unbounded lifetime accepted")
	}
	if _, err := r.Descriptor(); err != nil {
		t.Fatal("historical descriptor lost", err)
	}
}

func TestDialPrivateQMPWaitsForLateListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qmp.sock")
	ready := make(chan net.Listener, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		l, err := net.Listen("unix", path)
		if err != nil {
			ready <- nil
			return
		}
		ready <- l
	}()
	conn, err := dialPrivateQMP(context.Background(), path, func(ctx context.Context, p string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	})
	l := <-ready
	if l == nil {
		t.Fatal("listener failed")
	}
	defer l.Close()
	if err != nil {
		t.Fatalf("dial after late listen: %v", err)
	}
	_ = conn.Close()
}

func TestDialPrivateQMPDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	want := errors.New("permission")
	_, err := dialPrivateQMP(context.Background(), "x", func(context.Context, string) (net.Conn, error) {
		calls++
		return nil, want
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDialPrivateQMPStopsAtCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := dialPrivateQMP(ctx, "x", func(context.Context, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	})
	if !errors.Is(err, syscall.ECONNREFUSED) || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	if _, err = dialPrivateQMP(nil, "x", nil); err == nil { //nolint:staticcheck // nil parent is rejected
		t.Fatal("nil parent accepted")
	}
}
