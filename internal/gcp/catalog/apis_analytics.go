package catalog

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Reviewed bounded specifications for Google Cloud data analytics APIs.
// Every collection is configuration metadata: no broker, pipeline, stream,
// metastore, interpreter, notebook, Airflow, Looker, Spark or data plane runs.

var (
	// Kafka topic names allow letters, digits, '.', '_' and '-'.
	kafkaTopicID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)
	// Dataproc Metastore IDs begin with a letter and allow upper case.
	metastoreServiceID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,61}[A-Za-z0-9]$`)
	metastoreBackupID  = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?$`)
	// Cloud Data Fusion instance names have at most 30 characters.
	dataFusionInstanceID = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$`)
	// Cloud Composer environment IDs have at most 64 characters.
	composerEnvironmentID = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,62}[a-z0-9])?$`)
	// BigLake catalog, database and table IDs follow Hive identifiers.
	bigLakeID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
	// Dataproc autoscaling policy IDs have 3 to 50 characters.
	dataprocPolicyID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{1,48}[A-Za-z0-9]$`)
)

var managedKafka = &API{
	Key: "managedkafka.v1", Title: "Managed Service for Apache Kafka", Issues: []int{399}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.managedkafka.v1.",
	Collections: []*Collection{
		{ID: "clusters", Schema: "Cluster", IDParam: "clusterId", Limit: 10, Permission: "managedkafka.clusters",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable:  []string{"labels", "capacityConfig", "rebalanceConfig", "tlsConfig", "updateOptions"}},
		{ID: "topics", Schema: "Topic", Parent: "clusters", IDParam: "topicId", IDPattern: kafkaTopicID, Limit: 100, Permission: "managedkafka.topics",
			Mutable: []string{"configs"}},
		{ID: "connectClusters", Schema: "ConnectCluster", IDParam: "connectClusterId", Limit: 10, Permission: "managedkafka.connectClusters",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable:  []string{"labels", "capacityConfig"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.clusters.create", Kind: Create, Collection: "clusters",
			Check: analyticsAll(kafkaCapacity, analyticsRequire("gcpConfig.accessConfig.networkConfigs[].subnet"))},
		{Method: "projects.locations.clusters.get", Kind: Get, Collection: "clusters"},
		{Method: "projects.locations.clusters.list", Kind: List, Collection: "clusters"},
		{Method: "projects.locations.clusters.patch", Kind: Patch, Collection: "clusters", Check: kafkaCapacity},
		{Method: "projects.locations.clusters.delete", Kind: Delete, Collection: "clusters"},
		{Method: "projects.locations.clusters.topics.create", Kind: Create, Collection: "topics", Check: kafkaTopic},
		{Method: "projects.locations.clusters.topics.get", Kind: Get, Collection: "topics"},
		{Method: "projects.locations.clusters.topics.list", Kind: List, Collection: "topics"},
		{Method: "projects.locations.clusters.topics.patch", Kind: Patch, Collection: "topics"},
		{Method: "projects.locations.clusters.topics.delete", Kind: Delete, Collection: "topics"},
		{Method: "projects.locations.connectClusters.create", Kind: Create, Collection: "connectClusters",
			Check: analyticsAll(kafkaCapacity, analyticsRefuse("config"), analyticsRequire("gcpConfig.accessConfig.networkConfigs[].primarySubnet"))},
		{Method: "projects.locations.connectClusters.get", Kind: Get, Collection: "connectClusters"},
		{Method: "projects.locations.connectClusters.list", Kind: List, Collection: "connectClusters"},
		{Method: "projects.locations.connectClusters.patch", Kind: Patch, Collection: "connectClusters", Check: analyticsAll(kafkaCapacity, analyticsRefuse("config"))},
		{Method: "projects.locations.connectClusters.delete", Kind: Delete, Collection: "connectClusters"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Clusters, topics and Connect clusters are configuration metadata: no Kafka broker, bootstrap address, partition, message, consumer group, connector or network endpoint exists, and clusters report ACTIVE immediately.",
		"Capacity is validated against the documented minimums (3 vCPUs, 3 GiB, 1 to 8 GiB per vCPU) but nothing is provisioned; view=FULL returns no broker details or Kafka version.",
		"Topic partition counts cannot be changed by the simulator, Connect cluster kafkaCluster references are not resolved, and the reserved Connect worker config is refused.",
		"Delete topics before their cluster: deleting a cluster that still has topics fails with FAILED_PRECONDITION. ACLs, consumer groups, connectors and schema registries are unimplemented.",
	},
}

var datastream = &API{
	Key: "datastream.v1", Title: "Datastream", Issues: []int{397}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.datastream.v1.",
	Collections: []*Collection{
		{ID: "connectionProfiles", Schema: "ConnectionProfile", IDParam: "connectionProfileId", Limit: 50, Permission: "datastream.connectionProfiles",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"},
			Mutable: []string{"displayName", "labels", "mysqlProfile", "oracleProfile", "postgresqlProfile", "sqlServerProfile", "mongodbProfile", "salesforceProfile",
				"salesforceMarketingCloudProfile", "serviceNowProfile", "dataverseProfile", "workdayProfile", "spannerProfile", "gcsProfile", "bigqueryProfile",
				"forwardSshConnectivity", "privateConnectivity", "staticServiceIpConnectivity"}},
		{ID: "privateConnections", Schema: "PrivateConnection", IDParam: "privateConnectionId", Limit: 10, Permission: "datastream.privateConnections", Cascade: true,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "CREATED"}},
		{ID: "routes", Schema: "Route", Parent: "privateConnections", IDParam: "routeId", Limit: 20, Permission: "datastream.routes",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"}},
		{ID: "streams", Schema: "Stream", IDParam: "streamId", Limit: 50, Permission: "datastream.streams",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "NOT_STARTED"},
			Mutable:  []string{"displayName", "labels", "sourceConfig", "destinationConfig", "backfillAll", "backfillNone", "ruleSets"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.connectionProfiles.create", Kind: Create, Collection: "connectionProfiles", Check: datastreamSecrets},
		{Method: "projects.locations.connectionProfiles.get", Kind: Get, Collection: "connectionProfiles"},
		{Method: "projects.locations.connectionProfiles.list", Kind: List, Collection: "connectionProfiles"},
		{Method: "projects.locations.connectionProfiles.patch", Kind: Patch, Collection: "connectionProfiles", Check: datastreamSecrets},
		{Method: "projects.locations.connectionProfiles.delete", Kind: Delete, Collection: "connectionProfiles"},
		{Method: "projects.locations.privateConnections.create", Kind: Create, Collection: "privateConnections",
			Check: analyticsAll(privateConnectivity, analyticsRequire("vpcPeeringConfig?.vpc", "vpcPeeringConfig?.subnet", "pscInterfaceConfig?.networkAttachment"))},
		{Method: "projects.locations.privateConnections.get", Kind: Get, Collection: "privateConnections"},
		{Method: "projects.locations.privateConnections.list", Kind: List, Collection: "privateConnections"},
		{Method: "projects.locations.privateConnections.delete", Kind: Delete, Collection: "privateConnections"},
		{Method: "projects.locations.privateConnections.routes.create", Kind: Create, Collection: "routes"},
		{Method: "projects.locations.privateConnections.routes.get", Kind: Get, Collection: "routes"},
		{Method: "projects.locations.privateConnections.routes.list", Kind: List, Collection: "routes"},
		{Method: "projects.locations.privateConnections.routes.delete", Kind: Delete, Collection: "routes"},
		{Method: "projects.locations.streams.create", Kind: Create, Collection: "streams",
			Check: analyticsAll(streamNotStarted, analyticsRequire("sourceConfig.sourceConnectionProfile", "destinationConfig.destinationConnectionProfile"))},
		{Method: "projects.locations.streams.get", Kind: Get, Collection: "streams"},
		{Method: "projects.locations.streams.list", Kind: List, Collection: "streams"},
		{Method: "projects.locations.streams.patch", Kind: Patch, Collection: "streams", Check: streamNotStarted},
		{Method: "projects.locations.streams.delete", Kind: Delete, Collection: "streams"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Connection profiles, private connections, routes and streams are configuration metadata: no source or destination is contacted, connectivity is never validated (as with force=true), and no VPC peering, PSC interface or route is created.",
		"Passwords, private keys, client secrets, security tokens and raw secret values are refused and never stored; Secret Manager references are kept as given.",
		"Streams stay NOT_STARTED: a create or patch that sets any other state is refused, streams.run is unimplemented, and no data, stream object or backfill exists.",
		"Connection profile references in streams are not resolved; deleting a private connection with routes requires force=true.",
	},
}

var dataprocMetastore = &API{
	Key: "metastore.v1", Title: "Dataproc Metastore", Issues: []int{396}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.metastore.v1.",
	Collections: []*Collection{
		{ID: "services", Schema: "Service", IDParam: "serviceId", IDPattern: metastoreServiceID, Limit: 10, Permission: "metastore.services",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable: []string{"labels", "port", "tier", "maintenanceWindow", "hiveMetastoreConfig", "metadataIntegration", "scalingConfig", "scheduledBackup",
				"telemetryConfig", "releaseChannel"}},
		{ID: "backups", Schema: "Backup", Parent: "services", IDParam: "backupId", IDPattern: metastoreBackupID, Limit: 20, Permission: "metastore.backups",
			Generate: map[string]any{"createTime": "$now", "endTime": "$now", "state": "ACTIVE"}},
		{ID: "federations", Schema: "Federation", IDParam: "federationId", IDPattern: metastoreServiceID, Limit: 10, Permission: "metastore.federations",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable:  []string{"labels", "backendMetastores"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.services.create", Kind: Create, Collection: "services", Check: metastoreService},
		{Method: "projects.locations.services.get", Kind: Get, Collection: "services"},
		{Method: "projects.locations.services.list", Kind: List, Collection: "services"},
		{Method: "projects.locations.services.patch", Kind: Patch, Collection: "services", Check: metastoreService},
		{Method: "projects.locations.services.delete", Kind: Delete, Collection: "services"},
		{Method: "projects.locations.services.backups.create", Kind: Create, Collection: "backups"},
		{Method: "projects.locations.services.backups.get", Kind: Get, Collection: "backups"},
		{Method: "projects.locations.services.backups.list", Kind: List, Collection: "backups"},
		{Method: "projects.locations.services.backups.delete", Kind: Delete, Collection: "backups"},
		{Method: "projects.locations.federations.create", Kind: Create, Collection: "federations", Check: analyticsRefuse("tags")},
		{Method: "projects.locations.federations.get", Kind: Get, Collection: "federations"},
		{Method: "projects.locations.federations.list", Kind: List, Collection: "federations"},
		{Method: "projects.locations.federations.patch", Kind: Patch, Collection: "federations"},
		{Method: "projects.locations.federations.delete", Kind: Delete, Collection: "federations"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Services, backups and federations are configuration metadata: no Hive metastore, Thrift or gRPC endpoint, database, Spanner or MySQL instance exists, and they report ACTIVE immediately.",
		"Backups hold no Hive metadata and no service revision; restore, metadata import and export, migrations and scheduled backups never run and their methods are unimplemented.",
		"Resource Manager tags and deletion protection are refused rather than ignored; delete backups before their service, which otherwise fails with FAILED_PRECONDITION.",
	},
}

var dataform = &API{
	Key: "dataform.v1", Title: "Dataform", Issues: []int{394}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.dataform.v1.",
	Collections: []*Collection{
		{ID: "repositories", Schema: "Repository", IDParam: "repositoryId", Limit: 50, Permission: "dataform.repositories",
			Generate: map[string]any{"createTime": "$now"},
			Mutable: []string{"displayName", "labels", "gitRemoteSettings", "workspaceCompilationOverrides", "npmrcEnvironmentVariablesSecretVersion",
				"serviceAccount", "kmsKeyName"}},
		{ID: "workspaces", Schema: "Workspace", Parent: "repositories", IDParam: "workspaceId", Limit: 50, Permission: "dataform.workspaces",
			Generate: map[string]any{"createTime": "$now"}},
		{ID: "releaseConfigs", Schema: "ReleaseConfig", Parent: "repositories", IDParam: "releaseConfigId", Limit: 50, Permission: "dataform.releaseConfigs",
			Mutable: []string{"gitCommitish", "codeCompilationConfig", "cronSchedule", "timeZone", "disabled"}},
		{ID: "workflowConfigs", Schema: "WorkflowConfig", Parent: "repositories", IDParam: "workflowConfigId", Limit: 50, Permission: "dataform.workflowConfigs",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"},
			Mutable:  []string{"releaseConfig", "invocationConfig", "cronSchedule", "timeZone", "disabled"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.repositories.create", Kind: Create, Collection: "repositories", Check: analyticsRefuse("setAuthenticatedUserAdmin", "containingFolder")},
		{Method: "projects.locations.repositories.get", Kind: Get, Collection: "repositories"},
		{Method: "projects.locations.repositories.list", Kind: List, Collection: "repositories"},
		{Method: "projects.locations.repositories.patch", Kind: Patch, Collection: "repositories"},
		{Method: "projects.locations.repositories.delete", Kind: Delete, Collection: "repositories"},
		{Method: "projects.locations.repositories.workspaces.create", Kind: Create, Collection: "workspaces"},
		{Method: "projects.locations.repositories.workspaces.get", Kind: Get, Collection: "workspaces"},
		{Method: "projects.locations.repositories.workspaces.list", Kind: List, Collection: "workspaces"},
		{Method: "projects.locations.repositories.workspaces.delete", Kind: Delete, Collection: "workspaces"},
		{Method: "projects.locations.repositories.releaseConfigs.create", Kind: Create, Collection: "releaseConfigs", Check: analyticsRefuse("releaseCompilationResult")},
		{Method: "projects.locations.repositories.releaseConfigs.get", Kind: Get, Collection: "releaseConfigs"},
		{Method: "projects.locations.repositories.releaseConfigs.list", Kind: List, Collection: "releaseConfigs"},
		{Method: "projects.locations.repositories.releaseConfigs.patch", Kind: Patch, Collection: "releaseConfigs"},
		{Method: "projects.locations.repositories.releaseConfigs.delete", Kind: Delete, Collection: "releaseConfigs"},
		{Method: "projects.locations.repositories.workflowConfigs.create", Kind: Create, Collection: "workflowConfigs"},
		{Method: "projects.locations.repositories.workflowConfigs.get", Kind: Get, Collection: "workflowConfigs"},
		{Method: "projects.locations.repositories.workflowConfigs.list", Kind: List, Collection: "workflowConfigs"},
		{Method: "projects.locations.repositories.workflowConfigs.patch", Kind: Patch, Collection: "workflowConfigs"},
		{Method: "projects.locations.repositories.workflowConfigs.delete", Kind: Delete, Collection: "workflowConfigs"},
	},
	Bounds: []string{
		"Repositories, workspaces, release configs and workflow configs are configuration metadata: no Git repository, file, commit, remote, npm package or BigQuery job exists.",
		"Compilation and invocation never happen: cron schedules never fire, compilation results and workflow invocations are unimplemented, and release compilation results are refused.",
		"Folders and the input-only admin grant are refused; a repository with workspaces, release configs or workflow configs cannot be deleted, as in Dataform, even with force=true.",
	},
}

var dataFusion = &API{
	Key: "datafusion.v1", Title: "Cloud Data Fusion", Issues: []int{390}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.datafusion.v1.",
	Collections: []*Collection{
		{ID: "instances", Schema: "Instance", IDParam: "instanceId", IDPattern: dataFusionInstanceID, Limit: 10, Permission: "datafusion.instances", Cascade: true,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable: []string{"labels", "options", "enableStackdriverLogging", "enableStackdriverMonitoring", "enableRbac", "eventPublishConfig", "loggingConfig",
				"monitoringConfig", "maintenancePolicy", "dataplexDataLineageIntegrationEnabled", "displayName", "description"}},
		{ID: "dnsPeerings", Schema: "DnsPeering", Parent: "instances", IDParam: "dnsPeeringId", Limit: 20, Permission: "datafusion.dnsPeerings"},
	},
	Bindings: []Binding{
		{Method: "projects.locations.instances.create", Kind: Create, Collection: "instances", Check: analyticsRefuse("tags")},
		{Method: "projects.locations.instances.get", Kind: Get, Collection: "instances"},
		{Method: "projects.locations.instances.list", Kind: List, Collection: "instances"},
		{Method: "projects.locations.instances.patch", Kind: Patch, Collection: "instances"},
		{Method: "projects.locations.instances.delete", Kind: Delete, Collection: "instances"},
		{Method: "projects.locations.instances.dnsPeerings.create", Kind: Create, Collection: "dnsPeerings"},
		{Method: "projects.locations.instances.dnsPeerings.list", Kind: List, Collection: "dnsPeerings"},
		{Method: "projects.locations.instances.dnsPeerings.delete", Kind: Delete, Collection: "dnsPeerings"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Instances and DNS peerings are configuration metadata: no CDAP, pipeline, Dataproc cluster, bucket, service endpoint or DNS zone exists, and instances report ACTIVE immediately.",
		"Restart, version listing and IAM policy methods are unimplemented; Resource Manager tags are refused; deleting an instance with DNS peerings requires force=true.",
	},
}

var looker = &API{
	Key: "looker.v1", Title: "Looker (Google Cloud core)", Issues: []int{402}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.looker.v1.",
	Collections: []*Collection{
		{ID: "instances", Schema: "Instance", IDParam: "instanceId", Limit: 10, Permission: "looker.instances", Cascade: true,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable: []string{"adminSettings", "maintenanceWindow", "denyMaintenancePeriod", "userMetadata", "oauthConfig", "customDomain", "ingressIpAllowlistConfig",
				"releaseChannel", "geminiEnabled"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.instances.create", Kind: Create, Collection: "instances", Check: analyticsSecrets("clientSecret")},
		{Method: "projects.locations.instances.get", Kind: Get, Collection: "instances"},
		{Method: "projects.locations.instances.list", Kind: List, Collection: "instances"},
		{Method: "projects.locations.instances.patch", Kind: Patch, Collection: "instances", Check: analyticsSecrets("clientSecret")},
		{Method: "projects.locations.instances.delete", Kind: Delete, Collection: "instances"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Instances are configuration metadata: no Looker application, URI, IP address, database or custom domain certificate exists, and instances report ACTIVE immediately.",
		"OAuth client secrets are refused and never stored. Deletion is immediate rather than soft, so showDeleted lists nothing extra; undelete, restart, import, export, restore and backups are unimplemented.",
	},
}

var composer = &API{
	Key: "composer.v1", Title: "Managed Service for Apache Airflow", Issues: []int{403}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.orchestration.airflow.service.v1.",
	Collections: []*Collection{
		{ID: "environments", Schema: "Environment", IDField: "name", FullName: true, IDPattern: composerEnvironmentID, Limit: 10, Permission: "composer.environments",
			Generate: map[string]any{"uuid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "RUNNING"},
			Mutable:  []string{"labels"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.environments.create", Kind: Create, Collection: "environments", Check: analyticsRefuse("state")},
		{Method: "projects.locations.environments.get", Kind: Get, Collection: "environments"},
		{Method: "projects.locations.environments.list", Kind: List, Collection: "environments"},
		{Method: "projects.locations.environments.patch", Kind: Patch, Collection: "environments"},
		{Method: "projects.locations.environments.delete", Kind: Delete, Collection: "environments"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Environments are configuration metadata (formerly Cloud Composer): no Airflow scheduler, web server, DAG bucket, GKE cluster or database exists, they report RUNNING immediately and no airflowUri or dagGcsPrefix is assigned.",
		"The state field is server-assigned and refused in requests; only labels may be updated, and nested update mask paths such as config.softwareConfig.pypiPackages are refused.",
		"Airflow commands, snapshots, upgrade checks, database failover, web server restarts, workloads and user workload config maps and secrets are unimplemented.",
	},
}

var dataplex = &API{
	Key: "dataplex.v1", Title: "Knowledge Catalog", Issues: []int{400}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.dataplex.v1.",
	Collections: []*Collection{
		{ID: "lakes", Schema: "GoogleCloudDataplexV1Lake", Type: "Lake", IDParam: "lakeId", Limit: 20, Permission: "dataplex.lakes",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable:  []string{"displayName", "description", "labels", "metastore"}},
		{ID: "zones", Schema: "GoogleCloudDataplexV1Zone", Type: "Zone", Parent: "lakes", IDParam: "zoneId", Limit: 20, Permission: "dataplex.zones",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable:  []string{"displayName", "description", "labels", "discoverySpec"}},
		{ID: "assets", Schema: "GoogleCloudDataplexV1Asset", Type: "Asset", Parent: "zones", IDParam: "assetId", Limit: 50, Permission: "dataplex.assets",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "state": "ACTIVE"},
			Mutable:  []string{"displayName", "description", "labels", "discoverySpec"}},
		{ID: "entryGroups", Schema: "GoogleCloudDataplexV1EntryGroup", Type: "EntryGroup", IDParam: "entryGroupId", Limit: 50, Permission: "dataplex.entryGroups",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "etag": "$etag"},
			Mutable:  []string{"displayName", "description", "labels"}},
		{ID: "entryTypes", Schema: "GoogleCloudDataplexV1EntryType", Type: "EntryType", IDParam: "entryTypeId", Limit: 50, Permission: "dataplex.entryTypes",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "etag": "$etag"},
			Mutable:  []string{"displayName", "description", "labels", "typeAliases", "platform", "system", "requiredAspects", "authorization"}},
		{ID: "aspectTypes", Schema: "GoogleCloudDataplexV1AspectType", Type: "AspectType", IDParam: "aspectTypeId", Limit: 50, Permission: "dataplex.aspectTypes",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "etag": "$etag"},
			Mutable:  []string{"displayName", "description", "labels", "metadataTemplate", "authorization", "dataClassification"}},
		{ID: "glossaries", Schema: "GoogleCloudDataplexV1Glossary", Type: "Glossary", IDParam: "glossaryId", Limit: 20, Permission: "dataplex.glossaries",
			Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now", "etag": "$etag"},
			Mutable:  []string{"displayName", "description", "labels"}},
		{ID: "categories", Schema: "GoogleCloudDataplexV1GlossaryCategory", Type: "GlossaryCategory", Parent: "glossaries", IDParam: "categoryId", Limit: 100,
			Permission: "dataplex.glossaryCategories", Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now"},
			Mutable: []string{"displayName", "description", "labels"}},
		{ID: "terms", Schema: "GoogleCloudDataplexV1GlossaryTerm", Type: "GlossaryTerm", Parent: "glossaries", IDParam: "termId", Limit: 200,
			Permission: "dataplex.glossaryTerms", Generate: map[string]any{"uid": "$uid", "createTime": "$now", "updateTime": "$now"},
			Mutable: []string{"displayName", "description", "labels"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.lakes.create", Kind: Create, Collection: "lakes"},
		{Method: "projects.locations.lakes.get", Kind: Get, Collection: "lakes"},
		{Method: "projects.locations.lakes.list", Kind: List, Collection: "lakes"},
		{Method: "projects.locations.lakes.patch", Kind: Patch, Collection: "lakes"},
		{Method: "projects.locations.lakes.delete", Kind: Delete, Collection: "lakes"},
		{Method: "projects.locations.lakes.zones.create", Kind: Create, Collection: "zones", Check: analyticsRequire("resourceSpec.locationType", "discoverySpec?.enabled")},
		{Method: "projects.locations.lakes.zones.get", Kind: Get, Collection: "zones"},
		{Method: "projects.locations.lakes.zones.list", Kind: List, Collection: "zones"},
		{Method: "projects.locations.lakes.zones.patch", Kind: Patch, Collection: "zones", Check: analyticsRequire("discoverySpec?.enabled")},
		{Method: "projects.locations.lakes.zones.delete", Kind: Delete, Collection: "zones"},
		{Method: "projects.locations.lakes.zones.assets.create", Kind: Create, Collection: "assets", Check: analyticsRequire("resourceSpec.type")},
		{Method: "projects.locations.lakes.zones.assets.get", Kind: Get, Collection: "assets"},
		{Method: "projects.locations.lakes.zones.assets.list", Kind: List, Collection: "assets"},
		{Method: "projects.locations.lakes.zones.assets.patch", Kind: Patch, Collection: "assets"},
		{Method: "projects.locations.lakes.zones.assets.delete", Kind: Delete, Collection: "assets"},
		{Method: "projects.locations.entryGroups.create", Kind: Create, Collection: "entryGroups"},
		{Method: "projects.locations.entryGroups.get", Kind: Get, Collection: "entryGroups"},
		{Method: "projects.locations.entryGroups.list", Kind: List, Collection: "entryGroups"},
		{Method: "projects.locations.entryGroups.patch", Kind: Patch, Collection: "entryGroups"},
		{Method: "projects.locations.entryGroups.delete", Kind: Delete, Collection: "entryGroups"},
		{Method: "projects.locations.entryTypes.create", Kind: Create, Collection: "entryTypes", Check: analyticsRequire("requiredAspects[]?.type")},
		{Method: "projects.locations.entryTypes.get", Kind: Get, Collection: "entryTypes"},
		{Method: "projects.locations.entryTypes.list", Kind: List, Collection: "entryTypes"},
		{Method: "projects.locations.entryTypes.patch", Kind: Patch, Collection: "entryTypes", Check: analyticsRequire("requiredAspects[]?.type")},
		{Method: "projects.locations.entryTypes.delete", Kind: Delete, Collection: "entryTypes"},
		{Method: "projects.locations.aspectTypes.create", Kind: Create, Collection: "aspectTypes", Check: analyticsRequire("metadataTemplate.name", "metadataTemplate.type")},
		{Method: "projects.locations.aspectTypes.get", Kind: Get, Collection: "aspectTypes"},
		{Method: "projects.locations.aspectTypes.list", Kind: List, Collection: "aspectTypes"},
		{Method: "projects.locations.aspectTypes.patch", Kind: Patch, Collection: "aspectTypes", Check: analyticsRequire("metadataTemplate?.name", "metadataTemplate?.type")},
		{Method: "projects.locations.aspectTypes.delete", Kind: Delete, Collection: "aspectTypes"},
		{Method: "projects.locations.glossaries.create", Kind: Create, Collection: "glossaries"},
		{Method: "projects.locations.glossaries.get", Kind: Get, Collection: "glossaries"},
		{Method: "projects.locations.glossaries.list", Kind: List, Collection: "glossaries"},
		{Method: "projects.locations.glossaries.patch", Kind: Patch, Collection: "glossaries"},
		{Method: "projects.locations.glossaries.delete", Kind: Delete, Collection: "glossaries"},
		{Method: "projects.locations.glossaries.categories.create", Kind: Create, Collection: "categories", Check: glossaryParent},
		{Method: "projects.locations.glossaries.categories.get", Kind: Get, Collection: "categories"},
		{Method: "projects.locations.glossaries.categories.list", Kind: List, Collection: "categories"},
		{Method: "projects.locations.glossaries.categories.patch", Kind: Patch, Collection: "categories"},
		{Method: "projects.locations.glossaries.categories.delete", Kind: Delete, Collection: "categories"},
		{Method: "projects.locations.glossaries.terms.create", Kind: Create, Collection: "terms", Check: glossaryParent},
		{Method: "projects.locations.glossaries.terms.get", Kind: Get, Collection: "terms"},
		{Method: "projects.locations.glossaries.terms.list", Kind: List, Collection: "terms"},
		{Method: "projects.locations.glossaries.terms.patch", Kind: Patch, Collection: "terms"},
		{Method: "projects.locations.glossaries.terms.delete", Kind: Delete, Collection: "terms"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Lakes, zones, assets, entry groups, entry types, aspect types and business glossaries are configuration metadata (formerly Dataplex Universal Catalog): no bucket, dataset, metastore or entry is read, created or published, and lakes, zones and assets report ACTIVE immediately.",
		"Discovery, data scans, tasks and metadata jobs never run; asset resources, lake metastores, required aspect types and glossary parents are not resolved, and zone IDs are unique only within their lake.",
		"Delete zones before their lake, assets before their zone, and categories and terms before their glossary, as Dataplex requires. Entries, entry links, data products and taxonomies are unimplemented.",
	},
}

var bigLake = &API{
	Key: "biglake.v1", Title: "Lakehouse", Issues: []int{401}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.bigquery.biglake.v1.",
	Collections: []*Collection{
		{ID: "catalogs", Schema: "Catalog", IDParam: "catalogId", IDPattern: bigLakeID, Limit: 20, Permission: "biglake.catalogs",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"}},
		{ID: "databases", Schema: "Database", Parent: "catalogs", IDParam: "databaseId", IDPattern: bigLakeID, Limit: 50, Permission: "biglake.databases",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"},
			Mutable:  []string{"hiveOptions"}},
		{ID: "tables", Schema: "Table", Parent: "databases", IDParam: "tableId", IDPattern: bigLakeID, Limit: 100, Permission: "biglake.tables",
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "etag": "$etag"},
			Mutable:  []string{"hiveOptions"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.catalogs.create", Kind: Create, Collection: "catalogs"},
		{Method: "projects.locations.catalogs.get", Kind: Get, Collection: "catalogs"},
		{Method: "projects.locations.catalogs.list", Kind: List, Collection: "catalogs"},
		{Method: "projects.locations.catalogs.delete", Kind: Delete, Collection: "catalogs"},
		{Method: "projects.locations.catalogs.databases.create", Kind: Create, Collection: "databases"},
		{Method: "projects.locations.catalogs.databases.get", Kind: Get, Collection: "databases"},
		{Method: "projects.locations.catalogs.databases.list", Kind: List, Collection: "databases"},
		{Method: "projects.locations.catalogs.databases.patch", Kind: Patch, Collection: "databases"},
		{Method: "projects.locations.catalogs.databases.delete", Kind: Delete, Collection: "databases"},
		{Method: "projects.locations.catalogs.databases.tables.create", Kind: Create, Collection: "tables"},
		{Method: "projects.locations.catalogs.databases.tables.get", Kind: Get, Collection: "tables"},
		{Method: "projects.locations.catalogs.databases.tables.list", Kind: List, Collection: "tables"},
		{Method: "projects.locations.catalogs.databases.tables.patch", Kind: Patch, Collection: "tables"},
		{Method: "projects.locations.catalogs.databases.tables.delete", Kind: Delete, Collection: "tables"},
	},
	Bounds: []string{
		"Catalogs, databases and tables are Hive-style metastore metadata (formerly BigLake): no Cloud Storage object, Iceberg file, BigQuery table or query engine is read or written.",
		"Deletes are immediate and return the deleted resource without deleteTime or expireTime; delete tables before their database and databases before their catalog.",
		"tables.list returns full tables for every view; rename, the Iceberg REST catalog and IAM policy methods are unimplemented.",
	},
}

var dataproc = &API{
	Key: "dataproc.v1", Title: "Managed Service for Apache Spark", Issues: []int{404}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.dataproc.v1.",
	Collections: []*Collection{
		{ID: "autoscalingPolicies", Schema: "AutoscalingPolicy", IDField: "id", IDPattern: dataprocPolicyID, Limit: 50, Permission: "dataproc.autoscalingPolicies"},
		{ID: "sessionTemplates", Schema: "SessionTemplate", IDField: "name", FullName: true, Limit: 50, Permission: "dataproc.sessionTemplates",
			Generate: map[string]any{"uuid": "$uid", "createTime": "$now", "updateTime": "$now"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.autoscalingPolicies.create", Kind: Create, Collection: "autoscalingPolicies", Check: dataprocAutoscaling},
		{Method: "projects.locations.autoscalingPolicies.get", Kind: Get, Collection: "autoscalingPolicies"},
		{Method: "projects.locations.autoscalingPolicies.list", Kind: List, Collection: "autoscalingPolicies"},
		{Method: "projects.locations.autoscalingPolicies.update", Kind: Replace, Collection: "autoscalingPolicies", Check: dataprocAutoscaling},
		{Method: "projects.locations.autoscalingPolicies.delete", Kind: Delete, Collection: "autoscalingPolicies"},
		{Method: "projects.locations.sessionTemplates.create", Kind: Create, Collection: "sessionTemplates"},
		{Method: "projects.locations.sessionTemplates.get", Kind: Get, Collection: "sessionTemplates"},
		{Method: "projects.locations.sessionTemplates.list", Kind: List, Collection: "sessionTemplates"},
		{Method: "projects.locations.sessionTemplates.patch", Kind: Replace, Collection: "sessionTemplates"},
		{Method: "projects.locations.sessionTemplates.delete", Kind: Delete, Collection: "sessionTemplates"},
	},
	Bounds: []string{
		"Autoscaling policies and session templates under projects.locations are configuration metadata (formerly Dataproc): no cluster, batch, session, Spark application or autoscaler runs, and no policy is attached to anything.",
		"Updates replace the whole policy or template, as the official methods do, and cannot change its ID.",
		"Clusters, jobs, batches, sessions and workflow templates run or describe workloads and are unimplemented, as are the projects.regions paths.",
	},
}

// kafkaCapacity validates capacityConfig against the documented minimums.
func kafkaCapacity(body map[string]any) error {
	raw, ok := body["capacityConfig"]
	if !ok {
		return nil
	}
	config, _ := raw.(map[string]any)
	vcpus, err1 := strconv.ParseInt(fmt.Sprint(config["vcpuCount"]), 10, 64)
	memory, err2 := strconv.ParseInt(fmt.Sprint(config["memoryBytes"]), 10, 64)
	if err1 != nil || err2 != nil {
		return invalid("Fields 'capacityConfig.vcpuCount' and 'capacityConfig.memoryBytes' are required.")
	}
	const gib = 1 << 30
	if vcpus < 3 || memory < 3*gib || memory < vcpus*gib || memory > 8*vcpus*gib {
		return invalid("Capacity needs at least 3 vCPUs and 3 GiB with 1 to 8 GiB of memory per vCPU.")
	}
	return nil
}

// kafkaTopic requires a positive partition count and replication factor.
func kafkaTopic(body map[string]any) error {
	for _, field := range []string{"partitionCount", "replicationFactor"} {
		if n, _ := body[field].(int64); n < 1 {
			return invalid("Field '%s' must be positive.", field)
		}
	}
	return nil
}

// datastreamSecrets refuses inline credentials in connection profiles.
var datastreamSecrets = analyticsSecrets("password", "privateKey", "clientKey", "clientSecret", "securityToken", "rawValue")

// privateConnectivity requires exactly one of VPC peering or a PSC interface.
func privateConnectivity(body map[string]any) error {
	_, peering := body["vpcPeeringConfig"]
	_, psc := body["pscInterfaceConfig"]
	if peering == psc {
		return invalid("Exactly one of 'vpcPeeringConfig' and 'pscInterfaceConfig' is required.")
	}
	return nil
}

// streamNotStarted refuses any state change: the simulator never starts streams.
func streamNotStarted(body map[string]any) error {
	if state, ok := body["state"]; ok && state != "NOT_STARTED" {
		return invalid("The simulator never starts streams; state must be NOT_STARTED.")
	}
	return nil
}

var glossaryParentName = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/glossaries/[^/]+(/categories/[^/]+)?$`)

