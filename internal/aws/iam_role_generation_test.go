package aws

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Model the real admission/transaction boundary deterministically: authorize a
// captured canonical ARN, replace its role, then invoke the production port.
// This is not an assertion that two HTTP requests were scheduled at an exact
// uninstrumented point. The composition test separately fences native callers.
func TestIAMRoleGenerationSixPreauthorizedPortsRejectReplacement(t *testing.T) {
	actions := []string{"PutRolePolicy", "GetRolePolicy", "DeleteRolePolicy", "ListRolePolicies", "UpdateAssumeRolePolicy", "DeleteRole"}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			db, store := managedStore(t)
			old := managedRole(t, store, "GenerationRole")
			grant, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:` + action + `","Resource":"` + old.Arn + `"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			ids := core.NewIdentities(db)
			if err = ids.Set("generation-caller", grant); err != nil {
				t.Fatal(err)
			}
			decision, err := ids.DecisionContext(ctx, "generation-caller", "iam:"+action, old.Arn)
			if err != nil || decision != "allowed" {
				t.Fatal("old canonical authorization missing", err)
			}
			if err = store.Delete(ctx, old.RoleName); err != nil {
				t.Fatal(err)
			}
			fresh := managedRole(t, store, "GENERATIONROLE")
			peer := managedRole(t, store, "generation-peer")
			if fresh.RoleID == old.RoleID || fresh.Arn == old.Arn {
				t.Fatal("replacement fixture retained old incarnation")
			}
			decision, err = ids.DecisionContext(ctx, "generation-caller", "iam:"+action, fresh.Arn)
			if err != nil || decision != "implicitDeny" {
				t.Fatal("replacement resource unexpectedly authorized", err)
			}
			policy := managedPolicy(t, store, "generation-policy", managedAllow)
			for _, role := range []IAMRole{fresh, peer} {
				if err = store.AttachRolePolicy(ctx, role.RoleName, policy.Arn, role.RoleID); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.PutPolicy(ctx, fresh.RoleName, "protected-inline", managedDeny); err != nil {
				t.Fatal(err)
			}
			before := managedFrame(t, db.Database())
			switch action {
			case "PutRolePolicy":
				err = store.PutPolicy(ctx, old.RoleName, "protected-inline", managedAllow, old.RoleID)
			case "GetRolePolicy":
				var document string
				document, err = store.Policy(ctx, old.RoleName, "protected-inline", old.RoleID)
				if document != "" {
					t.Fatal("stale authorization disclosed replacement policy")
				}
			case "DeleteRolePolicy":
				err = store.DeletePolicy(ctx, old.RoleName, "protected-inline", old.RoleID)
			case "ListRolePolicies":
				var names []string
				names, err = store.PolicyNames(ctx, old.RoleName, old.RoleID)
				if names != nil {
					t.Fatal("stale authorization disclosed replacement policy names")
				}
			case "UpdateAssumeRolePolicy":
				denyTrust := strings.Replace(lambdaServiceTrust, `"Allow"`, `"Deny"`, 1)
				if denyTrust == lambdaServiceTrust {
					t.Fatal("trust fixture did not change effect")
				}
				err = store.UpdateTrust(ctx, old.RoleName, denyTrust, old.RoleID)
			case "DeleteRole":
				err = store.Delete(ctx, old.RoleName, old.RoleID)
			}
			var failure ssmFailure
			if !errors.As(err, &failure) || failure.code != "NoSuchEntity" {
				t.Fatal("stale authorization did not fail with missing incarnation", err)
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("stale authorization changed six-table/peer frame")
			}
			managedDecision(t, db, fresh.RoleName, "explicitDeny")
			managedDecision(t, db, peer.RoleName, "allowed")
		})
	}
}

