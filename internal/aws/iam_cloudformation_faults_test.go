package aws

import (
	"context"
	"strings"
	"testing"
)

func TestCFNNativeIAMCreationAcknowledgesOnlyExactCommittedEffects(t *testing.T) {
	faults := map[string]string{
		"ignore-role":                `CREATE TRIGGER fault BEFORE INSERT ON aws_iam_roles WHEN NEW.name='NativeRole' BEGIN SELECT RAISE(IGNORE); END`,
		"alter-role":                 `CREATE TRIGGER fault AFTER INSERT ON aws_iam_roles WHEN NEW.name='NativeRole' BEGIN UPDATE aws_iam_roles SET duration=7200 WHERE name=NEW.name; END`,
		"ignore-profile":             `CREATE TRIGGER fault BEFORE INSERT ON aws_iam_instance_profiles WHEN NEW.name='NativeProfile' BEGIN SELECT RAISE(IGNORE); END`,
		"ignore-membership":          `CREATE TRIGGER fault BEFORE INSERT ON aws_iam_instance_profile_roles WHEN NEW.profile_name='NativeProfile' BEGIN SELECT RAISE(IGNORE); END`,
		"undo-membership":            `CREATE TRIGGER fault AFTER INSERT ON aws_iam_instance_profile_roles WHEN NEW.profile_name='NativeProfile' BEGIN DELETE FROM aws_iam_instance_profile_roles WHERE profile_name=NEW.profile_name; END`,
		"ignore-policy":              `CREATE TRIGGER fault BEFORE INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' BEGIN SELECT RAISE(IGNORE); END`,
		"undo-policy":                `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' BEGIN DELETE FROM aws_iam_role_policies WHERE role_name=NEW.role_name; END`,
		"ignore-aggregate":           `CREATE TRIGGER fault BEFORE INSERT ON identity_policies WHEN NEW.name='NativeRole' BEGIN SELECT RAISE(IGNORE); END`,
		"ignore-ownership":           `CREATE TRIGGER fault BEFORE INSERT ON aws_cfn_ownership WHEN NEW.kind='AWS::IAM::InstanceProfile' BEGIN SELECT RAISE(IGNORE); END`,
		"ignore-generation-document": `CREATE TRIGGER fault BEFORE UPDATE OF document ON aws_cfn_stacks BEGIN SELECT RAISE(IGNORE); END`,
		"late-role-change":           `CREATE TRIGGER fault AFTER INSERT ON aws_cfn_events WHEN NEW.resource_type='AWS::CloudFormation::Stack' AND NEW.status='CREATE_COMPLETE' BEGIN UPDATE aws_iam_roles SET duration=7200 WHERE name='NativeRole'; END`,
		"late-profile-retirement":    `CREATE TRIGGER fault AFTER INSERT ON aws_cfn_events WHEN NEW.resource_type='AWS::CloudFormation::Stack' AND NEW.status='CREATE_COMPLETE' BEGIN DELETE FROM aws_iam_instance_profile_roles WHERE profile_name='NativeProfile'; UPDATE aws_iam_instance_profiles SET deleted=1 WHERE name='NativeProfile'; END`,
		"late-ownership-removal":     `CREATE TRIGGER fault AFTER INSERT ON aws_cfn_events WHEN NEW.resource_type='AWS::CloudFormation::Stack' AND NEW.status='CREATE_COMPLETE' BEGIN DELETE FROM aws_cfn_ownership WHERE kind='AWS::IAM::InstanceProfile'; END`,
	}
	for name, sql := range faults {
		t.Run(name, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			roles := &IAMRoleStore{db.Database()}
			profileRole(t, roles, "PeerRole")
			profileCreate(t, roles, "PeerProfile")
			body := strings.Replace(cfnProfileTemplate, "      RoleName: NativeRole", "      RoleName: NativeRole\n      Policies: [{PolicyName: Read, PolicyDocument: "+nativeReadPolicy+"}]", 1)
			record := cfnIAMCompile(t, body)
			profileSQL(t, roles.db, sql)
			before := cleanupACKFrame(t, roles.db)
			if err := stacks.Create(record); err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("ignored/undone write acknowledged or changed peer frame", err)
			}
			profileSQL(t, roles.db, "DROP TRIGGER fault")
			if err := stacks.Create(record); err != nil {
				t.Fatal("retry after repair", err)
			}
			if err := stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCFNNativeIAMCleanupAcknowledgesOnlyFinalRetirement(t *testing.T) {
	faults := map[string]string{
		"ignore-membership-removal":   `CREATE TRIGGER fault BEFORE DELETE ON aws_iam_instance_profile_roles WHEN OLD.profile_name='NativeProfile' BEGIN SELECT RAISE(IGNORE); END`,
		"restore-membership":          `CREATE TRIGGER fault AFTER DELETE ON aws_iam_instance_profile_roles WHEN OLD.profile_name='NativeProfile' BEGIN INSERT INTO aws_iam_instance_profile_roles VALUES(OLD.profile_name,OLD.profile_id,OLD.role_name,OLD.role_id); END`,
		"ignore-profile-retirement":   `CREATE TRIGGER fault BEFORE UPDATE OF deleted ON aws_iam_instance_profiles WHEN OLD.name='NativeProfile' BEGIN SELECT RAISE(IGNORE); END`,
		"undo-profile-retirement":     `CREATE TRIGGER fault AFTER UPDATE OF deleted ON aws_iam_instance_profiles WHEN OLD.name='NativeProfile' AND NEW.deleted=1 BEGIN UPDATE aws_iam_instance_profiles SET deleted=0 WHERE name=NEW.name; END`,
		"ignore-policy-delete":        `CREATE TRIGGER fault BEFORE DELETE ON aws_iam_role_policies WHEN OLD.role_name='NativeRole' BEGIN SELECT RAISE(IGNORE); END`,
		"restore-policy":              `CREATE TRIGGER fault AFTER DELETE ON aws_iam_role_policies WHEN OLD.role_name='NativeRole' BEGIN INSERT INTO aws_iam_role_policies VALUES(OLD.role_name,OLD.name,OLD.document); END`,
		"ignore-aggregate-delete":     `CREATE TRIGGER fault BEFORE DELETE ON identity_policies WHEN OLD.name='NativeRole' BEGIN SELECT RAISE(IGNORE); END`,
		"ignore-role-retirement":      `CREATE TRIGGER fault BEFORE UPDATE OF deleted ON aws_iam_roles WHEN OLD.name='NativeRole' BEGIN SELECT RAISE(IGNORE); END`,
		"undo-role-retirement":        `CREATE TRIGGER fault AFTER UPDATE OF deleted ON aws_iam_roles WHEN OLD.name='NativeRole' AND NEW.deleted=1 BEGIN UPDATE aws_iam_roles SET deleted=0 WHERE name=NEW.name; END`,
		"late-role-resurrection":      `CREATE TRIGGER fault AFTER DELETE ON aws_cfn_stacks BEGIN UPDATE aws_iam_roles SET deleted=0 WHERE name='NativeRole'; END`,
		"late-profile-resurrection":   `CREATE TRIGGER fault AFTER UPDATE OF deleted ON aws_cfn_event_history WHEN NEW.deleted=1 BEGIN UPDATE aws_iam_instance_profiles SET deleted=0 WHERE name='NativeProfile'; END`,
		"late-aggregate-resurrection": `CREATE TRIGGER fault AFTER DELETE ON aws_cfn_stacks BEGIN INSERT INTO identity_policies(name,document) VALUES('NativeRole','{}'); END`,
		"late-generation-change":      `CREATE TRIGGER fault AFTER DELETE ON aws_cfn_stacks BEGIN UPDATE aws_iam_roles SET role_id='AROA0000000000000000' WHERE name='NativeRole'; END`,
		"ignore-stack":                `CREATE TRIGGER fault BEFORE DELETE ON aws_cfn_stacks BEGIN SELECT RAISE(IGNORE); END`,
	}
	for name, sql := range faults {
		t.Run(name, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			roles := &IAMRoleStore{db.Database()}
			profileRole(t, roles, "PeerRole")
			profileCreate(t, roles, "PeerProfile")
			body := strings.Replace(cfnProfileTemplate, "      RoleName: NativeRole", "      RoleName: NativeRole\n      Policies: [{PolicyName: Read, PolicyDocument: "+nativeReadPolicy+"}]", 1)
			record := cfnIAMCompile(t, body)
			if err := stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			profileSQL(t, roles.db, sql)
			before := cleanupACKFrame(t, roles.db)
			if err := stacks.Delete(record.Region, record.ID); err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("cleanup falsely acknowledged or changed peer frame", err)
			}
			profileSQL(t, roles.db, "DROP TRIGGER fault")
			if err := stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal("retry after repair", err)
			}
		})
	}
}

