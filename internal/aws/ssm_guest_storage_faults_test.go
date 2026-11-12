package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
)

func faultLeaseStore(t *testing.T, db *sql.DB) *EC2GuestLeaseStore {
	t.Helper()
	b := &leaseBackendDouble{dispatch: func(guestlease.Intent) (guestlease.Disposition, error) { return guestlease.Accepted, nil }}
	b.observe = func(i guestlease.Intent) (guestlease.Observation, error) {
		state := guestlease.Acquired
		if i.Action == guestlease.Contain {
			state = guestlease.Contained
		}
		if i.Action == guestlease.Release {
			state = guestlease.Released
		}
		return guestlease.Observation{Intent: i, Lease: "guest-" + i.Fence, State: state}, nil
	}
	s, err := NewEC2GuestLeaseStore(db, b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func faultLeases(t *testing.T, s *EC2GuestLeaseStore, count int) []EC2GuestLease {
	t.Helper()
	var leases []EC2GuestLease
	for n := 0; n < count; n++ {
		o := leaseOwner()
		o.Instance = fmt.Sprintf("i-%02d", n)
		r, err := s.Acquire(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if r, err = s.Reconcile(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		leases = append(leases, r)
	}
	return leases
}

func TestEC2GuestLeaseStorageFaultsRecoverOnRetry(t *testing.T) {
	t.Run("acquire and reconcile", func(t *testing.T) {
		sweepStorageFaultsRecover(t, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := faultLeaseStore(t, db)
			return func(ctx context.Context) error {
				r, err := s.Acquire(ctx, leaseOwner())
				if err != nil {
					// A retry must find the durable intent instead of forking a lease.
					if r, err = s.Get(ctx, leaseOwner()); err != nil {
						return err
					}
				}
				r, err = s.Reconcile(ctx, r)
				if err == nil && r.State != "held" {
					t.Fatalf("lease state %v", r.State)
				}
				return err
			}
		})
	})
	t.Run("advance to containment", func(t *testing.T) {
		sweepStorageFaultsRecover(t, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := faultLeaseStore(t, db)
			owner := faultLeases(t, s, 1)[0].Owner
			return func(ctx context.Context) error {
				current, err := s.Get(ctx, owner)
				if err != nil {
					return err
				}
				if current.State == "held" {
					if current, err = s.Advance(ctx, current); err != nil {
						return err
					}
				}
				_, err = s.Reconcile(ctx, current)
				return err
			}
		})
	})
	t.Run("list", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := faultLeaseStore(t, db)
			faultLeases(t, s, 2)
			return func(ctx context.Context) error {
				got, err := s.List(ctx, "000000000000", "us-east-1", "", 10)
				if err == nil && len(got) != 2 {
					t.Fatalf("listed %d leases", len(got))
				}
				return err
			}
		})
	})
}

func faultCommandStore(t *testing.T, db *sql.DB, leases int) (*SSMCommandStore, SSMCommandAuthority, []EC2GuestLease) {
	t.Helper()
	ls := faultLeaseStore(t, db)
	held := faultLeases(t, ls, leases)
	b := &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }, observe: func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Success", StartedAt: i.RequestedAt, FinishedAt: i.RequestedAt, Stdout: "hello"}, nil
	}}
	s, err := NewSSMCommandStore(db, b)
	if err != nil {
		t.Fatal(err)
	}
	return s, SSMCommandAuthority{Account: "000000000000", Region: "us-east-1", Principal: "organizer", Deployment: leaseOwner().Deployment}, held
}

func TestSSMCommandStoreStorageFaults(t *testing.T) {
	t.Run("send", func(t *testing.T) {
		sweepStorageFaults(t, false, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, a, leases := faultCommandStore(t, db, 2)
			return func(ctx context.Context) error {
				c, err := s.Send(ctx, a, ssmCommandRequestFixture(leases), leases)
				if err == nil && len(c.Request.InstanceIds) != 2 {
					t.Fatalf("command targets %v", c.Request.InstanceIds)
				}
				return err
			}
		})
	})
	t.Run("reconcile and read", func(t *testing.T) {
		sweepStorageFaultsRecover(t, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, a, leases := faultCommandStore(t, db, 1)
			sent, err := s.Send(context.Background(), a, ssmCommandRequestFixture(leases), leases)
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				if _, err := s.Reconcile(ctx, a, sent.CommandId, leases[0].Owner.Instance); err != nil {
					return err
				}
				got, err := s.Get(ctx, a, sent.CommandId)
				if err != nil {
					return err
				}
				if !got.Successful() {
					t.Fatalf("reconciled command not successful: %+v", got)
				}
				_, err = s.GetInvocation(ctx, a, sent.CommandId, leases[0].Owner.Instance)
				return err
			}
		})
	})
	t.Run("paged list", func(t *testing.T) {
		sweepStorageFaults(t, false, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, a, leases := faultCommandStore(t, db, 1)
			for i := 0; i < 2; i++ {
				if _, err := s.Send(context.Background(), a, ssmCommandRequestFixture(leases), leases); err != nil {
					t.Fatal(err)
				}
			}
			return func(ctx context.Context) error {
				first, token, err := s.List(ctx, a, "1970-01-01T00:00:00Z", 1, "")
				if err != nil {
					return err
				}
				if len(first) != 1 || token == "" {
					t.Fatalf("first page %v %q", first, token)
				}
				last, next, err := s.List(ctx, a, "1970-01-01T00:00:00Z", 1, token)
				if err == nil && (len(last) != 1 || next != "" || last[0].CommandId == first[0].CommandId) {
					t.Fatalf("second page %v %q", last, next)
				}
				return err
			}
		})
	})
}

