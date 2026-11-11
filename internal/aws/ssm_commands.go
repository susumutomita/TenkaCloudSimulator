package aws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

var ErrSSMCommandInvalid = errors.New("invalid or unavailable durable SSM command")
var ErrSSMCommandUnknown = errors.New("SSM command outcome requires reconciliation; do not resubmit")

var ssmCommandID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

const ssmCommandLimit = 128
const ssmCommandBytes = 2097152
const ssmCommandOutput = 4096

// SSMCommandAuthority is trusted provider context, not participant-selected IAM.
// No public handler grants this authority. Caller must perform current IAM checks
// on every operation before using this internal store.
type SSMCommandAuthority struct{ Account, Region, Principal, Deployment string }
type SSMCommandRequest struct {
	DocumentName   string
	InstanceIds    []string
	Parameters     map[string][]string
	Comment        string
	TimeoutSeconds int
}
type SSMCommandTarget struct {
	Lease       EC2GuestLease
	Intent      guestcommand.DurableIntent
	Observation guestcommand.DurableObservation
}
type SSMCommand struct {
	CommandId   string
	Authority   SSMCommandAuthority
	Request     SSMCommandRequest
	RequestedAt int64
	Targets     []SSMCommandTarget
}

// SSMCommandStore is durable and opt-in. SSMCommandAPI exposes its bounded native
// API only when composed with the canonical backend. Immutable commands
// and target obligations are retained forever, including failures; global quota
// is 128 commands, eight targets/command, 8 KiB scripts and 4 KiB/output stream.
// Records are capped at 2 MiB each. There is no automatic retry, command deletion,
// or CancelCommand operation. Interactive sessions are separate. TimeoutSeconds admits 30..3600 seconds
// and is retained as delivery intent, never as proof of guest cancellation.
type SSMCommandStore struct {
	db      *sql.DB
	backend guestcommand.DurableBackend
}

