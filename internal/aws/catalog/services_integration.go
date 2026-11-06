package catalog

import (
	"encoding/json"
	"strings"
)

// Reviewed bounded specifications for application-integration, messaging,
// workflow and blockchain services. Each binds only official operations whose
// effect is configuration metadata; delivery, sending, execution and external
// sign-up operations stay unbound and return HTTP 501.

var eventBridge = &Service{
	Model: "events", Title: "Amazon EventBridge", IAMPrefix: "events", Issues: []int{79},
	Resources: []*Resource{
		{Kind: "eventBus", Shape: "DescribeEventBusResponse", Summary: "EventBus", ID: "name",
			ARN:       "arn:{partition}:events:{region}:{account}:event-bus/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"Arn": "$arn", "EventBusArn": "$arn", "CreationTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "rule", Shape: "DescribeRuleResponse", Summary: "Rule", Parent: "eventBus", ParentMember: "EventBusName", ID: "name",
			ARN:       "arn:{partition}:events:{region}:{account}:rule/{parentName}/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"Arn": "$arn", "RuleArn": "$arn", "EventBusName": "$parentName", "CreatedBy": "$account", "State": "ENABLED"}},
		{Kind: "archive", Shape: "DescribeArchiveResponse", Summary: "Archive", ID: "name",
			ARN:       "arn:{partition}:events:{region}:{account}:archive/{name}",
			ARNMember: "ArchiveArn", NameMember: "ArchiveName", Unique: true, Limit: 100,
			Generate: map[string]any{"State": "ENABLED", "CreationTime": "$now", "SizeBytes": 0, "EventCount": 0},
			Refs:     map[string]string{"EventSourceArn": "eventBus"}},
		{Kind: "connection", Shape: "DescribeConnectionResponse", Summary: "Connection", ID: "uuid",
			ARN:       "arn:{partition}:events:{region}:{account}:connection/{name}/{id}",
			ARNMember: "ConnectionArn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"ConnectionState": "AUTHORIZED", "CreationTime": "$now", "LastModifiedTime": "$now", "LastAuthorizedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateEventBus", Kind: Create, Resource: "eventBus", Check: newEventBus},
		{Name: "DescribeEventBus", Kind: Read, Resource: "eventBus", Lookup: "Name", Check: customEventBus("Name")},
		{Name: "ListEventBuses", Kind: List, Resource: "eventBus"},
		{Name: "UpdateEventBus", Kind: Update, Resource: "eventBus", Lookup: "Name", Check: customEventBus("Name"),
			Fields: []string{"KmsKeyIdentifier", "Description", "DeadLetterConfig", "LogConfig"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteEventBus", Kind: Delete, Resource: "eventBus", Lookup: "Name", Check: customEventBus("Name")},
		{Name: "PutRule", Kind: Put, Resource: "rule", Lookup: "Name", Parent: "EventBusName", Check: eventsPutRule},
		{Name: "DescribeRule", Kind: Read, Resource: "rule", Lookup: "Name", Parent: "EventBusName", Check: customEventBus("EventBusName")},
		{Name: "ListRules", Kind: List, Resource: "rule", Parent: "EventBusName", Check: customEventBus("EventBusName")},
		{Name: "EnableRule", Kind: Update, Resource: "rule", Lookup: "Name", Parent: "EventBusName", Check: customEventBus("EventBusName"), Set: map[string]any{"State": "ENABLED"}},
		{Name: "DisableRule", Kind: Update, Resource: "rule", Lookup: "Name", Parent: "EventBusName", Check: customEventBus("EventBusName"), Set: map[string]any{"State": "DISABLED"}},
		{Name: "DeleteRule", Kind: Delete, Resource: "rule", Lookup: "Name", Parent: "EventBusName", Check: customEventBus("EventBusName"), Accept: []string{"Force"}},
		{Name: "CreateArchive", Kind: Create, Resource: "archive", Check: eventsArchivePattern},
		{Name: "DescribeArchive", Kind: Read, Resource: "archive", Lookup: "ArchiveName"},
		{Name: "ListArchives", Kind: List, Resource: "archive", Filters: map[string]string{"EventSourceArn": "EventSourceArn", "State": "State"}},
		{Name: "UpdateArchive", Kind: Update, Resource: "archive", Lookup: "ArchiveName", Check: eventsArchivePattern,
			Fields: []string{"Description", "EventPattern", "RetentionDays", "KmsKeyIdentifier"}},
		{Name: "DeleteArchive", Kind: Delete, Resource: "archive", Lookup: "ArchiveName"},
		{Name: "CreateConnection", Kind: Create, Resource: "connection", Check: eventsCreateConnection},
		{Name: "DescribeConnection", Kind: Read, Resource: "connection", Lookup: "Name"},
		{Name: "ListConnections", Kind: List, Resource: "connection", Filters: map[string]string{"ConnectionState": "ConnectionState"}},
		{Name: "UpdateConnection", Kind: Update, Resource: "connection", Lookup: "Name", Check: eventsUpdateConnection,
			Fields: []string{"Description", "AuthorizationType", "AuthParameters", "KmsKeyIdentifier"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteConnection", Kind: Delete, Resource: "connection", Lookup: "Name", Set: map[string]any{"ConnectionState": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN", Check: eventsTaggable},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN", Check: eventsTaggable},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN", Check: eventsTaggable},
	},
	Bounds: []string{
		"Custom event buses, their event-pattern rules, archives and connections are stored as configuration metadata: no event is accepted, matched, archived, replayed or delivered, and PutEvents, PutTargets, StartReplay and the other delivery APIs return UnknownOperationException with HTTP 501.",
		"The account's default event bus and partner event buses are not modeled: rule operations require EventBusName to name a custom event bus, schedule expressions (which AWS supports only on the default bus) are refused, and every rule needs a JSON event pattern whose values are arrays or objects.",
		"PutRule on an existing rule replaces only the members it supplies; omitted members keep their stored values instead of being cleared.",
		"Connections support BASIC and API_KEY authorization and are reported AUTHORIZED without contacting any endpoint; OAuth client credentials, private connectivity and secret invocation parameters are refused. Passwords and API key values stay in the private simulator database, are never returned, and no Secrets Manager secret is created. UpdateConnection replaces AuthParameters as a whole, so it requires AuthorizationType and complete credentials.",
		"Event buses with rules cannot be deleted; archives keep their EventSourceArn when the bus is deleted. Only rules and event buses can be tagged. The Limit member of list operations without an official paginator and NamePrefix filters are refused.",
	},
}

// newEventBus refuses event bus names the simulator cannot own: the account
// default bus and partner event buses, whose names contain a slash.
func newEventBus(input map[string]any) error {
	name, _ := input["Name"].(string)
	if name == "default" {
		return Failure("ValidationException", "The default event bus is reserved and not modeled by the simulator.")
	}
	if strings.Contains(name, "/") {
		return Failure("ValidationException", "Partner event buses need partner event sources, which the simulator does not implement.")
	}
	return nil
}

// customEventBus requires the member to name a custom event bus; requests
// that would address the account default bus are refused explicitly.
func customEventBus(member string) func(map[string]any) error {
	return func(input map[string]any) error {
		name, ok := input[member].(string)
		if !ok || name == "default" || strings.HasSuffix(name, ":event-bus/default") {
			return Failure("ValidationException", member+" must name a custom event bus; the simulator does not model the default event bus.")
		}
		return nil
	}
}

// eventsPutRule applies the documented constraints of rules on custom event buses.
func eventsPutRule(input map[string]any) error {
	if err := customEventBus("EventBusName")(input); err != nil {
		return err
	}
	if _, ok := input["ScheduleExpression"]; ok {
		return Failure("ValidationException", "ScheduleExpression is supported only on the default event bus, which the simulator does not model.")
	}
	pattern, ok := input["EventPattern"].(string)
	if !ok {
		return Failure("ValidationException", "A rule on a custom event bus needs an EventPattern.")
	}
	return validEventPattern(pattern)
}

func eventsArchivePattern(input map[string]any) error {
	if pattern, ok := input["EventPattern"].(string); ok {
		return validEventPattern(pattern)
	}
	return nil
}

// validEventPattern checks the structural event-pattern rule: a non-empty
// JSON object whose values are non-empty arrays of matchers or nested objects.
func validEventPattern(text string) error {
	var pattern map[string]any
	if err := json.Unmarshal([]byte(text), &pattern); err != nil || !eventPatternObject(pattern) {
		return Failure("InvalidEventPatternException", "Event pattern is not valid: use a JSON object whose values are non-empty arrays or objects.")
	}
	return nil
}

func eventPatternObject(object map[string]any) bool {
	if len(object) == 0 {
		return false
	}
	for _, value := range object {
		switch v := value.(type) {
		case map[string]any:
			if !eventPatternObject(v) {
				return false
			}
		case []any:
			if len(v) == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// eventsConnectionScope refuses connection features that need an authorization
// endpoint call, private networking or secret storage beyond this database.
func eventsConnectionScope(input map[string]any) error {
	if input["AuthorizationType"] == "OAUTH_CLIENT_CREDENTIALS" {
		return Failure("ValidationException", "OAuth client-credential connections need an authorization endpoint call, which the simulator does not perform.")
	}
	if _, ok := input["InvocationConnectivityParameters"]; ok {
		return Failure("ValidationException", "Private connectivity through resource configurations is not supported by the simulator.")
	}
	auth, _ := input["AuthParameters"].(map[string]any)
	for _, member := range []string{"OAuthParameters", "ConnectivityParameters"} {
		if _, ok := auth[member]; ok {
			return Failure("ValidationException", "AuthParameters."+member+" is not supported by the simulator.")
		}
	}
	parameters, _ := auth["InvocationHttpParameters"].(map[string]any)
	for _, list := range []string{"HeaderParameters", "QueryStringParameters", "BodyParameters"} {
		items, _ := parameters[list].([]any)
		for _, item := range items {
			if parameter, _ := item.(map[string]any); parameter["IsValueSecret"] == true {
				return Failure("ValidationException", "Secret invocation parameters are not stored by the simulator.")
			}
		}
	}
	return nil
}

// eventsAuthBlock names the AuthParameters member and the complete credential
// members required for one supported authorization type.
func eventsAuthBlock(kind any) (string, []string) {
	if kind == "API_KEY" {
		return "ApiKeyAuthParameters", []string{"ApiKeyName", "ApiKeyValue"}
	}
	return "BasicAuthParameters", []string{"Username", "Password"}
}

// eventsConnectionCredentials requires exactly the credentials of the declared
// authorization type, complete, so that a stored block is never partial.
func eventsConnectionCredentials(input map[string]any) error {
	auth, _ := input["AuthParameters"].(map[string]any)
	member, required := eventsAuthBlock(input["AuthorizationType"])
	other := "ApiKeyAuthParameters"
	if member == other {
		other = "BasicAuthParameters"
	}
	if _, ok := auth[other]; ok {
		return Failure("ValidationException", "AuthParameters."+other+" does not match AuthorizationType.")
	}
	block, ok := auth[member].(map[string]any)
	if !ok {
		return Failure("ValidationException", "AuthParameters."+member+" is required for this AuthorizationType.")
	}
	for _, name := range required {
		if _, ok := block[name]; !ok {
			return Failure("ValidationException", "AuthParameters."+member+"."+name+" is required.")
		}
	}
	return nil
}

func eventsCreateConnection(input map[string]any) error {
	if err := eventsConnectionScope(input); err != nil {
		return err
	}
	return eventsConnectionCredentials(input)
}

// eventsUpdateConnection requires AuthorizationType and complete credentials
// whenever authorization changes, because the stored block is replaced whole.
func eventsUpdateConnection(input map[string]any) error {
	if err := eventsConnectionScope(input); err != nil {
		return err
	}
	_, auth := input["AuthParameters"]
	_, kind := input["AuthorizationType"]
	if !auth && !kind {
		return nil
	}
	if !auth || !kind {
		return Failure("ValidationException", "The simulator replaces authorization as a whole: send AuthorizationType with complete AuthParameters.")
	}
	return eventsConnectionCredentials(input)
}

// eventsTaggable limits tagging to the EventBridge resources that AWS lets
// callers tag: rules and event buses.
func eventsTaggable(input map[string]any) error {
	arn, _ := input["ResourceARN"].(string)
	if !strings.Contains(arn, ":rule/") && !strings.Contains(arn, ":event-bus/") {
		return Failure("ValidationException", "Only rules and event buses can be tagged.")
	}
	return nil
}

var mwaa = &Service{
	Model: "mwaa", Title: "Amazon Managed Workflows for Apache Airflow", IAMPrefix: "airflow", Issues: []int{81},
	Resources: []*Resource{
		{Kind: "environment", Shape: "Environment", ID: "name", ARN: "arn:{partition}:airflow:{region}:{account}:environment/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 10,
			Generate: map[string]any{"Status": "CREATING", "CreatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateEnvironment", Kind: Create, Resource: "environment", Check: airflowCapacity},
		{Name: "GetEnvironment", Kind: Read, Resource: "environment", Lookup: "Name"},
		{Name: "ListEnvironments", Kind: List, Resource: "environment", ListValue: "Name"},
		{Name: "UpdateEnvironment", Kind: Update, Resource: "environment", Lookup: "Name", Check: airflowCapacity,
			Fields: []string{"ExecutionRoleArn", "AirflowConfigurationOptions", "AirflowVersion", "DagS3Path", "EnvironmentClass", "LoggingConfiguration",
				"MaxWorkers", "MinWorkers", "MaxWebservers", "MinWebservers", "PluginsS3Path", "PluginsS3ObjectVersion", "RequirementsS3Path",
				"RequirementsS3ObjectVersion", "Schedulers", "SourceBucketArn", "StartupScriptS3Path", "StartupScriptS3ObjectVersion",
				"WebserverAccessMode", "WeeklyMaintenanceWindowStart"}},
		{Name: "DeleteEnvironment", Kind: Delete, Resource: "environment", Lookup: "Name"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Environments are configuration metadata and stay CREATING: no Airflow scheduler, worker, web server, VPC endpoint or log group is provisioned, and DAG, plugin, requirement and startup-script paths in S3 are not read.",
		"CreateCliToken, CreateWebLoginToken, InvokeRestApi and PublishMetrics return UnknownOperationException with HTTP 501; no WebserverUrl or service-linked role is assigned.",
		"UpdateEnvironment cannot change NetworkConfiguration or WorkerReplacementStrategy. SDK clients must disable host-prefix injection (botocore inject_host_prefix=False) because the official api. host prefix cannot address the loopback endpoint.",
	},
}

// airflowCapacity rejects inverted worker or web server ranges in one request.
func airflowCapacity(input map[string]any) error {
	for _, pair := range [][2]string{{"MinWorkers", "MaxWorkers"}, {"MinWebservers", "MaxWebservers"}} {
		low, lowOK := input[pair[0]].(int64)
		high, highOK := input[pair[1]].(int64)
		if lowOK && highOK && low > high {
			return Failure("ValidationException", pair[0]+" must not exceed "+pair[1]+".")
		}
	}
	return nil
}

var managedBlockchain = &Service{
	Model: "managedblockchain", Title: "Amazon Managed Blockchain", IAMPrefix: "managedblockchain", Issues: []int{85},
	Resources: []*Resource{
		{Kind: "accessor", Shape: "Accessor", Summary: "AccessorSummary", ID: "alnum:26:ac-",
			ARN:       "arn:{partition}:managedblockchain:{region}:{account}:accessors/{id}",
			ARNMember: "Arn", IDMember: "Id", Limit: 50, Aliases: map[string]string{"AccessorType": "Type"},
			Generate: map[string]any{"AccessorId": "$id", "Status": "AVAILABLE", "CreationDate": "$now", "BillingToken": "$fmt:tcsim-billing{id}"}},
	},
	Ops: []Op{
		{Name: "CreateAccessor", Kind: Create, Resource: "accessor"},
		{Name: "GetAccessor", Kind: Read, Resource: "accessor", Lookup: "AccessorId"},
		{Name: "ListAccessors", Kind: List, Resource: "accessor", Filters: map[string]string{"NetworkType": "NetworkType"}},
		{Name: "DeleteAccessor", Kind: Delete, Resource: "accessor", Lookup: "AccessorId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Only token-based accessors are stored. Their BillingToken is a local placeholder derived from the accessor ID that grants no access, and no Ethereum or Polygon node is reachable.",
		"Hyperledger Fabric networks, members, nodes, proposals and invitations are not implemented and return UnknownOperationException with HTTP 501. Deleted accessors are removed immediately instead of passing through PENDING_DELETION.",
	},
}

var appFabric = &Service{
	Model: "appfabric", Title: "AWS AppFabric", IAMPrefix: "appfabric", Issues: []int{88},
	Resources: []*Resource{
		{Kind: "appBundle", Shape: "AppBundle", Summary: "AppBundleSummary", ID: "uuid",
			ARN:       "arn:{partition}:appfabric:{region}:{account}:appbundle/{id}",
			ARNMember: "arn", Limit: 10, Aliases: map[string]string{"customerManagedKeyIdentifier": "customerManagedKeyArn"}},
		{Kind: "ingestion", Shape: "Ingestion", Summary: "IngestionSummary", Parent: "appBundle", ParentMember: "appBundleArn", ID: "uuid",
			ARN:       "arn:{partition}:appfabric:{region}:{account}:appbundle/{parent}/ingestion/{id}",
			ARNMember: "arn", Limit: 50,
			Generate: map[string]any{"appBundleArn": "$parentArn", "state": "disabled", "createdAt": "$now", "updatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateAppBundle", Kind: Create, Resource: "appBundle", Check: appBundleKey},
		{Name: "GetAppBundle", Kind: Read, Resource: "appBundle", Lookup: "appBundleIdentifier"},
		{Name: "ListAppBundles", Kind: List, Resource: "appBundle"},
		{Name: "DeleteAppBundle", Kind: Delete, Resource: "appBundle", Lookup: "appBundleIdentifier"},
		{Name: "CreateIngestion", Kind: Create, Resource: "ingestion", Parent: "appBundleIdentifier"},
		{Name: "GetIngestion", Kind: Read, Resource: "ingestion", Parent: "appBundleIdentifier", Lookup: "ingestionIdentifier"},
		{Name: "ListIngestions", Kind: List, Resource: "ingestion", Parent: "appBundleIdentifier"},
		{Name: "DeleteIngestion", Kind: Delete, Resource: "ingestion", Parent: "appBundleIdentifier", Lookup: "ingestionIdentifier"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"App bundles and audit-log ingestions are configuration metadata: ingestions stay disabled because StartIngestion, StopIngestion and data collection are not implemented, and no SaaS application is contacted.",
		"App authorizations (OAuth and API-key credentials), ingestion destinations and user-access tasks return UnknownOperationException with HTTP 501. App bundles with ingestions cannot be deleted, and customer managed keys must be given as key ARNs; no encryption is performed.",
	},
}

// appBundleKey requires a key ARN because the stored member is the key ARN
// and the simulator does not resolve KMS key IDs for another service.
func appBundleKey(input map[string]any) error {
	if key, ok := input["customerManagedKeyIdentifier"].(string); ok && !strings.HasPrefix(key, "arn:") {
		return Failure("ValidationException", "Specify the customer managed key as a key ARN.")
	}
	return nil
}

var pinpoint = &Service{
	Model: "pinpoint", Title: "Amazon Pinpoint", IAMPrefix: "mobiletargeting", Issues: []int{97, 94},
	Resources: []*Resource{
		{Kind: "app", Shape: "ApplicationResponse", ID: "hex:32", ARN: "arn:{partition}:mobiletargeting:{region}:{account}:apps/{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 100,
			Generate: map[string]any{"CreationDate": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateApp", Kind: Create, Resource: "app", From: "CreateApplicationRequest", Check: pinpointUntagged},
		{Name: "GetApp", Kind: Read, Resource: "app", Lookup: "ApplicationId"},
		{Name: "DeleteApp", Kind: Delete, Resource: "app", Lookup: "ApplicationId"},
	},
	Bounds: []string{
		"Only projects (apps) are stored: no message, campaign, journey, segment, endpoint, template or event is sent, scheduled or recorded, and SendMessages and the other sending APIs return UnknownOperationException with HTTP 501.",
		"Push, email, SMS and voice channels are not implemented, so no APNs, FCM, Baidu or ADM credential is accepted. GetApps and the tagging APIs are not implemented, and CreateApp refuses tags.",
	},
}

// pinpointUntagged refuses creation tags: the tagging APIs, which use a
// wrapped TagsModel payload, are not implemented, so tags could not be read.
func pinpointUntagged(input map[string]any) error {
	if request, _ := input["CreateApplicationRequest"].(map[string]any); request["tags"] != nil {
		return Failure("BadRequestException", "Project tags are not supported by the simulator.")
	}
	return nil
}

var smsVoice = &Service{
	Model: "pinpoint-sms-voice-v2", Title: "AWS End User Messaging SMS", IAMPrefix: "sms-voice", Issues: []int{95},
	Resources: []*Resource{
		{Kind: "configurationSet", Shape: "ConfigurationSetInformation", ID: "name",
			ARN:       "arn:{partition}:sms-voice:{region}:{account}:configuration-set/{name}",
			ARNMember: "ConfigurationSetArn", NameMember: "ConfigurationSetName", Unique: true, Limit: 25,
			Generate: map[string]any{"CreatedTimestamp": "$now", "EventDestinations": []any{}, "DefaultMessageFeedbackEnabled": false}},
		{Kind: "optOutList", Shape: "OptOutListInformation", ID: "name",
			ARN:       "arn:{partition}:sms-voice:{region}:{account}:opt-out-list/{name}",
			ARNMember: "OptOutListArn", NameMember: "OptOutListName", Unique: true, Limit: 25,
			Generate: map[string]any{"CreatedTimestamp": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateConfigurationSet", Kind: Create, Resource: "configurationSet"},
		{Name: "DescribeConfigurationSets", Kind: List, Resource: "configurationSet", Names: "ConfigurationSetNames"},
		{Name: "DeleteConfigurationSet", Kind: Delete, Resource: "configurationSet", Lookup: "ConfigurationSetName"},
		{Name: "CreateOptOutList", Kind: Create, Resource: "optOutList", Check: smsOptOutListName},
		{Name: "DescribeOptOutLists", Kind: List, Resource: "optOutList", Names: "OptOutListNames"},
		{Name: "DeleteOptOutList", Kind: Delete, Resource: "optOutList", Lookup: "OptOutListName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Configuration sets and opt-out lists are configuration metadata: no text, voice or media message is sent, no phone number, sender ID, pool or registration is requested, and SendTextMessage and the other sending APIs return UnknownOperationException with HTTP 501.",
		"Event destinations, default sender settings, opted-out numbers, protect configurations and spend limits are not implemented. The account's Default opt-out list is not pre-created and its name is reserved. Describe filters and the Owner member are refused.",
	},
}

// smsOptOutListName reserves the name of the account's default opt-out list,
// which AWS creates itself and the simulator does not model.
func smsOptOutListName(input map[string]any) error {
	if input["OptOutListName"] == "Default" {
		return Failure("ValidationException", "The Default opt-out list is reserved and not modeled by the simulator.")
	}
	return nil
}

var sesV2 = &Service{
	Model: "sesv2", Title: "Amazon Simple Email Service", IAMPrefix: "ses", Issues: []int{98},
	Resources: []*Resource{
		{Kind: "configurationSet", Shape: "GetConfigurationSetResponse", ID: "name",
			ARN:        "arn:{partition}:ses:{region}:{account}:configuration-set/{name}",
			NameMember: "ConfigurationSetName", Unique: true, Limit: 100},
		{Kind: "contactList", Shape: "GetContactListResponse", Summary: "ContactList", ID: "name",
			ARN:        "arn:{partition}:ses:{region}:{account}:contact-list/{name}",
			NameMember: "ContactListName", Unique: true, Limit: 1,
			Generate: map[string]any{"CreatedTimestamp": "$now", "LastUpdatedTimestamp": "$now"}},
		{Kind: "emailTemplate", Shape: "GetEmailTemplateResponse", Summary: "EmailTemplateMetadata", ID: "name",
			ARN:        "arn:{partition}:ses:{region}:{account}:template/{name}",
			NameMember: "TemplateName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedTimestamp": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateConfigurationSet", Kind: Create, Resource: "configurationSet"},
		{Name: "GetConfigurationSet", Kind: Read, Resource: "configurationSet", Lookup: "ConfigurationSetName"},
		{Name: "ListConfigurationSets", Kind: List, Resource: "configurationSet", ListValue: "ConfigurationSetName"},
		{Name: "DeleteConfigurationSet", Kind: Delete, Resource: "configurationSet", Lookup: "ConfigurationSetName"},
		{Name: "CreateContactList", Kind: Create, Resource: "contactList"},
		{Name: "GetContactList", Kind: Read, Resource: "contactList", Lookup: "ContactListName"},
		{Name: "ListContactLists", Kind: List, Resource: "contactList"},
		{Name: "UpdateContactList", Kind: Update, Resource: "contactList", Lookup: "ContactListName",
			Fields: []string{"Topics", "Description"}, Touch: []string{"LastUpdatedTimestamp"}},
		{Name: "DeleteContactList", Kind: Delete, Resource: "contactList", Lookup: "ContactListName"},
		{Name: "CreateEmailTemplate", Kind: Create, Resource: "emailTemplate"},
		{Name: "GetEmailTemplate", Kind: Read, Resource: "emailTemplate", Lookup: "TemplateName"},
		{Name: "ListEmailTemplates", Kind: List, Resource: "emailTemplate"},
		{Name: "UpdateEmailTemplate", Kind: Update, Resource: "emailTemplate", Lookup: "TemplateName", Fields: []string{"TemplateContent"}},
		{Name: "DeleteEmailTemplate", Kind: Delete, Resource: "emailTemplate", Lookup: "TemplateName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Configuration sets, one contact list and email templates are stored as configuration metadata: no email is sent, rendered, tracked or archived, and SendEmail, SendBulkEmail and the other sending APIs return UnknownOperationException with HTTP 501.",
		"Email identities, DKIM, dedicated IPs, contacts, suppression lists, event destinations, the Put*Options operations and account settings are not implemented. Configuration sets keep the options given at creation, and the PageSize member is refused.",
	},
}
