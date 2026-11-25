//go:build linux || darwin

package aws

import (
	"context"
	"encoding/json"
)

// VerifyCompletedGuest checks a retained runtime receipt against its canonical
// CloudFormation generation. It is a read-only readiness check, not a permit to
// perform later I/O. An actual connector must still use the live owner fence.
func (s *StackStore) VerifyCompletedGuest(ctx context.Context, source *EC2VMProbeLaunch) error {
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
	if instances != 1 || ctx.Err() != nil || !commands.current(target) {
		return errGuestLease
	}
	return nil
}
