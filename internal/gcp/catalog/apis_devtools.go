package catalog

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Reviewed bounded specifications for Google Cloud developer tools and the
// compute-adjacent configuration APIs grouped with them.

// devtoolsOutputs adds creation and update timestamps to the
// collection-specific output fields.
func devtoolsOutputs(extra map[string]any) map[string]any {
	out := map[string]any{"createTime": "$now", "updateTime": "$now"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

var artifactRegistry = &API{
	Key: "artifactregistry.v1", Title: "Artifact Registry", Issues: []int{379}, Scope: "location",
	TypeURL: "type.googleapis.com/google.devtools.artifactregistry.v1.",
	Collections: []*Collection{
		{ID: "repositories", Schema: "Repository", IDParam: "repositoryId", Limit: 50, Permission: "artifactregistry.repositories",
			Generate: devtoolsOutputs(map[string]any{"sizeBytes": "0", "mode": "STANDARD_REPOSITORY"}),
			Mutable:  []string{"description", "labels", "cleanupPolicies", "cleanupPolicyDryRun", "dockerConfig", "mavenConfig", "vulnerabilityScanningConfig", "platformLogsConfig"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.repositories.create", Kind: Create, Collection: "repositories", Check: repositoryMode},
		{Method: "projects.locations.repositories.get", Kind: Get, Collection: "repositories"},
		{Method: "projects.locations.repositories.list", Kind: List, Collection: "repositories"},
		{Method: "projects.locations.repositories.patch", Kind: Patch, Collection: "repositories"},
		{Method: "projects.locations.repositories.delete", Kind: Delete, Collection: "repositories"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
	},
	Bounds: []string{
		"Repositories are configuration metadata: no package, image, file, version or tag is stored, no registry endpoint (registryUri) is served and sizeBytes stays 0.",
		"Remote and virtual repositories never contact an upstream. Upstream credentials must be Secret Manager version names, which are stored as references and never resolved.",
		"Cleanup policies and vulnerability scanning settings are stored but never run. Mode, format, KMS key, remote and virtual configuration cannot be updated. Long-running operations complete synchronously.",
	},
}

var cloudBuild = &API{
	Key: "cloudbuild.v1", Title: "Cloud Build", Issues: []int{381}, Scope: "location",
	TypeURL: "type.googleapis.com/google.devtools.cloudbuild.v1.",
	Collections: []*Collection{
		{ID: "triggers", Schema: "BuildTrigger", IDField: "name", ShortName: true, IDPattern: regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?$`),
			Limit: 100, Permission: "cloudbuild.builds",
			Generate: map[string]any{"id": "$id", "resourceName": "$name", "createTime": "$now"},
			Mutable: []string{"description", "tags", "disabled", "substitutions", "filename", "filter", "ignoredFiles", "includedFiles", "build", "autodetect",
				"triggerTemplate", "github", "pubsubConfig", "webhookConfig", "sourceToBuild", "gitFileSource", "repositoryEventConfig", "developerConnectEventConfig",
				"bitbucketServerTriggerConfig", "gitlabEnterpriseEventsConfig", "approvalConfig", "serviceAccount", "includeBuildLogs", "eventType"}},
		{ID: "workerPools", Schema: "WorkerPool", IDParam: "workerPoolId", Limit: 20, Permission: "cloudbuild.workerpools",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "state": "RUNNING"}),
			Mutable:  []string{"displayName", "annotations", "privatePoolV1Config"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.triggers.create", Kind: Create, Collection: "triggers", Check: buildTrigger},
		{Method: "projects.locations.triggers.get", Kind: Get, Collection: "triggers"},
		{Method: "projects.locations.triggers.list", Kind: List, Collection: "triggers"},
		{Method: "projects.locations.triggers.patch", Kind: Patch, Collection: "triggers", Check: buildTrigger},
		{Method: "projects.locations.triggers.delete", Kind: Delete, Collection: "triggers"},
		{Method: "projects.locations.workerPools.create", Kind: Create, Collection: "workerPools"},
		{Method: "projects.locations.workerPools.get", Kind: Get, Collection: "workerPools"},
		{Method: "projects.locations.workerPools.list", Kind: List, Collection: "workerPools"},
		{Method: "projects.locations.workerPools.patch", Kind: Patch, Collection: "workerPools"},
		{Method: "projects.locations.workerPools.delete", Kind: Delete, Collection: "workerPools", Refuse: []string{"allowMissing"}},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
	},
	Bounds: []string{
		"Build triggers and private worker pools are configuration metadata: no build runs, no webhook, Pub/Sub or repository event is received, no source repository is contacted and no worker VM is provisioned; worker pools report RUNNING immediately.",
		"A trigger's ID equals its name, so the resource name, id and name address the same trigger. The legacy projectId and triggerId query parameters are not consulted; the resource name decides.",
		"Secret Manager references are stored as names and never resolved; inline KMS-encrypted secrets (build.secrets, build.availableSecrets.inline) are refused. Cloud Build v2 connections are not served.",
	},
}

var cloudDeploy = &API{
	Key: "clouddeploy.v1", Title: "Cloud Deploy", Issues: []int{382}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.deploy.v1.",
	Collections: []*Collection{
		{ID: "deliveryPipelines", Schema: "DeliveryPipeline", IDParam: "deliveryPipelineId", Limit: 50, Permission: "clouddeploy.deliveryPipelines",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag"}),
			Mutable:  []string{"description", "annotations", "labels", "serialPipeline", "suspended"}},
		{ID: "targets", Schema: "Target", IDParam: "targetId", Limit: 50, Permission: "clouddeploy.targets",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "targetId": "$id"}),
			Mutable: []string{"description", "annotations", "labels", "requireApproval", "gke", "anthosCluster", "run", "multiTarget", "customTarget",
				"executionConfigs", "deployParameters", "associatedEntities"}},
		{ID: "customTargetTypes", Schema: "CustomTargetType", IDParam: "customTargetTypeId", Limit: 50, Permission: "clouddeploy.customTargetTypes",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "customTargetTypeId": "$id"}),
			Mutable:  []string{"description", "annotations", "labels", "customActions", "tasks"}},
		{ID: "deployPolicies", Schema: "DeployPolicy", IDParam: "deployPolicyId", Limit: 50, Permission: "clouddeploy.deployPolicies",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag"}),
			Mutable:  []string{"description", "annotations", "labels", "suspended", "selectors", "rules"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.deliveryPipelines.create", Kind: Create, Collection: "deliveryPipelines"},
		{Method: "projects.locations.deliveryPipelines.get", Kind: Get, Collection: "deliveryPipelines"},
		{Method: "projects.locations.deliveryPipelines.list", Kind: List, Collection: "deliveryPipelines"},
		{Method: "projects.locations.deliveryPipelines.patch", Kind: Patch, Collection: "deliveryPipelines"},
		{Method: "projects.locations.deliveryPipelines.delete", Kind: Delete, Collection: "deliveryPipelines", Refuse: []string{"allowMissing"}},
		{Method: "projects.locations.targets.create", Kind: Create, Collection: "targets"},
		{Method: "projects.locations.targets.get", Kind: Get, Collection: "targets"},
		{Method: "projects.locations.targets.list", Kind: List, Collection: "targets"},
		{Method: "projects.locations.targets.patch", Kind: Patch, Collection: "targets"},
		{Method: "projects.locations.targets.delete", Kind: Delete, Collection: "targets", Refuse: []string{"allowMissing"}},
		{Method: "projects.locations.customTargetTypes.create", Kind: Create, Collection: "customTargetTypes"},
		{Method: "projects.locations.customTargetTypes.get", Kind: Get, Collection: "customTargetTypes"},
		{Method: "projects.locations.customTargetTypes.list", Kind: List, Collection: "customTargetTypes"},
		{Method: "projects.locations.customTargetTypes.patch", Kind: Patch, Collection: "customTargetTypes"},
		{Method: "projects.locations.customTargetTypes.delete", Kind: Delete, Collection: "customTargetTypes", Refuse: []string{"allowMissing"}},
		{Method: "projects.locations.deployPolicies.create", Kind: Create, Collection: "deployPolicies"},
		{Method: "projects.locations.deployPolicies.get", Kind: Get, Collection: "deployPolicies"},
		{Method: "projects.locations.deployPolicies.list", Kind: List, Collection: "deployPolicies"},
		{Method: "projects.locations.deployPolicies.patch", Kind: Patch, Collection: "deployPolicies"},
		{Method: "projects.locations.deployPolicies.delete", Kind: Delete, Collection: "deployPolicies", Refuse: []string{"allowMissing"}},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Delivery pipelines, targets, custom target types and deploy policies are configuration metadata: no release, rollout, render, deploy job, verification or automation runs, and no GKE, Cloud Run, Anthos or custom target is contacted.",
		"Pipeline conditions are not computed, and pipeline, target and policy references are stored as given. allowMissing is refused.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var workstations = &API{
	Key: "workstations.v1", Title: "Cloud Workstations", Issues: []int{384}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.workstations.v1.",
	Collections: []*Collection{
		{ID: "workstationClusters", Schema: "WorkstationCluster", IDParam: "workstationClusterId", Limit: 10, Permission: "workstations.workstationClusters", Cascade: true,
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "reconciling": false, "degraded": false}),
			Mutable:  []string{"displayName", "annotations", "labels", "workstationLaunchUrl", "workstationAuthorizationUrl", "gatewayConfig"}},
		{ID: "workstationConfigs", Schema: "WorkstationConfig", Parent: "workstationClusters", IDParam: "workstationConfigId", Limit: 20, Permission: "workstations.workstationConfigs", Cascade: true,
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "reconciling": false, "degraded": false}),
			Mutable: []string{"displayName", "annotations", "labels", "idleTimeout", "runningTimeout", "host", "persistentDirectories", "ephemeralDirectories",
				"container", "readinessChecks", "allowedPorts", "disableTcpConnections", "enableAuditAgent", "maxUsableWorkstations", "grantWorkstationAdminRoleOnCreate"}},
		{ID: "workstations", Schema: "Workstation", Parent: "workstationConfigs", IDParam: "workstationId", Limit: 50, Permission: "workstations.workstations",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "reconciling": false, "state": "STATE_STOPPED"}),
			Mutable:  []string{"displayName", "annotations", "labels", "env"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.workstationClusters.create", Kind: Create, Collection: "workstationClusters"},
		{Method: "projects.locations.workstationClusters.get", Kind: Get, Collection: "workstationClusters"},
		{Method: "projects.locations.workstationClusters.list", Kind: List, Collection: "workstationClusters"},
		{Method: "projects.locations.workstationClusters.patch", Kind: Patch, Collection: "workstationClusters"},
		{Method: "projects.locations.workstationClusters.delete", Kind: Delete, Collection: "workstationClusters"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.create", Kind: Create, Collection: "workstationConfigs"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.get", Kind: Get, Collection: "workstationConfigs"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.list", Kind: List, Collection: "workstationConfigs"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.patch", Kind: Patch, Collection: "workstationConfigs"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.delete", Kind: Delete, Collection: "workstationConfigs"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.workstations.create", Kind: Create, Collection: "workstations"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.workstations.get", Kind: Get, Collection: "workstations"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.workstations.list", Kind: List, Collection: "workstations"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.workstations.patch", Kind: Patch, Collection: "workstations"},
		{Method: "projects.locations.workstationClusters.workstationConfigs.workstations.delete", Kind: Delete, Collection: "workstations"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Workstation clusters, configurations and workstations are configuration metadata: no control plane, gateway, VM, disk or container is created, and workstations stay STATE_STOPPED; start, stop and access tokens are not implemented.",
		"Control plane IP addresses, workstation hosts and usable-workstation listings are not reported. Deleting a cluster or configuration that has children requires force=true.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var developerConnect = &API{
	Key: "developerconnect.v1", Title: "Developer Connect", Issues: []int{385}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.developerconnect.v1.",
	Collections: []*Collection{
		{ID: "connections", Schema: "Connection", IDParam: "connectionId", Limit: 20, Permission: "developerconnect.connections",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "reconciling": false}),
			Mutable:  []string{"labels", "annotations", "disabled"}},
		{ID: "gitRepositoryLinks", Schema: "GitRepositoryLink", Parent: "connections", IDParam: "gitRepositoryLinkId", Limit: 100, Permission: "developerconnect.gitRepositoryLinks",
			IDPattern: regexp.MustCompile(`^[A-Za-z0-9._~%!$&'()*+,;=@-]{1,63}$`),
			Generate:  devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "reconciling": false})},
		{ID: "accountConnectors", Schema: "AccountConnector", IDParam: "accountConnectorId", Limit: 20, Permission: "developerconnect.accountConnectors",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag"}),
			Mutable:  []string{"labels", "annotations"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.connections.create", Kind: Create, Collection: "connections", Check: gitConnection},
		{Method: "projects.locations.connections.get", Kind: Get, Collection: "connections"},
		{Method: "projects.locations.connections.list", Kind: List, Collection: "connections"},
		{Method: "projects.locations.connections.patch", Kind: Patch, Collection: "connections"},
		{Method: "projects.locations.connections.delete", Kind: Delete, Collection: "connections"},
		{Method: "projects.locations.connections.gitRepositoryLinks.create", Kind: Create, Collection: "gitRepositoryLinks"},
		{Method: "projects.locations.connections.gitRepositoryLinks.get", Kind: Get, Collection: "gitRepositoryLinks"},
		{Method: "projects.locations.connections.gitRepositoryLinks.list", Kind: List, Collection: "gitRepositoryLinks"},
		{Method: "projects.locations.connections.gitRepositoryLinks.delete", Kind: Delete, Collection: "gitRepositoryLinks"},
		{Method: "projects.locations.accountConnectors.create", Kind: Create, Collection: "accountConnectors", Check: accountConnector},
		{Method: "projects.locations.accountConnectors.get", Kind: Get, Collection: "accountConnectors"},
		{Method: "projects.locations.accountConnectors.list", Kind: List, Collection: "accountConnectors"},
		{Method: "projects.locations.accountConnectors.patch", Kind: Patch, Collection: "accountConnectors"},
		{Method: "projects.locations.accountConnectors.delete", Kind: Delete, Collection: "accountConnectors"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Connections, Git repository links and account connectors are configuration metadata: no Git provider, GitHub App, OAuth flow, webhook or Git proxy is contacted or created, and installation state, webhook IDs and proxy URIs are not reported.",
		"Credentials must be Secret Manager version names, which are stored as references and never resolved. Custom OAuth client secrets and enabling a proxy are refused, and only labels, annotations and disabled can be updated.",
		"Deleting a connection that still has Git repository links fails with FAILED_PRECONDITION. Long-running operations complete synchronously.",
	},
}

var secureSourceManager = &API{
	Key: "securesourcemanager.v1", Title: "Secure Source Manager", Issues: []int{388}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.securesourcemanager.v1.",
	Collections: []*Collection{
		{ID: "instances", Schema: "Instance", IDParam: "instanceId", Limit: 5, Permission: "securesourcemanager.instances",
			Generate: devtoolsOutputs(map[string]any{"state": "ACTIVE"})},
		{ID: "repositories", Schema: "Repository", IDParam: "repositoryId", Limit: 100, Permission: "securesourcemanager.repositories",
			IDPattern: regexp.MustCompile(`^[a-z][a-z0-9-]{2,61}[a-z0-9]$`),
			Generate:  devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag"}),
			Mutable:   []string{"description"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.instances.create", Kind: Create, Collection: "instances"},
		{Method: "projects.locations.instances.get", Kind: Get, Collection: "instances"},
		{Method: "projects.locations.instances.list", Kind: List, Collection: "instances"},
		{Method: "projects.locations.instances.delete", Kind: Delete, Collection: "instances"},
		{Method: "projects.locations.repositories.create", Kind: Create, Collection: "repositories", Check: noInitialContent},
		{Method: "projects.locations.repositories.get", Kind: Get, Collection: "repositories"},
		{Method: "projects.locations.repositories.list", Kind: List, Collection: "repositories", Refuse: []string{"instance"}},
		{Method: "projects.locations.repositories.patch", Kind: Patch, Collection: "repositories"},
		{Method: "projects.locations.repositories.delete", Kind: Delete, Collection: "repositories", Refuse: []string{"allowMissing"}},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Instances and repositories are configuration metadata: no Git server, web interface, host name or repository URI exists, and instances report ACTIVE immediately.",
		"Repository initialConfig is refused because no Git content is stored, and the instance field is stored as given. Listing repositories by instance and allowMissing deletes are refused.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var appHosting = &API{
	Key: "firebaseapphosting.v1", Title: "Firebase App Hosting", Issues: []int{357}, Scope: "location",
	TypeURL: "type.googleapis.com/google.firebase.apphosting.v1.",
	Collections: []*Collection{
		{ID: "backends", Schema: "Backend", IDParam: "backendId", Limit: 20, Permission: "firebaseapphosting.backends",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "reconciling": false}),
			Mutable:  []string{"displayName", "labels", "annotations", "codebase", "serviceAccount", "appId", "environment", "mode", "requestLogsDisabled"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.backends.create", Kind: Create, Collection: "backends"},
		{Method: "projects.locations.backends.get", Kind: Get, Collection: "backends"},
		{Method: "projects.locations.backends.list", Kind: List, Collection: "backends", Refuse: []string{"showDeleted"}},
		{Method: "projects.locations.backends.patch", Kind: Patch, Collection: "backends"},
		{Method: "projects.locations.backends.delete", Kind: Delete, Collection: "backends"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Backends are configuration metadata: no build, rollout, Cloud Run service, domain or traffic split is created, no URI is served and no managed resources are reported.",
		"Connected repositories are stored as Developer Connect resource names and never watched. Deleted backends are removed at once, so showDeleted is refused.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var blockchainNodeEngine = &API{
	Key: "blockchainnodeengine.v1", Title: "Blockchain Node Engine", Issues: []int{320}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.blockchainnodeengine.v1.",
	Collections: []*Collection{
		{ID: "blockchainNodes", Schema: "BlockchainNode", IDParam: "blockchainNodeId", Limit: 10, Permission: "blockchainnodeengine.blockchainnodes",
			Generate: devtoolsOutputs(map[string]any{"state": "RUNNING"}),
			Mutable:  []string{"labels"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.blockchainNodes.create", Kind: Create, Collection: "blockchainNodes"},
		{Method: "projects.locations.blockchainNodes.get", Kind: Get, Collection: "blockchainNodes"},
		{Method: "projects.locations.blockchainNodes.list", Kind: List, Collection: "blockchainNodes"},
		{Method: "projects.locations.blockchainNodes.patch", Kind: Patch, Collection: "blockchainNodes"},
		{Method: "projects.locations.blockchainNodes.delete", Kind: Delete, Collection: "blockchainNodes"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Blockchain nodes are configuration metadata: no Ethereum execution, consensus or validator client, storage or endpoint runs; connection information is not reported and nodes report RUNNING immediately.",
		"Only labels can be updated. Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var clusterDirector = &API{
	Key: "hypercomputecluster.v1", Title: "Cluster Director", Issues: []int{321}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.hypercomputecluster.v1.",
	Collections: []*Collection{
		{ID: "clusters", Schema: "Cluster", IDParam: "clusterId", IDPattern: regexp.MustCompile(`^[a-z][a-z0-9]{0,9}$`), Limit: 10, Permission: "hypercomputecluster.clusters",
			Generate: devtoolsOutputs(map[string]any{"reconciling": false}),
			Mutable:  []string{"description", "labels"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.clusters.create", Kind: Create, Collection: "clusters"},
		{Method: "projects.locations.clusters.get", Kind: Get, Collection: "clusters"},
		{Method: "projects.locations.clusters.list", Kind: List, Collection: "clusters"},
		{Method: "projects.locations.clusters.patch", Kind: Patch, Collection: "clusters"},
		{Method: "projects.locations.clusters.delete", Kind: Delete, Collection: "clusters"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Clusters are configuration metadata: no VM, accelerator, network, storage or Slurm orchestrator is provisioned and no job runs.",
		"Only description and labels can be updated. Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

var vmwareEngine = &API{
	Key: "vmwareengine.v1", Title: "Google Cloud VMware Engine", Issues: []int{324}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.vmwareengine.v1.",
	Collections: []*Collection{
		{ID: "vmwareEngineNetworks", Schema: "VmwareEngineNetwork", IDParam: "vmwareEngineNetworkId", IDPattern: vmwareID, Limit: 10, Permission: "vmwareengine.vmwareEngineNetworks",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid", "etag": "$etag", "state": "ACTIVE"}),
			Mutable:  []string{"description"}},
		{ID: "networkPolicies", Schema: "NetworkPolicy", IDParam: "networkPolicyId", IDPattern: vmwareID, Limit: 20, Permission: "vmwareengine.networkPolicies",
			Generate: devtoolsOutputs(map[string]any{"uid": "$uid"}),
			Mutable:  []string{"description", "edgeServicesCidr"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.vmwareEngineNetworks.create", Kind: Create, Collection: "vmwareEngineNetworks", Check: vmwareStandardNetwork},
		{Method: "projects.locations.vmwareEngineNetworks.get", Kind: Get, Collection: "vmwareEngineNetworks"},
		{Method: "projects.locations.vmwareEngineNetworks.list", Kind: List, Collection: "vmwareEngineNetworks"},
		{Method: "projects.locations.vmwareEngineNetworks.patch", Kind: Patch, Collection: "vmwareEngineNetworks"},
		{Method: "projects.locations.vmwareEngineNetworks.delete", Kind: Delete, Collection: "vmwareEngineNetworks"},
		{Method: "projects.locations.networkPolicies.create", Kind: Create, Collection: "networkPolicies", Check: vmwareNetworkPolicy},
		{Method: "projects.locations.networkPolicies.get", Kind: Get, Collection: "networkPolicies"},
		{Method: "projects.locations.networkPolicies.list", Kind: List, Collection: "networkPolicies"},
		{Method: "projects.locations.networkPolicies.patch", Kind: Patch, Collection: "networkPolicies", Check: vmwareNetworkPolicy},
		{Method: "projects.locations.networkPolicies.delete", Kind: Delete, Collection: "networkPolicies"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"VMware Engine networks and network policies are configuration metadata: no private cloud, vCenter, NSX, VPC, internet access or external IP service is provisioned, and networks report ACTIVE immediately.",
		"Only STANDARD networks are accepted. The simulator does not check that a network is created in the global location or that a policy's network exists. Private clouds are not served because they live in zone locations.",
		"Only descriptions and edge services CIDR ranges can be updated. Long-running operations complete synchronously.",
	},
}

var osConfig = &API{
	Key: "osconfig.v1", Title: "VM Manager (OS Config)", Issues: []int{326}, Scope: "project",
	TypeURL: "type.googleapis.com/google.cloud.osconfig.v1.",
	Collections: []*Collection{
		{ID: "patchDeployments", Schema: "PatchDeployment", IDParam: "patchDeploymentId", Limit: 50, Permission: "osconfig.patchDeployments",
			Relax:    []string{"oneTimeSchedule", "recurringSchedule"},
			Generate: devtoolsOutputs(map[string]any{"state": "ACTIVE"}),
			Mutable:  []string{"description", "instanceFilter", "patchConfig", "duration", "rollout"}},
	},
	Bindings: []Binding{
		{Method: "projects.patchDeployments.create", Kind: Create, Collection: "patchDeployments", Check: patchSchedule},
		{Method: "projects.patchDeployments.get", Kind: Get, Collection: "patchDeployments"},
		{Method: "projects.patchDeployments.list", Kind: List, Collection: "patchDeployments"},
		{Method: "projects.patchDeployments.patch", Kind: Patch, Collection: "patchDeployments"},
		{Method: "projects.patchDeployments.delete", Kind: Delete, Collection: "patchDeployments"},
	},
	Bounds: []string{
		"Patch deployments are configuration metadata: no patch job is ever executed on any VM, no execution time is computed, and pause, resume and patch jobs are not implemented.",
		"Exactly one of oneTimeSchedule and recurringSchedule is required, and schedules cannot be updated. OS policy assignments are not served because they live in zone locations.",
	},
}

var workloadManager = &API{
	Key: "workloadmanager.v1", Title: "Workload Manager", Issues: []int{328}, Scope: "location",
	TypeURL: "type.googleapis.com/google.cloud.workloadmanager.v1.",
	Collections: []*Collection{
		{ID: "evaluations", Schema: "Evaluation", IDParam: "evaluationId", Limit: 20, Permission: "workloadmanager.evaluations",
			Generate: devtoolsOutputs(map[string]any{"resourceStatus": map[string]any{"state": "ACTIVE"}}),
			Mutable:  []string{"description", "labels", "ruleNames", "resourceFilter", "schedule", "customRulesBucket", "bigQueryDestination"}},
	},
	Bindings: []Binding{
		{Method: "projects.locations.evaluations.create", Kind: Create, Collection: "evaluations"},
		{Method: "projects.locations.evaluations.get", Kind: Get, Collection: "evaluations"},
		{Method: "projects.locations.evaluations.list", Kind: List, Collection: "evaluations"},
		{Method: "projects.locations.evaluations.patch", Kind: Patch, Collection: "evaluations"},
		{Method: "projects.locations.evaluations.delete", Kind: Delete, Collection: "evaluations"},
		{Method: "projects.locations.operations.get", Kind: OperationGet},
		{Method: "projects.locations.operations.list", Kind: OperationList},
	},
	Bounds: []string{
		"Evaluations are configuration metadata: no execution, scan, rule result or BigQuery export ever runs, scheduled or on demand.",
		"Long-running operations complete synchronously and remain readable through operations.get/list.",
	},
}

// devtoolsSecretVersion matches Secret Manager secret version names.
var devtoolsSecretVersion = regexp.MustCompile(`^projects/[^/]+/(locations/[^/]+/)?secrets/[^/]+/versions/[^/]+$`)

// vmwareID follows the VMware Engine RFC 1034 identifier rules.
var vmwareID = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// devtoolsSecretReferences requires every field documented as a Secret Manager
// version reference to hold a version name, so that a pasted token,
// password or key is never stored as metadata.
func devtoolsSecretReferences(value any, at string) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			path := joinPath(at, key)
			if strings.HasSuffix(key, "SecretVersion") || strings.HasSuffix(key, "SecretVersionName") {
				if text, _ := item.(string); !devtoolsSecretVersion.MatchString(text) {
					return invalid("Field '%s' must name a Secret Manager secret version; secret values are not stored by the simulator.", path)
				}
			}
			if err := devtoolsSecretReferences(item, path); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := devtoolsSecretReferences(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// repositoryMode keeps the mode and its mode-specific configuration
// consistent and refuses inline upstream secrets.
func repositoryMode(body map[string]any) error {
	if err := devtoolsSecretReferences(body, ""); err != nil {
		return err
	}
	mode, _ := body["mode"].(string)
	if body["disallowUnspecifiedMode"] == true && (mode == "" || mode == "MODE_UNSPECIFIED") {
		return invalid("Field 'mode' is required when disallowUnspecifiedMode is true.")
	}
	for field, want := range map[string]string{"remoteRepositoryConfig": "REMOTE_REPOSITORY", "virtualRepositoryConfig": "VIRTUAL_REPOSITORY"} {
		if _, ok := body[field]; ok && mode != want {
			return invalid("Field '%s' requires mode %s.", field, want)
		}
	}
	return nil
}

// buildTrigger refuses secret material in build triggers: only Secret
// Manager version names are stored, never KMS ciphertext.
func buildTrigger(body map[string]any) error {
	if err := devtoolsSecretReferences(body, ""); err != nil {
		return err
	}
	if hook, ok := body["webhookConfig"].(map[string]any); ok {
		if text, _ := hook["secret"].(string); !devtoolsSecretVersion.MatchString(text) {
			return invalid("Field 'webhookConfig.secret' must name a Secret Manager secret version.")
		}
	}
	build, _ := body["build"].(map[string]any)
	if _, ok := build["secrets"]; ok {
		return invalid("Field 'build.secrets' carries KMS-encrypted secret values, which the simulator does not store; use build.availableSecrets.secretManager.")
	}
	available, _ := build["availableSecrets"].(map[string]any)
	if _, ok := available["inline"]; ok {
		return invalid("Field 'build.availableSecrets.inline' carries KMS-encrypted secret values, which the simulator does not store.")
	}
	managed, _ := available["secretManager"].([]any)
	for i, item := range managed {
		entry, _ := item.(map[string]any)
		if text, _ := entry["versionName"].(string); !devtoolsSecretVersion.MatchString(text) {
			return invalid("Field 'build.availableSecrets.secretManager[%d].versionName' must name a Secret Manager secret version.", i)
		}
	}
	return nil
}

var gitConnectionProviders = []string{"githubConfig", "githubEnterpriseConfig", "gitlabConfig", "gitlabEnterpriseConfig", "bitbucketCloudConfig", "bitbucketDataCenterConfig", "secureSourceManagerInstanceConfig", "httpConfig"}

// gitConnection requires exactly one provider configuration, Secret Manager
// credential references and no Git proxy, which the simulator cannot serve.
func gitConnection(body map[string]any) error {
	if err := devtoolsSecretReferences(body, ""); err != nil {
		return err
	}
	count := 0
	for _, field := range gitConnectionProviders {
		if _, ok := body[field]; ok {
			count++
		}
	}
	if count != 1 {
		return invalid("Exactly one of %s is required.", strings.Join(gitConnectionProviders, ", "))
	}
	return devtoolsNoProxy(body, "gitProxyConfig")
}

// accountConnector refuses custom OAuth applications, whose client secret
// is required plain text, and proxies the simulator cannot serve.
func accountConnector(body map[string]any) error {
	if _, ok := body["customOauthConfig"]; ok {
		return invalid("Field 'customOauthConfig' requires a plain-text client secret, which the simulator does not store; use providerOauthConfig.")
	}
	if _, ok := body["providerOauthConfig"]; !ok {
		return invalid("Field 'providerOauthConfig' is required.")
	}
	return devtoolsNoProxy(body, "proxyConfig")
}

func devtoolsNoProxy(body map[string]any, field string) error {
	if config, _ := body[field].(map[string]any); config["enabled"] == true {
		return invalid("Field '%s.enabled' is not supported: the simulator serves no proxy.", field)
	}
	return nil
}

// noInitialContent refuses repository initialization: the simulator stores
// no Git content, so a README, .gitignore or license would be dropped.
func noInitialContent(body map[string]any) error {
	if _, ok := body["initialConfig"]; ok {
		return invalid("Field 'initialConfig' is not supported: the simulator stores no Git content.")
	}
	return nil
}

// vmwareStandardNetwork accepts only STANDARD networks; LEGACY networks are no
// longer used for new VMware Engine deployments.
func vmwareStandardNetwork(body map[string]any) error {
	if body["type"] != "STANDARD" {
		return invalid("Only VMware Engine networks of type STANDARD are supported by the simulator.")
	}
	return nil
}

// vmwareNetworkPolicy requires an RFC 1918 /26 edge services range and internet
// access whenever external IP addresses are enabled.
func vmwareNetworkPolicy(body map[string]any) error {
	if text, ok := body["edgeServicesCidr"].(string); ok {
		ip, network, err := net.ParseCIDR(text)
		if ones, _ := vmwareMaskSize(network); err != nil || ones != 26 || ip.To4() == nil || !ip.IsPrivate() || !ip.Equal(network.IP) {
			return invalid("Field 'edgeServicesCidr' must be an RFC 1918 IPv4 range with a /26 prefix.")
		}
	}
	external, _ := body["externalIp"].(map[string]any)
	internet, _ := body["internetAccess"].(map[string]any)
	if external["enabled"] == true && internet["enabled"] != true {
		return invalid("Field 'externalIp.enabled' requires internetAccess.enabled.")
	}
	return nil
}

func vmwareMaskSize(network *net.IPNet) (int, int) {
	if network == nil {
		return 0, 0
	}
	return network.Mask.Size()
}

// patchSchedule requires exactly one of the two schedule kinds, which the
// official descriptions each mark as required.
func patchSchedule(body map[string]any) error {
	_, once := body["oneTimeSchedule"]
	_, recurring := body["recurringSchedule"]
	if once == recurring {
		return invalid("Exactly one of oneTimeSchedule and recurringSchedule is required.")
	}
	return nil
}
