//go:build linux

package aws

import (
	"context"
	"testing"
)

// Reuse the real StackStore lifecycle and the inert VM fixture. None of these
// admission decisions creates a route, contacts a database service or starts QEMU.
func testCompletedGuestNetworkMembership(t *testing.T, ctx context.Context, stacks *StackStore, runtime *EC2InstanceRuntime, source *EC2VMProbeLaunch, peer *EC2NetworkLease, privateSubnet bool) {
	t.Helper()
	issued, err := source.Issuance()
	if err != nil {
		t.Fatal(err)
	}
	owner := issued.Lease.Owner
	runtime.mu.Lock()
	entry := runtime.retained[owner.Instance]
	lease := entry.network
	runtime.mu.Unlock()
	require := func(t *testing.T, allowed bool) {
		t.Helper()
		if err := stacks.VerifyCompletedGuest(ctx, source); (err == nil) != allowed {
			t.Fatalf("network membership admission: want %v, got %v", allowed, err)
		}
	}
	t.Run("retained owned VPC and subnet", func(t *testing.T) {
		require(t, true)
		if lease.snapshot.PublicRoute == privateSubnet {
			t.Fatal("fixture did not reproduce the expected public/private route policy")
		}
		member := lease.snapshot.membership()
		if member.vpc == "" || member.subnet != entry.plan.SubnetID || member.stack != owner.Deployment {
			t.Fatal("missing owned topology identity")
		}
	})
	t.Run("different retained network lease", func(t *testing.T) {
		if peer == nil || !peer.Active() || peer.snapshot.StackID == owner.Deployment {
			t.Fatal("fixture must supply a distinct active lease")
		}
		runtime.mu.Lock()
		entry.network = peer
		runtime.mu.Unlock()
		defer func() {
			runtime.mu.Lock()
			entry.network = lease
			runtime.mu.Unlock()
		}()
		require(t, false)
	})
	require(t, true)
	t.Run("same IPv4 topology with stale generation", func(t *testing.T) {
		// Controlled storage fault to verify fail-closed behavior even when no
		// NIC seal callback reported it. No address or route is changed.
		tx, err := runtime.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		vpc, err := networkRead(ctx, tx, owner.Region, lease.snapshot.vpcID)
		if err != nil {
			t.Fatal(err)
		}
		vpc.Revision++
		if err := networkWrite(ctx, tx, vpc, false); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if !source.Ready() || !lease.Active() {
			t.Fatal("readiness flags changed; test did not isolate topology readback")
		}
		require(t, false)
	})
}
