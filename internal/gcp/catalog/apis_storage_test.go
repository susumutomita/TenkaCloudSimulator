package catalog

import (
	"net/url"
	"strings"
	"testing"
)

const storageProject = "projects/tenkacloud-local"

// storageCycle creates, reads, lists and patches one resource and returns
// its name and the create response.
func storageCycle(t *testing.T, h *harness, collection, query string, body map[string]any, items, mask string, patch map[string]any) (string, reply) {
	t.Helper()
	target := collection
	if query != "" {
		target += "?" + query
	}
	created := h.call("owner", "POST", target, body)
	expect(t, created, 200, "")
	resource := created.body
	if response, ok := created.body["response"].(map[string]any); ok {
		resource = response
	}
	name, _ := resource["name"].(string)
	if name == "" {
		t.Fatalf("create %s returned no resource name: %v", collection, created.body)
	}
	api := "/" + strings.SplitN(strings.TrimPrefix(collection, "/"), "/", 3)[0] + "/" + strings.SplitN(strings.TrimPrefix(collection, "/"), "/", 3)[1] + "/"
	expect(t, h.call("owner", "GET", api+name, nil), 200, "")
	listed := h.call("owner", "GET", collection, nil)
	expect(t, listed, 200, "")
	if found, _ := listed.body[items].([]any); len(found) == 0 {
		t.Fatalf("list %s returned no %s: %v", collection, items, listed.body)
	}
	if mask != "" {
		expect(t, h.call("owner", "PATCH", api+name+"?updateMask="+url.QueryEscape(mask), patch), 200, "")
		expect(t, h.call("owner", "PATCH", api+name+"?updateMask=name", map[string]any{"name": "x"}), 400, "INVALID_ARGUMENT")
	}
	return name, created
}

// storageOperations reads one completed operation and lists its scope.
func storageOperations(t *testing.T, h *harness, api string, created reply, scope string) {
	t.Helper()
	op, _ := created.body["name"].(string)
	if created.body["done"] != true || !strings.HasPrefix(op, scope+"/operations/") {
		t.Fatalf("operation %v is not under %s", created.body, scope)
	}
	expect(t, h.call("owner", "GET", api+op, nil), 200, "")
	listed := h.call("owner", "GET", api+scope+"/operations", nil)
	expect(t, listed, 200, "")
	if ops, _ := listed.body["operations"].([]any); len(ops) == 0 {
		t.Fatalf("operations under %s: %v", scope, listed.body)
	}
}

func storageRefused(t *testing.T, r reply, field string) {
	t.Helper()
	expect(t, r, 400, "INVALID_ARGUMENT")
	if !strings.Contains(r.raw, field) {
		t.Fatalf("refusal does not name %s: %s", field, r.raw)
	}
}

