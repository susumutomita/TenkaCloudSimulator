package guesttcp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// TestProbeSoakPastLifetimeCaps is opt-in, long-running evidence that scoped
// probes on one persistent NIC keep working past the former per-NIC lifetime
// caps (1024 tuples, 1024 retained handles, 65536 attachment frames). Running
// out of tuples or handles is now temporary, so the loop waits and retries. It drives
// real frames through the in-memory gVisor peer on the real clock, so tuple
// quarantine and handle retention elapse for real. Run it with, for example:
//
//	TENKACLOUD_PROBE_SOAK=30m go test -run TestProbeSoakPastLifetimeCaps -v -timeout 40m ./internal/workload/guesttcp
func TestProbeSoakPastLifetimeCaps(t *testing.T) {
	soak, err := time.ParseDuration(os.Getenv("TENKACLOUD_PROBE_SOAK"))
	if err != nil || soak <= 0 {
		t.Skip("set TENKACLOUD_PROBE_SOAK to a duration to run the soak")
	}
	saved := probeHTTPFixtureLifetime
	probeHTTPFixtureLifetime = soak + 2*time.Minute
	t.Cleanup(func() { probeHTTPFixtureLifetime = saved })
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	frames := func() int {
		f.transport.nic.base.budgetMu.Lock()
		defer f.transport.nic.base.budgetMu.Unlock()
		return f.transport.nic.base.packets
	}
	started := time.Now()
	end := started.Add(soak)
	succeeded, waits, totalFrames := 0, 0, 0
	services := []string{"web", "health"}
	for i := 1; time.Now().Before(end); i++ {
		before := frames()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		h, err := s.Start(ctx, operationKey(uint64(i)), services[i%2])
		if errors.Is(err, ErrCapacity) {
			// Every retained handle is still inside its retention: temporary.
			cancel()
			waits++
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if err != nil {
			cancel()
			t.Fatalf("probe %d after %d successes: start refused: %v", i, succeeded, err)
		}
		got := operationSettled(t, h)
		cancel()
		for drained := false; !drained; {
			select {
			case <-f.requests:
			default:
				drained = true
			}
		}
		after := frames()
		if after >= before {
			totalFrames += after - before
		} else { // a new budget window started during this probe
			totalFrames += after
		}
		switch {
		case got.ResultError == nil && got.Observation.StatusCode == 200:
			succeeded++
		case errors.Is(got.ResultError, guestnet.ErrProbeCapacity):
			// Every tuple is live or quarantined: temporary, wait and retry.
			waits++
			time.Sleep(250 * time.Millisecond)
		default:
			t.Fatalf("probe %d after %d successes failed: %+v", i, succeeded, got)
		}
		if i%500 == 0 {
			t.Logf("%s: %d probes ok, %d capacity waits, %d attachment frames", time.Since(started).Round(time.Second), succeeded, waits, totalFrames)
		}
	}
	t.Logf("done after %s: %d probes ok, %d capacity waits, %d attachment frames; transport alive=%v",
		time.Since(started).Round(time.Second), succeeded, waits, totalFrames, f.transport.Lifetime().Err() == nil)
	if succeeded <= guestnet.MaxProbeSessions {
		t.Fatalf("soak did not pass the old 1024 probe cap: %d", succeeded)
	}
	if f.transport.Lifetime().Err() != nil {
		t.Fatal("attachment ended during the soak", f.transport.WaitContext(context.Background()))
	}
}