// glossaryParent requires the documented glossary or category name form.
func glossaryParent(body map[string]any) error {
	if parent, _ := body["parent"].(string); !glossaryParentName.MatchString(parent) {
		return invalid("Field 'parent' must name a glossary or glossary category.")
	}
	return nil
}

// metastoreService refuses tag bindings and deletion protection, whose
// effects the simulator does not enforce.
func metastoreService(body map[string]any) error {
	if body["deletionProtection"] == true {
		return invalid("Deletion protection is not enforced by the simulator.")
	}
	return analyticsRefuse("tags")(body)
}

// dataprocAutoscaling requires the nested fields the official schema marks required.
func dataprocAutoscaling(body map[string]any) error {
	return analyticsRequire("workerConfig.maxInstances", "secondaryWorkerConfig?.maxInstances",
		"basicAlgorithm?.yarnConfig?.gracefulDecommissionTimeout", "basicAlgorithm?.yarnConfig?.scaleUpFactor", "basicAlgorithm?.yarnConfig?.scaleDownFactor",
		"basicAlgorithm?.sparkStandaloneConfig?.gracefulDecommissionTimeout", "basicAlgorithm?.sparkStandaloneConfig?.scaleUpFactor",
		"basicAlgorithm?.sparkStandaloneConfig?.scaleDownFactor")(body)
}

