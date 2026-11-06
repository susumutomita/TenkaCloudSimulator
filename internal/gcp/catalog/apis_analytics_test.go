package catalog

import (
	"strings"
	"testing"
)

const analyticsScope = "projects/tenkacloud-local/locations/us-central1"

// analyticsOK calls a method as the owner, requires success and unwraps a
// completed long-running operation to its response.
func analyticsOK(t *testing.T, h *harness, method, path string, body any) map[string]any {
	t.Helper()
	r := h.call("owner", method, path, body)
	expect(t, r, 200, "")
	if r.body["done"] == true {
		if response, ok := r.body["response"].(map[string]any); ok {
			return response
		}
	}
	return r.body
}

func analyticsCount(t *testing.T, h *harness, path, field string, want int) {
	t.Helper()
	got, _ := analyticsOK(t, h, "GET", path, nil)[field].([]any)
	if len(got) != want {
		t.Fatalf("%s %s: want %d, got %v", path, field, want, got)
	}
}

// analyticsOperations checks that a create returned a stored operation.
func analyticsOperations(t *testing.T, h *harness, api, typeURL string, created reply, want int) {
	t.Helper()
	expect(t, created, 200, "")
	name, _ := created.body["name"].(string)
	if created.body["done"] != true || !strings.HasPrefix(name, analyticsScope+"/operations/") {
		t.Fatalf("operation %v", created.body)
	}
	if got := created.body["response"].(map[string]any)["@type"]; got != typeURL {
		t.Fatalf("@type %v, want %s", got, typeURL)
	}
	expect(t, h.call("owner", "GET", "/"+api+"/v1/"+name, nil), 200, "")
	analyticsCount(t, h, "/"+api+"/v1/"+analyticsScope+"/operations", "operations", want)
}

