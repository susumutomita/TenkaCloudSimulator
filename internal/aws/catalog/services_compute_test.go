package catalog

import (
	"errors"
	"testing"
)

func pipelineInput(mutate func(map[string]any)) map[string]any {
	action := func(name, category string) map[string]any {
		return map[string]any{"name": name, "actionTypeId": map[string]any{"category": category, "owner": "AWS", "provider": "S3", "version": "1"}}
	}
	pipeline := map[string]any{"artifactStore": map[string]any{"type": "S3", "location": "b"}, "stages": []any{
		map[string]any{"name": "Source", "actions": []any{action("Source", "Source")}},
		map[string]any{"name": "Deploy", "actions": []any{action("Deploy", "Deploy")}},
	}}
	if mutate != nil {
		mutate(pipeline)
	}
	return map[string]any{"pipeline": pipeline}
}

func stage(pipeline map[string]any, i int) map[string]any {
	return pipeline["stages"].([]any)[i].(map[string]any)
}

func TestComputeCheckHooksApplyDocumentedRules(t *testing.T) {
	fargate := func(cpu, memory, mode string) map[string]any {
		return map[string]any{"family": "web", "requiresCompatibilities": []any{"FARGATE"}, "networkMode": mode, "cpu": cpu, "memory": memory}
	}
	cases := []struct {
		name  string
		hook  func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"scaling order", appRunnerScaling, map[string]any{"MinSize": int64(10), "MaxSize": int64(5)}, "InvalidRequestException"},
		{"scaling defaults", appRunnerScaling, map[string]any{"MinSize": int64(3)}, ""},
		{"image builder uppercase", imageBuilderName, map[string]any{"name": "Fixture Dist"}, "InvalidRequestException"},
		{"image builder lowercase", imageBuilderName, map[string]any{"name": "fixture-dist"}, ""},
		{"cluster name", ecsCreateCluster, map[string]any{"clusterName": "bad name"}, "InvalidParameterException"},
		{"custom provider", ecsCreateCluster, map[string]any{"capacityProviders": []any{"asg"}}, "InvalidParameterException"},
		{"unassociated strategy", ecsCapacityProviders, map[string]any{"defaultCapacityProviderStrategy": []any{map[string]any{"capacityProvider": "FARGATE"}}}, "InvalidParameterException"},
		{"service connect", ecsCapacityProviders, map[string]any{"serviceConnectDefaults": map[string]any{"namespace": "ns"}}, "NamespaceNotFoundException"},
		{"fargate providers", ecsCapacityProviders, map[string]any{"capacityProviders": []any{"FARGATE", "FARGATE_SPOT"}, "defaultCapacityProviderStrategy": []any{map[string]any{"capacityProvider": "FARGATE_SPOT"}}}, ""},
		{"family characters", ecsTaskDefinition, map[string]any{"family": "web:1"}, "ClientException"},
		{"ec2 task", ecsTaskDefinition, map[string]any{"family": "web", "networkMode": "bridge"}, ""},
		{"fargate network", ecsTaskDefinition, fargate("256", "512", "bridge"), "ClientException"},
		{"fargate size", ecsTaskDefinition, fargate("256", "1536", "awsvpc"), "ClientException"},
		{"fargate units", ecsTaskDefinition, fargate("1 vCPU", "2 GB", "awsvpc"), ""},
		{"fargate bad units", ecsTaskDefinition, fargate("many vCPU", "2 GB", "awsvpc"), "ClientException"},
		{"other revision", ecsTaskDefinitionRevision, map[string]any{"taskDefinition": "web:2"}, "ClientException"},
		{"family only", ecsTaskDefinitionRevision, map[string]any{"taskDefinition": "web"}, ""},
		{"outposts", eksCluster, map[string]any{"outpostConfig": map[string]any{}}, "InvalidParameterException"},
		{"deletion protection", eksCluster, map[string]any{"deletionProtection": true}, "InvalidParameterException"},
		{"kubernetes version", eksCluster, map[string]any{"version": "latest"}, "InvalidParameterException"},
		{"eks cluster", eksCluster, map[string]any{"version": "1.31", "deletionProtection": false}, ""},
		{"upstreams", codeArtifactUpstreams, map[string]any{"upstreams": []any{map[string]any{"repositoryName": "r"}}}, "ValidationException"},
		{"no upstreams", codeArtifactUpstreams, map[string]any{"upstreams": []any{}}, ""},
		{"predefined read", codeDeployPredefined("InvalidDeploymentConfigNameException"), map[string]any{"deploymentConfigName": "CodeDeployDefault.AllAtOnce"}, "InvalidDeploymentConfigNameException"},
		{"predefined create", codeDeployCreateConfig, map[string]any{"deploymentConfigName": "CodeDeployDefault.Mine"}, "InvalidDeploymentConfigNameException"},
		{"server hosts", codeDeployCreateConfig, map[string]any{"deploymentConfigName": "c"}, "InvalidMinimumHealthyHostValueException"},
		{"server routing", codeDeployCreateConfig, map[string]any{"deploymentConfigName": "c", "minimumHealthyHosts": map[string]any{}, "trafficRoutingConfig": map[string]any{}}, "InvalidTrafficRoutingConfigurationException"},
		{"lambda routing", codeDeployCreateConfig, map[string]any{"deploymentConfigName": "c", "computePlatform": "Lambda", "trafficRoutingConfig": map[string]any{}}, ""},
		{"valid pipeline", codePipelineDeclaration, pipelineInput(nil), ""},
		{"two stores", codePipelineDeclaration, pipelineInput(func(p map[string]any) { p["artifactStores"] = map[string]any{} }), "InvalidStructureException"},
		{"one stage", codePipelineDeclaration, pipelineInput(func(p map[string]any) { p["stages"] = p["stages"].([]any)[:1] }), "InvalidStructureException"},
		{"duplicate stage", codePipelineDeclaration, pipelineInput(func(p map[string]any) { stage(p, 1)["name"] = "Source" }), "InvalidStageDeclarationException"},
		{"empty stage", codePipelineDeclaration, pipelineInput(func(p map[string]any) { stage(p, 1)["actions"] = []any{} }), "InvalidStageDeclarationException"},
		{"third party", codePipelineDeclaration, pipelineInput(func(p map[string]any) {
			stage(p, 0)["actions"].([]any)[0].(map[string]any)["actionTypeId"].(map[string]any)["owner"] = "ThirdParty"
		}), "InvalidActionDeclarationException"},
		{"late source", codePipelineDeclaration, pipelineInput(func(p map[string]any) {
			stage(p, 1)["actions"].([]any)[0].(map[string]any)["actionTypeId"].(map[string]any)["category"] = "Source"
		}), "InvalidStructureException"},
		{"duplicate action", codePipelineDeclaration, pipelineInput(func(p map[string]any) {
			actions := stage(p, 1)["actions"].([]any)
			stage(p, 1)["actions"] = append(actions, actions[0])
		}), "InvalidActionDeclarationException"},
		{"pipeline version", codePipelineVersion, map[string]any{"version": int64(2)}, "PipelineVersionNotFoundException"},
		{"pipeline first version", codePipelineVersion, map[string]any{"version": int64(1)}, ""},
		{"git suffix", codeCommitName, map[string]any{"repositoryName": "app.git"}, "InvalidRepositoryNameException"},
		{"repository name", codeCommitName, map[string]any{"repositoryName": "app"}, ""},
	}
	for _, c := range cases {
		err := c.hook(c.input)
		var f *fault
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &f) || f.code != c.code) {
			t.Fatalf("%s: want %q, got %v", c.name, c.code, err)
		}
	}
	sizes := map[[2]int64]bool{{512, 1024}: true, {512, 1536}: false, {1024, 8192}: true, {2048, 4096}: true,
		{4096, 30720}: true, {8192, 20480}: true, {8192, 18432}: false, {16384, 40960}: true, {100, 512}: false}
	for size, want := range sizes {
		if fargateSize(size[0], size[1]) != want {
			t.Fatalf("Fargate size %v: want %v", size, want)
		}
	}
}

