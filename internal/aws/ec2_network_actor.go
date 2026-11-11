package aws

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
)

// Actors are shared by constructors using the same authoritative *sql.DB.
// A host must not construct independent DB pools for one live network authority.
var ec2NetworkActors sync.Map

type ec2NetworkActorKey struct {
	db            *sql.DB
	region, owner string
}
type ec2NetworkActor struct {
	gate    sync.RWMutex
	mu      sync.Mutex
	pending int
	leases  map[*EC2NetworkLease]struct{}
}
type EC2NetworkLease struct {
	store       *EC2NetworkStore
	actor       *ec2NetworkActor
	snapshot    EC2NetworkSnapshot
	sealed      atomic.Bool
	sealNIC     func()
	fixedPolicy bool
}

func networkActor(db *sql.DB, region, owner string) *ec2NetworkActor {
	key := ec2NetworkActorKey{db, region, owner}
	v, _ := ec2NetworkActors.LoadOrStore(key, &ec2NetworkActor{leases: map[*EC2NetworkLease]struct{}{}})
	return v.(*ec2NetworkActor)
}

// BeginMutation seals NIC admission before waiting for active frame callbacks.
// The returned release must run after commit/rollback. Never acquire this guard
// while holding a SQLite transaction. sealNIC must be non-blocking and must not
// acquire this gate; it seals the actual packet actor, not merely a flag here.
func (s *EC2NetworkStore) BeginMutation(region, owner string) func() {
	release, _ := s.beginMutation(region, owner, false)
	return release
}
func (s *EC2NetworkStore) beginMutation(region, owner string, policyChange bool) (func(), error) {
	a := networkActor(s.db, region, owner)
	a.mu.Lock()
	if policyChange {
		for lease := range a.leases {
			if lease.fixedPolicy && !lease.sealed.Load() {
				a.mu.Unlock()
				return nil, fmt.Errorf("live NIC policy updates are unsupported")
			}
		}
	}
	a.pending++
	callbacks := []func(){}
	for lease := range a.leases {
		if lease.sealed.CompareAndSwap(false, true) {
			callbacks = append(callbacks, lease.sealNIC)
		}
	}
	a.mu.Unlock()
	for _, seal := range callbacks {
		seal()
	}
	a.gate.Lock()
	var once sync.Once
	return func() { once.Do(func() { a.mu.Lock(); a.pending--; a.mu.Unlock(); a.gate.Unlock() }) }, nil
}
func (s *EC2NetworkStore) Lease(ctx context.Context, region, owner, subnet string, groups []string, sealNIC func()) (*EC2NetworkLease, error) {
	return s.lease(ctx, region, owner, subnet, groups, sealNIC, false)
}

// LeaseFixedPolicy is mandatory for a live backend that cannot refresh policy
// without killing the guest. Policy updates reject before any revocation/write;
// intentional owner deletion still seals and joins the actual NIC.
func (s *EC2NetworkStore) LeaseFixedPolicy(ctx context.Context, region, owner, subnet string, groups []string, sealNIC func()) (*EC2NetworkLease, error) {
	return s.lease(ctx, region, owner, subnet, groups, sealNIC, true)
}
func (s *EC2NetworkStore) lease(ctx context.Context, region, owner, subnet string, groups []string, sealNIC func(), fixedPolicy bool) (*EC2NetworkLease, error) {
	if sealNIC == nil {
		return nil, fmt.Errorf("actual NIC seal callback required")
	}
	a := networkActor(s.db, region, owner)
	a.gate.RLock()
	defer a.gate.RUnlock()
	snap, e := s.Snapshot(ctx, region, owner, subnet, groups)
	if e != nil {
		return nil, e
	}
	l := &EC2NetworkLease{store: s, actor: a, snapshot: snap, sealNIC: sealNIC, fixedPolicy: fixedPolicy}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending != 0 {
		return nil, fmt.Errorf("network mutation in progress")
	}
	a.leases[l] = struct{}{}
	return l, nil
}
func (l *EC2NetworkLease) Close() {
	if l.sealed.CompareAndSwap(false, true) {
		l.sealNIC()
	}
	l.actor.mu.Lock()
	delete(l.actor.leases, l)
	l.actor.mu.Unlock()
}
func (l *EC2NetworkLease) Active() bool { return !l.sealed.Load() }