func TestManagedKafkaClustersTopicsAndConnectClusters(t *testing.T) {
	h := newHarness(t)
	base := "/managedkafka/v1/" + analyticsScope
	network := map[string]any{"accessConfig": map[string]any{"networkConfigs": []any{map[string]any{"subnet": "projects/tenkacloud-local/regions/us-central1/subnetworks/default"}}}}
	cluster := func(vcpus, memory string, gcp any) map[string]any {
		return map[string]any{"capacityConfig": map[string]any{"vcpuCount": vcpus, "memoryBytes": memory}, "gcpConfig": gcp, "labels": map[string]any{"team": "a"}}
	}
	for _, bad := range []map[string]any{
		cluster("2", "3221225472", network),
		cluster("3", "32212254720", network),
		cluster("3", "3221225472", map[string]any{"accessConfig": map[string]any{"networkConfigs": []any{}}}),
		{"capacityConfig": map[string]any{}, "gcpConfig": network},
		{"gcpConfig": network},
	} {
		expect(t, h.call("owner", "POST", base+"/clusters?clusterId=bad", bad), 400, "INVALID_ARGUMENT")
	}
	created := h.call("owner", "POST", base+"/clusters?clusterId=events", cluster("3", "3221225472", network))
	analyticsOperations(t, h, "managedkafka", "type.googleapis.com/google.cloud.managedkafka.v1.Cluster", created, 1)
	name := analyticsScope + "/clusters/events"
	if got := analyticsOK(t, h, "GET", base+"/clusters/events?view=CLUSTER_VIEW_FULL", nil); got["state"] != "ACTIVE" || got["name"] != name {
		t.Fatalf("cluster %v", got)
	}
	analyticsCount(t, h, base+"/clusters", "clusters", 1)
	analyticsOK(t, h, "PATCH", base+"/clusters/events?updateMask=capacityConfig", map[string]any{"capacityConfig": map[string]any{"vcpuCount": "4", "memoryBytes": "8589934592"}})
	expect(t, h.call("owner", "PATCH", base+"/clusters/events?updateMask=capacityConfig", map[string]any{"capacityConfig": map[string]any{"vcpuCount": "1", "memoryBytes": "3221225472"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", base+"/clusters/events?updateMask=gcpConfig", map[string]any{"gcpConfig": network}), 400, "INVALID_ARGUMENT")

	topics := base + "/clusters/events/topics"
	expect(t, h.call("owner", "POST", topics+"?topicId=bad*", map[string]any{"partitionCount": 3, "replicationFactor": 3}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", topics+"?topicId=t0", map[string]any{"partitionCount": 0, "replicationFactor": 3}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", topics+"?topicId=t1", map[string]any{"partitionCount": 3}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/clusters/missing/topics?topicId=t1", map[string]any{"partitionCount": 3, "replicationFactor": 3}), 404, "NOT_FOUND")
	topic := analyticsOK(t, h, "POST", topics+"?topicId=orders.v1_raw", map[string]any{"partitionCount": 6, "replicationFactor": 3, "configs": map[string]any{"cleanup.policy": "compact"}})
	if topic["name"] != name+"/topics/orders.v1_raw" || topic["done"] != nil {
		t.Fatalf("topic %v", topic)
	}
	analyticsOK(t, h, "GET", topics+"/orders.v1_raw", nil)
	analyticsCount(t, h, topics, "topics", 1)
	analyticsOK(t, h, "PATCH", topics+"/orders.v1_raw?updateMask=configs", map[string]any{"configs": map[string]any{"retention.ms": "3600000"}})
	expect(t, h.call("owner", "PATCH", topics+"/orders.v1_raw?updateMask=partitionCount", map[string]any{"partitionCount": 12}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "DELETE", base+"/clusters/events", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "POST", base+"/clusters/events/acls?aclId=cluster", map[string]any{"aclEntries": []any{}}), 501, "UNIMPLEMENTED")

	connect := map[string]any{"kafkaCluster": name, "capacityConfig": map[string]any{"vcpuCount": "3", "memoryBytes": "3221225472"},
		"gcpConfig": map[string]any{"accessConfig": map[string]any{"networkConfigs": []any{map[string]any{"primarySubnet": "projects/tenkacloud-local/regions/us-central1/subnetworks/default"}}}}}
	withConfig := map[string]any{"config": map[string]any{"a": "b"}}
	for k, v := range connect {
		withConfig[k] = v
	}
	expect(t, h.call("owner", "POST", base+"/connectClusters?connectClusterId=bad", withConfig), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/connectClusters?connectClusterId=bad", map[string]any{"kafkaCluster": name, "capacityConfig": connect["capacityConfig"], "gcpConfig": map[string]any{"accessConfig": map[string]any{"networkConfigs": []any{map[string]any{}}}}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", base+"/connectClusters?connectClusterId=sink", connect)
	analyticsOK(t, h, "GET", base+"/connectClusters/sink", nil)
	analyticsCount(t, h, base+"/connectClusters", "connectClusters", 1)
	analyticsOK(t, h, "PATCH", base+"/connectClusters/sink?updateMask=labels", map[string]any{"labels": map[string]any{"a": "b"}})
	expect(t, h.call("owner", "PATCH", base+"/connectClusters/sink?updateMask=config", map[string]any{"config": map[string]any{"a": "b"}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "DELETE", base+"/connectClusters/sink", nil)
	analyticsOK(t, h, "DELETE", topics+"/orders.v1_raw", nil)
	analyticsOK(t, h, "DELETE", base+"/clusters/events", nil)
	analyticsCount(t, h, base+"/clusters", "clusters", 0)
}

func TestDatastreamProfilesPrivateConnectionsAndStreams(t *testing.T) {
	h := newHarness(t)
	base := "/datastream/v1/" + analyticsScope
	mysql := map[string]any{"hostname": "10.0.0.5", "username": "replicator", "secretManagerStoredPassword": "projects/tenkacloud-local/secrets/db/versions/1"}
	for _, bad := range []map[string]any{
		{"displayName": "x", "mysqlProfile": map[string]any{"hostname": "h", "username": "u", "password": "p"}},
		{"displayName": "x", "mysqlProfile": mysql, "forwardSshConnectivity": map[string]any{"hostname": "bastion", "username": "u", "privateKey": "-----BEGIN"}},
		{"displayName": "x", "serviceNowProfile": map[string]any{"instanceUrl": "https://example.invalid", "userPasswordCredentials": map[string]any{"username": "u", "password": map[string]any{"rawValue": "p"}}}},
		{"displayName": "x", "salesforceProfile": map[string]any{"domain": "d", "oauth2ClientCredentials": map[string]any{"clientId": "c", "clientSecret": "s"}}},
		{"displayName": "x", "mongodbProfile": map[string]any{"hostAddresses": []any{map[string]any{"hostname": "h"}}, "sslConfig": map[string]any{"clientKey": "k"}}},
		{"mysqlProfile": mysql},
	} {
		expect(t, h.call("owner", "POST", base+"/connectionProfiles?connectionProfileId=bad", bad), 400, "INVALID_ARGUMENT")
	}
	created := h.call("owner", "POST", base+"/connectionProfiles?connectionProfileId=source", map[string]any{"displayName": "Source", "mysqlProfile": mysql})
	analyticsOperations(t, h, "datastream", "type.googleapis.com/google.cloud.datastream.v1.ConnectionProfile", created, 1)
	analyticsOK(t, h, "POST", base+"/connectionProfiles?connectionProfileId=dest&force=true", map[string]any{"displayName": "Destination", "bigqueryProfile": map[string]any{}})
	analyticsOK(t, h, "GET", base+"/connectionProfiles/source", nil)
	analyticsCount(t, h, base+"/connectionProfiles", "connectionProfiles", 2)
	analyticsOK(t, h, "PATCH", base+"/connectionProfiles/source?updateMask=displayName", map[string]any{"displayName": "Renamed"})
	expect(t, h.call("owner", "PATCH", base+"/connectionProfiles/source?updateMask=mysqlProfile", map[string]any{"mysqlProfile": map[string]any{"hostname": "h", "username": "u", "password": "p"}}), 400, "INVALID_ARGUMENT")

	for _, bad := range []map[string]any{
		{"displayName": "x", "vpcPeeringConfig": map[string]any{"vpc": "projects/tenkacloud-local/global/networks/default"}},
		{"displayName": "x", "pscInterfaceConfig": map[string]any{}},
	} {
		expect(t, h.call("owner", "POST", base+"/privateConnections?privateConnectionId=bad", bad), 400, "INVALID_ARGUMENT")
	}
	pc := analyticsOK(t, h, "POST", base+"/privateConnections?privateConnectionId=peer", map[string]any{"displayName": "Peer", "vpcPeeringConfig": map[string]any{"vpc": "projects/tenkacloud-local/global/networks/default", "subnet": "10.10.0.0/29"}})
	if pc["state"] != "CREATED" {
		t.Fatalf("private connection %v", pc)
	}
	analyticsOK(t, h, "GET", base+"/privateConnections/peer", nil)
	analyticsCount(t, h, base+"/privateConnections", "privateConnections", 1)
	routes := base + "/privateConnections/peer/routes"
	expect(t, h.call("owner", "POST", routes+"?routeId=db", map[string]any{"displayName": "DB"}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", routes+"?routeId=db", map[string]any{"displayName": "DB", "destinationAddress": "10.0.0.5", "destinationPort": 3306})
	analyticsOK(t, h, "GET", routes+"/db", nil)
	analyticsCount(t, h, routes, "routes", 1)
	expect(t, h.call("owner", "DELETE", base+"/privateConnections/peer", nil), 400, "FAILED_PRECONDITION")
	analyticsOK(t, h, "DELETE", routes+"/db", nil)
	analyticsOK(t, h, "POST", routes+"?routeId=db2", map[string]any{"displayName": "DB", "destinationAddress": "10.0.0.6"})
	analyticsOK(t, h, "DELETE", base+"/privateConnections/peer?force=true", nil)
	expect(t, h.call("owner", "GET", routes+"/db2", nil), 404, "NOT_FOUND")

	stream := map[string]any{"displayName": "CDC", "sourceConfig": map[string]any{"sourceConnectionProfile": analyticsScope + "/connectionProfiles/source", "mysqlSourceConfig": map[string]any{}},
		"destinationConfig": map[string]any{"destinationConnectionProfile": analyticsScope + "/connectionProfiles/dest", "bigqueryDestinationConfig": map[string]any{}}, "backfillNone": map[string]any{}}
	running := map[string]any{"state": "RUNNING"}
	for k, v := range stream {
		running[k] = v
	}
	expect(t, h.call("owner", "POST", base+"/streams?streamId=bad", running), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/streams?streamId=bad", map[string]any{"displayName": "x", "sourceConfig": map[string]any{}, "destinationConfig": stream["destinationConfig"]}), 400, "INVALID_ARGUMENT")
	if got := analyticsOK(t, h, "POST", base+"/streams?streamId=cdc", stream); got["state"] != "NOT_STARTED" {
		t.Fatalf("stream %v", got)
	}
	analyticsOK(t, h, "GET", base+"/streams/cdc", nil)
	analyticsCount(t, h, base+"/streams", "streams", 1)
	analyticsOK(t, h, "PATCH", base+"/streams/cdc?updateMask=displayName", map[string]any{"displayName": "CDC 2"})
	expect(t, h.call("owner", "PATCH", base+"/streams/cdc?updateMask=state", map[string]any{"state": "RUNNING"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PATCH", base+"/streams/cdc?updateMask=state", map[string]any{"state": "NOT_STARTED"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/streams/cdc:run", map[string]any{}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", base+"/streams/cdc", nil)
	analyticsOK(t, h, "DELETE", base+"/connectionProfiles/source", nil)
}

func TestDataprocMetastoreServicesBackupsAndFederations(t *testing.T) {
	h := newHarness(t)
	base := "/metastore/v1/" + analyticsScope
	expect(t, h.call("owner", "POST", base+"/services?serviceId=1bad", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/services?serviceId=Tagged", map[string]any{"tags": map[string]any{"123/env": "dev"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/services?serviceId=Guarded", map[string]any{"deletionProtection": true}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base+"/services?serviceId=Hive-1", map[string]any{"tier": "DEVELOPER", "deletionProtection": false,
		"hiveMetastoreConfig": map[string]any{"version": "3.1.2", "kerberosConfig": map[string]any{"keytab": map[string]any{"cloudSecret": "projects/1/secrets/keytab/versions/1"}}}})
	analyticsOperations(t, h, "metastore", "type.googleapis.com/google.cloud.metastore.v1.Service", created, 1)
	if got := analyticsOK(t, h, "GET", base+"/services/Hive-1", nil); got["state"] != "ACTIVE" || got["uid"] == nil || got["endpointUri"] != nil {
		t.Fatalf("service %v", got)
	}
	analyticsCount(t, h, base+"/services", "services", 1)
	analyticsOK(t, h, "PATCH", base+"/services/Hive-1?updateMask=labels", map[string]any{"labels": map[string]any{"a": "b"}})
	expect(t, h.call("owner", "PATCH", base+"/services/Hive-1?updateMask=deletionProtection", map[string]any{"deletionProtection": true}), 400, "INVALID_ARGUMENT")
	backups := base + "/services/Hive-1/backups"
	analyticsOK(t, h, "POST", backups+"?backupId=nightly", map[string]any{"description": "before upgrade"})
	if got := analyticsOK(t, h, "GET", backups+"/nightly", nil); got["state"] != "ACTIVE" {
		t.Fatalf("backup %v", got)
	}
	analyticsCount(t, h, backups, "backups", 1)
	expect(t, h.call("owner", "DELETE", base+"/services/Hive-1", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "POST", base+"/services/Hive-1:restore", map[string]any{"backup": backups + "/nightly"}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", backups+"/nightly", nil)
	analyticsOK(t, h, "DELETE", base+"/services/Hive-1", nil)

	expect(t, h.call("owner", "POST", base+"/federations?federationId=fed", map[string]any{"tags": map[string]any{"k": "v"}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", base+"/federations?federationId=fed", map[string]any{"version": "3.1.2", "backendMetastores": map[string]any{"1": map[string]any{"name": "projects/tenkacloud-local", "metastoreType": "BIGQUERY"}}})
	analyticsOK(t, h, "GET", base+"/federations/fed", nil)
	analyticsCount(t, h, base+"/federations", "federations", 1)
	analyticsOK(t, h, "PATCH", base+"/federations/fed?updateMask=labels", map[string]any{"labels": map[string]any{"a": "b"}})
	analyticsOK(t, h, "DELETE", base+"/federations/fed", nil)
}

func TestDataformRepositoriesWorkspacesAndConfigs(t *testing.T) {
	h := newHarness(t)
	base := "/dataform/v1/" + analyticsScope
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=r", map[string]any{"setAuthenticatedUserAdmin": true}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/repositories?repositoryId=r", map[string]any{"containingFolder": analyticsScope + "/folders/f"}), 400, "INVALID_ARGUMENT")
	repo := analyticsOK(t, h, "POST", base+"/repositories?repositoryId=analytics", map[string]any{"displayName": "Analytics",
		"gitRemoteSettings": map[string]any{"url": "https://example.invalid/analytics.git", "defaultBranch": "main", "authenticationTokenSecretVersion": "projects/tenkacloud-local/secrets/git/versions/1"}})
	if repo["name"] != analyticsScope+"/repositories/analytics" || repo["createTime"] == nil {
		t.Fatalf("repository %v", repo)
	}
	analyticsOK(t, h, "GET", base+"/repositories/analytics", nil)
	analyticsCount(t, h, base+"/repositories", "repositories", 1)
	analyticsOK(t, h, "PATCH", base+"/repositories/analytics?updateMask=displayName", map[string]any{"displayName": "Renamed"})
	r := base + "/repositories/analytics"
	analyticsOK(t, h, "POST", r+"/workspaces?workspaceId=dev", map[string]any{})
	analyticsOK(t, h, "GET", r+"/workspaces/dev", nil)
	analyticsCount(t, h, r+"/workspaces", "workspaces", 1)
	expect(t, h.call("owner", "POST", r+"/releaseConfigs?releaseConfigId=prod", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", r+"/releaseConfigs?releaseConfigId=prod", map[string]any{"gitCommitish": "main", "releaseCompilationResult": r + "/compilationResults/x"}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", r+"/releaseConfigs?releaseConfigId=prod", map[string]any{"gitCommitish": "main", "codeCompilationConfig": map[string]any{"defaultDatabase": "tenkacloud-local"}})
	analyticsOK(t, h, "GET", r+"/releaseConfigs/prod", nil)
	analyticsCount(t, h, r+"/releaseConfigs", "releaseConfigs", 1)
	analyticsOK(t, h, "PATCH", r+"/releaseConfigs/prod?updateMask=cronSchedule", map[string]any{"cronSchedule": "0 2 * * *"})
	analyticsOK(t, h, "POST", r+"/workflowConfigs?workflowConfigId=daily", map[string]any{"releaseConfig": r + "/releaseConfigs/prod", "invocationConfig": map[string]any{"includedTags": []any{"daily"}}})
	analyticsOK(t, h, "GET", r+"/workflowConfigs/daily", nil)
	analyticsCount(t, h, r+"/workflowConfigs", "workflowConfigs", 1)
	analyticsOK(t, h, "PATCH", r+"/workflowConfigs/daily?updateMask=disabled", map[string]any{"disabled": true})
	expect(t, h.call("owner", "DELETE", r+"?force=true", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "POST", r+"/compilationResults", map[string]any{"gitCommitish": "main"}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", r+"/workflowConfigs/daily", nil)
	analyticsOK(t, h, "DELETE", r+"/releaseConfigs/prod", nil)
	analyticsOK(t, h, "DELETE", r+"/workspaces/dev", nil)
	analyticsOK(t, h, "DELETE", r, nil)
}

func TestDataFusionInstancesAndDNSPeerings(t *testing.T) {
	h := newHarness(t)
	base := "/datafusion/v1/" + analyticsScope
	expect(t, h.call("owner", "POST", base+"/instances?instanceId=pipelines", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/instances?instanceId="+strings.Repeat("a", 31), map[string]any{"type": "BASIC"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/instances?instanceId=pipelines", map[string]any{"type": "BASIC", "tags": map[string]any{"k": "v"}}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base+"/instances?instanceId=pipelines", map[string]any{"type": "BASIC", "version": "6.10.1", "labels": map[string]any{"a": "b"}})
	analyticsOperations(t, h, "datafusion", "type.googleapis.com/google.cloud.datafusion.v1.Instance", created, 1)
	if got := analyticsOK(t, h, "GET", base+"/instances/pipelines", nil); got["state"] != "ACTIVE" || got["serviceEndpoint"] != nil {
		t.Fatalf("instance %v", got)
	}
	analyticsCount(t, h, base+"/instances", "instances", 1)
	analyticsOK(t, h, "PATCH", base+"/instances/pipelines?updateMask=enableStackdriverLogging", map[string]any{"enableStackdriverLogging": true})
	peerings := base + "/instances/pipelines/dnsPeerings"
	expect(t, h.call("owner", "POST", peerings+"?dnsPeeringId=corp", map[string]any{"targetProject": "tenkacloud-local"}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", peerings+"?dnsPeeringId=corp", map[string]any{"domain": "corp.example.", "targetProject": "tenkacloud-local", "targetNetwork": "default"})
	analyticsCount(t, h, peerings, "dnsPeerings", 1)
	analyticsOK(t, h, "DELETE", peerings+"/corp", nil)
	analyticsOK(t, h, "POST", peerings+"?dnsPeeringId=corp2", map[string]any{"domain": "corp2.example."})
	expect(t, h.call("owner", "DELETE", base+"/instances/pipelines", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "POST", base+"/instances/pipelines:restart", map[string]any{}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", base+"/instances/pipelines?force=true", nil)
	analyticsCount(t, h, base+"/instances", "instances", 0)
}

func TestLookerInstances(t *testing.T) {
	h := newHarness(t)
	base := "/looker/v1/" + analyticsScope
	expect(t, h.call("owner", "POST", base+"/instances?instanceId=bi", map[string]any{"oauthConfig": map[string]any{"clientId": "c", "clientSecret": "s"}}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base+"/instances?instanceId=bi", map[string]any{"platformEdition": "LOOKER_CORE_STANDARD", "oauthConfig": map[string]any{"clientId": "c", "sharedOauthClientEnabled": false}})
	analyticsOperations(t, h, "looker", "type.googleapis.com/google.cloud.looker.v1.Instance", created, 1)
	if got := analyticsOK(t, h, "GET", base+"/instances/bi", nil); got["state"] != "ACTIVE" || got["lookerUri"] != nil {
		t.Fatalf("instance %v", got)
	}
	analyticsCount(t, h, base+"/instances?showDeleted=true", "instances", 1)
	analyticsOK(t, h, "PATCH", base+"/instances/bi?updateMask=adminSettings", map[string]any{"adminSettings": map[string]any{"allowedEmailDomains": []any{"example.com"}}})
	expect(t, h.call("owner", "PATCH", base+"/instances/bi?updateMask=oauthConfig", map[string]any{"oauthConfig": map[string]any{"clientSecret": "s"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/instances/bi:undelete", map[string]any{}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", base+"/instances/bi", nil)
}

func TestComposerEnvironments(t *testing.T) {
	h := newHarness(t)
	base := "/composer/v1/" + analyticsScope
	expect(t, h.call("owner", "POST", base+"/environments", map[string]any{}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/environments", map[string]any{"name": "projects/tenkacloud-local/locations/asia-northeast1/environments/airflow"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/environments", map[string]any{"name": analyticsScope + "/environments/airflow", "state": "ERROR"}), 400, "INVALID_ARGUMENT")
	created := h.call("owner", "POST", base+"/environments", map[string]any{"name": analyticsScope + "/environments/airflow",
		"config": map[string]any{"softwareConfig": map[string]any{"imageVersion": "composer-3-airflow-2.10.5"}, "environmentSize": "ENVIRONMENT_SIZE_SMALL"}})
	analyticsOperations(t, h, "composer", "type.googleapis.com/google.cloud.orchestration.airflow.service.v1.Environment", created, 1)
	got := analyticsOK(t, h, "GET", base+"/environments/airflow", nil)
	if got["state"] != "RUNNING" || got["uuid"] == nil || got["config"].(map[string]any)["airflowUri"] != nil {
		t.Fatalf("environment %v", got)
	}
	expect(t, h.call("owner", "POST", base+"/environments", map[string]any{"name": analyticsScope + "/environments/airflow"}), 409, "ALREADY_EXISTS")
	analyticsCount(t, h, base+"/environments", "environments", 1)
	analyticsOK(t, h, "PATCH", base+"/environments/airflow?updateMask=labels", map[string]any{"labels": map[string]any{"a": "b"}})
	expect(t, h.call("owner", "PATCH", base+"/environments/airflow?updateMask=config.nodeCount", map[string]any{"config": map[string]any{"nodeCount": 4}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/environments/airflow:executeAirflowCommand", map[string]any{}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", base+"/environments/airflow", nil)
}

func TestDataplexLakesZonesAssetsAndCatalogTypes(t *testing.T) {
	h := newHarness(t)
	base := "/dataplex/v1/" + analyticsScope
	created := h.call("owner", "POST", base+"/lakes?lakeId=sales", map[string]any{"displayName": "Sales"})
	analyticsOperations(t, h, "dataplex", "type.googleapis.com/google.cloud.dataplex.v1.Lake", created, 1)
	analyticsOK(t, h, "GET", base+"/lakes/sales", nil)
	analyticsCount(t, h, base+"/lakes", "lakes", 1)
	analyticsOK(t, h, "PATCH", base+"/lakes/sales?updateMask=description", map[string]any{"description": "Sales data"})
	zones := base + "/lakes/sales/zones"
	expect(t, h.call("owner", "POST", zones+"?zoneId=raw", map[string]any{"type": "RAW", "resourceSpec": map[string]any{}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", zones+"?zoneId=raw", map[string]any{"type": "RAW", "resourceSpec": map[string]any{"locationType": "SINGLE_REGION"}, "discoverySpec": map[string]any{}}), 400, "INVALID_ARGUMENT")
	zone := analyticsOK(t, h, "POST", zones+"?zoneId=raw", map[string]any{"type": "RAW", "resourceSpec": map[string]any{"locationType": "SINGLE_REGION"}, "discoverySpec": map[string]any{"enabled": false}})
	if zone["@type"] != "type.googleapis.com/google.cloud.dataplex.v1.Zone" || zone["state"] != "ACTIVE" {
		t.Fatalf("zone %v", zone)
	}
	analyticsOK(t, h, "GET", zones+"/raw", nil)
	analyticsCount(t, h, zones, "zones", 1)
	analyticsOK(t, h, "PATCH", zones+"/raw?updateMask=discoverySpec", map[string]any{"discoverySpec": map[string]any{"enabled": true, "schedule": "0 * * * *"}})
	expect(t, h.call("owner", "PATCH", zones+"/raw?updateMask=discoverySpec", map[string]any{"discoverySpec": map[string]any{}}), 400, "INVALID_ARGUMENT")
	assets := zones + "/raw/assets"
	expect(t, h.call("owner", "POST", assets+"?assetId=bucket", map[string]any{"resourceSpec": map[string]any{"name": "projects/tenkacloud-local/buckets/raw"}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", assets+"?assetId=bucket", map[string]any{"resourceSpec": map[string]any{"type": "STORAGE_BUCKET", "name": "projects/tenkacloud-local/buckets/raw"}})
	analyticsOK(t, h, "GET", assets+"/bucket", nil)
	analyticsCount(t, h, assets, "assets", 1)
	analyticsOK(t, h, "PATCH", assets+"/bucket?updateMask=labels", map[string]any{"labels": map[string]any{"a": "b"}})
	expect(t, h.call("owner", "DELETE", base+"/lakes/sales", nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", zones+"/raw", nil), 400, "FAILED_PRECONDITION")
	analyticsOK(t, h, "DELETE", assets+"/bucket", nil)
	analyticsOK(t, h, "DELETE", zones+"/raw", nil)
	analyticsOK(t, h, "DELETE", base+"/lakes/sales", nil)

	group := analyticsOK(t, h, "POST", base+"/entryGroups?entryGroupId=crm", map[string]any{"displayName": "CRM"})
	analyticsOK(t, h, "GET", base+"/entryGroups/crm", nil)
	analyticsCount(t, h, base+"/entryGroups", "entryGroups", 1)
	expect(t, h.call("owner", "PATCH", base+"/entryGroups/crm?updateMask=description", map[string]any{"description": "x", "etag": "stale"}), 409, "ABORTED")
	analyticsOK(t, h, "PATCH", base+"/entryGroups/crm?updateMask=description", map[string]any{"description": "x", "etag": group["etag"]})
	expect(t, h.call("owner", "DELETE", base+"/entryGroups/crm?etag=stale", nil), 409, "ABORTED")
	analyticsOK(t, h, "DELETE", base+"/entryGroups/crm", nil)

	aspect := analyticsScope + "/aspectTypes/owner"
	expect(t, h.call("owner", "POST", base+"/entryTypes?entryTypeId=table", map[string]any{"requiredAspects": []any{map[string]any{}}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", base+"/entryTypes?entryTypeId=table", map[string]any{"system": "crm", "requiredAspects": []any{map[string]any{"type": aspect}}})
	analyticsOK(t, h, "GET", base+"/entryTypes/table", nil)
	analyticsCount(t, h, base+"/entryTypes", "entryTypes", 1)
	analyticsOK(t, h, "PATCH", base+"/entryTypes/table?updateMask=typeAliases", map[string]any{"typeAliases": []any{"TABLE"}})
	expect(t, h.call("owner", "PATCH", base+"/entryTypes/table?updateMask=requiredAspects", map[string]any{"requiredAspects": []any{map[string]any{}}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "DELETE", base+"/entryTypes/table", nil)

	template := map[string]any{"name": "owner", "type": "record", "recordFields": []any{map[string]any{"name": "email", "type": "string", "index": 1}}}
	expect(t, h.call("owner", "POST", base+"/aspectTypes?aspectTypeId=owner", map[string]any{"metadataTemplate": map[string]any{"name": "owner"}}), 400, "INVALID_ARGUMENT")
	analyticsOK(t, h, "POST", base+"/aspectTypes?aspectTypeId=owner", map[string]any{"metadataTemplate": template})
	analyticsOK(t, h, "GET", base+"/aspectTypes/owner", nil)
	analyticsCount(t, h, base+"/aspectTypes", "aspectTypes", 1)
	analyticsOK(t, h, "PATCH", base+"/aspectTypes/owner?updateMask=description", map[string]any{"description": "Data owner"})
	expect(t, h.call("owner", "PATCH", base+"/aspectTypes/owner?updateMask=metadataTemplate", map[string]any{"metadataTemplate": map[string]any{"type": "record"}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", base+"/dataScans?dataScanId=dq", map[string]any{}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", base+"/aspectTypes/owner", nil)
}

func TestBigLakeCatalogsDatabasesAndTables(t *testing.T) {
	h := newHarness(t)
	base := "/biglake/v1/" + analyticsScope
	expect(t, h.call("owner", "POST", base+"/catalogs?catalogId=bad.id", map[string]any{}), 400, "INVALID_ARGUMENT")
	catalog := analyticsOK(t, h, "POST", base+"/catalogs?catalogId=sales_catalog", map[string]any{})
	if catalog["name"] != analyticsScope+"/catalogs/sales_catalog" || catalog["done"] != nil {
		t.Fatalf("catalog %v", catalog)
	}
	analyticsOK(t, h, "GET", base+"/catalogs/sales_catalog", nil)
	analyticsCount(t, h, base+"/catalogs", "catalogs", 1)
	c := base + "/catalogs/sales_catalog"
	analyticsOK(t, h, "POST", c+"/databases?databaseId=orders_db", map[string]any{"type": "HIVE", "hiveOptions": map[string]any{"locationUri": "gs://raw/orders"}})
	analyticsOK(t, h, "GET", c+"/databases/orders_db", nil)
	analyticsCount(t, h, c+"/databases", "databases", 1)
	analyticsOK(t, h, "PATCH", c+"/databases/orders_db?updateMask=hiveOptions", map[string]any{"hiveOptions": map[string]any{"locationUri": "gs://raw/orders2"}})
	d := c + "/databases/orders_db"
	table := analyticsOK(t, h, "POST", d+"/tables?tableId=orders", map[string]any{"type": "HIVE", "hiveOptions": map[string]any{"tableType": "EXTERNAL_TABLE"}})
	analyticsOK(t, h, "GET", d+"/tables/orders", nil)
	analyticsCount(t, h, d+"/tables?view=FULL", "tables", 1)
	expect(t, h.call("owner", "PATCH", d+"/tables/orders?updateMask=hiveOptions", map[string]any{"hiveOptions": map[string]any{"tableType": "MANAGED_TABLE"}, "etag": "stale"}), 409, "ABORTED")
	analyticsOK(t, h, "PATCH", d+"/tables/orders?updateMask=hiveOptions", map[string]any{"hiveOptions": map[string]any{"tableType": "MANAGED_TABLE"}, "etag": table["etag"]})
	expect(t, h.call("owner", "DELETE", c, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "POST", d+"/tables/orders:rename", map[string]any{"newName": d + "/tables/orders2"}), 501, "UNIMPLEMENTED")
	if deleted := analyticsOK(t, h, "DELETE", d+"/tables/orders", nil); deleted["name"] != analyticsScope+"/catalogs/sales_catalog/databases/orders_db/tables/orders" {
		t.Fatalf("deleted table %v", deleted)
	}
	analyticsOK(t, h, "DELETE", d, nil)
	if deleted := analyticsOK(t, h, "DELETE", c, nil); deleted["name"] != analyticsScope+"/catalogs/sales_catalog" {
		t.Fatalf("deleted catalog %v", deleted)
	}
}

func TestDataprocAutoscalingPoliciesAndSessionTemplates(t *testing.T) {
	h := newHarness(t)
	base := "/dataproc/v1/" + analyticsScope
	yarn := map[string]any{"gracefulDecommissionTimeout": "3600s", "scaleUpFactor": 0.5, "scaleDownFactor": 0.5}
	for _, bad := range []map[string]any{
		{"id": "a", "workerConfig": map[string]any{"maxInstances": 10}},
		{"id": "scale-1", "workerConfig": map[string]any{"minInstances": 2}},
		{"id": "scale-1", "workerConfig": map[string]any{"maxInstances": 10}, "basicAlgorithm": map[string]any{"yarnConfig": map[string]any{"gracefulDecommissionTimeout": "3600s"}}},
		{"id": "scale-1", "workerConfig": map[string]any{"maxInstances": 10}, "secondaryWorkerConfig": map[string]any{}},
		{"workerConfig": map[string]any{"maxInstances": 10}},
	} {
		expect(t, h.call("owner", "POST", base+"/autoscalingPolicies", bad), 400, "INVALID_ARGUMENT")
	}
	policy := analyticsOK(t, h, "POST", base+"/autoscalingPolicies", map[string]any{"id": "scale-1", "workerConfig": map[string]any{"maxInstances": 10}, "basicAlgorithm": map[string]any{"yarnConfig": yarn}})
	if policy["name"] != analyticsScope+"/autoscalingPolicies/scale-1" || policy["id"] != "scale-1" {
		t.Fatalf("policy %v", policy)
	}
	analyticsOK(t, h, "GET", base+"/autoscalingPolicies/scale-1", nil)
	analyticsCount(t, h, base+"/autoscalingPolicies", "policies", 1)
	expect(t, h.call("owner", "PUT", base+"/autoscalingPolicies/scale-1", map[string]any{"id": "scale-2", "workerConfig": map[string]any{"maxInstances": 20}}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "PUT", base+"/autoscalingPolicies/scale-1", map[string]any{"id": "scale-1", "workerConfig": map[string]any{}}), 400, "INVALID_ARGUMENT")
	if got := analyticsOK(t, h, "PUT", base+"/autoscalingPolicies/scale-1", map[string]any{"id": "scale-1", "workerConfig": map[string]any{"maxInstances": 20}}); got["basicAlgorithm"] != nil || got["name"] != analyticsScope+"/autoscalingPolicies/scale-1" {
		t.Fatalf("replaced policy %v", got)
	}
	expect(t, h.call("owner", "GET", "/dataproc/v1/projects/tenkacloud-local/regions/us-central1/autoscalingPolicies", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", base+"/batches?batchId=b", map[string]any{}), 501, "UNIMPLEMENTED")
	analyticsOK(t, h, "DELETE", base+"/autoscalingPolicies/scale-1", nil)

	expect(t, h.call("owner", "POST", base+"/sessionTemplates", map[string]any{"name": "projects/tenkacloud-local/locations/us-east1/sessionTemplates/nb"}), 400, "INVALID_ARGUMENT")
	tmpl := analyticsOK(t, h, "POST", base+"/sessionTemplates", map[string]any{"name": analyticsScope + "/sessionTemplates/nb", "runtimeConfig": map[string]any{"version": "2.2"}, "jupyterSession": map[string]any{"kernel": "PYTHON"}})
	if tmpl["uuid"] == nil || tmpl["createTime"] == nil {
		t.Fatalf("session template %v", tmpl)
	}
	analyticsOK(t, h, "GET", base+"/sessionTemplates/nb", nil)
	analyticsCount(t, h, base+"/sessionTemplates", "sessionTemplates", 1)
	expect(t, h.call("owner", "PATCH", base+"/sessionTemplates/nb", map[string]any{"name": analyticsScope + "/sessionTemplates/other"}), 400, "INVALID_ARGUMENT")
	if got := analyticsOK(t, h, "PATCH", base+"/sessionTemplates/nb", map[string]any{"name": "projects/100000000001/locations/us-central1/sessionTemplates/nb", "description": "Notebook"}); got["description"] != "Notebook" || got["runtimeConfig"] != nil || got["uuid"] != tmpl["uuid"] {
		t.Fatalf("replaced template %v", got)
	}
	analyticsOK(t, h, "DELETE", base+"/sessionTemplates/nb", nil)
}

func TestAnalyticsCheckHelpers(t *testing.T) {
	require := analyticsRequire("a[].b", "c?.d")
	for _, body := range []map[string]any{{"a": []any{}}, {"a": []any{map[string]any{"b": ""}}}, {"a": []any{map[string]any{"b": "x"}}, "c": map[string]any{}}} {
		if require(body) == nil {
			t.Fatalf("%v accepted", body)
		}
	}
	if err := require(map[string]any{"a": []any{map[string]any{"b": "x"}}, "c": map[string]any{"d": false}}); err != nil {
		t.Fatal(err)
	}
	secrets := analyticsSecrets("password")
	if secrets(map[string]any{"list": []any{map[string]any{"password": "p"}}}) == nil || secrets(map[string]any{"password": map[string]any{"secretVersion": "v"}}) != nil {
		t.Fatal("secrets are refused only as string values")
	}
	if kafkaCapacity(map[string]any{}) != nil || kafkaCapacity(map[string]any{"capacityConfig": "x"}) == nil {
		t.Fatal("capacity checks")
	}
}
