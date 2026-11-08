package aws

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func profileStore(t *testing.T) *IAMRoleStore {
	t.Helper()
	db, _ := s3TestState(t)
	s, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func profileCreate(t *testing.T, s *IAMRoleStore, name string) IAMInstanceProfile {
	t.Helper()
	p, e := s.CreateInstanceProfile(context.Background(), name)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func profileRole(t *testing.T, s *IAMRoleStore, name string) IAMRole {
	t.Helper()
	r, e := s.Create(context.Background(), name, lambdaServiceTrust, "", 3600)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func profileSQL(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, e := db.Exec(q, args...); e != nil {
		t.Fatal(e)
	}
}
func TestIAMInstanceProfilesLifecycleGenerationsAndCleanup(t *testing.T) {
	ctx := context.Background()
	s := profileStore(t)
	p := profileCreate(t, s, "Profile")
	r := profileRole(t, s, "Role")
	other := profileRole(t, s, "Other")
	if p.Path != "/" || p.Arn != iamInstanceProfileARN+"Profile" || !iamInstanceProfileID.MatchString(p.InstanceProfileID) || len(p.Roles) != 0 {
		t.Fatal(p)
	}
	if _, e := s.CreateInstanceProfile(ctx, "profile"); iamCode(e) != "EntityAlreadyExists" {
		t.Fatal(e)
	}
	if _, e := s.CreateInstanceProfile(ctx, "bad/name"); iamCode(e) != "ValidationError" {
		t.Fatal(e)
	}
	if e := s.ChangeInstanceProfileRole(ctx, "profile", p.InstanceProfileID, "role", r.RoleID, true); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetInstanceProfile(ctx, "PROFILE", p.InstanceProfileID)
	if e != nil || len(got.Roles) != 1 || got.Roles[0].RoleName != "Role" {
		t.Fatal(got, e)
	}
	if e = s.Delete(ctx, r.RoleName, r.RoleID); iamCode(e) != "DeleteConflict" {
		t.Fatal(e)
	}
	if e = s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); iamCode(e) != "DeleteConflict" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true); iamCode(e) != "EntityAlreadyExists" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, other.RoleName, other.RoleID, true); iamCode(e) != "LimitExceeded" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, other.RoleName, other.RoleID, false); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, false); e != nil {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, r.RoleName, r.RoleID); e != nil {
		t.Fatal(e)
	}
	r2 := profileRole(t, s, "Role")
	if r2.RoleID == r.RoleID {
		t.Fatal("reused role ID")
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if e = s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e != nil {
		t.Fatal(e)
	}
	p2 := profileCreate(t, s, "profile")
	if p2.InstanceProfileID == p.InstanceProfileID {
		t.Fatal("reused profile ID")
	}
	if e = s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if _, e = s.GetInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r2.RoleName, r2.RoleID, true); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p2.InstanceProfileName, p2.InstanceProfileID, "legacy", "AROA0000000000000000", true); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if e = s.DeleteInstanceProfile(ctx, p2.InstanceProfileName, p2.InstanceProfileID); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.db.QueryRow(`SELECT count(*) FROM aws_iam_instance_profiles WHERE deleted=1`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestIAMInstanceProfilesFaultsRollbackAndCorruption(t *testing.T) {
	for _, operation := range []string{"create", "add", "remove", "delete"} {
		for _, fault := range []string{"ignore", "abort", "undo"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				s := profileStore(t)
				ctx := context.Background()
				r := profileRole(t, s, "role")
				var p IAMInstanceProfile
				if operation != "create" {
					p = profileCreate(t, s, "profile")
				}
				if operation == "remove" {
					if e := s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true); e != nil {
						t.Fatal(e)
					}
				}
				event, table := "INSERT", "aws_iam_instance_profiles"
				switch operation {
				case "add":
					table = "aws_iam_instance_profile_roles"
				case "remove":
					event = "DELETE"
					table = "aws_iam_instance_profile_roles"
				case "delete":
					event = "UPDATE"
				}
				body := "SELECT RAISE(IGNORE);"
				timing := "BEFORE"
				if fault == "abort" {
					body = "SELECT RAISE(ABORT,'injected');"
				}
				if fault == "undo" {
					timing = "AFTER"
					switch operation {
					case "create":
						body = "UPDATE aws_iam_instance_profiles SET created='bad' WHERE name=NEW.name;"
					case "add":
						body = "DELETE FROM aws_iam_instance_profile_roles WHERE profile_name=NEW.profile_name;"
					case "remove":
						body = "INSERT INTO aws_iam_instance_profile_roles VALUES(OLD.profile_name,OLD.profile_id,OLD.role_name,OLD.role_id);"
					case "delete":
						body = "UPDATE aws_iam_instance_profiles SET deleted=0 WHERE name=NEW.name;"
					}
				}
				profileSQL(t, s.db, "CREATE TRIGGER injected "+timing+" "+event+" ON "+table+" BEGIN "+body+" END")
				var e error
				switch operation {
				case "create":
					_, e = s.CreateInstanceProfile(ctx, "profile")
				case "add":
					e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true)
				case "remove":
					e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, false)
				case "delete":
					e = s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID)
				}
				if e == nil {
					t.Fatal("ignored or corrupted write reported success")
				}
				profileSQL(t, s.db, "DROP TRIGGER injected")
				got, e := s.GetInstanceProfile(ctx, "profile")
				if operation == "create" {
					if iamCode(e) != "NoSuchEntity" {
						t.Fatal(got, e)
					}
					return
				}
				want := 0
				if operation == "remove" {
					want = 1
				}
				if e != nil || got.InstanceProfileID != p.InstanceProfileID || len(got.Roles) != want {
					t.Fatal("partial write", got, e)
				}
			})
		}
	}
	for _, column := range []string{"created", "profile_id"} {
		t.Run("corrupt/"+column, func(t *testing.T) {
			s := profileStore(t)
			p := profileCreate(t, s, "profile")
			profileSQL(t, s.db, "UPDATE aws_iam_instance_profiles SET "+column+"='bad'")
			if _, e := s.GetInstanceProfile(context.Background(), p.InstanceProfileName); e == nil {
				t.Fatal("corruption exposed")
			}
			if _, e := s.ListInstanceProfiles(context.Background(), "", 100, "", ""); e == nil {
				t.Fatal("corrupt listing")
			}
		})
	}
}
func TestIAMInstanceProfilesPersistencePaginationAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "profiles.sqlite")
	db, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	r := profileRole(t, s, "role")
	for _, name := range []string{"Zebra", "alpha", "Beta"} {
		p := profileCreate(t, s, name)
		if name != "Beta" {
			if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true); e != nil {
				t.Fatal(e)
			}
		}
	}
	db.Close()
	db, e = core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e = NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	all, e := s.ListInstanceProfiles(ctx, "", 2, "", "")
	if e != nil || len(all) != 2 || all[0].InstanceProfileName != "alpha" || all[1].InstanceProfileName != "Beta" {
		t.Fatal(all, e)
	}
	last, e := s.ListInstanceProfiles(ctx, "beta", 2, "", "")
	if e != nil || len(last) != 1 || last[0].InstanceProfileName != "Zebra" {
		t.Fatal(last, e)
	}
	matching, e := s.ListInstanceProfiles(ctx, "", 100, r.RoleName, r.RoleID)
	if e != nil || len(matching) != 2 {
		t.Fatal(matching, e)
	}
	if _, e = s.ListInstanceProfiles(ctx, "", 100, r.RoleName, "stale"); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`INSERT INTO aws_iam_instance_profile_roles VALUES('Beta','stale','role',?)`, r.RoleID); e == nil {
		t.Fatal("missing composite FK")
	}
	for _, p := range matching {
		if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, false); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Delete(ctx, r.RoleName, r.RoleID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListInstanceProfiles(ctx, "", 0, "", ""); iamCode(e) != "InvalidInput" {
		t.Fatal(e)
	}
	for _, p := range append(all, last...) {
		if e = s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e != nil {
			t.Fatal(e)
		}
	}
	clean, e := s.ListInstanceProfiles(ctx, "", 100, "", "")
	if e != nil || len(clean) != 0 {
		t.Fatal(clean, e)
	}
}

