package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
)

var ErrEC2GuestAttachmentPending = errors.New("guest attachment is sealed or requires reconciliation")

// EC2GuestIssuance is copied association supplied by the SAME already-qualified
// resource. Matching metadata never creates physical authority.
type EC2GuestIssuance struct {
	Lease              EC2GuestLease
	BackendIncarnation [32]byte
	PolicyGeneration   string
}
type EC2IssuedPersistentProbeResource interface {
	guesttcp.IssuedPersistentProbeResource
	EC2Issuance() EC2GuestIssuance
}
type ec2AttachmentRecord struct {
	Issuance         EC2GuestIssuance
	Descriptor       guesttcp.OwnedProbeDescriptor
	Supervisor       string
	Deadline         time.Time
	OriginalLifetime time.Time
}
type ec2CapturedResource struct {
	EC2IssuedPersistentProbeResource
	descriptor guesttcp.OwnedProbeDescriptor
}

func (r ec2CapturedResource) PersistentDescriptor() guesttcp.OwnedProbeDescriptor {
	return r.descriptor
}

type ec2AttachmentEntry struct {
	mu           sync.Mutex // reservation/publication only; no external or DB calls
	journal      sync.Mutex // serializes short journal transactions, never callbacks/joins
	record       ec2AttachmentRecord
	raw          string
	sealed       bool
	constructing bool
	consumed     bool
	owner        *guesttcp.OwnedProbeSession
	attachment   *guesttcp.OwnedProbeAttachment
	scope        *guesttcp.HTTPProbeOperationScope
}

// EC2GuestAttachmentReceipt retains cleanup/discovery; it cannot be restored
// from JSON or used to manufacture an issued resource.
type EC2GuestAttachmentReceipt struct{ entry *ec2AttachmentEntry }

func (r *EC2GuestAttachmentReceipt) Scope() *guesttcp.HTTPProbeOperationScope {
	if r == nil || r.entry == nil {
		return nil
	}
	r.entry.mu.Lock()
	defer r.entry.mu.Unlock()
	return r.entry.scope
}
func (r *EC2GuestAttachmentReceipt) Ownership() (consumed, serviceTransferred, constructing bool) {
	if r == nil || r.entry == nil {
		return
	}
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	consumed, constructing = e.consumed, e.constructing
	if e.attachment != nil {
		serviceTransferred = e.attachment.ServiceTransferred()
	}
	return
}

// Exclusive one-actor ownership of the database/provider incarnation is a
// deployment prerequisite, NOT enforced by a *sql.DB pointer identity. There is
// no production registration, restart adoption, FD reopening or issuer here.
type EC2GuestAttachmentCoordinator struct {
	store      *EC2GuestLeaseStore
	supervisor string
	mu         sync.Mutex
	entries    map[EC2GuestOwner]*ec2AttachmentEntry
	vmLaunches map[EC2GuestOwner]interface{ Close() error }
}

