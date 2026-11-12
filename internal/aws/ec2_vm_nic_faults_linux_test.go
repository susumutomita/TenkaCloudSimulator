//go:build linux

package aws

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestprocess"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

// statusControl reports a configurable QMP state before activation.
type statusControl struct {
	vmFakeControl
	status qmp.Status
	err    error
}

func (c *statusControl) QueryStatus(context.Context) (qmp.Status, error) { return c.status, c.err }

type readyFailProcess struct{ *vmFakeProcess }

func heldVMLease(t *testing.T) (*EC2VMBackend, *EC2GuestAttachmentCoordinator, EC2GuestLease, *attachmentPassive, context.Context) {
	t.Helper()
	b, s, c, _, _, _, ctx := vmBackendFixture(t)
	lease, err := s.Acquire(ctx, leaseOwner())
	if err != nil {
		t.Fatal(err)
	}
	if lease, err = s.Reconcile(ctx, lease); err != nil || lease.State != "held" {
		t.Fatal(lease, err)
	}
	passiveCtx, stop := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	t.Cleanup(stop)
	return b, c, lease, &attachmentPassive{ctx: passiveCtx, cancel: stop}, ctx
}

func newFakeProcess() *vmFakeProcess {
	return &vmFakeProcess{attachmentMemoryStream: &attachmentMemoryStream{closed: make(chan struct{})}}
}

// Every startup stage failure must fail closed, return the retained receipt
// for cleanup and never publish readiness.
func TestLaunchVMAndAttachFailsClosedAtEveryStartupStage(t *testing.T) {
	boom := errors.New("injected stage failure")
	okLaunch := func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) { return newFakeProcess(), nil }
	okControl := func(context.Context, *guestvm.Prepared) (vmNICControl, error) { return &vmFakeControl{}, nil }
	okAgent := func(context.Context, *guestvm.Prepared) (io.Closer, error) { return vmFakeAgent{}, nil }
	cases := map[string]struct {
		ops  vmNICOperations
		want error
	}{
		"launch error": {vmNICOperations{launch: func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) { return nil, boom }, control: okControl, agent: okAgent}, boom},
		"launch nil":   {vmNICOperations{launch: func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) { return nil, nil }, control: okControl, agent: okAgent}, guestprocess.ErrOwnershipUncertain},
		"launch error with process": {vmNICOperations{launch: func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) {
			return newFakeProcess(), boom
		}, control: okControl, agent: okAgent}, boom},
		"not ready": {vmNICOperations{launch: func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) {
			p := newFakeProcess()
			p.ready = func() error { return boom }
			return readyFailProcess{p}, nil
		}, control: okControl, agent: okAgent}, boom},
		"control error":        {vmNICOperations{launch: okLaunch, control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) { return nil, boom }, agent: okAgent}, boom},
		"control error closes": {vmNICOperations{launch: okLaunch, control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) { return &vmFakeControl{}, boom }, agent: okAgent}, boom},
		"control nil":          {vmNICOperations{launch: okLaunch, control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) { return nil, nil }, agent: okAgent}, errGuestLease},
		"already running": {vmNICOperations{launch: okLaunch, control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) {
			return &statusControl{status: qmp.Status{Running: true, State: "running"}}, nil
		}, agent: okAgent}, qmp.ErrNotPaused},
		"status error": {vmNICOperations{launch: okLaunch, control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) {
			return &statusControl{err: boom}, nil
		}, agent: okAgent}, boom},
		"agent error":        {vmNICOperations{launch: okLaunch, control: okControl, agent: func(context.Context, *guestvm.Prepared) (io.Closer, error) { return nil, boom }}, boom},
		"agent error closes": {vmNICOperations{launch: okLaunch, control: okControl, agent: func(context.Context, *guestvm.Prepared) (io.Closer, error) { return vmFakeAgent{}, boom }}, boom},
		"agent nil":          {vmNICOperations{launch: okLaunch, control: okControl, agent: func(context.Context, *guestvm.Prepared) (io.Closer, error) { return nil, nil }}, errGuestLease},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b, c, lease, passive, ctx := heldVMLease(t)
			receipt, err := c.launchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(3*time.Second), tc.ops)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if receipt == nil || receipt.Ready() {
				t.Fatal("failed startup published readiness or lost cleanup debt")
			}
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err = receipt.WaitCleanup(cleanup)
			if name == "launch error" || name == "launch nil" {
				// No owned process was established: ownership stays an
				// unresolved debt, never a successful join.
				if err == nil {
					t.Fatal("unowned launch reported clean cleanup")
				}
			} else if err != nil {
				t.Fatal("cleanup not joined:", err)
			}
		})
	}
}

