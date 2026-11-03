package aws

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

//go:embed testdata/lambda-inline.yaml
var inlineStackTemplate string

func TestLambdaStackSharesNativeStateAndDeletesInDependencyOrder(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	lambdas, err := NewLambdaStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer lambdas.Close()
	record, err := compileStack("ap-northeast-1", "inline", inlineStackTemplate, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if record.Resources[0].Type != "AWS::IAM::Role" || record.Resources[1].Type != "AWS::Lambda::Function" || record.Outputs["Name"] != "cfn-inline-app" || record.Outputs["Arn"] != lambdaARN(record.Region, "cfn-inline-app") {
		t.Fatal(record)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	f, err := lambdas.get(record.Region, record.Outputs["Name"])
	if err != nil || f.Timeout != 15 || f.Environment["DEMO_VALUE"] != "safe-dummy" || f.Tags["Purpose"] != "local-practice" || f.Revision == "" {
		t.Fatal(f, err)
	}
	identities := core.NewIdentities(db)
	p, _, err := identities.Policy(strings.TrimPrefix(f.Role, "arn:aws:iam::000000000000:role/"))
	if err != nil {
		t.Fatal(err)
	}
	if core.Evaluate(p, "logs:CreateLogStream", "*") == "allowed" {
		t.Fatal("managed policy metadata granted actual authority")
	}
	// Enforce real dependency teardown: the role must still exist when its
	// dependent function is removed, and the function must be gone before role.
	_, err = db.Database().Exec(`CREATE TRIGGER guard_role_deletion BEFORE DELETE ON identity_policies WHEN EXISTS(SELECT 1 FROM aws_lambda_functions) BEGIN SELECT RAISE(ABORT,'function still exists'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = lambdas.get(record.Region, f.Name); err == nil {
		t.Fatal("function survived stack deletion")
	}
	if err = db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestLambdaStackRollbackDriftAndSafeOwnershipNamespace(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	lambdas, err := NewLambdaStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer lambdas.Close()
	record, err := compileStack("us-east-1", "inline", inlineStackTemplate, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Database().Exec(`CREATE TRIGGER fail_function BEFORE INSERT ON aws_lambda_functions BEGIN SELECT RAISE(ABORT,'private-dummy-detail'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err == nil || strings.Contains(err.Error(), "private-dummy-detail") {
		t.Fatal(err)
	}
	for _, table := range []string{"aws_cfn_stacks", "aws_cfn_ownership", "identity_policies", "aws_lambda_functions"} {
		var n int
		if err = db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	if _, err = db.Database().Exec("DROP TRIGGER fail_function"); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	// Pre-Lambda CFN readers use a plain region namespace for unknown kinds.
	// That namespace must not match new claims and authorize an IAM fallback.
	var n int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND kind='AWS::Lambda::Function'", record.Region).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err = db.Database().Exec("DELETE FROM aws_lambda_functions"); err != nil {
		t.Fatal(err)
	}
	other, err := compileStack(record.Region, "another", inlineStackTemplate, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(other); err == nil {
		t.Fatal("drifted function reservation was adopted")
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(other); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Delete(record.Region, other.ID); err != nil {
		t.Fatal(err)
	}
}

func baseInlineProperties() map[string]any {
	return map[string]any{"Runtime": "python3.12", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/exec", "Code": map[string]any{"ZipFile": "def handler(event,context): return 42"}}
}
func TestLambdaStackCompileValidationAndGeneratedNames(t *testing.T) {
	changes := []map[string]any{
		{"FunctionName": true}, {"FunctionName": "bad name"}, {"Runtime": "nodejs22.x"}, {"Handler": "app.handler"}, {"Role": "arn:aws:iam::123456789012:role/exec"}, {"Code": nil}, {"Code": map[string]any{"S3Bucket": "external"}}, {"Code": map[string]any{"ZipFile": ""}},
		{"Timeout": "0"}, {"Timeout": "31"}, {"Timeout": true}, {"MemorySize": "256"}, {"PackageType": "Image"}, {"Description": true}, {"Description": strings.Repeat("x", 257)}, {"Unknown": "x"},
		{"Environment": nil}, {"Environment": map[string]any{"Variables": nil}}, {"Environment": map[string]any{"Variables": map[string]any{"X": "bad"}}}, {"Environment": map[string]any{"Variables": map[string]any{"XX": true}}}, {"Environment": map[string]any{"Variables": map[string]any{"XX": "a\x00"}}}, {"Environment": map[string]any{"Variables": map[string]any{"XX": strings.Repeat("x", 4096)}}},
		{"Tags": true}, {"Tags": []any{nil}}, {"Tags": []any{map[string]any{"Key": "x", "Value": true}}}, {"Tags": []any{map[string]any{"Key": "x", "Value": "y"}, map[string]any{"Key": "x", "Value": "z"}}},
	}
	for i, change := range changes {
		p := baseInlineProperties()
		for k, v := range change {
			p[k] = v
		}
		if _, err := compileStackFunction("us-east-1", "stack", "id", "Function", p); err == nil {
			t.Errorf("invalid case%d accepted", i)
		}
	}
	if _, err := compileStackFunction("us-east-1", "stack", "id", "bad/name", baseInlineProperties()); err == nil {
		t.Fatal("generated an unreachable function name")
	}
	f, err := compileStackFunction("us-east-1", strings.Repeat("s", 128), "id", "Function", baseInlineProperties())
	if err != nil || len(f.Name) > 64 || f.Timeout != 3 {
		t.Fatal(f, err)
	}
	other, err := compileStackFunction("eu-west-1", strings.Repeat("s", 128), "other-id", "Function", baseInlineProperties())
	if err != nil || f.Name == other.Name {
		t.Fatal(other, err)
	}
}
func TestExplicitResourceDependenciesValidateAndOrder(t *testing.T) {
	makeResource := func(dep any) map[string]any {
		r := map[string]any{"Type": "AWS::SSM::Parameter", "Properties": map[string]any{"Type": "String", "Value": "safe"}}
		if dep != nil {
			r["DependsOn"] = dep
		}
		return r
	}
	for _, dep := range []any{"Z", []any{"Z"}} {
		resources := map[string]any{"A": makeResource(dep), "Z": makeResource(nil)}
		order, err := resourceOrder(resources)
		if err != nil || strings.Join(order, ",") != "Z,A" {
			t.Fatal(order, err)
		}
	}
	for _, dep := range []any{true, []any{}, []any{true}, "missing", "Z.Arn", "A"} {
		if _, err := resourceOrder(map[string]any{"A": makeResource(dep), "Z": makeResource(nil)}); err == nil {
			t.Fatal("accepted invalid dependency", dep)
		}
	}
	resources := map[string]any{"A": makeResource("Z"), "Z": makeResource("A")}
	if _, err := resourceOrder(resources); err == nil {
		t.Fatal("accepted cycle")
	}
}
func TestLambdaStackUnderlyingPermissionPlan(t *testing.T) {
	record, err := compileStack("ap-northeast-1", "inline", inlineStackTemplate, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"iam:PassRole", "lambda:CreateFunction", "lambda:TagResource"} {
		seen := false
		err = stackResourcePermissions(record, false, func(action, arn string) error {
			if action == denied {
				seen = true
				return errors.New("denied")
			}
			return nil
		})
		if err == nil || !seen {
			t.Fatal(denied, err)
		}
	}
	var calls []string
	if err = stackResourcePermissions(record, true, func(action, arn string) error { calls = append(calls, action+" "+arn); return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "lambda:DeleteFunction "+record.Outputs["Arn"]) {
		t.Fatal(calls)
	}
	encoded, _ := json.Marshal(record)
	if !json.Valid(encoded) {
		t.Fatal("invalid record")
	}
}

func TestLambdaStackDeleteRollbackRestoresFunctionCodeAndClaims(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	lambdas, err := NewLambdaStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer lambdas.Close()
	record, err := compileStack("us-east-1", "rollback", inlineStackTemplate, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	before, err := lambdas.get(record.Region, "cfn-inline-app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER fail_role_cleanup BEFORE DELETE ON identity_policies BEGIN SELECT RAISE(ABORT,'dummy-private-fault'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Delete(record.Region, record.ID); err == nil {
		t.Fatal("accepted failing teardown")
	}
	after, err := lambdas.get(record.Region, before.Name)
	if err != nil || string(after.Code) != string(before.Code) || after.Revision != before.Revision {
		t.Fatal("failed teardown lost function", err)
	}
	var count int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?", record.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("claims lost", count, err)
	}
	if _, err = db.Database().Exec("DROP TRIGGER fail_role_cleanup"); err != nil {
		t.Fatal(err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
}