func NewEC2GuestAttachmentCoordinator(store *EC2GuestLeaseStore, supervisor string) (*EC2GuestAttachmentCoordinator, error) {
	if store == nil || !guestLeaseToken.MatchString(supervisor) {
		return nil, errGuestLease
	}
	_, err := store.db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_guest_attachments (
 account TEXT NOT NULL,region TEXT NOT NULL,instance TEXT NOT NULL,
 profile_name TEXT NOT NULL,profile_id TEXT NOT NULL,lease TEXT NOT NULL UNIQUE,
 record TEXT NOT NULL CHECK(length(record)<=32768),
 state TEXT NOT NULL CHECK(state IN ('preparing','attached','seal_pending','locally_drained')),
 revision INTEGER NOT NULL CHECK(revision>0), PRIMARY KEY(account,region,instance));`)
	if err != nil {
		return nil, err
	}
	return &EC2GuestAttachmentCoordinator{store: store, supervisor: supervisor, entries: make(map[EC2GuestOwner]*ec2AttachmentEntry)}, nil
}

// Attach persists the exact association before resource consumption, registers
// the owner before transport I/O, and never retries an ambiguous constructor.
// lifetime is the original owner lifetime, NOT a request-scoped defer-cancel.
func (c *EC2GuestAttachmentCoordinator) Attach(admission, lifetime context.Context, expected EC2GuestLease, resource EC2IssuedPersistentProbeResource, service guesttcp.RevocablePassiveService, deadline time.Time) (*EC2GuestAttachmentReceipt, error) {
	if c == nil || admission == nil || lifetime == nil || nilGuestAttachmentResource(resource) || !validGuestRecord(expected) || expected.State != "held" {
		return nil, errGuestLease
	}
	if err := admission.Err(); err != nil {
		return nil, err
	}
	original, ok := lifetime.Deadline()
	if !ok || !original.After(time.Now()) || deadline.IsZero() || deadline.After(original) {
		return nil, errGuestLease
	}
	issuance := resource.EC2Issuance()
	descriptor := resource.PersistentDescriptor()
	if issuance.Lease != expected || issuance.BackendIncarnation == ([32]byte{}) || issuance.BackendIncarnation != descriptor.Binding.BackendIncarnation || !guestLeaseToken.MatchString(issuance.PolicyGeneration) {
		return nil, errGuestLease
	}
	record := ec2AttachmentRecord{Issuance: issuance, Descriptor: descriptor, Supervisor: c.supervisor, Deadline: deadline, OriginalLifetime: original}
	bytes, err := json.Marshal(record)
	if err != nil || len(bytes) > 32768 {
		return nil, errGuestLease
	}
	// JSON round-trip deep-copies all policy slices and optional ARP fields.
	if err = json.Unmarshal(bytes, &record); err != nil {
		return nil, err
	}
	e := &ec2AttachmentEntry{record: record, raw: string(bytes), constructing: true}
	c.mu.Lock()
	if len(c.entries) >= guestLeaseLimit || c.entries[expected.Owner] != nil {
		c.mu.Unlock()
		return nil, ErrEC2GuestAttachmentPending
	}
	c.entries[expected.Owner] = e
	c.mu.Unlock()
	receipt := &EC2GuestAttachmentReceipt{e}
	e.journal.Lock()
	err = c.prepare(admission, e)
	e.journal.Unlock()
	if err != nil {
		e.mu.Lock()
		e.sealed = true
		e.constructing = false
		e.mu.Unlock()
		return receipt, errors.Join(ErrEC2GuestAttachmentPending, err)
	}
	e.mu.Lock()
	sealed := e.sealed
	e.mu.Unlock()
	if sealed || admission.Err() != nil {
		c.finishConstruction(e, nil, nil)
		_, sealErr := c.Seal(context.Background(), receipt)
		return receipt, errors.Join(ErrEC2GuestAttachmentPending, admission.Err(), sealErr)
	}
	owner, err := guesttcp.NewOwnedPersistentProbeSession(lifetime, ec2CapturedResource{resource, record.Descriptor})
	if err != nil {
		c.finishConstruction(e, nil, nil)
		_, sealErr := c.Seal(admission, receipt)
		return receipt, errors.Join(err, sealErr)
	}
	// Publish the retained owner BEFORE AttachPersistent can emit any frame.
	e.mu.Lock()
	e.owner = owner
	e.consumed = true
	sealed = e.sealed
	e.mu.Unlock()
	if sealed || admission.Err() != nil {
		owner.Revoke()
		c.finishConstruction(e, owner, nil)
		_, sealErr := c.Seal(admission, receipt)
		return receipt, errors.Join(ErrEC2GuestAttachmentPending, sealErr)
	}
	attachment, attachErr := owner.AttachPersistent(admission, service, deadline)
	c.finishConstruction(e, owner, attachment)
	if attachErr != nil {
		_, sealErr := c.Seal(admission, receipt)
		return receipt, errors.Join(attachErr, sealErr)
	}
	e.journal.Lock()
	e.mu.Lock()
	sealed = e.sealed
	e.mu.Unlock()
	if !sealed {
		err = c.transition(admission, e, "preparing", "attached")
	}
	e.mu.Lock()
	if err == nil && !sealed && !e.sealed && admission.Err() == nil {
		e.scope = attachment.Scope()
	} else {
		sealed = true
	}
	e.mu.Unlock()
	e.journal.Unlock()
	if sealed || err != nil {
		_, sealErr := c.Seal(admission, receipt)
		return receipt, errors.Join(ErrEC2GuestAttachmentPending, err, sealErr)
	}
	return receipt, nil
}
func (c *EC2GuestAttachmentCoordinator) finishConstruction(e *ec2AttachmentEntry, owner *guesttcp.OwnedProbeSession, h *guesttcp.OwnedProbeAttachment) {
	e.mu.Lock()
	e.owner = owner
	e.attachment = h
	e.constructing = false
	e.mu.Unlock()
}
func (c *EC2GuestAttachmentCoordinator) prepare(ctx context.Context, e *ec2AttachmentEntry) error {
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, _, err := guestLeaseRead(ctx, tx, e.record.Issuance.Lease.Owner)
	if err != nil || current != e.record.Issuance.Lease {
		return errors.Join(err, errGuestLease)
	}
	o := current.Owner
	if o.ProfileName != "" {
		p, err := iamProfileRead(ctx, tx, o.ProfileName, false)
		if err != nil || p.InstanceProfileID != o.ProfileID {
			return errors.Join(err, errGuestLease)
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_guest_attachments(account,region,instance,profile_name,profile_id,lease,record,state,revision) SELECT ?,?,?,?,?,?,?,'preparing',1 WHERE (SELECT count(*) FROM aws_ec2_guest_attachments)<?`, o.Account, o.Region, o.Instance, o.ProfileName, o.ProfileID, current.Intent.Lease, e.raw, guestLeaseLimit)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(err, errGuestLease)
	}
	return tx.Commit()
}
func (c *EC2GuestAttachmentCoordinator) transition(ctx context.Context, e *ec2AttachmentEntry, from, to string) error {
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o := e.record.Issuance.Lease.Owner
	result, err := tx.ExecContext(ctx, `UPDATE aws_ec2_guest_attachments SET state=?,revision=revision+1 WHERE account=? AND region=? AND instance=? AND record=? AND state=?`, to, o.Account, o.Region, o.Instance, e.raw, from)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(err, ErrEC2GuestAttachmentPending)
	}
	return tx.Commit()
}
func (c *EC2GuestAttachmentCoordinator) owns(r *EC2GuestAttachmentReceipt) bool {
	if c == nil || r == nil || r.entry == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[r.entry.record.Issuance.Lease.Owner] == r.entry
}

