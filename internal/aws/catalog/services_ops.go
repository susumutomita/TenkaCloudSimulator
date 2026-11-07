package catalog

import (
	"fmt"
	"regexp"
)

// Reviewed bounded specifications for operations, governance, billing
// configuration and resilience tooling. Each binds only official operations
// whose effect is configuration metadata owned by the caller: no notification
// is sent, no chat or incident system is contacted, no experiment, assessment,
// deployment or report runs, and no billing, cost or usage data is read,
// computed or invented.

var accountManagement = &Service{
	Model: "account", Title: "AWS Account Management", IAMPrefix: "account", Issues: []int{186}, Global: true,
	Resources: []*Resource{
		// One alternate contact per type; the type is the identity.
		{Kind: "alternateContact", Shape: "AlternateContact", ID: "name", ARN: "arn:{partition}:account::{account}:account/alternate-contact/{name}",
			NameMember: "AlternateContactType", Unique: true, Limit: 3},
	},
	Ops: []Op{
		{Name: "PutAlternateContact", Kind: Put, Resource: "alternateContact", Lookup: "AlternateContactType", Check: standaloneAccount},
		{Name: "GetAlternateContact", Kind: Read, Resource: "alternateContact", Lookup: "AlternateContactType", Check: standaloneAccount},
		{Name: "DeleteAlternateContact", Kind: Delete, Resource: "alternateContact", Lookup: "AlternateContactType", Check: standaloneAccount},
	},
	Bounds: []string{
		"The BILLING, OPERATIONS and SECURITY alternate contacts of the standalone simulator account are stored metadata: no email, call or notification is ever sent to them and the address and phone number are not verified.",
		"Each contact type is authorized as arn:aws:account::000000000000:account/alternate-contact/<TYPE> because the engine stores one resource per contact, so IAM policies that name the official account ARN need a trailing wildcard to match.",
		"AccountId is refused because the simulator account is not in an organization; contact information, account names, primary email, Region opt-in and Region listing return UnknownOperationException with HTTP 501.",
	},
}

// standaloneAccount refuses AccountId: AWS accepts it only from an
// organization's management or delegated administrator account.
func standaloneAccount(input map[string]any) error {
	if _, ok := input["AccountId"]; ok {
		return Failure("ValidationException", "The simulator account is standalone; omit AccountId to address the calling account.")
	}
	return nil
}