// The default cluster and family:1 identifiers follow the official API.
func TestECSDefaultClusterAndSingleRevision(t *testing.T) {
	h := newHarness(t, ecs)
	created := h.json1("ecs", "CreateCluster", map[string]any{})
	expect(t, created, 200, "")
	if created.body["cluster"].(map[string]any)["clusterArn"] != "arn:aws:ecs:us-east-1:000000000000:cluster/default" {
		t.Fatalf("default cluster %v", created.body)
	}
	described := h.json1("ecs", "DescribeClusters", map[string]any{})
	if clusters := described.body["clusters"].([]any); len(clusters) != 1 {
		t.Fatalf("default describe %v", described.body)
	}
	expect(t, h.json1("ecs", "CreateCluster", map[string]any{"clusterName": "default"}), 400, "InvalidParameterException")
	task := map[string]any{"family": "web", "containerDefinitions": []any{map[string]any{"name": "web", "image": "nginx"}}}
	registered := h.json1("ecs", "RegisterTaskDefinition", task)
	expect(t, registered, 200, "")
	arn := registered.body["taskDefinition"].(map[string]any)["taskDefinitionArn"]
	if arn != "arn:aws:ecs:us-east-1:000000000000:task-definition/web:1" {
		t.Fatalf("task definition ARN %v", arn)
	}
	for _, identifier := range []any{"web", "web:1", arn} {
		expect(t, h.json1("ecs", "DescribeTaskDefinition", map[string]any{"taskDefinition": identifier}), 200, "")
	}
	expect(t, h.json1("ecs", "DescribeTaskDefinition", map[string]any{"taskDefinition": "web:3"}), 400, "ClientException")
	expect(t, h.json1("ecs", "RegisterTaskDefinition", task), 400, "InvalidParameterException")
	deregistered := h.json1("ecs", "DeregisterTaskDefinition", map[string]any{"taskDefinition": "web:1"})
	if deregistered.body["taskDefinition"].(map[string]any)["status"] != "INACTIVE" {
		t.Fatalf("deregistered %v", deregistered.body)
	}
	expect(t, h.json1("ecs", "RegisterTaskDefinition", task), 200, "")
}
