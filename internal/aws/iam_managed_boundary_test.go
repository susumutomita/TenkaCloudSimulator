package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Check the public error adapter too: SQL details/corrupt policy text are not
// allowed to escape as a successful or ordinary missing-resource response.
func managedBoundaryInfrastructure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("infrastructure/corrupt-state operation acknowledged success")
	}
	var failure ssmFailure
	if errors.As(err, &failure) {
		t.Fatal("persisted failure was classified as caller input", failure.code)
	}
	response := httptest.NewRecorder()
	iamError(response, err)
	if response.Code != 500 || !strings.Contains(response.Body.String(), "Local IAM state operation failed.") || strings.Contains(response.Body.String(), "boundary-private-fault") || strings.Contains(response.Body.String(), "SELECT ") {
		t.Fatal("infrastructure response was not sanitized")
	}
}

func TestManagedIAMBoundaryDocumentNameAndDescription(t *testing.T) {
	ctx := context.Background()
	db, s := managedStore(t)
	// Exactly 6144 compact runes, including quoted whitespace. Formatting is
	// independently padded to the 32KiB transport-independent raw document cap.
	base := strings.Replace(managedAllow, `"Effect":"Allow"`, `"Sid":"","Effect":"Allow"`, 1)
	compact := strings.Replace(base, `"Sid":""`, `"Sid":"`+strings.Repeat(" ", 6144-len(base))+`"`, 1)
	if len(compact) != 6144 {
		t.Fatal("boundary fixture is not exact")
	}
	raw := compact + strings.Repeat(" ", (32<<10)-len(compact))
	name := strings.Repeat("A", 128)
	description := strings.Repeat("é", 1000)
	p, e := s.CreateManagedPolicy(ctx, name, "/", description, raw)
	if e != nil {
		t.Fatal("document/description/name boundary rejected", e)
	}
	got, e := s.ManagedPolicyVersion(ctx, p.Arn, "v1")
	if e != nil || got.Document != raw {
		t.Fatal("boundary document bytes not retained", e)
	}
	meta, e := s.ManagedPolicy(ctx, p.Arn)
	if e != nil || meta.Description != description || meta.PolicyName != name {
		t.Fatal("boundary metadata changed", e)
	}
	invalid := []struct{ name, description, document, code string }{
		{"", "", managedAllow, "InvalidInput"},
		{strings.Repeat("A", 129), "", managedAllow, "InvalidInput"},
		{"bad/name", "", managedAllow, "InvalidInput"},
		{"BadDescription", strings.Repeat("x", 1001), managedAllow, "InvalidInput"},
		{"BadDescription", "\x00", managedAllow, "InvalidInput"},
		{"BadDescription", string([]byte{0xff}), managedAllow, "InvalidInput"},
		{"BadDescription", "雪", managedAllow, "InvalidInput"},
		{"BadDocument", "", "", "MalformedPolicyDocument"},
		{"BadDocument", "", raw + " ", "MalformedPolicyDocument"},
		{"BadDocument", "", strings.Replace(compact, `"Sid":"`, `"Sid":" `, 1), "MalformedPolicyDocument"},
		{"BadDocument", "", string([]byte{0xff}), "MalformedPolicyDocument"},
		{"BadDocument", "", `{"Version":"2012-10-17"} trailing`, "MalformedPolicyDocument"},
	}
	for i, tt := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			before := managedFrame(t, db.Database())
			_, e := s.CreateManagedPolicy(ctx, tt.name, "/", tt.description, tt.document)
			if iamCode(e) != tt.code {
				t.Fatal("invalid boundary classification", e)
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("invalid boundary wrote policy/peer data")
			}
		})
	}
	before := managedFrame(t, db.Database())
	if _, e = s.CreateManagedPolicy(ctx, strings.ToLower(name), "/", "", managedAllow); iamCode(e) != "EntityAlreadyExists" {
		t.Fatal("case-insensitive name claim reused", e)
	}
	if _, e = s.CreateManagedPolicy(ctx, "Other", "/nested/", "", managedAllow); iamCode(e) != "NotImplemented" {
		t.Fatal(e)
	}
	for _, limit := range []int{0, 1002} {
		if _, e = s.ManagedPolicies(ctx, "", limit); iamCode(e) != "InvalidInput" {
			t.Fatal(e)
		}
		if _, _, e = s.AttachedRolePolicies(ctx, "absent", "", limit); iamCode(e) != "InvalidInput" {
			t.Fatal(e)
		}
	}
	if _, e = s.ManagedPolicies(ctx, "bad/name", 10); iamCode(e) != "InvalidInput" {
		t.Fatal(e)
	}
	if _, _, e = s.AttachedRolePolicies(ctx, "absent", "bad/name", 10); iamCode(e) != "InvalidInput" {
		t.Fatal(e)
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("list/input refusals changed frame")
	}
}

