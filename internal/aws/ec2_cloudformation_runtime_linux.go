//go:build linux

package aws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
)

// EC2InstanceRuntimeConfig is trusted, explicitly installed operator runtime
// configuration. No image/template identifier can create it through HTTP.
type EC2InstanceRuntimeConfig struct {
	Lifetime  context.Context
	Bundle    *guestbundle.Bundle
	Layout    guestvm.Layout
	Directory string
	// Image IDs are explicit aliases for this exact pinned bundle, not downloads.
	SessionImageSHA256 string // Explicit reviewed relay-image pin; empty disables guest PTY channel.
	ImageIDs           []string
	OnReady            func(*EC2VMProbeLaunch) error
}
type EC2InstancePlan struct {
	runtime                                                                              *EC2InstanceRuntime
	canonical                                                                            string
	StackID, Region, LogicalID, InstanceID, SubnetID, ProfileName, ImageID, InstanceType string
	SecurityGroupIDs                                                                     []string
	UserData                                                                             []byte
}
type EC2InstanceRuntime struct {
	commandBackend *CanonicalSSMCommandBackend
	shutting       bool
	launches       int
	launchesDone   chan struct{}
	sessionBackend *CanonicalSSMSessionBackend
	operations     vmNICOperations
	db             *sql.DB
	network        *EC2NetworkStore
	config         EC2InstanceRuntimeConfig
	mu             sync.Mutex
	retained       map[string]*ec2InstanceRuntimeEntry
}

type ec2InstanceRuntimeEntry struct {
	sealed      bool
	plan        EC2InstancePlan
	backend     *EC2VMBackend
	network     *EC2NetworkLease
	coordinator *EC2GuestAttachmentCoordinator
	receipt     *EC2VMProbeLaunch
	done        chan struct{}
}

