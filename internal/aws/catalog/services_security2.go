package catalog

import (
	"regexp"
	"strconv"
	"unicode/utf8"
)

// Reviewed bounded specifications for further security, identity and
// compliance services. Each binds only official operations whose effect is
// configuration metadata: nothing is scanned or detected, no finding,
// evidence or key material exists, no directory, identity store or HSM is
// provisioned, no log is ingested and no external system is contacted.
//
// AWS Directory Service and AWS CloudHSM are not served: every configurable
// resource of either belongs to a directory or an HSM cluster, and creating
// one of those provisions infrastructure.

var guardDuty = &Service{
	Model: "guardduty", Title: "Amazon GuardDuty", IAMPrefix: "guardduty", Issues: []int{255},
	Resources: []*Resource{
		{Kind: "detector", Shape: "GetDetectorResponse", ID: "hex:32", IDMember: "DetectorId", Limit: 1, Cascade: true,
			ARN: "arn:{partition}:guardduty:{region}:{account}:detector/{id}",
			Generate: map[string]any{"CreatedAt": "$now", "UpdatedAt": "$now", "Status": "ENABLED", "FindingPublishingFrequency": "SIX_HOURS",
				"ServiceRole": "$fmt:arn:{partition}:iam::{account}:role/aws-service-role/guardduty.amazonaws.com/AWSServiceRoleForAmazonGuardDuty"}},
		{Kind: "filter", Shape: "GetFilterResponse", Parent: "detector", ID: "name", NameMember: "Name", Unique: true, Limit: 100,
			ARN:      "arn:{partition}:guardduty:{region}:{account}:detector/{parent}/filter/{name}",
			Generate: map[string]any{"Action": "NOOP"}},
	},
	Ops: []Op{
		{Name: "CreateDetector", Kind: Create, Resource: "detector", Accept: []string{"Enable"}, Check: enabledDetector},
		{Name: "GetDetector", Kind: Read, Resource: "detector", Lookup: "DetectorId"},
		{Name: "ListDetectors", Kind: List, Resource: "detector", ListValue: "DetectorId"},
		{Name: "UpdateDetector", Kind: Update, Resource: "detector", Lookup: "DetectorId", Accept: []string{"Enable"}, Check: enabledDetector,
			Fields: []string{"FindingPublishingFrequency"}, Touch: []string{"UpdatedAt"}},
		{Name: "DeleteDetector", Kind: Delete, Resource: "detector", Lookup: "DetectorId"},
		{Name: "CreateFilter", Kind: Create, Resource: "filter", Parent: "DetectorId"},
		{Name: "GetFilter", Kind: Read, Resource: "filter", Parent: "DetectorId", Lookup: "FilterName"},
		{Name: "ListFilters", Kind: List, Resource: "filter", Parent: "DetectorId", ListValue: "Name"},
		{Name: "UpdateFilter", Kind: Update, Resource: "filter", Parent: "DetectorId", Lookup: "FilterName", Fields: []string{"Description", "Action", "Rank", "FindingCriteria"}},
		{Name: "DeleteFilter", Kind: Delete, Resource: "filter", Parent: "DetectorId", Lookup: "FilterName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Detectors and findings filters are configuration metadata: no CloudTrail, VPC Flow Logs, DNS, runtime or other data source is read, no finding is generated, archived or published, and no service-linked role is created.",
		"Each Region holds one enabled detector: a second CreateDetector is refused with LimitExceededException rather than BadRequestException, disabling (Enable false), DataSources and Features are refused, and deleting a detector deletes its filters.",
		"Missing detectors and filters are reported as BadRequestException, the only client error the official model defines. Filter Rank is stored only when supplied and never renumbered, and Action defaults to NOOP.",
		"Findings, sample findings, IP sets, threat intel and threat entity sets, malware protection plans and scans, publishing destinations, members, invitations, coverage, usage statistics and organization APIs return UnknownOperationException with HTTP 501.",
	},
}

// enabledDetector keeps every stored detector enabled: disabling it, or
// configuring data sources and features, would change monitoring that the
// simulator never performs.
func enabledDetector(input map[string]any) error {
	if enabled, ok := input["Enable"].(bool); ok && !enabled {
		return Failure("BadRequestException", "The simulator keeps detectors enabled; disabling a detector is not supported.")
	}
	for _, member := range []string{"DataSources", "Features"} {
		if _, ok := input[member]; ok {
			return Failure("BadRequestException", member+" are not supported by the simulator because no data source is monitored.")
		}
	}
	return nil
}

var auditManager = &Service{
	Model: "auditmanager", Title: "AWS Audit Manager", IAMPrefix: "auditmanager", Issues: []int{256},
	Resources: []*Resource{
		{Kind: "control", Shape: "Control", Summary: "ControlMetadata", ID: "uuid",
			ARN:       "arn:{partition}:auditmanager:{region}:{account}:control/{id}",
			ARNMember: "arn", IDMember: "id", NameMember: "name", Limit: 100,
			Generate: map[string]any{"type": "Custom", "state": "ACTIVE", "createdAt": "$now", "lastUpdatedAt": "$now"}},
		{Kind: "settings", Shape: "Settings", Singleton: true, ID: "name", NameMember: "kmsKey",
			ARN: "arn:{partition}:auditmanager:{region}:{account}:settings",
			Generate: map[string]any{"isAwsOrgEnabled": false, "evidenceFinderEnablement": map[string]any{"enablementStatus": "DISABLED"},
				"deregistrationPolicy": map[string]any{"deleteResources": "DEFAULT"}}},
	},
	Ops: []Op{
		{Name: "CreateControl", Kind: Create, Resource: "control"},
		{Name: "GetControl", Kind: Read, Resource: "control", Lookup: "controlId"},
		{Name: "ListControls", Kind: List, Resource: "control", Filters: map[string]string{"controlType": "type"}},
		{Name: "UpdateControl", Kind: Update, Resource: "control", Lookup: "controlId", Touch: []string{"lastUpdatedAt"},
			Fields: []string{"name", "description", "testingInformation", "actionPlanTitle", "actionPlanInstructions", "controlMappingSources"}},
		{Name: "DeleteControl", Kind: Delete, Resource: "control", Lookup: "controlId"},
		{Name: "GetSettings", Kind: Read, Resource: "settings", Accept: []string{"attribute"}, Check: allSettings},
		{Name: "UpdateSettings", Kind: Update, Resource: "settings",
			Fields: []string{"snsTopic", "defaultAssessmentReportsDestination", "defaultProcessOwners", "kmsKey", "deregistrationPolicy", "defaultExportDestination"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Custom controls and account settings are configuration metadata that need no prior RegisterAccount call: no assessment runs, no evidence is collected from AWS CloudTrail, AWS Config, AWS Security Hub or API calls, and no report is written to Amazon S3.",
		"ListControls lists only stored Custom controls (no Standard or Core control is served) and refuses controlCatalogId; control mapping sources get no sourceId, and controlSources, createdBy and lastUpdatedBy are not reported.",
		"GetSettings accepts attribute ALL only. UpdateSettings refuses evidenceFinderEnabled, and the SNS topic, report and export destinations, process owners and KMS key are stored without being checked or used.",
		"Assessments, assessment frameworks and their sharing, evidence, delegations, assessment reports, insights, notifications, account registration and organization APIs return UnknownOperationException with HTTP 501.",
	},
}

// allSettings serves the complete settings document only; selecting a
// single attribute would need a projection the bounded read does not make.
func allSettings(input map[string]any) error {
	if input["attribute"] != "ALL" {
		return Failure("ValidationException", "The simulator returns Audit Manager settings for attribute ALL only.")
	}
	return nil
}

var cloudDirectory = &Service{
	Model: "clouddirectory", Title: "Amazon Cloud Directory", IAMPrefix: "clouddirectory", Issues: []int{257},
	Resources: []*Resource{
		// The create request is the only reviewed official structure that
		// holds the development schema name.
		{Kind: "schema", Shape: "CreateSchemaRequest", ID: "name", ARN: "arn:{partition}:clouddirectory:{region}:{account}:schema/development/{name}",
			ARNMember: "SchemaArn", NameMember: "Name", Unique: true, Limit: 20, Cascade: true},
		{Kind: "facet", Shape: "Facet", Parent: "schema", ID: "name", ARN: "arn:{partition}:clouddirectory:{region}:{account}:schema/development/{parent}/facet/{name}",
			NameMember: "Name", Unique: true, Limit: 100},
	},
	Ops: []Op{
		{Name: "CreateSchema", Kind: Create, Resource: "schema"},
		{Name: "ListDevelopmentSchemaArns", Kind: List, Resource: "schema", ListValue: "SchemaArn"},
		{Name: "DeleteSchema", Kind: Delete, Resource: "schema", Lookup: "SchemaArn"},
		{Name: "CreateFacet", Kind: Create, Resource: "facet", Parent: "SchemaArn"},
		{Name: "GetFacet", Kind: Read, Resource: "facet", Parent: "SchemaArn", Lookup: "Name"},
		{Name: "ListFacetNames", Kind: List, Resource: "facet", Parent: "SchemaArn", ListValue: "Name"},
		{Name: "UpdateFacet", Kind: Update, Resource: "facet", Parent: "SchemaArn", Lookup: "Name", Fields: []string{"ObjectType"}},
		{Name: "DeleteFacet", Kind: Delete, Resource: "facet", Parent: "SchemaArn", Lookup: "Name"},
	},
	Bounds: []string{
		"Development schemas and their facets are configuration metadata: schemas are never published, applied or upgraded and no directory is created, so no object, link, index or policy exists.",
		"Facets are stored without attributes: Attributes and AttributeUpdates are refused and UpdateFacet changes ObjectType only. Deleting a development schema deletes its facets.",
		"Facet operations are authorized against the schema ARN followed by /facet/<facet name> rather than the schema ARN alone, and a missing facet is reported as ResourceNotFoundException rather than FacetNotFoundException.",
		"GetSchemaAsJson, PutSchemaFromJson, UpdateSchema, published, applied and managed schemas, typed link facets, directories, objects, indexes, policies, batch operations and tagging (which applies to directories only) return UnknownOperationException with HTTP 501.",
	},
}

var inspector = &Service{
	Model: "inspector2", Title: "Amazon Inspector", IAMPrefix: "inspector2", Issues: []int{261},
	Resources: []*Resource{
		{Kind: "filter", Shape: "Filter", ID: "hex:16", ARN: "arn:{partition}:inspector2:{region}:{account}:owner/{account}/filter/{id}",
			ARNMember: "arn", Limit: 100, Aliases: map[string]string{"filterCriteria": "criteria"},
			Generate: map[string]any{"ownerId": "$account", "createdAt": "$now", "updatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateFilter", Kind: Create, Resource: "filter"},
		{Name: "ListFilters", Kind: List, Resource: "filter", Names: "arns", Filters: map[string]string{"action": "action"}},
		{Name: "UpdateFilter", Kind: Update, Resource: "filter", Lookup: "filterArn", Touch: []string{"updatedAt"},
			Fields: []string{"action", "description", "filterCriteria", "name", "reason"}},
		{Name: "DeleteFilter", Kind: Delete, Resource: "filter", Lookup: "arn"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Findings filters are configuration metadata that need no prior Enable call: no Amazon EC2 instance, Amazon ECR image, Lambda function or code repository is scanned and no finding exists, so a SUPPRESS filter hides nothing.",
		"ListFilters returns stored filters in creation order; a filter ARN named in arns that does not exist fails the request with ValidationException.",
		"Scanning enablement, findings, coverage, CIS scan configurations and sessions, code security integrations and scans, SBOM exports, findings reports, encryption keys, members, delegated administrators and organization configuration return UnknownOperationException with HTTP 501.",
	},
}

var macie = &Service{
	Model: "macie2", Title: "Amazon Macie", IAMPrefix: "macie2", Issues: []int{262},
	Resources: []*Resource{
		{Kind: "allowList", Shape: "GetAllowListResponse", Summary: "AllowListSummary", ID: "alnum:22",
			ARN:       "arn:{partition}:macie2:{region}:{account}:allow-list/{id}",
			ARNMember: "arn", IDMember: "id", NameMember: "name", Limit: 10,
			Generate: map[string]any{"createdAt": "$now", "updatedAt": "$now", "status": map[string]any{"code": "OK"}}},
		{Kind: "customDataIdentifier", Shape: "GetCustomDataIdentifierResponse", Summary: "CustomDataIdentifierSummary", ID: "uuid",
			ARN:       "arn:{partition}:macie2:{region}:{account}:custom-data-identifier/{id}",
			ARNMember: "arn", IDMember: "id", NameMember: "name", Limit: 100,
			Generate: map[string]any{"customDataIdentifierId": "$id", "createdAt": "$now", "deleted": false, "maximumMatchDistance": 50,
				"severityLevels": []any{map[string]any{"occurrencesThreshold": 1, "severity": "MEDIUM"}}}},
		{Kind: "findingsFilter", Shape: "GetFindingsFilterResponse", Summary: "FindingsFilterListItem", ID: "uuid",
			ARN:       "arn:{partition}:macie2:{region}:{account}:findings-filter/{id}",
			ARNMember: "arn", IDMember: "id", NameMember: "name", Limit: 100},
	},
	Ops: []Op{
		{Name: "CreateAllowList", Kind: Create, Resource: "allowList", Check: regexAllowList},
		{Name: "GetAllowList", Kind: Read, Resource: "allowList", Lookup: "id"},
		{Name: "ListAllowLists", Kind: List, Resource: "allowList"},
		{Name: "UpdateAllowList", Kind: Update, Resource: "allowList", Lookup: "id", Check: regexAllowList,
			Fields: []string{"criteria", "description", "name"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteAllowList", Kind: Delete, Resource: "allowList", Lookup: "id", Accept: []string{"ignoreJobChecks"}},
		{Name: "CreateCustomDataIdentifier", Kind: Create, Resource: "customDataIdentifier", Check: customDataIdentifier},
		{Name: "GetCustomDataIdentifier", Kind: Read, Resource: "customDataIdentifier", Lookup: "id"},
		{Name: "ListCustomDataIdentifiers", Kind: List, Resource: "customDataIdentifier"},
		{Name: "DeleteCustomDataIdentifier", Kind: Delete, Resource: "customDataIdentifier", Lookup: "id"},
		{Name: "CreateFindingsFilter", Kind: Create, Resource: "findingsFilter", Check: macieFindingsFilter},
		{Name: "GetFindingsFilter", Kind: Read, Resource: "findingsFilter", Lookup: "id"},
		{Name: "ListFindingsFilters", Kind: List, Resource: "findingsFilter"},
		{Name: "UpdateFindingsFilter", Kind: Update, Resource: "findingsFilter", Lookup: "id", Check: macieFindingsFilter, Accept: []string{"clientToken"},
			Fields: []string{"action", "description", "findingCriteria", "name", "position"}},
		{Name: "DeleteFindingsFilter", Kind: Delete, Resource: "findingsFilter", Lookup: "id"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Allow lists, custom data identifiers and findings filters are configuration metadata that need no prior EnableMacie call: no Amazon S3 bucket is inventoried or inspected, no classification job or automated discovery runs and no finding exists.",
		"Allow lists use regex criteria only and report status OK; s3WordsList is refused because it reads an Amazon S3 object. Custom data identifier expressions are stored without being compiled, tested or matched against any data.",
		"DeleteCustomDataIdentifier removes the identifier immediately instead of soft-deleting it, ignoreJobChecks is accepted because no classification job exists, and UpdateFindingsFilter accepts clientToken without replaying an earlier result.",
		"Classification jobs, findings, sample findings, bucket and usage statistics, the Macie session, publication, export and reveal configuration, sensitivity inspection templates, resource profiles, members, invitations, BatchGetCustomDataIdentifiers and TestCustomDataIdentifier return UnknownOperationException with HTTP 501.",
	},
}

// regexAllowList keeps allow lists whose criteria the request itself
// carries: an S3 words list would make Macie read an object in Amazon S3.
func regexAllowList(input map[string]any) error {
	criteria, _ := input["criteria"].(map[string]any)
	if _, ok := criteria["s3WordsList"]; ok {
		return Failure("ValidationException", "Allow lists that read an Amazon S3 words list are not supported by the simulator; use regex criteria.")
	}
	if _, ok := criteria["regex"]; !ok {
		return Failure("ValidationException", "Allow list criteria need a regex.")
	}
	return nil
}

// customDataIdentifier applies the documented size rules of custom data
// identifiers that the official shapes do not constrain.
func customDataIdentifier(input map[string]any) error {
	if regex, _ := input["regex"].(string); utf8.RuneCountInString(regex) > 512 {
		return Failure("ValidationException", "A custom data identifier regex can contain at most 512 characters.")
	}
	if name, _ := input["name"].(string); utf8.RuneCountInString(name) > 128 {
		return Failure("ValidationException", "A custom data identifier name can contain at most 128 characters.")
	}
	if description, _ := input["description"].(string); utf8.RuneCountInString(description) > 512 {
		return Failure("ValidationException", "A custom data identifier description can contain at most 512 characters.")
	}
	if !wordList(input["keywords"], 50, 3) {
		return Failure("ValidationException", "keywords holds at most 50 entries of 3-90 characters each.")
	}
	if !wordList(input["ignoreWords"], 10, 4) {
		return Failure("ValidationException", "ignoreWords holds at most 10 entries of 4-90 characters each.")
	}
	if distance, ok := input["maximumMatchDistance"].(int64); ok && (distance < 1 || distance > 300) {
		return Failure("ValidationException", "maximumMatchDistance must be 1-300 characters.")
	}
	return nil
}

// wordList reports whether an optional list has at most max words of
// minimum-90 characters each.
func wordList(value any, max, minimum int) bool {
	words, _ := value.([]any)
	if len(words) > max {
		return false
	}
	for _, word := range words {
		if n := utf8.RuneCountInString(word.(string)); n < minimum || n > 90 {
			return false
		}
	}
	return true
}

// macieFindingsFilter applies the documented name and description lengths
// of findings filters.
func macieFindingsFilter(input map[string]any) error {
	if name, ok := input["name"].(string); ok && (utf8.RuneCountInString(name) < 3 || utf8.RuneCountInString(name) > 64) {
		return Failure("ValidationException", "A findings filter name must contain 3-64 characters.")
	}
	if description, _ := input["description"].(string); utf8.RuneCountInString(description) > 512 {
		return Failure("ValidationException", "A findings filter description can contain at most 512 characters.")
	}
	return nil
}

var paymentCryptography = &Service{
	Model: "payment-cryptography", Title: "AWS Payment Cryptography", IAMPrefix: "payment-cryptography", Issues: []int{264},
	Resources: []*Resource{
		{Kind: "alias", Shape: "Alias", ID: "name", ARN: "arn:{partition}:payment-cryptography:{region}:{account}:{name}",
			NameMember: "AliasName", Unique: true, Limit: 100},
	},
	Ops: []Op{
		{Name: "CreateAlias", Kind: Create, Resource: "alias", Check: noPaymentKey},
		{Name: "GetAlias", Kind: Read, Resource: "alias", Lookup: "AliasName"},
		{Name: "ListAliases", Kind: List, Resource: "alias", Check: noPaymentKey},
		{Name: "UpdateAlias", Kind: Update, Resource: "alias", Lookup: "AliasName", Check: noPaymentKey},
		{Name: "DeleteAlias", Kind: Delete, Resource: "alias", Lookup: "AliasName"},
	},
	Bounds: []string{
		"Aliases are configuration metadata that never name a key: creating, importing or exporting a key generates or carries key material, so CreateKey, ImportKey, ExportKey, GetParametersForImport, GetParametersForExport, certificates, key usage, replication and every other key operation return UnknownOperationException with HTTP 501.",
		"KeyArn is refused with ResourceNotFoundException on CreateAlias, UpdateAlias and ListAliases because no key exists; UpdateAlias without KeyArn keeps the alias unassociated.",
		"Tagging applies to keys only and returns UnknownOperationException with HTTP 501; the separate data-plane service (encryption, PIN, MAC and card data operations) is not served.",
	},
}

// noPaymentKey reports every key ARN as missing: the simulator never
// creates payment keys because doing so would generate key material.
func noPaymentKey(input map[string]any) error {
	if _, ok := input["KeyArn"]; ok {
		return Failure("ResourceNotFoundException", "The simulator stores no AWS Payment Cryptography keys, so no key can be associated with an alias.")
	}
	return nil
}

var securityIncidentResponse = &Service{
	Model: "security-ir", Title: "AWS Security Incident Response", IAMPrefix: "security-ir", Issues: []int{269},
	Resources: []*Resource{
		{Kind: "membership", Shape: "GetMembershipResponse", Summary: "ListMembershipItem", ID: "alnum:16:m-",
			ARN:       "arn:{partition}:security-ir:{region}:{account}:membership/{id}",
			ARNMember: "membershipArn", IDMember: "membershipId", NameMember: "membershipName", Limit: 1,
			Generate: map[string]any{"accountId": "$account", "region": "$region", "membershipStatus": "Active", "membershipActivationTimestamp": "$now",
				"customerType": "Standalone", "numberOfAccountsCovered": 1}},
	},
	Ops: []Op{
		{Name: "CreateMembership", Kind: Create, Resource: "membership", Accept: []string{"coverEntireOrganization"}, Check: standaloneMembership},
		{Name: "GetMembership", Kind: Read, Resource: "membership", Lookup: "membershipId"},
		{Name: "ListMemberships", Kind: List, Resource: "membership"},
		{Name: "UpdateMembership", Kind: Update, Resource: "membership", Lookup: "membershipId", Check: standaloneMembership,
			Fields: []string{"membershipName", "incidentResponseTeam", "optInFeatures"}},
		{Name: "CancelMembership", Kind: Update, Resource: "membership", Lookup: "membershipId",
			Set: map[string]any{"membershipStatus": "Cancelled", "membershipDeactivationTimestamp": "$now"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Memberships are configuration metadata: no subscription or billing starts, no AWS Customer Incident Response Team or responder is engaged, and no email or other notification is sent to the incident response team.",
		"Each Region holds one standalone membership that covers the simulator account only; coverEntireOrganization true, organizational units and enabling the Triage opt-in feature (which monitors findings) are refused.",
		"CancelMembership keeps the membership readable with status Cancelled and it cannot be undone; memberships are never deleted, so a cancelled membership still counts toward the one-membership limit.",
		"Cases, case comments, attachments, edits, investigations, feedback and BatchGetMemberAccountDetails return UnknownOperationException with HTTP 501.",
	},
}

// standaloneMembership refuses organization-wide coverage and the Triage
// feature, which would monitor findings across accounts.
func standaloneMembership(input map[string]any) error {
	if cover, _ := input["coverEntireOrganization"].(bool); cover {
		return Failure("ValidationException", "The simulator account is not in an organization, so a membership cannot cover an entire organization.")
	}
	features, _ := input["optInFeatures"].([]any)
	for _, feature := range features {
		if enabled, _ := feature.(map[string]any)["isEnabled"].(bool); enabled {
			return Failure("ValidationException", "The Triage feature monitors findings, which the simulator does not do; set isEnabled to false.")
		}
	}
	return nil
}

var securityLake = &Service{
	Model: "securitylake", Title: "Amazon Security Lake", IAMPrefix: "securitylake", Issues: []int{270},
	Resources: []*Resource{
		{Kind: "exceptionSubscription", Shape: "GetDataLakeExceptionSubscriptionResponse", Singleton: true, ID: "name", Unique: true,
			ARN: "arn:{partition}:securitylake:{region}:{account}:data-lake/default", NameMember: "notificationEndpoint"},
	},
	Ops: []Op{
		{Name: "CreateDataLakeExceptionSubscription", Kind: Create, Resource: "exceptionSubscription"},
		{Name: "GetDataLakeExceptionSubscription", Kind: Read, Resource: "exceptionSubscription"},
		{Name: "UpdateDataLakeExceptionSubscription", Kind: Update, Resource: "exceptionSubscription",
			Fields: []string{"exceptionTimeToLive", "notificationEndpoint", "subscriptionProtocol"}},
		{Name: "DeleteDataLakeExceptionSubscription", Kind: Delete, Resource: "exceptionSubscription"},
	},
	Bounds: []string{
		"Only the data lake exception subscription is served, as configuration metadata that needs no data lake: no AWS or custom log source is ingested, so no exception occurs and no notification is ever sent to the endpoint.",
		"GetDataLakeExceptionSubscription returns no members before a subscription exists, UpdateDataLakeExceptionSubscription stores a subscription even when none exists, and a second CreateDataLakeExceptionSubscription is refused with ConflictException.",
		"Data lakes, log sources, subscribers and subscriber notifications, organization configuration, delegated administrators, data lake exceptions and tagging return UnknownOperationException with HTTP 501.",
	},
}

var firewallManager = &Service{
	Model: "fms", Title: "AWS Firewall Manager", IAMPrefix: "fms", Issues: []int{259},
	Resources: []*Resource{
		{Kind: "appsList", Shape: "AppsListData", Summary: "AppsListDataSummary", ID: "uuid",
			ARN:       "arn:{partition}:fms:{region}:{account}:applications-list/{id}",
			ARNMember: "AppsListArn", IDMember: "ListId", NameMember: "ListName", Limit: 100,
			Generate: map[string]any{"ListArn": "$arn", "CreateTime": "$now", "LastUpdateTime": "$now", "DefaultList": false}},
		{Kind: "protocolsList", Shape: "ProtocolsListData", Summary: "ProtocolsListDataSummary", ID: "uuid",
			ARN:       "arn:{partition}:fms:{region}:{account}:protocols-list/{id}",
			ARNMember: "ProtocolsListArn", IDMember: "ListId", NameMember: "ListName", Limit: 100,
			Generate: map[string]any{"ListArn": "$arn", "CreateTime": "$now", "LastUpdateTime": "$now", "DefaultList": false}},
		{Kind: "resourceSet", Shape: "ResourceSet", Summary: "ResourceSetSummary", ID: "alnum:22",
			ARN:       "arn:{partition}:fms:{region}:{account}:resource-set/{id}",
			ARNMember: "ResourceSetArn", IDMember: "Id", NameMember: "Name", Limit: 100,
			Generate: map[string]any{"LastUpdateTime": "$now", "ResourceSetStatus": "ACTIVE"}},
	},
	Ops: []Op{
		{Name: "PutAppsList", Kind: Create, Resource: "appsList", From: "AppsList", Check: newFirewallResource("AppsList", "ListId", "ListUpdateToken", "PreviousAppsList")},
		{Name: "GetAppsList", Kind: Read, Resource: "appsList", Lookup: "ListId", Accept: []string{"DefaultList"}, Check: noDefaultList},
		{Name: "ListAppsLists", Kind: List, Resource: "appsList", Filters: map[string]string{"DefaultLists": "DefaultList"}},
		{Name: "DeleteAppsList", Kind: Delete, Resource: "appsList", Lookup: "ListId"},
		{Name: "PutProtocolsList", Kind: Create, Resource: "protocolsList", From: "ProtocolsList", Check: newFirewallResource("ProtocolsList", "ListId", "ListUpdateToken", "PreviousProtocolsList")},
		{Name: "GetProtocolsList", Kind: Read, Resource: "protocolsList", Lookup: "ListId", Accept: []string{"DefaultList"}, Check: noDefaultList},
		{Name: "ListProtocolsLists", Kind: List, Resource: "protocolsList", Filters: map[string]string{"DefaultLists": "DefaultList"}},
		{Name: "DeleteProtocolsList", Kind: Delete, Resource: "protocolsList", Lookup: "ListId"},
		{Name: "PutResourceSet", Kind: Create, Resource: "resourceSet", From: "ResourceSet", Check: newFirewallResource("ResourceSet", "Id", "UpdateToken", "ResourceSetStatus")},
		{Name: "GetResourceSet", Kind: Read, Resource: "resourceSet", Lookup: "Identifier"},
		{Name: "ListResourceSets", Kind: List, Resource: "resourceSet"},
		{Name: "DeleteResourceSet", Kind: Delete, Resource: "resourceSet", Lookup: "Identifier"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Applications lists, protocols lists and resource sets are configuration metadata that need no Firewall Manager administrator account or AWS Organizations: no policy, AWS WAF rule, security group, network firewall or DNS firewall is created or applied to any account.",
		"PutAppsList, PutProtocolsList and PutResourceSet only create new resources: ListId, Id, update tokens, previous list versions and ResourceSetStatus are refused, so delete and recreate a list or resource set to change it.",
		"Managed default lists are not served: ListAppsLists and ListProtocolsLists with DefaultLists true return no lists, and GetAppsList or GetProtocolsList with DefaultList true reports ResourceNotFoundException.",
		"Policies, compliance, violations, protection status, notification channels, administrator accounts and scopes, resource set associations, discovered resources and third-party firewalls return UnknownOperationException with HTTP 501.",
	},
}

// newFirewallResource refuses the members that update an existing list or
// resource set: the bounded Put operations only create.
func newFirewallResource(member string, refused ...string) func(map[string]any) error {
	return func(input map[string]any) error {
		data, _ := input[member].(map[string]any)
		for _, key := range refused {
			if _, ok := data[key]; ok {
				return Failure("InvalidInputException", key+" is not supported: the simulator only creates new Firewall Manager lists and resource sets.")
			}
		}
		return nil
	}
}

// noDefaultList reports managed default lists as missing; the simulator
// serves only lists created in the simulator account.
func noDefaultList(input map[string]any) error {
	if defaultList, _ := input["DefaultList"].(bool); defaultList {
		return Failure("ResourceNotFoundException", "Firewall Manager managed default lists are not served by the simulator.")
	}
	return nil
}

var identityCenter = &Service{
	Model: "sso-admin", Title: "AWS IAM Identity Center", IAMPrefix: "sso", Issues: []int{260},
	Resources: []*Resource{
		{Kind: "instance", Shape: "DescribeInstanceResponse", Summary: "InstanceMetadata", ID: "hex:16:ssoins-",
			ARN: "arn:{partition}:sso:::instance/{id}", ARNMember: "InstanceArn", NameMember: "Name", Limit: 1, Cascade: true,
			Generate: map[string]any{"OwnerAccountId": "$account", "CreatedDate": "$now", "Status": "ACTIVE"}},
		{Kind: "permissionSet", Shape: "PermissionSet", Parent: "instance", ID: "hex:16:ps-",
			ARN:       "arn:{partition}:sso:::permissionSet/{parent}/{id}",
			ARNMember: "PermissionSetArn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedDate": "$now", "SessionDuration": "PT1H"}},
	},
	Ops: []Op{
		{Name: "CreateInstance", Kind: Create, Resource: "instance"},
		{Name: "DescribeInstance", Kind: Read, Resource: "instance", Lookup: "InstanceArn"},
		{Name: "ListInstances", Kind: List, Resource: "instance"},
		{Name: "UpdateInstance", Kind: Update, Resource: "instance", Lookup: "InstanceArn", Fields: []string{"Name"}},
		{Name: "DeleteInstance", Kind: Delete, Resource: "instance", Lookup: "InstanceArn"},
		{Name: "CreatePermissionSet", Kind: Create, Resource: "permissionSet", Parent: "InstanceArn", Check: sessionDuration},
		{Name: "DescribePermissionSet", Kind: Read, Resource: "permissionSet", Parent: "InstanceArn", Lookup: "PermissionSetArn"},
		{Name: "ListPermissionSets", Kind: List, Resource: "permissionSet", Parent: "InstanceArn", ListValue: "PermissionSetArn"},
		{Name: "UpdatePermissionSet", Kind: Update, Resource: "permissionSet", Parent: "InstanceArn", Lookup: "PermissionSetArn", Check: sessionDuration,
			Fields: []string{"Description", "SessionDuration", "RelayState"}},
		{Name: "DeletePermissionSet", Kind: Delete, Resource: "permissionSet", Parent: "InstanceArn", Lookup: "PermissionSetArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Account instances and permission sets are configuration metadata: no identity store, user, group, AWS access portal, sign-in, token or session exists, so nobody can authenticate and IdentityStoreId is never returned.",
		"Each Region holds one ACTIVE account instance owned by the simulator account; organization instances and customer managed KMS encryption are not available, and deleting an instance deletes its permission sets.",
		"Permission sets carry a name, description, session duration (PT1H-PT12H, default PT1H) and relay state only: inline, AWS managed, customer managed and permissions boundary policies, provisioning and account assignments return UnknownOperationException with HTTP 501, so no IAM role is created in any account.",
		"Tagging operations refuse InstanceArn and NextToken; applications, application assignments, trusted token issuers and access control attributes return UnknownOperationException with HTTP 501.",
	},
}

// isoHours matches the ISO-8601 hour, minute and second durations that
// permission set sessions use.
var isoHours = regexp.MustCompile(`^PT(?:(\d{1,5})H)?(?:(\d{1,5})M)?(?:(\d{1,5})S)?$`)

// sessionDuration applies the documented one-to-twelve-hour bound on
// permission set sessions, which the official pattern cannot express.
func sessionDuration(input map[string]any) error {
	duration, ok := input["SessionDuration"].(string)
	if !ok {
		return nil
	}
	parts := isoHours.FindStringSubmatch(duration)
	seconds := 0
	for i, unit := range []int{3600, 60, 1} {
		if parts != nil && parts[i+1] != "" {
			n, _ := strconv.Atoi(parts[i+1])
			seconds += n * unit
		}
	}
	if seconds < 3600 || seconds > 12*3600 {
		return Failure("ValidationException", "SessionDuration must be an ISO-8601 duration from PT1H to PT12H.")
	}
	return nil
}