func nativeListFixture(t *testing.T, db *sql.DB, commands int) *SSMCommandAPI {
	t.Helper()
	ls := faultLeaseStore(t, db)
	leases := faultLeases(t, ls, 1)
	double := &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { return nil }, observe: func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		return guestcommand.DurableObservation{Intent: i, State: "Unknown"}, nil
	}}
	backend := &nativeCommandBackendDouble{ssmCommandDouble: double}
	backend.resolve = func(context.Context, string, string) (EC2GuestLease, error) { return leases[0], nil }
	store, err := NewSSMCommandStore(db, backend)
	if err != nil {
		t.Fatal(err)
	}
	a := SSMCommandAuthority{Account: "000000000000", Region: "us-east-1", Principal: "organizer", Deployment: leaseOwner().Deployment}
	for i := 0; i < commands; i++ {
		if _, err = store.Send(context.Background(), a, ssmCommandRequestFixture(leases), leases); err != nil {
			t.Fatal(err)
		}
	}
	api, err := NewSSMCommandAPI(store)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestSSMCommandNativeListStorageFaults(t *testing.T) {
	sweepStorageFaults(t, false, func(t *testing.T, db *sql.DB) func(context.Context) error {
		api := nativeListFixture(t, db, 3)
		return func(ctx context.Context) error {
			input := nativeCommandInput{InvokedAfter: "1970-01-01T00:00:00Z", MaxResults: 2}
			first, token, err := api.list(ctx, "organizer", "us-east-1", input)
			if err != nil {
				return err
			}
			if len(first) != 2 || token == "" {
				t.Fatalf("first page %d %q", len(first), token)
			}
			input.NextToken = token
			rest, next, err := api.list(ctx, "organizer", "us-east-1", input)
			if err == nil && (len(rest) != 1 || next != "") {
				t.Fatalf("second page %d %q", len(rest), next)
			}
			return err
		}
	})
}

// A stored listing cursor is an immutable snapshot bound to its caller: tokens
// are refused when malformed, foreign, out of range, or when the stored
// snapshot was altered.
func TestSSMCommandNativeListRefusesForeignOrAlteredCursors(t *testing.T) {
	db := newFaultDB(t)
	api := nativeListFixture(t, db.DB, 3)
	ctx := context.Background()
	input := nativeCommandInput{InvokedAfter: "1970-01-01T00:00:00Z", MaxResults: 1}
	_, token, err := api.list(ctx, "organizer", "us-east-1", input)
	if err != nil || token == "" {
		t.Fatal(token, err)
	}
	prefix := token[:strings.Index(token, ".")]
	for name, tc := range map[string]struct{ principal, region, token string }{
		"malformed":       {"organizer", "us-east-1", "not-a-token"},
		"zero offset":     {"organizer", "us-east-1", prefix + ".0"},
		"padded offset":   {"organizer", "us-east-1", prefix + ".01"},
		"unknown token":   {"organizer", "us-east-1", strings.Repeat("0", len(prefix)) + ".1"},
		"other principal": {"intruder", "us-east-1", token},
		"other region":    {"organizer", "us-west-2", token},
	} {
		in := input
		in.NextToken = tc.token
		if _, _, err := api.list(ctx, tc.principal, tc.region, in); err == nil {
			t.Errorf("%s cursor accepted", name)
		}
	}
	if _, _, err := api.list(ctx, "organizer", "us-east-1", nativeCommandInput{InvokedAfter: "yesterday", MaxResults: 1}); err == nil {
		t.Error("unparseable InvokedAfter accepted")
	}
	var raw string
	if err = db.QueryRow(`SELECT record FROM aws_ssm_native_command_pages WHERE token=?`, prefix).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*nativeCommandPage){
		"duplicate command": func(p *nativeCommandPage) { p.Commands[1] = p.Commands[0] },
		"reordered":         func(p *nativeCommandPage) { p.Commands[0], p.Commands[1] = p.Commands[1], p.Commands[0] },
		"bad command ID":    func(p *nativeCommandPage) { p.Commands[1].CommandId = "bogus" },
		"bad InvokedAfter":  func(p *nativeCommandPage) { p.InvokedAfter = "later" },
		"forged request":    func(p *nativeCommandPage) { p.Commands[1].Request.Comment = "forged" },
	} {
		var page nativeCommandPage
		if err = json.Unmarshal([]byte(raw), &page); err != nil {
			t.Fatal(err)
		}
		alter(&page)
		if _, err = db.Exec(`UPDATE aws_ssm_native_command_pages SET record=? WHERE token=?`, ssmEncode(page), prefix); err != nil {
			t.Fatal(err)
		}
		in := input
		in.NextToken = token
		if _, _, err := api.list(ctx, "organizer", "us-east-1", in); err == nil {
			t.Errorf("%s snapshot accepted", name)
		}
	}
}