func TestIAMRoleGenerationFreshAndTrustedPortsKeepInlineSemantics(t *testing.T) {
	ctx := context.Background()
	db, store := managedStore(t)
	role := managedRole(t, store, "fresh-generation")
	// Existing trusted embedding calls can omit the fence. Native calls carry it.
	if err := store.PutPolicy(ctx, role.RoleName, "trusted", managedAllow); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Policy(ctx, role.RoleName, "trusted", role.RoleID)
	if err != nil || doc != managedAllow {
		t.Fatal("current generation read", err)
	}
	names, err := store.PolicyNames(ctx, role.RoleName, role.RoleID)
	if err != nil || len(names) != 1 || names[0] != "trusted" {
		t.Fatal("current generation list", names, err)
	}
	if err = store.UpdateTrust(ctx, role.RoleName, lambdaServiceTrust, role.RoleID); err != nil {
		t.Fatal(err)
	}
	if err = store.PutPolicy(ctx, role.RoleName, "trusted", managedDeny, role.RoleID); err != nil {
		t.Fatal(err)
	}
	managedDecision(t, db, role.RoleName, "explicitDeny")
	if err = store.DeletePolicy(ctx, role.RoleName, "trusted", role.RoleID); err != nil {
		t.Fatal(err)
	}
	managedDecision(t, db, role.RoleName, "implicitDeny")
	if _, err = store.Policy(ctx, role.RoleName, "trusted", role.RoleID); iamCode(err) != "NoSuchEntity" {
		t.Fatal("missing inline policy semantics changed", err)
	}
	if err = store.Delete(ctx, role.RoleName, role.RoleID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, role.RoleName); iamCode(err) != "NoSuchEntity" {
		t.Fatal("deleted role became visible", err)
	}
}

func TestIAMRoleGenerationAllNineNativePortsCarryCapturedID(t *testing.T) {
	sources := map[string]map[string]int{
		"iam_query.go":         {"PutPolicy": 5, "Policy": 4, "DeletePolicy": 4, "PolicyNames": 3, "UpdateTrust": 4, "Delete": 3},
		"iam_managed_query.go": {"AttachRolePolicy": 4, "DetachRolePolicy": 4, "AttachedRolePolicies": 5},
	}
	for file, expected := range sources {
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		ast.Inspect(tree, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := selector.X.(*ast.SelectorExpr)
			if !ok || receiver.Sel.Name != "Roles" {
				return true
			}
			count, needed := expected[selector.Sel.Name]
			if !needed {
				return true
			}
			seen[selector.Sel.Name]++
			if len(call.Args) != count {
				t.Error(file, selector.Sel.Name, "omitted expected incarnation")
				return true
			}
			id, ok := call.Args[count-1].(*ast.SelectorExpr)
			if !ok || id.Sel.Name != "RoleID" {
				t.Error(file, selector.Sel.Name, "must pass captured RoleID")
				return true
			}
			role, ok := id.X.(*ast.Ident)
			if !ok || role.Name != "role" {
				t.Error(file, selector.Sel.Name, "must use preauthorized role snapshot")
			}
			return true
		})
		for name := range expected {
			if seen[name] != 1 {
				t.Error(file, name, "expected exactly one native call")
			}
		}
	}
}

func TestIAMRoleGenerationTrustIgnoredWriteCannotAcknowledgeSuccess(t *testing.T) {
	ctx := context.Background()
	db, store := managedStore(t)
	role := managedRole(t, store, "trust-ack")
	peer := managedRole(t, store, "trust-peer")
	policy := managedPolicy(t, store, "trust-policy", managedAllow)
	if err := store.AttachRolePolicy(ctx, peer.RoleName, policy.Arn, peer.RoleID); err != nil {
		t.Fatal(err)
	}
	denied := strings.Replace(lambdaServiceTrust, `"Allow"`, `"Deny"`, 1)
	if _, err := db.Database().Exec(`CREATE TRIGGER generation_ignore_trust BEFORE UPDATE OF trust ON aws_iam_roles WHEN OLD.name='trust-ack' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before := managedFrame(t, db.Database())
	err := store.UpdateTrust(ctx, role.RoleName, denied, role.RoleID)
	var failure ssmFailure
	if err == nil || errors.As(err, &failure) {
		t.Fatal("ignored mandatory trust write did not fail as infrastructure error", err)
	}
	if managedFrame(t, db.Database()) != before {
		t.Fatal("ignored trust write changed owned or peer frame")
	}
	if _, err = db.Database().Exec(`DROP TRIGGER generation_ignore_trust`); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateTrust(ctx, role.RoleName, denied, role.RoleID); err != nil {
		t.Fatal("repaired trust update", err)
	}
	updated, err := store.Get(ctx, role.RoleName)
	if err != nil || updated.AssumeRolePolicyDocument != denied {
		t.Fatal("repaired trust value not persisted", err)
	}
	managedDecision(t, db, peer.RoleName, "allowed")
}
