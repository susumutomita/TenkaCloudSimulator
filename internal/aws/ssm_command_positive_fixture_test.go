//go:build linux

package aws

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
)

// Parameterized test-only copy: every host process/control operation is replaced
// before Acquire, as in vmBackendFixture. Owner is fixed before construction.
func positiveCommandVMFixture(t *testing.T, ownerBuilder func(*sql.DB) EC2GuestOwner) (*EC2VMBackend, *EC2GuestLeaseStore, *EC2GuestAttachmentCoordinator, *vmFakeProcess, *vmFakeControl, *atomic.Int32, context.Context) {
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
	owner := ownerBuilder(db.Database())
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
	backend, e := NewEC2VMBackend(ctx, db.Database(), owner, bundle, layout, dir, []byte("# original fixture bytes\n"), guestseed.IPv4Network{MACAddress: "02:00:00:00:00:02", Address: "10.0.1.4/24", Gateway: "10.0.1.1"}, descriptor)
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