// Seal acknowledges local admission closure plus durable intent, not drain or
// backend containment. No SQL transaction surrounds Revoke, Close or Wait.
func (c *EC2GuestAttachmentCoordinator) Seal(ctx context.Context, r *EC2GuestAttachmentReceipt) (*EC2GuestAttachmentReceipt, error) {
	if ctx == nil || !c.owns(r) {
		return r, errGuestLease
	}
	e := r.entry
	e.mu.Lock()
	e.sealed = true
	owner := e.owner
	e.mu.Unlock()
	if owner != nil {
		owner.Revoke()
	}
	e.journal.Lock()
	defer e.journal.Unlock()
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	o := e.record.Issuance.Lease.Owner
	result, err := tx.ExecContext(ctx, `UPDATE aws_ec2_guest_attachments SET state=CASE WHEN state='locally_drained' THEN state ELSE 'seal_pending' END,revision=revision+1 WHERE account=? AND region=? AND instance=? AND record=?`, o.Account, o.Region, o.Instance, e.raw)
	if err != nil {
		return r, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return r, errors.Join(err, ErrEC2GuestAttachmentPending)
	}
	return r, tx.Commit()
}

// ObserveCleanup never retries construction or invents settlement on timeout.
func (c *EC2GuestAttachmentCoordinator) ObserveCleanup(ctx context.Context, r *EC2GuestAttachmentReceipt) error {
	if ctx == nil || !c.owns(r) {
		return errGuestLease
	}
	e := r.entry
	e.mu.Lock()
	sealed, constructing, owner, h := e.sealed, e.constructing, e.owner, e.attachment
	e.mu.Unlock()
	if !sealed || constructing || owner == nil {
		return ErrEC2GuestAttachmentPending
	}
	var err error
	if h != nil {
		// Reassert the same one-shot shutdown: nil proves joins and Close results,
		// unlike WaitContext which also returns the terminal operation error.
		err = h.CloseContext(ctx)
	} else {
		err = owner.Wait(ctx)
	}
	if err != nil {
		return errors.Join(ErrEC2GuestAttachmentPending, err)
	}
	e.journal.Lock()
	defer e.journal.Unlock()
	status, readErr := c.Get(ctx, e.record.Issuance.Lease.Owner)
	if readErr != nil {
		return readErr
	}
	if status.State == "locally_drained" {
		return nil
	}
	return c.transition(ctx, e, "seal_pending", "locally_drained")
}

