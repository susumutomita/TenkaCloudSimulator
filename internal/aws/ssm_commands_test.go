package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
)

// This is a contract test double, never evidence of a durable executing backend.
type ssmCommandDouble struct {
	dispatch func(guestcommand.DurableIntent) error
	observe  func(guestcommand.DurableIntent) (guestcommand.DurableObservation, error)
}

func (b *ssmCommandDouble) Dispatch(_ context.Context, i guestcommand.DurableIntent) error {
	return b.dispatch(i)
}
func (b *ssmCommandDouble) Observe(_ context.Context, i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
	return b.observe(i)
}
func ssmFixture(t *testing.T, count int) (*SSMCommandStore, *ssmCommandDouble, *sql.DB, SSMCommandAuthority, []EC2GuestLease) {
	t.Helper()
	ls, lb, db := leaseFixture(t)
	ctx := context.Background()
	lb.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		return guestlease.Observation{Intent: i, Lease: "guest-" + i.Fence, State: guestlease.Acquired}, nil
	}
	leases := []EC2GuestLease{}
	for n := 0; n < count; n++ {
		o := leaseOwner()
		o.Instance = fmt.Sprintf("i-%02d", n)
		r, e := ls.Acquire(ctx, o)
		if e != nil {
			t.Fatal(e)
		}
		r, e = ls.Reconcile(ctx, r)
		if e != nil {
			t.Fatal(e)
		}
		leases = append(leases, r)
	}
	b := &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }, observe: func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Unknown"}, nil
	}}
	s, e := NewSSMCommandStore(db, b)
	if e != nil {
		t.Fatal(e)
	}
	a := SSMCommandAuthority{Account: "000000000000", Region: "us-east-1", Principal: "organizer", Deployment: leaseOwner().Deployment}
	return s, b, db, a, leases
}
func ssmCommandRequestFixture(leases []EC2GuestLease) SSMCommandRequest {
	r := SSMCommandRequest{DocumentName: "AWS-RunShellScript", Parameters: map[string][]string{"commands": {"printf hello"}}, Comment: "operation-1", TimeoutSeconds: 30}
	for _, l := range leases {
		r.InstanceIds = append(r.InstanceIds, l.Owner.Instance)
	}
	return r
}
func ssmSend(t *testing.T, s *SSMCommandStore, a SSMCommandAuthority, ls []EC2GuestLease) SSMCommand {
	t.Helper()
	r, e := s.Send(context.Background(), a, ssmCommandRequestFixture(ls), ls)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestSSMCommandsDurableIntentUnknownRestart(t *testing.T) {
	s, b, db, a, ls := ssmFixture(t, 2)
	ctx := context.Background()
	calls := 0
	b.dispatch = func(i guestcommand.DurableIntent) error {
		calls++
		got, e := s.Get(ctx, a, i.Command)
		if e != nil || len(got.Targets) != 2 || got.Targets[0].Intent != i {
			t.Fatalf("intent not committed: %+v %v", got, e)
		}
		return errors.New("uncertain transport")
	}
	r, e := s.Send(ctx, a, ssmCommandRequestFixture(ls), ls)
	if !errors.Is(e, ErrSSMCommandUnknown) || calls != 1 {
		t.Fatalf("send: %v calls=%d", e, calls)
	}
	s, e = NewSSMCommandStore(db, b)
	if e != nil {
		t.Fatal(e)
	}
	for _, l := range ls {
		got, e := s.Reconcile(ctx, a, r.CommandId, l.Owner.Instance)
		if e != nil || got.Successful() {
			t.Fatalf("reconcile: %+v %v", got, e)
		}
	}
	if calls != 1 {
		t.Fatal("restart redispatched")
	}
	summaries, next, e := s.List(ctx, a, time.UnixMilli(r.RequestedAt-60000).UTC().Format(time.RFC3339Nano), 50, "")
	if e != nil || next != "" || len(summaries) != 1 || summaries[0].CommandId != r.CommandId || ssmEncode(summaries[0].Request) != ssmEncode(r.Request) {
		t.Fatalf("recovery mismatch %+v %s %v", summaries, next, e)
	}
}
func TestSSMCommandsSuccessRequiresEveryTarget(t *testing.T) {
	s, b, _, a, ls := ssmFixture(t, 2)
	r := ssmSend(t, s, a, ls)
	ctx := context.Background()
	b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Success", StartedAt: i.RequestedAt, FinishedAt: i.RequestedAt, Stdout: "hello"}, nil
	}
	for n, l := range ls {
		var e error
		r, e = s.Reconcile(ctx, a, r.CommandId, l.Owner.Instance)
		if e != nil {
			t.Fatal(e)
		}
		if r.Successful() != (n == 1) {
			t.Fatal("premature completion")
		}
	}
	b.observe = func(guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		t.Fatal("terminal observation polled again")
		return guestcommand.DurableObservation{}, nil
	}
	if _, e := s.Reconcile(ctx, a, r.CommandId, ls[0].Owner.Instance); e != nil {
		t.Fatal(e)
	}
}
func TestSSMCommandsRejectUnsupportedAndStaleLease(t *testing.T) {
	for _, name := range []string{"document", "parameter", "duplicate", "timeout", "script", "comment", "authority", "stale", "missing", "contained", "invalid-utf8"} {
		t.Run(name, func(t *testing.T) {
			s, b, db, a, ls := ssmFixture(t, 1)
			req := ssmCommandRequestFixture(ls)
			calls := 0
			b.dispatch = func(guestcommand.DurableIntent) error { calls++; return nil }
			switch name {
			case "invalid-utf8":
				req.Parameters["commands"] = []string{string([]byte{255})}
			case "document":
				req.DocumentName = "AWS-RunPowerShellScript"
			case "parameter":
				req.Parameters["workingDirectory"] = []string{"/"}
			case "duplicate":
				req.InstanceIds = append(req.InstanceIds, req.InstanceIds[0])
				ls = append(ls, ls[0])
			case "timeout":
				req.TimeoutSeconds = 3601
			case "script":
				req.Parameters["commands"] = []string{strings.Repeat("x", 8193)}
			case "comment":
				req.Comment = strings.Repeat("x", 101)
			case "authority":
				a.Deployment = "another"
			case "stale":
				ls[0].Intent.Fence = strings.Repeat("a", 64)
			case "missing":
				if _, e := db.Exec(`DELETE FROM aws_ec2_guest_leases`); e != nil {
					t.Fatal(e)
				}
			case "contained":
				ls[0].State = "contained"
				ls[0].Intent.Action = guestlease.Contain
				ls[0].Intent.Operation = 2
			}
			if _, e := s.Send(context.Background(), a, req, ls); e == nil || calls != 0 {
				t.Fatalf("admitted unsupported command: %v %d", e, calls)
			}
		})
	}
	if _, e := NewSSMCommandStore(nil, nil); e == nil {
		t.Fatal("missing backend admitted")
	}
}
func TestSSMCommandsObservationFences(t *testing.T) {
	for _, name := range []string{"token", "command", "lease", "fence", "request", "script", "timeout", "time", "truncated", "cleanup", "exit", "future", "output", "unknown-success"} {
		t.Run(name, func(t *testing.T) {
			s, b, _, a, ls := ssmFixture(t, 1)
			r := ssmSend(t, s, a, ls)
			b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
				o := guestcommand.DurableObservation{Intent: i, State: "Success", StartedAt: i.RequestedAt, FinishedAt: i.RequestedAt}
				switch name {
				case "token":
					o.Intent.Token = strings.Repeat("a", 64)
				case "command":
					o.Intent.Command = "another"
				case "lease":
					o.Intent.Lease = "another"
				case "fence":
					o.Intent.LeaseFence = "another"
				case "request":
					o.Intent.Request = "another"
				case "script":
					o.Intent.ScriptJSON = `["other"]`
				case "timeout":
					o.Intent.TimeoutSeconds++
				case "time":
					o.Intent.RequestedAt++
				case "truncated":
					o.OutputTruncated = true
				case "cleanup":
					o.CleanupRequired = true
				case "exit":
					o.ExitCode = 1
				case "future":
					o.FinishedAt = time.Now().Add(time.Hour).UnixMilli()
				case "output":
					o.Stdout = strings.Repeat("x", ssmCommandOutput+1)
				case "unknown-success":
					o.State = "Unknown"
				}
				return o, nil
			}
			if _, e := s.Reconcile(context.Background(), a, r.CommandId, ls[0].Owner.Instance); e == nil {
				t.Fatal("bad observation admitted")
			}
			got, e := s.Get(context.Background(), a, r.CommandId)
			if e != nil || got.Targets[0].Observation.State != "Unknown" {
				t.Fatalf("mutated %v %+v", e, got)
			}
		})
	}
}
func TestSSMCommandsWriteAcknowledgement(t *testing.T) {
	for _, table := range []string{"aws_ssm_commands", "aws_ssm_command_tokens"} {
		t.Run(table, func(t *testing.T) {
			s, b, db, a, ls := ssmFixture(t, 1)
			calls := 0
			b.dispatch = func(guestcommand.DurableIntent) error { calls++; return nil }
			if _, e := db.Exec(`CREATE TRIGGER ignore_command BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(IGNORE); END`); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Send(context.Background(), a, ssmCommandRequestFixture(ls), ls); e == nil || calls != 0 {
				t.Fatalf("unacknowledged insert dispatched: %v %d", e, calls)
			}
		})
	}
	s, b, db, a, ls := ssmFixture(t, 1)
	r := ssmSend(t, s, a, ls)
	b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Success", StartedAt: i.RequestedAt, FinishedAt: i.RequestedAt}, nil
	}
	if _, e := db.Exec(`CREATE TRIGGER ignore_observation BEFORE UPDATE ON aws_ssm_commands BEGIN SELECT RAISE(IGNORE); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reconcile(context.Background(), a, r.CommandId, ls[0].Owner.Instance); e == nil {
		t.Fatal("ignored update acknowledged")
	}
}
func TestSSMCommandsListSnapshotAndScope(t *testing.T) {
	s, b, db, a, ls := ssmFixture(t, 1)
	ctx := context.Background()
	ssmSend(t, s, a, ls)
	ssmSend(t, s, a, ls)
	after := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	first, token, e := s.List(ctx, a, after, 1, "")
	if e != nil || len(first) != 1 || token == "" {
		t.Fatalf("first %v %s %v", first, token, e)
	}
	later := ssmSend(t, s, a, ls) // Later insert must not join the existing snapshot.
	s, e = NewSSMCommandStore(db, b)
	if e != nil {
		t.Fatal(e)
	}
	last, next, e := s.List(ctx, a, after, 1, token)
	if e != nil || len(last) != 1 || next != "" || last[0].CommandId == later.CommandId {
		t.Fatalf("snapshot %v %s %v", last, next, e)
	}
	replay, _, e := s.List(ctx, a, after, 1, token)
	if e != nil || ssmEncode(replay) != ssmEncode(last) {
		t.Fatal("token replay unstable")
	}
	other := a
	other.Principal = "participant"
	if _, _, e = s.List(ctx, other, after, 1, token); e == nil {
		t.Fatal("cross-authority token")
	}
	if _, _, e = s.List(ctx, a, after, 2, token); e == nil {
		t.Fatal("changed page size")
	}
	if _, _, e = s.List(ctx, a, time.Now().UTC().Format(time.RFC3339Nano), 1, token); e == nil {
		t.Fatal("changed filter")
	}
	if _, e = s.Get(ctx, other, last[0].CommandId); e == nil {
		t.Fatal("cross-authority get")
	}
	if _, e = db.Exec(`UPDATE aws_ssm_command_pages SET record=json_set(record,'$.ExpiresAt',1)`); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.List(ctx, a, after, 1, token); e == nil {
		t.Fatal("expired token")
	}
}
func TestSSMCommandsQuotaAndRequestCopies(t *testing.T) {
	s, b, _, a, ls := ssmFixture(t, 1)
	ctx := context.Background()
	calls := 0
	b.dispatch = func(guestcommand.DurableIntent) error { calls++; return nil }
	req := ssmCommandRequestFixture(ls)
	r, e := s.Send(ctx, a, req, ls)
	if e != nil {
		t.Fatal(e)
	}
	req.Parameters["commands"][0] = "mutated"
	req.InstanceIds[0] = "i-other"
	got, e := s.Get(ctx, a, r.CommandId)
	if e != nil || got.Request.Parameters["commands"][0] != "printf hello" {
		t.Fatal("caller mutated durable request")
	}
	for n := 1; n < ssmCommandLimit; n++ {
		ssmSend(t, s, a, ls)
	}
	if _, e = s.Send(ctx, a, ssmCommandRequestFixture(ls), ls); e == nil || calls != ssmCommandLimit {
		t.Fatalf("quota dispatched: %v %d", e, calls)
	}
}

func TestSSMCommandsLostRunningObservationRetainsCleanup(t *testing.T) {
	s, b, _, a, ls := ssmFixture(t, 1)
	ctx := context.Background()
	r := ssmSend(t, s, a, ls)
	b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "InProgress", StartedAt: i.RequestedAt}, nil
	}
	if _, e := s.Reconcile(ctx, a, r.CommandId, ls[0].Owner.Instance); e != nil {
		t.Fatal(e)
	}
	b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Unknown", StartedAt: i.RequestedAt, CleanupRequired: true}, nil
	}
	r, e := s.Reconcile(ctx, a, r.CommandId, ls[0].Owner.Instance)
	if e != nil || !r.Targets[0].Observation.CleanupRequired || r.Successful() {
		t.Fatalf("cleanup lost: %+v %v", r, e)
	}
	b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "InProgress", StartedAt: i.RequestedAt}, nil
	}
	if _, e = s.Reconcile(ctx, a, r.CommandId, ls[0].Owner.Instance); e == nil {
		t.Fatal("uncertainty silently cleared")
	}
}

func TestSSMCommandsDamagedRecordAndTokenIndex(t *testing.T) {
	for _, damage := range []string{"unknown-field", "token-index", "request"} {
		t.Run(damage, func(t *testing.T) {
			s, _, db, a, ls := ssmFixture(t, 1)
			r := ssmSend(t, s, a, ls)
			var e error
			switch damage {
			case "unknown-field":
				_, e = db.Exec(`UPDATE aws_ssm_commands SET record=json_set(record,'$.Ignored',true)`)
			case "token-index":
				_, e = db.Exec(`DELETE FROM aws_ssm_command_tokens`)
			case "request":
				_, e = db.Exec(`UPDATE aws_ssm_commands SET record=json_set(record,'$.Request.Comment','changed')`)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Get(context.Background(), a, r.CommandId); e == nil {
				t.Fatal("damaged state accepted")
			}
		})
	}
}

func TestSSMCommandsDatabaseReopen(t *testing.T) {
	s, b, db, a, ls := ssmFixture(t, 1)
	ctx := context.Background()
	calls := 0
	b.dispatch = func(guestcommand.DurableIntent) error { calls++; return errors.New("lost acknowledgement") }
	r, e := s.Send(ctx, a, ssmCommandRequestFixture(ls), ls)
	if !errors.Is(e, ErrSSMCommandUnknown) {
		t.Fatal(e)
	}
	var seq int
	var name, path string
	if e = db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	s, e = NewSSMCommandStore(reopened.Database(), b)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.GetInvocation(ctx, a, r.CommandId, ls[0].Owner.Instance)
	if e != nil || got.State != "Unknown" || got.Intent != r.Targets[0].Intent {
		t.Fatalf("reopen %+v %v", got, e)
	}
	if _, e = s.Reconcile(ctx, a, r.CommandId, ls[0].Owner.Instance); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("reopen redispatched")
	}
}
