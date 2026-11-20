package catalog

import (
	"regexp"
	"strings"
)

// Reviewed bounded specifications for AWS Deadline Cloud, AWS Database
// Migration Service, AWS Cost Explorer, AWS Billing and Cost Management data
// exports, Amazon Braket, AWS Artifact and Amazon CloudWatch investigations.
// Each binds only official operations whose effect is stored configuration:
// nothing renders, migrates, runs a quantum task, queries billing data or
// investigates anything.

var deadlineTaggable = regexp.MustCompile(`^arn:[a-z-]+:deadline:[a-z0-9-]+:[0-9]{12}:farm/farm-[0-9a-f]{32}(/(queue|fleet)/(queue|fleet)-[0-9a-f]{32})?$`)

var deadlineCloud = &Service{
	Model: "deadline", Title: "AWS Deadline Cloud", IAMPrefix: "deadline", Issues: []int{218},
	Resources: []*Resource{
		{Kind: "farm", Shape: "GetFarmResponse", Summary: "FarmSummary", ID: "hex:32:farm-",
			ARN:        "arn:{partition}:deadline:{region}:{account}:farm/{id}",
			NameMember: "displayName", IDMember: "farmId", Limit: 10,
			Generate: map[string]any{"createdAt": "$now", "createdBy": "$account"}},
		{Kind: "queue", Shape: "GetQueueResponse", Summary: "QueueSummary", ID: "hex:32:queue-", Parent: "farm", ParentMember: "farmId",
			ARN:        "arn:{partition}:deadline:{region}:{account}:farm/{parent}/queue/{id}",
			NameMember: "displayName", IDMember: "queueId", Limit: 50,
			Generate: map[string]any{"farmId": "$parentId", "status": "IDLE", "createdAt": "$now", "createdBy": "$account"}},
		{Kind: "fleet", Shape: "GetFleetResponse", Summary: "FleetSummary", ID: "hex:32:fleet-", Parent: "farm", ParentMember: "farmId",
			ARN:        "arn:{partition}:deadline:{region}:{account}:farm/{parent}/fleet/{id}",
			NameMember: "displayName", IDMember: "fleetId", Limit: 50,
			Generate: map[string]any{"farmId": "$parentId", "status": "ACTIVE", "workerCount": 0, "targetWorkerCount": 0, "createdAt": "$now", "createdBy": "$account"}},
		{Kind: "storageProfile", Shape: "GetStorageProfileResponse", Summary: "StorageProfileSummary", ID: "hex:32:sp-", Parent: "farm", ParentMember: "farmId",
			ARN:        "arn:{partition}:deadline:{region}:{account}:farm/{parent}/storage-profile/{id}",
			NameMember: "displayName", IDMember: "storageProfileId", Limit: 50,
			Generate: map[string]any{"createdAt": "$now", "createdBy": "$account"}},
		{Kind: "limit", Shape: "GetLimitResponse", Summary: "LimitSummary", ID: "hex:32:limit-", Parent: "farm", ParentMember: "farmId",
			ARN:        "arn:{partition}:deadline:{region}:{account}:farm/{parent}/limit/{id}",
			NameMember: "displayName", IDMember: "limitId", Limit: 50,
			Generate: map[string]any{"farmId": "$parentId", "currentCount": 0, "createdAt": "$now", "createdBy": "$account"}},
		{Kind: "budget", Shape: "GetBudgetResponse", Summary: "BudgetSummary", ID: "hex:32:budget-", Parent: "farm", ParentMember: "farmId",
			ARN:        "arn:{partition}:deadline:{region}:{account}:farm/{parent}/budget/{id}",
			NameMember: "displayName", IDMember: "budgetId", Limit: 50,
			Generate: map[string]any{"status": "ACTIVE", "usages": map[string]any{"approximateDollarUsage": 0}, "createdAt": "$now", "createdBy": "$account"}},
	},
	Ops: []Op{
		{Name: "CreateFarm", Kind: Create, Resource: "farm"},
		{Name: "GetFarm", Kind: Read, Resource: "farm", Lookup: "farmId"},
		{Name: "ListFarms", Kind: List, Resource: "farm"},
		{Name: "UpdateFarm", Kind: Update, Resource: "farm", Lookup: "farmId", Fields: []string{"displayName", "description"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteFarm", Kind: Delete, Resource: "farm", Lookup: "farmId"},
		{Name: "CreateQueue", Kind: Create, Resource: "queue", Parent: "farmId", Check: deadlineQueueDefaults},
		{Name: "GetQueue", Kind: Read, Resource: "queue", Parent: "farmId", Lookup: "queueId"},
		{Name: "ListQueues", Kind: List, Resource: "queue", Parent: "farmId", Filters: map[string]string{"status": "status"}},
		{Name: "UpdateQueue", Kind: Update, Resource: "queue", Parent: "farmId", Lookup: "queueId", Accept: []string{"clientToken"}, Touch: []string{"updatedAt"},
			Fields: []string{"displayName", "description", "defaultBudgetAction", "jobAttachmentSettings", "roleArn", "jobRunAsUser"}},
		{Name: "DeleteQueue", Kind: Delete, Resource: "queue", Parent: "farmId", Lookup: "queueId"},
		{Name: "CreateFleet", Kind: Create, Resource: "fleet", Parent: "farmId", Check: deadlineFleet},
		{Name: "GetFleet", Kind: Read, Resource: "fleet", Parent: "farmId", Lookup: "fleetId"},
		{Name: "ListFleets", Kind: List, Resource: "fleet", Parent: "farmId", Filters: map[string]string{"displayName": "displayName", "status": "status"}},
		{Name: "UpdateFleet", Kind: Update, Resource: "fleet", Parent: "farmId", Lookup: "fleetId", Accept: []string{"clientToken"}, Touch: []string{"updatedAt"},
			Fields: []string{"displayName", "description", "roleArn", "minWorkerCount", "maxWorkerCount", "configuration", "hostConfiguration"},
			Check:  deadlineFleetUpdate, Merged: deadlineFleetWorkers},
		{Name: "DeleteFleet", Kind: Delete, Resource: "fleet", Parent: "farmId", Lookup: "fleetId", Accept: []string{"clientToken"}},
		{Name: "CreateStorageProfile", Kind: Create, Resource: "storageProfile", Parent: "farmId"},
		{Name: "GetStorageProfile", Kind: Read, Resource: "storageProfile", Parent: "farmId", Lookup: "storageProfileId"},
		{Name: "ListStorageProfiles", Kind: List, Resource: "storageProfile", Parent: "farmId"},
		{Name: "UpdateStorageProfile", Kind: Update, Resource: "storageProfile", Parent: "farmId", Lookup: "storageProfileId", Accept: []string{"clientToken"},
			Fields: []string{"displayName", "osFamily"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteStorageProfile", Kind: Delete, Resource: "storageProfile", Parent: "farmId", Lookup: "storageProfileId"},
		{Name: "CreateLimit", Kind: Create, Resource: "limit", Parent: "farmId", Check: deadlineLimit},
		{Name: "GetLimit", Kind: Read, Resource: "limit", Parent: "farmId", Lookup: "limitId"},
		{Name: "ListLimits", Kind: List, Resource: "limit", Parent: "farmId"},
		{Name: "UpdateLimit", Kind: Update, Resource: "limit", Parent: "farmId", Lookup: "limitId", Fields: []string{"displayName", "description", "maxCount"},
			Check: deadlineLimit, Touch: []string{"updatedAt"}},
		{Name: "DeleteLimit", Kind: Delete, Resource: "limit", Parent: "farmId", Lookup: "limitId"},
		{Name: "CreateBudget", Kind: Create, Resource: "budget", Parent: "farmId"},
		{Name: "GetBudget", Kind: Read, Resource: "budget", Parent: "farmId", Lookup: "budgetId"},
		{Name: "ListBudgets", Kind: List, Resource: "budget", Parent: "farmId", Filters: map[string]string{"status": "status"}},
		{Name: "UpdateBudget", Kind: Update, Resource: "budget", Parent: "farmId", Lookup: "budgetId", Accept: []string{"clientToken"},
			Fields: []string{"displayName", "description", "status", "approximateDollarLimit", "schedule"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteBudget", Kind: Delete, Resource: "budget", Parent: "farmId", Lookup: "budgetId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn", Check: deadlineTagTarget},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn", Check: deadlineTagTarget},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn", Check: deadlineTagTarget},
	},
	Bounds: []string{
		"Farms, queues, fleets, storage profiles, limits and budgets are stored records: no job, step, task, session, worker, license endpoint, metered product or monitor exists, nothing is rendered or scheduled, no capacity is provisioned and no worker joins a fleet. CreateJob, CreateWorker, CreateMonitor, CreateLicenseEndpoint, the Assume*Role operations and every other official operation not listed here return UnknownOperationException with HTTP 501.",
		"A fleet is a record that is ACTIVE at once with workerCount and targetWorkerCount 0 and never reports capabilities; its worker scripts and host configuration are stored and never run. A queue stays IDLE, a limit reports currentCount 0 and a budget reports no consumed usage and stays ACTIVE until updated; no budget action is ever applied. createdBy reports the account ID instead of a user identity, and updatedBy is not reported.",
		"Queue-fleet associations, queue-limit associations, queue environments and farm, queue, fleet and job membership are not implemented, so a queue never schedules on a fleet and a limit never throttles. The add and remove list members of UpdateQueue, UpdateStorageProfile and UpdateBudget (for example allowedStorageProfileIdsToAdd) and the member filter principalId of the list operations are refused instead of being ignored. Referenced IDs (allowed storage profiles, the budget's queue), roles and the KMS key are not resolved.",
		"Tags are accepted at creation and by TagResource, UntagResource and ListTagsForResource for farms, queues and fleets only; the ARN of any other resource fails with ValidationException. A farm reports no kmsKeyArn unless one was supplied, and KMS is never called. Display names are not unique. Resource counts per farm and per Region are local limits, not Service Quotas. Deleting a farm that still has queues, fleets, storage profiles, limits or budgets is refused with ValidationException, because DeleteFarm models no conflict error.",
	},
}

// deadlineQueueDefaults applies the documented default budget action.
func deadlineQueueDefaults(input map[string]any) error {
	if _, ok := input["defaultBudgetAction"]; !ok {
		input["defaultBudgetAction"] = "NONE"
	}
	return nil
}

// deadlineFleetConfiguration requires exactly one fleet kind, as the union
// of customer managed and service managed Amazon EC2 fleets documents.
func deadlineFleetConfiguration(input map[string]any) error {
	if configuration, ok := input["configuration"].(map[string]any); ok && len(configuration) != 1 {
		return invalid("A fleet configuration holds exactly one of customerManaged or serviceManagedEc2.")
	}
	return nil
}

func deadlineFleet(input map[string]any) error {
	if err := deadlineFleetConfiguration(input); err != nil {
		return err
	}
	if _, ok := input["minWorkerCount"]; !ok {
		input["minWorkerCount"] = int64(0)
	}
	return deadlineWorkerRange(input["minWorkerCount"], input["maxWorkerCount"])
}

func deadlineFleetUpdate(input map[string]any) error {
	return deadlineFleetConfiguration(input)
}

// deadlineWorkerRange refuses a minimum above the maximum; a maximum of -1
// documents no restriction.
func deadlineWorkerRange(minimum, maximum any) error {
	low, lowOK := Number(minimum)
	high, highOK := Number(maximum)
	if lowOK && highOK && high >= 0 && low > high {
		return invalid("minWorkerCount must not exceed maxWorkerCount.")
	}
	return nil
}

// deadlineFleetWorkers applies the same range to the merged fleet.
func deadlineFleetWorkers(document, _ map[string]any) error {
	return deadlineWorkerRange(document["minWorkerCount"], document["maxWorkerCount"])
}

// deadlineLimit applies the documented rule that maxCount must not be 0.
func deadlineLimit(input map[string]any) error {
	if count, ok := Number(input["maxCount"]); ok && count == 0 {
		return invalid("maxCount must not be 0; use -1 for no restriction.")
	}
	return nil
}

// deadlineTagTarget limits tagging to farms, queues and fleets.
func deadlineTagTarget(input map[string]any) error {
	arn, _ := input["resourceArn"].(string)
	if !deadlineTaggable.MatchString(arn) || strings.Contains(arn, "/queue/fleet-") || strings.Contains(arn, "/fleet/queue-") {
		return invalid("resourceArn must be the ARN of a farm, queue or fleet.")
	}
	return nil
}
