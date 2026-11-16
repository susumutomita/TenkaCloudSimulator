package aws

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const ecsTaskTemplate = `Parameters:
  Port: {Type: String, Default: '8080'}
Resources:
  AppTask:
    Type: AWS::ECS::TaskDefinition
    DeletionPolicy: Retain
    UpdateReplacePolicy: Retain
    Properties:
      Family: demo
      Cpu: '256'
      Memory: '512'
      NetworkMode: awsvpc
      RequiresCompatibilities: [FARGATE]
      ExecutionRoleArn: arn:aws:iam::000000000000:role/app
      ContainerDefinitions:
        - Name: app
          Essential: true
          Image: example/app:v1
          PortMappings: [{ContainerPort: !Ref Port}]
          Environment: [{Name: PORT, Value: !Ref Port}]
          Secrets: [{Name: PGPASSWORD, ValueFrom: 'arn:aws:ssm:ap-northeast-1:000000000000:parameter/app/password'}]
          LogConfiguration: {LogDriver: awslogs, Options: {awslogs-region: !Ref 'AWS::Region', awslogs-group: app}}
      Tags: [{Key: tenant, Value: trial}]
  SavedRevision:
    Type: AWS::SSM::Parameter
    Properties: {Type: String, Name: /demo/revision, Value: !Ref AppTask}
Outputs:
  Revision: {Value: !GetAtt AppTask.TaskDefinitionArn}
`

func stackECSGateway(t *testing.T) *Gateway {
	t.Helper()
	g := ssmValueGateway(t)
	engine, err := catalog.New(g.Store.Database(), catalog.Config{Regions: Regions, Account: "000000000000", DefaultPrincipal: core.Administrator, Authorize: g.Identities.DecisionContext}, catalog.Services())
	if err != nil {
		t.Fatal(err)
	}
	g.Catalog = engine
	g.CatalogOwns = engine.Owns
	return g
}

func nativeStackECS(t *testing.T, g *Gateway, op, input string, want int) map[string]any {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(input))
	r.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113."+op)
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/20261106/ap-northeast-1/ecs/aws4_request, SignedHeaders=host, Signature=0")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s returned %d: %s", op, w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStackECSTaskDefinitionPublishesExactNativeRevisionAndRetainsOnDelete(t *testing.T) {
	g := stackECSGateway(t)
	nativeStackECS(t, g, "RegisterTaskDefinition", `{"family":"demo","containerDefinitions":[{"name":"app","image":"example/old"}]}`, 200)
	params := url.Values{"ClientRequestToken": {"publish"}}
	for range 2 {
		w := conditionBoundarySend(g, "CreateStack", ecsTaskTemplate, "", params)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	const arn = "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/demo:2"
	records, err := g.Stacks.Records("ap-northeast-1", "boundary")
	if err != nil || len(records) != 1 || records[0].Outputs["Revision"] != arn {
		t.Fatalf("stack record: %v, %v", records, err)
	}
	saved, err := g.Parameters.Get("ap-northeast-1", "/demo/revision")
	if err != nil || saved.Value != arn {
		t.Fatal(saved, err)
	}
	describe := func(ref string) map[string]any {
		return nativeStackECS(t, g, "DescribeTaskDefinition", fmt.Sprintf(`{"taskDefinition":%q}`, ref), 200)["taskDefinition"].(map[string]any)
	}
	def := describe("demo")
	containers := def["containerDefinitions"].([]any)
	app := containers[0].(map[string]any)
	if def["taskDefinitionArn"] != arn || app["portMappings"].([]any)[0].(map[string]any)["containerPort"] != float64(8080) || app["environment"].([]any)[0].(map[string]any)["value"] != "8080" || app["logConfiguration"].(map[string]any)["options"].(map[string]any)["awslogs-region"] != "ap-northeast-1" {
		t.Fatalf("native task %v", def)
	}
	if r := conditionBoundarySend(g, "DeleteStack", "", "", nil); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if describe(arn)["status"] != "ACTIVE" {
		t.Fatal("retained revision was retired")
	}
	w := conditionBoundarySend(g, "CreateStack", strings.Replace(ecsTaskTemplate, "DeletionPolicy: Retain", "DeletionPolicy: Delete", 1), "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if describe("demo")["revision"] != float64(3) {
		t.Fatal("wrong replacement revision")
	}
	if r := conditionBoundarySend(g, "DeleteStack", "", "", nil); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if describe("demo")["taskDefinitionArn"] != arn {
		t.Fatal("deletion changed a previous revision")
	}
	nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo:3"}`, 400)
}

func TestStackECSTaskDefinitionRollsBackOnLaterResourceFailure(t *testing.T) {
	g := stackECSGateway(t)
	ssmValuePut(t, g, "ap-northeast-1", "/demo/revision", "String", "existing")
	w := conditionBoundarySend(g, "CreateStack", ecsTaskTemplate, "", nil)
	if w.Code == 200 {
		t.Fatal("conflicting later SSM parameter was accepted")
	}
	nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo"}`, 400)
	record := nativeStackECS(t, g, "RegisterTaskDefinition", `{"family":"demo","containerDefinitions":[{"name":"app","image":"example/new"}]}`, 200)
	if record["taskDefinition"].(map[string]any)["revision"] != float64(1) {
		t.Fatal("rolled back stack consumed a revision")
	}
}

func TestStackECSTaskRejectsStalePlanAndDeniedPassRole(t *testing.T) {
	g := stackECSGateway(t)
	record, err := compileStack("ap-northeast-1", "guarded", ecsTaskTemplate, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = g.Stacks.createChecked(record, func(action, arn string) error {
		if action == "iam:PassRole" && arn == "arn:aws:iam::000000000000:role/app" {
			return fmt.Errorf("role denied")
		}
		return nil
	})
	if err == nil || err.Error() != "role denied" {
		t.Fatalf("wrong authorization result %v", err)
	}
	nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo"}`, 400)
	nativeStackECS(t, g, "RegisterTaskDefinition", `{"family":"demo","containerDefinitions":[{"name":"app","image":"example/existing"}]}`, 200)
	if err = g.Stacks.Create(record); err == nil || !strings.Contains(err.Error(), "family advanced") {
		t.Fatal("stale stack plan should fail", err)
	}
	r := nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo:1"}`, 200)
	container := r["taskDefinition"].(map[string]any)["containerDefinitions"].([]any)[0].(map[string]any)
	if container["image"] != "example/existing" {
		t.Fatal("stale plan overwrote a revision")
	}
	stacks, err := g.Stacks.Records(record.Region, "")
	if err != nil || len(stacks) != 0 {
		t.Fatal(stacks, err)
	}
}