func NewEC2InstanceRuntime(db *sql.DB, network *EC2NetworkStore, config EC2InstanceRuntimeConfig) (*EC2InstanceRuntime, error) {
	if db == nil || network == nil || config.Lifetime == nil || config.Bundle == nil || len(config.ImageIDs) == 0 || config.OnReady == nil {
		return nil, errGuestLease
	}
	d, ok := config.Lifetime.Deadline()
	if !ok || !time.Now().Before(d) || d.After(time.Now().Add(guesttcp.MaxPassiveLifetime)) {
		return nil, errGuestLease
	}
	if config.SessionImageSHA256 != "" {
		if err := guestvm.ValidateSessionImage(config.Bundle, config.Layout, config.SessionImageSHA256); err != nil {
			return nil, err
		}
	}
	config.ImageIDs = append([]string(nil), config.ImageIDs...)
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_instance_runtime(region TEXT NOT NULL,stack_id TEXT NOT NULL,instance TEXT NOT NULL,plan TEXT NOT NULL,state TEXT NOT NULL,PRIMARY KEY(region,instance),UNIQUE(region,stack_id))`)
	if err != nil {
		return nil, err
	}
	return &EC2InstanceRuntime{operations: canonicalVMNICOperations(), db: db, network: network, config: config, retained: map[string]*ec2InstanceRuntimeEntry{}}, nil
}

// Prepare is pure validation/planning. It creates no VM and performs no SQL.
func (r *EC2InstanceRuntime) Prepare(ctx context.Context, stack StackRecord, resource StackResource) (*EC2InstancePlan, error) {
	if r == nil || ctx == nil || ctx.Err() != nil || resource.Type != "AWS::EC2::Instance" {
		return nil, errGuestLease
	}
	r.mu.Lock()
	shutting := r.shutting
	r.mu.Unlock()
	if shutting {
		return nil, errGuestLease
	}
	p := resource.Properties
	for key := range p {
		switch key {
		case "ImageId", "InstanceType", "IamInstanceProfile", "SubnetId", "SecurityGroupIds", "Tags", "UserData":
		default:
			return nil, fmt.Errorf("unsupported EC2 instance property %s", key)
		}
	}
	str := func(key string) (string, error) {
		s, ok := p[key].(string)
		if !ok || s == "" {
			return "", fmt.Errorf("invalid EC2 instance %s", key)
		}
		return s, nil
	}
	image, e := str("ImageId")
	if e != nil {
		return nil, e
	}
	allowed := false
	for _, v := range r.config.ImageIDs {
		allowed = allowed || v == image
	}
	if !allowed {
		return nil, fmt.Errorf("image is not configured in pinned runtime")
	}
	kind, e := str("InstanceType")
	if e != nil {
		return nil, e
	}
	if kind != "t3.micro" && kind != "t3.small" {
		return nil, fmt.Errorf("unsupported instance type")
	}
	subnet, e := str("SubnetId")
	if e != nil {
		return nil, e
	}
	profile, e := str("IamInstanceProfile")
	if e != nil {
		return nil, e
	}
	raw, e := str("UserData")
	if e != nil {
		return nil, e
	}
	userdata, e := base64.StdEncoding.Strict().DecodeString(raw)
	if e != nil || len(userdata) > 16384 {
		return nil, fmt.Errorf("invalid bounded EC2 UserData")
	}
	values, ok := p["SecurityGroupIds"].([]any)
	if !ok || len(values) < 1 || len(values) > 5 {
		return nil, fmt.Errorf("invalid security groups")
	}
	groups := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		s, ok := v.(string)
		if !ok || s == "" || seen[s] {
			return nil, fmt.Errorf("invalid security group")
		}
		seen[s] = true
		groups = append(groups, s)
	}
	hash := sha256.Sum256([]byte(stack.ID + "\x00" + resource.LogicalID))
	id := "i-" + hex.EncodeToString(hash[:])[:17]
	if resource.PhysicalID != "" && resource.PhysicalID != id {
		return nil, errGuestLease
	}
	plan := &EC2InstancePlan{StackID: stack.ID, Region: stack.Region, LogicalID: resource.LogicalID, InstanceID: id, SubnetID: subnet, ProfileName: profile, ImageID: image, InstanceType: kind, SecurityGroupIDs: groups, UserData: append([]byte(nil), userdata...)}
	plan.runtime = r
	canonical, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	plan.canonical = string(canonical)
	return plan, nil
}

// Launch MUST run after the CFN transaction commits CREATE_IN_PROGRESS. It
// reserves once, performs canonical acquisition outside SQL, and never reports
// successful creation until attachment, activation and private QGA are ready.
func (r *EC2InstanceRuntime) Launch(ctx context.Context, plan *EC2InstancePlan) (receipt *EC2VMProbeLaunch, resultErr error) {
	if r == nil || plan == nil || ctx == nil || plan.runtime != r {
		return nil, errGuestLease
	}
	captured := *plan
	captured.SecurityGroupIDs = append([]string(nil), plan.SecurityGroupIDs...)
	captured.UserData = append([]byte(nil), plan.UserData...)
	plan = &captured
	raw, e := json.Marshal(plan)
	if e == nil && string(raw) != plan.canonical {
		return nil, errGuestLease
	}
	if e != nil {
		return nil, e
	}
	r.mu.Lock()
	if r.shutting {
		r.mu.Unlock()
		return nil, errGuestLease
	}
	r.launches++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.launches--
		if r.shutting && r.launches == 0 && r.launchesDone != nil {
			close(r.launchesDone)
			r.launchesDone = nil
		}
		r.mu.Unlock()
	}()
	reservation, e := r.db.ExecContext(ctx, `INSERT INTO aws_ec2_instance_runtime(region,stack_id,instance,plan,state) SELECT ?,?,?,?,'launch_pending' WHERE (SELECT count(*) FROM aws_ec2_instance_runtime)<?`, plan.Region, plan.StackID, plan.InstanceID, string(raw), guestLeaseLimit)
	if e != nil {
		return nil, e
	}
	reserved, e := reservation.RowsAffected()
	if e != nil || reserved != 1 {
		return nil, errors.Join(e, errGuestLease)
	}
	entry := &ec2InstanceRuntimeEntry{plan: *plan, done: make(chan struct{})}
	r.mu.Lock()
	r.retained[plan.InstanceID] = entry
	shutdownDuringReserve := r.shutting
	r.mu.Unlock()
	defer close(entry.done)
	defer func() {
		if resultErr != nil {
			_ = r.Seal(ctx, plan.Region, plan.StackID, plan.InstanceID)
		}
	}()
	if shutdownDuringReserve {
		return nil, ErrEC2GuestAttachmentPending
	}
	owner := EC2GuestOwner{Account: "000000000000", Region: plan.Region, Instance: plan.InstanceID, Deployment: plan.StackID, ProfileName: plan.ProfileName}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	profile, e := iamProfileRead(ctx, tx, plan.ProfileName, false)
	if e != nil {
		tx.Rollback()
		return nil, e
	}
	owner.ProfileID = profile.InstanceProfileID
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	var backend *EC2VMBackend
	var gate sync.Mutex
	sealed := false
	network, e := r.network.LeaseFixedPolicy(ctx, plan.Region, plan.StackID, plan.SubnetID, plan.SecurityGroupIDs, func() {
		gate.Lock()
		sealed = true
		b := backend
		gate.Unlock()
		if b != nil {
			b.SealNIC()
		}
	})
	if e != nil {
		return nil, e
	}
	r.mu.Lock()
	entry.network = network
	r.mu.Unlock()
	var snapshot EC2NetworkSnapshot
	if e = network.Transact(ctx, func(s EC2NetworkSnapshot) error { snapshot = s; return nil }); e != nil {
		return nil, e
	}
	descriptor, seed, e := instanceNICDescriptor(owner, snapshot)
	if e != nil {
		network.Close()
		return nil, e
	}
	b, e := NewEC2VMBackend(r.config.Lifetime, r.db, owner, r.config.Bundle, r.config.Layout, r.config.Directory, plan.UserData, seed, descriptor)
	if e != nil {
		network.Close()
		return nil, e
	}
	if r.config.SessionImageSHA256 != "" {
		b.prepare = func(ctx context.Context, intent guestlease.Intent) (*guestvm.Prepared, error) {
			return guestvm.PrepareWithNetworkAndSession(ctx, b.directory, b.bundle, b.layout, intent, b.userdata, b.network, r.config.SessionImageSHA256)
		}
	}
	gate.Lock()
	backend = b
	wasSealed := sealed
	gate.Unlock()
	if wasSealed {
		b.SealNIC()
		return nil, errGuestLease
	}
	b.networkLease = network
	b.ops = r.operations
	r.mu.Lock()
	entry.backend = b
	runtimeSealed := entry.sealed
	r.mu.Unlock()
	if runtimeSealed {
		b.SealNIC()
		network.Close()
		return nil, ErrEC2GuestAttachmentPending
	}
	store, e := NewEC2GuestLeaseStore(r.db, b)
	if e != nil {
		network.Close()
		return nil, e
	}
	lease, e := store.Acquire(ctx, owner)
	if e != nil {
		return nil, e
	}
	lease, e = store.Reconcile(ctx, lease)
	if e != nil {
		return nil, e
	}
	var supervisor [32]byte
	if _, e = rand.Read(supervisor[:]); e != nil {
		return nil, e
	}
	coordinator, e := NewEC2GuestAttachmentCoordinator(store, hex.EncodeToString(supervisor[:]))
	if e != nil {
		return nil, e
	}
	r.mu.Lock()
	entry.coordinator = coordinator
	r.mu.Unlock()
	deadline, _ := r.config.Lifetime.Deadline()
	passive, e := newEC2MetadataService(r.config.Lifetime, descriptor, owner, deadline)
	if e != nil {
		b.SealNIC()
		return nil, e
	}
	receipt, e = coordinator.LaunchVMAndAttach(ctx, r.config.Lifetime, lease, b.Prepared(), descriptor, passive, deadline)
	if receipt != nil {
		r.mu.Lock()
		entry.receipt = receipt
		r.mu.Unlock()
	}
	if e != nil {
		known, transferred := receipt.PassiveOwnership()
		if known && !transferred {
			e = errors.Join(e, passive.Close())
		}
		return receipt, e
	}
	r.mu.Lock()
	wasSealed = entry.sealed
	r.mu.Unlock()
	if wasSealed || !receipt.Ready() {
		return receipt, ErrEC2GuestAttachmentPending
	}
	if e = r.config.OnReady(receipt); e != nil {
		_ = receipt.Seal(ctx)
		return receipt, e
	}
	result, e := r.db.ExecContext(ctx, `UPDATE aws_ec2_instance_runtime SET state='ready' WHERE region=? AND instance=? AND stack_id=? AND plan=? AND state='launch_pending'`, plan.Region, plan.InstanceID, plan.StackID, string(raw))
	if e != nil {
		_ = receipt.Seal(ctx)
		return receipt, e
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		_ = receipt.Seal(ctx)
		return receipt, errors.Join(e, errGuestLease)
	}
	r.mu.Lock()
	wasSealed = entry.sealed
	r.mu.Unlock()
	if wasSealed || !receipt.Ready() {
		return receipt, ErrEC2GuestAttachmentPending
	}
	return receipt, nil
}
func (r *EC2InstanceRuntime) Seal(ctx context.Context, region, stackID, instance string) error {
	r.mu.Lock()
	entry := r.retained[instance]
	if entry == nil || entry.plan.Region != region || entry.plan.StackID != stackID {
		r.mu.Unlock()
		return ErrEC2GuestAttachmentPending
	}
	entry.sealed = true
	receipt, backend, network := entry.receipt, entry.backend, entry.network
	r.mu.Unlock()
	if backend != nil {
		backend.SealNIC()
	}
	if network != nil {
		network.Close()
	}
	_, journalErr := r.db.ExecContext(ctx, `UPDATE aws_ec2_instance_runtime SET state='seal_pending' WHERE region=? AND instance=? AND stack_id=?`, region, instance, stackID)
	if receipt != nil {
		return errors.Join(journalErr, receipt.Seal(ctx))
	}
	return errors.Join(journalErr, ErrEC2GuestAttachmentPending)
}
func (r *EC2InstanceRuntime) WaitCleanup(ctx context.Context, region, stackID, instance string) error {
	_ = r.Seal(ctx, region, stackID, instance)
	r.mu.Lock()
	entry := r.retained[instance]
	r.mu.Unlock()
	if entry == nil || entry.plan.Region != region || entry.plan.StackID != stackID {
		return ErrEC2GuestAttachmentPending
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-entry.done:
	}
	r.mu.Lock()
	receipt, backend := entry.receipt, entry.backend
	r.mu.Unlock()
	if receipt != nil {
		return receipt.WaitCleanup(ctx)
	}
	if backend == nil {
		return ErrEC2GuestAttachmentPending
	}
	backend.mu.Lock()
	process := backend.process
	backend.mu.Unlock()
	if process == nil {
		return ErrEC2GuestAttachmentPending
	}
	return process.WaitCleanup(ctx)
}
func instanceNICDescriptor(owner EC2GuestOwner, s EC2NetworkSnapshot) (guesttcp.OwnedProbeDescriptor, guestseed.IPv4Network, error) {
	prefix, e := netip.ParsePrefix(s.SubnetCIDR)
	if e != nil || !prefix.Addr().Is4() {
		return guesttcp.OwnedProbeDescriptor{}, guestseed.IPv4Network{}, errGuestLease
	}
	// One VM per original bounded stack. The fourth address is a deterministic
	// simulator allocation, not an arbitrary address accepted from guest input.
	guest := prefix.Masked().Addr().Next().Next().Next().Next()
	gateway, e := netip.ParseAddr(s.GatewayIPv4)
	if e != nil || !prefix.Contains(guest) {
		return guesttcp.OwnedProbeDescriptor{}, guestseed.IPv4Network{}, errGuestLease
	}
	h := sha256.Sum256([]byte(owner.Deployment + "\x00" + owner.Instance))
	mac := [6]byte{2, h[0], h[1], h[2], h[3], h[4]}
	local := [6]byte{2, h[5], h[6], h[7], h[8], h[9]}
	nic := guestnet.ProbeNIC{LocalIP: netip.MustParseAddr("198.51.100.2"), PeerIP: guest, LocalMAC: local, PeerMAC: mac, ServicePorts: []uint16{80, 8080}}
	binding := guesttcp.HTTPProbeScopeBinding{Owner: sha256.Sum256([]byte(owner.Account)), Deployment: sha256.Sum256([]byte(owner.Deployment)), Lease: sha256.Sum256([]byte(owner.Instance)), BackendIncarnation: [32]byte{1}}
	d := guesttcp.OwnedProbeDescriptor{Config: guesttcp.OwnedProbeConfig{NIC: nic, Paths: []string{"/", "/healthz"}, EvidenceLimit: 128}, Binding: binding, Services: []guesttcp.HTTPProbeService{{Name: "frontend", Port: 80, Path: "/"}, {Name: "api", Port: 8080, Path: "/healthz"}}, Passive: guestnet.TCPRoute{LocalIP: netip.MustParseAddr("169.254.169.254"), PeerIP: guest, LocalMAC: local, PeerMAC: mac, LocalPort: 80, AnyPeerPort: true}, ARP: &guestnet.ARPConfig{GuestIP: guest, GatewayIP: gateway, GuestMAC: mac, GatewayMAC: local}}
	seed := guestseed.IPv4Network{MACAddress: fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]), Address: fmt.Sprintf("%s/%d", guest, prefix.Bits()), Gateway: gateway.String()}
	return d, seed, nil
}

// Shutdown seals every retained entry before observing joins. Any unknown debt
// remains an error; callers must not close the shared DB or discard this runtime
// when this method reports incomplete cleanup.
func (r *EC2InstanceRuntime) Shutdown(ctx context.Context) error {
	if r == nil || ctx == nil {
		return errGuestLease
	}
	r.mu.Lock()
	r.shutting = true
	if r.launchesDone == nil {
		r.launchesDone = make(chan struct{})
		if r.launches == 0 {
			close(r.launchesDone)
		}
	}
	launchesDone := r.launchesDone
	commands := r.commandBackend
	sessions := r.sessionBackend
	plans := make([]EC2InstancePlan, 0, len(r.retained))
	for _, e := range r.retained {
		plans = append(plans, e.plan)
	}
	r.mu.Unlock()
	var result error
	for _, p := range plans {
		err := r.Seal(ctx, p.Region, p.StackID, p.InstanceID)
		if err != nil && !errors.Is(err, ErrEC2GuestAttachmentPending) {
			result = errors.Join(result, err)
		}
	}
	select {
	case <-launchesDone:
	case <-ctx.Done():
		return errors.Join(result, ctx.Err())
	}
	r.mu.Lock()
	plans = plans[:0]
	for _, e := range r.retained {
		plans = append(plans, e.plan)
	}
	r.mu.Unlock()
	for _, p := range plans {
		result = errors.Join(result, r.WaitCleanup(ctx, p.Region, p.StackID, p.InstanceID))
	}
	if commands != nil {
		result = errors.Join(result, commands.Shutdown(ctx))
	}
	if sessions != nil {
		result = errors.Join(result, sessions.WaitCleanup(ctx))
	}
	return result
}

// publicAMIParameterPrefix is the AWS public Amazon Linux AMI alias namespace.
const publicAMIParameterPrefix = "/aws/service/ami-amazon-linux-latest/"

// PublicImageParameter maps AWS public Amazon Linux AMI aliases to the first
// configured pinned image. It never selects an unconfigured or remote image.
func (r *EC2InstanceRuntime) PublicImageParameter(name string) (string, bool) {
	if r == nil || len(r.config.ImageIDs) == 0 || !strings.HasPrefix(name, publicAMIParameterPrefix) || len(name) == len(publicAMIParameterPrefix) {
		return "", false
	}
	return r.config.ImageIDs[0], true
}