// Advance seals the exact live/preparing association before backend dispatch.
// A restarted coordinator has no entry: it cannot use persisted IDs as a permit.
func (c *EC2GuestAttachmentCoordinator) Advance(ctx context.Context, expected EC2GuestLease) (EC2GuestLease, error) {
	if c == nil {
		return expected, errGuestLease
	}
	c.mu.Lock()
	e := c.entries[expected.Owner]
	pendingVM := c.vmLaunches[expected.Owner]
	c.mu.Unlock()
	if pendingVM != nil {
		if err := pendingVM.Close(); err != nil {
			return expected, err
		}
	}
	if e == nil {
		return c.store.Advance(ctx, expected)
	}
	if e.record.Issuance.Lease.Owner != expected.Owner || e.record.Issuance.Lease.Intent.Lease != expected.Intent.Lease || e.record.Issuance.Lease.Intent.Fence != expected.Intent.Fence {
		return expected, errGuestLease
	}
	if _, err := c.Seal(ctx, &EC2GuestAttachmentReceipt{e}); err != nil {
		return expected, err
	}
	return c.store.advanceAttachment(ctx, expected, e)
}

// Schema absence means the opt-in feature has never created an obligation.
// Query inside the SAME mutation transaction; do not cache absence per DB handle.
func ec2AttachmentSchemaPresent(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_ec2_guest_attachments'`).Scan(&count)
	return count == 1, err
}
func ec2AttachmentProfileGuard(ctx context.Context, tx *sql.Tx, name, id string) error {
	if pending, err := ec2VMNICProfilePending(ctx, tx, name, id); err != nil {
		return err
	} else if pending {
		return ErrEC2GuestAttachmentPending
	}
	present, err := ec2AttachmentSchemaPresent(ctx, tx)
	if err != nil || !present {
		return err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_guest_attachments WHERE profile_name=? AND profile_id=? AND state<>'locally_drained'`, name, id).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return ssmFailure{"DeleteConflict", "Guest attachment must be sealed and drained before profile mutation."}
	}
	return nil
}
func ec2AttachmentAdvanceGuard(ctx context.Context, tx *sql.Tx, expected EC2GuestLease, permit *ec2AttachmentEntry) error {
	present, err := ec2AttachmentSchemaPresent(ctx, tx)
	if err != nil || !present {
		return err
	}
	var raw, state string
	o := expected.Owner
	err = tx.QueryRowContext(ctx, `SELECT record,state FROM aws_ec2_guest_attachments WHERE account=? AND region=? AND instance=?`, o.Account, o.Region, o.Instance).Scan(&raw, &state)
	if errors.Is(err, sql.ErrNoRows) {
		if pending, e := ec2VMNICPending(ctx, tx, o); e != nil {
			return e
		} else if pending {
			return ErrEC2GuestAttachmentPending
		}
		return nil
	}
	if err != nil {
		return err
	}
	record, decodeErr := decodeEC2AttachmentRecord(raw)
	if decodeErr != nil || record.Issuance.Lease.Owner != expected.Owner || record.Issuance.Lease.Intent.Lease != expected.Intent.Lease || record.Issuance.Lease.Intent.Fence != expected.Intent.Fence {
		return errGuestLease
	}
	if state == "locally_drained" {
		return nil
	}
	if permit == nil || permit.raw != raw || state != "seal_pending" {
		return ErrEC2GuestAttachmentPending
	}
	permit.mu.Lock()
	sealed := permit.sealed
	permit.mu.Unlock()
	if !sealed {
		return ErrEC2GuestAttachmentPending
	}
	return nil
}

func nilGuestAttachmentResource(r EC2IssuedPersistentProbeResource) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// EC2GuestAttachmentStatus is recovery metadata only, never a live receipt.
type EC2GuestAttachmentStatus struct {
	Issuance         EC2GuestIssuance
	Descriptor       guesttcp.OwnedProbeDescriptor
	Supervisor       string
	Deadline         time.Time
	OriginalLifetime time.Time
	State            string
	Revision         int64
	RecoveryRequired bool
}

func (c *EC2GuestAttachmentCoordinator) Get(ctx context.Context, o EC2GuestOwner) (EC2GuestAttachmentStatus, error) {
	if c == nil || ctx == nil || !validGuestOwner(o) {
		return EC2GuestAttachmentStatus{}, errGuestLease
	}
	var raw, state string
	var revision int64
	err := c.store.db.QueryRowContext(ctx, `SELECT record,state,revision FROM aws_ec2_guest_attachments WHERE account=? AND region=? AND instance=?`, o.Account, o.Region, o.Instance).Scan(&raw, &state, &revision)
	if err != nil {
		return EC2GuestAttachmentStatus{}, err
	}
	record, err := decodeEC2AttachmentRecord(raw)
	if err != nil || record.Issuance.Lease.Owner != o || revision < 1 {
		return EC2GuestAttachmentStatus{}, errors.Join(err, errGuestLease)
	}
	switch state {
	case "preparing", "attached", "seal_pending", "locally_drained":
	default:
		return EC2GuestAttachmentStatus{}, errGuestLease
	}
	c.mu.Lock()
	entry := c.entries[o]
	c.mu.Unlock()
	recovery := state != "locally_drained"
	if entry != nil && entry.raw == raw {
		entry.mu.Lock()
		recovery = recovery && (entry.sealed || entry.constructing || entry.owner == nil)
		entry.mu.Unlock()
	}
	return EC2GuestAttachmentStatus{Issuance: record.Issuance, Descriptor: record.Descriptor, Supervisor: record.Supervisor, Deadline: record.Deadline, OriginalLifetime: record.OriginalLifetime, State: state, Revision: revision, RecoveryRequired: recovery}, nil
}
func decodeEC2AttachmentRecord(raw string) (ec2AttachmentRecord, error) {
	var record ec2AttachmentRecord
	if len(raw) == 0 || len(raw) > 32768 || json.Unmarshal([]byte(raw), &record) != nil {
		return record, errGuestLease
	}
	encoded, err := json.Marshal(record)
	if err != nil || string(encoded) != raw || !validGuestRecord(record.Issuance.Lease) || record.Issuance.Lease.State != "held" || !guestLeaseToken.MatchString(record.Supervisor) || !guestLeaseToken.MatchString(record.Issuance.PolicyGeneration) || record.Issuance.BackendIncarnation == ([32]byte{}) || record.Descriptor.Binding.BackendIncarnation != record.Issuance.BackendIncarnation || record.Deadline.IsZero() || record.OriginalLifetime.IsZero() || record.Deadline.After(record.OriginalLifetime) {
		return record, errGuestLease
	}
	return record, nil
}