func TestManagedIAMBoundaryMetadataReadCorruptionAndRepair(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, damage, repair string
		args                 bool
	}{
		{"created-date", `UPDATE aws_iam_managed_policies SET created='boundary-private-fault' WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET created=? WHERE name='policy'`, true},
		{"updated-date", `UPDATE aws_iam_managed_policies SET updated='boundary-private-fault' WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET updated=? WHERE name='policy'`, true},
		{"updated-before-created", `UPDATE aws_iam_managed_policies SET updated='1900-01-01T00:00:00Z' WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET updated=? WHERE name='policy'`, true},
		{"default-scan-type", `UPDATE aws_iam_managed_policies SET default_version='boundary-private-fault' WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET default_version=1 WHERE name='policy'`, false},
		{"invalid-next", `UPDATE aws_iam_managed_policies SET next_version=1 WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET next_version=2 WHERE name='policy'`, false},
		{"path", `UPDATE aws_iam_managed_policies SET path='/unsupported/' WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET path='/' WHERE name='policy'`, false},
		{"description", `UPDATE aws_iam_managed_policies SET description=char(0) WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET description='dummy description' WHERE name='policy'`, false},
		{"version-date", `UPDATE aws_iam_managed_policy_versions SET created='boundary-private-fault' WHERE policy_name='policy'`, `UPDATE aws_iam_managed_policy_versions SET created=? WHERE policy_name='policy'`, true},
		{"version-before-policy", `UPDATE aws_iam_managed_policy_versions SET created='1900-01-01T00:00:00Z' WHERE policy_name='policy'`, `UPDATE aws_iam_managed_policy_versions SET created=? WHERE policy_name='policy'`, true},
		{"version-scan-type", `UPDATE aws_iam_managed_policy_versions SET version_number='boundary-private-fault' WHERE policy_name='policy'`, `UPDATE aws_iam_managed_policy_versions SET version_number=1 WHERE policy_name='policy'`, false},
		{"missing-default", `UPDATE aws_iam_managed_policies SET default_version=2,next_version=3 WHERE name='policy'`, `UPDATE aws_iam_managed_policies SET default_version=1,next_version=2 WHERE name='policy'`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, s := managedStore(t)
			role := managedRole(t, s, "role")
			managedRole(t, s, "peer")
			p := managedPolicy(t, s, "policy", managedAllow)
			managedPolicy(t, s, "aaa-peer", managedDeny)
			if e := s.AttachRolePolicy(ctx, role.RoleName, p.Arn); e != nil {
				t.Fatal(e)
			}
			var saved string
			if tt.args {
				if strings.HasPrefix(tt.name, "version") {
					if e := db.Database().QueryRow(`SELECT created FROM aws_iam_managed_policy_versions WHERE policy_name='policy'`).Scan(&saved); e != nil {
						t.Fatal(e)
					}
				} else {
					column := "updated"
					if tt.name == "created-date" {
						column = "created"
					}
					if e := db.Database().QueryRow("SELECT " + column + " FROM aws_iam_managed_policies WHERE name='policy'").Scan(&saved); e != nil {
						t.Fatal(e)
					}
				}
			}
			if _, e := db.Database().Exec(tt.damage); e != nil {
				t.Fatal(e)
			}
			before := managedFrame(t, db.Database())
			_, e := s.ManagedPolicy(ctx, p.Arn)
			managedBoundaryInfrastructure(t, e)
			list, e := s.ManagedPolicies(ctx, "", 1001)
			managedBoundaryInfrastructure(t, e)
			if list != nil {
				t.Fatal("corrupt second list entry leaked partial first result")
			}
			parent, versions, e := s.ManagedPolicyVersions(ctx, p.Arn)
			managedBoundaryInfrastructure(t, e)
			if parent.PolicyID != "" || versions != nil {
				t.Fatal("corrupt read exposed version metadata")
			}
			if _, e = s.ManagedPolicyVersion(ctx, p.Arn, "v1"); e == nil {
				t.Fatal("version read ignored metadata corruption")
			}
			if _, e = s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true); e == nil {
				t.Fatal("write accepted damaged persisted metadata")
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("corrupt read/write changed peer or authority frame")
			}
			var args []any
			if tt.args {
				args = []any{saved}
			}
			if _, e = db.Database().Exec(tt.repair, args...); e != nil {
				t.Fatal(e)
			}
			if _, e = s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true); e != nil {
				t.Fatal("repair retry failed", e)
			}
			managedDecision(t, db, role.RoleName, "explicitDeny")
			managedDecision(t, db, "peer", "implicitDeny")
		})
	}
}