func TestLaunchVMAndAttachRejectsInvalidAdmission(t *testing.T) {
	b, c, lease, passive, ctx := heldVMLease(t)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	ops := b.ops
	if _, err := c.launchVMAndAttach(ctx, expired, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(time.Second), ops); !errors.Is(err, errGuestLease) {
		t.Fatal("expired lifetime admitted", err)
	}
	if _, err := c.launchVMAndAttach(ctx, context.Background(), lease, b.Prepared(), b.descriptor, passive, time.Now().Add(time.Second), ops); !errors.Is(err, errGuestLease) {
		t.Fatal("unbounded lifetime admitted", err)
	}
	pending := lease
	pending.State = "pending"
	if _, err := c.launchVMAndAttach(ctx, ctx, pending, b.Prepared(), b.descriptor, passive, time.Now().Add(time.Second), ops); !errors.Is(err, errGuestLease) {
		t.Fatal("non-held lease admitted", err)
	}
	fenced := lease
	fenced.Intent.Fence += "-stale"
	if _, err := c.launchVMAndAttach(ctx, ctx, fenced, b.Prepared(), b.descriptor, passive, time.Now().Add(time.Second), ops); !errors.Is(err, errGuestLease) {
		t.Fatal("stale fence admitted", err)
	}
	var nilCoordinator *EC2GuestAttachmentCoordinator
	if _, err := nilCoordinator.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(time.Second)); !errors.Is(err, errGuestLease) {
		t.Fatal("nil coordinator launched", err)
	}
	other := cloneVMDescriptor(b.descriptor)
	other.Config.Paths = []string{"/other"}
	if _, err := c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), other, passive, time.Now().Add(time.Second)); !errors.Is(err, errGuestLease) {
		t.Fatal("caller descriptor replaced backend policy", err)
	}
}

// A shorter admission deadline bounds startup without ending the owned lifetime.
func TestLaunchVMAndAttachUsesEarlierAdmissionDeadline(t *testing.T) {
	b, c, lease, passive, ctx := heldVMLease(t)
	admission, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ops := vmNICOperations{
		launch:  func(context.Context, *guestvm.Prepared, [6]byte) (vmNICProcess, error) { return newFakeProcess(), nil },
		control: func(context.Context, *guestvm.Prepared) (vmNICControl, error) { return &vmFakeControl{}, nil },
		agent:   func(context.Context, *guestvm.Prepared) (io.Closer, error) { return vmFakeAgent{}, nil },
	}
	receipt, err := c.launchVMAndAttach(admission, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(3*time.Second), ops)
	if err != nil || !receipt.Ready() {
		t.Fatal("bounded admission startup failed", err)
	}
	cancel()
	if !receipt.Ready() {
		t.Fatal("admission cancellation ended the owned lifetime")
	}
	if err = receipt.Seal(ctx); err != nil {
		t.Fatal(err)
	}
}

