package aws

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func ssmCoreCFNTemplate() string {
	return strings.Replace(cfnProfileTemplate, "      RoleName: NativeRole", "      RoleName: NativeRole\n      ManagedPolicyArns: ['"+iamSSMCoreARN+"']", 1)
}
func TestCFNSSMCoreOriginalRoleProfileFragment(t *testing.T) {
	// The two original resources are unchanged. Only their NamePrefix declaration
	// wrapper is reduced. This is not a full Battle template or lifecycle test.
	body, err := os.ReadFile("testdata/hello-world-battle-iam-fragment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	record, err := compileStack("us-east-1", "fragment", string(body), map[string]string{"NamePrefix": "tc-hello-team"}, "CAPABILITY_NAMED_IAM")
	if err != nil {
		t.Fatal(err)
	}
	db, _, stacks, _ := s3StackState(t)
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	roles := &IAMRoleStore{db.Database()}
	profile, err := roles.GetInstanceProfile(context.Background(), "tc-hello-team-instance-profile")
	if err != nil || len(profile.Roles) != 1 {
		t.Fatal(profile, err)
	}
	role := profile.Roles[0]
	_, attached, err := roles.AttachedRolePolicies(context.Background(), role.RoleName, "", 100, role.RoleID)
	if err != nil || len(attached) != 1 || attached[0].PolicyArn != iamSSMCoreARN {
		t.Fatal(attached, err)
	}
	for action, want := range map[string]string{"ssm:GetParameter": "allowed", "ssm:GetParameters": "allowed", "ssm:PutParameter": "implicitDeny", "ssm:StartSession": "implicitDeny", "ssm:SendCommand": "implicitDeny"} {
		if got, e := core.NewIdentities(db).Decision(role.RoleName, action, "*"); e != nil || got != want {
			t.Fatal(action, got, e)
		}
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	p, err := roles.ManagedPolicy(context.Background(), iamSSMCoreARN)
	if err != nil || p.AttachmentCount != 0 {
		t.Fatal("cleanup removed builtin or retained authority", p, err)
	}
}

func TestCFNSSMCoreAcknowledgementRollback(t *testing.T) {
	faults := map[string]string{
		"ignore-attach":          `CREATE TRIGGER fault BEFORE INSERT ON aws_iam_role_managed_policies BEGIN SELECT RAISE(IGNORE); END`,
		"undo-attach":            `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_managed_policies BEGIN DELETE FROM aws_iam_role_managed_policies WHERE role_name=NEW.role_name; END`,
		"alter-inline-on-attach": `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_managed_policies BEGIN INSERT INTO aws_iam_role_policies VALUES(NEW.role_name,'Injected','{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}'); END`,
		"late-detach":            `CREATE TRIGGER fault AFTER INSERT ON aws_cfn_events WHEN NEW.resource_type='AWS::CloudFormation::Stack' AND NEW.status='CREATE_COMPLETE' BEGIN DELETE FROM aws_iam_role_managed_policies; END`,
		"late-pin-change":        `CREATE TRIGGER fault AFTER INSERT ON aws_cfn_events WHEN NEW.resource_type='AWS::CloudFormation::Stack' AND NEW.status='CREATE_COMPLETE' BEGIN UPDATE aws_iam_managed_policy_versions SET document='{}' WHERE policy_name='/aws/AmazonSSMManagedInstanceCore'; END`,
	}
	for name, statement := range faults {
		t.Run(name, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			record := cfnIAMCompile(t, ssmCoreCFNTemplate())
			profileSQL(t, db.Database(), statement)
			before := cleanupACKFrame(t, db.Database())
			if err := stacks.Create(record); err == nil || cleanupACKFrame(t, db.Database()) != before {
				t.Fatal("create acknowledged drift or partial effects", err)
			}
		})
	}
	for name, statement := range map[string]string{
		"ignore-detach":   `CREATE TRIGGER fault BEFORE DELETE ON aws_iam_role_managed_policies BEGIN SELECT RAISE(IGNORE); END`,
		"restore-detach":  `CREATE TRIGGER fault AFTER DELETE ON aws_iam_role_managed_policies BEGIN INSERT INTO aws_iam_role_managed_policies VALUES(OLD.role_name,OLD.role_id,OLD.policy_name,OLD.policy_id); END`,
		"late-attachment": `CREATE TRIGGER fault AFTER DELETE ON aws_cfn_stacks BEGIN INSERT INTO aws_iam_role_managed_policies SELECT name,role_id,'/aws/AmazonSSMManagedInstanceCore','ANPA0000000000000001' FROM aws_iam_roles WHERE name='NativeRole'; END`,
	} {
		t.Run(name, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			record := cfnIAMCompile(t, ssmCoreCFNTemplate())
			if err := stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			profileSQL(t, db.Database(), statement)
			before := cleanupACKFrame(t, db.Database())
			if err := stacks.Delete(record.Region, record.ID); err == nil || cleanupACKFrame(t, db.Database()) != before {
				t.Fatal("delete acknowledged drift or partial effects", err)
			}
		})
	}
}

