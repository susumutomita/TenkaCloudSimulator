//go:build linux || darwin

package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"reflect"
	"slices"
)

// VerifyCompletedGuest checks a retained runtime receipt against its canonical
// CloudFormation generation. It is a read-only readiness check, not a permit to
// perform later I/O. An actual connector must still use the live owner fence.
func (s *StackStore) VerifyCompletedGuest(ctx context.Context, source *EC2VMProbeLaunch) error {
	return s.verifyCompletedGuest(ctx, source, nil)
}

// The peer check runs inside the authenticated NIC's network read fence. Its
// arguments are recovered from the retained resource, never from client input.
type ec2GuestNetworkCheck func(EC2GuestOwner, EC2NetworkSnapshot, netip.Addr) error

func (s *StackStore) verifyCompletedGuest(ctx context.Context, source *EC2VMProbeLaunch, check ec2GuestNetworkCheck) error {
	if s == nil || ctx == nil || ctx.Err() != nil || !source.Ready() {
		return errGuestLease
	}
	runtime, ok := s.instanceRuntime.(*EC2InstanceRuntime)
	if !ok || runtime == nil || runtime.db != s.db {
		return errGuestLease
	}
	issuance, err := source.Issuance()
	if err != nil {
		return errGuestLease
	}
	owner := issuance.Lease.Owner
	value, ok := s.instanceAttempts.Load(owner.Deployment)
	if !ok {
		return errGuestLease // Never adopt a persisted stack without its original attempt.
	}
	attempt := value.(*stackInstanceAttempt)
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	run, ok := attempt.attempt.(*ec2StackAttempt)
	if attempt.revoked || !ok || run.runtime != runtime || run.plan == nil ||
		run.plan.StackID != owner.Deployment || run.plan.Region != owner.Region ||
		run.plan.InstanceID != owner.Instance || attempt.resource.PhysicalID != owner.Instance {
		return errGuestLease
	}

	runtime.mu.Lock()
	commands := runtime.commandBackend
	runtime.mu.Unlock()
	if commands == nil || commands.runtime != runtime {
		return errGuestLease
	}
	// Reuse the canonical runtime's retained pointer, exact ready plan and held
	// lease readback. Run Command need not be dispatched to verify these facts.
	target, lease, err := commands.target(ctx, owner.Region, owner.Instance)
	if err != nil || target.receipt != source || lease != issuance.Lease {
		return errGuestLease
	}
	var raw []byte
	err = s.db.QueryRowContext(ctx, `
		SELECT s.document FROM aws_cfn_stacks s
		JOIN aws_cfn_ownership o ON o.stack_id=s.id
		WHERE s.region=? AND s.id=? AND o.namespace=?
		AND o.kind='AWS::EC2::Instance' AND o.physical_id=?`,
		owner.Region, owner.Deployment, stackResourceNamespace(owner.Region, "AWS::EC2::Instance"),
		owner.Instance).Scan(&raw)
	var stack StackRecord
	if err != nil || json.Unmarshal(raw, &stack) != nil || stack.Status != "CREATE_COMPLETE" ||
		stack.ID != owner.Deployment || stack.Region != owner.Region {
		return errGuestLease
	}
	instances := 0
	for _, res := range stack.Resources {
		if res.Type == "AWS::EC2::Instance" {
			instances++
			if res.PhysicalID != owner.Instance || res.LogicalID != attempt.resource.LogicalID {
				return errGuestLease
			}
		}
	}
	if instances != 1 || target.plan.canonical != run.plan.canonical ||
		runtime.verifyGuestNetworkMembership(ctx, target, issuance, check) != nil ||
		ctx.Err() != nil || !commands.current(target) {
		return errGuestLease
	}
	return nil
}

// ec2GuestNetworkMembership identifies provider-owned topology, not a route
// permit. Identical CIDRs in different VPCs or deployments are not equivalent.
type ec2GuestNetworkMembership struct {
	region, stack, vpc, subnet, generation string
}

func (s EC2NetworkSnapshot) membership() ec2GuestNetworkMembership {
	return ec2GuestNetworkMembership{s.Region, s.StackID, s.vpcID, s.SubnetID, s.Seal}
}

// verifyGuestNetworkMembership reads the exact retained network lease through
// its mutation fence. It grants neither ingress nor access to a backend. Future
// traffic still needs admission inside the actual live owner and policy fence.
func (r *EC2InstanceRuntime) verifyGuestNetworkMembership(ctx context.Context, target *ec2InstanceRuntimeEntry, issuance EC2GuestIssuance, check ec2GuestNetworkCheck) error {
	if ctx == nil || ctx.Err() != nil || target == nil {
		return errGuestLease
	}
	owner := issuance.Lease.Owner
	r.mu.Lock()
	lease, source, backend, plan := target.network, target.receipt, target.backend, target.plan
	current := !r.shutting && !target.sealed && r.retained[owner.Instance] == target
	r.mu.Unlock()
	if !current || source == nil || r.network == nil || r.network.db != r.db || lease == nil ||
		lease.store != r.network || backend == nil || backend.networkLease != lease || backend.owner != owner {
		return errGuestLease
	}
	source.mu.Lock()
	resource, ready := source.resource, source.ready && !source.sealed
	source.mu.Unlock()
	if !ready || resource == nil || resource.network != lease || resource.issuance != issuance ||
		issuance.BackendIncarnation == ([32]byte{}) || resource.sealed.Load() {
		return errGuestLease
	}
	groups := slices.Clone(plan.SecurityGroupIDs)
	slices.Sort(groups)
	return lease.Transact(ctx, func(snapshot EC2NetworkSnapshot) error {
		member := snapshot.membership()
		if member != lease.snapshot.membership() || member.region != owner.Region || member.stack != owner.Deployment ||
			member.vpc == "" || member.subnet != plan.SubnetID || !guestLeaseToken.MatchString(member.generation) ||
			!slices.Equal(groups, snapshot.SecurityGroupIDs) {
			return errGuestLease
		}
		// Rebuild only the canonical NIC descriptor, not the VM or its network.
		// This check applies equally to private subnets; no public route is needed.
		nic, _, err := instanceNICDescriptor(owner, snapshot)
		nic.Binding.BackendIncarnation = issuance.BackendIncarnation
		if err != nil || !reflect.DeepEqual(nic, resource.descriptor) {
			return errGuestLease
		}
		policy, err := json.Marshal(nic)
		digest := sha256.Sum256(policy)
		if err != nil || issuance.PolicyGeneration != hex.EncodeToString(digest[:]) || resource.sealed.Load() || ctx.Err() != nil {
			return errGuestLease
		}
		if check != nil {
			if err := check(owner, snapshot, nic.Config.NIC.PeerIP); err != nil {
				return err
			}
			// Mutation pre-seals before waiting for this gate. Do not return
			// success after the check if teardown has begun on either receipt.
			if !lease.Active() || !source.Ready() || ctx.Err() != nil {
				return errGuestLease
			}
		}
		return nil
	})
}
