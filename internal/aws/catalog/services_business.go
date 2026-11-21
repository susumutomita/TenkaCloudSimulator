package catalog

import (
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

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

// The Amazon Chime SDK models all sign with the name chime and share the
// routes POST /tags?operation=tag-resource, POST /tags?operation=untag-resource
// and GET /tags, so the engine routes those three requests to whichever chime
// model is registered first. Only chimeVoice binds the tag operations, so it
// must be registered before chimeIdentity and chimeMediaPipelines, and the
// other models refuse Tags instead of storing tags nothing could read.

// chimeNoTags refuses creation tags in the chime models that do not own the
// shared tag routes.
func chimeNoTags(input map[string]any) error {
	if _, ok := input["Tags"]; ok {
		return Failure("BadRequestException", "Tags are not supported here: the shared Amazon Chime SDK tag operations are served by the voice model only.")
	}
	return nil
}

// chimeVoice serves Amazon Chime SDK Voice Connectors, Voice Connector groups
// and SIP media applications as stored records. No call is placed or routed.
var chimeVoice = &Service{
	Model: "chime-sdk-voice", Title: "Amazon Chime SDK Voice", IAMPrefix: "chime", Issues: []int{90},
	Resources: []*Resource{
		{Kind: "voiceConnector", Shape: "VoiceConnector", ID: "alnum:22", Limit: 50,
			ARN: "arn:{partition}:chime:{region}:{account}:vc/{id}", ARNMember: "VoiceConnectorArn", IDMember: "VoiceConnectorId",
			Generate: map[string]any{"CreatedTimestamp": "$now", "UpdatedTimestamp": "$now", "AwsRegion": "us-east-1", "NetworkType": "IPV4_ONLY"}},
		{Kind: "voiceConnectorGroup", Shape: "VoiceConnectorGroup", ID: "uuid", Limit: 50,
			ARN: "arn:{partition}:chime:{region}:{account}:vcg/{id}", ARNMember: "VoiceConnectorGroupArn", IDMember: "VoiceConnectorGroupId",
			Generate: map[string]any{"CreatedTimestamp": "$now", "UpdatedTimestamp": "$now", "VoiceConnectorItems": []any{}}},
		{Kind: "sipMediaApplication", Shape: "SipMediaApplication", ID: "uuid", Limit: 25,
			ARN: "arn:{partition}:chime:{region}:{account}:sma/{id}", ARNMember: "SipMediaApplicationArn", IDMember: "SipMediaApplicationId",
			Generate: map[string]any{"CreatedTimestamp": "$now", "UpdatedTimestamp": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateVoiceConnector", Kind: Create, Resource: "voiceConnector"},
		{Name: "GetVoiceConnector", Kind: Read, Resource: "voiceConnector", Lookup: "VoiceConnectorId"},
		{Name: "ListVoiceConnectors", Kind: List, Resource: "voiceConnector"},
		{Name: "UpdateVoiceConnector", Kind: Update, Resource: "voiceConnector", Lookup: "VoiceConnectorId", Touch: []string{"UpdatedTimestamp"},
			Fields: []string{"Name", "RequireEncryption"}},
		{Name: "DeleteVoiceConnector", Kind: Delete, Resource: "voiceConnector", Lookup: "VoiceConnectorId"},
		{Name: "CreateVoiceConnectorGroup", Kind: Create, Resource: "voiceConnectorGroup"},
		{Name: "GetVoiceConnectorGroup", Kind: Read, Resource: "voiceConnectorGroup", Lookup: "VoiceConnectorGroupId"},
		{Name: "ListVoiceConnectorGroups", Kind: List, Resource: "voiceConnectorGroup"},
		{Name: "UpdateVoiceConnectorGroup", Kind: Update, Resource: "voiceConnectorGroup", Lookup: "VoiceConnectorGroupId", Touch: []string{"UpdatedTimestamp"},
			Fields: []string{"Name", "VoiceConnectorItems"}},
		{Name: "DeleteVoiceConnectorGroup", Kind: Delete, Resource: "voiceConnectorGroup", Lookup: "VoiceConnectorGroupId"},
		{Name: "CreateSipMediaApplication", Kind: Create, Resource: "sipMediaApplication"},
		{Name: "GetSipMediaApplication", Kind: Read, Resource: "sipMediaApplication", Lookup: "SipMediaApplicationId"},
		{Name: "ListSipMediaApplications", Kind: List, Resource: "sipMediaApplication"},
		{Name: "UpdateSipMediaApplication", Kind: Update, Resource: "sipMediaApplication", Lookup: "SipMediaApplicationId", Touch: []string{"UpdatedTimestamp"},
			Fields: []string{"Name", "Endpoints"}},
		{Name: "DeleteSipMediaApplication", Kind: Delete, Resource: "sipMediaApplication", Lookup: "SipMediaApplicationId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Voice Connectors, Voice Connector groups and SIP media applications are stored records: no call is placed, received or routed, no SIP signaling or media exists, and no Lambda function is invoked. A Voice Connector reports no OutboundHostName. Phone numbers and orders, SIP rules (which name a claimed number or a host name), proxy sessions, the termination, origination, streaming, logging and emergency calling settings of a Voice Connector, voice profiles and domains, speaker search and voice tone tasks, and the global settings return UnknownOperationException with HTTP 501.",
		"A Voice Connector defaults to AwsRegion us-east-1 and NetworkType IPV4_ONLY; the Voice Connector IDs named by a group (priority 1 to 99 as modeled) and the Lambda ARN of a SIP media application endpoint (exactly one, as modeled) are stored without being resolved. Counts per Region (50 Voice Connectors, 50 groups and 25 SIP media applications) are simulator limits, and the ID and ARN forms (vc/, vcg/ and sma/) are inferred.",
		"All Amazon Chime SDK models sign as chime and share the POST /tags?operation=tag-resource, POST /tags?operation=untag-resource and GET /tags routes, which the engine cannot tell apart by model. Only this model binds the tag operations, so it applies to Voice Connectors and SIP media applications by ARN and must be registered before the other chime models; the identity and media pipelines models refuse Tags on creation, and the messaging model is not served.",
	},
}

// chimeIdentity serves Amazon Chime SDK Identity app instances as stored
// records. No messaging identity can sign in, and no channel or message exists.
var chimeIdentity = &Service{
	Model: "chime-sdk-identity", Title: "Amazon Chime SDK Identity", IAMPrefix: "chime", Issues: []int{90},
	Resources: []*Resource{
		{Kind: "appInstance", Shape: "AppInstance", Summary: "AppInstanceSummary", ID: "uuid", Limit: 25,
			ARN: "arn:{partition}:chime:{region}:{account}:app-instance/{id}", ARNMember: "AppInstanceArn",
			Generate: map[string]any{"CreatedTimestamp": "$now", "LastUpdatedTimestamp": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateAppInstance", Kind: Create, Resource: "appInstance", Check: chimeNoTags},
		{Name: "DescribeAppInstance", Kind: Read, Resource: "appInstance", Lookup: "AppInstanceArn"},
		{Name: "ListAppInstances", Kind: List, Resource: "appInstance"},
		{Name: "UpdateAppInstance", Kind: Update, Resource: "appInstance", Lookup: "AppInstanceArn", Touch: []string{"LastUpdatedTimestamp"},
			Fields: []string{"Name", "Metadata"}},
		{Name: "DeleteAppInstance", Kind: Delete, Resource: "appInstance", Lookup: "AppInstanceArn"},
	},
	Bounds: []string{
		"App instances are stored records: no messaging identity can sign in and no channel, message or push endpoint exists. Admins, bots, users and their endpoints, retention settings and expiration settings return UnknownOperationException with HTTP 501, because each is keyed by an app instance ARN that the stored model cannot attach a child to.",
		"The tag operations are not bound here: all Amazon Chime SDK models share the same tag routes and the engine serves them from the voice model only (see the chime-sdk-voice bounds), so CreateAppInstance refuses Tags. At most 25 app instances per Region exist as a simulator limit, and the ARN form app-instance/id is inferred.",
	},
}

// chimeMediaPipelines serves Amazon Chime SDK media insights pipeline
// configurations as stored records. No media is captured or analyzed.
var chimeMediaPipelines = &Service{
	Model: "chime-sdk-media-pipelines", Title: "Amazon Chime SDK Media Pipelines", IAMPrefix: "chime", Issues: []int{90},
	Resources: []*Resource{
		{Kind: "insightsConfiguration", Shape: "MediaInsightsPipelineConfiguration", Summary: "MediaInsightsPipelineConfigurationSummary", ID: "uuid", Unique: true, Limit: 25,
			ARN: "arn:{partition}:chime:{region}:{account}:media-insights-pipeline-configuration/{name}", ARNMember: "MediaInsightsPipelineConfigurationArn",
			IDMember: "MediaInsightsPipelineConfigurationId", NameMember: "MediaInsightsPipelineConfigurationName",
			Generate: map[string]any{"CreatedTimestamp": "$now", "UpdatedTimestamp": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateMediaInsightsPipelineConfiguration", Kind: Create, Resource: "insightsConfiguration", Check: chimeNoTags},
		{Name: "GetMediaInsightsPipelineConfiguration", Kind: Read, Resource: "insightsConfiguration", Lookup: "Identifier"},
		{Name: "ListMediaInsightsPipelineConfigurations", Kind: List, Resource: "insightsConfiguration"},
		{Name: "UpdateMediaInsightsPipelineConfiguration", Kind: Update, Resource: "insightsConfiguration", Lookup: "Identifier", Touch: []string{"UpdatedTimestamp"},
			Fields: []string{"ResourceAccessRoleArn", "RealTimeAlertConfiguration", "Elements"}},
		{Name: "DeleteMediaInsightsPipelineConfiguration", Kind: Delete, Resource: "insightsConfiguration", Lookup: "Identifier"},
	},
	Bounds: []string{
		"Media insights pipeline configurations are stored records: no media is captured, concatenated, transcribed or analyzed, no pipeline runs, and no Kinesis Video Stream pool exists. Media pipelines of every kind, Kinesis Video Stream pools, speaker search and voice tone analysis tasks return UnknownOperationException with HTTP 501.",
		"The elements (processors and sinks), the alert configuration and the access role ARN are stored without being evaluated or resolved. A configuration is identified by its unique name, its generated ID or its ARN (media-insights-pipeline-configuration/name, as inferred). The tag operations are not bound here: all Amazon Chime SDK models share the same tag routes and the engine serves them from the voice model only, so CreateMediaInsightsPipelineConfiguration refuses Tags. At most 25 configurations per Region exist as a simulator limit.",
	},
}

// workMail serves Amazon WorkMail organizations and the access and
// impersonation configuration they own as stored records. No mail service
// runs: nothing is received, stored, sent or delivered, and no mailbox, user,
// group or resource exists.
var workMail = &Service{
	Model: "workmail", Title: "Amazon WorkMail", IAMPrefix: "workmail", Issues: []int{100},
	Resources: []*Resource{
		{Kind: "organization", Shape: "DescribeOrganizationResponse", Summary: "OrganizationSummary", ID: "hex:32:m-", Unique: true, Limit: 5, Cascade: true,
			ARN: "arn:{partition}:workmail:{region}:{account}:organization/{id}", ARNMember: "ARN", IDMember: "OrganizationId", NameMember: "Alias",
			Aliases:  map[string]string{"EnableInteroperability": "InteroperabilityEnabled"},
			Generate: map[string]any{"State": "Creating", "InteroperabilityEnabled": false}},
		{Kind: "impersonationRole", Shape: "GetImpersonationRoleResponse", Summary: "ImpersonationRole", ID: "alnum:20", Limit: 30, Parent: "organization", ParentMember: "OrganizationId",
			ARN: "arn:{partition}:workmail:{region}:{account}:organization/{parent}/impersonation-role/{id}", IDMember: "ImpersonationRoleId",
			Generate: map[string]any{"DateCreated": "$now", "DateModified": "$now"}},
		{Kind: "mobileDeviceAccessRule", Shape: "MobileDeviceAccessRule", ID: "alnum:20", Limit: 10, Parent: "organization", ParentMember: "OrganizationId", Unique: true,
			ARN: "arn:{partition}:workmail:{region}:{account}:organization/{parent}/mobile-device-access-rule/{id}", IDMember: "MobileDeviceAccessRuleId", NameMember: "Name",
			Generate: map[string]any{"DateCreated": "$now", "DateModified": "$now"}},
		{Kind: "accessControlRule", Shape: "AccessControlRule", ID: "name", Unique: true, Limit: 10, Parent: "organization", ParentMember: "OrganizationId",
			ARN: "arn:{partition}:workmail:{region}:{account}:organization/{parent}/access-control-rule/{name}", NameMember: "Name",
			Generate: map[string]any{"DateCreated": "$now", "DateModified": "$now"}},
		{Kind: "availabilityConfiguration", Shape: "AvailabilityConfiguration", ID: "name", Unique: true, Limit: 10, Parent: "organization", ParentMember: "OrganizationId",
			ARN: "arn:{partition}:workmail:{region}:{account}:organization/{parent}/availability-configuration/{name}", NameMember: "DomainName",
			Generate: map[string]any{"DateCreated": "$now", "DateModified": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateOrganization", Kind: Create, Resource: "organization", Check: workMailOrganization},
		{Name: "DescribeOrganization", Kind: Read, Resource: "organization", Lookup: "OrganizationId"},
		{Name: "ListOrganizations", Kind: List, Resource: "organization"},
		{Name: "DeleteOrganization", Kind: Delete, Resource: "organization", Lookup: "OrganizationId", Set: map[string]any{"State": "Deleted"},
			Accept: []string{"ClientToken", "DeleteDirectory", "ForceDelete", "DeleteIdentityCenterApplication"}},
		{Name: "CreateImpersonationRole", Kind: Create, Resource: "impersonationRole", Parent: "OrganizationId"},
		{Name: "GetImpersonationRole", Kind: Read, Resource: "impersonationRole", Parent: "OrganizationId", Lookup: "ImpersonationRoleId"},
		{Name: "ListImpersonationRoles", Kind: List, Resource: "impersonationRole", Parent: "OrganizationId"},
		{Name: "UpdateImpersonationRole", Kind: Update, Resource: "impersonationRole", Parent: "OrganizationId", Lookup: "ImpersonationRoleId", Touch: []string{"DateModified"},
			Fields: []string{"Name", "Type", "Description", "Rules"}},
		{Name: "DeleteImpersonationRole", Kind: Delete, Resource: "impersonationRole", Parent: "OrganizationId", Lookup: "ImpersonationRoleId"},
		{Name: "CreateMobileDeviceAccessRule", Kind: Create, Resource: "mobileDeviceAccessRule", Parent: "OrganizationId"},
		{Name: "ListMobileDeviceAccessRules", Kind: List, Resource: "mobileDeviceAccessRule", Parent: "OrganizationId"},
		{Name: "UpdateMobileDeviceAccessRule", Kind: Update, Resource: "mobileDeviceAccessRule", Parent: "OrganizationId", Lookup: "MobileDeviceAccessRuleId", Touch: []string{"DateModified"},
			Fields: []string{"Name", "Description", "Effect", "DeviceTypes", "NotDeviceTypes", "DeviceModels", "NotDeviceModels", "DeviceOperatingSystems", "NotDeviceOperatingSystems",
				"DeviceUserAgents", "NotDeviceUserAgents"}},
		{Name: "DeleteMobileDeviceAccessRule", Kind: Delete, Resource: "mobileDeviceAccessRule", Parent: "OrganizationId", Lookup: "MobileDeviceAccessRuleId"},
		{Name: "PutAccessControlRule", Kind: Put, Resource: "accessControlRule", Parent: "OrganizationId", Lookup: "Name", Touch: []string{"DateModified"}, Check: workMailAccessRule},
		{Name: "ListAccessControlRules", Kind: List, Resource: "accessControlRule", Parent: "OrganizationId"},
		{Name: "DeleteAccessControlRule", Kind: Delete, Resource: "accessControlRule", Parent: "OrganizationId", Lookup: "Name"},
		{Name: "CreateAvailabilityConfiguration", Kind: Create, Resource: "availabilityConfiguration", Parent: "OrganizationId", Check: workMailAvailability},
		{Name: "ListAvailabilityConfigurations", Kind: List, Resource: "availabilityConfiguration", Parent: "OrganizationId"},
		{Name: "UpdateAvailabilityConfiguration", Kind: Update, Resource: "availabilityConfiguration", Parent: "OrganizationId", Lookup: "DomainName", Touch: []string{"DateModified"},
			Fields: []string{"LambdaProvider", "ProviderType"}, Check: workMailAvailability},
		{Name: "DeleteAvailabilityConfiguration", Kind: Delete, Resource: "availabilityConfiguration", Parent: "OrganizationId", Lookup: "DomainName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN", Check: workMailOrganizationARN},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN", Check: workMailOrganizationARN},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN", Check: workMailOrganizationARN},
	},
	Bounds: []string{
		"Organizations, impersonation roles, mobile device access rules, access control rules and availability configurations are stored records: no mail is received, stored, sent or delivered, no SMTP, IMAP, EWS or ActiveSync endpoint exists, and rules are stored without being evaluated. Users, groups, resources, aliases, mailbox permissions and exports, delegates, mail domains, retention policies, passwords and personal access tokens, identity provider and Identity Center configuration, the effect and test operations and every other official operation return UnknownOperationException with HTTP 501.",
		"An organization stays in state Creating, as the other provisioned records of this catalog do, and reports no DirectoryId, DirectoryType, DefaultMailDomain or MigrationAdmin because no directory or mail domain exists. CreateOrganization refuses DirectoryId, Domains and KmsKeyArn, which name a directory, mail domains or a key that would have to exist; Alias is the unique organization name; EnableInteroperability is stored as InteroperabilityEnabled. DeleteOrganization returns state Deleted, accepts but ignores DeleteDirectory, ForceDelete and DeleteIdentityCenterApplication, and deletes everything the organization owns, a cascade the official operation does not document. At most 5 organizations per Region exist as a simulator limit.",
		"Impersonation roles, mobile device access rules, access control rules and availability configurations are scoped to an organization; 30 roles, 10 mobile device access rules, 10 access control rules and 10 availability configurations per organization are simulator limits. Mobile device access rule names are unique per organization; PutAccessControlRule replaces a rule of the same name as documented. The user IDs, impersonation role IDs, Lambda ARN and domain name that rules and configurations mention are stored without being resolved.",
		"Availability configurations accept only a LambdaProvider: the EwsProvider carries an Exchange user name and password and is refused, so no password is stored. They report ProviderType LAMBDA. The organization, role, rule and configuration ID forms (m-, 20 character IDs) are inferred, and the child ARNs used for IAM evaluation are not defined by AWS.",
		"Tags apply to organizations only, by ARN (arn:aws:workmail:region:account:organization/id); the ARNs of the stored child records are refused.",
	},
}

// workMailOrganization refuses the members of an organization that name a
// directory, mail domains or a key, none of which exists.
func workMailOrganization(input map[string]any) error {
	for _, member := range []string{"DirectoryId", "Domains", "KmsKeyArn"} {
		if _, ok := input[member]; ok {
			return Failure("InvalidParameterException", member+" is not supported: no directory, mail domain or key exists in the simulator.")
		}
	}
	return nil
}

// workMailAvailability keeps availability configurations to the Lambda
// provider and records its provider type.
func workMailAvailability(input map[string]any) error {
	if _, ok := input["EwsProvider"]; ok {
		return Failure("InvalidParameterException", "EwsProvider is not supported: the simulator never stores an Exchange password.")
	}
	if _, ok := input["LambdaProvider"]; !ok {
		return Failure("InvalidParameterException", "The request must contain exactly one provider definition, here a LambdaProvider.")
	}
	input["ProviderType"] = "LAMBDA"
	return nil
}

var (
	workMailOrganizationPattern = regexp.MustCompile(`^arn:[a-z-]+:workmail:[a-z0-9-]*:[0-9]{12}:organization/[^/]+$`)
	workMailActions             = []string{"ActiveSync", "AutoDiscover", "EWS", "IMAP", "SMTP", "WindowsOutlook", "WebMail"}
)

// workMailAccessRule applies the documented value rules of an access control
// rule: IPv4 CIDR ranges and the known access protocol actions.
func workMailAccessRule(input map[string]any) error {
	for _, member := range []string{"IpRanges", "NotIpRanges"} {
		ranges, _ := input[member].([]any)
		for _, value := range ranges {
			prefix, err := netip.ParsePrefix(bizText(value))
			if err != nil || !prefix.Addr().Is4() {
				return Failure("InvalidParameterException", member+" must hold IPv4 CIDR ranges such as 192.0.2.0/24.")
			}
		}
	}
	for _, member := range []string{"Actions", "NotActions"} {
		actions, _ := input[member].([]any)
		for _, value := range actions {
			if !slices.Contains(workMailActions, bizText(value)) {
				return Failure("InvalidParameterException", member+" values are "+strings.Join(workMailActions, ", ")+".")
			}
		}
	}
	return nil
}

// workMailOrganizationARN limits tagging to the organization ARN.
func workMailOrganizationARN(input map[string]any) error {
	if !workMailOrganizationPattern.MatchString(bizText(input["ResourceARN"])) {
		return invalid("Only the ARN of an organization can be tagged.")
	}
	return nil
}
