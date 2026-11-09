package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
)

// ErrEC2GuestLeaseReconciliationRequired marks a dispatch whose durable outcome
// is not settled. It never authorizes a retry. Pre-dispatch validation/write
// failures do not invoke the backend; callers must still Get after DB errors.
var ErrEC2GuestLeaseReconciliationRequired = errors.New("guest lease reconciliation required")

var errGuestLease = errors.New("guest lease requires reconciliation or has invalid authority/state")
var guestLeaseToken = regexp.MustCompile(`^[a-f0-9]{64}$`)
var guestLeaseName = regexp.MustCompile(`^[A-Za-z0-9:_./-]{1,256}$`)

const guestLeaseLimit = 1024

// EC2GuestOwner is trusted provider authority, never caller-selected guest input.
// ProfileName must be canonical; ProfileID pins its incarnation, not permissions.
// Current IAM is single-account; this boundary accepts only that local account.
type EC2GuestOwner struct {
	Account, Region, Instance, Deployment, ProfileName, ProfileID string
}
type EC2GuestLease struct {
	Owner  EC2GuestOwner
	Intent guestlease.Intent
	State  string
}

// EC2GuestLeaseStore is explicitly opt-in. No public EC2/SSM handler uses it.
// Tombstones are retained forever and count toward a global 1024-record quota.
// This first slice intentionally offers no reuse, pruning or replacement API.
// Unknown acquisition cannot yet be retired without a positive acquired mapping:
// a future recovery protocol must fence dispatchers before proving absence.
// Profile deletion/membership hooks and credential authority are not integrated;
// profile changes never block containment or release of an existing obligation.
type EC2GuestLeaseStore struct {
	db      *sql.DB
	backend guestlease.Backend
}

