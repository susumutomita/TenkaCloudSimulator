package catalog

import (
	"regexp"
	"strings"
)

// Reviewed bounded specifications for AWS Batch, AWS Outposts, AWS Parallel
// Computing Service and AWS Serverless Application Repository. Each binds only
// official operations whose effect is configuration metadata: no job runs, no
// cluster, node or Outpost hardware exists, and no hardware is ordered.

// batchNamePattern is the documented "up to 128 letters ... uppercase and
// lowercase letters, numbers, hyphens (-), and underscores (_)" of Batch names.
var batchNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var batch = &Service{
	Model: "batch", Title: "AWS Batch", IAMPrefix: "batch", Issues: []int{105},
	Resources: []*Resource{
		{Kind: "schedulingPolicy", Shape: "SchedulingPolicyDetail", Summary: "SchedulingPolicyListingDetail", ID: "name",
			ARN:       "arn:{partition}:batch:{region}:{account}:scheduling-policy/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 50, InUse: "ClientException",
			InUseMessage: "The scheduling policy is still referenced by a job queue."},
		{Kind: "consumableResource", Shape: "DescribeConsumableResourceResponse", Summary: "ConsumableResourceSummary", ID: "name",
			ARN:       "arn:{partition}:batch:{region}:{account}:consumable-resource/{name}",
			ARNMember: "consumableResourceArn", NameMember: "consumableResourceName", Unique: true, Limit: 50,
			Generate: map[string]any{"resourceType": "REPLENISHABLE", "totalQuantity": 0, "inUseQuantity": 0}},
		{Kind: "computeEnvironment", Shape: "ComputeEnvironmentDetail", ID: "name",
			ARN:       "arn:{partition}:batch:{region}:{account}:compute-environment/{name}",
			ARNMember: "computeEnvironmentArn", NameMember: "computeEnvironmentName", Unique: true, Limit: 50,
			Generate: map[string]any{"state": "ENABLED", "status": "CREATING", "uuid": "$uuid"}},
		{Kind: "jobQueue", Shape: "JobQueueDetail", ID: "name",
			ARN:       "arn:{partition}:batch:{region}:{account}:job-queue/{name}",
			ARNMember: "jobQueueArn", NameMember: "jobQueueName", Unique: true, Limit: 50,
			Refs:     map[string]string{"schedulingPolicyArn": "schedulingPolicy"},
			Generate: map[string]any{"state": "ENABLED", "status": "VALID", "computeEnvironmentOrder": []any{}}},
		{Kind: "serviceEnvironment", Shape: "ServiceEnvironmentDetail", ID: "name",
			ARN:       "arn:{partition}:batch:{region}:{account}:service-environment/{name}",
			ARNMember: "serviceEnvironmentArn", NameMember: "serviceEnvironmentName", Unique: true, Limit: 50,
			Generate: map[string]any{"state": "ENABLED", "status": "VALID"}},
	},
	Ops: []Op{
		{Name: "CreateSchedulingPolicy", Kind: Create, Resource: "schedulingPolicy", Check: batchSchedulingPolicy},
		{Name: "DescribeSchedulingPolicies", Kind: List, Resource: "schedulingPolicy", Names: "arns"},
		{Name: "ListSchedulingPolicies", Kind: List, Resource: "schedulingPolicy"},
		{Name: "UpdateSchedulingPolicy", Kind: Update, Resource: "schedulingPolicy", Lookup: "arn", Fields: []string{"fairsharePolicy"}, Check: batchSchedulingPolicy},
		{Name: "DeleteSchedulingPolicy", Kind: Delete, Resource: "schedulingPolicy", Lookup: "arn"},
		{Name: "CreateConsumableResource", Kind: Create, Resource: "consumableResource", Check: batchConsumableResource},
		{Name: "DescribeConsumableResource", Kind: Read, Resource: "consumableResource", Lookup: "consumableResource"},
		{Name: "ListConsumableResources", Kind: List, Resource: "consumableResource"},
		{Name: "DeleteConsumableResource", Kind: Delete, Resource: "consumableResource", Lookup: "consumableResource"},
		{Name: "CreateComputeEnvironment", Kind: Create, Resource: "computeEnvironment", Check: batchComputeEnvironment},
		{Name: "DescribeComputeEnvironments", Kind: List, Resource: "computeEnvironment", Names: "computeEnvironments"},
		{Name: "UpdateComputeEnvironment", Kind: Update, Resource: "computeEnvironment", Lookup: "computeEnvironment",
			Fields: []string{"state", "unmanagedvCpus", "serviceRole", "updatePolicy", "context"}, Merged: batchUpdateComputeEnvironment},
		{Name: "DeleteComputeEnvironment", Kind: Delete, Resource: "computeEnvironment", Lookup: "computeEnvironment"},
		{Name: "CreateJobQueue", Kind: Create, Resource: "jobQueue", Check: batchCreateJobQueue},
		{Name: "DescribeJobQueues", Kind: List, Resource: "jobQueue", Names: "jobQueues"},
		{Name: "UpdateJobQueue", Kind: Update, Resource: "jobQueue", Lookup: "jobQueue",
			Fields: []string{"state", "schedulingPolicyArn", "priority", "computeEnvironmentOrder", "serviceEnvironmentOrder", "jobStateTimeLimitActions"}, Check: batchJobQueue, Merged: batchUpdateJobQueue},
		{Name: "DeleteJobQueue", Kind: Delete, Resource: "jobQueue", Lookup: "jobQueue"},
		{Name: "CreateServiceEnvironment", Kind: Create, Resource: "serviceEnvironment", Check: batchServiceEnvironment},
		{Name: "DescribeServiceEnvironments", Kind: List, Resource: "serviceEnvironment", Names: "serviceEnvironments"},
		{Name: "UpdateServiceEnvironment", Kind: Update, Resource: "serviceEnvironment", Lookup: "serviceEnvironment",
			Fields: []string{"state", "capacityLimits"}, Check: batchServiceEnvironment},
		{Name: "DeleteServiceEnvironment", Kind: Delete, Resource: "serviceEnvironment", Lookup: "serviceEnvironment"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Scheduling policies, consumable resources, compute environments, job queues and service environments are stored configuration records: no job is submitted, scheduled, run, listed, described or terminated, no instance, ECS or EKS cluster, Fargate task or SageMaker training job exists, and a compute environment stays CREATING with no ecsClusterArn. SubmitJob, SubmitServiceJob, CancelJob, TerminateJob, TerminateServiceJob, DescribeJobs, ListJobs, ListServiceJobs, DescribeServiceJob, ListJobsByConsumableResource, GetJobQueueSnapshot and every other official operation not bound here return UnknownOperationException with HTTP 501.",
		"Job definitions are not implemented: RegisterJobDefinition numbers revisions as name:revision and DeregisterJobDefinition marks a revision INACTIVE instead of deleting it, which the stored model does not express, so RegisterJobDefinition, DeregisterJobDefinition and DescribeJobDefinitions return HTTP 501. UpdateConsumableResource is not implemented: its SET, ADD and SUBTRACT quantity operations need the in-use accounting of running jobs, so total quantities are fixed at creation and availableQuantity always equals totalQuantity.",
		"Describe operations fail with ClientException (HTTP 400) when a named compute environment, job queue, service environment or scheduling policy does not exist, where AWS omits it from the result; Describe by name accepts a name or an ARN. ListConsumableResources filters are refused, and createdAt of a consumable resource is not reported because the stored clock is not epoch milliseconds.",
		"References are stored without being resolved: the compute environments and service environments of a job queue order, subnets, security groups, launch templates, instance roles, service roles, EKS clusters and Spot fleet roles are not checked, a queue is VALID at once even if its compute environments are not, and UpdateComputeEnvironment changes only state, unmanagedvCpus, serviceRole, updatePolicy and context (computeResources updates are refused). A schedulingPolicyArn must name a stored scheduling policy. Deleting a scheduling policy still referenced by any job queue returns ClientException (HTTP 400) and leaves both records unchanged. Deleting a compute environment or service environment that a queue still uses succeeds, and a compute environment need not be DISABLED before deletion.",
		"Job queue time-limit actions are configuration only: each timeout must be 600 to 86400 seconds. For a nonempty action list, the queue type or its environment order must identify one family: container queues accept CANCEL and SageMaker Training queues accept TERMINATE. No action is executed.",
		"Names are 1 to 128 letters, digits, hyphens or underscores. A compute environment's containerOrchestrationType is ECS, or EKS when eksConfiguration is given; MANAGED compute environments require computeResources, UNMANAGED creation requires a nonblank serviceRole reference, unmanagedvCpus applies only to UNMANAGED ones, and Fargate compute resources refuse ec2KeyPair and instanceRole. A job queue holds at most three compute environments and cannot use both order families. On update, an empty order can clear the old family while setting the other in the same request. Fair-share policies accept computeReservation 0 to 99 and shareDecaySeconds 0 to 604800. Each Region holds at most 50 resources of each kind (local limit). Tags apply to every kind by ARN.",
	},
}

func batchName(input map[string]any, member string) error {
	if name, ok := input[member].(string); ok && !batchNamePattern.MatchString(name) {
		return invalid("%s must be 1 to 128 letters, digits, hyphens or underscores.", member)
	}
	return nil
}

// batchRange applies an inclusive integer range to a member when present.
func batchRange(values map[string]any, member string, low, high int64) error {
	if n, ok := values[member].(int64); ok && (n < low || n > high) {
		return invalid("%s must be from %d to %d.", member, low, high)
	}
	return nil
}

func batchSchedulingPolicy(input map[string]any) error {
	if err := batchName(input, "name"); err != nil {
		return err
	}
	policy, _ := input["fairsharePolicy"].(map[string]any)
	if err := batchRange(policy, "shareDecaySeconds", 0, 604800); err != nil {
		return err
	}
	return batchRange(policy, "computeReservation", 0, 99)
}

func batchConsumableResource(input map[string]any) error {
	if err := batchName(input, "consumableResourceName"); err != nil {
		return err
	}
	if kind, ok := input["resourceType"].(string); ok && kind != "REPLENISHABLE" && kind != "NON_REPLENISHABLE" {
		return invalid("resourceType must be REPLENISHABLE or NON_REPLENISHABLE.")
	}
	total, _ := input["totalQuantity"].(int64)
	if total < 0 {
		return invalid("totalQuantity must be non-negative.")
	}
	input["availableQuantity"] = total
	return nil
}

func batchComputeEnvironment(input map[string]any) error {
	if err := batchName(input, "computeEnvironmentName"); err != nil {
		return err
	}
	resources, hasResources := input["computeResources"].(map[string]any)
	switch input["type"] {
	case "MANAGED":
		if !hasResources {
			return invalid("computeResources is required for a MANAGED compute environment.")
		}
		if _, ok := input["unmanagedvCpus"]; ok {
			return invalid("unmanagedvCpus is only supported for an UNMANAGED compute environment.")
		}
	default:
		if hasResources {
			return invalid("computeResources applies only to a MANAGED compute environment.")
		}
		role, _ := input["serviceRole"].(string)
		if strings.TrimSpace(role) == "" {
			return invalid("serviceRole is required for an UNMANAGED compute environment.")
		}
	}
	if kind, _ := resources["type"].(string); kind == "FARGATE" || kind == "FARGATE_SPOT" {
		for _, member := range []string{"ec2KeyPair", "instanceRole"} {
			if _, ok := resources[member]; ok {
				return invalid("%s does not apply to Fargate compute resources.", member)
			}
		}
		if subnets, _ := resources["subnets"].([]any); len(subnets) > 16 {
			return invalid("Fargate compute resources hold at most 16 subnets.")
		}
	}
	if _, ok := input["eksConfiguration"]; ok {
		input["containerOrchestrationType"] = "EKS"
	} else {
		input["containerOrchestrationType"] = "ECS"
	}
	return nil
}

func batchUpdateComputeEnvironment(document, supplied map[string]any) error {
	if _, ok := supplied["unmanagedvCpus"]; ok && document["type"] != "UNMANAGED" {
		return invalid("unmanagedvCpus is only supported for an UNMANAGED compute environment.")
	}
	return nil
}

func batchCreateJobQueue(input map[string]any) error {
	if _, compute := input["computeEnvironmentOrder"]; compute {
		if _, service := input["serviceEnvironmentOrder"]; service {
			return invalid("computeEnvironmentOrder and serviceEnvironmentOrder cannot both be supplied.")
		}
	}
	if err := batchJobQueue(input); err != nil {
		return err
	}
	return batchQueueTimeLimits(input)
}

func batchUpdateJobQueue(document, _ map[string]any) error {
	compute, _ := document["computeEnvironmentOrder"].([]any)
	service, _ := document["serviceEnvironmentOrder"].([]any)
	if len(compute) != 0 && len(service) != 0 {
		return invalid("computeEnvironmentOrder and serviceEnvironmentOrder cannot both be populated.")
	}
	return batchQueueTimeLimits(document)
}

// Validate using the effective queue configuration: an update may supply just
// new actions, or replace an environment order while retaining its old actions.
func batchQueueTimeLimits(queue map[string]any) error {
	actions, _ := queue["jobStateTimeLimitActions"].([]any)
	if len(actions) == 0 {
		return nil
	}
	compute, _ := queue["computeEnvironmentOrder"].([]any)
	service, _ := queue["serviceEnvironmentOrder"].([]any)
	container, training := len(compute) != 0, len(service) != 0
	switch queue["jobQueueType"] {
	case "ECS", "EKS", "ECS_FARGATE":
		container = true
	case "SAGEMAKER_TRAINING":
		training = true
	}
	if container == training {
		return invalid("Job queue time-limit actions require one unambiguous container or service environment family.")
	}
	want := "CANCEL"
	if training {
		want = "TERMINATE"
	}
	for _, value := range actions {
		rule, _ := value.(map[string]any)
		seconds, ok := Number(rule["maxTimeSeconds"])
		if !ok || seconds < 600 || seconds > 86400 {
			return invalid("maxTimeSeconds must be from 600 to 86400.")
		}
		if rule["action"] != want {
			return invalid("This job queue accepts only %s time-limit actions.", want)
		}
	}
	return nil
}

func batchJobQueue(input map[string]any) error {
	if err := batchName(input, "jobQueueName"); err != nil {
		return err
	}
	environments, _ := input["computeEnvironmentOrder"].([]any)
	if len(environments) > 3 {
		return invalid("A job queue holds at most three compute environments.")
	}
	for _, item := range environments {
		order, _ := item.(map[string]any)
		if err := batchRange(order, "order", 0, 1<<31-1); err != nil {
			return err
		}
	}
	return nil
}

func batchServiceEnvironment(input map[string]any) error {
	return batchName(input, "serviceEnvironmentName")
}

var outposts = &Service{
	Model: "outposts", Title: "AWS Outposts", IAMPrefix: "outposts", Issues: []int{108},
	Resources: []*Resource{
		{Kind: "site", Shape: "Site", ID: "hex:17:os-",
			ARN:       "arn:{partition}:outposts:{region}:{account}:site/{id}",
			ARNMember: "SiteArn", NameMember: "Name", IDMember: "SiteId", Limit: 50,
			Generate: map[string]any{"AccountId": "$account"}},
		{Kind: "outpost", Shape: "Outpost", ID: "hex:17:op-",
			ARN:       "arn:{partition}:outposts:{region}:{account}:outpost/{id}",
			ARNMember: "OutpostArn", NameMember: "Name", IDMember: "OutpostId", Limit: 50,
			Generate: map[string]any{"OwnerId": "$account", "LifeCycleStatus": "REQUESTED"}},
	},
	Ops: []Op{
		{Name: "CreateSite", Kind: Create, Resource: "site", Check: outpostsSite},
		{Name: "GetSite", Kind: Read, Resource: "site", Lookup: "SiteId"},
		{Name: "UpdateSite", Kind: Update, Resource: "site", Lookup: "SiteId", Fields: []string{"Name", "Description", "Notes"}},
		{Name: "DeleteSite", Kind: Delete, Resource: "site", Lookup: "SiteId"},
		{Name: "CreateOutpost", Kind: Create, Resource: "outpost", Check: outpostsOutpost},
		{Name: "GetOutpost", Kind: Read, Resource: "outpost", Lookup: "OutpostId"},
		{Name: "UpdateOutpost", Kind: Update, Resource: "outpost", Lookup: "OutpostId", Fields: []string{"Name", "Description", "SupportedHardwareType"}},
		{Name: "DeleteOutpost", Kind: Delete, Resource: "outpost", Lookup: "OutpostId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Sites and Outposts are stored records: no Outpost hardware, rack, server, asset, order, shipment, connection or capacity exists, nothing is ordered or shipped, and an Outpost stays at LifeCycleStatus REQUESTED because no hardware is ever installed. CreateOrder, CancelOrder, GetOrder, ListOrders, the catalog item, asset, capacity task, connection, billing information, instance type and decommission operations, UpdateSiteRackPhysicalProperties, GetSiteAddress and UpdateSiteAddress return UnknownOperationException with HTTP 501.",
		"Site addresses are not stored: OperatingAddress and ShippingAddress are refused at creation (and so no OperatingAddress country, state or city is reported) because the site address operations are not implemented. RackPhysicalProperties are stored as given without checking that the combination is orderable.",
		"An Outpost's SiteId is stored without being resolved: the site need not exist, an ARN is reduced to its site ID, SiteArn is not reported, and deleting a site does not check the Outposts that name it. AvailabilityZone and AvailabilityZoneId are stored unchecked and neither is required. LifeCycleStatus REQUESTED is inferred, not documented by the pinned model.",
		"ListSites and ListOutposts are not implemented: their NextToken must match ^(\\d+)##(\\S+)$, which the catalog's page tokens do not, so sites and Outposts can be read only by identifier, and both operations return UnknownOperationException with HTTP 501. Site and Outpost names need not be unique, and each Region holds at most 50 sites and 50 Outposts (local limits). Tags apply to both kinds by ARN.",
	},
}

// outpostsSite refuses the address members whose operations are not bound.
func outpostsSite(input map[string]any) error {
	for _, member := range []string{"OperatingAddress", "ShippingAddress"} {
		if _, ok := input[member]; ok {
			return Failure("ValidationException", member+" is not stored by the simulator, which does not implement the site address operations.")
		}
	}
	return nil
}

// outpostsOutpost stores the site by its identifier whether the request named
// it by identifier or by ARN.
func outpostsOutpost(input map[string]any) error {
	if site, ok := input["SiteId"].(string); ok {
		input["SiteId"] = site[strings.LastIndex(site, "/")+1:]
	}
	return nil
}

// pcsNamePattern is the documented name rule of PCS resources. The official
// pattern uses a negative lookahead the engine cannot compile, so it is
// applied here: a letter, then letters, digits or hyphens.
var pcsNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]+$`)

var pcs = &Service{
	Model: "pcs", Title: "AWS Parallel Computing Service", IAMPrefix: "pcs", Issues: []int{109},
	Resources: []*Resource{
		{Kind: "cluster", Shape: "Cluster", Summary: "ClusterSummary", ID: "alnum:10:pcs_", ARN: "arn:{partition}:pcs:{region}:{account}:cluster/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 20, Aliases: map[string]string{"clusterName": "name"},
			Generate: map[string]any{"status": "CREATING", "createdAt": "$now", "modifiedAt": "$now"}},
		{Kind: "computeNodeGroup", Shape: "ComputeNodeGroup", Summary: "ComputeNodeGroupSummary", Parent: "cluster", ParentMember: "clusterId", ID: "alnum:10:pcs_",
			ARN:       "arn:{partition}:pcs:{region}:{account}:computenodegroup/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 50, Aliases: map[string]string{"computeNodeGroupName": "name"},
			Generate: map[string]any{"clusterId": "$parentId", "status": "CREATING", "createdAt": "$now", "modifiedAt": "$now", "purchaseOption": "ONDEMAND"}},
		{Kind: "queue", Shape: "Queue", Summary: "QueueSummary", Parent: "cluster", ParentMember: "clusterId", ID: "alnum:10:pcs_",
			ARN:       "arn:{partition}:pcs:{region}:{account}:queue/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 50, Aliases: map[string]string{"queueName": "name"},
			Generate: map[string]any{"clusterId": "$parentId", "status": "CREATING", "createdAt": "$now", "modifiedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateCluster", Kind: Create, Resource: "cluster", Check: pcsCluster},
		{Name: "GetCluster", Kind: Read, Resource: "cluster", Lookup: "clusterIdentifier"},
		{Name: "ListClusters", Kind: List, Resource: "cluster"},
		{Name: "DeleteCluster", Kind: Delete, Resource: "cluster", Lookup: "clusterIdentifier", Accept: []string{"clientToken"}},
		{Name: "CreateComputeNodeGroup", Kind: Create, Resource: "computeNodeGroup", Parent: "clusterIdentifier", Check: pcsNodeGroup},
		{Name: "GetComputeNodeGroup", Kind: Read, Resource: "computeNodeGroup", Parent: "clusterIdentifier", Lookup: "computeNodeGroupIdentifier"},
		{Name: "ListComputeNodeGroups", Kind: List, Resource: "computeNodeGroup", Parent: "clusterIdentifier"},
		{Name: "UpdateComputeNodeGroup", Kind: Update, Resource: "computeNodeGroup", Parent: "clusterIdentifier", Lookup: "computeNodeGroupIdentifier",
			Fields: []string{"amiId", "subnetIds", "customLaunchTemplate", "purchaseOption", "spotOptions", "scalingConfiguration", "iamInstanceProfileArn", "slurmConfiguration"},
			Accept: []string{"clientToken"}, Touch: []string{"modifiedAt"}, Merged: pcsNodeGroupMerged},
		{Name: "DeleteComputeNodeGroup", Kind: Delete, Resource: "computeNodeGroup", Parent: "clusterIdentifier", Lookup: "computeNodeGroupIdentifier", Accept: []string{"clientToken"}},
		{Name: "CreateQueue", Kind: Create, Resource: "queue", Parent: "clusterIdentifier", Check: pcsQueue},
		{Name: "GetQueue", Kind: Read, Resource: "queue", Parent: "clusterIdentifier", Lookup: "queueIdentifier"},
		{Name: "ListQueues", Kind: List, Resource: "queue", Parent: "clusterIdentifier"},
		{Name: "UpdateQueue", Kind: Update, Resource: "queue", Parent: "clusterIdentifier", Lookup: "queueIdentifier",
			Fields: []string{"computeNodeGroupConfigurations", "slurmConfiguration"}, Accept: []string{"clientToken"}, Touch: []string{"modifiedAt"}},
		{Name: "DeleteQueue", Kind: Delete, Resource: "queue", Parent: "clusterIdentifier", Lookup: "queueIdentifier", Accept: []string{"clientToken"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Clusters, compute node groups and queues are stored configuration records that stay CREATING: no Slurm controller, accounting database, REST endpoint, compute instance, launch or network interface exists, a cluster reports no endpoints or authKey, and no job can be submitted. RegisterComputeNodeGroupInstance, which PCS itself calls for the nodes it launches, and UpdateCluster return UnknownOperationException with HTTP 501.",
		"UpdateCluster is not implemented because its slurmConfiguration is a partial update of nested settings (scale-down time, custom settings, accounting, Slurm REST) that the stored model would replace as a whole. Compute node group and queue updates replace each supplied member as a whole, and the cluster name or ID of a child operation may be either, as the official identifier pattern allows.",
		"CreateQueue requires one or more computeNodeGroupConfigurations entries. UpdateQueue retains its separate replacement semantics. Subnets, security groups, launch templates, AMIs, instance profiles, instance types and the compute node groups a queue names are stored without being resolved: nothing checks that they exist, that subnets share a VPC with the cluster, or that a queue's compute node groups belong to its cluster. A compute node group's scaling minimum must not exceed its maximum, and a SPOT purchase option is the only one that accepts spotOptions. Deleting a cluster that still has compute node groups or queues, and deleting a compute node group that a queue names, behave as stored dependents: the first is refused with ConflictException, the second is not checked.",
		"Names are 3 to 40 characters for clusters and 3 to 25 for compute node groups and queues, begin with a letter and hold letters, digits and hyphens; the official pattern's pcs_ exclusion is implied by that rule. The scheduler type is SLURM with a version stored as given. ARN shapes (cluster/pcs_id, computenodegroup/pcs_id, queue/pcs_id) are inferred. Resource counts per Region (20 clusters, 50 compute node groups and 50 queues per cluster) are local limits.",
	},
}

func pcsName(input map[string]any, member string, high int) error {
	if name, ok := input[member].(string); ok && (!pcsNamePattern.MatchString(name) || len(name) < 3 || len(name) > high) {
		return invalid("%s must be 3 to %d letters, digits or hyphens and begin with a letter.", member, high)
	}
	return nil
}

func pcsCluster(input map[string]any) error {
	return pcsName(input, "clusterName", 40)
}

func pcsNodeGroup(input map[string]any) error {
	if err := pcsName(input, "computeNodeGroupName", 25); err != nil {
		return err
	}
	return pcsNodeGroupMerged(input, input)
}

// pcsNodeGroupMerged applies the scaling and purchase rules to a whole
// compute node group document.
func pcsNodeGroupMerged(document, _ map[string]any) error {
	if scaling, ok := document["scalingConfiguration"].(map[string]any); ok {
		low, _ := Number(scaling["minInstanceCount"])
		high, _ := Number(scaling["maxInstanceCount"])
		if low > high {
			return invalid("minInstanceCount must not exceed maxInstanceCount.")
		}
	}
	if _, ok := document["spotOptions"]; ok && document["purchaseOption"] != "SPOT" {
		return invalid("spotOptions applies only to the SPOT purchase option.")
	}
	return nil
}

func pcsQueue(input map[string]any) error {
	if err := pcsName(input, "queueName", 25); err != nil {
		return err
	}
	groups, _ := input["computeNodeGroupConfigurations"].([]any)
	if len(groups) == 0 {
		return invalid("CreateQueue requires at least one compute node group configuration.")
	}
	return nil
}

var (
	serverlessRepoName   = regexp.MustCompile(`^[a-zA-Z0-9-]{1,140}$`)
	serverlessRepoAuthor = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	serverlessRepoLabel  = regexp.MustCompile(`^[a-zA-Z0-9+\-_:/@]{1,127}$`)
	awsAccountID         = regexp.MustCompile(`^[0-9]{12}$`)
)

var serverlessRepo = &Service{
	Model: "serverlessrepo", Title: "AWS Serverless Application Repository", IAMPrefix: "serverlessrepo", Issues: []int{110},
	Resources: []*Resource{
		{Kind: "application", Shape: "GetApplicationResponse", Summary: "ApplicationSummary", ID: "name", Cascade: true,
			ARN:       "arn:{partition}:serverlessrepo:{region}:{account}:applications/{name}",
			ARNMember: "ApplicationId", NameMember: "Name", Unique: true, Limit: 50,
			Generate: map[string]any{"CreationTime": "$now", "IsVerifiedAuthor": false}},
		{Kind: "policy", Shape: "PutApplicationPolicyRequest", ID: "name", Parent: "application", Limit: 1,
			ARN:        "arn:{partition}:serverlessrepo:{region}:{account}:applications/{parentName}/policy",
			NameMember: "ApplicationId"},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application", Check: serverlessRepoCreate},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "ApplicationId"},
		{Name: "ListApplications", Kind: List, Resource: "application"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "ApplicationId",
			Fields: []string{"Author", "Description", "HomePageUrl", "Labels"}, Check: serverlessRepoMembers},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "ApplicationId"},
		{Name: "PutApplicationPolicy", Kind: Put, Resource: "policy", Parent: "ApplicationId", AuthorizeParent: true, Lookup: "ApplicationId", Check: serverlessRepoPolicy},
		{Name: "GetApplicationPolicy", Kind: Read, Resource: "policy", Parent: "ApplicationId", AuthorizeParent: true, Lookup: "ApplicationId"},
	},
	Bounds: []string{
		"Applications and their sharing policies are stored records: no SAM template, source archive, readme, license text or application version is stored, nothing is deployed, and no S3 object or CloudFormation change set exists. Application versions (CreateApplicationVersion, ListApplicationVersions), CreateCloudFormationChangeSet, CreateCloudFormationTemplate, GetCloudFormationTemplate, ListApplicationDependencies and UnshareApplication return UnknownOperationException with HTTP 501.",
		"An application is created without a version: SemanticVersion, TemplateBody, TemplateUrl, SourceCodeUrl, SourceCodeArchiveUrl, LicenseBody, LicenseUrl, ReadmeBody and ReadmeUrl are refused (the official responses would return generated S3 URLs and parameter definitions derived from the template, which the simulator does not produce), so no application reports a Version, LicenseUrl, ReadmeUrl or VerifiedAuthorUrl and IsVerifiedAuthor is always false. UpdateApplication changes Author, Description, HomePageUrl and Labels only, and GetApplication refuses SemanticVersion.",
		"The application ID is its ARN (arn:aws:serverlessrepo:region:account:applications/name). Names are unique per Region, 1 to 140 letters, digits or hyphens; authors are 1 to 127 lowercase letters, digits or single hyphens, not starting or ending with a hyphen; descriptions are 1 to 256 characters; at most 10 labels of 1 to 127 characters. SpdxLicenseId is stored without checking it against the SPDX list.",
		"The application policy is stored, never evaluated: it shares nothing and does not grant any account access. PutApplicationPolicy replaces the whole statement list, principals must be 12-digit account IDs or *, and the statement actions are stored without checking them against the documented Application Permissions. GetApplicationPolicy fails with NotFoundException for an application whose policy was never put, where AWS returns an empty statement list. Deleting an application deletes its policy. At most 50 applications exist per Region (local limit); the service has no tag API.",
	},
}

func serverlessRepoCreate(input map[string]any) error {
	for _, member := range []string{"SemanticVersion", "TemplateBody", "TemplateUrl", "SourceCodeUrl", "SourceCodeArchiveUrl", "LicenseBody", "LicenseUrl", "ReadmeBody", "ReadmeUrl"} {
		if _, ok := input[member]; ok {
			return invalid("%s is not supported: the simulator stores applications without versions, templates, licenses or readmes.", member)
		}
	}
	if name, ok := input["Name"].(string); ok && !serverlessRepoName.MatchString(name) {
		return invalid("Name must be 1 to 140 letters, digits or hyphens.")
	}
	return serverlessRepoMembers(input)
}

// serverlessRepoMembers applies the documented author, description and label
// rules to whichever of those members a request supplies.
func serverlessRepoMembers(input map[string]any) error {
	if author, ok := input["Author"].(string); ok && (len(author) > 127 || !serverlessRepoAuthor.MatchString(author) || strings.Contains(author, "--")) {
		return invalid("Author must be 1 to 127 lowercase letters, digits or single hyphens, and must not start or end with a hyphen.")
	}
	if description, ok := input["Description"].(string); ok && (description == "" || len(description) > 256) {
		return invalid("Description must be 1 to 256 characters.")
	}
	labels, _ := input["Labels"].([]any)
	if len(labels) > 10 {
		return invalid("An application has at most 10 labels.")
	}
	for _, item := range labels {
		if label, _ := item.(string); !serverlessRepoLabel.MatchString(label) {
			return invalid("Labels are 1 to 127 letters, digits or + - _ : / @.")
		}
	}
	return nil
}

// serverlessRepoPolicy requires the application ARN that identifies the
// application and account IDs or * as the principals of every statement.
func serverlessRepoPolicy(input map[string]any) error {
	if id, _ := input["ApplicationId"].(string); !strings.HasPrefix(id, "arn:") {
		return invalid("ApplicationId must be the application ARN.")
	}
	statements, _ := input["Statements"].([]any)
	for _, item := range statements {
		statement, _ := item.(map[string]any)
		principals, _ := statement["Principals"].([]any)
		for _, principal := range principals {
			if text, _ := principal.(string); text != "*" && !awsAccountID.MatchString(text) {
				return invalid("Principals are 12-digit AWS account IDs or *.")
			}
		}
	}
	return nil
}
