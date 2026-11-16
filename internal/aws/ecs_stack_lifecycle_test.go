package aws

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestECSTemplateRejectsInvalidPoliciesAndNestedContainerProperties(t *testing.T) {
	g := stackECSGateway(t)
	for _, tc := range []struct{ operation, old, replacement, message string }{
		{"ValidateTemplate", "DeletionPolicy: Retain", "DeletionPolicy: Snapshot", "Retain or Delete"},
		{"CreateStack", "UpdateReplacePolicy: Retain", "UpdateReplacePolicy: Snapshot", "Retain or Delete"},
		{"CreateStack", "Essential: true", "UnrecognizedOption: true", "unsupported task definition property"},
		{"CreateStack", "ContainerPort: !Ref Port", "ContainerPort: 'not-a-port'", "containerPort"},
		{"CreateStack", "Memory: '512'", "Memory: '900'", "supported task-level cpu and memory"},
	} {
		t.Run(tc.operation+"/"+tc.replacement, func(t *testing.T) {
			body := strings.Replace(ecsTaskTemplate, tc.old, tc.replacement, 1)
			var reply *httptest.ResponseRecorder
			if tc.operation == "ValidateTemplate" {
				reply = validationRequest(g, body, "", nil)
			} else {
				reply = conditionBoundarySend(g, tc.operation, body, "", nil)
			}
			if reply.Code != 400 || !strings.Contains(reply.Body.String(), tc.message) {
				t.Fatalf("want validation error %q, got %d %s", tc.message, reply.Code, reply.Body)
			}
		})
	}
	nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo"}`, 400)
}

func TestECSStackRejectsChangedFamilyOrMalformedStoredRevision(t *testing.T) {
	g := stackECSGateway(t)
	for _, tc := range []struct{ name, physical, message string }{
		{"absent", "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/demo", "Invalid task definition revision"},
		{"zero", "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/demo:0", "Invalid task definition revision"},
		{"nonnumeric", "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/demo:first", "Invalid task definition revision"},
		{"family", "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/other:1", "differs from the planned ARN"},
		{"properties", "", "unsupported task definition property"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, err := compileStack("ap-northeast-1", "bad-record", ecsTaskTemplate, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			for i := range record.Resources {
				r := &record.Resources[i]
				if r.Type != stackTaskDefinition {
					continue
				}
				if tc.physical != "" {
					r.PhysicalID = tc.physical
				} else {
					r.Properties["BadProperty"] = "value"
				}
			}
			if err := g.Stacks.Create(record); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("want error %q, got %v", tc.message, err)
			}
			records, err := g.Stacks.Records(record.Region, "")
			if err != nil || len(records) != 0 {
				t.Fatalf("unexpected records %v, err %v", records, err)
			}
			nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo"}`, 400)
		})
	}
}

func TestECSStackPlansEachEnabledRevisionOnceAndPropagatesLookupFailure(t *testing.T) {
	const template = `Conditions:
  Skip: !Equals ['one', 'two']
Resources:
  AExcluded:
    Type: AWS::ECS::TaskDefinition
    Condition: Skip
    Properties: {Family: demo, ContainerDefinitions: [{Name: app, Image: first}]}
  BFirst:
    Type: AWS::ECS::TaskDefinition
    Properties: {Family: demo, ContainerDefinitions: [{Name: app, Image: second}]}
  CSecond:
    Type: AWS::ECS::TaskDefinition
    DependsOn: BFirst
    Properties: {Family: demo, ContainerDefinitions: [{Name: app, Image: third}]}
`
	failure := errors.New("revision store unavailable")
	_, err := compileStackWithResolvers("us-east-1", "lookup", template, nil, "", nil, func(string, string) (int, error) { return 0, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("want resolver failure, got %v", err)
	}
	var reads int
	r, err := compileStackWithResolvers("us-east-1", "lookup", template, nil, "", nil, func(id, family string) (int, error) {
		if family != "demo" || !strings.Contains(id, ":stack/lookup/") {
			t.Fatalf("unexpected resolver scope %s %s", id, family)
		}
		reads++
		return 6, nil
	})
	if err != nil || reads != 1 || len(r.Resources) != 2 {
		t.Fatalf("reads=%d resources=%v err=%v", reads, r.Resources, err)
	}
	if !strings.HasSuffix(r.Resources[0].PhysicalID, "/demo:6") || !strings.HasSuffix(r.Resources[1].PhysicalID, "/demo:7") {
		t.Fatalf("unexpected revisions %v", r.Resources)
	}
}

func TestECSTaskAndStackEventAreCommittedTogether(t *testing.T) {
	g := stackECSGateway(t)
	db := g.Store.Database()
	// An unavailable event store must roll back both the registered revision and its stack ownership.
	if _, err := db.Exec(`CREATE TRIGGER fail_ecs_create_event BEFORE INSERT ON aws_cfn_events
WHEN NEW.resource_type='AWS::ECS::TaskDefinition' AND NEW.status='CREATE_COMPLETE'
BEGIN SELECT RAISE(ABORT,'test event failure'); END`); err != nil {
		t.Fatal(err)
	}
	create := func() int { return conditionBoundarySend(g, "CreateStack", ecsTaskTemplate, "", nil).Code }
	if code := create(); code == 200 {
		t.Fatal("failed event produced a successful stack")
	}
	nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo"}`, 400)
	if _, err := db.Exec("DROP TRIGGER fail_ecs_create_event"); err != nil {
		t.Fatal(err)
	}
	if code := create(); code != 200 {
		t.Fatalf("creation retry returned %d", code)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_ecs_retain_event BEFORE INSERT ON aws_cfn_events
WHEN NEW.resource_type='AWS::ECS::TaskDefinition' AND NEW.status='DELETE_SKIPPED'
BEGIN SELECT RAISE(ABORT,'test retain event failure'); END`); err != nil {
		t.Fatal(err)
	}
	remove := func() int { return conditionBoundarySend(g, "DeleteStack", "", "", nil).Code }
	if remove() == 200 {
		t.Fatal("failed event removed stack")
	}
	if _, err := g.Parameters.Get("ap-northeast-1", "/demo/revision"); err != nil {
		t.Fatalf("partial stack deletion: %v", err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_ecs_retain_event"); err != nil {
		t.Fatal(err)
	}
	if code := remove(); code != 200 {
		t.Fatalf("deletion retry returned %d", code)
	}
	nativeStackECS(t, g, "DescribeTaskDefinition", `{"taskDefinition":"demo:1"}`, 200)
}
