//go:build linux

package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
)

// CanonicalSSMCommandBackend persists at-most-once target reservations before
// any guest-exec. Durable reservations without process-local receipts are never
// resumed. It owns no host shell and never opens a replacement QGA connection.
type CanonicalSSMCommandBackend struct {
	runtime    *EC2InstanceRuntime
	mu         sync.Mutex
	closed     bool
	calls      int
	callsDone  chan struct{}
	jobs       map[string]*canonicalCommandJob
	dispatches map[string]int
}
type canonicalCommandJob struct {
	intent guestcommand.DurableIntent
	done   chan struct{}
	err    error
	handle guestcommand.Handle
	owner  *canonicalCommandOwner
}

// A single Driver owns the captured ready QGA client for this physical owner.
// Known exited commands release Driver admission; uncertain commands quarantine
// it. The submit gate encloses the actual guest-exec RPC, not just metadata.
type canonicalCommandOwner struct {
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	resource  *ec2VMNICResource
	driver    *guestcommand.Driver
	gate      sync.Mutex
}

func (o *canonicalCommandOwner) submit(ctx context.Context, c qga.Command, budget time.Duration) (guestcommand.Handle, guestcommand.Snapshot, error) {
	o.gate.Lock()
	defer o.gate.Unlock()
	if o.resource.sealed.Load() || ctx.Err() != nil {
		return guestcommand.Handle{}, guestcommand.Snapshot{}, ErrSSMCommandUnknown
	}
	return o.driver.Start(ctx, c, budget)
}
func (o *canonicalCommandOwner) requestSeal() {
	o.closeOnce.Do(func() {
		go func() { o.closeErr = o.driver.Close(); o.gate.Lock(); o.gate.Unlock(); close(o.closeDone) }()
	})
}