// Transact holds the authoritative actor gate across the actual frame/observation
// callback, but its consistency read releases the SQL transaction before calling
// it. Mutation pre-seals the NIC and then joins this gate. A stale lease cannot
// revive merely because a route/rule is changed back to its previous value.
func (l *EC2NetworkLease) Transact(ctx context.Context, fn func(EC2NetworkSnapshot) error) error {
	if fn == nil {
		return fmt.Errorf("network callback required")
	}
	l.actor.gate.RLock()
	defer l.actor.gate.RUnlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if l.sealed.Load() {
		return fmt.Errorf("network lease sealed")
	}
	current, e := l.store.Snapshot(ctx, l.snapshot.Region, l.snapshot.StackID, l.snapshot.SubnetID, l.snapshot.SecurityGroupIDs)
	if e != nil {
		return e
	}
	if current.Seal != l.snapshot.Seal {
		return fmt.Errorf("network state changed")
	}
	if l.sealed.Load() {
		return fmt.Errorf("network lease sealed")
	}
	return fn(current)
}

// ChangeIngress is trusted provider mutation, not a public IAM bypass. Callers
// supply authorization performed on the exact resource while its transaction is
// locked. Preflight denials and invalid rules do not seal. Once admitted, a
// failed commit still leaves the previous generation sealed (fail closed).
func (s *EC2NetworkStore) ChangeIngress(ctx context.Context, region, owner, id string, rules []EC2IngressRule, authorize func(string, string) error) error {
	raw := []any{}
	for _, rule := range rules {
		raw = append(raw, map[string]any{"IpProtocol": rule.Protocol, "FromPort": rule.FromPort, "ToPort": rule.ToPort, "CidrIp": rule.CIDR})
	}
	normalized, e := networkRules(raw)
	if e != nil {
		return e
	}
	if e := s.preflightMutation(ctx, region, owner, id, "AWS::EC2::SecurityGroup", []string{"ec2:RevokeSecurityGroupIngress", "ec2:AuthorizeSecurityGroupIngress"}, authorize); e != nil {
		return e
	}
	release, err := s.beginMutation(region, owner, true)
	if err != nil {
		return err
	}
	defer release()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := networkRead(ctx, tx, region, id)
	if e != nil {
		return e
	}
	if r.Kind != "AWS::EC2::SecurityGroup" || r.StackID != owner || authorize == nil {
		return fmt.Errorf("invalid network mutation authority")
	}
	for _, action := range []string{"ec2:RevokeSecurityGroupIngress", "ec2:AuthorizeSecurityGroupIngress"} {
		if e = authorize(action, networkARN(region, "security-group", id)); e != nil {
			return e
		}
	}
	r.Ingress = normalized
	r.Revision++
	if e = networkWrite(ctx, tx, r, false); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *EC2NetworkStore) ChangeDefaultRoute(ctx context.Context, region, owner, id string, enabled bool, authorize func(string, string) error) error {
	preAction := "ec2:DeleteRoute"
	if enabled {
		preAction = "ec2:CreateRoute"
	}
	if e := s.preflightMutation(ctx, region, owner, id, "AWS::EC2::Route", []string{preAction}, authorize); e != nil {
		return e
	}
	release, err := s.beginMutation(region, owner, true)
	if err != nil {
		return err
	}
	defer release()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := networkRead(ctx, tx, region, id)
	if e != nil {
		return e
	}
	if r.Kind != "AWS::EC2::Route" || r.StackID != owner || authorize == nil {
		return fmt.Errorf("invalid network mutation authority")
	}
	action := "ec2:DeleteRoute"
	if enabled {
		action = "ec2:CreateRoute"
	}
	if e = authorize(action, networkARN(region, "route-table", r.RouteTableID)); e != nil {
		return e
	}
	r.RouteEnabled = enabled
	r.Revision++
	if e = networkWrite(ctx, tx, r, false); e != nil {
		return e
	}
	return tx.Commit()
}

func (s *EC2NetworkStore) preflightMutation(ctx context.Context, region, owner, id, kind string, actions []string, authorize func(string, string) error) error {
	if authorize == nil {
		return fmt.Errorf("network authorization required")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := networkRead(ctx, tx, region, id)
	if e != nil {
		return e
	}
	if r.StackID != owner || r.Kind != kind {
		return fmt.Errorf("network mutation identity mismatch")
	}
	resource := networkARN(region, "security-group", id)
	if kind == "AWS::EC2::Route" {
		resource = networkARN(region, "route-table", r.RouteTableID)
	}
	for _, action := range actions {
		if e = authorize(action, resource); e != nil {
			return e
		}
	}
	return tx.Commit()
}
