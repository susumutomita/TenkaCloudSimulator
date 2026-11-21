package catalog

// Business applications: AWS B2B Data Interchange, Amazon Q Business, Amazon
// Connect, Amazon Chime SDK, Amazon WorkMail and AWS re:Post Private. Every
// bound resource is a stored configuration record; nothing that exchanges,
// answers, calls, sends or hosts is bound.

// b2bi serves B2B Data Interchange profiles, capabilities, transformers and
// partnerships as stored records. No EDI document is parsed or transformed.
var b2bi = &Service{
	Model: "b2bi", Title: "AWS B2B Data Interchange", IAMPrefix: "b2bi", Issues: []int{78},
	Resources: []*Resource{
		{Kind: "profile", Shape: "GetProfileResponse", Summary: "ProfileSummary", ID: "alnum:25:p-",
			ARN: "arn:{partition}:b2bi:{region}:{account}:profiles/{id}", ARNMember: "profileArn", IDMember: "profileId", Limit: 5,
			Generate: map[string]any{"createdAt": "$now"}},
		{Kind: "capability", Shape: "GetCapabilityResponse", Summary: "CapabilitySummary", ID: "alnum:25:ca-",
			ARN: "arn:{partition}:b2bi:{region}:{account}:capabilities/{id}", ARNMember: "capabilityArn", IDMember: "capabilityId", Limit: 100,
			Generate: map[string]any{"createdAt": "$now"}},
		{Kind: "transformer", Shape: "GetTransformerResponse", Summary: "TransformerSummary", ID: "alnum:25:tr-",
			ARN: "arn:{partition}:b2bi:{region}:{account}:transformers/{id}", ARNMember: "transformerArn", IDMember: "transformerId", Limit: 100,
			Generate: map[string]any{"createdAt": "$now", "status": "inactive"}},
		{Kind: "partnership", Shape: "GetPartnershipResponse", Summary: "PartnershipSummary", ID: "alnum:25:ps-",
			ARN: "arn:{partition}:b2bi:{region}:{account}:partnerships/{id}", ARNMember: "partnershipArn", IDMember: "partnershipId", Limit: 100,
			Generate: map[string]any{"createdAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateProfile", Kind: Create, Resource: "profile"},
		{Name: "GetProfile", Kind: Read, Resource: "profile", Lookup: "profileId"},
		{Name: "ListProfiles", Kind: List, Resource: "profile"},
		{Name: "UpdateProfile", Kind: Update, Resource: "profile", Lookup: "profileId", Touch: []string{"modifiedAt"},
			Fields: []string{"name", "email", "phone", "businessName"}},
		{Name: "DeleteProfile", Kind: Delete, Resource: "profile", Lookup: "profileId"},
		{Name: "CreateCapability", Kind: Create, Resource: "capability"},
		{Name: "GetCapability", Kind: Read, Resource: "capability", Lookup: "capabilityId"},
		{Name: "ListCapabilities", Kind: List, Resource: "capability"},
		{Name: "UpdateCapability", Kind: Update, Resource: "capability", Lookup: "capabilityId", Touch: []string{"modifiedAt"},
			Fields: []string{"name", "configuration", "instructionsDocuments"}},
		{Name: "DeleteCapability", Kind: Delete, Resource: "capability", Lookup: "capabilityId"},
		{Name: "CreateTransformer", Kind: Create, Resource: "transformer"},
		{Name: "GetTransformer", Kind: Read, Resource: "transformer", Lookup: "transformerId"},
		{Name: "ListTransformers", Kind: List, Resource: "transformer"},
		{Name: "UpdateTransformer", Kind: Update, Resource: "transformer", Lookup: "transformerId", Touch: []string{"modifiedAt"},
			Fields: []string{"name", "status", "fileFormat", "mappingTemplate", "ediType", "sampleDocument", "inputConversion", "mapping",
				"outputConversion", "sampleDocuments"}},
		{Name: "DeleteTransformer", Kind: Delete, Resource: "transformer", Lookup: "transformerId"},
		{Name: "CreatePartnership", Kind: Create, Resource: "partnership"},
		{Name: "GetPartnership", Kind: Read, Resource: "partnership", Lookup: "partnershipId"},
		{Name: "ListPartnerships", Kind: List, Resource: "partnership", Filters: map[string]string{"profileId": "profileId"}},
		{Name: "UpdatePartnership", Kind: Update, Resource: "partnership", Lookup: "partnershipId", Touch: []string{"modifiedAt"},
			Fields: []string{"name", "capabilities", "capabilityOptions"}},
		{Name: "DeletePartnership", Kind: Delete, Resource: "partnership", Lookup: "partnershipId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Profiles, capabilities, transformers and partnerships are stored records: no EDI file is received, parsed, converted or delivered, no mapping template (JSONata or XSLT) is evaluated, and StartTransformerJob, GetTransformerJob, TestMapping, TestParsing, TestConversion, GenerateMapping and CreateStarterMappingTemplate return UnknownOperationException with HTTP 501.",
		"Profiles are limited to five per Region as documented; the other limits of 100 per Region are simulator limits. A profile never creates a log group, so logGroupName is not returned and logging only records ENABLED or DISABLED. Updating a profile cannot change logging, as the official UpdateProfile has no such member.",
		"Transformers are created with status inactive and UpdateTransformer may set active or inactive; neither changes behavior because nothing runs. Mapping templates, sample documents and the S3 locations and instruction documents of capabilities are stored as given: no bucket or key is read or checked.",
		"A partnership stores the profileId and capability IDs it was given without checking that the profile or capabilities exist, and deleting a profile, capability or transformer does not check for partnerships or capabilities that mention it. The official name patterns, lengths and enumerations are validated. clientToken is accepted and replays the original resource for identical input.",
		"Tags apply to the four resource kinds by ARN (arn:aws:b2bi:region:account:profiles/id, capabilities/id, transformers/id, partnerships/id).",
	},
}

// qBusiness serves Amazon Q Business applications, indexes, retrievers as
// stored records. No document is indexed,
// no question is answered and no web experience exists.
var qBusiness = &Service{
	Model: "qbusiness", Title: "Amazon Q Business", IAMPrefix: "qbusiness", Issues: []int{84},
	Resources: []*Resource{
		{Kind: "application", Shape: "GetApplicationResponse", Summary: "Application", ID: "uuid", Limit: 20,
			ARN: "arn:{partition}:qbusiness:{region}:{account}:application/{id}", ARNMember: "applicationArn", IDMember: "applicationId", Cascade: true,
			Generate: map[string]any{"createdAt": "$now", "updatedAt": "$now", "status": "CREATING"}},
		{Kind: "index", Shape: "GetIndexResponse", Summary: "Index", ID: "uuid", Limit: 5, Parent: "application", ParentMember: "applicationId",
			ARN: "arn:{partition}:qbusiness:{region}:{account}:application/{parent}/index/{id}", ARNMember: "indexArn", IDMember: "indexId",
			Generate: map[string]any{"createdAt": "$now", "updatedAt": "$now", "status": "CREATING", "type": "ENTERPRISE"}},
		{Kind: "retriever", Shape: "GetRetrieverResponse", Summary: "Retriever", ID: "uuid", Limit: 5, Parent: "application", ParentMember: "applicationId",
			ARN: "arn:{partition}:qbusiness:{region}:{account}:application/{parent}/retriever/{id}", ARNMember: "retrieverArn", IDMember: "retrieverId",
			Generate: map[string]any{"createdAt": "$now", "updatedAt": "$now", "status": "ACTIVE"}},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application", Check: qBusinessApplication},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "applicationId"},
		{Name: "ListApplications", Kind: List, Resource: "application"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "applicationId", Touch: []string{"updatedAt"},
			Fields: []string{"displayName", "description", "roleArn", "attachmentsConfiguration", "qAppsConfiguration", "personalizationConfiguration", "autoSubscriptionConfiguration"}},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "applicationId"},
		{Name: "CreateIndex", Kind: Create, Resource: "index", Parent: "applicationId"},
		{Name: "GetIndex", Kind: Read, Resource: "index", Parent: "applicationId", Lookup: "indexId"},
		{Name: "ListIndices", Kind: List, Resource: "index", Parent: "applicationId"},
		{Name: "UpdateIndex", Kind: Update, Resource: "index", Parent: "applicationId", Lookup: "indexId", Touch: []string{"updatedAt"},
			Fields: []string{"displayName", "description", "capacityConfiguration", "documentAttributeConfigurations"}},
		{Name: "DeleteIndex", Kind: Delete, Resource: "index", Parent: "applicationId", Lookup: "indexId"},
		{Name: "CreateRetriever", Kind: Create, Resource: "retriever", Parent: "applicationId", Check: qBusinessRetriever},
		{Name: "GetRetriever", Kind: Read, Resource: "retriever", Parent: "applicationId", Lookup: "retrieverId"},
		{Name: "ListRetrievers", Kind: List, Resource: "retriever", Parent: "applicationId"},
		{Name: "UpdateRetriever", Kind: Update, Resource: "retriever", Parent: "applicationId", Lookup: "retrieverId", Touch: []string{"updatedAt"},
			Fields: []string{"configuration", "displayName", "roleArn"}},
		{Name: "DeleteRetriever", Kind: Delete, Resource: "retriever", Parent: "applicationId", Lookup: "retrieverId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceARN"},
	},
	Bounds: []string{
		"Applications, indexes and retrievers are stored records: no index is built, no document is stored or searched, no question is answered and no web experience exists. Documents, data sources and sync jobs, plugins, web experiences, subscriptions, users and groups, conversations, Chat and ChatSync, SearchRelevantContent, data accessors, chat response configurations, chat controls (a stored record per application, which this engine cannot attach to its application) and the access policy operations return UnknownOperationException with HTTP 501.",
		"Applications must use identityType ANONYMOUS, because the other identity types need an IAM Identity Center instance, an OIDC or SAML provider or Amazon QuickSight, none of which exists; identityType is required here although the official shape makes it optional, and identityCenterInstanceArn, iamIdentityProviderArn, clientIdsForOIDC and quickSightConfiguration are refused. Applications and indexes stay in status CREATING, as the other provisioned records of this catalog do, and a retriever is stored as ACTIVE because there is nothing to provision for it. No service-linked role is created when roleArn is omitted.",
		"Indexes default to type ENTERPRISE and store their capacity configuration without limiting anything; they report no index statistics. A retriever's type must match its configuration (NATIVE_INDEX needs nativeIndexConfiguration, KENDRA_INDEX needs kendraIndexConfiguration), and the index IDs, role ARNs, KMS key and S3 locations they name are stored without being resolved. Deleting an application deletes its indexes and retrievers, a cascade the official operation does not document. Resource counts per Region (20 applications, 5 indexes and 5 retrievers per application) are simulator limits.",
		"Tags apply to applications, indexes and retrievers by ARN (arn:aws:qbusiness:region:account:application/id, application/id/index/id and application/id/retriever/id). clientToken replays the original resource for identical input.",
	},
}

// qBusinessApplication keeps the application to the identity type that needs
// no identity provider.
func qBusinessApplication(input map[string]any) error {
	for _, member := range []string{"identityCenterInstanceArn", "iamIdentityProviderArn", "clientIdsForOIDC", "quickSightConfiguration"} {
		if _, ok := input[member]; ok {
			return Failure("ValidationException", member+" is not supported: only ANONYMOUS applications are served.")
		}
	}
	if input["identityType"] != "ANONYMOUS" {
		return Failure("ValidationException", "identityType must be ANONYMOUS: no identity provider exists in the simulator.")
	}
	return nil
}

// qBusinessRetriever pairs the retriever type with its configuration member,
// since the official configuration is a union.
func qBusinessRetriever(input map[string]any) error {
	configuration, _ := input["configuration"].(map[string]any)
	want := map[string]string{"NATIVE_INDEX": "nativeIndexConfiguration", "KENDRA_INDEX": "kendraIndexConfiguration"}[bizText(input["type"])]
	if _, ok := configuration[want]; want == "" || !ok {
		return Failure("ValidationException", "The retriever configuration must hold "+want+" for type "+bizText(input["type"])+".")
	}
	return nil
}

// bizText reads a string member, empty when absent or not a string.
func bizText(value any) string {
	text, _ := value.(string)
	return text
}

// connectChild declares one resource owned by an Amazon Connect instance. The
// official list summaries name their identity members Id and Arn, so both are
// stored beside the identity members of the full description.
func connectChild(kind, shape, summary, path string, limit int, idMember, arnMember, nameMember string, generate map[string]any) *Resource {
	values := map[string]any{"Id": "$id", "Arn": "$arn", "LastModifiedTime": "$now", "LastModifiedRegion": "$region"}
	for key, value := range generate {
		values[key] = value
	}
	return &Resource{Kind: kind, Shape: shape, Summary: summary, ID: "uuid", Unique: true, Limit: limit, Parent: "instance", ParentMember: "InstanceId",
		ARN: "arn:{partition}:connect:{region}:{account}:instance/{parent}/" + path + "/{id}", ARNMember: arnMember, IDMember: idMember, NameMember: nameMember, Generate: values}
}

// connectUpdate binds one partial update operation of a child resource.
func connectUpdate(name, kind, lookup string, fields ...string) Op {
	return Op{Name: name, Kind: Update, Resource: kind, Parent: "InstanceId", Lookup: lookup, Touch: []string{"LastModifiedTime"}, Fields: fields}
}

// connectConfigUpdate binds UpdateQuickConnectConfig with the same destination
// rule as CreateQuickConnect.
func connectConfigUpdate() Op {
	op := connectUpdate("UpdateQuickConnectConfig", "quickConnect", "QuickConnectId", "QuickConnectConfig")
	op.Check = connectQuickConnect
	return op
}

// connect serves Amazon Connect instances and the routing configuration they
// own (hours of operation, queues, quick connects and agent statuses) as
// stored records. No contact center runs: nothing is answered, routed,
// dialed or recorded.
var connect = &Service{
	Model: "connect", Title: "Amazon Connect", IAMPrefix: "connect", Issues: []int{86},
	Resources: []*Resource{
		{Kind: "instance", Shape: "Instance", Summary: "InstanceSummary", ID: "uuid", Unique: true, Limit: 5, Cascade: true,
			ARN: "arn:{partition}:connect:{region}:{account}:instance/{id}", ARNMember: "Arn", IDMember: "Id", NameMember: "InstanceAlias",
			Generate: map[string]any{"CreatedTime": "$now", "InstanceStatus": "CREATION_IN_PROGRESS"}},
		connectChild("hoursOfOperation", "HoursOfOperation", "HoursOfOperationSummary", "operating-hours", 100, "HoursOfOperationId", "HoursOfOperationArn", "Name", nil),
		connectChild("queue", "Queue", "QueueSummary", "queue", 100, "QueueId", "QueueArn", "Name", map[string]any{"Status": "ENABLED", "QueueType": "STANDARD"}),
		connectChild("quickConnect", "QuickConnect", "QuickConnectSummary", "transfer-destination", 100, "QuickConnectId", "QuickConnectARN", "Name",
			map[string]any{"QuickConnectType": "QUEUE"}),
		connectChild("agentStatus", "AgentStatus", "AgentStatusSummary", "agent-state", 50, "AgentStatusId", "AgentStatusARN", "Name",
			map[string]any{"Type": "CUSTOM", "State": "ENABLED"}),
	},
	Ops: []Op{
		{Name: "CreateInstance", Kind: Create, Resource: "instance", Check: connectInstance},
		{Name: "DescribeInstance", Kind: Read, Resource: "instance", Lookup: "InstanceId"},
		{Name: "ListInstances", Kind: List, Resource: "instance"},
		{Name: "DeleteInstance", Kind: Delete, Resource: "instance", Lookup: "InstanceId", Accept: []string{"ClientToken"}},
		{Name: "CreateHoursOfOperation", Kind: Create, Resource: "hoursOfOperation", Parent: "InstanceId"},
		{Name: "DescribeHoursOfOperation", Kind: Read, Resource: "hoursOfOperation", Parent: "InstanceId", Lookup: "HoursOfOperationId"},
		{Name: "ListHoursOfOperations", Kind: List, Resource: "hoursOfOperation", Parent: "InstanceId"},
		connectUpdate("UpdateHoursOfOperation", "hoursOfOperation", "HoursOfOperationId", "Name", "Description", "TimeZone", "Config"),
		{Name: "DeleteHoursOfOperation", Kind: Delete, Resource: "hoursOfOperation", Parent: "InstanceId", Lookup: "HoursOfOperationId"},
		{Name: "CreateQueue", Kind: Create, Resource: "queue", Parent: "InstanceId", Check: connectQueue},
		{Name: "DescribeQueue", Kind: Read, Resource: "queue", Parent: "InstanceId", Lookup: "QueueId"},
		{Name: "ListQueues", Kind: List, Resource: "queue", Parent: "InstanceId"},
		connectUpdate("UpdateQueueName", "queue", "QueueId", "Name", "Description"),
		connectUpdate("UpdateQueueMaxContacts", "queue", "QueueId", "MaxContacts"),
		connectUpdate("UpdateQueueHoursOfOperation", "queue", "QueueId", "HoursOfOperationId"),
		connectUpdate("UpdateQueueStatus", "queue", "QueueId", "Status"),
		{Name: "DeleteQueue", Kind: Delete, Resource: "queue", Parent: "InstanceId", Lookup: "QueueId"},
		{Name: "CreateQuickConnect", Kind: Create, Resource: "quickConnect", Parent: "InstanceId", Check: connectQuickConnect},
		{Name: "DescribeQuickConnect", Kind: Read, Resource: "quickConnect", Parent: "InstanceId", Lookup: "QuickConnectId"},
		{Name: "ListQuickConnects", Kind: List, Resource: "quickConnect", Parent: "InstanceId"},
		connectUpdate("UpdateQuickConnectName", "quickConnect", "QuickConnectId", "Name", "Description"),
		connectConfigUpdate(),
		{Name: "DeleteQuickConnect", Kind: Delete, Resource: "quickConnect", Parent: "InstanceId", Lookup: "QuickConnectId"},
		{Name: "CreateAgentStatus", Kind: Create, Resource: "agentStatus", Parent: "InstanceId"},
		{Name: "DescribeAgentStatus", Kind: Read, Resource: "agentStatus", Parent: "InstanceId", Lookup: "AgentStatusId"},
		{Name: "ListAgentStatuses", Kind: List, Resource: "agentStatus", Parent: "InstanceId"},
		connectUpdate("UpdateAgentStatus", "agentStatus", "AgentStatusId", "Name", "Description", "State", "DisplayOrder"),
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Instances, hours of operation, queues, quick connects and agent statuses are stored records: no contact center runs, so nothing is answered, routed, dialed, recorded or transcribed, and no access URL, service role, storage, Lambda or Lex association exists. Users, routing profiles, security profiles, contact flows, prompts, rules, phone numbers, contacts, metrics and every other official operation return UnknownOperationException with HTTP 501.",
		"Instances must use identityManagementType CONNECT_MANAGED, because SAML and existing directories need an external identity system, and DirectoryId is refused. An instance stays in status CREATION_IN_PROGRESS, as the other provisioned records of this catalog do, and reports no InstanceAccessUrl or ServiceRole. The instance alias must be unique within the Region (a simulator rule), and at most 5 instances exist per Region as a simulator limit. Deleting an instance deletes everything it owns, a cascade the official operation does not document.",
		"Hours of operation, queue, quick connect and agent status names are unique within an instance as the DuplicateResourceException documents; 100 hours of operation, queues and quick connects and 50 agent statuses per instance are simulator limits. The time zone, the hours of operation of a queue and the queue and flow IDs of a quick connect are stored without being resolved, so a queue may name hours of operation that do not exist.",
		"Queues refuse an outbound caller ID number (OutboundCallerIdNumberId), an outbound email configuration and QuickConnectIds, because no phone number or email address can be claimed and no queue-to-quick-connect association is stored; the other queue members are stored. Quick connects must be of type QUEUE with a QueueConfig, since USER, PHONE_NUMBER and FLOW destinations need users, a dialed number or a flow. Agent statuses are always of type CUSTOM and no preset status is created. List operations refuse the QueueTypes, QuickConnectTypes and AgentStatusTypes filters.",
		"Tags apply to the instance and the four child kinds by ARN (arn:aws:connect:region:account:instance/id and instance/id/queue/id, operating-hours/id, transfer-destination/id and agent-state/id).",
	},
}

func connectInstance(input map[string]any) error {
	if _, ok := input["DirectoryId"]; ok {
		return Failure("InvalidRequestException", "DirectoryId is not supported: no directory exists in the simulator.")
	}
	if input["IdentityManagementType"] != "CONNECT_MANAGED" {
		return Failure("InvalidRequestException", "IdentityManagementType must be CONNECT_MANAGED: no identity provider or directory exists in the simulator.")
	}
	return nil
}

// connectQueue refuses the members that name a claimed phone number or an
// email address, neither of which the simulator holds.
func connectQueue(input map[string]any) error {
	if _, ok := input["OutboundEmailConfig"]; ok {
		return Failure("InvalidRequestException", "OutboundEmailConfig is not supported: no email address exists in the simulator.")
	}
	if caller, ok := input["OutboundCallerConfig"].(map[string]any); ok {
		if _, ok := caller["OutboundCallerIdNumberId"]; ok {
			return Failure("InvalidRequestException", "OutboundCallerIdNumberId is not supported: no phone number can be claimed in the simulator.")
		}
	}
	return nil
}

// connectQuickConnect keeps quick connects to queue destinations.
func connectQuickConnect(input map[string]any) error {
	config, _ := input["QuickConnectConfig"].(map[string]any)
	if config == nil {
		return nil
	}
	if config["QuickConnectType"] != "QUEUE" || config["QueueConfig"] == nil {
		return Failure("InvalidRequestException", "QuickConnectType must be QUEUE with a QueueConfig: no user, phone number or flow destination exists in the simulator.")
	}
	return nil
}