func (r *EC2VMProbeLaunch) commandOwner() (*canonicalCommandOwner, error) {
	r.mu.Lock()
	resource, agent := r.resource, r.agent
	ready := r.ready && !r.sealed
	r.mu.Unlock()
	if !ready || resource == nil {
		return nil, ErrSSMCommandUnknown
	}
	resource.commandMu.Lock()
	defer resource.commandMu.Unlock()
	if resource.sealed.Load() {
		return nil, ErrSSMCommandUnknown
	}
	if resource.command != nil {
		return resource.command, nil
	}
	client, ok := agent.(*qga.Client)
	if !ok {
		return nil, ErrSSMCommandUnknown
	}
	driver, err := guestcommand.New(client)
	if err != nil {
		return nil, err
	}
	resource.command = &canonicalCommandOwner{resource: resource, driver: driver, closeDone: make(chan struct{})}
	return resource.command, nil
}
func NewCanonicalSSMCommandBackend(runtime *EC2InstanceRuntime) (*CanonicalSSMCommandBackend, error) {
	if runtime == nil || runtime.db == nil {
		return nil, ErrSSMCommandInvalid
	}
	_, err := runtime.db.Exec(`CREATE TABLE IF NOT EXISTS aws_canonical_commands(token TEXT PRIMARY KEY,intent TEXT NOT NULL,observation TEXT NOT NULL)`)
	if err != nil {
		return nil, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.shutting {
		return nil, ErrSSMCommandUnknown
	}
	if runtime.commandBackend == nil {
		runtime.commandBackend = &CanonicalSSMCommandBackend{runtime: runtime, jobs: map[string]*canonicalCommandJob{}}
	}
	return runtime.commandBackend, nil
}

// ResolveCommandTarget returns the current authoritative lease of the retained
// instance, never a caller-supplied deployment or role-to-stack inference.
func (b *CanonicalSSMCommandBackend) ResolveCommandTarget(ctx context.Context, region, instance string) (EC2GuestLease, error) {
	_, lease, err := b.target(ctx, region, instance)
	return lease, err
}
func (b *CanonicalSSMCommandBackend) target(ctx context.Context, region, instance string) (*ec2InstanceRuntimeEntry, EC2GuestLease, error) {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return nil, EC2GuestLease{}, ErrSSMCommandInvalid
	}
	b.runtime.mu.Lock()
	e := b.runtime.retained[instance]
	if b.runtime.shutting || e == nil || e.sealed || e.plan.Region != region || e.receipt == nil || e.backend == nil || !e.receipt.Ready() {
		b.runtime.mu.Unlock()
		return nil, EC2GuestLease{}, ErrSSMCommandUnknown
	}
	canonical, stack := e.plan.canonical, e.plan.StackID
	issuance, err := e.receipt.Issuance()
	b.runtime.mu.Unlock()
	if err != nil {
		return nil, EC2GuestLease{}, err
	}
	tx, err := b.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, EC2GuestLease{}, err
	}
	defer tx.Rollback()
	var state, plan string
	if err = tx.QueryRowContext(ctx, `SELECT state,plan FROM aws_ec2_instance_runtime WHERE region=? AND stack_id=? AND instance=?`, region, stack, instance).Scan(&state, &plan); err != nil || state != "ready" || plan != canonical {
		return nil, EC2GuestLease{}, ErrSSMCommandUnknown
	}
	lease, _, err := guestLeaseRead(ctx, tx, issuance.Lease.Owner)
	if err != nil || lease != issuance.Lease || lease.State != "held" {
		return nil, EC2GuestLease{}, ErrSSMCommandUnknown
	}
	if err = tx.Commit(); err != nil {
		return nil, EC2GuestLease{}, err
	}
	if !b.current(e) {
		return nil, EC2GuestLease{}, ErrSSMCommandUnknown
	}
	return e, lease, nil
}
func (b *CanonicalSSMCommandBackend) current(e *ec2InstanceRuntimeEntry) bool {
	b.runtime.mu.Lock()
	defer b.runtime.mu.Unlock()
	return !b.runtime.shutting && !e.sealed && b.runtime.retained[e.plan.InstanceID] == e && e.receipt.Ready()
}
func (b *CanonicalSSMCommandBackend) admittedIntent(ctx context.Context, i guestcommand.DurableIntent) (SSMCommandTarget, error) {
	var raw string
	if err := b.runtime.db.QueryRowContext(ctx, `SELECT record FROM aws_ssm_commands WHERE id=?`, i.Command).Scan(&raw); err != nil {
		return SSMCommandTarget{}, err
	}
	var command SSMCommand
	if len(raw) > ssmCommandBytes || json.Unmarshal([]byte(raw), &command) != nil || !ssmRecordValid(command) || ssmEncode(command) != raw {
		return SSMCommandTarget{}, ErrSSMCommandInvalid
	}
	for _, target := range command.Targets {
		if target.Intent == i {
			return target, nil
		}
	}
	return SSMCommandTarget{}, ErrSSMCommandInvalid
}
func unknownCommand(i guestcommand.DurableIntent, started int64) guestcommand.DurableObservation {
	return guestcommand.DurableObservation{Intent: i, State: "Unknown", StartedAt: started, CleanupRequired: true}
}
func (b *CanonicalSSMCommandBackend) Dispatch(ctx context.Context, i guestcommand.DurableIntent) error {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return ErrSSMCommandUnknown
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrSSMCommandUnknown
	}
	b.calls++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.calls--
		if b.closed && b.calls == 0 && b.callsDone != nil {
			close(b.callsDone)
			b.callsDone = nil
		}
		b.mu.Unlock()
	}()
	target, err := b.admittedIntent(ctx, i)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.dispatches == nil {
		b.dispatches = map[string]int{}
	}
	b.dispatches[i.Token]++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.dispatches[i.Token]--
		if b.dispatches[i.Token] == 0 {
			delete(b.dispatches, i.Token)
		}
		b.mu.Unlock()
	}()
	tx, err := b.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT intent FROM aws_canonical_commands WHERE token=?`, i.Token).Scan(&existing)
	if err == nil {
		if existing != ssmEncode(i) {
			return ErrSSMCommandInvalid
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	initial := guestcommand.DurableObservation{Intent: i, State: "Unknown"}
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_canonical_commands SELECT ?,?,? WHERE (SELECT count(*) FROM aws_canonical_commands)<1024`, i.Token, ssmEncode(i), ssmEncode(initial))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrSSMCommandUnknown
	}
	var checkIntent, checkObs string
	if err = tx.QueryRowContext(ctx, `SELECT intent,observation FROM aws_canonical_commands WHERE token=?`, i.Token).Scan(&checkIntent, &checkObs); err != nil || checkIntent != ssmEncode(i) || checkObs != ssmEncode(initial) {
		return ErrSSMCommandUnknown
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrSSMCommandUnknown, err)
	}
	// No SQL transaction crosses the authoritative owner fence or guest RPC.
	e, lease, err := b.target(ctx, target.Lease.Owner.Region, target.Lease.Owner.Instance)
	if err != nil || lease != target.Lease {
		return ErrSSMCommandUnknown
	}
	owner, err := e.receipt.commandOwner()
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrSSMCommandUnknown
	}
	job := &canonicalCommandJob{intent: i, owner: owner, done: make(chan struct{})}
	b.jobs[i.Token] = job
	b.mu.Unlock()
	go b.run(job, e)
	return nil
}
func (b *CanonicalSSMCommandBackend) persist(ctx context.Context, o guestcommand.DurableObservation) error {
	if !ssmObservationValid(o, o.Intent) {
		return ErrSSMCommandInvalid
	}
	tx, err := b.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var intent, raw string
	if err = tx.QueryRowContext(ctx, `SELECT intent,observation FROM aws_canonical_commands WHERE token=?`, o.Intent.Token).Scan(&intent, &raw); err != nil || intent != ssmEncode(o.Intent) {
		return ErrSSMCommandUnknown
	}
	var old guestcommand.DurableObservation
	if json.Unmarshal([]byte(raw), &old) != nil || !ssmObservationValid(old, o.Intent) || ssmEncode(old) != raw {
		return ErrSSMCommandUnknown
	}
	if ssmTerminal(old) {
		if old != o {
			return ErrSSMCommandUnknown
		}
		return tx.Commit()
	}
	if old.StartedAt != 0 && o.StartedAt != old.StartedAt {
		return ErrSSMCommandUnknown
	}
	next := ssmEncode(o)
	result, err := tx.ExecContext(ctx, `UPDATE aws_canonical_commands SET observation=? WHERE token=? AND intent=? AND observation=?`, next, o.Intent.Token, intent, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrSSMCommandUnknown
	}
	var check string
	if err = tx.QueryRowContext(ctx, `SELECT observation FROM aws_canonical_commands WHERE token=?`, o.Intent.Token).Scan(&check); err != nil || check != next {
		return ErrSSMCommandUnknown
	}
	return tx.Commit()
}
func (b *CanonicalSSMCommandBackend) run(job *canonicalCommandJob, e *ec2InstanceRuntimeEntry) {
	defer close(job.done)
	i := job.intent
	e.receipt.mu.Lock()
	lifetime, resource := e.receipt.lifetime, e.receipt.resource
	e.receipt.mu.Unlock()
	end := time.UnixMilli(i.RequestedAt).Add(time.Duration(i.TimeoutSeconds) * time.Second)
	if ownerEnd, ok := lifetime.Deadline(); !ok {
		job.err = ErrSSMCommandUnknown
		return
	} else if ownerEnd.Before(end) {
		end = ownerEnd
	}
	ctx, cancel := context.WithDeadline(lifetime, end)
	defer cancel()
	var scripts []string
	if json.Unmarshal([]byte(i.ScriptJSON), &scripts) != nil || ctx.Err() != nil || !b.current(e) {
		job.err = ErrSSMCommandUnknown
		return
	}
	started := time.Now().UnixMilli()
	if started < i.RequestedAt {
		job.err = ErrSSMCommandUnknown
		return
	}
	handle, snapshot, err := job.owner.submit(ctx, qga.Command{Path: "/bin/bash", Args: []string{"-c", strings.Join(scripts, "\n")}}, time.Until(end))
	job.handle = handle
	if (snapshot.State == guestcommand.Rejected && !snapshot.CleanupRequired) || errors.Is(err, guestcommand.ErrBusy) || errors.Is(err, guestcommand.ErrInvalidTimeout) {
		job.err = b.persist(lifetime, guestcommand.DurableObservation{Intent: i, State: "Rejected", FinishedAt: time.Now().UnixMilli()})
		return
	}
	if snapshot.State != guestcommand.Accepted || err != nil {
		job.err = errors.Join(err, b.persist(lifetime, unknownCommand(i, started)))
		_ = resource.Close()
		return
	}
	stop := context.AfterFunc(ctx, func() { _, _ = job.owner.driver.RequestCancellation(handle) })
	defer stop()
	if err = b.persist(lifetime, guestcommand.DurableObservation{Intent: i, State: "InProgress", StartedAt: started}); err != nil {
		job.err = err
		_ = resource.Close()
		return
	}
	for n := 0; n < guestcommand.MaxPolls; n++ {
		timer := time.NewTimer(guestcommand.MinPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			job.err = b.persist(lifetime, unknownCommand(i, started))
			_ = resource.Close()
			return
		case <-timer.C:
		}
		snapshot, err = job.owner.driver.Poll(ctx, handle)
		if snapshot.State == guestcommand.Accepted && err == nil {
			continue
		}
		if snapshot.State == guestcommand.Exited && err == nil && !snapshot.CleanupRequired {
			o := guestcommand.DurableObservation{Intent: i, State: "Failed", StartedAt: started, FinishedAt: time.Now().UnixMilli()}
			result := snapshot.Result
			if result.ExitCode != nil && *result.ExitCode >= 0 && *result.ExitCode <= 255 {
				o.ExitCode = int(*result.ExitCode)
			} else if result.Signal != nil && *result.Signal > 0 && *result.Signal < 128 {
				o.ExitCode = 128 + int(*result.Signal)
			} else {
				job.err = b.persist(lifetime, unknownCommand(i, started))
				_ = resource.Close()
				return
			}
			if !utf8.Valid(result.Stdout) || !utf8.Valid(result.Stderr) {
				job.err = b.persist(lifetime, unknownCommand(i, started))
				_ = resource.Close()
				return
			}
			o.Stdout, o.Stderr = string(result.Stdout), string(result.Stderr)
			for _, text := range []*string{&o.Stdout, &o.Stderr} {
				if len(*text) > ssmCommandOutput {
					o.OutputTruncated = true
					*text = (*text)[:ssmCommandOutput]
					for !utf8.ValidString(*text) {
						*text = (*text)[:len(*text)-1]
					}
				}
			}
			if o.ExitCode == 0 && !o.OutputTruncated {
				o.State = "Success"
			}
			job.err = b.persist(lifetime, o)
			if job.err != nil {
				_ = resource.Close()
			}
			return
		}
		job.err = errors.Join(err, b.persist(lifetime, unknownCommand(i, started)))
		_ = resource.Close()
		return
	}
	job.err = errors.Join(guestcommand.ErrPollLimit, b.persist(lifetime, unknownCommand(i, started)))
	_ = resource.Close()
}

