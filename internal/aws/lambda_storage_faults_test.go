package aws

import (
	"context"
	"database/sql"
	"testing"
)

// Function create, read and code update against a native execution role:
// each storage fault is reported and leaves no partial function record.
func TestLambdaFunctionStorageFaultsNeverPartiallyCommit(t *testing.T) {
	setup := func(t *testing.T, db *sql.DB, seeded bool) (*LambdaStore, IAMRole) {
		_, role := faultRoles(t, db, true)
		s, err := NewLambdaStore(db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if seeded {
			f := &LambdaFunction{Name: "fn", Region: "us-east-1", Role: role.Arn, Handler: "main.handler", Revision: "r1", Code: []byte("seed")}
			if err = s.save(f, true, ""); err != nil {
				t.Fatal(err)
			}
		}
		return s, role
	}
	t.Run("create", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, role := setup(t, db, false)
			return func(context.Context) error {
				return s.save(&LambdaFunction{Name: "fn", Region: "us-east-1", Role: role.Arn, Handler: "main.handler", Revision: "r1", Code: []byte("code")}, true, "")
			}
		})
	})
	t.Run("update", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, _ := setup(t, db, true)
			return func(context.Context) error {
				f := &LambdaFunction{Name: "fn", Region: "us-east-1", Revision: "r2", Code: []byte("updated")}
				if err := s.save(f, false, "r1"); err != nil {
					return err
				}
				if f.Handler != "main.handler" || string(f.Code) != "updated" {
					t.Fatalf("update did not merge onto the stored function: %+v", f)
				}
				return nil
			}
		})
	})
	t.Run("read", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, _ := setup(t, db, true)
			return func(context.Context) error {
				f, err := s.get("us-east-1", "fn")
				if err == nil && string(f.Code) != "seed" {
					t.Fatalf("read %+v", f)
				}
				return err
			}
		})
	})
}

func TestLambdaFunctionSaveRefusesConflictsAndUnknownRoles(t *testing.T) {
	db := newFaultDB(t)
	s, role := func() (*LambdaStore, IAMRole) {
		_, role := faultRoles(t, db.DB, true)
		s, err := NewLambdaStore(db.DB)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s, role
	}()
	fn := func() *LambdaFunction {
		return &LambdaFunction{Name: "fn", Region: "us-east-1", Role: role.Arn, Handler: "main.handler", Revision: "r1", Code: []byte("code")}
	}
	if err := s.save(fn(), false, ""); err == nil {
		t.Fatal("update of a missing function accepted")
	}
	if err := s.save(fn(), true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.save(fn(), true, ""); err == nil {
		t.Fatal("duplicate function accepted")
	}
	if err := s.save(&LambdaFunction{Name: "fn", Region: "us-east-1", Revision: "r3", Code: []byte("x")}, false, "stale"); err == nil {
		t.Fatal("stale revision accepted")
	}
	stranger := fn()
	stranger.Name, stranger.Role = "other", "arn:aws:iam::000000000000:role/nobody"
	if err := s.save(stranger, true, ""); err == nil {
		t.Fatal("unknown execution role accepted")
	}
	if _, err := db.Exec(`UPDATE aws_iam_roles SET deleted=1 WHERE name=?`, role.RoleName); err != nil {
		t.Fatal(err)
	}
	gone := fn()
	gone.Name = "after-delete"
	if err := s.save(gone, true, ""); err == nil {
		t.Fatal("deleted execution role accepted")
	}
	if _, err := s.get("us-east-1", "after-delete"); err == nil {
		t.Fatal("refused function persisted")
	}
}