func TestCFNSSMCoreExactARNAndAuthorization(t *testing.T) {
	for _, value := range []string{"['']", "['" + iamSSMCoreARN + "','" + iamSSMCoreARN + "']", "['arn:aws:iam::000000000000:policy/AmazonSSMManagedInstanceCore']", "['arn:aws:iam::aws:policy/AdministratorAccess']", "null", "{}"} {
		body := strings.Replace(ssmCoreCFNTemplate(), "['"+iamSSMCoreARN+"']", value, 1)
		if _, err := compileStack("us-east-1", "bad", body, nil, "CAPABILITY_NAMED_IAM"); err == nil {
			t.Fatal("unsupported ARN shape accepted", value)
		}
	}
	for _, action := range []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"} {
		t.Run(action, func(t *testing.T) {
			g := ssmValueGateway(t)
			doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"` + action + `","Resource":"arn:aws:iam::000000000000:role/NativeRole"}]}`
			policy, err := core.ParsePolicy([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			if err = g.Identities.Set("caller", policy); err != nil {
				t.Fatal(err)
			}
			before := cleanupACKFrame(t, g.Store.Database())
			if action == "iam:AttachRolePolicy" {
				out := cfnIAMQuery(g, "CreateStack", "denied", ssmCoreCFNTemplate(), "caller")
				if out.Code != 403 || cleanupACKFrame(t, g.Store.Database()) != before {
					t.Fatal(out.Code, out.Body.String())
				}
			} else {
				out := cfnIAMQuery(g, "CreateStack", "denied", ssmCoreCFNTemplate(), "")
				if out.Code != 200 {
					t.Fatal(out.Body.String())
				}
				before = cleanupACKFrame(t, g.Store.Database())
				out = cfnIAMQuery(g, "DeleteStack", "denied", "", "caller")
				if out.Code != 403 || cleanupACKFrame(t, g.Store.Database()) != before {
					t.Fatal(out.Code, out.Body.String())
				}
			}
		})
	}
}

func TestCFNSSMCoreCleanupPreservesRequestedPinsThroughFinalACK(t *testing.T) {
	boundaries := map[string]string{
		"detach":             `AFTER DELETE ON aws_iam_role_managed_policies WHEN OLD.policy_name='/aws/AmazonSSMManagedInstanceCore'`,
		"stack-row":          `AFTER DELETE ON aws_cfn_stacks`,
		"delete-event":       `AFTER INSERT ON aws_cfn_events WHEN NEW.status='DELETE_COMPLETE' AND NEW.resource_type='AWS::CloudFormation::Stack'`,
		"history-retirement": `AFTER UPDATE OF deleted ON aws_cfn_event_history WHEN NEW.deleted=1`,
	}
	for boundary, trigger := range boundaries {
		for name, fault := range ssmCorePinFaults() {
			t.Run(boundary+"/"+name, func(t *testing.T) {
				db, _, stacks, _ := s3StackState(t)
				record := cfnIAMCompile(t, ssmCoreCFNTemplate())
				if err := stacks.Create(record); err != nil {
					t.Fatal(err)
				}
				profileSQL(t, db.Database(), "CREATE TRIGGER fault "+trigger+" BEGIN "+fault+" END")
				before := cleanupACKFrame(t, db.Database())
				if err := stacks.Delete(record.Region, record.ID); err == nil {
					t.Fatal("cleanup acknowledged damaged requested pin")
				}
				if cleanupACKFrame(t, db.Database()) != before {
					t.Fatal("cleanup failure did not roll back all state")
				}
				profileSQL(t, db.Database(), "DROP TRIGGER fault")
				if err := stacks.Delete(record.Region, record.ID); err != nil {
					t.Fatal("clean cleanup after rollback", err)
				}
				roles := &IAMRoleStore{db.Database()}
				p, err := roles.ManagedPolicy(context.Background(), iamSSMCoreARN)
				if err != nil || p.AttachmentCount != 0 {
					t.Fatal(p, err)
				}
			})
		}
	}
}