var chatbot = &Service{
	Model: "chatbot", Title: "AWS Chatbot", IAMPrefix: "chatbot", Issues: []int{189}, Global: true,
	Resources: []*Resource{
		{Kind: "chimeWebhookConfiguration", Shape: "ChimeWebhookConfiguration", ID: "name",
			ARN:       "arn:{partition}:chatbot::{account}:chat-configuration/chime-webhook/{name}",
			ARNMember: "ChatConfigurationArn", NameMember: "ConfigurationName", Unique: true, Limit: 50,
			Generate: map[string]any{"State": "ENABLED"}},
		{Kind: "customAction", Shape: "CustomAction", ID: "name", ARN: "arn:{partition}:chatbot::{account}:custom-action/{name}",
			ARNMember: "CustomActionArn", NameMember: "ActionName", Unique: true, Limit: 50},
		{Kind: "accountPreferences", Shape: "AccountPreferences", Singleton: true, ID: "name",
			ARN: "arn:{partition}:chatbot::{account}:account-preferences", NameMember: "UserAuthorizationRequired"},
	},
	Ops: []Op{
		{Name: "CreateChimeWebhookConfiguration", Kind: Create, Resource: "chimeWebhookConfiguration", Accept: []string{"WebhookUrl"}},
		{Name: "DescribeChimeWebhookConfigurations", Kind: List, Resource: "chimeWebhookConfiguration", Filters: map[string]string{"ChatConfigurationArn": "ChatConfigurationArn"}},
		{Name: "UpdateChimeWebhookConfiguration", Kind: Update, Resource: "chimeWebhookConfiguration", Lookup: "ChatConfigurationArn", Accept: []string{"WebhookUrl"},
			Fields: []string{"WebhookDescription", "SnsTopicArns", "IamRoleArn", "LoggingLevel"}},
		{Name: "DeleteChimeWebhookConfiguration", Kind: Delete, Resource: "chimeWebhookConfiguration", Lookup: "ChatConfigurationArn"},
		{Name: "CreateCustomAction", Kind: Create, Resource: "customAction"},
		{Name: "GetCustomAction", Kind: Read, Resource: "customAction", Lookup: "CustomActionArn"},
		{Name: "ListCustomActions", Kind: List, Resource: "customAction", ListValue: "CustomActionArn"},
		{Name: "UpdateCustomAction", Kind: Update, Resource: "customAction", Lookup: "CustomActionArn", Fields: []string{"Definition", "AliasName", "Attachments"}},
		{Name: "DeleteCustomAction", Kind: Delete, Resource: "customAction", Lookup: "CustomActionArn"},
		{Name: "GetAccountPreferences", Kind: Read, Resource: "accountPreferences"},
		{Name: "UpdateAccountPreferences", Kind: Update, Resource: "accountPreferences", Fields: []string{"UserAuthorizationRequired", "TrainingDataCollectionEnabled"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Amazon Chime webhook configurations, custom actions and account preferences are stored metadata: no SNS topic is subscribed, no message is posted to any webhook or chat channel, no command or custom action runs and no role is assumed.",
		"WebhookUrl is accepted but neither stored nor contacted, as AWS never returns it; SNS topic and role ARNs are not checked. Slack and Microsoft Teams configurations, workspace and user identities and associations return UnknownOperationException with HTTP 501 because they need OAuth authorization with an external chat system.",
		"GetAccountPreferences omits preferences that were never updated instead of reporting service defaults.",
	},
}

var licenseManager = &Service{
	Model: "license-manager", Title: "AWS License Manager", IAMPrefix: "license-manager", Issues: []int{200},
	Resources: []*Resource{
		{Kind: "licenseConfiguration", Shape: "GetLicenseConfigurationResponse", Summary: "LicenseConfiguration", ID: "hex:32:lic-",
			ARN:       "arn:{partition}:license-manager:{region}:{account}:license-configuration:{id}",
			ARNMember: "LicenseConfigurationArn", IDMember: "LicenseConfigurationId", NameMember: "Name", Limit: 100,
			Aliases: map[string]string{"LicenseConfigurationStatus": "Status"},
			Generate: map[string]any{"Status": "AVAILABLE", "OwnerAccountId": "$account", "ConsumedLicenses": 0,
				"ConsumedLicenseSummaryList": []any{}, "ManagedResourceSummaryList": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateLicenseConfiguration", Kind: Create, Resource: "licenseConfiguration"},
		{Name: "GetLicenseConfiguration", Kind: Read, Resource: "licenseConfiguration", Lookup: "LicenseConfigurationArn"},
		{Name: "ListLicenseConfigurations", Kind: List, Resource: "licenseConfiguration", Names: "LicenseConfigurationArns"},
		{Name: "UpdateLicenseConfiguration", Kind: Update, Resource: "licenseConfiguration", Lookup: "LicenseConfigurationArn",
			Fields: []string{"LicenseConfigurationStatus", "LicenseRules", "LicenseCount", "LicenseCountHardLimit", "Name", "Description",
				"ProductInformationList", "DisassociateWhenNotFound", "LicenseExpiry"}},
		{Name: "DeleteLicenseConfiguration", Kind: Delete, Resource: "licenseConfiguration", Lookup: "LicenseConfigurationArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"License configurations are stored metadata: no instance, host, AMI or database is discovered, associated or counted, license rules and product information filters are not evaluated, so ConsumedLicenses stays 0 and the consumed and managed resource summaries stay empty.",
		"ListLicenseConfigurations selects by LicenseConfigurationArns only and refuses Filters; a missing ARN is refused instead of omitted.",
		"Seller-issued licenses, grants, tokens, checkouts, report generators, conversion tasks, asset groups, inventory and service settings return UnknownOperationException with HTTP 501.",
	},
}

var proton = &Service{
	Model: "proton", Title: "AWS Proton", IAMPrefix: "proton", Issues: []int{204},
	Resources: []*Resource{
		{Kind: "environmentTemplate", Shape: "EnvironmentTemplate", Summary: "EnvironmentTemplateSummary", ID: "name",
			ARN: "arn:{partition}:proton:{region}:{account}:environment-template/{name}", ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"createdAt": "$now", "lastModifiedAt": "$now"}},
		{Kind: "serviceTemplate", Shape: "ServiceTemplate", Summary: "ServiceTemplateSummary", ID: "name",
			ARN: "arn:{partition}:proton:{region}:{account}:service-template/{name}", ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"createdAt": "$now", "lastModifiedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateEnvironmentTemplate", Kind: Create, Resource: "environmentTemplate"},
		{Name: "GetEnvironmentTemplate", Kind: Read, Resource: "environmentTemplate", Lookup: "name"},
		{Name: "ListEnvironmentTemplates", Kind: List, Resource: "environmentTemplate"},
		{Name: "UpdateEnvironmentTemplate", Kind: Update, Resource: "environmentTemplate", Lookup: "name", Fields: []string{"description", "displayName"}, Touch: []string{"lastModifiedAt"}},
		{Name: "DeleteEnvironmentTemplate", Kind: Delete, Resource: "environmentTemplate", Lookup: "name"},
		{Name: "CreateServiceTemplate", Kind: Create, Resource: "serviceTemplate"},
		{Name: "GetServiceTemplate", Kind: Read, Resource: "serviceTemplate", Lookup: "name"},
		{Name: "ListServiceTemplates", Kind: List, Resource: "serviceTemplate"},
		{Name: "UpdateServiceTemplate", Kind: Update, Resource: "serviceTemplate", Lookup: "name", Fields: []string{"description", "displayName"}, Touch: []string{"lastModifiedAt"}},
		{Name: "DeleteServiceTemplate", Kind: Delete, Resource: "serviceTemplate", Lookup: "name"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Environment and service templates are stored metadata without versions, so recommendedVersion is never set; no template bundle is read from Amazon S3 or a repository and the encryption key is not used.",
		"Template versions, environments, services, service instances, pipelines, components, repositories, sync configurations, deployments and account settings return UnknownOperationException with HTTP 501, so nothing is provisioned or deployed.",
		"ListTagsForResource returns every tag in one page and refuses maxResults and nextToken.",
	},
}

var resilienceHub = &Service{
	Model: "resiliencehub", Title: "AWS Resilience Hub", IAMPrefix: "resiliencehub", Issues: []int{205},
	Resources: []*Resource{
		{Kind: "resiliencyPolicy", Shape: "ResiliencyPolicy", ID: "uuid", ARN: "arn:{partition}:resiliencehub:{region}:{account}:resiliency-policy/{id}",
			ARNMember: "policyArn", NameMember: "policyName", Limit: 100,
			Generate: map[string]any{"creationTime": "$now"}},
		{Kind: "app", Shape: "App", Summary: "AppSummary", ID: "uuid", ARN: "arn:{partition}:resiliencehub:{region}:{account}:app/{id}",
			ARNMember: "appArn", NameMember: "name", Limit: 100,
			Refs:     map[string]string{"policyArn": "resiliencyPolicy"},
			Generate: map[string]any{"creationTime": "$now", "status": "Active"}},
	},
	Ops: []Op{
		{Name: "CreateResiliencyPolicy", Kind: Create, Resource: "resiliencyPolicy", Check: resiliencyPolicyTargets},
		{Name: "DescribeResiliencyPolicy", Kind: Read, Resource: "resiliencyPolicy", Lookup: "policyArn"},
		{Name: "ListResiliencyPolicies", Kind: List, Resource: "resiliencyPolicy", Filters: map[string]string{"policyName": "policyName"}},
		{Name: "UpdateResiliencyPolicy", Kind: Update, Resource: "resiliencyPolicy", Lookup: "policyArn", Check: resiliencyPolicyTargets,
			Fields: []string{"dataLocationConstraint", "policy", "policyDescription", "policyName", "tier"}},
		{Name: "DeleteResiliencyPolicy", Kind: Delete, Resource: "resiliencyPolicy", Lookup: "policyArn", Accept: []string{"clientToken"}},
		{Name: "CreateApp", Kind: Create, Resource: "app", Check: unscheduledAssessments},
		{Name: "DescribeApp", Kind: Read, Resource: "app", Lookup: "appArn"},
		{Name: "ListApps", Kind: List, Resource: "app", Filters: map[string]string{"name": "name", "appArn": "appArn", "awsApplicationArn": "awsApplicationArn"}},
		{Name: "UpdateApp", Kind: Update, Resource: "app", Lookup: "appArn", Check: unscheduledAssessments,
			Fields: []string{"assessmentSchedule", "description", "eventSubscriptions", "permissionModel", "policyArn"}},
		{Name: "DeleteApp", Kind: Delete, Resource: "app", Lookup: "appArn", Accept: []string{"clientToken", "forceDelete"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Resiliency policies and applications are stored metadata: no resource is imported or resolved, no assessment, drift check or recommendation runs and no event is sent to the subscribed SNS topics, so applications report no compliance, drift, resiliency score or estimated cost tier.",
		"Scheduled Daily assessments are refused and clearResiliencyPolicyArn is not supported; a referenced policyArn must name a stored policy, but deleting a policy does not check the applications that use it.",
		"App versions, components, resources, input sources, assessments, recommendations, suggested policies, grouping recommendations and metrics return UnknownOperationException with HTTP 501. ListApps filters by name and ARNs only, and delete client tokens are accepted without replay.",
	},
}

// resiliencyPolicyTargets requires the software, hardware and Availability
// Zone objectives every resiliency policy defines; Region is optional.
func resiliencyPolicyTargets(input map[string]any) error {
	policy, ok := input["policy"].(map[string]any)
	if !ok {
		return nil
	}
	for _, disruption := range []string{"Software", "Hardware", "AZ"} {
		if _, ok := policy[disruption]; !ok {
			return Failure("ValidationException", "A resiliency policy needs RTO and RPO targets for the Software, Hardware and AZ disruption types.")
		}
	}
	return nil
}

// unscheduledAssessments refuses scheduled assessments, which the simulator
// never runs.
func unscheduledAssessments(input map[string]any) error {
	if input["assessmentSchedule"] == "Daily" {
		return Failure("ValidationException", "Scheduled assessments are not run by the simulator; use the Disabled assessment schedule.")
	}
	return nil
}

var resourceExplorer = &Service{
	Model: "resource-explorer-2", Title: "AWS Resource Explorer", IAMPrefix: "resource-explorer-2", Issues: []int{206},
	Resources: []*Resource{
		{Kind: "index", Shape: "Index", ID: "uuid", ARN: "arn:{partition}:resource-explorer-2:{region}:{account}:index/{id}",
			ARNMember: "Arn", Limit: 1,
			Generate: map[string]any{"Type": "LOCAL", "State": "ACTIVE", "Region": "$region", "CreatedAt": "$now", "LastUpdatedAt": "$now"}},
		// The create request is the only official shape that carries the
		// view name that forms the view ARN.
		{Kind: "view", Shape: "CreateViewInput", Summary: "View", ID: "uuid", ARN: "arn:{partition}:resource-explorer-2:{region}:{account}:view/{name}/{id}",
			ARNMember: "ViewArn", NameMember: "ViewName", Unique: true, Limit: 100,
			Generate: map[string]any{"Owner": "$account", "Scope": "$fmt:arn:{partition}:iam::{account}:root", "LastUpdatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateIndex", Kind: Create, Resource: "index"},
		{Name: "ListIndexes", Kind: List, Resource: "index", Filters: map[string]string{"Type": "Type"}},
		{Name: "UpdateIndexType", Kind: Update, Resource: "index", Lookup: "Arn", Fields: []string{"Type"}, Touch: []string{"LastUpdatedAt"}},
		{Name: "DeleteIndex", Kind: Delete, Resource: "index", Lookup: "Arn", Set: map[string]any{"State": "DELETED"}},
		{Name: "CreateView", Kind: Create, Resource: "view", Check: accountScopeView},
		{Name: "GetView", Kind: Read, Resource: "view", Lookup: "ViewArn"},
		{Name: "ListViews", Kind: List, Resource: "view", ListValue: "ViewArn"},
		{Name: "BatchGetView", Kind: List, Resource: "view", Names: "ViewArns", Failures: "Errors",
			FailureItem: map[string]any{"ViewArn": "$name", "ErrorMessage": "The view does not exist."}},
		{Name: "UpdateView", Kind: Update, Resource: "view", Lookup: "ViewArn", Fields: []string{"IncludedProperties", "Filters"}, Touch: []string{"LastUpdatedAt"}},
		{Name: "DeleteView", Kind: Delete, Resource: "view", Lookup: "ViewArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Indexes and views are stored metadata: no resource is discovered, indexed or replicated, and Search, ListResources and ListSupportedResourceTypes return UnknownOperationException with HTTP 501, so no AWS resource inventory is reported.",
		"Each Region holds at most one index, ACTIVE immediately; a second CreateIndex is refused with ValidationException, an aggregator index replicates nothing, ListIndexes lists only the requested Region and its Regions member is refused, and DeleteIndex removes the index at once.",
		"Views need no index, filter strings are stored without parsing and every view has the account scope, so Scope is refused. GetIndex, default views, managed views, service views and indexes, organization setup and member listings return UnknownOperationException with HTTP 501.",
	},
}

// accountScopeView refuses organization and organizational-unit view scopes,
// which need AWS Organizations; every view has the account scope.
func accountScopeView(input map[string]any) error {
	if _, ok := input["Scope"]; ok {
		return Failure("ValidationException", "The simulator account is standalone, so every view has the account scope; omit Scope.")
	}
	return nil
}

var incidentManager = &Service{
	Model: "ssm-incidents", Title: "AWS Systems Manager Incident Manager", IAMPrefix: "ssm-incidents", Issues: []int{211}, Global: true,
	Resources: []*Resource{
		{Kind: "responsePlan", Shape: "GetResponsePlanOutput", Summary: "ResponsePlanSummary", ID: "name",
			ARN: "arn:{partition}:ssm-incidents::{account}:response-plan/{name}", ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100},
	},
	Ops: []Op{
		{Name: "CreateResponsePlan", Kind: Create, Resource: "responsePlan", Check: noIncidentIntegrations},
		{Name: "GetResponsePlan", Kind: Read, Resource: "responsePlan", Lookup: "arn"},
		{Name: "ListResponsePlans", Kind: List, Resource: "responsePlan"},
		{Name: "UpdateResponsePlan", Kind: Update, Resource: "responsePlan", Lookup: "arn", Accept: []string{"clientToken"}, Check: noIncidentIntegrations,
			Fields: []string{"displayName", "chatChannel", "engagements", "actions"}},
		{Name: "DeleteResponsePlan", Kind: Delete, Resource: "responsePlan", Lookup: "arn", IdempotentDelete: true},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Response plans are stored metadata that need no replication set and are shared by every simulator Region, as their ARNs are Region-less: no incident is ever started, so no responder is engaged, no chat channel or SNS topic is notified and no Systems Manager Automation runbook runs.",
		"PagerDuty integrations are refused because they contact an external system; UpdateResponsePlan changes the display name, chat channel, engagements and actions only and refuses the incidentTemplate members, and its client token is accepted without replay.",
		"Incidents, timeline events, related items, findings, replication sets, resource policies and deletion protection return UnknownOperationException with HTTP 501; deleting a missing response plan succeeds as the official operation documents no not-found error.",
	},
}

// noIncidentIntegrations refuses third-party incident integrations, which
// would page responders through an external system.
func noIncidentIntegrations(input map[string]any) error {
	if _, ok := input["integrations"]; ok {
		return Failure("ValidationException", "PagerDuty integrations contact an external system and are not supported by the simulator.")
	}
	return nil
}

var telcoNetworkBuilder = &Service{
	Model: "tnb", Title: "AWS Telco Network Builder", IAMPrefix: "tnb", Issues: []int{212},
	Resources: []*Resource{
		{Kind: "functionPackage", Shape: "GetSolFunctionPackageOutput", Summary: "ListSolFunctionPackageInfo", ID: "hex:17:fp-",
			ARN: "arn:{partition}:tnb:{region}:{account}:function-package/{id}", ARNMember: "arn", IDMember: "id", Limit: 50,
			Generate: map[string]any{"onboardingState": "CREATED", "operationalState": "DISABLED", "usageState": "NOT_IN_USE",
				"metadata": map[string]any{"createdAt": "$now", "lastModified": "$now"}}},
		{Kind: "networkPackage", Shape: "GetSolNetworkPackageOutput", Summary: "ListSolNetworkPackageInfo", ID: "hex:17:np-",
			ARN: "arn:{partition}:tnb:{region}:{account}:network-package/{id}", ARNMember: "arn", IDMember: "id", Limit: 50,
			Generate: map[string]any{"nsdOnboardingState": "CREATED", "nsdOperationalState": "DISABLED", "nsdUsageState": "NOT_IN_USE",
				"metadata": map[string]any{"createdAt": "$now", "lastModified": "$now"}}},
	},
	Ops: []Op{
		{Name: "CreateSolFunctionPackage", Kind: Create, Resource: "functionPackage"},
		{Name: "GetSolFunctionPackage", Kind: Read, Resource: "functionPackage", Lookup: "vnfPkgId"},
		{Name: "ListSolFunctionPackages", Kind: List, Resource: "functionPackage"},
		{Name: "UpdateSolFunctionPackage", Kind: Update, Resource: "functionPackage", Lookup: "vnfPkgId", Fields: []string{"operationalState"}, Check: disabledPackage},
		{Name: "DeleteSolFunctionPackage", Kind: Delete, Resource: "functionPackage", Lookup: "vnfPkgId"},
		{Name: "CreateSolNetworkPackage", Kind: Create, Resource: "networkPackage"},
		{Name: "GetSolNetworkPackage", Kind: Read, Resource: "networkPackage", Lookup: "nsdInfoId"},
		{Name: "ListSolNetworkPackages", Kind: List, Resource: "networkPackage"},
		{Name: "UpdateSolNetworkPackage", Kind: Update, Resource: "networkPackage", Lookup: "nsdInfoId", Fields: []string{"nsdOperationalState"}, Check: disabledPackage},
		{Name: "DeleteSolNetworkPackage", Kind: Delete, Resource: "networkPackage", Lookup: "nsdInfoId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Function and network packages are empty CREATED metadata: package content upload, validation and descriptor retrieval return UnknownOperationException with HTTP 501, so no CSAR archive, VNFD or NSD is parsed and descriptor members stay absent.",
		"Packages stay DISABLED and NOT_IN_USE because their content is never onboarded; enabling one is refused, so every package can be deleted. Package updates do not change metadata.lastModified.",
		"Network instances, function instances and network operations (instantiate, update, terminate, cancel) return UnknownOperationException with HTTP 501, so no network or infrastructure is deployed.",
	},
}

// disabledPackage refuses enabling a package whose content was never
// onboarded; the simulator never onboards package content.
func disabledPackage(input map[string]any) error {
	if input["operationalState"] == "ENABLED" || input["nsdOperationalState"] == "ENABLED" {
		return Failure("ValidationException", "Package content is never onboarded by the simulator, so packages stay DISABLED.")
	}
	return nil
}

var userNotifications = &Service{
	Model: "notifications", Title: "AWS User Notifications", IAMPrefix: "notifications", Issues: []int{214}, Global: true,
	Resources: []*Resource{
		{Kind: "notificationConfiguration", Shape: "GetNotificationConfigurationResponse", Summary: "NotificationConfigurationStructure", ID: "alnum:27",
			ARN: "arn:{partition}:notifications::{account}:configuration/{id}", ARNMember: "arn", NameMember: "name", Limit: 100,
			Generate: map[string]any{"status": "INACTIVE", "subtype": "ACCOUNT", "creationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateNotificationConfiguration", Kind: Create, Resource: "notificationConfiguration"},
		{Name: "GetNotificationConfiguration", Kind: Read, Resource: "notificationConfiguration", Lookup: "arn"},
		{Name: "ListNotificationConfigurations", Kind: List, Resource: "notificationConfiguration", Filters: map[string]string{"status": "status", "subtype": "subtype"}},
		{Name: "UpdateNotificationConfiguration", Kind: Update, Resource: "notificationConfiguration", Lookup: "arn", Fields: []string{"name", "description", "aggregationDuration"}},
		{Name: "DeleteNotificationConfiguration", Kind: Delete, Resource: "notificationConfiguration", Lookup: "arn"},
		{Name: "TagResource", Kind: Tag, Lookup: "arn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "arn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "arn"},
	},
	Bounds: []string{
		"Notification configurations are stored metadata that stay INACTIVE: no event is matched, aggregated or delivered and no console, email, chat or mobile channel is contacted.",
		"Event rules, channel associations, managed notifications, notification events, notification hubs, organization access and organizational units return UnknownOperationException with HTTP 501; ListNotificationConfigurations filters by status and subtype only.",
	},
}

var wellArchitected = &Service{
	Model: "wellarchitected", Title: "AWS Well-Architected Tool", IAMPrefix: "wellarchitected", Issues: []int{216},
	Resources: []*Resource{
		{Kind: "workload", Shape: "Workload", Summary: "WorkloadSummary", ID: "hex:32", ARN: "arn:{partition}:wellarchitected:{region}:{account}:workload/{id}",
			ARNMember: "WorkloadArn", IDMember: "WorkloadId", NameMember: "WorkloadName", Unique: true, Limit: 100,
			Generate: map[string]any{"Owner": "$account", "UpdatedAt": "$now"}},
		{Kind: "globalSettings", Shape: "GetGlobalSettingsOutput", Singleton: true, ID: "name",
			ARN: "arn:{partition}:wellarchitected:{region}:{account}:global-settings", NameMember: "OrganizationSharingStatus",
			Generate: map[string]any{"OrganizationSharingStatus": "DISABLED", "DiscoveryIntegrationStatus": "DISABLED"}},
	},
	Ops: []Op{
		{Name: "CreateWorkload", Kind: Create, Resource: "workload", Check: workloadIntegrations},
		{Name: "GetWorkload", Kind: Read, Resource: "workload", Lookup: "WorkloadId"},
		{Name: "ListWorkloads", Kind: List, Resource: "workload"},
		{Name: "UpdateWorkload", Kind: Update, Resource: "workload", Lookup: "WorkloadId", Check: workloadIntegrations, Touch: []string{"UpdatedAt"},
			Fields: []string{"WorkloadName", "Description", "Environment", "AccountIds", "AwsRegions", "NonAwsRegions", "PillarPriorities", "ArchitecturalDesign",
				"ReviewOwner", "IsReviewOwnerUpdateAcknowledged", "IndustryType", "Industry", "Notes", "ImprovementStatus", "Applications"}},
		{Name: "DeleteWorkload", Kind: Delete, Resource: "workload", Lookup: "WorkloadId", Accept: []string{"ClientRequestToken"}},
		{Name: "GetGlobalSettings", Kind: Read, Resource: "globalSettings"},
		{Name: "UpdateGlobalSettings", Kind: Update, Resource: "globalSettings", Fields: []string{"OrganizationSharingStatus", "DiscoveryIntegrationStatus"}, Check: standaloneSettings},
		{Name: "TagResource", Kind: Tag, Lookup: "WorkloadArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "WorkloadArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "WorkloadArn"},
	},
	Bounds: []string{
		"Workloads are stored metadata: lens aliases are stored without loading any lens, so no lens review, question, answer, improvement plan, risk count, milestone or report is produced, and AWS Trusted Advisor, AppRegistry and Jira are never contacted.",
		"Jira configuration and Trusted Advisor discovery are refused, profiles and review templates cannot be attached, ListWorkloads refuses WorkloadNamePrefix, and the delete client token is accepted without replay.",
		"Global settings stay DISABLED because the simulator account is not in an organization and discovery integration needs Trusted Advisor. Lenses, lens reviews, answers, milestones, profiles, review templates, shares and notifications return UnknownOperationException with HTTP 501.",
	},
}

// workloadIntegrations refuses workload integrations with Jira and Trusted
// Advisor, which would contact or read systems outside the simulator.
func workloadIntegrations(input map[string]any) error {
	if _, ok := input["JiraConfiguration"]; ok {
		return Failure("ValidationException", "Jira integration contacts an external system and is not supported by the simulator.")
	}
	if discovery, ok := input["DiscoveryConfig"].(map[string]any); ok && discovery["TrustedAdvisorIntegrationStatus"] == "ENABLED" {
		return Failure("ValidationException", "Trusted Advisor discovery is not performed by the simulator.")
	}
	return nil
}

// standaloneSettings keeps global settings that need an organization, Jira or
// Trusted Advisor disabled.
func standaloneSettings(input map[string]any) error {
	if _, ok := input["JiraConfiguration"]; ok {
		return Failure("ValidationException", "Jira integration contacts an external system and is not supported by the simulator.")
	}
	if input["OrganizationSharingStatus"] == "ENABLED" || input["DiscoveryIntegrationStatus"] == "ENABLED" {
		return Failure("ValidationException", "Organization sharing and discovery integration need AWS Organizations and Trusted Advisor, which the simulator does not provide.")
	}
	return nil
}

var budgets = &Service{
	Model: "budgets", Title: "AWS Budgets", IAMPrefix: "budgets", Issues: []int{283}, Global: true,
	AccountMembers: []string{"AccountId"},
	Resources: []*Resource{
		{Kind: "budget", Shape: "Budget", ID: "name", ARN: "arn:{partition}:budgets::{account}:budget/{name}",
			NameMember: "BudgetName", Unique: true, Limit: 100,
			Generate: map[string]any{"LastUpdatedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateBudget", Action: "ModifyBudget", Kind: Create, Resource: "budget", From: "Budget", Check: budgetDefinition},
		{Name: "DescribeBudget", Action: "ViewBudget", Kind: Read, Resource: "budget", Lookup: "BudgetName", Accept: []string{"ShowFilterExpression"}},
		{Name: "DescribeBudgets", Action: "ViewBudget", Kind: List, Resource: "budget", Accept: []string{"ShowFilterExpression"}},
		{Name: "UpdateBudget", Action: "ModifyBudget", Kind: Update, Resource: "budget", From: "NewBudget", Lookup: "NewBudget.BudgetName", Check: budgetDefinition, Touch: []string{"LastUpdatedTime"},
			Fields: []string{"BudgetName", "BudgetLimit", "PlannedBudgetLimits", "CostFilters", "CostTypes", "TimeUnit", "TimePeriod", "BudgetType", "Metrics"}},
		{Name: "DeleteBudget", Action: "ModifyBudget", Kind: Delete, Resource: "budget", Lookup: "BudgetName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Budgets are stored definitions: no cost, usage, reservation or Savings Plans data is read, so CalculatedSpend, forecasts and health status are never reported, and the time period, cost types and auto-adjustment are not filled in with service defaults.",
		"CalculatedSpend, HealthStatus, AutoAdjustData, FilterExpression, BillingViewArn, LastUpdatedTime and notifications with subscribers are refused; ShowFilterExpression is accepted because no filter expression is stored. UpdateBudget changes only the members supplied in NewBudget.",
		"Notifications, subscribers, budget actions, performance history and action histories return UnknownOperationException with HTTP 501, so no email or SNS notification is sent and no IAM, SCP or Systems Manager action runs.",
	},
}

// budgetOwnedMembers are budget members computed by AWS from billing data or
// tied to billing views and filter expressions the simulator does not model.
var budgetOwnedMembers = []string{"CalculatedSpend", "HealthStatus", "AutoAdjustData", "FilterExpression", "BillingViewArn", "LastUpdatedTime"}

// budgetDefinition refuses AWS-computed budget members and applies the
// documented limit rules of cost and usage budgets.
func budgetDefinition(input map[string]any) error {
	budget, _ := input["Budget"].(map[string]any)
	if budget == nil {
		budget, _ = input["NewBudget"].(map[string]any)
	}
	for _, member := range budgetOwnedMembers {
		if _, ok := budget[member]; ok {
			return Failure("InvalidParameterException", member+" is computed from billing data or billing views and is not supported by the simulator.")
		}
	}
	_, limit := budget["BudgetLimit"]
	_, planned := budget["PlannedBudgetLimits"]
	if limit && planned {
		return Failure("InvalidParameterException", "Specify BudgetLimit or PlannedBudgetLimits, not both.")
	}
	if kind := budget["BudgetType"]; (kind == "COST" || kind == "USAGE") && !limit && !planned {
		return Failure("InvalidParameterException", "Cost and usage budgets need BudgetLimit or PlannedBudgetLimits.")
	}
	return nil
}

var billingConductor = &Service{
	Model: "billingconductor", Title: "AWS Billing Conductor", IAMPrefix: "billingconductor", Issues: []int{281}, Global: true,
	Resources: []*Resource{
		{Kind: "pricingRule", Shape: "PricingRuleListElement", ID: "alnum:10", ARN: "arn:{partition}:billingconductor::{account}:pricingrule/{id}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"AssociatedPricingPlanCount": 0}},
	},
	Ops: []Op{
		{Name: "CreatePricingRule", Kind: Create, Resource: "pricingRule", Check: pricingRuleScope},
		{Name: "ListPricingRules", Kind: List, Resource: "pricingRule"},
		{Name: "UpdatePricingRule", Kind: Update, Resource: "pricingRule", Lookup: "Arn", Check: pricingRuleModifier, Merged: pricingRuleMerged,
			Fields: []string{"Name", "Description", "Type", "ModifierPercentage", "Tiering"}},
		{Name: "DeletePricingRule", Kind: Delete, Resource: "pricingRule", Lookup: "Arn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Pricing rules are stored metadata that need no AWS Organizations management account: no pricing plan, billing group or pro forma cost is computed and no billing data is read, so AssociatedPricingPlanCount stays 0 and creation and modification times are not reported.",
		"ListPricingRules lists every stored rule and refuses BillingPeriod and Filters. ListTagsForResource is not implemented because its official HTTP 204 response cannot carry the Tags body, so tags are written by CreatePricingRule, TagResource and UntagResource only.",
		"Pricing plans, billing groups, account associations, custom line items and cost reports return UnknownOperationException with HTTP 501.",
	},
}

// pricingRuleScope applies the documented member rules of each pricing rule
// scope and type.
func pricingRuleScope(input map[string]any) error {
	scope, _ := input["Scope"].(string)
	_, service := input["Service"]
	_, entity := input["BillingEntity"]
	_, usage := input["UsageType"]
	_, operation := input["Operation"]
	required := map[string]bool{"SERVICE": service, "BILLING_ENTITY": entity, "SKU": service && usage && operation, "GLOBAL": true}
	if !required[scope] {
		return Failure("ValidationException", "SERVICE scope needs Service, BILLING_ENTITY scope needs BillingEntity and SKU scope needs Service, UsageType and Operation.")
	}
	if scope == "GLOBAL" && (service || entity || usage || operation) {
		return Failure("ValidationException", "GLOBAL pricing rules take no Service, BillingEntity, UsageType or Operation.")
	}
	_, modifier := input["ModifierPercentage"]
	_, tiering := input["Tiering"]
	if input["Type"] == "TIERING" && (!tiering || modifier) || input["Type"] != "TIERING" && (!modifier || tiering) {
		return Failure("ValidationException", "TIERING pricing rules need Tiering; MARKUP and DISCOUNT pricing rules need ModifierPercentage.")
	}
	return nil
}

// pricingRuleMerged keeps an updated rule consistent with its type: a type
// change drops the other type's configuration, and the resulting rule must
// carry its own (Tiering for TIERING, ModifierPercentage otherwise).
func pricingRuleMerged(document map[string]any) error {
	if document["Type"] == "TIERING" {
		delete(document, "ModifierPercentage")
	} else {
		delete(document, "Tiering")
	}
	_, modifier := document["ModifierPercentage"]
	_, tiering := document["Tiering"]
	if document["Type"] == "TIERING" && !tiering || document["Type"] != "TIERING" && !modifier {
		return Failure("ValidationException", "A TIERING pricing rule needs Tiering; a MARKUP or DISCOUNT pricing rule needs ModifierPercentage.")
	}
	return nil
}

// pricingRuleModifier refuses an update that sets both a percentage modifier
// and a tiering configuration.
func pricingRuleModifier(input map[string]any) error {
	_, modifier := input["ModifierPercentage"]
	_, tiering := input["Tiering"]
	if modifier && tiering {
		return Failure("ValidationException", "A pricing rule has either ModifierPercentage or Tiering, not both.")
	}
	return nil
}

var costUsageReports = &Service{
	Model: "cur", Title: "AWS Cost and Usage Report Service", IAMPrefix: "cur", Issues: []int{284},
	Resources: []*Resource{
		{Kind: "reportDefinition", Shape: "ReportDefinition", ID: "name", ARN: "arn:{partition}:cur:{region}:{account}:definition/{name}",
			NameMember: "ReportName", Unique: true, Limit: 25},
	},
	Ops: []Op{
		{Name: "PutReportDefinition", Kind: Create, Resource: "reportDefinition", From: "ReportDefinition", Check: reportDefinition},
		{Name: "DescribeReportDefinitions", Kind: List, Resource: "reportDefinition"},
		{Name: "ModifyReportDefinition", Kind: Update, Resource: "reportDefinition", From: "ReportDefinition", Lookup: "ReportName", Check: reportModification,
			Fields: []string{"TimeUnit", "Format", "Compression", "AdditionalSchemaElements", "S3Bucket", "S3Prefix", "S3Region", "AdditionalArtifacts", "RefreshClosedReports", "ReportVersioning"}},
		{Name: "DeleteReportDefinition", Kind: Delete, Resource: "reportDefinition", Lookup: "ReportName"},
	},
	Bounds: []string{
		"Report definitions are stored configuration: no cost or usage report is generated or refreshed, nothing is written to the named Amazon S3 bucket, which is not checked, and no Athena, Amazon Redshift or Amazon QuickSight artifact is produced.",
		"ReportStatus and BillingViewArn are refused; report definitions are kept per simulator Region although AWS serves this API from us-east-1 only.",
		"Tags supplied at creation are stored, but TagResource, UntagResource and ListTagsForResource are not implemented because they identify the report by name rather than ARN.",
	},
}

// reportDefinition applies the documented format, compression and artifact
// combinations and refuses members reported by AWS.
func reportDefinition(input map[string]any) error {
	report, _ := input["ReportDefinition"].(map[string]any)
	for _, member := range []string{"ReportStatus", "BillingViewArn"} {
		if _, ok := report[member]; ok {
			return Failure("ValidationException", member+" is not supported by the simulator.")
		}
	}
	format, compression := report["Format"], report["Compression"]
	if (format == "Parquet") != (compression == "Parquet") {
		return Failure("ValidationException", "Parquet reports use Parquet compression; textORcsv reports use ZIP or GZIP compression.")
	}
	artifacts, _ := report["AdditionalArtifacts"].([]any)
	for _, artifact := range artifacts {
		if artifact == "ATHENA" && (len(artifacts) > 1 || format != "Parquet" || report["ReportVersioning"] != "OVERWRITE_REPORT") {
			return Failure("ValidationException", "Athena integration needs a Parquet report with OVERWRITE_REPORT versioning and no other artifact.")
		}
		if artifact != "ATHENA" && format != "textORcsv" {
			return Failure("ValidationException", "Amazon Redshift and Amazon QuickSight artifacts need a textORcsv report.")
		}
	}
	return nil
}

// reportModification also requires the modified definition to keep its name.
func reportModification(input map[string]any) error {
	if report, _ := input["ReportDefinition"].(map[string]any); report["ReportName"] != input["ReportName"] {
		return Failure("ValidationException", "A report definition cannot be renamed; ReportDefinition.ReportName must equal ReportName.")
	}
	return reportDefinition(input)
}

var xray = &Service{
	Model: "xray", Title: "AWS X-Ray", IAMPrefix: "xray", Issues: []int{141},
	Resources: []*Resource{
		{Kind: "group", Shape: "Group", Summary: "GroupSummary", ID: "alnum:32", ARN: "arn:{partition}:xray:{region}:{account}:group/{name}/{id}",
			ARNMember: "GroupARN", NameMember: "GroupName", Unique: true, Limit: 25},
	},
	Ops: []Op{
		{Name: "CreateGroup", Kind: Create, Resource: "group"},
		{Name: "GetGroup", Kind: Read, Resource: "group", Lookup: "GroupName", LookupAlt: []string{"GroupARN"}},
		{Name: "GetGroups", Kind: List, Resource: "group"},
		{Name: "UpdateGroup", Kind: Update, Resource: "group", Lookup: "GroupName", LookupAlt: []string{"GroupARN"}, Fields: []string{"FilterExpression", "InsightsConfiguration"}},
		{Name: "DeleteGroup", Kind: Delete, Resource: "group", Lookup: "GroupName", LookupAlt: []string{"GroupARN"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Groups are stored metadata: no trace segment or telemetry is accepted, filter expressions are stored without parsing or evaluation, and no insight or insight notification is generated. The implicit Default group is not listed.",
		"Traces, service graphs, statistics, insights, trace retrieval, segment destinations, indexing rules and resource policies return UnknownOperationException with HTTP 501.",
		"Sampling rules and the encryption configuration are not implemented: a sampling rule record nests its identity inside SamplingRule, and an encryption configuration switched back to NONE would keep its key, which the bounded model cannot represent. ListTagsForResource refuses NextToken.",
	},
}

var faultInjection = &Service{
	Model: "fis", Title: "AWS Fault Injection Service", IAMPrefix: "fis", Issues: []int{140},
	Resources: []*Resource{
		{Kind: "experimentTemplate", Shape: "ExperimentTemplate", Summary: "ExperimentTemplateSummary", ID: "alnum:14:EXT",
			ARN: "arn:{partition}:fis:{region}:{account}:experiment-template/{id}", ARNMember: "arn", IDMember: "id", Limit: 100,
			Generate: map[string]any{"creationTime": "$now", "lastUpdateTime": "$now", "targetAccountConfigurationsCount": 0,
				"experimentOptions": map[string]any{"accountTargeting": "single-account", "emptyTargetResolutionMode": "fail"}}},
	},
	Ops: []Op{
		{Name: "CreateExperimentTemplate", Kind: Create, Resource: "experimentTemplate", Check: experimentTemplate},
		{Name: "GetExperimentTemplate", Kind: Read, Resource: "experimentTemplate", Lookup: "id"},
		{Name: "ListExperimentTemplates", Kind: List, Resource: "experimentTemplate"},
		{Name: "UpdateExperimentTemplate", Kind: Update, Resource: "experimentTemplate", Lookup: "id", Check: experimentTemplate, Touch: []string{"lastUpdateTime"},
			Fields: []string{"description", "stopConditions", "targets", "actions", "roleArn", "logConfiguration", "experimentReportConfiguration"}},
		{Name: "DeleteExperimentTemplate", Kind: Delete, Resource: "experimentTemplate", Lookup: "id"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Experiment templates are stored metadata: StartExperiment, StopExperiment and every experiment, action, target resource type and safety lever operation return UnknownOperationException with HTTP 501, so no fault is ever injected, no target is resolved, no role is assumed and no log or report is written.",
		"Action identifiers, target resource types, selection modes, filters and alarm ARNs are stored without checking the AWS action catalog or the named resources; experiment options default to single-account targeting and the fail empty-target mode.",
		"Multi-account targeting and target account configurations are refused, and UpdateExperimentTemplate refuses experimentOptions.",
	},
}

// experimentTemplate refuses multi-account experiments and applies the
// documented stop-condition sources.
func experimentTemplate(input map[string]any) error {
	if options, ok := input["experimentOptions"].(map[string]any); ok && options["accountTargeting"] == "multi-account" {
		return Failure("ValidationException", "Multi-account experiments need target account configurations, which the simulator does not support.")
	}
	conditions, _ := input["stopConditions"].([]any)
	for _, item := range conditions {
		condition, _ := item.(map[string]any)
		_, value := condition["value"]
		switch condition["source"] {
		case "none":
			if value {
				return Failure("ValidationException", "A stop condition with source none takes no value.")
			}
		case "aws:cloudwatch:alarm":
			if !value {
				return Failure("ValidationException", "A CloudWatch alarm stop condition needs the alarm ARN as its value.")
			}
		default:
			return Failure("ValidationException", fmt.Sprintf("Stop condition source %v must be none or aws:cloudwatch:alarm.", condition["source"]))
		}
	}
	return nil
}

var cloud9 = &Service{
	Model: "cloud9", Title: "AWS Cloud9", IAMPrefix: "cloud9", Issues: []int{133},
	Resources: []*Resource{
		{Kind: "environment", Shape: "Environment", ID: "hex:32", ARN: "arn:{partition}:cloud9:{region}:{account}:environment:{id}",
			ARNMember: "arn", IDMember: "id", NameMember: "name", Limit: 25,
			Generate: map[string]any{"environmentId": "$id", "type": "ec2", "connectionType": "CONNECT_SSH", "lifecycle": map[string]any{"status": "CREATED"}}},
	},
	Ops: []Op{
		{Name: "CreateEnvironmentEC2", Kind: Create, Resource: "environment", Check: cloud9Environment,
			Accept: []string{"instanceType", "imageId", "subnetId", "automaticStopTimeMinutes"}},
		{Name: "DescribeEnvironments", Kind: List, Resource: "environment", Names: "environmentIds"},
		{Name: "ListEnvironments", Kind: List, Resource: "environment", ListValue: "id"},
		{Name: "UpdateEnvironment", Kind: Update, Resource: "environment", Lookup: "environmentId", Fields: []string{"name", "description"}},
		{Name: "DeleteEnvironment", Kind: Delete, Resource: "environment", Lookup: "environmentId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"EC2 environments are stored metadata: no EC2 instance, AMI, security group or IDE is launched, connected or stopped, so instanceType, imageId, subnetId and automaticStopTimeMinutes are validated and accepted but not used, as AWS never returns them.",
		"ownerArn is required because the simulator does not resolve the calling identity's ARN; clientRequestToken, dryRun and managed credentials actions are refused, and only the documented Amazon Linux 2, Amazon Linux 2023 and Ubuntu 22.04 images are accepted.",
		"Environment status and environment memberships return UnknownOperationException with HTTP 501.",
	},
}

// cloud9Images are the documented AMI aliases and SSM paths still offered.
var cloud9Images = regexp.MustCompile(`^(resolve:ssm:/aws/service/cloud9/amis/)?(amazonlinux-2-x86_64|amazonlinux-2023-x86_64|ubuntu-22\.04-x86_64)$`)

// cloud9Environment requires an owner ARN and a documented Cloud9 image.
func cloud9Environment(input map[string]any) error {
	if _, ok := input["ownerArn"]; !ok {
		return Failure("BadRequestException", "Specify ownerArn; the simulator does not resolve the calling identity's ARN.")
	}
	if image, _ := input["imageId"].(string); !cloud9Images.MatchString(image) {
		return Failure("BadRequestException", "imageId must be a documented Amazon Linux 2, Amazon Linux 2023 or Ubuntu 22.04 alias or SSM path.")
	}
	return nil
}
