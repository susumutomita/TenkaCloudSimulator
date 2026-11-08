package catalog

// Reviewed bounded specifications for Google Cloud database APIs.

var alloyDB = &API{
	Key: "alloydb.v1", Title: "AlloyDB for PostgreSQL", Issues: []int{336}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.alloydb.v1.",
	Collections: []*Collection{
		{ID: "clusters", Schema: "Cluster", IDParam: "clusterId", Limit: 20, Permission: "alloydb.clusters", Cascade: true, Relax: []string{"network"},
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "READY", "etag": "$etag", "clusterType": "PRIMARY", "reconciling": false},
			Mutable:  []string{"displayName", "labels", "annotations", "automatedBackupPolicy", "continuousBackupConfig", "maintenanceUpdatePolicy"}},
		{ID: "instances", Schema: "Instance", Parent: "clusters", IDParam: "instanceId", Limit: 20, Permission: "alloydb.instances",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "READY", "etag": "$etag", "reconciling": false},
			Mutable:  []string{"displayName", "labels", "annotations", "machineConfig", "databaseFlags", "availabilityType"}},
		{ID: "users", Schema: "User", Parent: "clusters", IDParam: "userId", Limit: 50, Permission: "alloydb.users",
			Generate: map[string]any{"userType": "ALLOYDB_BUILT_IN"},
			Mutable:  []string{"databaseRoles"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.clusters.create", Kind: Create, Collection: "clusters", Check: clusterNetwork},
		{Method: "projects.locations.clusters.get", Kind: Get, Collection: "clusters"},
		{Method: "projects.locations.clusters.list", Kind: List, Collection: "clusters"},
		{Method: "projects.locations.clusters.patch", Kind: Patch, Collection: "clusters"},
		{Method: "projects.locations.clusters.delete", Kind: Delete, Collection: "clusters"},
		{Method: "projects.locations.clusters.instances.create", Kind: Create, Collection: "instances"},
		{Method: "projects.locations.clusters.instances.get", Kind: Get, Collection: "instances"},
		{Method: "projects.locations.clusters.instances.list", Kind: List, Collection: "instances"},
		{Method: "projects.locations.clusters.instances.patch", Kind: Patch, Collection: "instances"},
		{Method: "projects.locations.clusters.instances.delete", Kind: Delete, Collection: "instances"},
		{Method: "projects.locations.clusters.users.create", Kind: Create, Collection: "users", Check: noPassword},
		{Method: "projects.locations.clusters.users.get", Kind: Get, Collection: "users"},
		{Method: "projects.locations.clusters.users.list", Kind: List, Collection: "users"},
		{Method: "projects.locations.clusters.users.patch", Kind: Patch, Collection: "users", Check: noPassword},
		{Method: "projects.locations.clusters.users.delete", Kind: Delete, Collection: "users"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Clusters, instances and users are configuration metadata: no PostgreSQL engine, network endpoint, storage or backup is created; instances report READY immediately.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
		"User passwords are refused and never stored; delete a cluster with children only with force=true.",
	},
}

// noPassword refuses credentials: the simulator never stores secrets for a
// database engine it does not run.
func noPassword(body map[string]any) error {
	if _, ok := body["password"]; ok {
		return invalid("Passwords are not stored by the simulator.")
	}
	return nil
}

// clusterNetwork accepts the current networkConfig.network or the deprecated
// top-level network, as the official field descriptions allow. It also
// refuses initialUser.password, which the simulator never stores.
func clusterNetwork(body map[string]any) error {
	if user, ok := body["initialUser"].(map[string]any); ok {
		if err := noPassword(user); err != nil {
			return err
		}
	}
	if network, _ := body["network"].(string); network != "" {
		return nil
	}
	if config, ok := body["networkConfig"].(map[string]any); ok {
		if network, _ := config["network"].(string); network != "" {
			return nil
		}
	}
	return invalid("Field 'networkConfig.network' is required.")
}