func TestIAMInstanceProfilesRecreationFaultsPreserveTombstone(t *testing.T) {
	for _, fault := range []string{"ignore", "abort", "undo"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			s := profileStore(t)
			p := profileCreate(t, s, "Retired")
			if e := s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e != nil {
				t.Fatal(e)
			}
			snapshot := func() [4]string {
				t.Helper()
				var row [4]string
				if e := s.db.QueryRow(`SELECT name,profile_id,created,CAST(deleted AS TEXT) FROM aws_iam_instance_profiles WHERE name='Retired'`).Scan(&row[0], &row[1], &row[2], &row[3]); e != nil {
					t.Fatal(e)
				}
				return row
			}
			before := snapshot()
			timing, body := "BEFORE", "SELECT RAISE(IGNORE);"
			if fault == "abort" {
				body = "SELECT RAISE(ABORT,'injected recreation');"
			}
			if fault == "undo" {
				timing = "AFTER"
				body = "UPDATE aws_iam_instance_profiles SET name=OLD.name,profile_id=OLD.profile_id,created=OLD.created,deleted=OLD.deleted WHERE name=NEW.name;"
			}
			profileSQL(t, s.db, "CREATE TRIGGER injected "+timing+" UPDATE ON aws_iam_instance_profiles BEGIN "+body+" END")
			if _, e := s.CreateInstanceProfile(ctx, "RETIRED"); e == nil {
				t.Fatal("failed recreation reported success")
			}
			profileSQL(t, s.db, "DROP TRIGGER injected")
			if after := snapshot(); after != before {
				t.Fatal("retired incarnation changed on failed recreation", before, after)
			}
			if _, e := s.GetInstanceProfile(ctx, "Retired"); iamCode(e) != "NoSuchEntity" {
				t.Fatal(e)
			}
			fresh := profileCreate(t, s, "RETIRED")
			if fresh.InstanceProfileID == p.InstanceProfileID || fresh.InstanceProfileName != "RETIRED" || len(fresh.Roles) != 0 {
				t.Fatal(fresh)
			}
		})
	}
}

