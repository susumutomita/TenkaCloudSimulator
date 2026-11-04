package aws

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"go.yaml.in/yaml/v3"
)

func cfnAPIBoundaryJSON(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(apiStackTemplate), &node); err != nil {
		t.Fatal(err)
	}
	tree, err := taggedYAML(&node)
	if err != nil {
		t.Fatal(err)
	}
	document := object(tree)
	edit(object(document["Resources"]))
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func cfnAPIBoundaryProperties(resources map[string]any, logical string) map[string]any {
	return object(object(resources[logical])["Properties"])
}

func assertCFNAPIBoundaryPreserved(t *testing.T, db *core.Store, apis *APIGatewayStore, foreign RESTAPI, before []RESTResource) {
	t.Helper()
	for table, expected := range map[string]int{"aws_cfn_stacks": 0, "aws_cfn_ownership": 0, "aws_apigateway_apis": 1, "aws_apigateway_resources": len(before), "aws_apigateway_deployments": 0, "aws_apigateway_stages": 0} {
		var count int
		if err := db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != expected {
			t.Fatalf("rejected input changed %s: got%d want%d err%v", table, count, expected, err)
		}
	}
	current, err := apis.API(context.Background(), "us-east-1", foreign.ID)
	if err != nil || !reflect.DeepEqual(current, foreign) {
		t.Fatal("foreign API metadata changed", current, foreign, err)
	}
	resources, err := apis.Resources(context.Background(), "us-east-1", foreign.ID, "", 100)
	if err != nil || !reflect.DeepEqual(resources, before) {
		t.Fatal("foreign resource graph changed", resources, before, err)
	}
}

