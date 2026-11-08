package catalog

import (
	"strings"
	"testing"
)

const project = "projects/tenkacloud-local"

// lifecycle creates one resource, reads and lists it, optionally patches
// one mutable field, and returns the created resource and its name.
func lifecycle(t *testing.T, h *harness, base, collection, query string, body map[string]any, mask string, value any) (map[string]any, string) {
	t.Helper()
	path := base + "/" + collection
	created := h.call("owner", "POST", path+query, body)
	expect(t, created, 200, "")
	resource := created.body
	if created.body["done"] == true {
		resource = created.body["response"].(map[string]any)
		op := h.call("owner", "GET", strings.SplitN(base, "/projects/", 2)[0]+"/"+created.body["name"].(string), nil)
		expect(t, op, 200, "")
	}
	name, _ := resource["name"].(string)
	if !strings.HasPrefix(name, project+"/") {
		name = strings.TrimPrefix(path, strings.SplitN(path, "/projects/", 2)[0]+"/") + "/" + name
	}
	prefix := strings.SplitN(base, "/projects/", 2)[0] + "/"
	expect(t, h.call("owner", "GET", prefix+name, nil), 200, "")
	listed := h.call("owner", "GET", path, nil)
	expect(t, listed, 200, "")
	if items, _ := listed.body[collection].([]any); len(items) == 0 {
		t.Fatalf("%s list is empty: %v", collection, listed.body)
	}
	if mask != "" {
		expect(t, h.call("owner", "PATCH", prefix+name+"?updateMask="+mask, map[string]any{mask: value}), 200, "")
		got := h.call("owner", "GET", prefix+name, nil)
		if got.body[mask] == nil {
			t.Fatalf("%s patch did not persist: %v", collection, got.body)
		}
	}
	return resource, prefix + name
}

func operationsListed(t *testing.T, h *harness, base string, want int) {
	t.Helper()
	ops := h.call("owner", "GET", base+"/operations", nil)
	expect(t, ops, 200, "")
	if got := len(ops.body["operations"].([]any)); got != want {
		t.Fatalf("%s operations %d, want %d", base, got, want)
	}
}

