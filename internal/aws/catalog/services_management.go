package catalog

import (
	"encoding/json"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Reviewed bounded specifications for management and governance services.
// Each binds only official operations whose effect is configuration metadata
// owned by the caller; deployments, log or event delivery, rule evaluation,
// snapshot creation and scaling are never performed.

var appConfig = &Service{
	Model: "appconfig", Title: "AWS AppConfig", IAMPrefix: "appconfig", Issues: []int{187},
	Resources: []*Resource{
		{Kind: "application", Shape: "Application", ID: "alnum:7", ARN: "arn:{partition}:appconfig:{region}:{account}:application/{id}",
			NameMember: "Name", IDMember: "Id", Limit: 100},
		{Kind: "environment", Shape: "Environment", Parent: "application", ParentMember: "ApplicationId", ID: "alnum:7",
			ARN:        "arn:{partition}:appconfig:{region}:{account}:application/{parent}/environment/{id}",
			NameMember: "Name", IDMember: "Id", Limit: 20,
			Generate: map[string]any{"ApplicationId": "$parentId", "State": "READY_FOR_DEPLOYMENT"}},
		{Kind: "configurationProfile", Shape: "ConfigurationProfile", Summary: "ConfigurationProfileSummary", Parent: "application", ParentMember: "ApplicationId", ID: "alnum:7",
			ARN:        "arn:{partition}:appconfig:{region}:{account}:application/{parent}/configurationprofile/{id}",
			NameMember: "Name", IDMember: "Id", Limit: 100,
			Generate: map[string]any{"ApplicationId": "$parentId"}},
		{Kind: "deploymentStrategy", Shape: "DeploymentStrategy", ID: "alnum:7", ARN: "arn:{partition}:appconfig:{region}:{account}:deploymentstrategy/{id}",
			NameMember: "Name", IDMember: "Id", Limit: 20},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application"},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "ApplicationId"},
		{Name: "ListApplications", Kind: List, Resource: "application"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "ApplicationId", Fields: []string{"Name", "Description"}},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "ApplicationId"},
		{Name: "CreateEnvironment", Kind: Create, Resource: "environment", Parent: "ApplicationId"},
		{Name: "GetEnvironment", Kind: Read, Resource: "environment", Parent: "ApplicationId", Lookup: "EnvironmentId"},
		{Name: "ListEnvironments", Kind: List, Resource: "environment", Parent: "ApplicationId"},
		{Name: "UpdateEnvironment", Kind: Update, Resource: "environment", Parent: "ApplicationId", Lookup: "EnvironmentId", Fields: []string{"Name", "Description", "Monitors"}},
		{Name: "DeleteEnvironment", Kind: Delete, Resource: "environment", Parent: "ApplicationId", Lookup: "EnvironmentId", Accept: []string{"DeletionProtectionCheck"}},
		{Name: "CreateConfigurationProfile", Kind: Create, Resource: "configurationProfile", Parent: "ApplicationId", Check: appConfigProfile},
		{Name: "GetConfigurationProfile", Kind: Read, Resource: "configurationProfile", Parent: "ApplicationId", Lookup: "ConfigurationProfileId"},
		{Name: "ListConfigurationProfiles", Kind: List, Resource: "configurationProfile", Parent: "ApplicationId", Filters: map[string]string{"Type": "Type"}},
		{Name: "UpdateConfigurationProfile", Kind: Update, Resource: "configurationProfile", Parent: "ApplicationId", Lookup: "ConfigurationProfileId",
			Fields: []string{"Name", "Description", "RetrievalRoleArn", "Validators", "KmsKeyIdentifier"}},
		{Name: "DeleteConfigurationProfile", Kind: Delete, Resource: "configurationProfile", Parent: "ApplicationId", Lookup: "ConfigurationProfileId", Accept: []string{"DeletionProtectionCheck"}},
		{Name: "CreateDeploymentStrategy", Kind: Create, Resource: "deploymentStrategy", Check: appConfigStrategy},
		{Name: "GetDeploymentStrategy", Kind: Read, Resource: "deploymentStrategy", Lookup: "DeploymentStrategyId"},
		{Name: "ListDeploymentStrategies", Kind: List, Resource: "deploymentStrategy"},
		{Name: "UpdateDeploymentStrategy", Kind: Update, Resource: "deploymentStrategy", Lookup: "DeploymentStrategyId",
			Fields: []string{"Description", "DeploymentDurationInMinutes", "FinalBakeTimeInMinutes", "GrowthFactor", "GrowthType"}},
		{Name: "DeleteDeploymentStrategy", Kind: Delete, Resource: "deploymentStrategy", Lookup: "DeploymentStrategyId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Applications, environments, configuration profiles and deployment strategies are stored metadata: no configuration is read from its LocationUri, validated, deployed, polled or rolled back, and CloudWatch alarm monitors are never evaluated.",
		"Environments stay READY_FOR_DEPLOYMENT; StartDeployment, hosted configuration versions, extensions and account settings return UnknownOperationException with HTTP 501.",
		"Predefined AppConfig.* deployment strategies are not listed, ReplicateTo SSM_DOCUMENT is refused, and deleting an application that still has environments or configuration profiles is refused.",
		"DeletionProtectionCheck is accepted because no configuration retrieval is ever recorded, so deletion protection never blocks a deletion.",
	},
}

// appConfigLocations are the documented LocationUri forms other than hosted.
var appConfigLocations = regexp.MustCompile(`^(ssm-parameter|ssm-document|codepipeline|secretsmanager|s3)://.+$|^arn:aws[a-z-]*:ssm:[a-z0-9-]+:[0-9]{12}:(parameter|document)/.+$`)

// appConfigProfile applies the documented profile rules: the two profile
// types, feature flags only in the hosted store, and a retrieval role for
// every source except the hosted store and CodePipeline.
func appConfigProfile(input map[string]any) error {
	location, _ := input["LocationUri"].(string)
	if location != "hosted" && !appConfigLocations.MatchString(location) {
		return Failure("BadRequestException", "LocationUri must be hosted or a documented ssm-parameter, ssm-document, codepipeline, secretsmanager or s3 location.")
	}
	if kind, ok := input["Type"].(string); ok {
		if kind != "AWS.Freeform" && kind != "AWS.AppConfig.FeatureFlags" {
			return Failure("BadRequestException", "Type must be AWS.Freeform or AWS.AppConfig.FeatureFlags.")
		}
		if kind == "AWS.AppConfig.FeatureFlags" && location != "hosted" {
			return Failure("BadRequestException", "Feature flag profiles must use the hosted configuration store.")
		}
	}
	if _, role := input["RetrievalRoleArn"]; !role && location != "hosted" && !strings.HasPrefix(location, "codepipeline://") {
		return Failure("BadRequestException", "RetrievalRoleArn is required for configurations outside the hosted store and CodePipeline.")
	}
	return nil
}

// appConfigStrategy refuses replication into an SSM document, which would
// create a resource in another service.
func appConfigStrategy(input map[string]any) error {
	if input["ReplicateTo"] == "SSM_DOCUMENT" {
		return Failure("BadRequestException", "Saving a deployment strategy to an SSM document is not supported by the simulator.")
	}
	return nil
}

var cloudTrail = &Service{
	Model: "cloudtrail", Title: "AWS CloudTrail", IAMPrefix: "cloudtrail", Issues: []int{188},
	Resources: []*Resource{
		{Kind: "trail", Shape: "Trail", Summary: "TrailInfo", ID: "name", ARN: "arn:{partition}:cloudtrail:{region}:{account}:trail/{name}",
			ARNMember: "TrailARN", NameMember: "Name", Unique: true, Limit: 5,
			Aliases: map[string]string{"EnableLogFileValidation": "LogFileValidationEnabled"},
			Generate: map[string]any{"HomeRegion": "$region", "IsMultiRegionTrail": false, "LogFileValidationEnabled": false,
				"IsOrganizationTrail": false, "HasCustomEventSelectors": false, "HasInsightSelectors": false}},
	},
	Ops: []Op{
		{Name: "CreateTrail", Kind: Create, Resource: "trail", Check: trailCreate},
		{Name: "GetTrail", Kind: Read, Resource: "trail", Lookup: "Name"},
		{Name: "DescribeTrails", Kind: List, Resource: "trail", Accept: []string{"includeShadowTrails"}},
		{Name: "ListTrails", Kind: List, Resource: "trail"},
		{Name: "UpdateTrail", Kind: Update, Resource: "trail", Lookup: "Name", Check: noOrganizationTrail,
			Fields: []string{"S3BucketName", "S3KeyPrefix", "SnsTopicName", "IncludeGlobalServiceEvents", "IsMultiRegionTrail", "EnableLogFileValidation",
				"CloudWatchLogsLogGroupArn", "CloudWatchLogsRoleArn", "KmsKeyId", "IsOrganizationTrail"}},
		{Name: "DeleteTrail", Kind: Delete, Resource: "trail", Lookup: "Name"},
	},
	Bounds: []string{
		"Trails are stored configuration: logging never starts, no event is recorded or delivered to Amazon S3, Amazon SNS or CloudWatch Logs, and the named bucket, topic, log group, role and KMS key are not checked.",
		"Trails are kept in their home Region only: multi-Region trails create no shadow trails (so includeShadowTrails has no effect) and ListTrails lists the requested Region; organization trails are refused because the simulator account is not in an organization.",
		"DescribeTrails returns every trail of the Region and refuses trailNameList; StartLogging, event selectors, insights, CloudTrail Lake, channels, dashboards and LookupEvents return UnknownOperationException with HTTP 501.",
		"Tags supplied at creation are stored, but AddTags, RemoveTags and ListTags are not implemented.",
	},
}

var (
	trailNameCharacters  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	trailAdjacentSymbols = regexp.MustCompile(`[._-]{2}`)
)

// trailCreate applies the documented trail-name rules and refuses
// organization trails.
func trailCreate(input map[string]any) error {
	name, _ := input["Name"].(string)
	if len(name) < 3 || len(name) > 128 || !trailNameCharacters.MatchString(name) || trailAdjacentSymbols.MatchString(name) || net.ParseIP(name) != nil {
		return Failure("InvalidTrailNameException", "Trail names have 3-128 ASCII letters, digits, periods, underscores or dashes, start and end with a letter or digit, have no adjacent symbols and are not IP addresses.")
	}
	return noOrganizationTrail(input)
}

// noOrganizationTrail refuses organization trails: the simulator account is
// never a member of an AWS Organizations organization.
func noOrganizationTrail(input map[string]any) error {
	if enabled, _ := input["IsOrganizationTrail"].(bool); enabled {
		return Failure("OrganizationsNotInUseException", "The simulator account is not a member of an organization, so organization trails are not available.")
	}
	return nil
}

var awsConfig = &Service{
	Model: "config", Title: "AWS Config", IAMPrefix: "config", Issues: []int{194},
	Resources: []*Resource{
		{Kind: "storedQuery", Shape: "StoredQuery", Summary: "StoredQueryMetadata", ID: "alnum:16:query-",
			ARN:       "arn:{partition}:config:{region}:{account}:stored-query/{name}/{id}",
			ARNMember: "QueryArn", NameMember: "QueryName", IDMember: "QueryId", Unique: true, Limit: 300},
		{Kind: "retentionConfiguration", Shape: "RetentionConfiguration", Singleton: true, ID: "name",
			ARN: "arn:{partition}:config:{region}:{account}:retention-configuration/default", NameMember: "Name",
			Generate: map[string]any{"Name": "default"}},
	},
	Ops: []Op{
		{Name: "PutStoredQuery", Kind: Create, Resource: "storedQuery", From: "StoredQuery", Check: storedQueryExpression},
		{Name: "GetStoredQuery", Kind: Read, Resource: "storedQuery", Lookup: "QueryName"},
		{Name: "ListStoredQueries", Kind: List, Resource: "storedQuery"},
		{Name: "DeleteStoredQuery", Kind: Delete, Resource: "storedQuery", Lookup: "QueryName"},
		{Name: "PutRetentionConfiguration", Kind: Put, Resource: "retentionConfiguration"},
		{Name: "DescribeRetentionConfigurations", Kind: List, Resource: "retentionConfiguration", Names: "RetentionConfigurationNames"},
		{Name: "DeleteRetentionConfiguration", Kind: Delete, Resource: "retentionConfiguration", Lookup: "RetentionConfigurationName", Check: retentionName},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Stored queries and the default retention configuration are stored metadata: no configuration item is recorded, retained, aggregated or queried, and query expressions are neither parsed nor run.",
		"PutStoredQuery only creates a query; a second PutStoredQuery with an existing QueryName is refused with ValidationException instead of updating it, so delete the query before replacing it.",
		"Configuration recorders, delivery channels, config rules, conformance packs, remediation, aggregators and evaluations return UnknownOperationException with HTTP 501.",
	},
}

// storedQueryExpression requires the expression that every new stored
// query must have; PutStoredQuery here never updates an existing query.
func storedQueryExpression(input map[string]any) error {
	query, _ := input["StoredQuery"].(map[string]any)
	if _, ok := query["Expression"]; !ok {
		return Failure("ValidationException", "A new stored query needs QueryName and Expression.")
	}
	return nil
}

// retentionName reports any name other than default as missing: AWS Config
// names its only retention configuration default.
func retentionName(input map[string]any) error {
	if input["RetentionConfigurationName"] != "default" {
		return Failure("NoSuchRetentionConfigurationException", "The only retention configuration is named default.")
	}
	return nil
}

var dataLifecycleManager = &Service{
	Model: "dlm", Title: "Amazon Data Lifecycle Manager", IAMPrefix: "dlm", Issues: []int{196},
	Resources: []*Resource{
		{Kind: "lifecyclePolicy", Shape: "LifecyclePolicy", Summary: "LifecyclePolicySummary", ID: "hex:17:policy-",
			ARN: "arn:{partition}:dlm:{region}:{account}:policy/{id}", ARNMember: "PolicyArn", IDMember: "PolicyId", Limit: 100,
			Generate: map[string]any{"DateCreated": "$now", "DateModified": "$now", "DefaultPolicy": false}},
	},
	Ops: []Op{
		{Name: "CreateLifecyclePolicy", Kind: Create, Resource: "lifecyclePolicy", Check: customLifecyclePolicy},
		{Name: "GetLifecyclePolicy", Kind: Read, Resource: "lifecyclePolicy", Lookup: "PolicyId"},
		{Name: "GetLifecyclePolicies", Kind: List, Resource: "lifecyclePolicy", Names: "PolicyIds", Filters: map[string]string{"State": "State"}},
		{Name: "UpdateLifecyclePolicy", Kind: Update, Resource: "lifecyclePolicy", Lookup: "PolicyId", Touch: []string{"DateModified"},
			Fields: []string{"ExecutionRoleArn", "State", "Description", "PolicyDetails"}},
		{Name: "DeleteLifecyclePolicy", Kind: Delete, Resource: "lifecyclePolicy", Lookup: "PolicyId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Custom lifecycle policies are stored metadata: their schedules never run, so no EBS snapshot, EBS-backed AMI or cross-Region or cross-account copy is created, retained or deleted, and the execution role is not assumed.",
		"Default policies (DefaultPolicy and its CreateInterval, RetainInterval, CopyTags, ExtendDeletion, CrossRegionCopyTargets and Exclusions request members) are refused; PolicyDetails is required.",
		"GetLifecyclePolicies filters by PolicyIds and State only; the policy type is not repeated in list summaries.",
	},
}

// customLifecyclePolicy requires the PolicyDetails of a custom policy and
// refuses the default-policy form, which the simulator does not model.
func customLifecyclePolicy(input map[string]any) error {
	if _, ok := input["DefaultPolicy"]; ok {
		return Failure("InvalidRequestException", "Default policies are not supported by the simulator.")
	}
	if _, ok := input["PolicyDetails"]; !ok {
		return Failure("InvalidRequestException", "A custom lifecycle policy requires PolicyDetails.")
	}
	return nil
}

var managedPrometheus = &Service{
	Model: "amp", Title: "Amazon Managed Service for Prometheus", IAMPrefix: "aps", Issues: []int{202},
	Resources: []*Resource{
		{Kind: "workspace", Shape: "WorkspaceDescription", Summary: "WorkspaceSummary", ID: "hex:32:ws-",
			ARN: "arn:{partition}:aps:{region}:{account}:workspace/{id}", ARNMember: "arn", IDMember: "workspaceId", Limit: 25, Cascade: true,
			Generate: map[string]any{"status": map[string]any{"statusCode": "ACTIVE"}, "createdAt": "$now"}},
		{Kind: "ruleGroupsNamespace", Shape: "RuleGroupsNamespaceDescription", Summary: "RuleGroupsNamespaceSummary", Parent: "workspace", ParentMember: "workspaceId", ID: "name",
			ARN:       "arn:{partition}:aps:{region}:{account}:rulegroupsnamespace/{parent}/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 50,
			Generate: map[string]any{"status": map[string]any{"statusCode": "ACTIVE"}, "createdAt": "$now", "modifiedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateWorkspace", Kind: Create, Resource: "workspace"},
		{Name: "DescribeWorkspace", Kind: Read, Resource: "workspace", Lookup: "workspaceId"},
		{Name: "ListWorkspaces", Kind: List, Resource: "workspace"},
		{Name: "UpdateWorkspaceAlias", Kind: Update, Resource: "workspace", Lookup: "workspaceId", Fields: []string{"alias"}, Accept: []string{"clientToken"}},
		{Name: "DeleteWorkspace", Kind: Delete, Resource: "workspace", Lookup: "workspaceId", Accept: []string{"clientToken"}},
		{Name: "CreateRuleGroupsNamespace", Kind: Create, Resource: "ruleGroupsNamespace", Parent: "workspaceId", Check: prometheusRules},
		{Name: "DescribeRuleGroupsNamespace", Kind: Read, Resource: "ruleGroupsNamespace", Parent: "workspaceId", Lookup: "name"},
		{Name: "ListRuleGroupsNamespaces", Kind: List, Resource: "ruleGroupsNamespace", Parent: "workspaceId"},
		{Name: "PutRuleGroupsNamespace", Kind: Update, Resource: "ruleGroupsNamespace", Parent: "workspaceId", Lookup: "name",
			Fields: []string{"data"}, Touch: []string{"modifiedAt"}, Accept: []string{"clientToken"}, Check: prometheusRules},
		{Name: "DeleteRuleGroupsNamespace", Kind: Delete, Resource: "ruleGroupsNamespace", Parent: "workspaceId", Lookup: "name", Accept: []string{"clientToken"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Workspaces and rule groups namespaces are ACTIVE metadata without a prometheusEndpoint: no remote write, ingestion, PromQL query, recording or alerting rule evaluation is performed, and kmsKeyArn is stored without encryption or key checks.",
		"Rules files are stored as supplied after a check for a top-level groups key; deleting a workspace deletes its rule groups namespaces. Aliases are stored exactly as supplied, and the alias and name prefix filters of the list operations are refused.",
		"Alert manager definitions, scrapers, anomaly detectors, logging, resource policies and workspace configuration return UnknownOperationException with HTTP 501.",
	},
}

// prometheusRules requires a rules file with the top-level groups key that
// every Prometheus rules file has, as YAML or as JSON.
func prometheusRules(input map[string]any) error {
	data, ok := input["data"].([]byte)
	if !ok {
		return nil
	}
	text := string(data)
	var object map[string]json.RawMessage
	if utf8.ValidString(text) {
		if json.Unmarshal(data, &object) == nil && object["groups"] != nil {
			return nil
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "groups:") {
				return nil
			}
		}
	}
	return Failure("ValidationException", "The rules file must be a UTF-8 YAML document with a top-level groups list.")
}

var resourceGroups = &Service{
	Model: "resource-groups", Title: "AWS Resource Groups", IAMPrefix: "resource-groups", Issues: []int{207},
	Resources: []*Resource{
		{Kind: "group", Shape: "Group", Summary: "GroupIdentifier", ID: "name", ARN: "arn:{partition}:resource-groups:{region}:{account}:group/{name}",
			ARNMember: "GroupArn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"GroupName": "$name"}},
	},
	Ops: []Op{
		{Name: "CreateGroup", Kind: Create, Resource: "group", Check: groupName},
		{Name: "GetGroup", Kind: Read, Resource: "group", Lookup: "Group", Check: groupIdentifier},
		{Name: "ListGroups", Kind: List, Resource: "group"},
		{Name: "UpdateGroup", Kind: Update, Resource: "group", Lookup: "Group", Check: groupIdentifier, Fields: []string{"Description", "Criticality", "Owner", "DisplayName"}},
		{Name: "DeleteGroup", Kind: Delete, Resource: "group", Lookup: "Group", Check: groupIdentifier},
		{Name: "Tag", Kind: Tag, Lookup: "Arn"},
		{Name: "Untag", Kind: Untag, Lookup: "Arn"},
		{Name: "GetTags", Kind: ListTags, Lookup: "Arn"},
	},
	Bounds: []string{
		"Groups are stored without membership: resource queries and service configurations are refused, and resource searches, group membership, tag-sync tasks and group lifecycle events return UnknownOperationException with HTTP 501.",
		"Groups are identified by the Group member; the deprecated GroupName member and ListGroups filters are refused. Tag, Untag and GetTags responses omit the echoed Arn and Keys members.",
	},
}

// groupName applies the documented reservation of names starting with AWS.
func groupName(input map[string]any) error {
	if name, _ := input["Name"].(string); strings.HasPrefix(strings.ToLower(name), "aws") {
		return Failure("BadRequestException", "Resource group names cannot start with AWS; that prefix is reserved.")
	}
	return nil
}

// groupIdentifier requires the current Group member that names the group.
func groupIdentifier(input map[string]any) error {
	if _, ok := input["Group"]; !ok {
		return Failure("BadRequestException", "Specify the group name or ARN with Group.")
	}
	return nil
}

var serviceCatalog = &Service{
	Model: "servicecatalog", Title: "AWS Service Catalog", IAMPrefix: "servicecatalog", Issues: []int{208},
	Resources: []*Resource{
		{Kind: "portfolio", Shape: "PortfolioDetail", ID: "alnum:13:port-", ARN: "arn:{partition}:catalog:{region}:{account}:portfolio/{id}",
			ARNMember: "ARN", NameMember: "DisplayName", IDMember: "Id", Limit: 100,
			Generate: map[string]any{"CreatedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreatePortfolio", Kind: Create, Resource: "portfolio", Accept: []string{"AcceptLanguage"}, Check: englishOnly},
		{Name: "DescribePortfolio", Kind: Read, Resource: "portfolio", Lookup: "Id", Accept: []string{"AcceptLanguage"}, Check: englishOnly},
		{Name: "ListPortfolios", Kind: List, Resource: "portfolio", Accept: []string{"AcceptLanguage"}, Check: englishOnly},
		{Name: "UpdatePortfolio", Kind: Update, Resource: "portfolio", Lookup: "Id", Accept: []string{"AcceptLanguage"}, Check: englishOnly,
			Fields: []string{"DisplayName", "Description", "ProviderName"}},
		{Name: "DeletePortfolio", Kind: Delete, Resource: "portfolio", Lookup: "Id", Accept: []string{"AcceptLanguage"}, Check: englishOnly},
	},
	Bounds: []string{
		"Portfolios are stored metadata: products, provisioning artifacts, launches, provisioned products, constraints, principal and product associations, sharing, TagOptions and budgets are not implemented and return UnknownOperationException with HTTP 501.",
		"Tags are set only at creation; UpdatePortfolio refuses AddTags and RemoveTags, and AcceptLanguage is accepted only as en.",
	},
}

// englishOnly accepts AcceptLanguage only for English, the language of every
// simulator message.
func englishOnly(input map[string]any) error {
	if language, ok := input["AcceptLanguage"]; ok && language != "en" {
		return Failure("InvalidParametersException", "The simulator returns messages in English only (AcceptLanguage en).")
	}
	return nil
}