func NewEC2GuestLeaseStore(db *sql.DB, backend guestlease.Backend) (*EC2GuestLeaseStore, error) {
	if db == nil || backend == nil {
		return nil, errGuestLease
	}
	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_guest_leases (
 account TEXT NOT NULL, region TEXT NOT NULL, instance TEXT NOT NULL,
 record TEXT NOT NULL CHECK(length(record)<=4096),
 lease TEXT UNIQUE CHECK(lease IS NULL OR (length(lease)>0 AND length(lease)<=256)),
 PRIMARY KEY(account,region,instance));`)
	if e != nil {
		return nil, e
	}
	return &EC2GuestLeaseStore{db, backend}, nil
}
func validGuestOwner(o EC2GuestOwner) bool {
	return o.Account == "000000000000" && guestLeaseName.MatchString(o.Region) && guestLeaseName.MatchString(o.Instance) && guestLeaseName.MatchString(o.Deployment) && ((o.ProfileName == "" && o.ProfileID == "") || (iamInlineName.MatchString(o.ProfileName) && iamInstanceProfileID.MatchString(o.ProfileID)))
}
func validGuestRecord(r EC2GuestLease) bool {
	if !validGuestOwner(r.Owner) || !guestLeaseToken.MatchString(r.Intent.Fence) || r.Intent.Operation < 1 || r.Intent.Operation > 3 || len(r.Intent.Lease) > 256 {
		return false
	}
	switch r.State {
	case "acquire_pending", "rejected":
		return r.Intent.Action == guestlease.Acquire && r.Intent.Operation == 1 && r.Intent.Lease == ""
	case "held":
		return r.Intent.Action == guestlease.Acquire && r.Intent.Operation == 1 && r.Intent.Lease != ""
	case "contain_pending", "contained":
		return r.Intent.Action == guestlease.Contain && r.Intent.Operation == 2 && r.Intent.Lease != ""
	case "release_pending", "released":
		return r.Intent.Action == guestlease.Release && r.Intent.Operation == 3 && r.Intent.Lease != ""
	}
	return false
}
func guestLeaseRead(ctx context.Context, tx *sql.Tx, o EC2GuestOwner) (EC2GuestLease, string, error) {
	var r EC2GuestLease
	var raw string
	var lease sql.NullString
	e := tx.QueryRowContext(ctx, `SELECT record,lease FROM aws_ec2_guest_leases WHERE account=? AND region=? AND instance=?`, o.Account, o.Region, o.Instance).Scan(&raw, &lease)
	if e != nil {
		return r, raw, e
	}
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &r) != nil || !validGuestRecord(r) || r.Owner != o || lease.String != r.Intent.Lease || lease.Valid != (r.Intent.Lease != "") {
		return r, raw, errGuestLease
	}
	// Reject noncanonical/unknown fields, rather than silently dropping authority.
	b, _ := json.Marshal(r)
	if string(b) != raw {
		return r, raw, errGuestLease
	}
	return r, raw, nil
}
func guestLeaseWrite(ctx context.Context, tx *sql.Tx, r EC2GuestLease, old string) error {
	if !validGuestRecord(r) {
		return errGuestLease
	}
	b, e := json.Marshal(r)
	if e != nil || len(b) > 4096 {
		return errGuestLease
	}
	var res sql.Result
	var lease any
	if r.Intent.Lease != "" {
		lease = r.Intent.Lease
	}
	if old == "" {
		res, e = tx.ExecContext(ctx, `INSERT INTO aws_ec2_guest_leases(account,region,instance,record,lease) SELECT ?,?,?,?,? WHERE (SELECT count(*) FROM aws_ec2_guest_leases)<?`, r.Owner.Account, r.Owner.Region, r.Owner.Instance, string(b), lease, guestLeaseLimit)
	} else {
		res, e = tx.ExecContext(ctx, `UPDATE aws_ec2_guest_leases SET record=?,lease=? WHERE account=? AND region=? AND instance=? AND record=?`, string(b), lease, r.Owner.Account, r.Owner.Region, r.Owner.Instance, old)
	}
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errGuestLease
	}
	got, raw, e := guestLeaseRead(ctx, tx, r.Owner)
	if e != nil {
		return e
	}
	if raw != string(b) || got != r {
		return errGuestLease
	}
	return nil
}
func (s *EC2GuestLeaseStore) Get(ctx context.Context, o EC2GuestOwner) (EC2GuestLease, error) {
	if !validGuestOwner(o) {
		return EC2GuestLease{}, errGuestLease
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return EC2GuestLease{}, e
	}
	defer tx.Rollback()
	r, _, e := guestLeaseRead(ctx, tx, o)
	if e != nil {
		return EC2GuestLease{}, e
	}
	return r, tx.Commit()
}

// Acquire commits its quarantine obligation before the first backend call.
// Failure after that commit must be recovered through Get/Reconcile, not Acquire.
func (s *EC2GuestLeaseStore) Acquire(ctx context.Context, o EC2GuestOwner) (EC2GuestLease, error) {
	if !validGuestOwner(o) {
		return EC2GuestLease{}, errGuestLease
	}
	var token [32]byte
	if _, e := rand.Read(token[:]); e != nil {
		return EC2GuestLease{}, e
	}
	r := EC2GuestLease{o, guestlease.Intent{Fence: hex.EncodeToString(token[:]), Operation: 1, Action: guestlease.Acquire}, "acquire_pending"}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return r, e
	}
	defer tx.Rollback()
	if o.ProfileName != "" {
		p, e := iamProfileRead(ctx, tx, o.ProfileName, false)
		if e != nil {
			return r, e
		}
		if p.InstanceProfileName != o.ProfileName || p.InstanceProfileID != o.ProfileID {
			return r, errGuestLease
		}
	}
	if e = guestLeaseWrite(ctx, tx, r, ""); e != nil {
		return r, e
	}
	if e = tx.Commit(); e != nil {
		return r, e
	}
	return s.dispatch(ctx, r)
}

// Advance requires the exact prior record, fencing stale owners and operations.
// There are only two further dispatches: containment, then release. Neither
// dispatch acknowledgement itself proves containment or release.
func (s *EC2GuestLeaseStore) Advance(ctx context.Context, expected EC2GuestLease) (EC2GuestLease, error) {
	if !validGuestRecord(expected) {
		return EC2GuestLease{}, errGuestLease
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return expected, e
	}
	defer tx.Rollback()
	r, raw, e := guestLeaseRead(ctx, tx, expected.Owner)
	if e != nil {
		return expected, e
	}
	if r != expected {
		return expected, errGuestLease
	}
	switch r.State {
	case "held":
		r.State = "contain_pending"
		r.Intent.Action = guestlease.Contain
	case "contained":
		r.State = "release_pending"
		r.Intent.Action = guestlease.Release
	default:
		return expected, errGuestLease
	}
	r.Intent.Operation++
	if e = guestLeaseWrite(ctx, tx, r, raw); e != nil {
		return expected, e
	}
	if e = tx.Commit(); e != nil {
		return expected, e
	}
	return s.dispatch(ctx, r)
}
func (s *EC2GuestLeaseStore) dispatch(ctx context.Context, r EC2GuestLease) (EC2GuestLease, error) {
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, e := s.backend.Dispatch(call, r.Intent)
	if e != nil {
		return r, fmt.Errorf("%w: %w", ErrEC2GuestLeaseReconciliationRequired, e)
	}
	if d == guestlease.Rejected && r.State == "acquire_pending" {
		next, err := s.replace(ctx, r, EC2GuestLease{r.Owner, r.Intent, "rejected"})
		if err != nil {
			return next, fmt.Errorf("%w: %w", ErrEC2GuestLeaseReconciliationRequired, err)
		}
		return next, nil
	}
	if d != guestlease.Accepted {
		return r, ErrEC2GuestLeaseReconciliationRequired
	}
	return r, nil
}
func (s *EC2GuestLeaseStore) replace(ctx context.Context, expected, next EC2GuestLease) (EC2GuestLease, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return expected, e
	}
	defer tx.Rollback()
	got, raw, e := guestLeaseRead(ctx, tx, expected.Owner)
	if e != nil {
		return expected, e
	}
	if got != expected {
		return expected, errGuestLease
	}
	if e = guestLeaseWrite(ctx, tx, next, raw); e != nil {
		return expected, e
	}
	if e = tx.Commit(); e != nil {
		return expected, e
	}
	return next, nil
}

// Reconcile never dispatches, retries or invents a successful guest state.
func (s *EC2GuestLeaseStore) Reconcile(ctx context.Context, expected EC2GuestLease) (EC2GuestLease, error) {
	current, e := s.Get(ctx, expected.Owner)
	if e != nil {
		return expected, e
	}
	if current != expected {
		return expected, errGuestLease
	}
	if current.State != "acquire_pending" && current.State != "contain_pending" && current.State != "release_pending" {
		return expected, errGuestLease
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	obs, e := s.backend.Observe(call, current.Intent)
	if e != nil {
		return expected, e
	}
	if obs.Intent != current.Intent || obs.Lease == "" || len(obs.Lease) > 256 {
		return expected, errGuestLease
	}
	next := current
	switch current.State {
	case "acquire_pending":
		if obs.State != guestlease.Acquired {
			return expected, errGuestLease
		}
		next.State = "held"
		next.Intent.Lease = obs.Lease
	case "contain_pending":
		if obs.State != guestlease.Contained || obs.Lease != current.Intent.Lease {
			return expected, errGuestLease
		}
		next.State = "contained"
	case "release_pending":
		if obs.State != guestlease.Released || obs.Lease != current.Intent.Lease {
			return expected, errGuestLease
		}
		next.State = "released"
	}
	return s.replace(ctx, current, next)
}

// List enumerates durable obligations and tombstones for trusted recovery code.
// Pagination uses the last returned Owner.Instance as after; an empty page ends
// iteration. Like Get, this is not participant authorization or credential access.
func (s *EC2GuestLeaseStore) List(ctx context.Context, account, region, after string, limit int) ([]EC2GuestLease, error) {
	if account != "000000000000" || !guestLeaseName.MatchString(region) || (after != "" && !guestLeaseName.MatchString(after)) || limit < 1 || limit > 100 {
		return nil, errGuestLease
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT instance,record,lease FROM aws_ec2_guest_leases WHERE account=? AND region=? AND instance>? ORDER BY instance LIMIT ?`, account, region, after, limit)
	if e != nil {
		return nil, e
	}
	out := []EC2GuestLease{}
	for rows.Next() {
		var instance, raw string
		var lease sql.NullString
		var r EC2GuestLease
		if e = rows.Scan(&instance, &raw, &lease); e != nil {
			_ = rows.Close()
			return nil, e
		}
		if len(raw) > 4096 || json.Unmarshal([]byte(raw), &r) != nil || !validGuestRecord(r) || r.Owner.Account != account || r.Owner.Region != region || r.Owner.Instance != instance || lease.String != r.Intent.Lease || lease.Valid != (r.Intent.Lease != "") {
			_ = rows.Close()
			return nil, errGuestLease
		}
		canonical, _ := json.Marshal(r)
		if string(canonical) != raw {
			_ = rows.Close()
			return nil, errGuestLease
		}
		out = append(out, r)
	}
	if e = rows.Err(); e != nil {
		_ = rows.Close()
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
