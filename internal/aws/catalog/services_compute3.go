package catalog

import (
	"regexp"
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
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 50},
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
		{Name: "CreateJobQueue", Kind: Create, Resource: "jobQueue", Check: batchJobQueue},
		{Name: "DescribeJobQueues", Kind: List, Resource: "jobQueue", Names: "jobQueues"},
		{Name: "UpdateJobQueue", Kind: Update, Resource: "jobQueue", Lookup: "jobQueue",
			Fields: []string{"state", "schedulingPolicyArn", "priority", "computeEnvironmentOrder", "serviceEnvironmentOrder", "jobStateTimeLimitActions"}, Check: batchJobQueue},
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
		"References are stored without being resolved: the compute environments and service environments of a job queue order, subnets, security groups, launch templates, instance roles, service roles, EKS clusters and Spot fleet roles are not checked, a queue is VALID at once even if its compute environments are not, and UpdateComputeEnvironment changes only state, unmanagedvCpus, serviceRole, updatePolicy and context (computeResources updates are refused). A schedulingPolicyArn must name a stored scheduling policy, but deleting a scheduling policy, compute environment or service environment that a queue still uses succeeds, and a compute environment need not be DISABLED before deletion.",
		"Names are 1 to 128 letters, digits, hyphens or underscores. A compute environment's containerOrchestrationType is ECS, or EKS when eksConfiguration is given; MANAGED compute environments require computeResources, unmanagedvCpus applies only to UNMANAGED ones, and Fargate compute resources refuse ec2KeyPair and instanceRole. A job queue holds at most three compute environments. Fair-share policies accept computeReservation 0 to 99 and shareDecaySeconds 0 to 604800. Each Region holds at most 50 resources of each kind (local limit). Tags apply to every kind by ARN.",
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