func TestCFNNativeIAMRetiredGenerationRecreationFaults(t *testing.T) {
	for _, kind := range []string{"role", "profile"} {
		t.Run(kind, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			roles := &IAMRoleStore{db.Database()}
			record := cfnIAMCompile(t, cfnProfileTemplate)
			if err := stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			if err := stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal(err)
			}
			table, column, name := "aws_iam_roles", "role_id", "NativeRole"
			if kind == "profile" {
				table, column, name = "aws_iam_instance_profiles", "profile_id", "NativeProfile"
			}
			profileSQL(t, roles.db, "CREATE TRIGGER fault AFTER UPDATE ON "+table+" WHEN NEW.name='"+name+"' AND NEW.deleted=0 BEGIN UPDATE "+table+" SET "+column+"=OLD."+column+",created=OLD.created,deleted=1 WHERE name=NEW.name; END")
			fresh := cfnIAMCompile(t, cfnProfileTemplate)
			before := cleanupACKFrame(t, roles.db)
			if err := stacks.Create(fresh); err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("old generation silently accepted", err)
			}
			profileSQL(t, roles.db, "DROP TRIGGER fault")
			if err := stacks.Create(fresh); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeIAMRoleDeleteFinalACKSurvivesHelperExtraction(t *testing.T) {
	for _, sql := range []string{
		`CREATE TRIGGER fault AFTER UPDATE OF deleted ON aws_iam_roles WHEN NEW.deleted=1 BEGIN UPDATE aws_iam_roles SET deleted=0 WHERE name=NEW.name; END`,
		`CREATE TRIGGER fault AFTER UPDATE OF deleted ON aws_iam_roles WHEN NEW.deleted=1 BEGIN INSERT INTO identity_policies VALUES(NEW.name,'{}'); END`,
	} {
		t.Run(sql, func(t *testing.T) {
			roles := profileStore(t)
			role := profileRole(t, roles, "NativeRole")
			profileSQL(t, roles.db, sql)
			before := cleanupACKFrame(t, roles.db)
			if err := roles.Delete(context.Background(), role.RoleName, role.RoleID); err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("native delete acknowledged an undone write", err)
			}
		})
	}
}

