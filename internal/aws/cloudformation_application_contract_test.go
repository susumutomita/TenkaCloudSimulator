package aws

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestStackStoreRefusesIncompatibleApplicationSchema(t *testing.T) {
	for _, table := range []string{
		"aws_elb_load_balancers", "aws_cloudfront_vpc_origins", "aws_ec2_network_resources",
		"aws_apigateway_apis", "aws_lambda_resource_policies", "aws_catalog_resources", "aws_iam_roles",
	} {
		t.Run(table, func(t *testing.T) {
			state, err := core.New(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			db := state.Database()
			// An index and a table cannot occupy the same name in SQLite.
			if _, err := db.Exec("CREATE TABLE existing_resource(id INTEGER); CREATE INDEX " + table + " ON existing_resource(id)"); err != nil {
				t.Fatal(err)
			}
			if store, err := NewStackStore(db); store != nil || err == nil || !strings.Contains(err.Error(), table) {
				t.Fatalf("expected startup to report the conflicting table %s, got %v", table, err)
			}
			if _, err := db.Exec("DROP INDEX " + table); err != nil {
				t.Fatal(err)
			}
			store, err := NewStackStore(db)
			if err != nil {
				t.Fatal(err)
			}
			if rows, err := store.Records("us-east-1", ""); err != nil || len(rows) != 0 {
				t.Fatalf("startup retry on the repaired database: %v, %v", rows, err)
			}
		})
	}
}

func TestStackRulesRejectInvalidGroupsBeforeCreatingResources(t *testing.T) {
	const assertion = "    Assertions: [{Assert: !Equals [a, a]}]\n"
	const resources = "Resources:\n  Setting: {Type: 'AWS::SSM::Parameter', Properties: {Type: String, Value: sample}}\n"
	var many strings.Builder
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&many, "  Rule%d:\n%s", i, assertion)
	}
	gateway := ssmValueGateway(t)
	before := stackConditionalDBFrame(t, gateway.Store.Database())
	for _, tc := range []struct{ name, rule, reason string }{
		{"too many rules", many.String(), "rule count exceeds"},
		{"invalid name", "  Bad-name:\n" + assertion, "invalid rule declaration"},
		{"empty assertions", "  Rule: {Assertions: []}\n", "requires 1–100 assertions"},
		{"invalid guard", "  Rule:\n    RuleCondition: !Not []\n" + assertion, "Not requires exactly one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "Rules:\n" + tc.rule + resources
			conditionBoundaryError(t, validationRequest(gateway, body, "", nil), 400, "ValidationError", tc.reason)
			conditionBoundaryError(t, conditionBoundarySend(gateway, "CreateStack", body, "", nil), 400, "ValidationError", tc.reason)
			if stackConditionalDBFrame(t, gateway.Store.Database()) != before {
				t.Fatal("rule rejection changed stack state")
			}
		})
	}
}

func TestApplicationStackAuthorizesEachResourceBeforeDelete(t *testing.T) {
	body, err := os.ReadFile("../../examples/cloudformation-rest-lambda.json")
	if err != nil {
		t.Fatal(err)
	}
	const region = "ap-northeast-1"
	record, err := compileStack(region, "sample", string(body), nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for _, r := range record.Resources {
		ids[r.LogicalID] = r.PhysicalID
	}
	base := "arn:aws:apigateway:" + region + "::/restapis/" + ids["Api"]
	state, _, stacks, _ := s3StackState(t)
	functions, err := NewLambdaStore(state.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer functions.Close()
	if err := stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("required application grant missing")
	for _, required := range []string{
		"apigateway:DELETE " + base,
		"apigateway:DELETE " + base + "/resources/" + ids["Resource"],
		"apigateway:DELETE " + base + "/resources/" + ids["Resource"] + "/methods/GET",
		"apigateway:DELETE " + base + "/deployments/" + ids["Deployment"],
		"apigateway:DELETE " + base + "/stages/prod",
		"lambda:RemovePermission " + lambdaARN(region, ids["Function"]),
		"iam:DeleteRolePolicy arn:aws:iam::000000000000:role/" + ids["Role"],
	} {
		t.Run(required, func(t *testing.T) {
			err := stacks.deleteChecked(region, record.ID, func(snapshot StackRecord) error {
				return stackResourcePermissions(snapshot, true, func(action, arn string) error {
					if action+" "+arn == required {
						return denied
					}
					return nil
				})
			})
			if !errors.Is(err, denied) {
				t.Fatalf("missing %s must reject deletion: %v", required, err)
			}
			rows, err := stacks.Records(region, record.ID)
			if err != nil || len(rows) != 1 || len(rows[0].Resources) != len(record.Resources) {
				t.Fatalf("denied delete changed the application stack: %v", err)
			}
		})
	}
	if err := stacks.Delete(region, record.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStackQueryRejectsAmbiguousInput(t *testing.T) {
	gateway := ssmValueGateway(t)
	for _, tc := range []struct {
		field  url.Values
		reason string
	}{
		{url.Values{"NextToken": {"unused"}}, "NextToken is supported only by DescribeStackEvents"},
		{url.Values{"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"one"},
			"Tags.member.2.Key": {"owner"}, "Tags.member.2.Value": {"two"}}, "Duplicate tag key"},
	} {
		response := conditionBoundarySend(gateway, "CreateStack", apiStackTemplate, "", tc.field)
		conditionBoundaryError(t, response, 400, "ValidationError", tc.reason)
	}
}