func NewSSMCommandStore(db *sql.DB, backend guestcommand.DurableBackend) (*SSMCommandStore, error) {
	if db == nil || backend == nil {
		return nil, ErrSSMCommandInvalid
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ssm_commands (
 id TEXT PRIMARY KEY, record TEXT NOT NULL CHECK(length(CAST(record AS BLOB))<=2097152));
 CREATE TABLE IF NOT EXISTS aws_ssm_command_tokens (token TEXT PRIMARY KEY, command_id TEXT NOT NULL REFERENCES aws_ssm_commands(id));
 CREATE TABLE IF NOT EXISTS aws_ssm_command_pages (token TEXT PRIMARY KEY, record TEXT NOT NULL CHECK(length(CAST(record AS BLOB))<=8388608));`)
	if err != nil {
		return nil, err
	}
	return &SSMCommandStore{db, backend}, nil
}
func ssmRandom() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func ssmNewCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
func ssmAuthorityValid(a SSMCommandAuthority) bool {
	return a.Account == "000000000000" && guestLeaseName.MatchString(a.Region) && guestLeaseName.MatchString(a.Principal) && guestLeaseName.MatchString(a.Deployment)
}
func ssmRequestValid(r SSMCommandRequest) bool {
	if r.DocumentName != "AWS-RunShellScript" || r.TimeoutSeconds < 30 || r.TimeoutSeconds > 3600 || !utf8.ValidString(r.Comment) || len(r.Comment) > 100 || strings.ContainsRune(r.Comment, 0) || len(r.InstanceIds) < 1 || len(r.InstanceIds) > 8 || len(r.Parameters) != 1 {
		return false
	}
	commands, ok := r.Parameters["commands"]
	if !ok || len(commands) < 1 || len(commands) > 32 {
		return false
	}
	size := 0
	for _, c := range commands {
		size += len(c)
		if !utf8.ValidString(c) || strings.ContainsRune(c, 0) {
			return false
		}
	}
	if size < 1 || size > 8192 {
		return false
	}
	for i, id := range r.InstanceIds {
		if !guestLeaseName.MatchString(id) || (i > 0 && r.InstanceIds[i-1] >= id) {
			return false
		}
	}
	return true
}
func ssmEncode(v any) string { b, _ := json.Marshal(v); return string(b) }
func ssmDigest(r SSMCommand) string {
	// Observation and request digest itself are excluded from immutable identity.
	type identity struct {
		ID        string
		Authority SSMCommandAuthority
		Request   SSMCommandRequest
		At        int64
		Leases    []EC2GuestLease
		Tokens    []string
	}
	i := identity{ID: r.CommandId, Authority: r.Authority, Request: r.Request, At: r.RequestedAt}
	for _, t := range r.Targets {
		i.Leases = append(i.Leases, t.Lease)
		i.Tokens = append(i.Tokens, t.Intent.Token)
	}
	sum := sha256.Sum256([]byte(ssmEncode(i)))
	return hex.EncodeToString(sum[:])
}
func ssmTerminal(o guestcommand.DurableObservation) bool {
	return o.State == "Success" || o.State == "Failed" || o.State == "TimedOut" || o.State == "Rejected"
}
func ssmObservationValid(o guestcommand.DurableObservation, i guestcommand.DurableIntent) bool {
	if o.Intent != i || !utf8.ValidString(o.Stdout) || !utf8.ValidString(o.Stderr) || len(o.Stdout) > ssmCommandOutput || len(o.Stderr) > ssmCommandOutput || o.StartedAt < 0 || o.FinishedAt < 0 {
		return false
	}
	if o.StartedAt != 0 && o.StartedAt < i.RequestedAt {
		return false
	}
	switch o.State {
	case "Unknown":
		return (o.StartedAt == 0 || o.CleanupRequired) && o.FinishedAt == 0 && o.ExitCode == 0 && o.Stdout == "" && o.Stderr == "" && !o.OutputTruncated
	case "InProgress":
		return o.StartedAt >= i.RequestedAt && o.FinishedAt == 0 && o.ExitCode == 0 && o.Stdout == "" && o.Stderr == "" && !o.OutputTruncated && !o.CleanupRequired
	case "Rejected":
		return o.StartedAt == 0 && o.FinishedAt >= i.RequestedAt && o.ExitCode == 0 && o.Stdout == "" && o.Stderr == "" && !o.OutputTruncated && !o.CleanupRequired
	case "Success", "Failed", "TimedOut":
		if o.StartedAt < i.RequestedAt || o.FinishedAt < o.StartedAt || o.CleanupRequired {
			return false
		}
		return o.State != "Success" || (o.ExitCode == 0 && !o.OutputTruncated)
	}
	return false
}
func ssmRecordValid(r SSMCommand) bool {
	if !ssmCommandID.MatchString(r.CommandId) || !ssmAuthorityValid(r.Authority) || !ssmRequestValid(r.Request) || r.RequestedAt <= 0 || len(r.Targets) != len(r.Request.InstanceIds) {
		return false
	}
	digest := ssmDigest(r)
	scripts := ssmEncode(r.Request.Parameters["commands"])
	seen := map[string]bool{}
	for n, t := range r.Targets {
		o := t.Lease.Owner
		i := t.Intent
		if !validGuestRecord(t.Lease) || t.Lease.State != "held" || o.Account != r.Authority.Account || o.Region != r.Authority.Region || o.Deployment != r.Authority.Deployment || o.Instance != r.Request.InstanceIds[n] || !guestLeaseToken.MatchString(i.Token) || seen[i.Token] {
			return false
		}
		seen[i.Token] = true
		if i.Command != r.CommandId || i.Lease != t.Lease.Intent.Lease || i.LeaseFence != t.Lease.Intent.Fence || i.Request != digest || i.ScriptJSON != scripts || i.TimeoutSeconds != r.Request.TimeoutSeconds || i.RequestedAt != r.RequestedAt || !ssmObservationValid(t.Observation, i) {
			return false
		}
	}
	return true
}
func ssmRead(ctx context.Context, tx *sql.Tx, id string, a SSMCommandAuthority) (SSMCommand, string, error) {
	var r SSMCommand
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT record FROM aws_ssm_commands WHERE id=?`, id).Scan(&raw)
	if err != nil {
		return r, raw, err
	}
	if len(raw) > ssmCommandBytes || json.Unmarshal([]byte(raw), &r) != nil || !ssmRecordValid(r) || r.CommandId != id || r.Authority != a || ssmEncode(r) != raw {
		return SSMCommand{}, raw, ErrSSMCommandInvalid
	}
	for _, t := range r.Targets {
		var bound string
		if err = tx.QueryRowContext(ctx, `SELECT command_id FROM aws_ssm_command_tokens WHERE token=?`, t.Intent.Token).Scan(&bound); err != nil {
			return SSMCommand{}, raw, err
		}
		if bound != id {
			return SSMCommand{}, raw, ErrSSMCommandInvalid
		}
	}
	return r, raw, nil
}
func (s *SSMCommandStore) Get(ctx context.Context, a SSMCommandAuthority, id string) (SSMCommand, error) {
	if !ssmAuthorityValid(a) || !ssmCommandID.MatchString(id) {
		return SSMCommand{}, ErrSSMCommandInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSMCommand{}, err
	}
	defer tx.Rollback()
	r, _, err := ssmRead(ctx, tx, id, a)
	if err != nil {
		return SSMCommand{}, err
	}
	return r, tx.Commit()
}