func TestNativeIAMEmptyCreationAndAggregateRemovalHaveExactEffects(t *testing.T) {
	for _, operation := range []string{"native-create-aggregate", "cfn-create-aggregate", "native-create-inline", "cfn-create-inline", "last-inline-delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			db, _, stacks, _ := s3StackState(t)
			roles := &IAMRoleStore{db.Database()}
			if operation == "last-inline-delete" {
				role := profileRole(t, roles, "NativeRole")
				if err := roles.PutPolicy(ctx, role.RoleName, "Read", nativeReadPolicy, role.RoleID); err != nil {
					t.Fatal(err)
				}
			} else {
				query := `CREATE TRIGGER inject AFTER INSERT ON aws_iam_roles WHEN NEW.name='NativeRole' BEGIN INSERT INTO identity_policies VALUES(NEW.name,'` + nativeReadPolicy + `'); END`
				if strings.HasSuffix(operation, "inline") {
					query = `CREATE TRIGGER inject AFTER INSERT ON aws_iam_roles WHEN NEW.name='NativeRole' BEGIN INSERT INTO aws_iam_role_policies VALUES(NEW.name,'Unexpected','` + nativeReadPolicy + `'); END`
				}
				profileSQL(t, roles.db, query)
			}
			if strings.HasSuffix(operation, "aggregate") || operation == "last-inline-delete" {
				profileSQL(t, roles.db, `CREATE TRIGGER restore AFTER DELETE ON identity_policies WHEN OLD.name='NativeRole' BEGIN INSERT INTO identity_policies VALUES(OLD.name,OLD.document); END`)
			}
			before := cleanupACKFrame(t, roles.db)
			var err error
			if strings.HasPrefix(operation, "cfn") {
				err = stacks.Create(cfnIAMCompile(t, cfnProfileTemplate))
			} else if operation == "last-inline-delete" {
				err = roles.DeletePolicy(ctx, "NativeRole", "Read")
			} else {
				_, err = roles.Create(ctx, "NativeRole", lambdaServiceTrust, "", 3600)
			}
			if err == nil || cleanupACKFrame(t, roles.db) != before {
				t.Fatal("empty role/removed policy retained unexpected authority", err)
			}
		})
	}
}