func TestManagedIAMBoundaryBrokenEdgesAndRetiredClaims(t *testing.T) {
	ctx := context.Background()
	for _, damage := range []string{"missing-role", "invalid-role-id", "retired-role", "extra-versions"} {
		t.Run(damage, func(t *testing.T) {
			db, s := managedStore(t)
			r := managedRole(t, s, "role")
			p := managedPolicy(t, s, "policy", managedAllow)
			if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Database().Exec(`PRAGMA foreign_keys=OFF`); e != nil {
				t.Fatal(e)
			}
			var repair string
			var args []any
			switch damage {
			case "missing-role":
				if _, e := db.Database().Exec(`UPDATE aws_iam_role_managed_policies SET role_name='missing-parent'`); e != nil {
					t.Fatal(e)
				}
				repair = `UPDATE aws_iam_role_managed_policies SET role_name='role'`
			case "invalid-role-id":
				if _, e := db.Database().Exec(`UPDATE aws_iam_roles SET role_id='boundary-private-fault' WHERE name='role'`); e != nil {
					t.Fatal(e)
				}
				repair = `UPDATE aws_iam_roles SET role_id=? WHERE name='role'`
				args = []any{r.RoleID}
			case "retired-role":
				if _, e := db.Database().Exec(`UPDATE aws_iam_roles SET deleted=1 WHERE name='role'`); e != nil {
					t.Fatal(e)
				}
				repair = `UPDATE aws_iam_roles SET deleted=0 WHERE name='role'`
			default:
				if _, e := db.Database().Exec(`UPDATE aws_iam_managed_policies SET next_version=7 WHERE name='policy'; INSERT INTO aws_iam_managed_policy_versions(policy_name,policy_id,version_number,document,created) SELECT policy_name,policy_id,x,document,created FROM aws_iam_managed_policy_versions CROSS JOIN (SELECT 2 x UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6) WHERE policy_name='policy' AND version_number=1`); e != nil {
					t.Fatal(e)
				}
				repair = `DELETE FROM aws_iam_managed_policy_versions WHERE version_number>1;UPDATE aws_iam_managed_policies SET next_version=2 WHERE name='policy'`
			}
			before := managedFrame(t, db.Database())
			_, e := s.ManagedPolicy(ctx, p.Arn)
			managedBoundaryInfrastructure(t, e)
			if e = s.SetDefaultManagedPolicyVersion(ctx, p.Arn, "v1"); e == nil {
				t.Fatal("corrupt graph update accepted")
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("corrupt graph changed authority frame")
			}
			if _, e = db.Database().Exec(repair, args...); e != nil {
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
	// Retired policy claims consume the real bounded namespace, yet a valid native
	// reactivation at capacity replaces its generation without adopting old edges.
	db, s := managedStore(t)
	active := managedPolicy(t, s, "active", managedAllow)
	if _, e := db.Database().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO aws_iam_managed_policies(name,policy_id,path,description,created,updated,default_version,next_version,deleted) SELECT printf('retired-%04d',x),printf('ANPA%016X',x),'/','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',1,2,1 FROM n`); e != nil {
		t.Fatal(e)
	}
	before := managedFrame(t, db.Database())
	if _, e := s.CreateManagedPolicy(ctx, "overflow", "/", "", managedAllow); iamCode(e) != "LimitExceeded" {
		t.Fatal(e)
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("claim quota changed frame")
	}
	p, e := s.CreateManagedPolicy(ctx, "retired-0001", "/", "", managedDeny)
	if e != nil || p.PolicyID == "ANPA0000000000000001" {
		t.Fatal("reactivation did not allocate fresh ID", e)
	}
	if meta, e := s.ManagedPolicy(ctx, active.Arn); e != nil || meta.PolicyID != active.PolicyID {
		t.Fatal("quota/recreation changed peer", e)
	}
	// A tombstone with leftover version authority must never be silently adopted.
	retired := managedPolicy(t, s, "retired-0002", managedAllow)
	if e = s.DeleteManagedPolicy(ctx, retired.Arn); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Database().Exec(`INSERT INTO aws_iam_managed_policy_versions(policy_name,policy_id,version_number,document,created) VALUES(?,?,1,?,'2026-01-01T00:00:00Z')`, retired.PolicyName, retired.PolicyID, managedAllow); e != nil {
		t.Fatal(e)
	}
	before = managedFrame(t, db.Database())
	_, e = s.CreateManagedPolicy(ctx, retired.PolicyName, "/", "", managedDeny)
	managedBoundaryInfrastructure(t, e)
	if managedFrame(t, db.Database()) != before {
		t.Fatal("corrupt tombstone adopted authority")
	}
	if _, e = db.Database().Exec(`DELETE FROM aws_iam_managed_policy_versions WHERE policy_name=?`, retired.PolicyName); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateManagedPolicy(ctx, retired.PolicyName, "/", "", managedDeny); e != nil {
		t.Fatal("tombstone repair retry", e)
	}
}

func managedBoundaryActions(s *IAMRoleStore, ctx context.Context, role, arn string) map[string]func() error {
	return map[string]func() error{
		"create-policy":  func() error { _, e := s.CreateManagedPolicy(ctx, "next-policy", "/", "", managedAllow); return e },
		"get-policy":     func() error { _, e := s.ManagedPolicy(ctx, arn); return e },
		"list-policies":  func() error { _, e := s.ManagedPolicies(ctx, "", 1001); return e },
		"delete-policy":  func() error { return s.DeleteManagedPolicy(ctx, arn) },
		"create-version": func() error { _, e := s.CreateManagedPolicyVersion(ctx, arn, managedDeny, true); return e },
		"get-version":    func() error { _, e := s.ManagedPolicyVersion(ctx, arn, "v1"); return e },
		"list-versions":  func() error { _, _, e := s.ManagedPolicyVersions(ctx, arn); return e },
		"set-default":    func() error { return s.SetDefaultManagedPolicyVersion(ctx, arn, "v1") },
		"delete-version": func() error { return s.DeleteManagedPolicyVersion(ctx, arn, "v2") },
		"attach":         func() error { return s.AttachRolePolicy(ctx, role, arn) },
		"detach":         func() error { return s.DetachRolePolicy(ctx, role, arn) },
		"list-attached":  func() error { _, _, e := s.AttachedRolePolicies(ctx, role, "", 1001); return e },
	}
}
func TestManagedIAMBoundaryClosedDatabaseAndSchemaFailures(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "closed.sqlite")
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
	before := managedFrame(t, db.Database())
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	for name, run := range managedBoundaryActions(s, ctx, r.RoleName, p.Arn) {
		t.Run("closed-"+name, func(t *testing.T) { managedBoundaryInfrastructure(t, run()) })
	}
	db, e = core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if managedFrame(t, db.Database()) != before {
		t.Fatal("closed-database calls changed persisted graph")
	}
	for _, tableColumn := range [][2]string{{"aws_iam_managed_policies", "created"}, {"aws_iam_managed_policy_versions", "document"}, {"aws_iam_role_managed_policies", "role_id"}} {
		t.Run(tableColumn[0], func(t *testing.T) {
			db, s := managedStore(t)
			r := managedRole(t, s, "role")
			p := managedPolicy(t, s, "policy", managedAllow)
			if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Database().Exec("ALTER TABLE " + tableColumn[0] + " RENAME COLUMN " + tableColumn[1] + " TO unavailable_column"); e != nil {
				t.Fatal(e)
			}
			frame := managedFrame(t, db.Database())
			for _, name := range []string{"get-policy", "list-policies", "get-version", "list-versions", "set-default", "create-version", "list-attached"} {
				run := managedBoundaryActions(s, ctx, r.RoleName, p.Arn)[name]
				managedBoundaryInfrastructure(t, run())
				if managedFrame(t, db.Database()) != frame {
					t.Fatal("schema failure partially changed data", name)
				}
			}
			// Create reaches different insert/readback paths depending on the unavailable
			// column. Every such failure must roll back the new physical claim as well.
			managedBoundaryInfrastructure(t, managedBoundaryActions(s, ctx, r.RoleName, p.Arn)["create-policy"]())
			if managedFrame(t, db.Database()) != frame {
				t.Fatal("post-insert schema fault left a claim")
			}
			if _, e := db.Database().Exec("ALTER TABLE " + tableColumn[0] + " RENAME COLUMN unavailable_column TO " + tableColumn[1]); e != nil {
				t.Fatal(e)
			}
			if _, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true); e != nil {
				t.Fatal("schema repair retry", e)
			}
			managedDecision(t, db, r.RoleName, "explicitDeny")
		})
	}
}

func TestManagedIAMBoundaryWriteAbortAndUnacknowledgedCounter(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"counter-ignore", "counter-abort", "default-ignore", "default-version-delete-ignore", "first-aggregate-insert-ignore", "inline-update-ignore"} {
		t.Run(kind, func(t *testing.T) {
			db, s := managedStore(t)
			r := managedRole(t, s, "role")
			managedRole(t, s, "peer")
			p := managedPolicy(t, s, "policy", managedAllow)
			event, table := "UPDATE", "aws_iam_managed_policies"
			when := ""
			failure := "IGNORE"
			if kind == "counter-abort" {
				failure = "ABORT,'boundary-private-fault'"
			}
			if kind == "default-ignore" {
				if _, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, false); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "default-version-delete-ignore" {
				event, table = "DELETE", "aws_iam_managed_policy_versions"
			}
			if kind == "first-aggregate-insert-ignore" {
				event, table = "INSERT", "identity_policies"
			}
			if kind == "inline-update-ignore" {
				if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
					t.Fatal(e)
				}
				if e := s.PutPolicy(ctx, r.RoleName, "inline", managedDeny); e != nil {
					t.Fatal(e)
				}
				event, table = "UPDATE", "aws_iam_role_policies"
				when = " WHEN OLD.name='inline'"
			}
			if _, e := db.Database().Exec("CREATE TRIGGER boundary_fault BEFORE " + event + " ON " + table + when + " BEGIN SELECT RAISE(" + failure + "); END"); e != nil {
				t.Fatal(e)
			}
			run := func() error {
				switch kind {
				case "default-ignore":
					return s.SetDefaultManagedPolicyVersion(ctx, p.Arn, "v2")
				case "default-version-delete-ignore":
					return s.DeleteManagedPolicy(ctx, p.Arn)
				case "first-aggregate-insert-ignore":
					return s.AttachRolePolicy(ctx, r.RoleName, p.Arn)
				case "inline-update-ignore":
					return s.PutPolicy(ctx, r.RoleName, "inline", managedAllow)
				default:
					_, e := s.CreateManagedPolicyVersion(ctx, p.Arn, managedDeny, true)
					return e
				}
			}
			frame := managedFrame(t, db.Database())
			managedBoundaryInfrastructure(t, run())
			if managedFrame(t, db.Database()) != frame {
				t.Fatal("failed acknowledgement partially committed")
			}
			if kind == "inline-update-ignore" {
				managedDecision(t, db, r.RoleName, "explicitDeny")
			}
			if _, e := db.Database().Exec(`DROP TRIGGER boundary_fault`); e != nil {
				t.Fatal(e)
			}
			if e := run(); e != nil {
				t.Fatal("write repair retry", e)
			}
		})
	}
}

func TestManagedIAMBoundaryMissingVersionsAndParents(t *testing.T) {
	ctx := context.Background()
	db, s := managedStore(t)
	r := managedRole(t, s, "role")
	p := managedPolicy(t, s, "policy", managedAllow)
	frame := managedFrame(t, db.Database())
	for _, id := range []string{"v2", "v1.external"} {
		if e := s.SetDefaultManagedPolicyVersion(ctx, p.Arn, id); iamCode(e) != "NoSuchEntity" {
			t.Fatal("unsaved default selector", e)
		}
		if e := s.DeleteManagedPolicyVersion(ctx, p.Arn, id); iamCode(e) != "NoSuchEntity" {
			t.Fatal("unsaved delete selector", e)
		}
	}
	for _, id := range []string{"v0", "v1!"} {
		if e := s.SetDefaultManagedPolicyVersion(ctx, p.Arn, id); iamCode(e) != "InvalidInput" {
			t.Fatal(e)
		}
		if e := s.DeleteManagedPolicyVersion(ctx, p.Arn, id); iamCode(e) != "InvalidInput" {
			t.Fatal(e)
		}
	}
	if e := s.AttachRolePolicy(ctx, "absent", p.Arn); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if _, _, e := s.AttachedRolePolicies(ctx, "absent", "", 1001); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if e := s.AttachRolePolicy(ctx, r.RoleName, iamManagedPrefix+"absent"); iamCode(e) != "NoSuchEntity" {
		t.Fatal(e)
	}
	if managedFrame(t, db.Database()) != frame {
		t.Fatal("missing selector changed authority graph")
	}
	if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
		t.Fatal("valid retry", e)
	}
	managedDecision(t, db, r.RoleName, "allowed")
}

func TestManagedIAMBoundaryClaimSchemaAndRetiredMetadata(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"lookup-column", "retired-edge-column", "invalid-retired-id"} {
		t.Run(kind, func(t *testing.T) {
			db, s := managedStore(t)
			managedPolicy(t, s, "peer", managedDeny)
			p := managedPolicy(t, s, "policy", managedAllow)
			if e := s.DeleteManagedPolicy(ctx, p.Arn); e != nil {
				t.Fatal(e)
			}
			var repair string
			var args []any
			switch kind {
			case "lookup-column":
				if _, e := db.Database().Exec(`ALTER TABLE aws_iam_managed_policies RENAME COLUMN policy_id TO unavailable_policy_id`); e != nil {
					t.Fatal(e)
				}
				repair = `ALTER TABLE aws_iam_managed_policies RENAME COLUMN unavailable_policy_id TO policy_id`
			case "retired-edge-column":
				if _, e := db.Database().Exec(`ALTER TABLE aws_iam_managed_policy_versions RENAME COLUMN policy_name TO unavailable_policy_name`); e != nil {
					t.Fatal(e)
				}
				repair = `ALTER TABLE aws_iam_managed_policy_versions RENAME COLUMN unavailable_policy_name TO policy_name`
			default:
				if _, e := db.Database().Exec(`UPDATE aws_iam_managed_policies SET policy_id='boundary-private-fault' WHERE name='policy'`); e != nil {
					t.Fatal(e)
				}
				repair = `UPDATE aws_iam_managed_policies SET policy_id=? WHERE name='policy'`
				args = []any{p.PolicyID}
			}
			frame := managedFrame(t, db.Database())
			_, e := s.CreateManagedPolicy(ctx, "policy", "/", "", managedAllow)
			managedBoundaryInfrastructure(t, e)
			if managedFrame(t, db.Database()) != frame {
				t.Fatal("schema/damaged tombstone adopted a policy")
			}
			if _, e = db.Database().Exec(repair, args...); e != nil {
				t.Fatal(e)
			}
			fresh, e := s.CreateManagedPolicy(ctx, "policy", "/", "", managedAllow)
			if e != nil || fresh.PolicyID == p.PolicyID {
				t.Fatal("claim repair did not create fresh generation", e)
			}
		})
	}
}

func TestManagedIAMBoundaryAllRoleMetadataAndByteBudgetRollback(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"second-role-date", "aggregate-bytes"} {
		t.Run(failure, func(t *testing.T) {
			db, s := managedStore(t)
			a := managedRole(t, s, "alpha")
			b := managedRole(t, s, "beta")
			managedRole(t, s, "peer")
			p := managedPolicy(t, s, "shared", managedAllow)
			for _, r := range []IAMRole{a, b} {
				if e := s.AttachRolePolicy(ctx, r.RoleName, p.Arn); e != nil {
					t.Fatal(e)
				}
			}
			doc := managedDeny
			var repair string
			var args []any
			if failure == "second-role-date" {
				var date string
				if e := db.Database().QueryRow(`SELECT created FROM aws_iam_roles WHERE name='beta'`).Scan(&date); e != nil {
					t.Fatal(e)
				}
				if _, e := db.Database().Exec(`UPDATE aws_iam_roles SET created='boundary-private-fault' WHERE name='beta'`); e != nil {
					t.Fatal(e)
				}
				repair = `UPDATE aws_iam_roles SET created=? WHERE name='beta'`
				args = []any{date}
			} else {
				// Each source document independently fits its own raw/compact contract.
				// The second role's existing inline plus the proposed managed default would
				// exceed the normalized UTF-8 aggregate byte budget, not its statement cap.
				base := strings.Replace(managedAllow, `"Effect":"Allow"`, `"Sid":"","Effect":"Allow"`, 1)
				inline := strings.Replace(base, `"Sid":""`, `"Sid":"`+strings.Repeat("é", 10500)+`"`, 1)
				if e := s.PutPolicy(ctx, b.RoleName, "wide-inline", inline); e != nil {
					t.Fatal(e)
				}
				doc = strings.Replace(strings.Replace(base, `"Sid":""`, `"Sid":"`+strings.Repeat("é", 6144-len(base))+`"`, 1), `"Effect":"Allow"`, `"Effect":"Deny"`, 1)
				// Allow->Deny shortens the document by one compact rune, so the
				// proposed default still fits the managed compact-character budget.
				repair = "delete-inline"
			}
			frame := managedFrame(t, db.Database())
			_, e := s.CreateManagedPolicyVersion(ctx, p.Arn, doc, true)
			if failure == "second-role-date" {
				managedBoundaryInfrastructure(t, e)
			} else if iamCode(e) != "LimitExceeded" {
				t.Fatal("aggregate byte cap did not refuse second-role update", e)
			}
			if managedFrame(t, db.Database()) != frame {
				t.Fatal("second-role failure changed default/version/first-role/peer")
			}
			managedDecision(t, db, a.RoleName, "allowed")
			managedDecision(t, db, "peer", "implicitDeny")
			if repair == "delete-inline" {
				if e := s.DeletePolicy(ctx, b.RoleName, "wide-inline"); e != nil {
					t.Fatal(e)
				}
			} else if _, e := db.Database().Exec(repair, args...); e != nil {
				t.Fatal(e)
			}
			if _, e := s.CreateManagedPolicyVersion(ctx, p.Arn, doc, true); e != nil {
				t.Fatal("all-role repair retry", e)
			}
			managedDecision(t, db, a.RoleName, "explicitDeny")
			managedDecision(t, db, b.RoleName, "explicitDeny")
		})
	}
}