// Send persists immutable authority, request, target incarnations and ALL dispatch
// obligations before any backend call. Only this stack frame may dispatch. A
// crash between commit and dispatch intentionally remains Unknown, never replayed.
// Any error requires recovery by List/Get, not automatic SendCommand resubmission.
// leases must be exact current held records, supplied by trusted provider code.
func (s *SSMCommandStore) Send(ctx context.Context, a SSMCommandAuthority, request SSMCommandRequest, leases []EC2GuestLease) (SSMCommand, error) {
	// Validate text before JSON copying, which otherwise repairs invalid UTF-8.
	if !utf8.ValidString(request.Comment) {
		return SSMCommand{}, ErrSSMCommandInvalid
	}
	for _, values := range request.Parameters {
		for _, v := range values {
			if !utf8.ValidString(v) {
				return SSMCommand{}, ErrSSMCommandInvalid
			}
		}
	}
	// Deep copy caller containers before retaining/dispatching them.
	var req SSMCommandRequest
	if json.Unmarshal([]byte(ssmEncode(request)), &req) != nil {
		return SSMCommand{}, ErrSSMCommandInvalid
	}
	sort.Strings(req.InstanceIds)
	if !ssmAuthorityValid(a) || !ssmRequestValid(req) || len(leases) != len(req.InstanceIds) {
		return SSMCommand{}, ErrSSMCommandInvalid
	}
	id, err := ssmNewCommandID()
	if err != nil {
		return SSMCommand{}, err
	}
	r := SSMCommand{CommandId: id, Authority: a, Request: req, RequestedAt: time.Now().UTC().UnixMilli()}
	byID := map[string]EC2GuestLease{}
	for _, l := range leases {
		if _, ok := byID[l.Owner.Instance]; ok {
			return SSMCommand{}, ErrSSMCommandInvalid
		}
		byID[l.Owner.Instance] = l
	}
	for _, instance := range req.InstanceIds {
		l, ok := byID[instance]
		if !ok {
			return SSMCommand{}, ErrSSMCommandInvalid
		}
		token, e := ssmRandom()
		if e != nil {
			return SSMCommand{}, e
		}
		r.Targets = append(r.Targets, SSMCommandTarget{Lease: l, Intent: guestcommand.DurableIntent{Token: token, Command: id, Lease: l.Intent.Lease, LeaseFence: l.Intent.Fence, ScriptJSON: ssmEncode(req.Parameters["commands"]), TimeoutSeconds: req.TimeoutSeconds, RequestedAt: r.RequestedAt}})
	}
	digest := ssmDigest(r)
	for n := range r.Targets {
		r.Targets[n].Intent.Request = digest
		r.Targets[n].Observation = guestcommand.DurableObservation{Intent: r.Targets[n].Intent, State: "Unknown"}
	}
	if !ssmRecordValid(r) {
		return SSMCommand{}, ErrSSMCommandInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	for _, t := range r.Targets {
		l, _, e := guestLeaseRead(ctx, tx, t.Lease.Owner)
		if e != nil {
			return r, e
		}
		if l != t.Lease {
			return r, ErrSSMCommandInvalid
		}
	}
	raw := ssmEncode(r)
	if len(raw) > ssmCommandBytes {
		return r, ErrSSMCommandInvalid
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO aws_ssm_commands(id,record) SELECT ?,? WHERE (SELECT count(*) FROM aws_ssm_commands)<?`, id, raw, ssmCommandLimit)
	if err != nil {
		return r, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return r, err
	}
	if n != 1 {
		return r, ErrSSMCommandInvalid
	}
	for _, t := range r.Targets {
		res, err = tx.ExecContext(ctx, `INSERT INTO aws_ssm_command_tokens(token,command_id) VALUES(?,?)`, t.Intent.Token, id)
		if err != nil {
			return r, err
		}
		n, err = res.RowsAffected()
		if err != nil {
			return r, err
		}
		if n != 1 {
			return r, ErrSSMCommandInvalid
		}
	}
	_, readback, err := ssmRead(ctx, tx, id, a)
	if err != nil {
		return r, err
	}
	if readback != raw {
		return r, ErrSSMCommandInvalid
	}
	if err = tx.Commit(); err != nil {
		return r, fmt.Errorf("%w: %w", ErrSSMCommandUnknown, err)
	}
	for _, t := range r.Targets {
		if err = ctx.Err(); err != nil {
			return r, fmt.Errorf("%w: %w", ErrSSMCommandUnknown, err)
		}
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = s.backend.Dispatch(call, t.Intent)
		cancel()
		if err != nil {
			return r, fmt.Errorf("%w: %w", ErrSSMCommandUnknown, err)
		}
	}
	return r, nil
}

// Reconcile observes one target without dispatch. Compare-and-swap prevents stale
// observations overwriting newer or terminal state, including concurrent readers.
func (s *SSMCommandStore) Reconcile(ctx context.Context, a SSMCommandAuthority, id, instance string) (SSMCommand, error) {
	r, err := s.Get(ctx, a, id)
	if err != nil {
		return r, err
	}
	index := -1
	for n, t := range r.Targets {
		if t.Lease.Owner.Instance == instance {
			index = n
			break
		}
	}
	if index < 0 {
		return r, ErrSSMCommandInvalid
	}
	old := ssmEncode(r)
	t := r.Targets[index]
	if ssmTerminal(t.Observation) {
		return r, nil
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	obs, err := s.backend.Observe(call, t.Intent)
	cancel()
	if err != nil {
		return r, err
	}
	if !ssmObservationValid(obs, t.Intent) || obs.StartedAt > time.Now().UnixMilli() || obs.FinishedAt > time.Now().UnixMilli() {
		return r, ErrSSMCommandInvalid
	}
	if t.Observation.StartedAt != 0 && (obs.State == "Rejected" || obs.StartedAt != t.Observation.StartedAt) {
		return r, ErrSSMCommandInvalid
	}
	if t.Observation.State == "InProgress" && obs.State == "Unknown" && !obs.CleanupRequired {
		return r, ErrSSMCommandInvalid
	}
	if t.Observation.CleanupRequired && !obs.CleanupRequired && !ssmTerminal(obs) {
		return r, ErrSSMCommandInvalid
	}
	r.Targets[index].Observation = obs
	raw := ssmEncode(r)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	_, current, err := ssmRead(ctx, tx, id, a)
	if err != nil {
		return r, err
	}
	if current != old {
		return r, ErrSSMCommandInvalid
	}
	res, err := tx.ExecContext(ctx, `UPDATE aws_ssm_commands SET record=? WHERE id=? AND record=?`, raw, id, old)
	if err != nil {
		return r, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return r, err
	}
	if n != 1 {
		return r, ErrSSMCommandInvalid
	}
	_, got, err := ssmRead(ctx, tx, id, a)
	if err != nil {
		return r, err
	}
	if got != raw {
		return r, ErrSSMCommandInvalid
	}
	return r, tx.Commit()
}

// Successful means every target has authoritative complete successful exit.
func (r SSMCommand) Successful() bool {
	if !ssmRecordValid(r) {
		return false
	}
	for _, t := range r.Targets {
		if t.Observation.State != "Success" {
			return false
		}
	}
	return true
}

// GetInvocation reads one persisted target observation. Missing or Unknown is
// never a successful invocation. This method neither polls nor dispatches.
func (s *SSMCommandStore) GetInvocation(ctx context.Context, a SSMCommandAuthority, id, instance string) (guestcommand.DurableObservation, error) {
	r, err := s.Get(ctx, a, id)
	if err != nil {
		return guestcommand.DurableObservation{}, err
	}
	for _, t := range r.Targets {
		if t.Lease.Owner.Instance == instance {
			return t.Observation, nil
		}
	}
	return guestcommand.DurableObservation{}, ErrSSMCommandInvalid
}