func TestCFNNativeIAMCreationRejectsUnexpectedInlineState(t *testing.T) {
	for _, fault := range []string{"extra-policy", "earlier-policy-change"} {
		t.Run(fault, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			body := strings.Replace(cfnProfileTemplate, "      RoleName: NativeRole", "      RoleName: NativeRole\n      Policies: [{PolicyName: First, PolicyDocument: "+nativeReadPolicy+"}, {PolicyName: Second, PolicyDocument: "+nativeReadPolicy+"}]", 1)
			sql := `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' AND NEW.name='Second' BEGIN INSERT INTO aws_iam_role_policies VALUES(NEW.role_name,'Unexpected','` + nativeReadPolicy + `'); END`
			if fault == "earlier-policy-change" {
				sql = `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' AND NEW.name='Second' BEGIN UPDATE aws_iam_role_policies SET document='` + managedDeny + `' WHERE role_name=NEW.role_name AND name='First'; END`
			}
			profileSQL(t, db.Database(), sql)
			before := cleanupACKFrame(t, db.Database())
			if err := stacks.Create(cfnIAMCompile(t, body)); err == nil || cleanupACKFrame(t, db.Database()) != before {
				t.Fatal("created policy set differed from template", err)
			}
		})
	}
}

func TestNativeIAMInlineMutationsPreserveUnrequestedPolicyFrame(t *testing.T) {
	for _, operation := range []string{"put-extra-inline", "put-change-other", "delete-change-other", "put-managed-attachment"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			db, roles := managedStore(t)
			role := profileRole(t, roles, "NativeRole")
			if err := roles.PutPolicy(ctx, role.RoleName, "Existing", nativeReadPolicy); err != nil {
				t.Fatal(err)
			}
			if err := roles.PutPolicy(ctx, role.RoleName, "DeleteMe", nativeReadPolicy); err != nil {
				t.Fatal(err)
			}
			query := `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' AND NEW.name='Requested' BEGIN INSERT INTO aws_iam_role_policies VALUES(NEW.role_name,'Unexpected','` + nativeReadPolicy + `'); END`
			if operation == "put-change-other" {
				query = `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' AND NEW.name='Requested' BEGIN UPDATE aws_iam_role_policies SET document='` + managedDeny + `' WHERE role_name=NEW.role_name AND name='Existing'; END`
			}
			if operation == "delete-change-other" {
				query = `CREATE TRIGGER fault AFTER DELETE ON aws_iam_role_policies WHEN OLD.role_name='NativeRole' AND OLD.name='DeleteMe' BEGIN UPDATE aws_iam_role_policies SET document='` + managedDeny + `' WHERE role_name=OLD.role_name AND name='Existing'; END`
			}
			if operation == "put-managed-attachment" {
				policy := managedPolicy(t, roles, "UnexpectedManaged", nativeReadPolicy)
				query = `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_policies WHEN NEW.role_name='NativeRole' AND NEW.name='Requested' BEGIN INSERT INTO aws_iam_role_managed_policies VALUES(NEW.role_name,'` + role.RoleID + `','UnexpectedManaged','` + policy.PolicyID + `'); END`
			}
			profileSQL(t, roles.db, query)
			before := cleanupACKFrame(t, db.Database())
			var err error
			if operation == "delete-change-other" {
				err = roles.DeletePolicy(ctx, role.RoleName, "DeleteMe", role.RoleID)
			} else {
				err = roles.PutPolicy(ctx, role.RoleName, "Requested", nativeReadPolicy, role.RoleID)
			}
			if err == nil || cleanupACKFrame(t, db.Database()) != before {
				t.Fatal("inline mutation changed unrelated policy state", err)
			}
		})
	}
}