// analyticsAll runs checks in order.
func analyticsAll(checks ...func(map[string]any) error) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, check := range checks {
			if err := check(body); err != nil {
				return err
			}
		}
		return nil
	}
}

// analyticsRefuse refuses top-level fields whose effects are not simulated.
func analyticsRefuse(fields ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, field := range fields {
			if _, ok := body[field]; ok {
				return invalid("Field '%s' is not supported by the simulator.", field)
			}
		}
		return nil
	}
}

// analyticsSecrets refuses string values of the named fields anywhere in a
// body: the simulator stores configuration metadata, never credentials.
func analyticsSecrets(names ...string) func(map[string]any) error {
	refused := map[string]bool{}
	for _, name := range names {
		refused[name] = true
	}
	var walk func(value any, at string) error
	walk = func(value any, at string) error {
		switch v := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if _, text := v[key].(string); text && refused[key] {
					return invalid("Field '%s' carries a secret; the simulator never stores credentials.", joinPath(at, key))
				}
				if err := walk(v[key], joinPath(at, key)); err != nil {
					return err
				}
			}
		case []any:
			for i, item := range v {
				if err := walk(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return func(body map[string]any) error { return walk(body, "") }
}

// analyticsRequire requires nested fields the official schema marks
// "Required." but the engine checks only at the top level. Paths are dotted;
// a segment ending in "?" is optional (the rest applies only when it is
// present) and "[]" applies the rest to every element of a non-empty list.
func analyticsRequire(paths ...string) func(map[string]any) error {
	return func(body map[string]any) error {
		for _, path := range paths {
			if !analyticsPresent(body, strings.Split(path, ".")) {
				return invalid("Field '%s' is required.", strings.NewReplacer("?", "", "[]", "").Replace(path))
			}
		}
		return nil
	}
}

func analyticsPresent(value any, path []string) bool {
	if len(path) == 0 {
		return value != nil && value != ""
	}
	object, _ := value.(map[string]any)
	segment := path[0]
	optional := strings.HasSuffix(segment, "?")
	segment = strings.TrimSuffix(segment, "?")
	list := strings.HasSuffix(segment, "[]")
	segment = strings.TrimSuffix(segment, "[]")
	child, ok := object[segment]
	if !ok {
		return optional
	}
	if !list {
		return analyticsPresent(child, path[1:])
	}
	items, _ := child.([]any)
	if len(items) == 0 {
		return optional
	}
	for _, item := range items {
		if !analyticsPresent(item, path[1:]) {
			return false
		}
	}
	return true
}
