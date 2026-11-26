package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// StackInstanceRuntime is installed only by trusted server composition. An HTTP
// template cannot construct or replace it. Prepare must be side-effect free;
// Launch must return success only after the actual backend readiness receipt.
type StackInstanceRuntime interface {
	PrepareStackInstance(context.Context, StackRecord, StackResource) (StackInstanceAttempt, error)
}
type StackInstanceAttempt interface {
	Launch(context.Context) error
	Seal(context.Context) error
	WaitCleanup(context.Context) error
}
type stackInstanceAttempt struct {
	mu       sync.Mutex
	revoked  bool
	attempt  StackInstanceAttempt
	resource StackResource
}

// ConfigureInstanceRuntime is startup-only, before this store serves requests.
func (s *StackStore) ConfigureInstanceRuntime(runtime StackInstanceRuntime) error {
	if runtime == nil || s.instanceRuntime != nil {
		return fmt.Errorf("instance runtime must be configured exactly once")
	}
	s.instanceRuntime = runtime
	return nil
}

func (s *StackStore) prepareStackInstance(ctx context.Context, record StackRecord) (*stackInstanceAttempt, error) {
	var selected *StackResource
	for _, resource := range record.Resources {
		if resource.Type != "AWS::EC2::Instance" {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("only one local instance per stack is supported")
		}
		copy := resource
		selected = &copy
	}
	if selected == nil {
		return nil, nil
	}
	if s.instanceRuntime == nil {
		return nil, ssmFailure{"NotImplemented", "A qualified local instance runtime is not configured."}
	}
	attempt, err := s.instanceRuntime.PrepareStackInstance(ctx, record, *selected)
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		return nil, fmt.Errorf("instance runtime returned no attempt")
	}
	return &stackInstanceAttempt{attempt: attempt, resource: *selected}, nil
}

// Placement is deliberately local to the stack: granting RunInstances on an
// ARN is not proof that the stack declared or owns the underlying network.
func validateStackInstancePlacement(ctx context.Context, tx *sql.Tx, record StackRecord, instance StackResource) error {
	invalid := ssmFailure{"ValidationError", "Instance requires a subnet and security groups in the same VPC owned by this stack."}
	owned := func(kind, id string) (EC2NetworkResource, error) {
		var declaration *StackResource
		for i := range record.Resources {
			r := &record.Resources[i]
			if r.Type == kind && r.PhysicalID == id && id != "" {
				if declaration != nil {
					return EC2NetworkResource{}, invalid
				}
				declaration = r
			}
		}
		if declaration == nil {
			return EC2NetworkResource{}, invalid
		}
		state, err := networkRead(ctx, tx, record.Region, id)
		if errors.Is(err, sql.ErrNoRows) {
			return state, invalid
		}
		if err != nil {
			return state, err
		}
		if state.Kind != kind || state.StackID != record.ID || state.VPCID != stringValue(declaration.Properties["VpcId"]) {
			return state, invalid
		}
		var claimed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=? AND stack_id=?)`,
			stackResourceNamespace(record.Region, kind), kind, id, record.ID).Scan(&claimed); err != nil {
			return state, err
		}
		if !claimed {
			return state, invalid
		}
		return state, nil
	}
	subnet, err := owned("AWS::EC2::Subnet", stringValue(instance.Properties["SubnetId"]))
	if err != nil {
		return err
	}
	if _, err = owned("AWS::EC2::VPC", subnet.VPCID); err != nil {
		return err
	}
	groups, ok := instance.Properties["SecurityGroupIds"].([]any)
	if !ok || len(groups) == 0 || len(groups) > 5 {
		return invalid
	}
	seen := map[string]bool{}
	for _, raw := range groups {
		id := stringValue(raw)
		if seen[id] {
			return invalid
		}
		seen[id] = true
		group, err := owned(ec2SecurityGroupKind, id)
		if err != nil {
			return err
		}
		if group.VPCID != subnet.VPCID {
			return invalid
		}
	}
	return nil
}

func stackStatus(record StackRecord) string {
	if record.Status == "" {
		return "CREATE_COMPLETE"
	} // Legacy atomic records only.
	return record.Status
}

func (s *StackStore) launchStackInstance(ctx context.Context, record StackRecord, entry *stackInstanceAttempt) error {
	err := entry.attempt.Launch(ctx)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		sealErr := entry.attempt.Seal(cleanupCtx)
		cleanupErr := entry.attempt.WaitCleanup(cleanupCtx)
		cancel()
		if finishErr := s.finishStackInstance(record, entry, false); finishErr != nil {
			return errors.Join(err, sealErr, cleanupErr, finishErr)
		}
		return errors.Join(err, sealErr, cleanupErr)
	}
	if err = s.finishStackInstance(record, entry, true); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		return errors.Join(err, entry.attempt.Seal(cleanupCtx), entry.attempt.WaitCleanup(cleanupCtx))
	}
	return nil
}

// Finalization is a new, short SQL transaction. No guest launch/readiness or
// cleanup wait is performed while SQLite is locked. A delete revokes the entry
// before shutdown so a late readiness result cannot complete that generation.
func (s *StackStore) finishStackInstance(record StackRecord, entry *stackInstanceAttempt, success bool) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.revoked {
		return fmt.Errorf("instance stack attempt revoked")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	records, err := stackRecords(tx, record.Region, record.Name)
	if err != nil {
		return err
	}
	if len(records) != 1 || records[0].ID != record.ID || records[0].Status != "CREATE_IN_PROGRESS" {
		return fmt.Errorf("stack incarnation or phase changed")
	}
	saved := records[0]
	status := "CREATE_FAILED"
	if success {
		status = "CREATE_COMPLETE"
	}
	saved.Status = status
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE aws_cfn_stacks SET document=? WHERE region=? AND name=? AND id=?`, string(raw), saved.Region, saved.Name, saved.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("stack finalization not persisted")
	}
	if err = appendStackEvent(tx, saved, &entry.resource, status); err != nil {
		return err
	}
	if err = appendStackEvent(tx, saved, nil, status); err != nil {
		return err
	}
	var stored string
	if err = tx.QueryRow("SELECT document FROM aws_cfn_stacks WHERE region=? AND name=? AND id=?", saved.Region, saved.Name, saved.ID).Scan(&stored); err != nil {
		return err
	}
	if stored != string(raw) {
		return fmt.Errorf("stack finalization readback differs")
	}
	return tx.Commit()
}

func (s *StackStore) sealStackInstance(ctx context.Context, record StackRecord) error {
	hasInstance := false
	for _, resource := range record.Resources {
		if resource.Type == "AWS::EC2::Instance" {
			hasInstance = true
		}
	}
	if !hasInstance {
		return nil
	}
	value, ok := s.instanceAttempts.Load(record.ID)
	if !ok {
		return fmt.Errorf("instance cleanup requires retained runtime ownership; no automatic crash adoption")
	}
	entry := value.(*stackInstanceAttempt)
	entry.mu.Lock()
	entry.revoked = true
	entry.mu.Unlock()
	if err := entry.attempt.Seal(ctx); err != nil {
		return err
	}
	return entry.attempt.WaitCleanup(ctx)
}
