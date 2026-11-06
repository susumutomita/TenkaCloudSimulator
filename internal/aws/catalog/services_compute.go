package catalog

import (
	"regexp"
	"strconv"
	"strings"
)

// Reviewed bounded specifications for compute and developer-tool services.
// Each binds only official operations whose effect is configuration metadata;
// operations that run builds, deployments, tasks, containers, pipeline
// executions, Git data, control planes or networks stay unbound (HTTP 501).

var appRunner = &Service{
	Model: "apprunner", Title: "AWS App Runner", IAMPrefix: "apprunner", Issues: []int{103},
	Resources: []*Resource{
		{Kind: "autoScalingConfiguration", Shape: "AutoScalingConfiguration", Summary: "AutoScalingConfigurationSummary", ID: "hex:32",
			ARN:       "arn:{partition}:apprunner:{region}:{account}:autoscalingconfiguration/{name}/1/{id}",
			ARNMember: "AutoScalingConfigurationArn", NameMember: "AutoScalingConfigurationName", Unique: true, Limit: 100,
			Generate: map[string]any{"AutoScalingConfigurationRevision": 1, "Latest": true, "Status": "ACTIVE", "CreatedAt": "$now",
				"MaxConcurrency": 100, "MinSize": 1, "MaxSize": 25, "HasAssociatedService": false, "IsDefault": false}},
		{Kind: "observabilityConfiguration", Shape: "ObservabilityConfiguration", Summary: "ObservabilityConfigurationSummary", ID: "hex:32",
			ARN:       "arn:{partition}:apprunner:{region}:{account}:observabilityconfiguration/{name}/1/{id}",
			ARNMember: "ObservabilityConfigurationArn", NameMember: "ObservabilityConfigurationName", Unique: true, Limit: 100,
			Generate: map[string]any{"ObservabilityConfigurationRevision": 1, "Latest": true, "Status": "ACTIVE", "CreatedAt": "$now"}},
		{Kind: "connection", Shape: "Connection", Summary: "ConnectionSummary", ID: "hex:32",
			ARN:       "arn:{partition}:apprunner:{region}:{account}:connection/{name}/{id}",
			ARNMember: "ConnectionArn", NameMember: "ConnectionName", Unique: true, Limit: 100,
			Generate: map[string]any{"Status": "PENDING_HANDSHAKE", "CreatedAt": "$now"}},
		{Kind: "vpcConnector", Shape: "VpcConnector", ID: "hex:32",
			ARN:       "arn:{partition}:apprunner:{region}:{account}:vpcconnector/{name}/1/{id}",
			ARNMember: "VpcConnectorArn", NameMember: "VpcConnectorName", Unique: true, Limit: 100,
			Generate: map[string]any{"VpcConnectorRevision": 1, "Status": "ACTIVE", "CreatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateAutoScalingConfiguration", Kind: Create, Resource: "autoScalingConfiguration", Check: appRunnerScaling},
		{Name: "DescribeAutoScalingConfiguration", Kind: Read, Resource: "autoScalingConfiguration", Lookup: "AutoScalingConfigurationArn"},
		{Name: "ListAutoScalingConfigurations", Kind: List, Resource: "autoScalingConfiguration",
			Filters: map[string]string{"AutoScalingConfigurationName": "AutoScalingConfigurationName"}, Accept: []string{"LatestOnly"}},
		{Name: "DeleteAutoScalingConfiguration", Kind: Delete, Resource: "autoScalingConfiguration", Lookup: "AutoScalingConfigurationArn",
			Accept: []string{"DeleteAllRevisions"}, Set: map[string]any{"Status": "INACTIVE", "Latest": false, "DeletedAt": "$now"}},
		{Name: "CreateObservabilityConfiguration", Kind: Create, Resource: "observabilityConfiguration"},
		{Name: "DescribeObservabilityConfiguration", Kind: Read, Resource: "observabilityConfiguration", Lookup: "ObservabilityConfigurationArn"},
		{Name: "ListObservabilityConfigurations", Kind: List, Resource: "observabilityConfiguration",
			Filters: map[string]string{"ObservabilityConfigurationName": "ObservabilityConfigurationName"}, Accept: []string{"LatestOnly"}},
		{Name: "DeleteObservabilityConfiguration", Kind: Delete, Resource: "observabilityConfiguration", Lookup: "ObservabilityConfigurationArn",
			Set: map[string]any{"Status": "INACTIVE", "Latest": false, "DeletedAt": "$now"}},
		{Name: "CreateConnection", Kind: Create, Resource: "connection"},
		{Name: "ListConnections", Kind: List, Resource: "connection", Filters: map[string]string{"ConnectionName": "ConnectionName"}},
		{Name: "DeleteConnection", Kind: Delete, Resource: "connection", Lookup: "ConnectionArn", Set: map[string]any{"Status": "DELETED"}},
		{Name: "CreateVpcConnector", Kind: Create, Resource: "vpcConnector"},
		{Name: "DescribeVpcConnector", Kind: Read, Resource: "vpcConnector", Lookup: "VpcConnectorArn"},
		{Name: "ListVpcConnectors", Kind: List, Resource: "vpcConnector"},
		{Name: "DeleteVpcConnector", Kind: Delete, Resource: "vpcConnector", Lookup: "VpcConnectorArn",
			Set: map[string]any{"Status": "INACTIVE", "DeletedAt": "$now"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Auto scaling configurations, observability configurations, connections and VPC connectors are configuration metadata. Each name holds exactly one revision (1); creating another revision of a live name is refused, so LatestOnly and DeleteAllRevisions have no further effect. The AWS-provided DefaultConfiguration is not listed.",
		"Connections stay PENDING_HANDSHAKE: no GitHub or Bitbucket handshake is performed. VPC connectors store subnet and security group identifiers without checking them or creating network interfaces.",
		"Services, custom domains, deployments, VPC ingress connections and operations are not implemented: CreateService, StartDeployment, PauseService and other workload APIs return UnknownOperationException with HTTP 501.",
	},
}

// appRunnerScaling applies the documented rule that the minimum instance
// count cannot exceed the maximum, using the official defaults of 1 and 25.
func appRunnerScaling(input map[string]any) error {
	minimum, maximum := int64(1), int64(25)
	if value, ok := input["MinSize"].(int64); ok {
		minimum = value
	}
	if value, ok := input["MaxSize"].(int64); ok {
		maximum = value
	}
	if minimum > maximum {
		return Failure("InvalidRequestException", "MinSize must not exceed MaxSize.")
	}
	return nil
}

var imageBuilder = &Service{
	Model: "imagebuilder", Title: "EC2 Image Builder", IAMPrefix: "imagebuilder", Issues: []int{106},
	Resources: []*Resource{
		{Kind: "distributionConfiguration", Shape: "DistributionConfiguration", Summary: "DistributionConfigurationSummary", ID: "name",
			ARN:       "arn:{partition}:imagebuilder:{region}:{account}:distribution-configuration/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"distributionConfigurationArn": "$arn", "dateCreated": "$now", "timeoutMinutes": 720}},
		{Kind: "lifecyclePolicy", Shape: "LifecyclePolicy", Summary: "LifecyclePolicySummary", ID: "name",
			ARN:       "arn:{partition}:imagebuilder:{region}:{account}:lifecycle-policy/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"lifecyclePolicyArn": "$arn", "dateCreated": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateDistributionConfiguration", Kind: Create, Resource: "distributionConfiguration", Check: imageBuilderName},
		{Name: "GetDistributionConfiguration", Kind: Read, Resource: "distributionConfiguration", Lookup: "distributionConfigurationArn"},
		{Name: "ListDistributionConfigurations", Kind: List, Resource: "distributionConfiguration"},
		{Name: "UpdateDistributionConfiguration", Kind: Update, Resource: "distributionConfiguration", Lookup: "distributionConfigurationArn",
			Fields: []string{"description", "distributions"}, Accept: []string{"clientToken"}, Touch: []string{"dateUpdated"}},
		{Name: "DeleteDistributionConfiguration", Kind: Delete, Resource: "distributionConfiguration", Lookup: "distributionConfigurationArn"},
		{Name: "CreateLifecyclePolicy", Kind: Create, Resource: "lifecyclePolicy", Check: imageBuilderName},
		{Name: "GetLifecyclePolicy", Kind: Read, Resource: "lifecyclePolicy", Lookup: "lifecyclePolicyArn"},
		{Name: "ListLifecyclePolicies", Kind: List, Resource: "lifecyclePolicy"},
		{Name: "UpdateLifecyclePolicy", Kind: Update, Resource: "lifecyclePolicy", Lookup: "lifecyclePolicyArn",
			Fields: []string{"description", "status", "executionRole", "resourceType", "policyDetails", "resourceSelection"},
			Accept: []string{"clientToken"}, Touch: []string{"dateUpdated"}},
		{Name: "DeleteLifecyclePolicy", Kind: Delete, Resource: "lifecyclePolicy", Lookup: "lifecyclePolicyArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Distribution configurations and lifecycle policies are configuration metadata: no AMI or container image is copied, distributed, deprecated or deleted, and lifecycle policies never run. Names must be lowercase letters, digits, hyphens and underscores so the ARN keeps the name unchanged.",
		"Update client tokens are accepted but not recorded. List filters are refused. Components, recipes, workflows, infrastructure configurations, image pipelines and images are not implemented; CreateImage, StartImagePipelineExecution and other build APIs return UnknownOperationException with HTTP 501.",
	},
}

var lowercaseImageBuilderName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// imageBuilderName refuses names whose official ARN would be a lowercased,
// hyphenated form that the simulator does not derive.
func imageBuilderName(input map[string]any) error {
	if name, ok := input["name"].(string); ok && !lowercaseImageBuilderName.MatchString(name) {
		return Failure("InvalidRequestException", "The simulator requires lowercase Image Builder names without spaces so the ARN keeps the name unchanged.")
	}
	return nil
}

var ecs = &Service{
	Model: "ecs", Title: "Amazon Elastic Container Service", IAMPrefix: "ecs", Issues: []int{112, 273},
	Resources: []*Resource{
		{Kind: "cluster", Shape: "Cluster", ID: "name", ARN: "arn:{partition}:ecs:{region}:{account}:cluster/{name}",
			ARNMember: "clusterArn", NameMember: "clusterName", Unique: true, Limit: 100,
			Generate: map[string]any{"status": "ACTIVE", "registeredContainerInstancesCount": 0, "runningTasksCount": 0,
				"pendingTasksCount": 0, "activeServicesCount": 0, "statistics": []any{}, "capacityProviders": []any{},
				"defaultCapacityProviderStrategy": []any{}, "attachments": []any{},
				"settings": []any{map[string]any{"name": "containerInsights", "value": "disabled"}}}},
		{Kind: "taskDefinition", Shape: "TaskDefinition", ID: "name", ARN: "arn:{partition}:ecs:{region}:{account}:task-definition/{name}:1",
			ARNMember: "taskDefinitionArn", NameMember: "family", Unique: true, Limit: 200,
			Generate: map[string]any{"revision": 1, "status": "ACTIVE", "registeredAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateCluster", Kind: Create, Resource: "cluster", Check: ecsCreateCluster},
		{Name: "DescribeClusters", Kind: List, Resource: "cluster", Names: "clusters", Accept: []string{"include"}, Check: ecsDescribeClusters,
			Failures: "failures", FailureItem: map[string]any{"arn": "$name", "reason": "MISSING"}},
		{Name: "ListClusters", Kind: List, Resource: "cluster", ListValue: "clusterArn"},
		{Name: "UpdateCluster", Kind: Update, Resource: "cluster", Lookup: "cluster", Fields: []string{"settings", "configuration"}, Check: ecsCapacityProviders},
		{Name: "UpdateClusterSettings", Kind: Update, Resource: "cluster", Lookup: "cluster", Fields: []string{"settings"}},
		{Name: "PutClusterCapacityProviders", Kind: Update, Resource: "cluster", Lookup: "cluster",
			Fields: []string{"capacityProviders", "defaultCapacityProviderStrategy"}, Check: ecsCapacityProviders},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "cluster", Set: map[string]any{"status": "INACTIVE"}},
		{Name: "RegisterTaskDefinition", Kind: Create, Resource: "taskDefinition", Check: ecsTaskDefinition},
		{Name: "DescribeTaskDefinition", Kind: Read, Resource: "taskDefinition", Lookup: "taskDefinition", Accept: []string{"include"}, Check: ecsTaskDefinitionRevision},
		{Name: "ListTaskDefinitions", Kind: List, Resource: "taskDefinition", ListValue: "taskDefinitionArn", Filters: map[string]string{"status": "status"}},
		{Name: "DeregisterTaskDefinition", Kind: Delete, Resource: "taskDefinition", Lookup: "taskDefinition", Check: ecsTaskDefinitionRevision,
			Set: map[string]any{"status": "INACTIVE", "deregisteredAt": "$now"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Clusters and task definitions are configuration metadata: no container instance, task, service, task set or Fargate capacity is started, so cluster counts stay 0. RunTask, StartTask, CreateService and other workload APIs return UnknownOperationException with HTTP 501.",
		"Clusters accept only the AWS-managed FARGATE and FARGATE_SPOT capacity providers; Service Connect namespaces do not exist. A missing clusterName creates the default cluster and DescribeClusters without clusters describes it. Creating a live cluster name again is refused. DescribeClusters and DescribeTaskDefinition always return settings, configuration and tags, and failures name the identifier as supplied.",
		"Each task definition family holds one revision (1): registering a live family again is refused and other revisions do not exist. Deregistration deletes the revision after returning it INACTIVE. Fargate task definitions must use awsvpc networking and a documented Linux task CPU and memory combination. familyPrefix and sort are refused.",
	},
}

var ecsName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// ecsCreateCluster applies the official default cluster name and the
// documented cluster name characters before the capacity-provider rules.
func ecsCreateCluster(input map[string]any) error {
	if _, ok := input["clusterName"]; !ok {
		input["clusterName"] = "default"
	}
	if name, _ := input["clusterName"].(string); !ecsName.MatchString(name) {
		return Failure("InvalidParameterException", "Cluster names contain up to 255 letters, numbers, hyphens and underscores.")
	}
	return ecsCapacityProviders(input)
}

// ecsDescribeClusters describes the default cluster when none is named,
// as the official API does.
func ecsDescribeClusters(input map[string]any) error {
	if names, _ := input["clusters"].([]any); len(names) == 0 {
		input["clusters"] = []any{"default"}
	}
	return nil
}

// ecsCapacityProviders allows only the AWS-managed Fargate capacity
// providers, requires the default strategy to name associated providers
// and refuses Service Connect, whose Cloud Map namespaces do not exist.
func ecsCapacityProviders(input map[string]any) error {
	if _, ok := input["serviceConnectDefaults"]; ok {
		return Failure("NamespaceNotFoundException", "The simulator has no AWS Cloud Map namespace for Service Connect.")
	}
	associated := map[string]bool{}
	providers, _ := input["capacityProviders"].([]any)
	for _, item := range providers {
		name, _ := item.(string)
		if name != "FARGATE" && name != "FARGATE_SPOT" {
			return Failure("InvalidParameterException", "Only the FARGATE and FARGATE_SPOT capacity providers exist in the simulator.")
		}
		associated[name] = true
	}
	strategy, _ := input["defaultCapacityProviderStrategy"].([]any)
	for _, item := range strategy {
		entry, _ := item.(map[string]any)
		if name, _ := entry["capacityProvider"].(string); !associated[name] {
			return Failure("InvalidParameterException", "The default capacity provider strategy may name only capacity providers associated with the cluster.")
		}
	}
	return nil
}

// ecsTaskDefinition checks the family name and the documented Fargate
// requirements: awsvpc networking and a supported task CPU/memory size.
func ecsTaskDefinition(input map[string]any) error {
	if family, _ := input["family"].(string); !ecsName.MatchString(family) {
		return Failure("ClientException", "Task definition families contain up to 255 letters, numbers, hyphens and underscores.")
	}
	compatibilities, _ := input["requiresCompatibilities"].([]any)
	fargate := false
	for _, item := range compatibilities {
		fargate = fargate || item == "FARGATE"
	}
	if !fargate {
		return nil
	}
	if input["networkMode"] != "awsvpc" {
		return Failure("ClientException", "Fargate task definitions require the awsvpc network mode.")
	}
	cpuText, _ := input["cpu"].(string)
	memoryText, _ := input["memory"].(string)
	cpu, cpuOK := fargateUnits(cpuText, "vcpu")
	memory, memoryOK := fargateUnits(memoryText, "gb")
	if !cpuOK || !memoryOK || !fargateSize(cpu, memory) {
		return Failure("ClientException", "Fargate task definitions require a supported task-level cpu and memory combination.")
	}
	return nil
}

// fargateUnits converts "512", "0.5 vCPU" or "1 GB" into CPU units or MiB.
func fargateUnits(text, unit string) (int64, bool) {
	text = strings.ToLower(strings.TrimSpace(text))
	if number := strings.TrimSpace(strings.TrimSuffix(text, unit)); number != text {
		value, err := strconv.ParseFloat(number, 64)
		return int64(value * 1024), err == nil && value > 0
	}
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil && value > 0
}

// fargateSize reports whether a task size is a documented Linux Fargate
// CPU (units) and memory (MiB) combination.
func fargateSize(cpu, memory int64) bool {
	within := func(low, high, step int64) bool { return memory >= low && memory <= high && (memory-low)%step == 0 }
	switch cpu {
	case 256:
		return memory == 512 || memory == 1024 || memory == 2048
	case 512:
		return within(1024, 4096, 1024)
	case 1024:
		return within(2048, 8192, 1024)
	case 2048:
		return within(4096, 16384, 1024)
	case 4096:
		return within(8192, 30720, 1024)
	case 8192:
		return within(16384, 61440, 4096)
	case 16384:
		return within(32768, 122880, 8192)
	}
	return false
}

// ecsTaskDefinitionRevision resolves family:1 to the single stored
// revision and refuses other revisions, which the simulator never registers.
func ecsTaskDefinitionRevision(input map[string]any) error {
	identifier, _ := input["taskDefinition"].(string)
	slash := strings.LastIndex(identifier, "/")
	family, revision, ok := strings.Cut(identifier[slash+1:], ":")
	if !ok {
		return nil
	}
	if revision != "1" {
		return Failure("ClientException", "The task definition revision does not exist.")
	}
	if slash < 0 {
		input["taskDefinition"] = family
	}
	return nil
}

var eks = &Service{
	Model: "eks", Title: "Amazon Elastic Kubernetes Service", IAMPrefix: "eks", Issues: []int{113},
	Resources: []*Resource{
		{Kind: "cluster", Shape: "Cluster", ID: "name", ARN: "arn:{partition}:eks:{region}:{account}:cluster/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"createdAt": "$now", "status": "CREATING"}},
	},
	Ops: []Op{
		{Name: "CreateCluster", Kind: Create, Resource: "cluster", Check: eksCluster},
		{Name: "DescribeCluster", Kind: Read, Resource: "cluster", Lookup: "name"},
		{Name: "ListClusters", Kind: List, Resource: "cluster", ListValue: "name"},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "name", Set: map[string]any{"status": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Clusters are configuration metadata that stay CREATING: no Kubernetes control plane, endpoint, certificate, OIDC issuer or network interface is provisioned, and no Kubernetes API is served. Kubernetes versions are stored as given in major.minor form without checking the supported list.",
		"Outposts local clusters and deletion protection are refused. Cluster updates, node groups, Fargate profiles, add-ons, access entries, pod identity associations and connected clusters return UnknownOperationException with HTTP 501; ListClusters include is refused.",
	},
}

var kubernetesVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// eksCluster refuses options whose effect the simulator cannot uphold.
func eksCluster(input map[string]any) error {
	if _, ok := input["outpostConfig"]; ok {
		return Failure("InvalidParameterException", "Amazon EKS local clusters on AWS Outposts are not simulated.")
	}
	if protected, _ := input["deletionProtection"].(bool); protected {
		return Failure("InvalidParameterException", "Deletion protection is not enforced by the simulator.")
	}
	if version, ok := input["version"].(string); ok && !kubernetesVersion.MatchString(version) {
		return Failure("InvalidParameterException", "The Kubernetes version must be in major.minor form.")
	}
	return nil
}

var codeArtifact = &Service{
	Model: "codeartifact", Title: "AWS CodeArtifact", IAMPrefix: "codeartifact", Issues: []int{135},
	AccountMembers: []string{"domainOwner", "administratorAccount"},
	Resources: []*Resource{
		{Kind: "domain", Shape: "DomainDescription", Summary: "DomainSummary", ID: "name", ARN: "arn:{partition}:codeartifact:{region}:{account}:domain/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100, Aliases: map[string]string{"domain": "name"},
			Generate: map[string]any{"owner": "$account", "status": "Active", "createdTime": "$now", "assetSizeBytes": 0}},
		{Kind: "repository", Shape: "RepositoryDescription", Summary: "RepositorySummary", Parent: "domain", ParentMember: "domainName", ID: "name",
			ARN:       "arn:{partition}:codeartifact:{region}:{account}:repository/{parent}/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100, Aliases: map[string]string{"repository": "name"},
			Generate: map[string]any{"domainName": "$parentId", "domainOwner": "$account", "administratorAccount": "$account",
				"createdTime": "$now", "upstreams": []any{}, "externalConnections": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateDomain", Kind: Create, Resource: "domain"},
		{Name: "DescribeDomain", Kind: Read, Resource: "domain", Lookup: "domain"},
		{Name: "ListDomains", Kind: List, Resource: "domain"},
		{Name: "DeleteDomain", Kind: Delete, Resource: "domain", Lookup: "domain", Set: map[string]any{"status": "Deleted"}},
		{Name: "CreateRepository", Kind: Create, Resource: "repository", Parent: "domain", Check: codeArtifactUpstreams},
		{Name: "DescribeRepository", Kind: Read, Resource: "repository", Parent: "domain", Lookup: "repository"},
		{Name: "ListRepositoriesInDomain", Kind: List, Resource: "repository", Parent: "domain"},
		{Name: "UpdateRepository", Kind: Update, Resource: "repository", Parent: "domain", Lookup: "repository",
			Fields: []string{"description", "upstreams"}, Check: codeArtifactUpstreams},
		{Name: "DeleteRepository", Kind: Delete, Resource: "repository", Parent: "domain", Lookup: "repository"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Domains and repositories are configuration metadata: no package, package version, asset, endpoint or authorization token exists, and encryption keys are stored without resolving them. Domains with repositories cannot be deleted; repositoryCount is not reported.",
		"Upstream repositories and external connections are refused. ListRepositories across domains, repositoryPrefix, permissions policies, package groups and package APIs return UnknownOperationException with HTTP 501 or are refused.",
	},
}

// codeArtifactUpstreams refuses upstream chains the simulator cannot resolve.
func codeArtifactUpstreams(input map[string]any) error {
	if upstreams, _ := input["upstreams"].([]any); len(upstreams) > 0 {
		return Failure("ValidationException", "Upstream repositories are not resolved by the simulator.")
	}
	return nil
}

var codeBuild = &Service{
	Model: "codebuild", Title: "AWS CodeBuild", IAMPrefix: "codebuild", Issues: []int{136},
	Resources: []*Resource{
		{Kind: "project", Shape: "Project", ID: "name", ARN: "arn:{partition}:codebuild:{region}:{account}:project/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"created": "$now", "lastModified": "$now", "timeoutInMinutes": 60, "queuedTimeoutInMinutes": 480,
				"projectVisibility": "PRIVATE", "badge": map[string]any{"badgeEnabled": false}}},
		{Kind: "reportGroup", Shape: "ReportGroup", ID: "name", ARN: "arn:{partition}:codebuild:{region}:{account}:report-group/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"created": "$now", "lastModified": "$now", "status": "ACTIVE"}},
	},
	Ops: []Op{
		{Name: "CreateProject", Kind: Create, Resource: "project"},
		{Name: "BatchGetProjects", Kind: List, Resource: "project", Names: "names"},
		{Name: "ListProjects", Kind: List, Resource: "project", ListValue: "name"},
		{Name: "UpdateProject", Kind: Update, Resource: "project", Lookup: "name", Touch: []string{"lastModified"},
			Fields: []string{"description", "source", "secondarySources", "sourceVersion", "secondarySourceVersions", "artifacts", "secondaryArtifacts",
				"cache", "environment", "serviceRole", "timeoutInMinutes", "queuedTimeoutInMinutes", "encryptionKey", "vpcConfig", "logsConfig",
				"fileSystemLocations", "buildBatchConfig", "concurrentBuildLimit", "autoRetryLimit"}},
		{Name: "DeleteProject", Kind: Delete, Resource: "project", Lookup: "name"},
		{Name: "CreateReportGroup", Kind: Create, Resource: "reportGroup"},
		{Name: "BatchGetReportGroups", Kind: List, Resource: "reportGroup", Names: "reportGroupArns"},
		{Name: "ListReportGroups", Kind: List, Resource: "reportGroup", ListValue: "arn"},
		{Name: "UpdateReportGroup", Kind: Update, Resource: "reportGroup", Lookup: "arn", Fields: []string{"exportConfig"}, Touch: []string{"lastModified"}},
		{Name: "DeleteReportGroup", Kind: Delete, Resource: "reportGroup", Lookup: "arn", Accept: []string{"deleteReports"}},
	},
	Bounds: []string{
		"Projects and report groups are configuration metadata: no build, batch, sandbox, report, webhook, fleet or source credential exists, and sources, roles and keys are stored without contacting them. StartBuild and other build APIs return UnknownOperationException with HTTP 501.",
		"BatchGetProjects and BatchGetReportGroups fail with ResourceNotFoundException when any identifier is missing instead of listing it as not found. Tags are set at creation only; tags and badgeEnabled in updates and sortBy/sortOrder are refused. Report groups hold no reports, so deleteReports has no further effect.",
	},
}

var codeDeploy = &Service{
	Model: "codedeploy", Title: "AWS CodeDeploy", IAMPrefix: "codedeploy", Issues: []int{138},
	Resources: []*Resource{
		{Kind: "application", Shape: "ApplicationInfo", ID: "uuid", ARN: "arn:{partition}:codedeploy:{region}:{account}:application:{name}",
			NameMember: "applicationName", IDMember: "applicationId", Unique: true, Limit: 100,
			Generate: map[string]any{"createTime": "$now", "linkedToGitHub": false, "computePlatform": "Server"}},
		{Kind: "deploymentConfig", Shape: "DeploymentConfigInfo", ID: "uuid", ARN: "arn:{partition}:codedeploy:{region}:{account}:deploymentconfig:{name}",
			NameMember: "deploymentConfigName", IDMember: "deploymentConfigId", Unique: true, Limit: 100,
			Generate: map[string]any{"createTime": "$now", "computePlatform": "Server"}},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application"},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "applicationName"},
		{Name: "ListApplications", Kind: List, Resource: "application", ListValue: "applicationName"},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "applicationName"},
		{Name: "CreateDeploymentConfig", Kind: Create, Resource: "deploymentConfig", Check: codeDeployCreateConfig},
		{Name: "GetDeploymentConfig", Kind: Read, Resource: "deploymentConfig", Lookup: "deploymentConfigName", Check: codeDeployPredefined("InvalidDeploymentConfigNameException")},
		{Name: "ListDeploymentConfigs", Kind: List, Resource: "deploymentConfig", ListValue: "deploymentConfigName"},
		{Name: "DeleteDeploymentConfig", Kind: Delete, Resource: "deploymentConfig", Lookup: "deploymentConfigName", Check: codeDeployPredefined("InvalidOperationException")},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Applications and custom deployment configurations are configuration metadata: no deployment, revision, instance, hook or GitHub link exists. CreateDeployment, deployment groups and on-premises instance APIs return UnknownOperationException with HTTP 501; applications cannot be renamed.",
		"Predefined CodeDeployDefault configurations are not modeled: they are neither listed nor readable, and the prefix is refused. Server configurations require minimumHealthyHosts and refuse traffic routing.",
	},
}

// codeDeployPredefined refuses the reserved CodeDeployDefault prefix with
// the operation's modeled exception.
func codeDeployPredefined(code string) func(map[string]any) error {
	return func(input map[string]any) error {
		if name, _ := input["deploymentConfigName"].(string); strings.HasPrefix(name, "CodeDeployDefault.") {
			return Failure(code, "CodeDeployDefault configurations are predefined by AWS and are not modeled by the simulator.")
		}
		return nil
	}
}

// codeDeployCreateConfig applies the documented compute-platform rules of
// deployment configurations.
func codeDeployCreateConfig(input map[string]any) error {
	if err := codeDeployPredefined("InvalidDeploymentConfigNameException")(input); err != nil {
		return err
	}
	if platform, _ := input["computePlatform"].(string); platform == "" || platform == "Server" {
		if _, ok := input["minimumHealthyHosts"]; !ok {
			return Failure("InvalidMinimumHealthyHostValueException", "Server deployment configurations require minimumHealthyHosts.")
		}
		if _, ok := input["trafficRoutingConfig"]; ok {
			return Failure("InvalidTrafficRoutingConfigurationException", "Traffic routing applies only to Lambda and ECS deployment configurations.")
		}
	}
	return nil
}

var codePipeline = &Service{
	Model: "codepipeline", Title: "AWS CodePipeline", IAMPrefix: "codepipeline", Issues: []int{139},
	Resources: []*Resource{
		{Kind: "pipeline", Shape: "PipelineDeclaration", Summary: "PipelineSummary", ID: "name", ARN: "arn:{partition}:codepipeline:{region}:{account}:{name}",
			NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"executionMode": "SUPERSEDED", "created": "$now", "updated": "$now"}},
	},
	Ops: []Op{
		{Name: "CreatePipeline", Kind: Create, Resource: "pipeline", From: "pipeline", Set: map[string]any{"version": 1}, Check: codePipelineDeclaration},
		{Name: "GetPipeline", Kind: Read, Resource: "pipeline", Lookup: "name", Accept: []string{"version"}, Check: codePipelineVersion},
		{Name: "ListPipelines", Kind: List, Resource: "pipeline"},
		{Name: "DeletePipeline", Kind: Delete, Resource: "pipeline", Lookup: "name"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Pipelines are stored declarations at version 1 after the documented structural checks: at least two stages, source actions only and always in the first stage, unique stage and action names, and exactly one artifact store setting. Action providers, roles and artifact buckets are stored without contacting them; GetPipeline returns no metadata.",
		"Only AWS-owned actions are accepted. Pipeline executions, approvals, stage transitions, jobs, webhooks, custom action types and UpdatePipeline return UnknownOperationException with HTTP 501.",
	},
}

// codePipelineDeclaration applies the documented pipeline structure rules
// and refuses action owners that need external job workers or OAuth.
func codePipelineDeclaration(input map[string]any) error {
	pipeline, _ := input["pipeline"].(map[string]any)
	_, single := pipeline["artifactStore"]
	_, regional := pipeline["artifactStores"]
	if single == regional {
		return Failure("InvalidStructureException", "Specify exactly one of artifactStore or artifactStores.")
	}
	stages, _ := pipeline["stages"].([]any)
	if len(stages) < 2 {
		return Failure("InvalidStructureException", "A pipeline must contain at least two stages.")
	}
	stageNames := map[string]bool{}
	for i, item := range stages {
		stage, _ := item.(map[string]any)
		name, _ := stage["name"].(string)
		actions, _ := stage["actions"].([]any)
		if stageNames[name] || len(actions) == 0 {
			return Failure("InvalidStageDeclarationException", "Stage names must be unique and each stage needs at least one action.")
		}
		stageNames[name] = true
		actionNames := map[string]bool{}
		for _, entry := range actions {
			action, _ := entry.(map[string]any)
			actionName, _ := action["name"].(string)
			typeID, _ := action["actionTypeId"].(map[string]any)
			if typeID["owner"] != "AWS" {
				return Failure("InvalidActionDeclarationException", "Only AWS-owned actions are supported; third-party and custom actions need external OAuth or job workers.")
			}
			if (typeID["category"] == "Source") != (i == 0) {
				return Failure("InvalidStructureException", "Only the first stage contains source actions, and it contains only source actions.")
			}
			if actionNames[actionName] {
				return Failure("InvalidActionDeclarationException", "Action names must be unique within a stage.")
			}
			actionNames[actionName] = true
		}
	}
	return nil
}

// codePipelineVersion refuses versions other than the single stored one.
func codePipelineVersion(input map[string]any) error {
	if version, ok := input["version"].(int64); ok && version != 1 {
		return Failure("PipelineVersionNotFoundException", "The simulator stores only version 1 of each pipeline.")
	}
	return nil
}

var codeCommit = &Service{
	Model: "codecommit", Title: "AWS CodeCommit", IAMPrefix: "codecommit", Issues: []int{286},
	Resources: []*Resource{
		{Kind: "repository", Shape: "RepositoryMetadata", Summary: "RepositoryNameIdPair", ID: "uuid", ARN: "arn:{partition}:codecommit:{region}:{account}:{name}",
			ARNMember: "Arn", NameMember: "repositoryName", IDMember: "repositoryId", Unique: true, Limit: 100,
			Generate: map[string]any{"accountId": "$account", "creationDate": "$now", "lastModifiedDate": "$now",
				"cloneUrlHttp": "$fmt:https://git-codecommit.{region}.amazonaws.com/v1/repos/{name}",
				"cloneUrlSsh":  "$fmt:ssh://git-codecommit.{region}.amazonaws.com/v1/repos/{name}"}},
	},
	Ops: []Op{
		{Name: "CreateRepository", Kind: Create, Resource: "repository", Check: codeCommitName},
		{Name: "GetRepository", Kind: Read, Resource: "repository", Lookup: "repositoryName"},
		{Name: "ListRepositories", Kind: List, Resource: "repository"},
		{Name: "UpdateRepositoryDescription", Kind: Update, Resource: "repository", Lookup: "repositoryName",
			Fields: []string{"repositoryDescription"}, Touch: []string{"lastModifiedDate"}},
		{Name: "DeleteRepository", Kind: Delete, Resource: "repository", Lookup: "repositoryName"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Repositories are configuration metadata without Git data: no Git endpoint is served at the official clone URLs, and branches, commits, files, pull requests, comments, merges and triggers return UnknownOperationException with HTTP 501. KMS key identifiers are stored without resolving them.",
		"Repositories cannot be renamed. Deleting a missing repository fails instead of returning an empty result, and sortBy/order are refused.",
	},
}

// codeCommitName applies the documented rule that names cannot end in .git.
func codeCommitName(input map[string]any) error {
	if name, _ := input["repositoryName"].(string); strings.HasSuffix(name, ".git") {
		return Failure("InvalidRepositoryNameException", "Repository names cannot end in .git.")
	}
	return nil
}