func TestArtifactRegistryRepositories(t *testing.T) {
	h := newHarness(t)
	base := "/artifactregistry/v1/" + project + "/locations/us-central1"
	repo, name := lifecycle(t, h, base, "repositories", "?repositoryId=docker", map[string]any{"format": "DOCKER", "labels": map[string]any{"team": "a"}, "sizeBytes": "99"}, "description", "Images")
	if repo["mode"] != "STANDARD_REPOSITORY" || repo["sizeBytes"] != "0" || repo["registryUri"] != nil || repo["createTime"] == nil {
		t.Fatalf("repository outputs %v", repo)
	}
	secret := "projects/tenkacloud-local/secrets/upstream/versions/1"
	remote := map[string]any{"format": "NPM", "mode": "REMOTE_REPOSITORY", "remoteRepositoryConfig": map[string]any{"npmRepository": map[string]any{"publicRepository": "NPMJS"},
		"upstreamCredentials": map[string]any{"usernamePasswordCredentials": map[string]any{"username": "u", "passwordSecretVersion": secret}}}}
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=remote", remote), 200, "")
	remote["remoteRepositoryConfig"].(map[string]any)["upstreamCredentials"] = map[string]any{"usernamePasswordCredentials": map[string]any{"passwordSecretVersion": "hunter2"}}
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=leak", remote), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=mixed", map[string]any{"mode": "STANDARD_REPOSITORY", "virtualRepositoryConfig": map[string]any{}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=strict", map[string]any{"disallowUnspecifiedMode": true}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", name+"?updateMask=format", map[string]any{"format": "MAVEN"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", base+"/repositories?orderBy=name", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", name+"/packages", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", name, nil), 200, "")
	expect(t, h.call("owner", "GET", name, nil), 404, "NOT_FOUND")
}

func TestCloudBuildTriggersAndWorkerPools(t *testing.T) {
	h := newHarness(t)
	base := "/cloudbuild/v1/" + project + "/locations/us-central1"
	trigger, name := lifecycle(t, h, base, "triggers", "", map[string]any{"name": "deploy-main", "id": "ignored", "filename": "cloudbuild.yaml",
		"github": map[string]any{"owner": "acme", "name": "app", "push": map[string]any{"branch": "^main$"}}}, "description", "Main")
	if trigger["name"] != "deploy-main" || trigger["id"] != "deploy-main" || trigger["resourceName"] != project+"/locations/us-central1/triggers/deploy-main" {
		t.Fatalf("trigger identity %v", trigger)
	}
	expect(t, h.call("owner", "POST", base+"/triggers", map[string]any{"name": "-bad"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/triggers", map[string]any{"name": "deploy-main"}), 409, "ALREADY_EXISTS")
	secret := "projects/tenkacloud-local/secrets/hook/versions/latest"
	expect(t, h.call("owner", "POST", base+"/triggers", map[string]any{"name": "hook", "webhookConfig": map[string]any{"secret": secret},
		"build": map[string]any{"steps": []any{map[string]any{"name": "gcr.io/cloud-builders/docker", "secretEnv": []any{"TOKEN"}}},
			"availableSecrets": map[string]any{"secretManager": []any{map[string]any{"env": "TOKEN", "versionName": secret}}}}}), 200, "")
	for label, body := range map[string]map[string]any{
		"webhook token":  {"name": "w", "webhookConfig": map[string]any{"secret": "raw-token"}},
		"kms secrets":    {"name": "k", "build": map[string]any{"secrets": []any{map[string]any{"kmsKeyName": "k", "secretEnv": map[string]any{"A": "YWI="}}}}},
		"inline secrets": {"name": "i", "build": map[string]any{"availableSecrets": map[string]any{"inline": []any{}}}},
		"manager value":  {"name": "m", "build": map[string]any{"availableSecrets": map[string]any{"secretManager": []any{map[string]any{"versionName": "value"}}}}},
		"proxy secret":   {"name": "p", "build": map[string]any{"gitConfig": map[string]any{"http": map[string]any{"proxySecretVersionName": "http://user:pw@proxy"}}}},
	} {
		t.Run(label, func(t *testing.T) {
			expect(t, h.call("owner", "POST", base+"/triggers", body), 400, "INVALID_ARGUMENT")
		})
	}
	expect(t, h.call("owner", "PATCH", name+"?updateMask=webhookConfig", map[string]any{"webhookConfig": map[string]any{"secret": "raw"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", name+":run", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", base+"/builds", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", name, nil), 200, "")
	pool, poolName := lifecycle(t, h, base, "workerPools", "?workerPoolId=private", map[string]any{"privatePoolV1Config": map[string]any{"workerConfig": map[string]any{"machineType": "e2-standard-4"}}}, "displayName", "Private")
	if pool["state"] != "RUNNING" || pool["uid"] == nil {
		t.Fatalf("worker pool outputs %v", pool)
	}
	expect(t, h.call("owner", "DELETE", poolName+"?allowMissing=true", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "DELETE", poolName, nil), 200, "")
	expect(t, h.call("owner", "GET", base+"/operations", nil), 404, "NOT_FOUND")
}

func TestCloudDeployConfiguration(t *testing.T) {
	h := newHarness(t)
	base := "/clouddeploy/v1/" + project + "/locations/us-central1"
	target, targetName := lifecycle(t, h, base, "targets", "?targetId=staging", map[string]any{"run": map[string]any{"location": project + "/locations/us-central1"}}, "requireApproval", true)
	if target["targetId"] != "staging" || target["uid"] == nil {
		t.Fatalf("target outputs %v", target)
	}
	_, pipeline := lifecycle(t, h, base, "deliveryPipelines", "?deliveryPipelineId=web", map[string]any{"serialPipeline": map[string]any{"stages": []any{map[string]any{"targetId": "staging"}}}}, "suspended", true)
	custom, customName := lifecycle(t, h, base, "customTargetTypes", "?customTargetTypeId=helm", map[string]any{"customActions": map[string]any{"deployAction": "deploy"}}, "description", "Helm")
	if custom["customTargetTypeId"] != "helm" {
		t.Fatalf("custom target type outputs %v", custom)
	}
	_, policy := lifecycle(t, h, base, "deployPolicies", "?deployPolicyId=freeze", map[string]any{
		"selectors": []any{map[string]any{"deliveryPipeline": map[string]any{"id": "web"}}},
		"rules":     []any{map[string]any{"rolloutRestriction": map[string]any{"id": "weekend", "timeWindows": map[string]any{"timeZone": "UTC"}}}}}, "description", "Freeze")
	expect(t, h.call("owner", "POST", base+"/deployPolicies?deployPolicyId=empty", map[string]any{"rules": []any{}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", pipeline+"/releases?releaseId=r1", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "PATCH", pipeline+"?allowMissing=true", map[string]any{"description": "x"}), 400, "INVALID_ARGUMENT")
	operationsListed(t, h, base, 8)
	for _, name := range []string{pipeline, targetName, customName, policy} {
		expect(t, h.call("owner", "DELETE", name+"?allowMissing=true", nil), 400, "INVALID_ARGUMENT")
		expect(t, h.call("owner", "DELETE", name, nil), 200, "")
	}
}

func TestWorkstationsHierarchy(t *testing.T) {
	h := newHarness(t)
	base := "/workstations/v1/" + project + "/locations/us-central1"
	_, cluster := lifecycle(t, h, base, "workstationClusters", "?workstationClusterId=dev", map[string]any{"network": project + "/global/networks/default", "subnetwork": project + "/regions/us-central1/subnetworks/default"}, "displayName", "Dev")
	clusterPath := strings.TrimPrefix(cluster, "/workstations/v1/")
	_, config := lifecycle(t, h, "/workstations/v1/"+clusterPath, "workstationConfigs", "?workstationConfigId=std", map[string]any{"host": map[string]any{"gceInstance": map[string]any{"machineType": "e2-standard-4"}}, "idleTimeout": "1200s"}, "runningTimeout", "43200s")
	ws, workstation := lifecycle(t, h, config, "workstations", "?workstationId=alice", map[string]any{"env": map[string]any{"EDITOR": "vim"}, "state": "STATE_RUNNING"}, "displayName", "Alice")
	if ws["state"] != "STATE_STOPPED" || ws["host"] != nil {
		t.Fatalf("workstations never start: %v", ws)
	}
	expect(t, h.call("owner", "POST", workstation+":start", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", workstation+":generateAccessToken", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", cluster, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", config, nil), 400, "FAILED_PRECONDITION")
	operationsListed(t, h, base, 6)
	expect(t, h.call("owner", "DELETE", workstation, nil), 200, "")
	expect(t, h.call("owner", "DELETE", cluster+"?force=true", nil), 200, "")
	expect(t, h.call("owner", "GET", config, nil), 404, "NOT_FOUND")
}

func TestDeveloperConnectConnections(t *testing.T) {
	h := newHarness(t)
	base := "/developerconnect/v1/" + project + "/locations/us-central1"
	token := "projects/tenkacloud-local/secrets/github/versions/1"
	conn, connection := lifecycle(t, h, base, "connections", "?connectionId=github", map[string]any{"githubConfig": map[string]any{"githubApp": "DEVELOPER_CONNECT", "appInstallationId": "123",
		"authorizerCredential": map[string]any{"oauthTokenSecretVersion": token}}}, "disabled", true)
	if conn["installationState"] != nil || conn["uid"] == nil {
		t.Fatalf("connection outputs %v", conn)
	}
	for label, body := range map[string]map[string]any{
		"raw token":  {"githubConfig": map[string]any{"githubApp": "DEVELOPER_CONNECT", "authorizerCredential": map[string]any{"oauthTokenSecretVersion": "ghp_secret"}}},
		"no config":  {"labels": map[string]any{"a": "b"}},
		"two config": {"githubConfig": map[string]any{"githubApp": "FIREBASE"}, "gitlabConfig": map[string]any{}},
		"git proxy":  {"githubConfig": map[string]any{"githubApp": "FIREBASE"}, "gitProxyConfig": map[string]any{"enabled": true}},
	} {
		t.Run(label, func(t *testing.T) {
			expect(t, h.call("owner", "POST", base+"/connections?connectionId=bad", body), 400, "INVALID_ARGUMENT")
		})
	}
	expect(t, h.call("owner", "PATCH", connection+"?updateMask=githubConfig", map[string]any{"githubConfig": map[string]any{}}), 400, "INVALID_ARGUMENT")
	link, linkName := lifecycle(t, h, connection, "gitRepositoryLinks", "?gitRepositoryLinkId=app.git", map[string]any{"cloneUri": "https://github.com/acme/app.git"}, "", nil)
	if link["webhookId"] != nil || link["cloneUri"] != "https://github.com/acme/app.git" {
		t.Fatalf("link outputs %v", link)
	}
	expect(t, h.call("owner", "POST", connection+"/gitRepositoryLinks?gitRepositoryLinkId=x", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", linkName+":fetchReadToken", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", connection, nil), 400, "FAILED_PRECONDITION")
	_, account := lifecycle(t, h, base, "accountConnectors", "?accountConnectorId=github", map[string]any{"providerOauthConfig": map[string]any{"systemProviderId": "GITHUB", "scopes": []any{"repo"}}}, "labels", map[string]any{"team": "a"})
	expect(t, h.call("owner", "POST", base+"/accountConnectors?accountConnectorId=custom", map[string]any{"customOauthConfig": map[string]any{"clientSecret": "s"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/accountConnectors?accountConnectorId=none", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/accountConnectors?accountConnectorId=proxy", map[string]any{"providerOauthConfig": map[string]any{"scopes": []any{"repo"}}, "proxyConfig": map[string]any{"enabled": true}}), 400, "INVALID_ARGUMENT")
	operationsListed(t, h, base, 5)
	for _, name := range []string{linkName, connection, account} {
		expect(t, h.call("owner", "DELETE", name, nil), 200, "")
	}
}

func TestSecureSourceManagerInstancesAndRepositories(t *testing.T) {
	h := newHarness(t)
	base := "/securesourcemanager/v1/" + project + "/locations/us-central1"
	instance, instanceName := lifecycle(t, h, base, "instances", "?instanceId=main", map[string]any{"labels": map[string]any{"env": "dev"}}, "", nil)
	if instance["state"] != "ACTIVE" || instance["hostConfig"] != nil {
		t.Fatalf("instance outputs %v", instance)
	}
	instanceRef := strings.TrimPrefix(instanceName, "/securesourcemanager/v1/")
	_, repo := lifecycle(t, h, base, "repositories", "?repositoryId=app-repo", map[string]any{"instance": instanceRef}, "description", "App")
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=abc", map[string]any{"instance": instanceRef}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=seeded", map[string]any{"instance": instanceRef, "initialConfig": map[string]any{"readme": "default"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", base+"/repositories?instance="+instanceRef, nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", repo+":fetchTree", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", repo+"?allowMissing=true", nil), 400, "INVALID_ARGUMENT")
	operationsListed(t, h, base, 3)
	expect(t, h.call("owner", "DELETE", repo, nil), 200, "")
	expect(t, h.call("owner", "DELETE", instanceName, nil), 200, "")
}

func TestAppHostingBackends(t *testing.T) {
	h := newHarness(t)
	base := "/firebaseapphosting/v1/" + project + "/locations/us-central1"
	backend, name := lifecycle(t, h, base, "backends", "?backendId=web", map[string]any{"serviceAccount": "apphosting@tenkacloud-local.iam.gserviceaccount.com", "servingLocality": "GLOBAL_ACCESS", "uri": "https://example.test"}, "displayName", "Web")
	if backend["uri"] != nil || backend["etag"] == nil {
		t.Fatalf("backend outputs %v", backend)
	}
	expect(t, h.call("owner", "POST", base+"/backends?backendId=x", map[string]any{"servingLocality": "GLOBAL_ACCESS"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", base+"/backends?showDeleted=true", nil), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "GET", base+"/backends?showDeleted=false", nil), 200, "")
	expect(t, h.call("owner", "POST", name+"/builds?buildId=b", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", name+"?etag=stale", nil), 409, "ABORTED")
	operationsListed(t, h, base, 2)
	expect(t, h.call("owner", "DELETE", name, nil), 200, "")
}

func TestBlockchainNodesClustersAndEvaluations(t *testing.T) {
	h := newHarness(t)
	nodes := "/blockchainnodeengine/v1/" + project + "/locations/us-central1"
	node, nodeName := lifecycle(t, h, nodes, "blockchainNodes", "?blockchainNodeId=eth", map[string]any{"blockchainType": "ETHEREUM", "ethereumDetails": map[string]any{"network": "TESTNET_SEPOLIA", "nodeType": "FULL"}}, "labels", map[string]any{"a": "b"})
	if node["state"] != "RUNNING" || node["connectionInfo"] != nil {
		t.Fatalf("node outputs %v", node)
	}
	expect(t, h.call("owner", "PATCH", nodeName+"?updateMask=ethereumDetails", map[string]any{"ethereumDetails": map[string]any{}}), 400, "INVALID_ARGUMENT")
	operationsListed(t, h, nodes, 2)
	expect(t, h.call("owner", "DELETE", nodeName, nil), 200, "")

	clusters := "/hypercomputecluster/v1/" + project + "/locations/us-central1"
	_, cluster := lifecycle(t, h, clusters, "clusters", "?clusterId=hpc001", map[string]any{"networkResources": map[string]any{"net": map[string]any{}}}, "description", "HPC")
	expect(t, h.call("owner", "POST", clusters+"/clusters?clusterId=too-long-id", map[string]any{}), 400, "INVALID_ARGUMENT")
	operationsListed(t, h, clusters, 2)
	expect(t, h.call("owner", "DELETE", cluster, nil), 200, "")

	evaluations := "/workloadmanager/v1/" + project + "/locations/us-central1"
	evaluation, evaluationName := lifecycle(t, h, evaluations, "evaluations", "?evaluationId=sap", map[string]any{"evaluationType": "SAP", "schedule": "0 */6 * * *"}, "description", "SAP")
	if evaluation["resourceStatus"].(map[string]any)["state"] != "ACTIVE" {
		t.Fatalf("evaluation outputs %v", evaluation)
	}
	expect(t, h.call("owner", "POST", evaluationName+"/executions:run", map[string]any{}), 501, "UNIMPLEMENTED")
	operationsListed(t, h, evaluations, 2)
	expect(t, h.call("owner", "DELETE", evaluationName, nil), 200, "")
}

// Equal relative names in AlloyDB and Cluster Director stay separate.
func TestClusterNamesDoNotCollideAcrossAPIs(t *testing.T) {
	h := newHarness(t)
	expect(t, h.call("owner", "POST", loc+"/clusters?clusterId=main", map[string]any{"network": "n"}), 200, "")
	expect(t, h.call("owner", "POST", loc+"/clusters/main/instances?instanceId=i", map[string]any{"instanceType": "PRIMARY"}), 200, "")
	director := "/hypercomputecluster/v1/" + project + "/locations/us-central1/clusters"
	expect(t, h.call("owner", "POST", director+"?clusterId=main", map[string]any{}), 200, "")
	expect(t, h.call("owner", "DELETE", director+"/main", nil), 200, "")
	expect(t, h.call("owner", "GET", loc+"/clusters/main/instances/i", nil), 200, "")
	ar := "/artifactregistry/v1/" + project + "/locations/us-central1/repositories"
	ssm := "/securesourcemanager/v1/" + project + "/locations/us-central1/repositories"
	expect(t, h.call("owner", "POST", ar+"?repositoryId=shared", map[string]any{"format": "DOCKER"}), 200, "")
	expect(t, h.call("owner", "POST", ssm+"?repositoryId=shared", map[string]any{}), 200, "")
	if h.call("owner", "GET", ssm+"/shared", nil).body["format"] != nil {
		t.Fatal("Secure Source Manager must not read the Artifact Registry repository")
	}
}

func TestVMwareEngineNetworksAndPolicies(t *testing.T) {
	h := newHarness(t)
	base := "/vmwareengine/v1/" + project + "/locations/us-central1"
	network, networkName := lifecycle(t, h, base, "vmwareEngineNetworks", "?vmwareEngineNetworkId=prod-net", map[string]any{"type": "STANDARD"}, "description", "Prod")
	if network["state"] != "ACTIVE" || network["name"] != project+"/locations/us-central1/vmwareEngineNetworks/prod-net" {
		t.Fatalf("network outputs %v", network)
	}
	expect(t, h.call("owner", "POST", base+"/vmwareEngineNetworks?vmwareEngineNetworkId=us-central1-default", map[string]any{"type": "LEGACY"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/vmwareEngineNetworks?vmwareEngineNetworkId=1net", map[string]any{"type": "STANDARD"}), 400, "INVALID_ARGUMENT")
	ref := strings.TrimPrefix(networkName, "/vmwareengine/v1/")
	_, policy := lifecycle(t, h, base, "networkPolicies", "?networkPolicyId=edge", map[string]any{"vmwareEngineNetwork": ref, "edgeServicesCidr": "192.168.0.0/26",
		"internetAccess": map[string]any{"enabled": true}, "externalIp": map[string]any{"enabled": true}}, "description", "Edge")
	for label, body := range map[string]map[string]any{
		"prefix":       {"edgeServicesCidr": "192.168.0.0/24"},
		"public":       {"edgeServicesCidr": "8.8.8.0/26"},
		"host bits":    {"edgeServicesCidr": "10.0.0.1/26"},
		"not cidr":     {"edgeServicesCidr": "edge"},
		"ipv6":         {"edgeServicesCidr": "fd00::/26"},
		"external ips": {"edgeServicesCidr": "10.0.0.0/26", "externalIp": map[string]any{"enabled": true}},
	} {
		t.Run(label, func(t *testing.T) {
			expect(t, h.call("owner", "POST", base+"/networkPolicies?networkPolicyId=bad", body), 400, "INVALID_ARGUMENT")
		})
	}
	expect(t, h.call("owner", "PATCH", policy+"?updateMask=edgeServicesCidr", map[string]any{"edgeServicesCidr": "10.1.0.0/24"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", policy+"?updateMask=edgeServicesCidr", map[string]any{"edgeServicesCidr": "10.1.0.0/26"}), 200, "")
	expect(t, h.call("owner", "POST", base+"/privateClouds?privateCloudId=pc", map[string]any{}), 501, "UNIMPLEMENTED")
	operationsListed(t, h, base, 5)
	expect(t, h.call("owner", "DELETE", policy, nil), 200, "")
	expect(t, h.call("owner", "DELETE", networkName, nil), 200, "")
}

func TestOSConfigPatchDeployments(t *testing.T) {
	h := newHarness(t)
	base := "/osconfig/v1/" + project
	deployment, name := lifecycle(t, h, base, "patchDeployments", "?patchDeploymentId=weekly", map[string]any{"instanceFilter": map[string]any{"all": true},
		"recurringSchedule": map[string]any{"timeZone": map[string]any{"id": "Asia/Tokyo"}, "frequency": "WEEKLY", "timeOfDay": map[string]any{"hours": 3},
			"weekly": map[string]any{"dayOfWeek": "SUNDAY"}}}, "description", "Weekly")
	if deployment["state"] != "ACTIVE" || deployment["done"] != nil {
		t.Fatalf("patch deployment outputs %v", deployment)
	}
	expect(t, h.call("owner", "POST", base+"/patchDeployments?patchDeploymentId=none", map[string]any{"instanceFilter": map[string]any{"all": true}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/patchDeployments?patchDeploymentId=both", map[string]any{"instanceFilter": map[string]any{"all": true},
		"oneTimeSchedule": map[string]any{"executeTime": "2026-12-01T00:00:00Z"}, "recurringSchedule": map[string]any{}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/patchDeployments?patchDeploymentId=once", map[string]any{"instanceFilter": map[string]any{"all": true},
		"oneTimeSchedule": map[string]any{"executeTime": "2026-12-01T00:00:00Z"}}), 200, "")
	expect(t, h.call("owner", "POST", base+"/patchDeployments?patchDeploymentId=nofilter", map[string]any{"oneTimeSchedule": map[string]any{"executeTime": "2026-12-01T00:00:00Z"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", name+"?updateMask=recurringSchedule", map[string]any{"recurringSchedule": map[string]any{}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", name+":pause", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", base+"/patchJobs:execute", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "GET", "/osconfig/v1/"+project+"/locations/us-central1-a/osPolicyAssignments", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", name, nil), 200, "")
	expect(t, h.call("owner", "DELETE", base+"/patchDeployments/once", nil), 200, "")
}

// Secret Manager references nested in lists are checked too.
func TestSecretReferencesInLists(t *testing.T) {
	bad := []any{map[string]any{"tokenSecretVersion": "plain"}}
	if devtoolsSecretReferences(map[string]any{"items": bad}, "") == nil {
		t.Fatal("a secret value in a list must be refused")
	}
	good := []any{map[string]any{"tokenSecretVersion": "projects/p/locations/us-central1/secrets/s/versions/2"}}
	if err := devtoolsSecretReferences(map[string]any{"items": good}, ""); err != nil {
		t.Fatal(err)
	}
}