func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// tcpFrame builds a checksummed Ethernet/IPv4/TCP SYN frame.
func tcpFrame(src, dst netip.Addr, sport, dport uint16) []byte {
	b := make([]byte, 54)
	copy(b[:12], []byte{2, 0, 0, 0, 0, 2, 2, 0, 0, 0, 0, 1})
	binary.BigEndian.PutUint16(b[12:], 0x0800)
	ip := b[14:]
	ip[0], ip[8], ip[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(ip[2:], 40)
	s, d := src.As4(), dst.As4()
	copy(ip[12:16], s[:])
	copy(ip[16:20], d[:])
	binary.BigEndian.PutUint16(ip[10:], checksum(ip[:20]))
	tcp := ip[20:]
	binary.BigEndian.PutUint16(tcp, sport)
	binary.BigEndian.PutUint16(tcp[2:], dport)
	binary.BigEndian.PutUint32(tcp[4:], 1)
	tcp[12], tcp[13] = 5<<4, 2
	binary.BigEndian.PutUint16(tcp[14:], 4096)
	pseudo := append(append([]byte(nil), ip[12:20]...), 0, 6, 0, 20)
	binary.BigEndian.PutUint16(tcp[16:], checksum(append(pseudo, tcp...)))
	return b
}

// Transact admits a frame only for the exact descriptor and the live security
// group policy; link-local metadata and non-TCP frames bypass SG ingress.
func TestVMNICTransactEnforcesDescriptorAndSecurityGroup(t *testing.T) {
	b, c, lease, passive, ctx := heldVMLease(t)
	receipt, err := c.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, passive, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	receipt.mu.Lock()
	r := receipt.resource
	receipt.mu.Unlock()
	d := r.PersistentDescriptor()
	nic := d.Config.NIC
	accept := func(dir guesttcp.FrameDirection, frame []byte, obs *guesttcp.ProbeHTTPObservation) guesttcp.OwnedProbeAcceptance {
		return guesttcp.OwnedProbeAcceptance{NIC: nic, Direction: dir, Frame: frame, Observation: obs, Descriptor: &d}
	}
	applied := 0
	apply := func() error { applied++; return nil }
	if err = r.Transact(nil, accept(guesttcp.ToGuest, nil, nil), apply); !errors.Is(err, errGuestLease) {
		t.Fatal("nil context admitted")
	}
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, nil, nil), nil); !errors.Is(err, errGuestLease) {
		t.Fatal("nil apply admitted")
	}
	wrong := cloneVMDescriptor(d)
	wrong.Config.Paths = []string{"/other"}
	if err = r.Transact(ctx, guesttcp.OwnedProbeAcceptance{NIC: nic, Descriptor: &wrong}, apply); !errors.Is(err, errGuestLease) {
		t.Fatal("foreign descriptor admitted")
	}
	allowed := tcpFrame(nic.PeerIP, nic.LocalIP, 80, 40000)
	if err = r.Transact(ctx, accept(guesttcp.FromGuest, allowed, nil), apply); err != nil {
		t.Fatal("SG-allowed reply denied:", err)
	}
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, tcpFrame(nic.LocalIP, nic.PeerIP, 40000, 8080), nil), apply); err != nil {
		t.Fatal("SG-allowed probe denied:", err)
	}
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, tcpFrame(nic.LocalIP, nic.PeerIP, 40000, 22), nil), apply); !errors.Is(err, errGuestLease) {
		t.Fatal("SG-denied port admitted:", err)
	}
	metadata := tcpFrame(nic.PeerIP, d.Passive.LocalIP, 40001, 22)
	if err = r.Transact(ctx, accept(guesttcp.FromGuest, metadata, nil), apply); err != nil {
		t.Fatal("link-local metadata treated as public ingress:", err)
	}
	if err = r.Transact(ctx, accept(guesttcp.FromGuest, []byte{0xff, 0xff}, nil), apply); err != nil {
		t.Fatal("trusted non-TCP frame denied:", err)
	}
	obs := &guesttcp.ProbeHTTPObservation{Route: guestnet.TCPRoute{PeerPort: 80}}
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, nil, obs), apply); err != nil {
		t.Fatal("observation on allowed port denied:", err)
	}
	obs.Route.PeerPort = 9999
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, nil, obs), apply); !errors.Is(err, errGuestLease) {
		t.Fatal("observation on closed port admitted")
	}
	if applied != 5 {
		t.Fatalf("applied %d admitted frames, want 5", applied)
	}
	network := r.network
	r.network = nil
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, allowed, nil), apply); !errors.Is(err, errGuestLease) {
		t.Fatal("frame admitted without network policy")
	}
	r.network = network
	if err = receipt.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	if err = r.Transact(ctx, accept(guesttcp.ToGuest, allowed, nil), apply); !errors.Is(err, errGuestLease) {
		t.Fatal("sealed resource admitted a frame")
	}
	if _, err = r.Read(make([]byte, 1)); !errors.Is(err, guesttcp.ErrClosed) {
		t.Fatal("sealed resource read")
	}
	if _, err = r.Write([]byte{1}); !errors.Is(err, guesttcp.ErrClosed) {
		t.Fatal("sealed resource write")
	}
}

// The canonical operations perform real dial/exec work; against absent
// sockets and an inert non-executable emulator they must fail, not hang.
func TestCanonicalVMNICOperationsFailClosedWithoutVM(t *testing.T) {
	b, _, _, _, _ := heldVMLease(t)
	ops := canonicalVMNICOperations()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if c, err := ops.control(ctx, b.Prepared()); err == nil || c != nil {
		t.Fatal("QMP control opened without a VM", err)
	}
	if a, err := ops.agent(ctx, b.Prepared()); err == nil || a != nil {
		t.Fatal("QGA agent opened without a VM", err)
	}
	if p, err := ops.launch(context.Background(), b.Prepared(), [6]byte{2, 0, 0, 0, 0, 2}); err == nil || p != nil {
		if p != nil {
			_ = p.Close()
		}
		t.Fatal("inert emulator launched", err)
	}
	if !strings.Contains(b.Prepared().QMPSocket(), b.directory) && !strings.HasPrefix(b.Prepared().QMPSocket(), "/") {
		t.Fatal("QMP socket outside private runtime directory")
	}
}
