package aws

import (
	"context"
	"database/sql"
	"fmt"
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