func TestIAMInstanceProfilesInitializeOverExistingRoleSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.sqlite")
	db, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewIAMRoleStore(db.Database())
	if e != nil {
		db.Close()
		t.Fatal(e)
	}
	role := profileRole(t, s, "ExistingRole")
	// Reconstruct the pre-profile schema, not an old-binary execution claim.
	profileSQL(t, s.db, "DROP TABLE aws_iam_instance_profile_roles; DROP TABLE aws_iam_instance_profiles")
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e = NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	p := profileCreate(t, s, "NewProfile")
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetInstanceProfile(ctx, p.InstanceProfileName)
	if e != nil || !reflect.DeepEqual(got.Roles, []IAMRole{role}) {
		t.Fatal("upgrade changed the existing native role", got, e)
	}
	if _, e = NewIAMRoleStore(db.Database()); e != nil {
		t.Fatal("schema initialization was not idempotent", e)
	}
	if e = s.Delete(ctx, role.RoleName, role.RoleID); iamCode(e) != "DeleteConflict" {
		t.Fatal(e)
	}
	if e = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, role.RoleName, role.RoleID, false); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e != nil {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, role.RoleName, role.RoleID); e != nil {
		t.Fatal(e)
	}
}
func TestIAMInstanceProfilesQuotaAndValidation(t *testing.T) {
	s := profileStore(t)
	ctx := context.Background()
	p := profileCreate(t, s, "original")
	profileSQL(t, s.db, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO aws_iam_instance_profiles SELECT 'name'||x,'AIPA0000000000000000','2026-01-01T00:00:00Z',1 FROM n`)
	if _, e := s.CreateInstanceProfile(ctx, "overflow"); iamCode(e) != "LimitExceeded" {
		t.Fatal(e)
	}
	if e := s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e != nil {
		t.Fatal(e)
	}
	profileCreate(t, s, "original")
	if _, e := s.CreateInstanceProfile(ctx, strings.Repeat("a", 129)); iamCode(e) != "ValidationError" {
		t.Fatal(e)
	}
	if e := s.DeleteInstanceProfile(ctx, "original", ""); iamCode(e) != "InvalidInput" {
		t.Fatal(e)
	}
	if _, e := s.GetInstanceProfile(ctx, "original", "a", "b"); e == nil {
		t.Fatal("multiple generations")
	}
}

func TestIAMInstanceProfilesCorruptMembershipFailsClosed(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE aws_iam_instance_profile_roles SET profile_name='PROFILE'`,
		`UPDATE aws_iam_instance_profile_roles SET role_name='ROLE'`,
		`UPDATE aws_iam_roles SET deleted=1`,
		`UPDATE aws_iam_roles SET trust='bad'`,
		`UPDATE aws_iam_roles SET duration=0`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := profileStore(t)
			ctx := context.Background()
			p := profileCreate(t, s, "profile")
			r := profileRole(t, s, "role")
			if e := s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true); e != nil {
				t.Fatal(e)
			}
			profileSQL(t, s.db, mutation)
			if _, e := s.GetInstanceProfile(ctx, "profile"); e == nil {
				t.Fatal("corrupt membership exposed")
			}
			if _, e := s.ListInstanceProfiles(ctx, "", 100, "", ""); e == nil {
				t.Fatal("corrupt listing exposed")
			}
			if e := s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); e == nil {
				t.Fatal("corrupt profile deleted")
			}
		})
	}
}
func TestIAMInstanceProfilesClosedStoreErrors(t *testing.T) {
	s := profileStore(t)
	s.db.Close()
	ctx := context.Background()
	if _, e := s.CreateInstanceProfile(ctx, "p"); e == nil {
		t.Fatal("closed create")
	}
	if _, e := s.GetInstanceProfile(ctx, "p"); e == nil {
		t.Fatal("closed get")
	}
	if _, e := s.ListInstanceProfiles(ctx, "", 100, "", ""); e == nil {
		t.Fatal("closed list")
	}
	if e := s.DeleteInstanceProfile(ctx, "p", "id"); e == nil {
		t.Fatal("closed delete")
	}
	if e := s.ChangeInstanceProfileRole(ctx, "p", "id", "r", "id", true); e == nil {
		t.Fatal("closed add")
	}
}
func TestIAMInstanceProfilesCompetingAddsAreAtomic(t *testing.T) {
	s := profileStore(t)
	ctx := context.Background()
	p := profileCreate(t, s, "profile")
	a := profileRole(t, s, "a")
	b := profileRole(t, s, "b")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, r := range []IAMRole{a, b} {
		go func() {
			<-start
			results <- s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first != nil && second != nil || first == nil && second == nil {
		t.Fatal(first, second)
	}
	failure := first
	if failure == nil {
		failure = second
	}
	if iamCode(failure) != "LimitExceeded" {
		t.Fatal(failure)
	}
	got, e := s.GetInstanceProfile(ctx, p.InstanceProfileName)
	if e != nil || len(got.Roles) != 1 {
		t.Fatal(got, e)
	}
}
