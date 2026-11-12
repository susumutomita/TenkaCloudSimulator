package aws

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

const faultInlinePolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"*"}]}`

func faultRoles(t *testing.T, db *sql.DB, role bool) (*IAMRoleStore, IAMRole) {
	t.Helper()
	s, err := NewIAMRoleStore(db)
	if err != nil {
		t.Fatal(err)
	}
	var r IAMRole
	if role {
		if r, err = s.Create(context.Background(), "fault-role", lambdaServiceTrust, "", 3600); err != nil {
			t.Fatal(err)
		}
	}
	return s, r
}

func TestIAMRoleStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	steps := map[string]struct {
		seed func(t *testing.T, s *IAMRoleStore, r IAMRole)
		op   func(ctx context.Context, s *IAMRoleStore, r IAMRole) error
	}{
		"create role": {nil, func(ctx context.Context, s *IAMRoleStore, _ IAMRole) error {
			_, err := s.Create(ctx, "fresh-role", lambdaServiceTrust, "described", 3600)
			return err
		}},
		"put inline policy": {nil, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			return s.PutPolicy(ctx, r.RoleName, "Inline", faultInlinePolicy, r.RoleID)
		}},
		"read role and inline policy": {func(t *testing.T, s *IAMRoleStore, r IAMRole) {
			if err := s.PutPolicy(context.Background(), r.RoleName, "Inline", faultInlinePolicy, r.RoleID); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			if _, err := s.Get(ctx, r.RoleName); err != nil {
				return err
			}
			if list, err := s.List(ctx, "", 10); err != nil {
				return err
			} else if len(list) == 0 {
				t.Fatal("role not listed")
			}
			if names, err := s.PolicyNames(ctx, r.RoleName, r.RoleID); err != nil {
				return err
			} else if len(names) != 1 {
				t.Fatalf("policy names %v", names)
			}
			if owned, err := s.Owns(ctx, r.RoleName); err != nil {
				return err
			} else if !owned {
				t.Fatal("simulator-created role not owned")
			}
			doc, err := s.Policy(ctx, r.RoleName, "Inline", r.RoleID)
			if err == nil && doc == "" {
				t.Fatal("empty policy document")
			}
			return err
		}},
		"delete inline policy": {func(t *testing.T, s *IAMRoleStore, r IAMRole) {
			if err := s.PutPolicy(context.Background(), r.RoleName, "Inline", faultInlinePolicy, r.RoleID); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			return s.DeletePolicy(ctx, r.RoleName, "Inline", r.RoleID)
		}},
		"update trust": {nil, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			return s.UpdateTrust(ctx, r.RoleName, lambdaServiceTrust, r.RoleID)
		}},
		"delete role": {nil, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			return s.Delete(ctx, r.RoleName, r.RoleID)
		}},
		"create managed policy": {nil, func(ctx context.Context, s *IAMRoleStore, _ IAMRole) error {
			_, err := s.CreateManagedPolicy(ctx, "FaultManaged", "/", "described", managedAllow)
			return err
		}},
		"attach and read managed policy": {nil, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			return s.AttachRolePolicy(ctx, r.RoleName, iamSSMCoreARN, r.RoleID)
		}},
		"list attached managed policies": {func(t *testing.T, s *IAMRoleStore, r IAMRole) {
			if err := s.AttachRolePolicy(context.Background(), r.RoleName, iamSSMCoreARN, r.RoleID); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			_, attached, err := s.AttachedRolePolicies(ctx, r.RoleName, "", 10, r.RoleID)
			if err == nil && len(attached) != 1 {
				t.Fatalf("attached %v", attached)
			}
			return err
		}},
		"detach managed policy": {func(t *testing.T, s *IAMRoleStore, r IAMRole) {
			if err := s.AttachRolePolicy(context.Background(), r.RoleName, iamSSMCoreARN, r.RoleID); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			return s.DetachRolePolicy(ctx, r.RoleName, iamSSMCoreARN, r.RoleID)
		}},
		"managed policy versions": {func(t *testing.T, s *IAMRoleStore, _ IAMRole) {
			if _, err := s.CreateManagedPolicy(context.Background(), "Versioned", "/", "", managedAllow); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *IAMRoleStore, _ IAMRole) error {
			list, err := s.ManagedPolicies(ctx, "", 100)
			if err != nil {
				return err
			}
			arn := ""
			for _, p := range list {
				if p.PolicyName == "Versioned" {
					arn = p.Arn
				}
			}
			if arn == "" {
				t.Fatal("customer managed policy not listed")
			}
			if _, err = s.ManagedPolicy(ctx, arn); err != nil {
				return err
			}
			v, err := s.CreateManagedPolicyVersion(ctx, arn, managedAllow, false)
			if err != nil {
				return err
			}
			if _, versions, err := s.ManagedPolicyVersions(ctx, arn); err != nil {
				return err
			} else if len(versions) != 2 {
				t.Fatalf("versions %v", versions)
			}
			if _, err = s.ManagedPolicyVersion(ctx, arn, v.VersionID); err != nil {
				return err
			}
			if err = s.SetDefaultManagedPolicyVersion(ctx, arn, v.VersionID); err != nil {
				return err
			}
			if err = s.DeleteManagedPolicyVersion(ctx, arn, "v1"); err != nil {
				return err
			}
			return s.DeleteManagedPolicy(ctx, arn)
		}},
		"instance profile lifecycle": {nil, func(ctx context.Context, s *IAMRoleStore, r IAMRole) error {
			p, err := s.CreateInstanceProfile(ctx, "FaultProfile")
			if err != nil {
				return err
			}
			if err = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, true); err != nil {
				return err
			}
			if got, err := s.GetInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID); err != nil {
				return err
			} else if len(got.Roles) != 1 {
				t.Fatalf("profile roles %v", got.Roles)
			}
			if list, err := s.ListInstanceProfiles(ctx, "", 10, r.RoleName, r.RoleID); err != nil {
				return err
			} else if len(list) != 1 {
				t.Fatalf("profiles for role %v", list)
			}
			if err = s.ChangeInstanceProfileRole(ctx, p.InstanceProfileName, p.InstanceProfileID, r.RoleName, r.RoleID, false); err != nil {
				return err
			}
			return s.DeleteInstanceProfile(ctx, p.InstanceProfileName, p.InstanceProfileID)
		}},
	}
	for name, step := range steps {
		step := step
		// Multi-call steps commit between calls, so only single-mutation steps are
		// held to whole-database atomicity.
		atomic := name != "managed policy versions" && name != "instance profile lifecycle" && name != "read role and inline policy"
		t.Run(name, func(t *testing.T) {
			sweepStorageFaults(t, atomic, func(t *testing.T, db *sql.DB) func(context.Context) error {
				s, r := faultRoles(t, db, true)
				if step.seed != nil {
					step.seed(t, s, r)
				}
				return func(ctx context.Context) error { return step.op(ctx, s, r) }
			})
		})
	}
}

func TestRoute53StoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	seeded := func(t *testing.T, db *sql.DB) (*Route53Store, string) {
		t.Helper()
		s, err := NewRoute53Store(db)
		if err != nil {
			t.Fatal(err)
		}
		zone, _, err := s.create(context.Background(), "example.test", "seed", "")
		if err != nil {
			t.Fatal(err)
		}
		return s, strings.TrimPrefix(zone.ID, "/hostedzone/")
	}
	t.Run("create zone", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, err := NewRoute53Store(db)
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error { _, _, err := s.create(ctx, "fresh.test", "ref", "comment"); return err }
		})
	})
	t.Run("change records", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, id := seeded(t, db)
			return func(ctx context.Context) error {
				_, err := s.change(ctx, id, "batch", []Route53Change{{"CREATE", r53record("a.example.test", "A", "192.0.2.1")}, {"UPSERT", r53record("b.example.test", "TXT", `"hello"`)}})
				return err
			}
		})
	})
	t.Run("snapshot and remove", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, id := seeded(t, db)
			return func(ctx context.Context) error {
				if _, records, err := s.snapshot(ctx, id); err != nil {
					return err
				} else if len(records) < 2 {
					t.Fatalf("default records missing: %v", records)
				}
				_, err := s.remove(ctx, id)
				return err
			}
		})
	})
}