// Observe never polls/reaps QGA, starts work, retries reservations, or infers exit
// from a DB row. It returns only the validated recorded worker observation.
func (b *CanonicalSSMCommandBackend) Observe(ctx context.Context, i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return unknownCommand(i, 0), ErrSSMCommandUnknown
	}
	var intent, raw string
	if err := b.runtime.db.QueryRowContext(ctx, `SELECT intent,observation FROM aws_canonical_commands WHERE token=?`, i.Token).Scan(&intent, &raw); err != nil {
		return unknownCommand(i, 0), err
	}
	var o guestcommand.DurableObservation
	if intent != ssmEncode(i) || len(raw) > ssmCommandBytes || json.Unmarshal([]byte(raw), &o) != nil || !ssmObservationValid(o, i) || ssmEncode(o) != raw {
		return unknownCommand(i, 0), ErrSSMCommandUnknown
	}
	// A nonterminal recorded state cannot imply a live worker after restart.
	b.mu.Lock()
	job, retained := b.jobs[i.Token]
	dispatching := b.dispatches[i.Token] > 0
	b.mu.Unlock()
	if retained && !ssmTerminal(o) {
		select {
		case <-job.done:
			if job.err != nil {
				return unknownCommand(i, o.StartedAt), nil
			}
		default:
		}
	}
	if !retained && !dispatching && !ssmTerminal(o) {
		return unknownCommand(i, o.StartedAt), nil
	}
	return o, nil
}
func (b *CanonicalSSMCommandBackend) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrSSMCommandInvalid
	}
	b.mu.Lock()
	b.closed = true
	if b.callsDone == nil {
		b.callsDone = make(chan struct{})
		if b.calls == 0 {
			close(b.callsDone)
		}
	}
	callsDone := b.callsDone
	b.mu.Unlock()
	select {
	case <-callsDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	jobs := make([]*canonicalCommandJob, 0, len(b.jobs))
	for _, job := range b.jobs {
		jobs = append(jobs, job)
	}
	b.mu.Unlock()
	var result error
	for _, job := range jobs {
		job.owner.requestSeal()
	}
	for _, job := range jobs {
		select {
		case <-job.owner.closeDone:
			result = errors.Join(result, job.owner.closeErr)
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
	}
	for _, job := range jobs {
		select {
		case <-job.done:
			result = errors.Join(result, job.err)
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
	}
	return result
}

var _ guestcommand.DurableBackend = (*CanonicalSSMCommandBackend)(nil)

// NewCanonicalSSMCommandStore is the production composition entrypoint. The
// native adapter must still bind current authenticated IAM authority per call.
func NewCanonicalSSMCommandStore(runtime *EC2InstanceRuntime) (*SSMCommandStore, *CanonicalSSMCommandBackend, error) {
	backend, err := NewCanonicalSSMCommandBackend(runtime)
	if err != nil {
		return nil, nil, err
	}
	store, err := NewSSMCommandStore(runtime.db, backend)
	if err != nil {
		return nil, nil, err
	}
	return store, backend, nil
}
