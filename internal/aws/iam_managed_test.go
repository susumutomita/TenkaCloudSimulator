package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const managedAllow = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::fixture/*"}]}`
const managedDeny = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::fixture/*"}]}`

func managedStore(t *testing.T) (*core.Store, *IAMRoleStore) {
	t.Helper()
	db, _ := s3TestState(t)
	s, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	return db, s
}
func managedRole(t *testing.T, s *IAMRoleStore, name string) IAMRole {
	t.Helper()
	r, e := s.Create(context.Background(), name, lambdaServiceTrust, "", 3600)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func managedPolicy(t *testing.T, s *IAMRoleStore, name, doc string) IAMManagedPolicy {
	t.Helper()
	p, e := s.CreateManagedPolicy(context.Background(), name, "/", "dummy description", doc)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func managedFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	all := map[string][][]any{}
	for _, table := range []string{"aws_iam_roles", "aws_iam_role_policies", "identity_policies", "aws_iam_managed_policies", "aws_iam_managed_policy_versions", "aws_iam_role_managed_policies"} {
		rows, e := db.Query("SELECT * FROM " + table + " ORDER BY 1,2")
		if e != nil {
			t.Fatal(e)
		}
		columns, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		values := [][]any{}
		for rows.Next() {
			v := make([]any, len(columns))
			dest := make([]any, len(v))
			for i := range v {
				dest[i] = &v[i]
			}
			if e = rows.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			values = append(values, v)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
		all[table] = values
	}
	data, e := json.Marshal(all)
	if e != nil {
		t.Fatal(e)
	}
	return string(data)
}
func managedDecision(t *testing.T, db *core.Store, role, want string) {
	t.Helper()
	got, e := core.NewIdentities(db).Decision(role, "s3:GetObject", "arn:aws:s3:::fixture/key")
	if e != nil || got != want {
		t.Fatalf("decision %s, wanted %s (error %v)", got, want, e)
	}
}
func TestManagedIAMSharedDefaultsInlineUnionAndGeneration(t *testing.T) {
	ctx := context.Background()
	db, s := managedStore(t)
	a := managedRole(t, s, "alpha")
	b := managedRole(t, s, "beta")
	peer := managedRole(t, s, "peer")
	if e := s.PutPolicy(ctx, a.RoleName, "inline", managedAllow); e != nil {
		t.Fatal(e)
	}
	p := managedPolicy(t, s, "Shared", managedAllow)
	for _, r := range []IAMRole{a, b} {
		if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.AttachRolePolicy(ctx, a.RoleName, p.Arn); e != nil {
		t.Fatal(e)
	}
	meta, e := s.ManagedPolicy(ctx, p.Arn)
	if e != nil || meta.AttachmentCount != 2 {
		t.Fatal(meta, e)
	}
	v, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, false)
	if e != nil || v.VersionID != "v2" || v.IsDefaultVersion {
		t.Fatal(v, e)
	}
	managedDecision(t, db, a.RoleName, "allowed")
	managedDecision(t, db, b.RoleName, "allowed")
	if e = s.SetDefaultManagedPolicyVersion(ctx, p.Arn, v.VersionID); e != nil {
		t.Fatal(e)
	}
	managedDecision(t, db, a.RoleName, "explicitDeny")
	managedDecision(t, db, b.RoleName, "explicitDeny")
	managedDecision(t, db, peer.RoleName, "implicitDeny")
	// Inline writes must retain attached managed Deny, including their delete path.
	if e = s.PutPolicy(ctx, a.RoleName, "second", managedAllow); e != nil {
		t.Fatal(e)
	}
	if e = s.DeletePolicy(ctx, a.RoleName, "second"); e != nil {
		t.Fatal(e)
	}
	managedDecision(t, db, a.RoleName, "explicitDeny")
	before := managedFrame(t, db.Database())
	if iamCode(s.Delete(ctx, b.RoleName)) != "DeleteConflict" || iamCode(s.DeleteManagedPolicy(ctx, p.Arn)) != "DeleteConflict" || iamCode(s.DeleteManagedPolicyVersion(ctx, p.Arn, "v2")) != "DeleteConflict" {
		t.Fatal("dependency not preserved")
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("conflict mutated state")
	}
	if e = s.DetachRolePolicy(ctx, a.RoleName, p.Arn); e != nil {
		t.Fatal(e)
	}
	managedDecision(t, db, a.RoleName, "allowed")
	if e = s.DetachRolePolicy(ctx, b.RoleName, p.Arn); e != nil {
		t.Fatal(e)
	}
	managedDecision(t, db, b.RoleName, "implicitDeny")
	before = managedFrame(t, db.Database())
	if e = s.DetachRolePolicy(ctx, b.RoleName, p.Arn); e != nil {
		t.Fatal(e)
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("absent detach was not no-op")
	}
	if e = s.DeleteManagedPolicyVersion(ctx, p.Arn, "v1"); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteManagedPolicy(ctx, p.Arn); e != nil {
		t.Fatal(e)
	}
	replacement := managedPolicy(t, s, "shared", managedDeny)
	if replacement.PolicyID == p.PolicyID {
		t.Fatal("policy generation reused")
	}
	if e = s.Delete(ctx, b.RoleName); e != nil {
		t.Fatal(e)
	}
	fresh := managedRole(t, s, b.RoleName)
	if fresh.RoleID == b.RoleID {
		t.Fatal("role generation reused")
	}
	_, items, e := s.AttachedRolePolicies(ctx, fresh.RoleName, "", 1001)
	if e != nil || len(items) != 0 {
		t.Fatal(items, e)
	}
	managedDecision(t, db, fresh.RoleName, "implicitDeny")
}
func TestManagedIAMDefaultSwitchAllRoleRollbackAndRepair(t *testing.T) {
	ctx := context.Background()
	for _, createDefault := range []bool{false, true} {
		t.Run(fmt.Sprint(createDefault), func(t *testing.T) {
			db, s := managedStore(t)
			managedRole(t, s, "alpha")
			managedRole(t, s, "beta")
			managedRole(t, s, "peer")
			p := managedPolicy(t, s, "Shared", managedAllow)
			for _, name := range []string{"alpha", "beta"} {
				if e := s.AttachRolePolicy(ctx, name, p.Arn); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, false); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Database().Exec(`CREATE TRIGGER second_role_ignore BEFORE UPDATE ON identity_policies WHEN NEW.name='beta' BEGIN SELECT RAISE(IGNORE); END`); e != nil {
				t.Fatal(e)
			}
			before := managedFrame(t, db.Database())
			var e error
			if createDefault {
				_, e = s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true)
			} else {
				e = s.SetDefaultManagedPolicyVersion(ctx, p.Arn, "v2")
			}
			if e == nil {
				t.Fatal("ignored second-role update accepted")
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("default transaction partially changed metadata/versions/first role/peer")
			}
			managedDecision(t, db, "alpha", "allowed")
			managedDecision(t, db, "beta", "allowed")
			if _, e = db.Database().Exec(`DROP TRIGGER second_role_ignore`); e != nil {
				t.Fatal(e)
			}
			if createDefault {
				v, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true)
				if e != nil || v.VersionID != "v3" {
					t.Fatal(v, e)
				}
			} else if e = s.SetDefaultManagedPolicyVersion(ctx, p.Arn, "v2"); e != nil {
				t.Fatal(e)
			}
			managedDecision(t, db, "alpha", "explicitDeny")
			managedDecision(t, db, "beta", "explicitDeny")
			managedDecision(t, db, "peer", "implicitDeny")
		})
	}
}
func TestManagedIAMIgonoredWritesAreNotAcknowledged(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"policy-create", "version-create", "attach", "detach", "version-delete", "policy-delete", "inline-put", "inline-delete", "role-delete", "aggregate-delete"} {
		t.Run(operation, func(t *testing.T) {
			db, s := managedStore(t)
			r := managedRole(t, s, "role")
			managedRole(t, s, "peer")
			p := managedPolicy(t, s, "policy", managedAllow)
			target, event := "aws_iam_managed_policies", "INSERT"
			switch operation {
			case "version-create":
				target = "aws_iam_managed_policy_versions"
			case "attach":
				target = "aws_iam_role_managed_policies"
			case "detach", "aggregate-delete":
				if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
					t.Fatal(e)
				}
				target, event = "aws_iam_role_managed_policies", "DELETE"
				if operation == "aggregate-delete" {
					target = "identity_policies"
				}
			case "version-delete":
				if _, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, false); e != nil {
					t.Fatal(e)
				}
				target, event = "aws_iam_managed_policy_versions", "DELETE"
			case "policy-delete":
				target, event = "aws_iam_managed_policies", "UPDATE"
			case "inline-put":
				target = "aws_iam_role_policies"
			case "inline-delete":
				if e := s.PutPolicy(ctx, r.RoleName, "inline", managedAllow); e != nil {
					t.Fatal(e)
				}
				target, event = "aws_iam_role_policies", "DELETE"
			case "role-delete":
				target, event = "aws_iam_roles", "UPDATE"
			}
			if _, e := db.Database().Exec("CREATE TRIGGER ignored BEFORE " + event + " ON " + target + " BEGIN SELECT RAISE(IGNORE); END"); e != nil {
				t.Fatal(e)
			}
			run := func() error {
				switch operation {
				case "policy-create":
					_, e := s.CreateManagedPolicy(ctx, "second", "/", "", managedAllow)
					return e
				case "version-create":
					_, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true)
					return e
				case "attach":
					return s.AttachRolePolicy(ctx, r.RoleName, p.Arn)
				case "detach", "aggregate-delete":
					return s.DetachRolePolicy(ctx, r.RoleName, p.Arn)
				case "version-delete":
					return s.DeleteManagedPolicyVersion(ctx, p.Arn, "v2")
				case "policy-delete":
					return s.DeleteManagedPolicy(ctx, p.Arn)
				case "inline-put":
					return s.PutPolicy(ctx, r.RoleName, "inline", managedAllow)
				case "inline-delete":
					return s.DeletePolicy(ctx, r.RoleName, "inline")
				default:
					return s.Delete(ctx, r.RoleName)
				}
			}
			before := managedFrame(t, db.Database())
			if e := run(); e == nil {
				t.Fatal("unacknowledged mutation accepted")
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("ignored write changed frame")
			}
			if _, e := db.Database().Exec("DROP TRIGGER ignored"); e != nil {
				t.Fatal(e)
			}
			if e := run(); e != nil {
				t.Fatal("repair retry", e)
			}
		})
	}
}
func TestManagedIAMVersionCapsValidationAndCursorParents(t *testing.T) {
	ctx := context.Background()
	db, s := managedStore(t)
	r := managedRole(t, s, "role")
	p := managedPolicy(t, s, "Zulu", managedAllow)
	for n := 2; n <= 5; n++ {
		v, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedAllow, false)
		if e != nil || v.VersionID != fmt.Sprint("v", n) {
			t.Fatal(v, e)
		}
	}
	before := managedFrame(t, db.Database())
	if _, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedAllow, false); iamCode(e) != "LimitExceeded" {
		t.Fatal(e)
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("version limit changed frame")
	}
	if e := s.DeleteManagedPolicyVersion(ctx, p.Arn, "v3"); e != nil {
		t.Fatal(e)
	}
	v, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedAllow, false)
	if e != nil || v.VersionID != "v6" {
		t.Fatal(v, e)
	}
	meta, versions, e := s.ManagedPolicyVersions(ctx, p.Arn)
	if e != nil || meta.PolicyID != p.PolicyID || len(versions) != 5 || versions[2].VersionID != "v4" {
		t.Fatal(meta, versions, e)
	}
	for _, id := range []string{"v3", "v1.other", "v999999999999999999999999999999999999"} {
		if _, e = s.ManagedPolicyVersion(ctx, p.Arn, id); iamCode(e) != "NoSuchEntity" {
			t.Fatal(id, e)
		}
	}
	for _, id := range []string{"v0", "01", "v1!"} {
		if _, e = s.ManagedPolicyVersion(ctx, p.Arn, id); iamCode(e) != "InvalidInput" {
			t.Fatal(id, e)
		}
	}
	a := managedPolicy(t, s, "Alpha", managedAllow)
	list, e := s.ManagedPolicies(ctx, "", 1)
	if e != nil || len(list) != 1 || list[0].PolicyID != a.PolicyID {
		t.Fatal(list, e)
	}
	list, e = s.ManagedPolicies(ctx, "alpha", 1001)
	if e != nil || len(list) != 1 || list[0].PolicyID != p.PolicyID {
		t.Fatal(list, e)
	}
	if e = s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
		t.Fatal(e)
	}
	parent, items, e := s.AttachedRolePolicies(ctx, r.RoleName, "", 1001)
	if e != nil || parent.RoleID != r.RoleID || len(items) != 1 || items[0].PolicyArn != p.Arn {
		t.Fatal(parent, items, e)
	}
	for _, doc := range []string{strings.Repeat(" ", 32<<10) + managedAllow, strings.Replace(managedAllow, "fixture", "☃", 1), strings.Replace(managedAllow, "fixture", strings.Repeat(" ", 6200), 1), `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Condition":{}}]}`} {
		before = managedFrame(t, db.Database())
		if _, e = s.CreateManagedPolicyVersion(ctx, p.Arn, doc, false); iamCode(e) != "MalformedPolicyDocument" {
			t.Fatal("invalid policy accepted", e)
		}
		if managedFrame(t, db.Database()) != before {
			t.Fatal("invalid input mutation")
		}
	}
	// Formatting whitespace is excluded, while whitespace inside quoted JSON values is not.
	spaced := strings.Repeat(" \n", 4000) + managedAllow
	if _, e = s.CreateManagedPolicy(ctx, "Whitespace", "/", "", spaced); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateManagedPolicy(ctx, "Other", "/nonroot/", "", managedAllow); iamCode(e) != "NotImplemented" {
		t.Fatal(e)
	}
	if _, e = s.ManagedPolicy(ctx, "arn:aws:iam::aws:policy/AdministratorAccess"); iamCode(e) != "InvalidInput" {
		t.Fatal("AWS managed metadata granted", e)
	}
}
func TestManagedIAMCorruptionCancellationAndAggregateRollback(t *testing.T) {
	ctx := context.Background()
	for _, corrupt := range []string{"default", "document", "role-generation", "policy-generation", "aggregate"} {
		t.Run(corrupt, func(t *testing.T) {
			db, s := managedStore(t)
			r := managedRole(t, s, "role")
			p := managedPolicy(t, s, "policy", managedAllow)
			if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
				t.Fatal(e)
			}
			var undo string
			var args []any
			switch corrupt {
			case "default":
				_, e := db.Database().Exec(`UPDATE aws_iam_managed_policies SET default_version=99`)
				if e != nil {
					t.Fatal(e)
				}
				undo = `UPDATE aws_iam_managed_policies SET default_version=1`
			case "document":
				_, e := db.Database().Exec(`UPDATE aws_iam_managed_policy_versions SET document='{}'`)
				if e != nil {
					t.Fatal(e)
				}
				undo = `UPDATE aws_iam_managed_policy_versions SET document=?`
				args = []any{managedAllow}
			case "role-generation", "policy-generation":
				if _, e := db.Database().Exec(`PRAGMA foreign_keys=OFF`); e != nil {
					t.Fatal(e)
				}
				column, id := "role_id", r.RoleID
				if corrupt == "policy-generation" {
					column, id = "policy_id", p.PolicyID
				}
				if _, e := db.Database().Exec("UPDATE aws_iam_role_managed_policies SET " + column + "='stale-generation'"); e != nil {
					t.Fatal(e)
				}
				undo = "UPDATE aws_iam_role_managed_policies SET " + column + "=?"
				args = []any{id}
			default:
				if _, e := db.Database().Exec(`UPDATE identity_policies SET document='{}' WHERE name='role'`); e != nil {
					t.Fatal(e)
				}
				undo = `UPDATE identity_policies SET document=? WHERE name='role'`
				normalized, _ := core.ParsePolicy([]byte(managedAllow))
				data, _ := json.Marshal(normalized)
				args = []any{string(data)}
			}
			before := managedFrame(t, db.Database())
			_, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true)
			if e == nil {
				t.Fatal("corrupt state accepted")
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("corrupt-state failure mutated frame")
			}
			if _, e = db.Database().Exec(undo, args...); e != nil {
				t.Fatal(e)
			}
			if _, e = db.Database().Exec(`PRAGMA foreign_keys=ON`); e != nil {
				t.Fatal(e)
			}
			if _, e = s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true); e != nil {
				t.Fatal("repair retry", e)
			}
			managedDecision(t, db, r.RoleName, "explicitDeny")
		})
	}
	db, s := managedStore(t)
	r := managedRole(t, s, "role")
	p := managedPolicy(t, s, "policy", managedAllow)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	before := managedFrame(t, db.Database())
	actions := []func() error{func() error { return s.AttachRolePolicy(cancelled, r.RoleName, p.Arn) }, func() error { _, e := s.CreateManagedPolicyVersion(cancelled, p.Arn, managedDeny, true); return e }, func() error { _, e := s.ManagedPolicy(cancelled, p.Arn); return e }, func() error { return s.DeleteManagedPolicy(cancelled, p.Arn) }}
	for _, action := range actions {
		if e := action(); !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("cancel mutation")
	}
}
func TestManagedIAMAttachmentsAndInlineHaveSeparateQuotas(t *testing.T) {
	ctx := context.Background()
	db, s := managedStore(t)
	r := managedRole(t, s, "role")
	for n := 0; n < 10; n++ {
		if e := s.PutPolicy(ctx, r.RoleName, fmt.Sprint("inline", n), managedAllow); e != nil {
			t.Fatal(e)
		}
	}
	for n := 0; n < 20; n++ {
		p := managedPolicy(t, s, fmt.Sprintf("managed%02d", n), managedAllow)
		if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
			t.Fatal("managed cap was combined with inline", n, e)
		}
	}
	p := managedPolicy(t, s, "overflow", managedAllow)
	before := managedFrame(t, db.Database())
	if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); iamCode(e) != "LimitExceeded" {
		t.Fatal(e)
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("attachment limit changed state")
	}
	// A valid replacement default can overflow only the second attached role.
	db2, s2 := managedStore(t)
	a := managedRole(t, s2, "a")
	b := managedRole(t, s2, "b")
	shared := managedPolicy(t, s2, "shared", managedAllow)
	for _, r := range []IAMRole{a, b} {
		if e := s2.AttachRolePolicy(ctx, r.RoleName, shared.Arn); e != nil {
			t.Fatal(e)
		}
	}
	many := `{"Version":"2012-10-17","Statement":[` + strings.TrimSuffix(strings.Repeat(`{"Effect":"Allow","Action":"*","Resource":"*"},`, 99), ",") + `]}`
	if e := s2.PutPolicy(ctx, b.RoleName, "large", many); e != nil {
		t.Fatal(e)
	}
	two := strings.Replace(managedDeny, `}]}`, `},{"Effect":"Allow","Action":"*","Resource":"*"}]}`, 1)
	before = managedFrame(t, db2.Database())
	if _, e := s2.CreateManagedPolicyVersion(ctx, shared.Arn, two, true); iamCode(e) != "LimitExceeded" {
		t.Fatal("aggregate limit accepted", e)
	}
	if managedFrame(t, db2.Database()) != before {
		t.Fatal("second role overflow partially switched first")
	}
	managedDecision(t, db2, a.RoleName, "allowed")
}
func TestManagedIAMReopenAndTwoDatabases(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "iam.sqlite")
	db, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewIAMRoleStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	r := managedRole(t, s, "role")
	p := managedPolicy(t, s, "policy", managedAllow)
	if e = s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true); e != nil {
		t.Fatal(e)
	}
	frame := managedFrame(t, db.Database())
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
	if managedFrame(t, db.Database()) != frame {
		t.Fatal("reopen changed policy state")
	}
	managedDecision(t, db, r.RoleName, "explicitDeny")
	meta, versions, e := s.ManagedPolicyVersions(ctx, p.Arn)
	if e != nil || meta.PolicyID != p.PolicyID || meta.DefaultVersionID != "v2" || len(versions) != 2 {
		t.Fatal(meta, versions, e)
	}
	peer, ps := managedStore(t)
	pr := managedRole(t, ps, "role")
	pp := managedPolicy(t, ps, "policy", managedAllow)
	if pp.PolicyID == p.PolicyID {
		t.Fatal("same generation across databases")
	}
	if e = ps.AttachRolePolicy(ctx, pr.RoleName, pp.Arn); e != nil {
		t.Fatal(e)
	}
	managedDecision(t, peer, pr.RoleName, "allowed")
	managedDecision(t, db, r.RoleName, "explicitDeny")
}
