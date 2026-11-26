//go:build linux && amd64

package catalog

import (
	"context"
	"database/sql"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

// ProbeGuestPeer is provider composition only. match is the EC2 provider's
// read-only topology join, evaluated before and after the fixed host probe in
// committed snapshots of the exact catalog generation. Neither the callback
// nor a destination is selected by a request.
// A successful synthetic host probe is not a permit for later traffic.
func (c *OwnedPrivatePostgres) ProbeGuestPeer(ctx context.Context, db *sql.DB, account, region string, match func(context.Context, *sql.Tx, RDSPrivatePostgresRefs) error) error {
	if c == nil || ctx == nil || ctx.Err() != nil || db == nil || match == nil ||
		c.engine == nil || c.engine.store.db != db || c.engine.cfg.Account != account ||
		c.options.Region != region || !c.mu.TryLock() {
		return rdsruntime.ErrNotReady
	}
	defer c.mu.Unlock()
	if c.closed || c.debt || !c.used || c.child == nil {
		return rdsruntime.ErrNotReady
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !c.currentMatching(bounded, match) {
		// A valid workload may correctly refuse this guest. Never retire it,
		// disclose its identity, or issue a transferable ticket on a denial.
		return rdsruntime.ErrNotReady
	}
	// No SQLite transaction spans the original host's fixed SCRAM probe.
	if err := c.child.Probe(bounded); err != nil {
		return err
	}
	// The owner mutex protects the process, not independently managed
	// destination dependencies. Read them again before reporting observation.
	if !c.currentMatching(bounded, match) || bounded.Err() != nil {
		return rdsruntime.ErrNotReady
	}
	return nil
}