func TestFilestoreInstancesSnapshotsAndBackups(t *testing.T) {
	h := newHarness(t, filestore)
	base := "/file/v1/" + storageProject + "/locations/us-central1"
	instance := map[string]any{"tier": "BASIC_HDD", "fileShares": []any{map[string]any{"name": "share", "capacityGb": "1024"}},
		"networks": []any{map[string]any{"network": "default"}}, "deletionProtectionEnabled": true}
	name, created := storageCycle(t, h, base+"/instances", "instanceId=files", instance, "instances", "description", map[string]any{"description": "Files"})
	if created.body["response"].(map[string]any)["@type"] != "type.googleapis.com/google.cloud.filestore.v1.Instance" || created.body["response"].(map[string]any)["state"] != "READY" {
		t.Fatalf("instance operation %v", created.body)
	}
	storageOperations(t, h, "/file/v1/", created, storageProject+"/locations/us-central1")
	snapshot, _ := storageCycle(t, h, "/file/v1/"+name+"/snapshots", "snapshotId=snap", map[string]any{"description": "s"}, "snapshots", "labels", map[string]any{"labels": map[string]any{"k": "v"}})
	backup, _ := storageCycle(t, h, base+"/backups", "backupId=nightly", map[string]any{"sourceInstance": "projects/100000000001/locations/us-central1/instances/files", "sourceFileShare": "share"},
		"backups", "description", map[string]any{"description": "Nightly"})
	storageRefused(t, h.call("owner", "POST", base+"/backups?backupId=x", map[string]any{"sourceInstance": name}), "sourceFileShare")
	expect(t, h.call("owner", "POST", base+"/backups?backupId=x", map[string]any{"sourceInstance": name + "-missing", "sourceFileShare": "share"}), 404, "NOT_FOUND")
	storageRefused(t, h.call("owner", "POST", base+"/instances?instanceId=x", map[string]any{"tags": map[string]any{"123/env": "prod"}}), "tags")
	storageRefused(t, h.call("owner", "POST", base+"/instances?instanceId=x", map[string]any{"fileShares": []any{map[string]any{"name": "s", "sourceBackup": backup}}}), "fileShares.sourceBackup")
	storageRefused(t, h.call("owner", "PATCH", "/file/v1/"+name+"?updateMask=fileShares", map[string]any{"fileShares": []any{map[string]any{"name": "s", "sourceBackupdrBackup": "b"}}}), "sourceBackupdrBackup")
	storageRefused(t, h.call("owner", "POST", base+"/instances?instanceId=x", map[string]any{"replication": map[string]any{"role": "STANDBY"}}), "replication")
	storageRefused(t, h.call("owner", "POST", "/file/v1/"+name+"/snapshots?snapshotId=t", map[string]any{"tags": map[string]any{"a": "b"}}), "tags")
	expect(t, h.call("owner", "POST", "/file/v1/"+name+":restore", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", "/file/v1/"+name, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "PATCH", "/file/v1/"+name+"?updateMask=deletionProtectionEnabled", map[string]any{"deletionProtectionEnabled": false}), 200, "")
	expect(t, h.call("owner", "DELETE", "/file/v1/"+name, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", "/file/v1/"+name+"?force=true", nil), 200, "")
	expect(t, h.call("owner", "GET", "/file/v1/"+snapshot, nil), 404, "NOT_FOUND")
	expect(t, h.call("owner", "PATCH", "/file/v1/"+backup+"?updateMask=labels", map[string]any{"labels": map[string]any{"kept": "yes"}}), 200, "")
	expect(t, h.call("owner", "DELETE", "/file/v1/"+backup, nil), 200, "")
	expect(t, h.call("owner", "DELETE", "/file/v1/"+snapshot, nil), 404, "NOT_FOUND")
}

func TestNetAppPoolsVolumesSnapshotsAndBackupResources(t *testing.T) {
	h := newHarness(t, netAppVolumes)
	api := "/netapp/v1/"
	base := api + storageProject + "/locations/us-central1"
	poolBody := map[string]any{"capacityGib": "2048", "network": "projects/tenkacloud-local/global/networks/default", "serviceLevel": "PREMIUM"}
	pool, created := storageCycle(t, h, base+"/storagePools", "storagePoolId=pool", poolBody, "storagePools", "capacityGib", map[string]any{"capacityGib": "4096"})
	storageOperations(t, h, api, created, storageProject+"/locations/us-central1")
	expect(t, h.call("owner", "POST", base+"/storagePools?storagePoolId=bad", map[string]any{"capacityGib": "1"}), 400, "INVALID_ARGUMENT")
	storageRefused(t, h.call("owner", "POST", base+"/storagePools?storagePoolId=ad", map[string]any{"capacityGib": "1", "network": "n", "serviceLevel": "FLEX", "activeDirectory": "x"}), "activeDirectory")
	volumeBody := map[string]any{"capacityGib": "100", "protocols": []any{"NFSV3"}, "shareName": "share", "storagePool": pool}
	volume, _ := storageCycle(t, h, base+"/volumes", "volumeId=vol", volumeBody, "volumes", "description,labels", map[string]any{"description": "Volume"})
	expect(t, h.call("owner", "POST", base+"/volumes?volumeId=orphan", map[string]any{"capacityGib": "1", "protocols": []any{"NFSV3"}, "shareName": "s", "storagePool": pool + "-missing"}), 404, "NOT_FOUND")
	for _, field := range []string{"restoreParameters", "backupConfig", "hybridReplicationParameters", "cacheParameters"} {
		body := map[string]any{"capacityGib": "1", "protocols": []any{"NFSV3"}, "shareName": "s", "storagePool": pool, field: map[string]any{}}
		storageRefused(t, h.call("owner", "POST", base+"/volumes?volumeId=refused", body), field)
	}
	storageRefused(t, h.call("owner", "PATCH", api+volume+"?updateMask=description", map[string]any{"description": "x", "blockDevices": []any{map[string]any{}}}), "blockDevices")
	snapshot, _ := storageCycle(t, h, api+volume+"/snapshots", "snapshotId=snap", map[string]any{"description": "s"}, "snapshots", "description", map[string]any{"description": "t"})
	vault, _ := storageCycle(t, h, base+"/backupVaults", "backupVaultId=vault", map[string]any{"description": "v"}, "backupVaults", "description", map[string]any{"description": "w"})
	storageRefused(t, h.call("owner", "POST", base+"/backupVaults?backupVaultId=x", map[string]any{"backupVaultType": "CROSS_REGION"}), "backupVaultType")
	storageRefused(t, h.call("owner", "POST", base+"/backupVaults?backupVaultId=x", map[string]any{"backupRegion": "projects/p/locations/us-east1"}), "backupRegion")
	policy, _ := storageCycle(t, h, base+"/backupPolicies", "backupPolicyId=daily", map[string]any{"dailyBackupLimit": 2, "enabled": true}, "backupPolicies", "enabled", map[string]any{"enabled": false})
	expect(t, h.call("owner", "DELETE", api+pool, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", api+volume, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", api+snapshot, nil), 200, "")
	for _, name := range []string{volume, pool, vault, policy} {
		expect(t, h.call("owner", "DELETE", api+name, nil), 200, "")
	}
}

func TestMemorystoreRedisAndMemcachedInstances(t *testing.T) {
	h := newHarness(t, memorystoreRedis, memorystoreMemcached)
	redis := "/redis/v1/" + storageProject + "/locations/us-central1"
	instance, created := storageCycle(t, h, redis+"/instances", "instanceId=cache", map[string]any{"memorySizeGb": 1, "tier": "BASIC", "authEnabled": true},
		"instances", "memorySizeGb", map[string]any{"memorySizeGb": 2})
	storageOperations(t, h, "/redis/v1/", created, storageProject+"/locations/us-central1")
	expect(t, h.call("owner", "GET", "/redis/v1/"+instance+"/authString", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "POST", redis+"/instances?instanceId=x", map[string]any{"tier": "BASIC"}), 400, "INVALID_ARGUMENT")
	storageRefused(t, h.call("owner", "POST", redis+"/instances?instanceId=x", map[string]any{"memorySizeGb": 1, "tier": "BASIC", "tags": map[string]any{"a": "b"}}), "tags")
	cluster, clusterOp := storageCycle(t, h, redis+"/clusters", "clusterId=shards", map[string]any{"shardCount": 3, "deletionProtectionEnabled": true},
		"clusters", "replicaCount", map[string]any{"replicaCount": 1})
	response := clusterOp.body["response"].(map[string]any)
	if response["@type"] != "type.googleapis.com/google.cloud.redis.cluster.v1.Cluster" || response["state"] != "ACTIVE" || response["uid"] == nil {
		t.Fatalf("cluster operation %v", clusterOp.body)
	}
	for _, field := range []string{"gcsSource", "managedBackupSource", "crossClusterReplicationConfig"} {
		storageRefused(t, h.call("owner", "POST", redis+"/clusters?clusterId=x", map[string]any{field: map[string]any{}}), field)
	}
	storageRefused(t, h.call("owner", "POST", redis+"/clusters?clusterId=x", map[string]any{"aclPolicy": "p"}), "aclPolicy")
	storageRefused(t, h.call("owner", "PATCH", "/redis/v1/"+cluster+"?updateMask=replicaCount", map[string]any{"replicaCount": 2, "rotateServerCertificate": true}), "rotateServerCertificate")
	expect(t, h.call("owner", "DELETE", "/redis/v1/"+cluster, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "PATCH", "/redis/v1/"+cluster+"?updateMask=deletionProtectionEnabled", map[string]any{"deletionProtectionEnabled": false}), 200, "")
	expect(t, h.call("owner", "DELETE", "/redis/v1/"+cluster, nil), 200, "")
	expect(t, h.call("owner", "DELETE", "/redis/v1/"+instance, nil), 200, "")

	memcache := "/memcache/v1/" + storageProject + "/locations/us-central1"
	node := map[string]any{"nodeCount": 1, "nodeConfig": map[string]any{"cpuCount": 1, "memorySizeMb": 1024}}
	memcached, memOp := storageCycle(t, h, memcache+"/instances", "instanceId=memo", node, "instances", "displayName", map[string]any{"displayName": "Memo"})
	storageOperations(t, h, "/memcache/v1/", memOp, storageProject+"/locations/us-central1")
	expect(t, h.call("owner", "PATCH", "/memcache/v1/"+memcached+"?updateMask=nodeCount", map[string]any{"nodeCount": 2}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", memcache+"/instances?instanceId=x", map[string]any{"nodeCount": 1}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "DELETE", "/memcache/v1/"+memcached, nil), 200, "")
}

func TestBackupAndDRVaultsPlansAndManagementServers(t *testing.T) {
	h := newHarness(t, backupAndDR)
	api := "/backupdr/v1/"
	base := api + storageProject + "/locations/us-central1"
	vault, created := storageCycle(t, h, base+"/backupVaults", "backupVaultId=vault", map[string]any{"backupMinimumEnforcedRetentionDuration": "86400s"},
		"backupVaults", "description", map[string]any{"description": "Vault"})
	storageOperations(t, h, api, created, storageProject+"/locations/us-central1")
	storageRefused(t, h.call("owner", "POST", base+"/backupVaults?backupVaultId=locked", map[string]any{"backupMinimumEnforcedRetentionDuration": "86400s", "effectiveTime": "2026-12-01T00:00:00Z"}), "effectiveTime")
	expect(t, h.call("owner", "POST", base+"/backupVaults?backupVaultId=x", map[string]any{}), 400, "INVALID_ARGUMENT")
	plan, _ := storageCycle(t, h, base+"/backupPlans", "backupPlanId=plan", map[string]any{"backupVault": vault, "resourceType": "compute.googleapis.com/Instance",
		"backupRules": []any{map[string]any{"ruleId": "daily", "backupRetentionDays": 7}}}, "backupPlans", "description", map[string]any{"description": "Plan"})
	expect(t, h.call("owner", "POST", base+"/backupPlans?backupPlanId=x", map[string]any{"backupVault": vault + "-missing", "resourceType": "compute.googleapis.com/Instance"}), 404, "NOT_FOUND")
	server, _ := storageCycle(t, h, base+"/managementServers", "managementServerId=console", map[string]any{"type": "BACKUP_RESTORE"}, "managementServers", "", nil)
	expect(t, h.call("owner", "DELETE", api+vault, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "DELETE", api+vault+"?ignoreBackupPlanReferences=true&validateOnly=true", nil), 200, "")
	expect(t, h.call("owner", "POST", base+"/backupPlanAssociations?backupPlanAssociationId=a", map[string]any{}), 501, "UNIMPLEMENTED")
	for _, name := range []string{plan, vault, server} {
		expect(t, h.call("owner", "DELETE", api+name, nil), 200, "")
	}
}

func TestBackupForGKEPlans(t *testing.T) {
	h := newHarness(t, backupForGKE)
	api := "/gkebackup/v1/"
	base := api + storageProject + "/locations/us-central1"
	cluster := storageProject + "/locations/us-central1/clusters/apps"
	plan, created := storageCycle(t, h, base+"/backupPlans", "backupPlanId=apps", map[string]any{"cluster": cluster, "backupConfig": map[string]any{"allNamespaces": true}},
		"backupPlans", "description", map[string]any{"description": "Apps"})
	storageOperations(t, h, api, created, storageProject+"/locations/us-central1")
	storageRefused(t, h.call("owner", "POST", base+"/backupPlans?backupPlanId=x", map[string]any{"cluster": "apps"}), "cluster")
	storageRefused(t, h.call("owner", "POST", base+"/backupPlans?backupPlanId=x", map[string]any{"cluster": cluster, "deactivated": true}), "deactivated")
	storageRefused(t, h.call("owner", "PATCH", api+plan+"?updateMask=retentionPolicy", map[string]any{"retentionPolicy": map[string]any{"locked": true}}), "retentionPolicy.locked")
	restoreBody := map[string]any{"backupPlan": plan, "cluster": cluster, "restoreConfig": map[string]any{"allNamespaces": true}}
	restore, _ := storageCycle(t, h, base+"/restorePlans", "restorePlanId=back", restoreBody, "restorePlans", "description", map[string]any{"description": "Back"})
	restoreBody["backupPlan"] = plan + "-missing"
	expect(t, h.call("owner", "POST", base+"/restorePlans?restorePlanId=x", restoreBody), 404, "NOT_FOUND")
	storageRefused(t, h.call("owner", "POST", base+"/restorePlans?restorePlanId=x", map[string]any{"backupPlan": plan, "cluster": "c", "restoreConfig": map[string]any{}}), "cluster")
	expect(t, h.call("owner", "POST", api+plan+"/backups?backupId=b", map[string]any{}), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", api+plan, nil), 200, "")
	expect(t, h.call("owner", "DELETE", api+restore, nil), 200, "")
}

func TestFirestoreDatabasesBackupSchedulesAndOperations(t *testing.T) {
	h := newHarness(t, firestoreAdmin)
	api := "/firestore/v1/"
	db, created := storageCycle(t, h, api+storageProject+"/databases", "databaseId=%28default%29", map[string]any{"locationId": "nam5", "type": "FIRESTORE_NATIVE", "deleteProtectionState": "DELETE_PROTECTION_ENABLED"},
		"databases", "pointInTimeRecoveryEnablement", map[string]any{"pointInTimeRecoveryEnablement": "POINT_IN_TIME_RECOVERY_ENABLED"})
	if db != storageProject+"/databases/(default)" || created.body["response"].(map[string]any)["@type"] != "type.googleapis.com/google.firestore.admin.v1.Database" {
		t.Fatalf("database operation %v", created.body)
	}
	storageOperations(t, h, api, created, db)
	expect(t, h.call("owner", "POST", api+storageProject+"/databases?databaseId=Bad", map[string]any{"locationId": "nam5", "type": "FIRESTORE_NATIVE"}), 400, "INVALID_ARGUMENT")
	expect(t, h.call("owner", "POST", api+storageProject+"/databases?databaseId=untyped", map[string]any{"locationId": "nam5"}), 400, "INVALID_ARGUMENT")
	storageRefused(t, h.call("owner", "POST", api+storageProject+"/databases?databaseId=tagged", map[string]any{"locationId": "nam5", "type": "FIRESTORE_NATIVE", "tags": map[string]any{"a": "b"}}), "tags")
	schedule, _ := storageCycle(t, h, api+db+"/backupSchedules", "", map[string]any{"retention": "604800s", "dailyRecurrence": map[string]any{}}, "backupSchedules", "retention", map[string]any{"retention": "86400s"})
	storageRefused(t, h.call("owner", "POST", api+db+"/backupSchedules", map[string]any{"retention": "604800s"}), "dailyRecurrence")
	storageRefused(t, h.call("owner", "POST", api+db+"/backupSchedules", map[string]any{"dailyRecurrence": map[string]any{}, "weeklyRecurrence": map[string]any{"day": "MONDAY"}}), "weeklyRecurrence")
	expect(t, h.call("owner", "GET", api+db+"/documents/users/alice", nil), 501, "UNIMPLEMENTED")
	expect(t, h.call("owner", "DELETE", api+db, nil), 400, "FAILED_PRECONDITION")
	expect(t, h.call("owner", "PATCH", api+db+"?updateMask=deleteProtectionState", map[string]any{"deleteProtectionState": "DELETE_PROTECTION_DISABLED"}), 200, "")
	deleted := h.call("owner", "DELETE", api+db, nil)
	expect(t, deleted, 200, "")
	if !strings.HasPrefix(deleted.body["name"].(string), db+"/operations/") {
		t.Fatalf("delete operation %v", deleted.body)
	}
	expect(t, h.call("owner", "GET", api+schedule, nil), 404, "NOT_FOUND")
	recreated := h.call("owner", "POST", api+storageProject+"/databases?databaseId=orders", map[string]any{"locationId": "nam5", "type": "DATASTORE_MODE"})
	expect(t, recreated, 200, "")
	second, _ := storageCycle(t, h, api+storageProject+"/databases/orders/backupSchedules", "", map[string]any{"weeklyRecurrence": map[string]any{"day": "MONDAY"}}, "backupSchedules", "", nil)
	expect(t, h.call("owner", "DELETE", api+second, nil), 200, "")
}

func TestStorageCheckHelpers(t *testing.T) {
	body := map[string]any{"a": []any{map[string]any{"b": 1}, map[string]any{"b": 2}}}
	if got := storageValues(body, []string{"a", "b"}); len(got) != 2 {
		t.Fatalf("values %v", got)
	}
	if storageValues(body, []string{"missing"}) != nil || storageValues("x", []string{"a"}) != nil {
		t.Fatal("absent paths have no values")
	}
	if storageRefuseValue("a.b", 3, "x")(body) != nil || storageRefuseValue("a.b", 2, "x")(body) == nil {
		t.Fatal("value refusal")
	}
}
