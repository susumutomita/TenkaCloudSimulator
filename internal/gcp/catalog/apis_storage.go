package catalog

import (
	"regexp"
	"strings"
)

// Reviewed bounded specifications for Google Cloud storage, cache, backup
// and database administration APIs. Every resource is configuration
// metadata; no file system, volume, cache node, backup data or database
// engine exists.

// storageCRUD binds the standard methods of one collection under prefix.
func storageCRUD(prefix, collection string, create, patch func(map[string]any) error, verbs ...Kind) []Binding {
	if len(verbs) == 0 {
		verbs = []Kind{Create, Get, List, Patch, Delete}
	}
	out := make([]Binding, 0, len(verbs))
	for _, kind := range verbs {
		b := Binding{Method: prefix + "." + string(kind), Kind: kind, Collection: collection}
		switch kind {
		case Create:
			b.Check = create
		case Patch:
			b.Check = patch
		}
		out = append(out, b)
	}
	return out
}

var storageLocationOperations = []Binding{
	{Method: "projects.locations.operations.get", Kind: OperationGet},
	{Method: "projects.locations.operations.list", Kind: OperationList},
}

func storageBindings(groups ...[]Binding) []Binding {
	var out []Binding
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

var filestore = &API{
	Key: "file.v1", Title: "Filestore", Issues: []int{333}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.filestore.v1.",
	Collections: []*Collection{
		{ID: "instances", Schema: "Instance", IDParam: "instanceId", Limit: 20, Permission: "file.instances", Cascade: true,
			Protection: map[string]any{"deletionProtectionEnabled": true},
			Generate:   map[string]any{"createTime": "$now", "state": "READY", "etag": "$etag"},
			Mutable:    []string{"description", "fileShares", "labels", "performanceConfig", "deletionProtectionEnabled", "deletionProtectionReason"}},
		{ID: "snapshots", Schema: "Snapshot", Parent: "instances", IDParam: "snapshotId", Limit: 50, Permission: "file.snapshots",
			Generate: map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:  []string{"description", "labels"}},
		{ID: "backups", Schema: "Backup", IDParam: "backupId", Limit: 50, Permission: "file.backups",
			References: map[string]Reference{"sourceInstance": {Collection: "instances"}},
			Generate:   map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:    []string{"description", "labels"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.locations.instances", "instances", filestoreInstance, filestoreInstance),
		storageCRUD("projects.locations.instances.snapshots", "snapshots", noTagBindings, nil),
		storageCRUD("projects.locations.backups", "backups", storageAll(noTagBindings, storageRequire("sourceInstance", "sourceFileShare")), nil),
		storageLocationOperations,
	),
	Bounds: []string{
		"Instances, snapshots and backups are configuration metadata: no NFS server, file share, network address or file data exists, and every resource reports READY immediately.",
		"A backup must name an existing instance as sourceInstance but captures no data; restore, revert and replica methods remain unimplemented, and file shares restored from backups, replication and tag bindings are refused.",
		"Long-running operations complete synchronously. Deletion protection blocks delete, and an instance with snapshots is deleted only with force=true.",
	},
}

var netAppVolumes = &API{
	Key: "netapp.v1", Title: "Google Cloud NetApp Volumes", Issues: []int{335}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.netapp.v1.",
	Collections: []*Collection{
		{ID: "storagePools", Schema: "StoragePool", IDParam: "storagePoolId", Limit: 10, Permission: "netapp.storagePools",
			Generate: map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:  []string{"description", "labels", "capacityGib", "allowAutoTiering", "totalThroughputMibps", "totalIops", "hotTierSizeGib", "enableHotTierAutoResize", "qosType"}},
		{ID: "volumes", Schema: "Volume", IDParam: "volumeId", Limit: 50, Permission: "netapp.volumes", Cascade: true,
			References: map[string]Reference{"storagePool": {Collection: "storagePools", Blocks: true}},
			Generate:   map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:    []string{"description", "labels", "capacityGib", "exportPolicy", "smbSettings", "snapshotPolicy", "unixPermissions", "snapReserve", "snapshotDirectory", "throughputMibps", "restrictedActions"}},
		{ID: "snapshots", Schema: "Snapshot", Parent: "volumes", IDParam: "snapshotId", Limit: 50, Permission: "netapp.snapshots",
			Generate: map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:  []string{"description", "labels"}},
		{ID: "backupVaults", Schema: "BackupVault", IDParam: "backupVaultId", Limit: 10, Permission: "netapp.backupVaults",
			Generate: map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:  []string{"description", "labels", "backupRetentionPolicy"}},
		{ID: "backupPolicies", Schema: "BackupPolicy", IDParam: "backupPolicyId", Limit: 10, Permission: "netapp.backupPolicies",
			Generate: map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:  []string{"description", "labels", "dailyBackupLimit", "weeklyBackupLimit", "monthlyBackupLimit", "enabled"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.locations.storagePools", "storagePools", netAppPool, netAppPool),
		storageCRUD("projects.locations.volumes", "volumes", netAppVolume, netAppVolume),
		storageCRUD("projects.locations.volumes.snapshots", "snapshots", nil, nil),
		storageCRUD("projects.locations.backupVaults", "backupVaults", netAppVault, netAppVault),
		storageCRUD("projects.locations.backupPolicies", "backupPolicies", nil, nil),
		storageLocationOperations,
	),
	Bounds: []string{
		"Storage pools, volumes, snapshots, backup vaults and backup policies are configuration metadata: no ONTAP system, NFS, SMB or block endpoint, capacity accounting or data exists, and every resource reports READY immediately.",
		"A volume must name an existing storage pool, and a pool cannot be deleted while a volume names it; derived volume fields such as serviceLevel and network are not populated.",
		"Active Directory, KMS, restore, backup, replication, cache, block device and cross-region vault settings are refused; delete a volume with snapshots only with force=true. Long-running operations complete synchronously.",
	},
}

var memorystoreRedis = &API{
	Key: "redis.v1", Title: "Memorystore for Redis", Issues: []int{339}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.redis.v1.",
	Collections: []*Collection{
		{ID: "instances", Schema: "Instance", IDParam: "instanceId", Limit: 20, Permission: "redis.instances",
			Generate: map[string]any{"createTime": "$now", "state": "READY"},
			Mutable:  []string{"displayName", "labels", "memorySizeGb", "redisConfigs", "replicaCount", "maintenancePolicy", "persistenceConfig"}},
		{ID: "clusters", Schema: "Cluster", Message: "type.googleapis.com/google.cloud.redis.cluster.v1.Cluster", IDParam: "clusterId", Limit: 10, Permission: "redis.clusters",
			Protection: map[string]any{"deletionProtectionEnabled": true},
			Generate:   map[string]any{"uid": "$uid", "createTime": "$now", "state": "ACTIVE"},
			Mutable:    []string{"shardCount", "replicaCount", "redisConfigs", "persistenceConfig", "deletionProtectionEnabled", "maintenancePolicy", "labels"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.locations.instances", "instances", noTagBindings, nil),
		storageCRUD("projects.locations.clusters", "clusters", redisCluster, redisCluster),
		storageLocationOperations,
	),
	Bounds: []string{
		"Instances and clusters are configuration metadata: no Redis node, endpoint, AUTH string, certificate or cached data exists, and they report READY or ACTIVE immediately; getAuthString, import, export, failover and upgrade remain unimplemented.",
		"Backup restores, cross-cluster replication, ACL policies, customer-managed CA pools, maintenance triggers and tag bindings are refused. Deletion protection blocks cluster delete. Long-running operations complete synchronously.",
	},
}

var memorystoreMemcached = &API{
	Key: "memcache.v1", Title: "Memorystore for Memcached", Issues: []int{339}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.memcache.v1.",
	Collections: []*Collection{
		{ID: "instances", Schema: "Instance", IDParam: "instanceId", Limit: 20, Permission: "memcache.instances",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "READY"},
			Mutable:  []string{"displayName"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.locations.instances", "instances", nil, nil),
		storageLocationOperations,
	),
	Bounds: []string{
		"Instances are configuration metadata: no Memcached node, discovery endpoint or cached data exists, and instances report READY immediately.",
		"Only displayName is updatable; applyParameters, updateParameters, upgrade and maintenance methods remain unimplemented. Long-running operations complete synchronously.",
	},
}

var backupAndDR = &API{
	Key: "backupdr.v1", Title: "Backup and DR Service", Issues: []int{377}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.backupdr.v1.",
	Collections: []*Collection{
		{ID: "backupVaults", Schema: "BackupVault", IDParam: "backupVaultId", Limit: 10, Permission: "backupdr.backupVaults",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "ACTIVE", "etag": "$etag", "deletable": true, "backupCount": "0", "totalStoredBytes": "0"},
			Mutable:  []string{"description", "labels", "annotations", "backupMinimumEnforcedRetentionDuration", "accessRestriction", "backupRetentionInheritance"}},
		{ID: "backupPlans", Schema: "BackupPlan", IDParam: "backupPlanId", Limit: 20, Permission: "backupdr.backupPlans",
			References: map[string]Reference{"backupVault": {Collection: "backupVaults", Blocks: true, Override: "ignoreBackupPlanReferences"}},
			Generate:   map[string]any{"createTime": "$now", "updateTime": "$now", "state": "ACTIVE", "etag": "$etag"},
			Mutable:    []string{"description", "labels", "backupRules", "logRetentionDays", "maxCustomOnDemandRetentionDays", "computeInstanceBackupPlanProperties", "diskBackupPlanProperties"}},
		{ID: "managementServers", Schema: "ManagementServer", IDParam: "managementServerId", Limit: 5, Permission: "backupdr.managementServers",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "READY"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.locations.backupVaults", "backupVaults", backupDRVault, backupDRVault),
		storageCRUD("projects.locations.backupPlans", "backupPlans", nil, nil),
		storageCRUD("projects.locations.managementServers", "managementServers", nil, nil, Create, Get, List, Delete),
		storageLocationOperations,
	),
	Bounds: []string{
		"Backup vaults, backup plans and management servers are configuration metadata: no backup is scheduled or taken, no data is stored or restored, and no management console, appliance or endpoint is deployed; resources report ACTIVE or READY immediately.",
		"A backup plan must name an existing backup vault, and a vault cannot be deleted while a plan names it unless ignoreBackupPlanReferences=true. Vault lock times are refused, and allowMissing on delete is not honored.",
		"Backup plan associations, data sources, backups and trials remain unimplemented. Long-running operations complete synchronously.",
	},
}

var backupForGKE = &API{
	Key: "gkebackup.v1", Title: "Backup for GKE", Issues: []int{329}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.gkebackup.v1.",
	Collections: []*Collection{
		{ID: "backupPlans", Schema: "BackupPlan", IDParam: "backupPlanId", Limit: 20, Permission: "gkebackup.backupPlans",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "READY", "etag": "$etag"},
			Mutable:  []string{"description", "labels", "backupConfig", "backupSchedule", "retentionPolicy"}},
		{ID: "restorePlans", Schema: "RestorePlan", IDParam: "restorePlanId", Limit: 20, Permission: "gkebackup.restorePlans",
			References: map[string]Reference{"backupPlan": {Collection: "backupPlans"}},
			Generate:   map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "READY", "etag": "$etag"},
			Mutable:    []string{"description", "labels", "restoreConfig"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.locations.backupPlans", "backupPlans", gkeBackupPlan, gkeBackupPlan),
		storageCRUD("projects.locations.restorePlans", "restorePlans", gkeCluster, nil),
		storageLocationOperations,
	),
	Bounds: []string{
		"Backup plans and restore plans are configuration metadata: the GKE cluster name is checked only for its format, no Backup for GKE agent runs, and no Kubernetes resource or volume is backed up or restored; plans report READY immediately.",
		"A restore plan must name an existing backup plan. Deactivated plans and locked retention policies are refused because their locks are not enforced. Backups, restores and channels remain unimplemented. Long-running operations complete synchronously.",
	},
}

var firestoreAdmin = &API{
	Key: "firestore.v1", Title: "Firestore", Issues: []int{338}, Scope: "project",
	TypeURL: "type.googleapis.com/google.firestore.admin.v1.",
	Collections: []*Collection{
		{ID: "databases", Schema: "GoogleFirestoreAdminV1Database", Message: "Database", IDParam: "databaseId", Limit: 20, Permission: "datastore.databases",
			IDPattern: regexp.MustCompile(`^(\(default\)|[a-z][a-z0-9-]{2,61}[a-z0-9])$`), Cascade: true, Operations: "databases",
			Protection: map[string]any{"deleteProtectionState": "DELETE_PROTECTION_ENABLED"},
			Generate:   map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "etag": "$etag"},
			Mutable:    []string{"deleteProtectionState", "pointInTimeRecoveryEnablement", "concurrencyMode", "appEngineIntegrationMode"}},
		{ID: "backupSchedules", Schema: "GoogleFirestoreAdminV1BackupSchedule", Message: "BackupSchedule", Parent: "databases", Limit: 2, Permission: "datastore.backupSchedules",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"},
			Mutable:  []string{"retention"}},
	},
	Bindings: storageBindings(
		storageCRUD("projects.databases", "databases", noTagBindings, nil),
		storageCRUD("projects.databases.backupSchedules", "backupSchedules", backupRecurrence, nil),
		[]Binding{
			{Method: "projects.databases.operations.get", Kind: OperationGet},
			{Method: "projects.databases.operations.list", Kind: OperationList},
		},
	),
	Bounds: []string{
		"Databases and backup schedules are configuration metadata: no document store, query engine, index build or backup exists. The documents, export, import, clone, restore, index, field and user credential methods remain unimplemented.",
		"Deleting a database removes its backup schedules immediately and keeps no deleted database for showDeleted; deletion protection blocks delete. Tag bindings are refused. Operations complete synchronously under projects/{project}/databases/{database}/operations.",
	},
}

// storageAll runs checks in order and returns the first failure.
func storageAll(checks ...func(map[string]any) error) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, check := range checks {
			if err := check(body); err != nil {
				return err
			}
		}
		return nil
	}
}

// storageValues returns every value at a dotted path, descending into lists.
func storageValues(value any, path []string) []any {
	if len(path) == 0 {
		return []any{value}
	}
	var out []any
	switch v := value.(type) {
	case map[string]any:
		if child, ok := v[path[0]]; ok {
			out = append(out, storageValues(child, path[1:])...)
		}
	case []any:
		for _, item := range v {
			out = append(out, storageValues(item, path)...)
		}
	}
	return out
}

// storageRefuse refuses fields whose effect the simulator does not provide.
func storageRefuse(reason string, paths ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, path := range paths {
			if len(storageValues(body, strings.Split(path, "."))) > 0 {
				return invalid("Field '%s' is not supported by the simulator: %s", path, reason)
			}
		}
		return nil
	}
}

// storageRefuseValue refuses one value of a field, such as a lock flag.
func storageRefuseValue(path string, value any, reason string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, found := range storageValues(body, strings.Split(path, ".")) {
			if found == value {
				return invalid("Field '%s' = %v is not supported by the simulator: %s", path, value, reason)
			}
		}
		return nil
	}
}

// storageRequire enforces fields the official service requires without a
// "Required." marker in the discovery document.
func storageRequire(fields ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, field := range fields {
			if text, _ := body[field].(string); text == "" {
				return invalid("Field '%s' is required.", field)
			}
		}
		return nil
	}
}

var (
	noTagBindings     = storageRefuse("Resource Manager tag bindings are not simulated.", "tags")
	filestoreInstance = storageAll(noTagBindings,
		storageRefuse("no file data exists to restore or replicate.", "fileShares.sourceBackup", "fileShares.sourceBackupdrBackup", "replication"))
	netAppPool = storageRefuse("Active Directory and KMS configurations are not implemented by the simulator.", "activeDirectory", "kmsConfig")
	// Volume settings that restore, back up, replicate, cache or export
	// block devices need data or peers the simulator does not have.
	netAppVolume = storageRefuse("no volume data, backup, replica, cache peer or host group exists.",
		"restoreParameters", "backupConfig", "hybridReplicationParameters", "cacheParameters", "blockDevices")
	netAppVault = storageAll(storageRefuse("KMS configurations and cross-region vault pairs are not implemented by the simulator.", "kmsConfig", "backupRegion"),
		storageRefuseValue("backupVaultType", "CROSS_REGION", "cross-region vault pairs are not created."))
	redisCluster = storageAll(
		storageRefuse("no backup, replica, ACL policy or CA pool exists.", "gcsSource", "managedBackupSource", "crossClusterReplicationConfig", "aclPolicy", "serverCaPool"),
		storageRefuse("maintenance and certificate actions are not simulated.", "rotateServerCertificate", "simulateMaintenanceEvent", "ondemandMaintenance"))
	backupDRVault  = storageRefuse("vault locks are not enforced.", "effectiveTime")
	gkeClusterName = regexp.MustCompile(`^projects/[^/]+/(locations|zones)/[^/]+/clusters/[^/]+$`)
	gkeBackupPlan  = storageAll(gkeCluster,
		storageRefuseValue("deactivated", true, "the deactivation lock is not enforced."),
		storageRefuseValue("retentionPolicy.locked", true, "the retention lock is not enforced."))
)

// gkeCluster accepts the documented GKE cluster name formats when present.
func gkeCluster(body map[string]any) error {
	if cluster, ok := body["cluster"].(string); ok && !gkeClusterName.MatchString(cluster) {
		return invalid("Field 'cluster' must be projects/*/locations/*/clusters/* or projects/*/zones/*/clusters/*.")
	}
	return nil
}

// backupRecurrence requires exactly one schedule, as the official oneof does.
func backupRecurrence(body map[string]any) error {
	_, daily := body["dailyRecurrence"]
	_, weekly := body["weeklyRecurrence"]
	if daily == weekly {
		return invalid("Exactly one of 'dailyRecurrence' and 'weeklyRecurrence' is required.")
	}
	return nil
}