func TestCFNAPIBoundaryInvalidContractsLeaveNoOwnedGraph(t *testing.T) {
	tests := []struct {
		name string
		late bool
		edit func(map[string]any)
	}{
		{"unsupported-api-key-resource", false, func(r map[string]any) { object(r["Proxy"])["Type"] = "AWS::ApiGateway::ApiKey" }},
		{"resource-parent-required", false, func(r map[string]any) { cfnAPIBoundaryProperties(r, "Proxy")["ParentId"] = "" }},
		{"resource-path-is-one-segment", false, func(r map[string]any) { cfnAPIBoundaryProperties(r, "Proxy")["PathPart"] = "bad/path" }},
		{"api-keys-not-supported", false, func(r map[string]any) { cfnAPIBoundaryProperties(r, "RootMethod")["ApiKeyRequired"] = true }},
		{"unknown-integration-template", false, func(r map[string]any) {
			object(cfnAPIBoundaryProperties(r, "RootMethod")["Integration"])["RequestTemplates"] = map[string]any{"application/json": "{}"}
		}},
		{"greedy-binding-must-be-map", false, func(r map[string]any) {
			object(cfnAPIBoundaryProperties(r, "ProxyMethod")["Integration"])["RequestParameters"] = []any{"unsupported"}
		}},
		{"greedy-binding-must-name-proxy", false, func(r map[string]any) {
			object(cfnAPIBoundaryProperties(r, "ProxyMethod")["Integration"])["RequestParameters"] = map[string]any{"integration.request.path.proxy": "method.request.path.other"}
		}},
		{"greedy-binding-needs-declared-method-param", false, func(r map[string]any) { delete(cfnAPIBoundaryProperties(r, "ProxyMethod"), "RequestParameters") }},
		{"mock-integration-not-supported", false, func(r map[string]any) {
			object(cfnAPIBoundaryProperties(r, "RootMethod")["Integration"])["Type"] = "MOCK"
		}},
		{"stage-variables-must-be-map", true, func(r map[string]any) { cfnAPIBoundaryProperties(r, "Stage")["Variables"] = []any{"not-a-map"} }},
		{"stage-variable-value-must-be-string", true, func(r map[string]any) {
			cfnAPIBoundaryProperties(r, "Stage")["Variables"] = map[string]any{"Invalid": true}
		}},
		{"method-parameters-must-be-map", true, func(r map[string]any) {
			p := cfnAPIBoundaryProperties(r, "ProxyMethod")
			p["RequestParameters"] = "not-a-map"
			delete(object(p["Integration"]), "RequestParameters")
		}},
		{"method-parameter-cannot-be-false", true, func(r map[string]any) {
			p := cfnAPIBoundaryProperties(r, "ProxyMethod")
			p["RequestParameters"] = map[string]any{"method.request.path.proxy": false}
			delete(object(p["Integration"]), "RequestParameters")
		}},
	}
	for _, principal := range []string{"events.amazonaws.com", "*"} {
		tests = append(tests, struct {
			name string
			late bool
			edit func(map[string]any)
		}{"foreign-lambda-principal-" + principal, false, func(r map[string]any) {
			r["Permission"] = map[string]any{"Type": "AWS::Lambda::Permission", "Properties": map[string]any{"FunctionName": "app", "Action": "lambda:InvokeFunction", "Principal": principal, "SourceArn": "arn:aws:execute-api:us-east-1:000000000000:0123456789/*/*/*"}}
		}})
	}
	tests = append(tests, struct {
		name string
		late bool
		edit func(map[string]any)
	}{"foreign-lambda-source-account", false, func(r map[string]any) {
		r["Permission"] = map[string]any{"Type": "AWS::Lambda::Permission", "Properties": map[string]any{"FunctionName": "app", "Action": "lambda:InvokeFunction", "Principal": "apigateway.amazonaws.com", "SourceArn": "arn:aws:execute-api:us-east-1:111111111111:0123456789/*/*/*"}}
	}})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			apis, err := NewAPIGatewayStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := apis.CreateAPI(context.Background(), "us-east-1", "foreign-boundary-api", "preserve-native", map[string]string{"Owner": "native"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := apis.Resources(context.Background(), "us-east-1", foreign.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			record, err := compileStack("us-east-1", "invalid-contract", cfnAPIBoundaryJSON(t, test.edit), nil, "")
			if test.late {
				if err != nil {
					t.Fatal("expected native provisioning boundary, not earlier rejection", err)
				}
				if err = stacks.Create(record); err == nil {
					t.Fatal("unsupported request metadata persisted")
				}
			} else if err == nil {
				t.Fatal("unsupported resource contract reached native provisioning")
			}
			assertCFNAPIBoundaryPreserved(t, db, apis, foreign, before)
			// A rejected template must not poison reservations or subsequent
			// native provisioning and reverse cleanup of a corrected template.
			repaired, err := compileStack("us-east-1", "invalid-contract", apiStackTemplate, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(repaired); err != nil {
				t.Fatal("repair could not provision", err)
			}
			if err = stacks.Delete(repaired.Region, repaired.ID); err != nil {
				t.Fatal("repair could not clean up", err)
			}
			assertCFNAPIBoundaryPreserved(t, db, apis, foreign, before)
		})
	}
}

func TestCFNAPIBoundaryCannotAdoptForeignNativeChildren(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := apis.CreateAPI(context.Background(), "us-east-1", "foreign-parent", "native-owned", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := apis.Resources(context.Background(), "us-east-1", foreign.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"Resources": map[string]any{
		"AOwnedAPI":     map[string]any{"Type": "AWS::ApiGateway::RestApi", "Properties": map[string]any{"Name": "rollback-owned-api", "EndpointConfiguration": map[string]any{"Types": []any{"REGIONAL"}}}},
		"ZForeignChild": map[string]any{"Type": "AWS::ApiGateway::Resource", "DependsOn": "AOwnedAPI", "Properties": map[string]any{"RestApiId": foreign.ID, "ParentId": foreign.RootResourceID, "PathPart": "must-not-adopt"}},
	}})
	record, err := compileStack("us-east-1", "foreign-child", string(body), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err == nil {
		t.Fatal("a stack adopted a child of an API it does not own")
	}
	assertCFNAPIBoundaryPreserved(t, db, apis, foreign, before)
}

func TestCFNAPIBoundaryValidTagsAndStageVariablesRoundTrip(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	body := cfnAPIBoundaryJSON(t, func(r map[string]any) {
		cfnAPIBoundaryProperties(r, "Api")["Tags"] = []any{map[string]any{"Key": "Owner", "Value": "learner"}, map[string]any{"Key": "Purpose", "Value": "roundtrip"}}
		cfnAPIBoundaryProperties(r, "Stage")["Variables"] = map[string]any{"environment": "local", "flag": "literal-value"}
	})
	record, err := compileStack("us-east-1", "valid-metadata", body, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	apis, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := apis.API(context.Background(), record.Region, record.Outputs["ApiId"])
	if err != nil || !reflect.DeepEqual(api.Tags, map[string]string{"Owner": "learner", "Purpose": "roundtrip"}) {
		t.Fatal("tags did not reach native state", api.Tags, err)
	}
	stage, err := apis.Stage(context.Background(), record.Region, api.ID, "prod")
	if err != nil || !reflect.DeepEqual(stage.Variables, map[string]string{"environment": "local", "flag": "literal-value"}) {
		t.Fatal("variables did not reach native state", stage.Variables, err)
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
}
