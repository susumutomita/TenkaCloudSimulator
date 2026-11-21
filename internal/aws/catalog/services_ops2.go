package catalog

import (
	"regexp"
	"strings"
	"time"
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

var (
	dmsSubnetGroupName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	dmsDataProviderARN = regexp.MustCompile(`^arn:[a-z-]+:dms:[a-z0-9-]+:[0-9]{12}:data-provider:[a-z0-9]{26}$`)
	dmsInstanceARN     = regexp.MustCompile(`^arn:[a-z-]+:dms:[a-z0-9-]+:[0-9]{12}:instance-profile:[a-z0-9]{26}$`)
	dmsEngines         = map[string]bool{"aurora": true, "aurora-postgresql": true, "mysql": true, "oracle": true, "postgres": true, "sqlserver": true,
		"redshift": true, "mariadb": true, "mongodb": true, "db2": true, "db2-zos": true, "docdb": true, "sybase": true}
)

var databaseMigration = &Service{
	Model: "dms", Title: "AWS Database Migration Service", IAMPrefix: "dms", Issues: []int{228},
	Resources: []*Resource{
		{Kind: "replicationSubnetGroup", Shape: "ReplicationSubnetGroup", ID: "name",
			ARN:        "arn:{partition}:dms:{region}:{account}:subgrp:{name}",
			NameMember: "ReplicationSubnetGroupIdentifier", Unique: true, Limit: 20,
			Generate: map[string]any{"SubnetGroupStatus": "Complete"}},
		{Kind: "eventSubscription", Shape: "EventSubscription", ID: "name",
			ARN:        "arn:{partition}:dms:{region}:{account}:es:{name}",
			NameMember: "CustSubscriptionId", Unique: true, Limit: 20,
			Aliases:  map[string]string{"SubscriptionName": "CustSubscriptionId", "EventCategories": "EventCategoriesList", "SourceIds": "SourceIdsList"},
			Generate: map[string]any{"CustomerAwsId": "$account", "Status": "active", "SubscriptionCreationTime": "$now", "Enabled": true}},
		{Kind: "dataProvider", Shape: "DataProvider", ID: "alnum:26",
			ARN:        "arn:{partition}:dms:{region}:{account}:data-provider:{id}",
			ARNMember:  "DataProviderArn",
			NameMember: "DataProviderName", Unique: true, Limit: 30,
			Generate: map[string]any{"DataProviderName": "$name", "DataProviderCreationTime": "$now"}},
		{Kind: "instanceProfile", Shape: "InstanceProfile", ID: "alnum:26",
			ARN:        "arn:{partition}:dms:{region}:{account}:instance-profile:{id}",
			ARNMember:  "InstanceProfileArn",
			NameMember: "InstanceProfileName", Unique: true, Limit: 30,
			Generate: map[string]any{"InstanceProfileName": "$name", "InstanceProfileCreationTime": "$now", "PubliclyAccessible": true}},
		{Kind: "migrationProject", Shape: "MigrationProject", ID: "alnum:26",
			ARN:        "arn:{partition}:dms:{region}:{account}:migration-project:{id}",
			ARNMember:  "MigrationProjectArn",
			NameMember: "MigrationProjectName", Unique: true, Limit: 30,
			Aliases:  map[string]string{"InstanceProfileIdentifier": "InstanceProfileArn"},
			Refs:     map[string]string{"InstanceProfileArn": "instanceProfile"},
			Generate: map[string]any{"MigrationProjectName": "$name", "MigrationProjectCreationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateReplicationSubnetGroup", Kind: Create, Resource: "replicationSubnetGroup", Check: dmsSubnetGroup},
		{Name: "DescribeReplicationSubnetGroups", Kind: List, Resource: "replicationSubnetGroup"},
		{Name: "ModifyReplicationSubnetGroup", Kind: Update, Resource: "replicationSubnetGroup", Lookup: "ReplicationSubnetGroupIdentifier",
			Fields: []string{"ReplicationSubnetGroupDescription", "Subnets"}, Check: dmsSubnetGroup},
		{Name: "DeleteReplicationSubnetGroup", Kind: Delete, Resource: "replicationSubnetGroup", Lookup: "ReplicationSubnetGroupIdentifier"},
		{Name: "CreateEventSubscription", Kind: Create, Resource: "eventSubscription", Check: dmsEventSubscription},
		{Name: "DescribeEventSubscriptions", Kind: List, Resource: "eventSubscription", Filters: map[string]string{"SubscriptionName": "CustSubscriptionId"}},
		{Name: "ModifyEventSubscription", Kind: Update, Resource: "eventSubscription", Lookup: "SubscriptionName",
			Fields: []string{"SnsTopicArn", "SourceType", "EventCategories", "Enabled"}, Check: dmsEventSubscription},
		{Name: "DeleteEventSubscription", Kind: Delete, Resource: "eventSubscription", Lookup: "SubscriptionName"},
		{Name: "CreateDataProvider", Kind: Create, Resource: "dataProvider", Check: dmsDataProvider},
		{Name: "DescribeDataProviders", Kind: List, Resource: "dataProvider"},
		{Name: "ModifyDataProvider", Kind: Update, Resource: "dataProvider", Lookup: "DataProviderIdentifier", Accept: []string{"ExactSettings"},
			Fields: []string{"DataProviderName", "Description", "Engine", "Virtual", "Settings"}, Check: dmsDataProviderUpdate},
		{Name: "DeleteDataProvider", Kind: Delete, Resource: "dataProvider", Lookup: "DataProviderIdentifier"},
		{Name: "CreateInstanceProfile", Kind: Create, Resource: "instanceProfile"},
		{Name: "DescribeInstanceProfiles", Kind: List, Resource: "instanceProfile"},
		{Name: "ModifyInstanceProfile", Kind: Update, Resource: "instanceProfile", Lookup: "InstanceProfileIdentifier",
			Fields: []string{"AvailabilityZone", "KmsKeyArn", "PubliclyAccessible", "NetworkType", "InstanceProfileName", "Description", "SubnetGroupIdentifier", "VpcSecurityGroups"}},
		{Name: "DeleteInstanceProfile", Kind: Delete, Resource: "instanceProfile", Lookup: "InstanceProfileIdentifier"},
		{Name: "CreateMigrationProject", Kind: Create, Resource: "migrationProject", Check: dmsMigrationProject},
		{Name: "DescribeMigrationProjects", Kind: List, Resource: "migrationProject"},
		{Name: "ModifyMigrationProject", Kind: Update, Resource: "migrationProject", Lookup: "MigrationProjectIdentifier",
			Fields: []string{"MigrationProjectName", "SourceDataProviderDescriptors", "TargetDataProviderDescriptors", "InstanceProfileArn", "TransformationRules", "Description", "SchemaConversionApplicationAttributes"},
			Check:  dmsMigrationProject},
		{Name: "DeleteMigrationProject", Kind: Delete, Resource: "migrationProject", Lookup: "MigrationProjectIdentifier"},
	},
	Bounds: []string{
		"Replication subnet groups, event subscriptions, data providers, instance profiles and migration projects are stored configuration records: no replication instance, replication task, endpoint, connection, assessment, schema conversion or migration exists, and nothing connects to any database. Endpoints (which carry database credentials), certificates, tagging (AddTagsToResource, RemoveTagsFromResource and ListTagsForResource), replication instances and tasks, serverless replication configurations, Fleet Advisor, metadata models, recommendations and every Start, Stop, Reload, Refresh, Test and Apply operation return UnknownOperationException with HTTP 501.",
		"The Filters member of the Describe operations is refused except DescribeEventSubscriptions SubscriptionName; Describe operations return every stored record, and an unknown SubscriptionName returns an empty list instead of a not-found fault. Event subscription categories, source identifiers and SNS topics, subnet IDs, security groups, KMS keys, roles and Availability Zones are stored without being checked, and no notification is sent.",
		"A replication subnet group needs two or more subnet IDs and reports no VpcId; the Availability Zone coverage of the subnets is not checked. Data provider settings hold the server name, port, database name and SSL mode as supplied and are never contacted; ModifyDataProvider accepts Settings only with ExactSettings set to true, which replaces the stored settings, because the merge form is not implemented. Instance profiles report no network address.",
		"A migration project must name its instance profile by ARN, and the profile must exist; its source and target data provider descriptors must name data providers by ARN and are not resolved, so the descriptors report DataProviderArn without DataProviderName, and the project reports InstanceProfileArn without InstanceProfileName. SecretsManagerSecretId values are stored as references and never read. Deleting an instance profile or data provider does not check the projects that use it. Tags are not stored: the official DMS tag structure carries a third ResourceArn member that the catalog tag model cannot represent, so the Tags member of the create operations is refused. Record counts are local limits, not Service Quotas.",
	},
}

// dmsSubnetGroup applies the documented identifier rule (stored lowercase,
// never default) and turns SubnetIds into the official Subnets list.
func dmsSubnetGroup(input map[string]any) error {
	if name, ok := input["ReplicationSubnetGroupIdentifier"].(string); ok {
		if !dmsSubnetGroupName.MatchString(name) || strings.EqualFold(name, "default") {
			return invalid("ReplicationSubnetGroupIdentifier must be 1 to 255 letters, digits, periods, underscores or hyphens and must not be default.")
		}
		input["ReplicationSubnetGroupIdentifier"] = strings.ToLower(name)
	}
	ids, ok := input["SubnetIds"].([]any)
	if !ok {
		return nil
	}
	if len(ids) < 2 {
		return invalid("A replication subnet group needs two or more subnet IDs.")
	}
	subnets := make([]any, 0, len(ids))
	for _, id := range ids {
		subnets = append(subnets, map[string]any{"SubnetIdentifier": id, "SubnetStatus": "Active"})
	}
	delete(input, "SubnetIds")
	input["Subnets"] = subnets
	return nil
}

// dmsEventSubscription applies the documented source types and the rule that
// source identifiers need a source type.
func dmsEventSubscription(input map[string]any) error {
	if name, ok := input["SubscriptionName"].(string); ok && len(name) >= 255 {
		return invalid("SubscriptionName must be less than 255 characters.")
	}
	sourceType, hasType := input["SourceType"].(string)
	if hasType && sourceType != "replication-instance" && sourceType != "replication-task" {
		return invalid("SourceType must be replication-instance or replication-task.")
	}
	if ids, ok := input["SourceIds"].([]any); ok && len(ids) > 0 && !hasType {
		return invalid("SourceIds need a SourceType.")
	}
	return nil
}

// dmsDataProviderSettings requires one engine member, as the settings union
// documents, and a documented engine.
func dmsDataProvider(input map[string]any) error {
	if engine, ok := input["Engine"].(string); ok && !dmsEngines[engine] {
		return invalid("Engine is not one of the documented data provider engines.")
	}
	if settings, ok := input["Settings"].(map[string]any); ok && len(settings) != 1 {
		return invalid("Settings hold exactly one engine settings member.")
	}
	return nil
}

func dmsDataProviderUpdate(input map[string]any) error {
	if err := dmsDataProvider(input); err != nil {
		return err
	}
	if _, ok := input["Settings"]; ok && input["ExactSettings"] != true {
		return invalid("Settings can be changed only with ExactSettings set to true.")
	}
	return nil
}

// dmsMigrationProject requires ARNs where the model offers a name or ARN, so
// the stored record never reports a name as an ARN.
func dmsMigrationProject(input map[string]any) error {
	if id, ok := input["InstanceProfileIdentifier"].(string); ok {
		if !dmsInstanceARN.MatchString(id) {
			return invalid("InstanceProfileIdentifier must be the ARN of an instance profile.")
		}
		input["InstanceProfileArn"] = id
		delete(input, "InstanceProfileIdentifier")
	}
	for _, key := range []string{"SourceDataProviderDescriptors", "TargetDataProviderDescriptors"} {
		descriptors, ok := input[key].([]any)
		if !ok {
			continue
		}
		if len(descriptors) == 0 {
			return invalid("%s needs one data provider.", key)
		}
		converted := make([]any, 0, len(descriptors))
		for _, item := range descriptors {
			d, _ := item.(map[string]any)
			id, _ := d["DataProviderIdentifier"].(string)
			if !dmsDataProviderARN.MatchString(id) {
				return invalid("DataProviderIdentifier must be the ARN of a data provider.")
			}
			out := map[string]any{"DataProviderArn": id}
			for _, member := range []string{"SecretsManagerSecretId", "SecretsManagerAccessRoleArn"} {
				if v, ok := d[member]; ok {
					out[member] = v
				}
			}
			converted = append(converted, out)
		}
		input[key] = converted
	}
	return nil
}

var (
	ceStart      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-01T00:00:00(\.000)?Z$`)
	ceMonitorARN = regexp.MustCompile(`^arn:[a-z-]+:ce::[0-9]{12}:anomalymonitor/[0-9a-f-]{36}$`)
)

var costExplorer = &Service{
	Model: "ce", Title: "AWS Cost Explorer", IAMPrefix: "ce", Issues: []int{101, 282}, Global: true,
	Resources: []*Resource{
		{Kind: "costCategory", Shape: "CostCategory", Summary: "CostCategoryReference", ID: "uuid",
			ARN: "arn:{partition}:ce::{account}:costcategory/{id}", ARNMember: "CostCategoryArn",
			NameMember: "Name", Unique: true, Limit: 20},
		{Kind: "anomalyMonitor", Shape: "AnomalyMonitor", ID: "uuid",
			ARN: "arn:{partition}:ce::{account}:anomalymonitor/{id}", ARNMember: "MonitorArn",
			NameMember: "MonitorName", Limit: 100,
			Generate: map[string]any{"CreationDate": "$now", "DimensionalValueCount": 0}},
		{Kind: "anomalySubscription", Shape: "AnomalySubscription", ID: "uuid",
			ARN: "arn:{partition}:ce::{account}:anomalysubscription/{id}", ARNMember: "SubscriptionArn",
			NameMember: "SubscriptionName", Limit: 100,
			Generate: map[string]any{"AccountId": "$account"}},
	},
	Ops: []Op{
		{Name: "CreateCostCategoryDefinition", Kind: Create, Resource: "costCategory", Check: ceCostCategory},
		{Name: "DescribeCostCategoryDefinition", Kind: Read, Resource: "costCategory", Lookup: "CostCategoryArn", Accept: []string{"EffectiveOn"}},
		{Name: "ListCostCategoryDefinitions", Kind: List, Resource: "costCategory", Accept: []string{"EffectiveOn"}},
		{Name: "UpdateCostCategoryDefinition", Kind: Update, Resource: "costCategory", Lookup: "CostCategoryArn",
			Fields: []string{"EffectiveStart", "RuleVersion", "Rules", "DefaultValue", "SplitChargeRules"}, Check: ceCostCategory, Merged: ceCostCategoryReplace},
		{Name: "DeleteCostCategoryDefinition", Kind: Delete, Resource: "costCategory", Lookup: "CostCategoryArn"},
		{Name: "CreateAnomalyMonitor", Kind: Create, Resource: "anomalyMonitor", From: "AnomalyMonitor", Check: ceAnomalyMonitor},
		{Name: "GetAnomalyMonitors", Kind: List, Resource: "anomalyMonitor", Names: "MonitorArnList"},
		{Name: "UpdateAnomalyMonitor", Kind: Update, Resource: "anomalyMonitor", Lookup: "MonitorArn", Fields: []string{"MonitorName"}, Touch: []string{"LastUpdatedDate"}},
		{Name: "DeleteAnomalyMonitor", Kind: Delete, Resource: "anomalyMonitor", Lookup: "MonitorArn"},
		{Name: "CreateAnomalySubscription", Kind: Create, Resource: "anomalySubscription", From: "AnomalySubscription", Check: ceAnomalySubscription},
		{Name: "GetAnomalySubscriptions", Kind: List, Resource: "anomalySubscription", Names: "SubscriptionArnList"},
		{Name: "UpdateAnomalySubscription", Kind: Update, Resource: "anomalySubscription", Lookup: "SubscriptionArn",
			Fields: []string{"Threshold", "Frequency", "MonitorArnList", "Subscribers", "SubscriptionName", "ThresholdExpression"}, Check: ceAnomalySubscription},
		{Name: "DeleteAnomalySubscription", Kind: Delete, Resource: "anomalySubscription", Lookup: "SubscriptionArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Cost categories, anomaly monitors and anomaly subscriptions are stored configuration: no cost, usage, forecast, anomaly, recommendation, reservation, Savings Plans or tag-and-dimension value is read, computed or invented. Every Get cost, usage, forecast, anomaly and recommendation operation, the cost allocation tag, commitment analysis and cost comparison operations return UnknownOperationException with HTTP 501.",
		"Cost category rules are stored without being evaluated, so nothing is categorized: the effective start defaults to the first day of the current month, no ProcessingStatus, NumberOfRules or Values are reported, the EffectiveOn filter is accepted but only the current definition exists, and updating a definition replaces its rules and drops a default value or split charge rules the request omits. Updates and deletes keep no history, so deleting a category reports no EffectiveEnd.",
		"Anomaly monitors never evaluate: DimensionalValueCount stays 0 and LastEvaluatedDate is not reported. DIMENSIONAL monitors need a MonitorDimension, CUSTOM monitors a MonitorSpecification and no dimension. Anomaly subscriptions must name monitor ARNs in the right form, but the monitors are not looked up and a deleted monitor stays listed; subscribers are stored and never notified or confirmed, so no Status is reported. The MonitorArn filter of GetAnomalySubscriptions and the ThresholdExpression and Threshold rules of AWS are not evaluated.",
		"The service is global with ARNs that name no Region. A missing monitor or subscription is reported with ResourceNotFoundException instead of the official UnknownMonitorException and UnknownSubscriptionException, which the catalog cannot select. Tags are stored for all three kinds; resource counts are local limits, not Service Quotas.",
	},
}

// ceCostCategory applies the documented effective start rule (a billing
// month start), defaults it to the current month and checks the rules list.
func ceCostCategory(input map[string]any) error {
	if start, ok := input["EffectiveStart"].(string); ok {
		if !ceStart.MatchString(start) {
			return invalid("EffectiveStart must be the first day of a month at 00:00:00 UTC.")
		}
	} else {
		input["EffectiveStart"] = time.Now().UTC().Format("2006-01") + "-01T00:00:00Z"
	}
	if name, ok := input["Name"].(string); ok && (len(name) == 0 || len(name) > 50) {
		return invalid("Name must be 1 to 50 characters.")
	}
	rules, _ := input["Rules"].([]any)
	if len(rules) == 0 {
		return invalid("A cost category needs at least one rule.")
	}
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		if rule["Type"] == "INHERITED_VALUE" {
			if rule["InheritedValue"] == nil || rule["Value"] != nil {
				return invalid("An INHERITED_VALUE rule needs InheritedValue and no Value.")
			}
		} else if rule["Value"] == nil || rule["InheritedValue"] != nil {
			return invalid("A REGULAR rule needs Value and no InheritedValue.")
		}
	}
	return nil
}

// ceCostCategoryReplace makes an update replace the optional members the
// request leaves out, as the official update replaces the definition.
func ceCostCategoryReplace(document, supplied map[string]any) error {
	for _, key := range []string{"DefaultValue", "SplitChargeRules"} {
		if _, ok := supplied[key]; !ok {
			delete(document, key)
		}
	}
	return nil
}

// ceAnomalyMonitor pairs the monitor type with the members it documents.
func ceAnomalyMonitor(input map[string]any) error {
	monitor, _ := input["AnomalyMonitor"].(map[string]any)
	switch monitor["MonitorType"] {
	case "DIMENSIONAL":
		if monitor["MonitorDimension"] == nil {
			return invalid("A DIMENSIONAL monitor needs MonitorDimension.")
		}
	case "CUSTOM":
		if monitor["MonitorDimension"] != nil || monitor["MonitorSpecification"] == nil {
			return invalid("A CUSTOM monitor needs MonitorSpecification and no MonitorDimension.")
		}
	}
	return nil
}

// ceAnomalySubscription checks the monitor ARNs, the subscribers and that
// one threshold form at most is used.
func ceAnomalySubscription(input map[string]any) error {
	if nested, ok := input["AnomalySubscription"].(map[string]any); ok {
		return ceSubscriptionMembers(nested)
	}
	return ceSubscriptionMembers(input)
}

func ceSubscriptionMembers(members map[string]any) error {
	if monitors, ok := members["MonitorArnList"].([]any); ok {
		if len(monitors) == 0 {
			return invalid("A subscription needs at least one monitor.")
		}
		for _, monitor := range monitors {
			if arn, _ := monitor.(string); !ceMonitorARN.MatchString(arn) {
				return invalid("MonitorArnList must hold anomaly monitor ARNs.")
			}
		}
	}
	if subscribers, ok := members["Subscribers"].([]any); ok {
		if len(subscribers) == 0 {
			return invalid("A subscription needs at least one subscriber.")
		}
		for _, item := range subscribers {
			subscriber, _ := item.(map[string]any)
			address, _ := subscriber["Address"].(string)
			switch subscriber["Type"] {
			case "EMAIL":
				if !strings.Contains(address, "@") {
					return invalid("An EMAIL subscriber needs an email address.")
				}
			case "SNS":
				if !strings.HasPrefix(address, "arn:") {
					return invalid("An SNS subscriber needs a topic ARN.")
				}
			}
		}
	}
	if members["Threshold"] != nil && members["ThresholdExpression"] != nil {
		return invalid("Specify only one of Threshold and ThresholdExpression.")
	}
	return nil
}

var bcmDataExports = &Service{
	Model: "bcm-data-exports", Title: "AWS Billing and Cost Management Data Exports", IAMPrefix: "bcm-data-exports", Issues: []int{101},
	Resources: []*Resource{
		{Kind: "export", Shape: "Export", Summary: "ExportReference", ID: "uuid",
			ARN: "arn:{partition}:bcm-data-exports:{region}:{account}:export/{name}-{id}", ARNMember: "ExportArn",
			NameMember: "Name", Limit: 50,
			Generate: map[string]any{"ExportName": "$name"}},
	},
	Ops: []Op{
		{Name: "CreateExport", Kind: Create, Resource: "export", From: "Export", Check: bcmExport},
		{Name: "GetExport", Kind: Read, Resource: "export", Lookup: "ExportArn"},
		{Name: "ListExports", Kind: List, Resource: "export"},
		{Name: "UpdateExport", Kind: Update, Resource: "export", Lookup: "ExportArn", From: "Export",
			Fields: []string{"Name", "Description", "DataQuery", "DestinationConfigurations", "RefreshCadence"}, Check: bcmExportUpdate, Merged: bcmExportReplace},
		{Name: "DeleteExport", Kind: Delete, Resource: "export", Lookup: "ExportArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Exports are stored definitions: the query is kept as text and never run, no table is read, nothing is written to Amazon S3 and no file, refresh or delivery happens. The export reports no ExportStatus and no LastRefreshedAt because nothing ever runs. GetTable and ListTables return UnknownOperationException with HTTP 501 because the table schemas are AWS-owned data.",
		"QueryStatement must be a SELECT statement but is not parsed, so unknown tables and columns are accepted; the S3 bucket, prefix and Region are stored without being checked or contacted. Export names are not unique. UpdateExport replaces the definition, so it needs the full Export and drops a Description the request omits, and an ExportArn inside Export must equal the ExportArn being updated.",
		"Exports per Region are a local limit, not a Service Quota. The data exports service is available in us-east-1 only in AWS; the simulator serves it in every Region it serves.",
	},
}

// bcmExport requires a SELECT query, the documented form of an export query.
func bcmExport(input map[string]any) error {
	export, _ := input["Export"].(map[string]any)
	query, _ := export["DataQuery"].(map[string]any)
	statement, _ := query["QueryStatement"].(string)
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(statement)), "select") {
		return invalid("QueryStatement must be a SELECT statement.")
	}
	return nil
}

// bcmExportUpdate applies the same query rule and refuses an Export that
// names another export's ARN.
func bcmExportUpdate(input map[string]any) error {
	if err := bcmExport(input); err != nil {
		return err
	}
	export, _ := input["Export"].(map[string]any)
	if arn, ok := export["ExportArn"]; ok {
		if arn != input["ExportArn"] {
			return invalid("The ExportArn of Export must equal ExportArn.")
		}
		delete(export, "ExportArn")
	}
	return nil
}

// bcmExportReplace keeps the list name current and makes an update replace
// the optional description.
func bcmExportReplace(document, supplied map[string]any) error {
	document["ExportName"] = document["Name"]
	if _, ok := supplied["Description"]; !ok {
		delete(document, "Description")
	}
	return nil
}
