package catalog

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Reviewed bounded specifications for machine learning and AI services.
// Each binds only official operations whose effect is configuration
// metadata. Inference, training, model import, analysis, synthesis,
// transcription, translation, detection, document indexing, human review
// loops and every job stay unbound (HTTP 501), and no resource ever claims
// to hold a trained model.

// replaceOmitted clears the optional members an update omits, for official
// updates that replace the whole stored draft instead of merging it.
func replaceOmitted(input map[string]any, members ...string) {
	for _, member := range members {
		if _, ok := input[member]; !ok {
			input[member] = nil
		}
	}
}

// refuseMembers refuses the first supplied member of a list whose official
// effect the simulator does not perform.
func refuseMembers(input map[string]any, code, message string, members ...string) error {
	for _, member := range members {
		if _, ok := input[member]; ok {
			return Failure(code, member+": "+message)
		}
	}
	return nil
}

var bedrock = &Service{
	Model: "bedrock", Title: "Amazon Bedrock", IAMPrefix: "bedrock", Issues: []int{162},
	Resources: []*Resource{
		{Kind: "guardrail", Shape: "GetGuardrailResponse", Summary: "GuardrailSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:bedrock:{region}:{account}:guardrail/{id}",
			ARNMember: "guardrailArn", NameMember: "name", IDMember: "guardrailId", Unique: true, Limit: 100,
			Generate: map[string]any{"id": "$id", "arn": "$arn", "version": "DRAFT", "status": "READY", "createdAt": "$now", "updatedAt": "$now"}},
		{Kind: "inferenceProfile", Shape: "GetInferenceProfileResponse", Summary: "InferenceProfileSummary", ID: "alnum:12",
			ARN:       "arn:{partition}:bedrock:{region}:{account}:application-inference-profile/{id}",
			ARNMember: "inferenceProfileArn", NameMember: "inferenceProfileName", IDMember: "inferenceProfileId", Unique: true, Limit: 100,
			Generate: map[string]any{"type": "APPLICATION", "status": "ACTIVE", "createdAt": "$now", "updatedAt": "$now"}},
		{Kind: "invocationLogging", Shape: "GetModelInvocationLoggingConfigurationResponse", Singleton: true, ID: "name",
			ARN: "arn:{partition}:bedrock:{region}:{account}:model-invocation-logging", NameMember: "loggingConfig"},
	},
	Ops: []Op{
		{Name: "CreateGuardrail", Kind: Create, Resource: "guardrail", Check: guardrailCreate},
		{Name: "GetGuardrail", Kind: Read, Resource: "guardrail", Lookup: "guardrailIdentifier", Accept: []string{"guardrailVersion"}, Check: guardrailDraft},
		{Name: "ListGuardrails", Kind: List, Resource: "guardrail", Filters: map[string]string{"guardrailIdentifier": "guardrailId"}, Check: guardrailFilter},
		{Name: "UpdateGuardrail", Kind: Update, Resource: "guardrail", Lookup: "guardrailIdentifier", Check: guardrailUpdate, Touch: []string{"updatedAt"},
			Fields: []string{"name", "description", "topicPolicy", "contentPolicy", "wordPolicy", "sensitiveInformationPolicy", "contextualGroundingPolicy",
				"blockedInputMessaging", "blockedOutputsMessaging", "kmsKeyArn"}},
		{Name: "DeleteGuardrail", Kind: Delete, Resource: "guardrail", Lookup: "guardrailIdentifier", Accept: []string{"guardrailVersion"}, Check: guardrailDraft},
		{Name: "CreateInferenceProfile", Kind: Create, Resource: "inferenceProfile", Check: inferenceProfileSource},
		{Name: "GetInferenceProfile", Kind: Read, Resource: "inferenceProfile", Lookup: "inferenceProfileIdentifier"},
		{Name: "ListInferenceProfiles", Kind: List, Resource: "inferenceProfile", Filters: map[string]string{"typeEquals": "type"}, Check: applicationProfiles, Fixed: map[string]any{"typeEquals": "APPLICATION"}},
		{Name: "DeleteInferenceProfile", Kind: Delete, Resource: "inferenceProfile", Lookup: "inferenceProfileIdentifier"},
		{Name: "GetModelInvocationLoggingConfiguration", Kind: Read, Resource: "invocationLogging"},
		{Name: "PutModelInvocationLoggingConfiguration", Kind: Put, Resource: "invocationLogging"},
		{Name: "DeleteModelInvocationLoggingConfiguration", Kind: Delete, Resource: "invocationLogging"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceARN"},
	},
	Bounds: []string{
		"Guardrails are stored DRAFT configuration that is READY at once: no prompt or model response is ever evaluated, filtered, masked or blocked, and ApplyGuardrail, guardrail versions, automated reasoning policies and cross-Region guardrail profiles are not implemented; automatedReasoningPolicyConfig and crossRegionConfig are refused.",
		"Policy configurations are stored in their official read form (for example topicPolicyConfig.topicsConfig is returned as topicPolicy.topics). kmsKeyId must be a KMS key ARN and is returned as kmsKeyArn without contacting AWS KMS. UpdateGuardrail replaces the whole draft, clearing omitted policies, description and key; a renamed guardrail keeps its original name reserved for uniqueness.",
		"guardrailVersion is accepted only as DRAFT on GetGuardrail; numbered versions do not exist, so any number returns ResourceNotFoundException. ListGuardrails lists only DRAFT versions and returns an empty list for an unknown guardrailIdentifier.",
		"Application inference profiles copy exactly one foundation model ARN, which is not checked against a model catalog; copying a system-defined cross-Region profile is refused, no system-defined profile exists, and ListInferenceProfiles requires typeEquals APPLICATION. Nothing is ever invoked through a profile.",
		"The model invocation logging configuration is stored without writing any log to Amazon S3 or CloudWatch Logs. DeleteModelInvocationLoggingConfiguration returns ResourceNotFoundException with HTTP 404 when no configuration is stored, which the official operation does not model.",
		"Model invocation, custom models (CreateCustomModel imports model weights), customization, import, copy, invocation and evaluation jobs, provisioned throughput, marketplace endpoints, foundation model listings and agreements, prompt routers (whose official ARN pattern admits only default routers) and enforced guardrail configurations return UnknownOperationException with HTTP 501.",
	},
}

// guardrailPolicyNames maps each create/update policy configuration member
// onto the member of the official GetGuardrail response that returns it.
var guardrailPolicyNames = map[string]string{
	"topicPolicyConfig": "topicPolicy", "contentPolicyConfig": "contentPolicy", "wordPolicyConfig": "wordPolicy",
	"sensitiveInformationPolicyConfig": "sensitiveInformationPolicy", "contextualGroundingPolicyConfig": "contextualGroundingPolicy",
}

// guardrailListNames maps the policy configuration lists onto the members
// of the official policy shapes; their elements share every member name.
var guardrailListNames = map[string]string{
	"topicsConfig": "topics", "tierConfig": "tier", "filtersConfig": "filters", "wordsConfig": "words",
	"managedWordListsConfig": "managedWordLists", "piiEntitiesConfig": "piiEntities", "regexesConfig": "regexes",
}

var kmsKeyARN = regexp.MustCompile(`^arn:aws(-[^:]+)?:kms:[a-z0-9-]+:[0-9]{12}:key/[a-zA-Z0-9-]{36}$`)

// guardrailCreate stores the policy configurations in their official read
// form and refuses policies that need other resources.
func guardrailCreate(input map[string]any) error {
	if err := refuseMembers(input, "ValidationException", "automated reasoning policies and cross-Region guardrail profiles are not supported by the simulator.",
		"automatedReasoningPolicyConfig", "crossRegionConfig"); err != nil {
		return err
	}
	if key, ok := input["kmsKeyId"].(string); ok {
		if !kmsKeyARN.MatchString(key) {
			return Failure("ValidationException", "kmsKeyId must be a KMS key ARN in the simulator; key IDs and aliases are not resolved.")
		}
		delete(input, "kmsKeyId")
		input["kmsKeyArn"] = key
	}
	for config, policy := range guardrailPolicyNames {
		value, ok := input[config].(map[string]any)
		if !ok {
			continue
		}
		renamed := map[string]any{}
		for member, item := range value {
			renamed[guardrailListNames[member]] = item
		}
		delete(input, config)
		input[policy] = renamed
	}
	return nil
}

// guardrailUpdate applies the create rules; the official update replaces
// the whole draft, so omitted optional members are cleared.
func guardrailUpdate(input map[string]any) error {
	if err := guardrailCreate(input); err != nil {
		return err
	}
	replaceOmitted(input, "description", "topicPolicy", "contentPolicy", "wordPolicy", "sensitiveInformationPolicy", "contextualGroundingPolicy", "kmsKeyArn")
	return nil
}

// guardrailDraft reports numbered guardrail versions as missing: only the
// working DRAFT exists because CreateGuardrailVersion is not implemented.
func guardrailDraft(input map[string]any) error {
	if version, ok := input["guardrailVersion"].(string); ok && version != "DRAFT" {
		return Failure("ResourceNotFoundException", "Guardrail version "+version+" does not exist; only the DRAFT version is stored.")
	}
	return nil
}

// guardrailFilter lets ListGuardrails name its guardrail by ID or ARN.
func guardrailFilter(input map[string]any) error {
	if identifier, ok := input["guardrailIdentifier"].(string); ok {
		input["guardrailIdentifier"] = identifier[strings.LastIndex(identifier, "/")+1:]
	}
	return nil
}

var foundationModelARN = regexp.MustCompile(`^arn:aws(-[^:]+)?:bedrock:[a-z0-9-]{1,20}::foundation-model/[a-z0-9-]{1,63}[.]([a-z0-9-]{1,63}[.]){0,2}[a-z0-9-]{1,63}([:][a-z0-9-]{1,63}){0,2}$`)

// inferenceProfileSource records the copied foundation model as the
// profile's only model, as an application inference profile reports it.
func inferenceProfileSource(input map[string]any) error {
	source, _ := input["modelSource"].(map[string]any)
	model, _ := source["copyFrom"].(string)
	if !foundationModelARN.MatchString(model) {
		return Failure("ValidationException", "modelSource.copyFrom must be a foundation model ARN; copying a system-defined inference profile is not supported by the simulator.")
	}
	delete(input, "modelSource")
	input["models"] = []any{map[string]any{"modelArn": model}}
	return nil
}

// applicationProfiles requires the APPLICATION type: system-defined
// cross-Region profiles are owned by AWS and do not exist in the simulator.
func applicationProfiles(input map[string]any) error {
	if input["typeEquals"] != "APPLICATION" {
		return Failure("ValidationException", "The simulator lists only application inference profiles; set typeEquals to APPLICATION.")
	}
	return nil
}

var bedrockAgentCore = &Service{
	Model: "bedrock-agentcore-control", Title: "Amazon Bedrock AgentCore Control", IAMPrefix: "bedrock-agentcore", Issues: []int{163},
	Resources: []*Resource{
		{Kind: "workloadIdentity", Shape: "GetWorkloadIdentityResponse", Summary: "WorkloadIdentityType", ID: "name",
			ARN:       "arn:{partition}:bedrock-agentcore:{region}:{account}:workload-identity-directory/default/workload-identity/{name}",
			ARNMember: "workloadIdentityArn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"createdTime": "$now", "lastUpdatedTime": "$now"}},
		{Kind: "tokenVault", Shape: "GetTokenVaultResponse", Singleton: true, ID: "name",
			ARN: "arn:{partition}:bedrock-agentcore:{region}:{account}:token-vault/default", NameMember: "tokenVaultId",
			Generate: map[string]any{"tokenVaultId": "default", "kmsConfiguration": map[string]any{"keyType": "ServiceManagedKey"}, "lastModifiedDate": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateWorkloadIdentity", Kind: Create, Resource: "workloadIdentity"},
		{Name: "GetWorkloadIdentity", Kind: Read, Resource: "workloadIdentity", Lookup: "name"},
		{Name: "ListWorkloadIdentities", Kind: List, Resource: "workloadIdentity"},
		{Name: "UpdateWorkloadIdentity", Kind: Update, Resource: "workloadIdentity", Lookup: "name",
			Fields: []string{"allowedResourceOauth2ReturnUrls"}, Touch: []string{"lastUpdatedTime"}},
		{Name: "DeleteWorkloadIdentity", Kind: Delete, Resource: "workloadIdentity", Lookup: "name"},
		{Name: "GetTokenVault", Kind: Read, Resource: "tokenVault", Accept: []string{"tokenVaultId"}, Check: defaultTokenVault},
		{Name: "SetTokenVaultCMK", Kind: Put, Resource: "tokenVault", Accept: []string{"tokenVaultId"}, Check: tokenVaultKey, Touch: []string{"lastModifiedDate"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Workload identities are stored in the default workload identity directory: no workload access token, OAuth2 flow or credential exchange is ever performed, and the allowed OAuth2 return URLs are never contacted.",
		"Only the default token vault exists; SetTokenVaultCMK stores its KMS configuration without contacting AWS KMS or encrypting any credential, and another tokenVaultId returns ResourceNotFoundException.",
		"Agent runtimes and endpoints, gateways and targets, memories, browsers, code interpreters (whose identifiers derive from their names), API key and OAuth2 credential providers (which hold secrets) return UnknownOperationException with HTTP 501.",
	},
}

// defaultTokenVault reports any vault other than default as missing.
func defaultTokenVault(input map[string]any) error {
	if id, ok := input["tokenVaultId"].(string); ok && id != "default" {
		return Failure("ResourceNotFoundException", "Only the default token vault exists in the simulator.")
	}
	return nil
}

// tokenVaultKey requires a key ARN exactly for customer managed keys.
func tokenVaultKey(input map[string]any) error {
	if err := defaultTokenVault(input); err != nil {
		return err
	}
	config, _ := input["kmsConfiguration"].(map[string]any)
	if _, key := config["kmsKeyArn"]; key != (config["keyType"] == "CustomerManagedKey") {
		return Failure("ValidationException", "kmsKeyArn is required for CustomerManagedKey and not allowed for ServiceManagedKey.")
	}
	return nil
}

var comprehend = &Service{
	Model: "comprehend", Title: "Amazon Comprehend", IAMPrefix: "comprehend", Issues: []int{166},
	Resources: []*Resource{
		// The create request is the only official shape that carries the
		// flywheel name; DescribeFlywheel returns FlywheelProperties.
		{Kind: "flywheel", Shape: "CreateFlywheelRequest", Summary: "FlywheelProperties", ID: "name",
			ARN:       "arn:{partition}:comprehend:{region}:{account}:flywheel/{name}",
			ARNMember: "FlywheelArn", NameMember: "FlywheelName", Unique: true, Limit: 50,
			Generate: map[string]any{"Status": "ACTIVE", "CreationTime": "$now", "LastModifiedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateFlywheel", Kind: Create, Resource: "flywheel", Check: flywheelCreate},
		{Name: "DescribeFlywheel", Kind: Read, Resource: "flywheel", Lookup: "FlywheelArn"},
		{Name: "UpdateFlywheel", Kind: Update, Resource: "flywheel", Lookup: "FlywheelArn", Check: noActiveModel,
			Fields: []string{"DataAccessRoleArn"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteFlywheel", Kind: Delete, Resource: "flywheel", Lookup: "FlywheelArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Flywheels are stored ACTIVE configuration for a new model: no data lake is created in the DataLakeS3Uri bucket, no dataset is ingested and no iteration trains, evaluates or promotes a model, so LatestFlywheelIteration and ActiveModelArn stay absent.",
		"ActiveModelArn is refused on CreateFlywheel and UpdateFlywheel because no trained document classifier or entity recognizer exists; TaskConfig must carry the configuration of its ModelType, and UpdateFlywheel changes only DataAccessRoleArn (DataSecurityConfig is refused).",
		"ListFlywheels is not implemented because its FlywheelSummary list differs from the FlywheelProperties description; use DescribeFlywheel with the flywheel ARN.",
		"Detection, classification and every analysis job, document classifiers and entity recognizers (which train), endpoints (which need a trained model), datasets, flywheel iterations, model import and resource policies return UnknownOperationException with HTTP 501.",
	},
}

// flywheelCreate starts flywheels only from a task configuration, because
// no trained model exists to become the active model.
func flywheelCreate(input map[string]any) error {
	if err := noActiveModel(input); err != nil {
		return err
	}
	task, _ := input["TaskConfig"].(map[string]any)
	modelType, _ := input["ModelType"].(string)
	_, classification := task["DocumentClassificationConfig"]
	_, recognition := task["EntityRecognitionConfig"]
	if task == nil || classification != (modelType == "DOCUMENT_CLASSIFIER") || recognition != (modelType == "ENTITY_RECOGNIZER") {
		return Failure("InvalidRequestException", "A new flywheel needs ModelType and a TaskConfig holding exactly that model type's configuration.")
	}
	return nil
}

// noActiveModel refuses active model ARNs: the simulator trains no model.
func noActiveModel(input map[string]any) error {
	if _, ok := input["ActiveModelArn"]; ok {
		return Failure("InvalidRequestException", "No trained Amazon Comprehend model exists in the simulator, so ActiveModelArn cannot be set.")
	}
	return nil
}

var kendra = &Service{
	Model: "kendra", Title: "Amazon Kendra", IAMPrefix: "kendra", Issues: []int{174},
	Resources: []*Resource{
		{Kind: "index", Shape: "DescribeIndexResponse", Summary: "IndexConfigurationSummary", ID: "uuid",
			ARN:        "arn:{partition}:kendra:{region}:{account}:index/{id}",
			NameMember: "Name", IDMember: "Id", Limit: 5, Cascade: true,
			Generate: map[string]any{"Edition": "ENTERPRISE_EDITION", "UserContextPolicy": "ATTRIBUTE_FILTER", "Status": "ACTIVE",
				"CreatedAt": "$now", "UpdatedAt": "$now", "CapacityUnits": map[string]any{"StorageCapacityUnits": 0, "QueryCapacityUnits": 0},
				"IndexStatistics": map[string]any{"FaqStatistics": map[string]any{"IndexedQuestionAnswersCount": 0},
					"TextDocumentStatistics": map[string]any{"IndexedTextDocumentsCount": 0, "IndexedTextBytes": 0}}}},
		{Kind: "dataSource", Shape: "DescribeDataSourceResponse", Summary: "DataSourceSummary", Parent: "index", ParentMember: "IndexId", ID: "uuid",
			ARN:        "arn:{partition}:kendra:{region}:{account}:index/{parent}/data-source/{id}",
			NameMember: "Name", IDMember: "Id", Limit: 50,
			Generate: map[string]any{"IndexId": "$parentId", "Status": "ACTIVE", "CreatedAt": "$now", "UpdatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateIndex", Kind: Create, Resource: "index"},
		{Name: "DescribeIndex", Kind: Read, Resource: "index", Lookup: "Id"},
		{Name: "ListIndices", Kind: List, Resource: "index"},
		{Name: "UpdateIndex", Kind: Update, Resource: "index", Lookup: "Id", Touch: []string{"UpdatedAt"},
			Fields: []string{"Name", "RoleArn", "Description", "UserTokenConfigurations", "UserContextPolicy", "UserGroupResolutionConfiguration"}},
		{Name: "DeleteIndex", Kind: Delete, Resource: "index", Lookup: "Id"},
		{Name: "CreateDataSource", Kind: Create, Resource: "dataSource", Parent: "IndexId", Check: kendraDataSource},
		{Name: "DescribeDataSource", Kind: Read, Resource: "dataSource", Parent: "IndexId", Lookup: "Id"},
		{Name: "ListDataSources", Kind: List, Resource: "dataSource", Parent: "IndexId"},
		{Name: "UpdateDataSource", Kind: Update, Resource: "dataSource", Parent: "IndexId", Lookup: "Id", Touch: []string{"UpdatedAt"},
			Fields: []string{"Name", "Configuration", "VpcConfiguration", "Description", "Schedule", "RoleArn", "LanguageCode", "CustomDocumentEnrichmentConfiguration"}},
		{Name: "DeleteDataSource", Kind: Delete, Resource: "dataSource", Parent: "IndexId", Lookup: "Id"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Indexes are stored ACTIVE configuration with no capacity units and zero indexed documents and FAQs: nothing is provisioned, indexed, searched or billed, and the role, KMS key, token configurations and IAM Identity Center settings are stored without being used. Index metadata field configurations are not reported and DocumentMetadataConfigurationUpdates and CapacityUnits updates are refused.",
		"Data sources are stored ACTIVE connector configuration: no repository, website, database or SaaS application is crawled, the schedule never runs and no enrichment Lambda function is invoked. CUSTOM data sources take no Configuration, RoleArn or Schedule; every other type needs Configuration and RoleArn. Deleting an index also deletes its data sources.",
		"Query, Retrieve, document and FAQ ingestion, data source sync jobs, thesauri, query suggestions, featured results, access control configurations, principal mappings, experiences and feedback return UnknownOperationException with HTTP 501.",
	},
}

// kendraDataSource applies the documented split between custom data
// sources, which documents reach through the API, and connector types.
func kendraDataSource(input map[string]any) error {
	if input["Type"] == "CUSTOM" {
		return refuseMembers(input, "ValidationException", "custom data sources take no connector configuration.", "Configuration", "RoleArn", "Schedule")
	}
	_, configuration := input["Configuration"]
	_, role := input["RoleArn"]
	if !configuration || !role {
		return Failure("ValidationException", "Connector data sources need Configuration and RoleArn.")
	}
	return nil
}

var lexModels = &Service{
	Model: "lexv2-models", Title: "Amazon Lex Model Building V2", IAMPrefix: "lex", Issues: []int{175},
	Resources: []*Resource{
		{Kind: "bot", Shape: "DescribeBotResponse", Summary: "BotSummary", ID: "alnum:10",
			ARN:        "arn:{partition}:lex:{region}:{account}:bot/{id}",
			NameMember: "botName", IDMember: "botId", Unique: true, Limit: 100, Cascade: true,
			Generate: map[string]any{"botStatus": "Available", "botType": "Bot", "creationDateTime": "$now", "lastUpdatedDateTime": "$now"}},
		{Kind: "botLocale", Shape: "DescribeBotLocaleResponse", Summary: "BotLocaleSummary", Parent: "bot", ParentMember: "botId", ID: "name",
			ARN:        "arn:{partition}:lex:{region}:{account}:bot/{parent}/DRAFT/{name}",
			NameMember: "localeId", Unique: true, Limit: 30,
			Generate: map[string]any{"botId": "$parentId", "botVersion": "DRAFT", "botLocaleStatus": "NotBuilt", "creationDateTime": "$now", "lastUpdatedDateTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateBot", Kind: Create, Resource: "bot", Check: lexBot},
		{Name: "DescribeBot", Kind: Read, Resource: "bot", Lookup: "botId"},
		{Name: "ListBots", Kind: List, Resource: "bot"},
		{Name: "UpdateBot", Kind: Update, Resource: "bot", Lookup: "botId", Check: lexBotUpdate, Touch: []string{"lastUpdatedDateTime"},
			Fields: []string{"botName", "description", "roleArn", "dataPrivacy", "idleSessionTTLInSeconds", "botType", "errorLogSettings"}},
		{Name: "DeleteBot", Kind: Delete, Resource: "bot", Lookup: "botId", Accept: []string{"skipResourceInUseCheck"}, Set: map[string]any{"botStatus": "Deleting"}},
		{Name: "CreateBotLocale", Kind: Create, Resource: "botLocale", Parent: "botId", Accept: []string{"botVersion"}, Check: lexLocale},
		{Name: "DescribeBotLocale", Kind: Read, Resource: "botLocale", Parent: "botId", Lookup: "localeId", Accept: []string{"botVersion"}, Check: lexDraft},
		{Name: "ListBotLocales", Kind: List, Resource: "botLocale", Parent: "botId", Accept: []string{"botVersion"}, Check: lexDraft},
		{Name: "UpdateBotLocale", Kind: Update, Resource: "botLocale", Parent: "botId", Lookup: "localeId", Accept: []string{"botVersion"},
			Touch: []string{"lastUpdatedDateTime"}, Fields: []string{"description", "nluIntentConfidenceThreshold", "voiceSettings", "unifiedSpeechSettings",
				"speechRecognitionSettings", "generativeAISettings", "speechDetectionSensitivity"}},
		{Name: "DeleteBotLocale", Kind: Delete, Resource: "botLocale", Parent: "botId", Lookup: "localeId", Accept: []string{"botVersion"}, Set: map[string]any{"botLocaleStatus": "Deleting"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceARN"},
	},
	Bounds: []string{
		"Bots are stored Available DRAFT configuration with botTags: no conversation is ever recognized or fulfilled, the role and error log settings are not used, and no TestBotAlias is created, so testBotAliasTags is refused (the CreateBot response repeats botTags in testBotAliasTags). Bot networks (botType BotNetwork and botMembers) are refused.",
		"UpdateBot replaces the bot settings, clearing an omitted description and error log settings; a renamed bot keeps its original name reserved for uniqueness. Deleting a bot also deletes its locales; ListBots refuses sortBy and filters.",
		"Bot locales exist only in the DRAFT version and stay NotBuilt; DescribeBotLocale and ListBotLocales report another version as missing. localeId must have the language_REGION form and is not checked against the locales Amazon Lex supports; localeName, intent and slot type counts are not reported. Locale operations are authorized on arn:aws:lex:region:account:bot/botId/DRAFT/localeId.",
		"Building, versions, aliases, intents, slots, slot types, custom vocabularies, imports, exports, recommendations, test sets, analytics, resource policies and replicas return UnknownOperationException with HTTP 501.",
	},
}

// lexBot refuses bot networks, whose members are other bots and versions
// that the simulator does not model.
func lexBot(input map[string]any) error {
	if input["botType"] == "BotNetwork" {
		return Failure("ValidationException", "Bot networks are not supported by the simulator.")
	}
	return refuseMembers(input, "ValidationException", "bot networks are not supported by the simulator.", "botMembers")
}

// lexBotUpdate applies the bot rules; the official update replaces the bot
// settings, so an omitted description and error log settings are cleared.
func lexBotUpdate(input map[string]any) error {
	if err := lexBot(input); err != nil {
		return err
	}
	replaceOmitted(input, "description", "errorLogSettings")
	return nil
}

var lexLocaleID = regexp.MustCompile(`^[a-z]{2,3}_[A-Z0-9]{2,3}$`)

// lexLocale requires the language_REGION locale identifier form.
func lexLocale(input map[string]any) error {
	if locale, _ := input["localeId"].(string); !lexLocaleID.MatchString(locale) {
		return Failure("ValidationException", "localeId must have the language_REGION form, for example en_US.")
	}
	return nil
}

// lexDraft reports numbered bot versions as missing: CreateBotVersion is
// not implemented, so only the DRAFT version exists.
func lexDraft(input map[string]any) error {
	if input["botVersion"] != "DRAFT" {
		return Failure("ResourceNotFoundException", "Only the DRAFT bot version exists in the simulator.")
	}
	return nil
}

var polly = &Service{
	Model: "polly", Title: "Amazon Polly", IAMPrefix: "polly", Issues: []int{180},
	Resources: []*Resource{
		{Kind: "lexicon", Shape: "Lexicon", Summary: "LexiconDescription", ID: "name",
			ARN:        "arn:{partition}:polly:{region}:{account}:lexicon/{name}",
			NameMember: "Name", Unique: true, Limit: 100},
	},
	Ops: []Op{
		{Name: "PutLexicon", Kind: Put, Resource: "lexicon", Lookup: "Name", Check: plsLexicon,
			Set: map[string]any{"LexiconAttributes": map[string]any{"LexiconArn": "$arn", "LastModified": "$now"},
				"Attributes": map[string]any{"LexiconArn": "$arn", "LastModified": "$now"}}},
		{Name: "GetLexicon", Kind: Read, Resource: "lexicon", Lookup: "Name"},
		{Name: "ListLexicons", Kind: List, Resource: "lexicon"},
		{Name: "DeleteLexicon", Kind: Delete, Resource: "lexicon", Lookup: "Name"},
	},
	Bounds: []string{
		"Pronunciation lexicons are stored PLS documents that are never applied: SynthesizeSpeech, speech synthesis tasks and DescribeVoices return UnknownOperationException with HTTP 501.",
		"PutLexicon checks that Content is a PLS lexicon element with an ipa or x-sampa alphabet, a supported xml:lang, at most 4,000 characters and lexemes of at most 100 characters; it creates or replaces the lexicon. Lexicon attributes report LexiconArn and LastModified but not Alphabet, LanguageCode, LexemesCount or Size.",
	},
}

// pollyLanguages are the official Polly language codes.
var pollyLanguages = map[string]bool{}

func init() {
	for _, code := range strings.Fields("arb cmn-CN cy-GB da-DK de-DE en-AU en-GB en-GB-WLS en-IN en-US es-ES es-MX es-US fr-CA fr-FR is-IS it-IT ja-JP hi-IN ko-KR nb-NO nl-NL pl-PL pt-BR pt-PT ro-RO ru-RU sv-SE tr-TR en-NZ en-ZA ca-ES de-AT yue-CN ar-AE fi-FI en-IE nl-BE fr-BE cs-CZ de-CH en-SG") {
		pollyLanguages[code] = true
	}
}

// plsDocument is the part of a W3C PLS lexicon that the checks read.
type plsDocument struct {
	XMLName  xml.Name
	Alphabet string `xml:"alphabet,attr"`
	Language string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Lexemes  []struct {
		Graphemes []string `xml:"grapheme"`
		Aliases   []string `xml:"alias"`
		Phonemes  []string `xml:"phoneme"`
	} `xml:"lexeme"`
}

// plsLexicon applies the documented PLS lexicon rules before storing it.
func plsLexicon(input map[string]any) error {
	content, _ := input["Content"].(string)
	if utf8.RuneCountInString(content) > 4000 {
		return Failure("LexiconSizeExceededException", "Lexicons are limited to 4,000 characters.")
	}
	var document plsDocument
	if err := xml.Unmarshal([]byte(content), &document); err != nil || document.XMLName.Local != "lexicon" || document.XMLName.Space != "http://www.w3.org/2005/01/pronunciation-lexicon" {
		return Failure("InvalidLexiconException", "Content must be a W3C PLS lexicon document.")
	}
	if document.Alphabet != "ipa" && document.Alphabet != "x-sampa" {
		return Failure("UnsupportedPlsAlphabetException", "The lexicon alphabet must be ipa or x-sampa.")
	}
	if !pollyLanguages[document.Language] {
		return Failure("UnsupportedPlsLanguageException", "The lexicon xml:lang is not an Amazon Polly language code.")
	}
	for _, lexeme := range document.Lexemes {
		if len(lexeme.Graphemes) == 0 || len(lexeme.Aliases)+len(lexeme.Phonemes) == 0 {
			return Failure("InvalidLexiconException", "Every lexeme needs a grapheme and an alias or phoneme.")
		}
		for _, text := range append(append(lexeme.Graphemes, lexeme.Aliases...), lexeme.Phonemes...) {
			if utf8.RuneCountInString(text) > 100 {
				return Failure("MaxLexemeLengthExceededException", "Lexeme graphemes, aliases and phonemes are limited to 100 characters.")
			}
		}
	}
	return nil
}

var rekognition = &Service{
	Model: "rekognition", Title: "Amazon Rekognition", IAMPrefix: "rekognition", Issues: []int{181},
	Resources: []*Resource{
		// Create requests are the only official shapes that carry the
		// caller-chosen collection ID and project name.
		{Kind: "collection", Shape: "CreateCollectionRequest", ID: "name",
			ARN:       "arn:{partition}:rekognition:{region}:{account}:collection/{name}",
			ARNMember: "CollectionArn", NameMember: "CollectionId", Unique: true, Limit: 100,
			Generate: map[string]any{"CollectionARN": "$arn", "FaceModelVersion": "7.0", "FaceCount": 0, "UserCount": 0, "CreationTimestamp": "$now", "StatusCode": 200}},
		{Kind: "project", Shape: "CreateProjectRequest", Summary: "ProjectDescription", ID: "name",
			ARN:       "arn:{partition}:rekognition:{region}:{account}:project/{name}/1",
			ARNMember: "ProjectArn", NameMember: "ProjectName", Unique: true, Limit: 100,
			Generate: map[string]any{"Feature": "CUSTOM_LABELS", "Status": "CREATED", "CreationTimestamp": "$now", "Datasets": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateCollection", Kind: Create, Resource: "collection"},
		{Name: "DescribeCollection", Kind: Read, Resource: "collection", Lookup: "CollectionId"},
		{Name: "DeleteCollection", Kind: Delete, Resource: "collection", Lookup: "CollectionId", Set: map[string]any{"StatusCode": 200}},
		{Name: "CreateProject", Kind: Create, Resource: "project", Check: rekognitionProject},
		{Name: "DescribeProjects", Kind: List, Resource: "project", Names: "ProjectNames"},
		{Name: "DeleteProject", Kind: Delete, Resource: "project", Lookup: "ProjectArn", Set: map[string]any{"Status": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Face collections are stored empty with face model version 7.0: no face or user is indexed, searched or associated, so FaceCount and UserCount stay 0. ListCollections is not implemented because its parallel CollectionIds and FaceModelVersions lists cannot be projected from one stored member.",
		"Projects are stored CREATED with no datasets and no project versions, so no Custom Labels or adapter model is ever trained, started or run; AutoUpdate is accepted only for CONTENT_MODERATION. Project ARNs end in the fixed number 1 instead of a creation timestamp, so re-creating a deleted project name reuses its ARN. DescribeProjects refuses Features.",
		"Every detection, comparison, search, face indexing, video analysis, media analysis job, Face Liveness session, stream processor, dataset, project version and project policy operation returns UnknownOperationException with HTTP 501.",
	},
}

// rekognitionProject applies the documented rule that automatic updates
// apply only to content moderation adapter projects.
func rekognitionProject(input map[string]any) error {
	if _, ok := input["AutoUpdate"]; ok && input["Feature"] != "CONTENT_MODERATION" {
		return Failure("InvalidParameterException", "AutoUpdate applies only to CONTENT_MODERATION projects.")
	}
	return nil
}

var transcribe = &Service{
	Model: "transcribe", Title: "Amazon Transcribe", IAMPrefix: "transcribe", Issues: []int{184},
	Resources: []*Resource{
		// The create requests are the only official shapes holding the
		// stored phrases and words, which the official reads never return.
		{Kind: "vocabulary", Shape: "CreateVocabularyRequest", Summary: "VocabularyInfo", ID: "name",
			ARN:        "arn:{partition}:transcribe:{region}:{account}:vocabulary/{name}",
			NameMember: "VocabularyName", Unique: true, Limit: 100,
			Generate: map[string]any{"VocabularyState": "READY", "LastModifiedTime": "$now"}},
		{Kind: "vocabularyFilter", Shape: "CreateVocabularyFilterRequest", Summary: "VocabularyFilterInfo", ID: "name",
			ARN:        "arn:{partition}:transcribe:{region}:{account}:vocabulary-filter/{name}",
			NameMember: "VocabularyFilterName", Unique: true, Limit: 100,
			Generate: map[string]any{"LastModifiedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateVocabulary", Kind: Create, Resource: "vocabulary", Check: inlineEntries("Phrases")},
		{Name: "GetVocabulary", Kind: Read, Resource: "vocabulary", Lookup: "VocabularyName"},
		{Name: "ListVocabularies", Kind: List, Resource: "vocabulary", Filters: map[string]string{"StateEquals": "VocabularyState"}},
		{Name: "UpdateVocabulary", Kind: Update, Resource: "vocabulary", Lookup: "VocabularyName", Check: inlineEntries("Phrases"),
			Fields: []string{"LanguageCode", "Phrases"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteVocabulary", Kind: Delete, Resource: "vocabulary", Lookup: "VocabularyName"},
		{Name: "CreateVocabularyFilter", Kind: Create, Resource: "vocabularyFilter", Check: inlineEntries("Words")},
		{Name: "GetVocabularyFilter", Kind: Read, Resource: "vocabularyFilter", Lookup: "VocabularyFilterName"},
		{Name: "ListVocabularyFilters", Kind: List, Resource: "vocabularyFilter"},
		{Name: "UpdateVocabularyFilter", Kind: Update, Resource: "vocabularyFilter", Lookup: "VocabularyFilterName", Check: inlineEntries("Words"),
			Fields: []string{"Words"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteVocabularyFilter", Kind: Delete, Resource: "vocabularyFilter", Lookup: "VocabularyFilterName"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Custom vocabularies and vocabulary filters store inline Phrases and Words that are never applied to audio; vocabularies are READY at once instead of passing through PENDING, and no DownloadUri is returned.",
		"VocabularyFileUri, VocabularyFilterFileUri and DataAccessRoleArn are refused because the simulator reads no file from Amazon S3. ListVocabularies filters only by StateEquals; NameContains is refused.",
		"Transcription, medical, call analytics and Medical Scribe jobs, medical vocabularies (which need a file), custom language models (which train) and call analytics categories return UnknownOperationException with HTTP 501.",
	},
}

// inlineEntries requires the inline list member and refuses entries that
// would be read from Amazon S3.
func inlineEntries(member string) func(map[string]any) error {
	return func(input map[string]any) error {
		if err := refuseMembers(input, "BadRequestException", "the simulator reads no file from Amazon S3.", "VocabularyFileUri", "VocabularyFilterFileUri", "DataAccessRoleArn"); err != nil {
			return err
		}
		if _, ok := input[member]; !ok {
			return Failure("BadRequestException", member+" is required because files from Amazon S3 are not read by the simulator.")
		}
		return nil
	}
}

var translate = &Service{
	Model: "translate", Title: "Amazon Translate", IAMPrefix: "translate", Issues: []int{185},
	Resources: []*Resource{
		{Kind: "terminology", Shape: "TerminologyProperties", ID: "name",
			ARN:       "arn:{partition}:translate:{region}:{account}:terminology/{name}/LATEST",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedAt": "$now"}},
		{Kind: "parallelData", Shape: "ParallelDataProperties", ID: "name",
			ARN:       "arn:{partition}:translate:{region}:{account}:parallel-data/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"Status": "CREATING", "CreatedAt": "$now", "LastUpdatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "ImportTerminology", Kind: Put, Resource: "terminology", Lookup: "Name", Check: terminologyFile, Touch: []string{"LastUpdatedAt"}},
		{Name: "GetTerminology", Kind: Read, Resource: "terminology", Lookup: "Name", Accept: []string{"TerminologyDataFormat"}},
		{Name: "ListTerminologies", Kind: List, Resource: "terminology"},
		{Name: "DeleteTerminology", Kind: Delete, Resource: "terminology", Lookup: "Name"},
		{Name: "CreateParallelData", Kind: Create, Resource: "parallelData", Check: parallelDataSource},
		{Name: "GetParallelData", Kind: Read, Resource: "parallelData", Lookup: "Name"},
		{Name: "ListParallelData", Kind: List, Resource: "parallelData"},
		{Name: "DeleteParallelData", Kind: Delete, Resource: "parallelData", Lookup: "Name", Set: map[string]any{"Status": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"ImportTerminology reads the inline CSV or TSV file (UNI directionality only; TMX and MULTI are refused): the header row names the source and target languages and each further row is one term. Only the derived properties are stored, so GetTerminology returns no TerminologyDataLocation, and re-importing replaces only the supplied properties. Terms are never applied to a translation.",
		"Parallel data stays CREATING: the ParallelDataConfig S3 file is never read, so no record is imported and no language codes or counts are reported; UpdateParallelData returns UnknownOperationException with HTTP 501.",
		"TranslateText, TranslateDocument, batch translation jobs and ListLanguages return UnknownOperationException with HTTP 501.",
	},
}

var translateLanguage = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2,4})?$`)

// terminologyFile derives the official terminology properties from the
// inline file and stores them instead of the file itself.
func terminologyFile(input map[string]any) error {
	data, _ := input["TerminologyData"].(map[string]any)
	file, _ := data["File"].([]byte)
	format, _ := data["Format"].(string)
	if format == "TMX" || data["Directionality"] == "MULTI" {
		return Failure("InvalidParameterValueException", "The simulator imports only UNI-directional CSV or TSV terminology files.")
	}
	reader := csv.NewReader(strings.NewReader(string(file)))
	if format == "TSV" {
		reader.Comma = '\t'
	}
	rows, err := reader.ReadAll()
	if err != nil || len(rows) < 2 || len(rows[0]) < 2 {
		return Failure("InvalidParameterValueException", "The terminology file needs a header row of language codes and at least one term row.")
	}
	targets := []any{}
	for i, code := range rows[0] {
		if !translateLanguage.MatchString(strings.TrimSpace(code)) {
			return Failure("InvalidParameterValueException", "The terminology header must list language codes.")
		}
		if i > 0 {
			targets = append(targets, strings.TrimSpace(code))
		}
	}
	delete(input, "TerminologyData")
	delete(input, "MergeStrategy")
	input["SourceLanguageCode"] = strings.TrimSpace(rows[0][0])
	input["TargetLanguageCodes"] = targets
	input["TermCount"] = int64(len(rows) - 1)
	input["SkippedTermCount"] = int64(0)
	input["SizeBytes"] = int64(len(file))
	input["Format"] = format
	input["Directionality"] = "UNI"
	return nil
}

// parallelDataSource requires the S3 location and format of the file,
// which the simulator records without reading.
func parallelDataSource(input map[string]any) error {
	config, _ := input["ParallelDataConfig"].(map[string]any)
	if _, uri := config["S3Uri"]; !uri || config["Format"] == nil {
		return Failure("InvalidParameterValueException", "ParallelDataConfig needs S3Uri and Format.")
	}
	return nil
}

var textract = &Service{
	Model: "textract", Title: "Amazon Textract", IAMPrefix: "textract", Issues: []int{183},
	Resources: []*Resource{
		{Kind: "adapter", Shape: "GetAdapterResponse", Summary: "AdapterOverview", ID: "hex:12",
			ARN:        "arn:{partition}:textract:{region}:{account}:/adapters/{id}",
			NameMember: "AdapterName", IDMember: "AdapterId", Limit: 100,
			Generate: map[string]any{"CreationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateAdapter", Kind: Create, Resource: "adapter"},
		{Name: "GetAdapter", Kind: Read, Resource: "adapter", Lookup: "AdapterId"},
		{Name: "ListAdapters", Kind: List, Resource: "adapter"},
		{Name: "UpdateAdapter", Kind: Update, Resource: "adapter", Lookup: "AdapterId", Fields: []string{"Description", "AdapterName", "AutoUpdate"}},
		{Name: "DeleteAdapter", Kind: Delete, Resource: "adapter", Lookup: "AdapterId"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Adapters are stored configuration with no adapter versions: nothing is trained, evaluated or applied to a document, so CreateAdapterVersion and the other adapter version operations return UnknownOperationException with HTTP 501.",
		"Document text detection, analysis, expense, identity and lending operations and their asynchronous jobs return UnknownOperationException with HTTP 501. ListAdapters refuses creation time filters.",
	},
}

var sageMaker = &Service{
	Model: "sagemaker", Title: "Amazon SageMaker", IAMPrefix: "sagemaker", Issues: []int{59, 161, 182},
	Resources: []*Resource{
		{Kind: "humanTaskUi", Shape: "DescribeHumanTaskUiResponse", Summary: "HumanTaskUiSummary", ID: "name",
			ARN:       "arn:{partition}:sagemaker:{region}:{account}:human-task-ui/{name}",
			ARNMember: "HumanTaskUiArn", NameMember: "HumanTaskUiName", Unique: true, Limit: 100,
			Generate: map[string]any{"HumanTaskUiStatus": "Active", "CreationTime": "$now"}},
		{Kind: "flowDefinition", Shape: "DescribeFlowDefinitionResponse", Summary: "FlowDefinitionSummary", ID: "name",
			ARN:       "arn:{partition}:sagemaker:{region}:{account}:flow-definition/{name}",
			ARNMember: "FlowDefinitionArn", NameMember: "FlowDefinitionName", Unique: true, Limit: 100,
			Generate: map[string]any{"FlowDefinitionStatus": "Active", "CreationTime": "$now"}},
		{Kind: "experiment", Shape: "DescribeExperimentResponse", Summary: "ExperimentSummary", ID: "name",
			ARN:       "arn:{partition}:sagemaker:{region}:{account}:experiment/{name}",
			ARNMember: "ExperimentArn", NameMember: "ExperimentName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreationTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "notebookInstanceLifecycleConfig", Shape: "DescribeNotebookInstanceLifecycleConfigOutput", Summary: "NotebookInstanceLifecycleConfigSummary", ID: "name",
			ARN:       "arn:{partition}:sagemaker:{region}:{account}:notebook-instance-lifecycle-config/{name}",
			ARNMember: "NotebookInstanceLifecycleConfigArn", NameMember: "NotebookInstanceLifecycleConfigName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreationTime": "$now", "LastModifiedTime": "$now"}},
		{Kind: "codeRepository", Shape: "DescribeCodeRepositoryOutput", Summary: "CodeRepositorySummary", ID: "name",
			ARN:       "arn:{partition}:sagemaker:{region}:{account}:code-repository/{name}",
			ARNMember: "CodeRepositoryArn", NameMember: "CodeRepositoryName", Unique: true, Limit: 100,
			Generate: map[string]any{"CreationTime": "$now", "LastModifiedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateHumanTaskUi", Kind: Create, Resource: "humanTaskUi", Check: humanTaskTemplate},
		{Name: "DescribeHumanTaskUi", Kind: Read, Resource: "humanTaskUi", Lookup: "HumanTaskUiName"},
		{Name: "ListHumanTaskUis", Kind: List, Resource: "humanTaskUi"},
		{Name: "DeleteHumanTaskUi", Kind: Delete, Resource: "humanTaskUi", Lookup: "HumanTaskUiName"},
		{Name: "CreateFlowDefinition", Kind: Create, Resource: "flowDefinition", Check: flowDefinitionSource},
		{Name: "DescribeFlowDefinition", Kind: Read, Resource: "flowDefinition", Lookup: "FlowDefinitionName"},
		{Name: "ListFlowDefinitions", Kind: List, Resource: "flowDefinition"},
		{Name: "DeleteFlowDefinition", Kind: Delete, Resource: "flowDefinition", Lookup: "FlowDefinitionName"},
		{Name: "CreateExperiment", Kind: Create, Resource: "experiment", Check: lowercaseName("ExperimentName")},
		{Name: "DescribeExperiment", Kind: Read, Resource: "experiment", Lookup: "ExperimentName"},
		{Name: "ListExperiments", Kind: List, Resource: "experiment"},
		{Name: "UpdateExperiment", Kind: Update, Resource: "experiment", Lookup: "ExperimentName", Fields: []string{"DisplayName", "Description"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteExperiment", Kind: Delete, Resource: "experiment", Lookup: "ExperimentName"},
		{Name: "CreateNotebookInstanceLifecycleConfig", Kind: Create, Resource: "notebookInstanceLifecycleConfig", Check: lifecycleScripts},
		{Name: "DescribeNotebookInstanceLifecycleConfig", Kind: Read, Resource: "notebookInstanceLifecycleConfig", Lookup: "NotebookInstanceLifecycleConfigName"},
		{Name: "ListNotebookInstanceLifecycleConfigs", Kind: List, Resource: "notebookInstanceLifecycleConfig"},
		{Name: "UpdateNotebookInstanceLifecycleConfig", Kind: Update, Resource: "notebookInstanceLifecycleConfig", Lookup: "NotebookInstanceLifecycleConfigName",
			Check: lifecycleScripts, Fields: []string{"OnCreate", "OnStart"}, Touch: []string{"LastModifiedTime"}},
		{Name: "DeleteNotebookInstanceLifecycleConfig", Kind: Delete, Resource: "notebookInstanceLifecycleConfig", Lookup: "NotebookInstanceLifecycleConfigName"},
		{Name: "CreateCodeRepository", Kind: Create, Resource: "codeRepository", Check: lowercaseName("CodeRepositoryName")},
		{Name: "DescribeCodeRepository", Kind: Read, Resource: "codeRepository", Lookup: "CodeRepositoryName"},
		{Name: "ListCodeRepositories", Kind: List, Resource: "codeRepository"},
		{Name: "DeleteCodeRepository", Kind: Delete, Resource: "codeRepository", Lookup: "CodeRepositoryName"},
		{Name: "AddTags", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "DeleteTags", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTags", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Augmented AI worker task templates and human review workflows (flow definitions) are stored Active configuration: no human loop is started, no task reaches a work team, no output is written to Amazon S3, and the referenced work team, task UI, role and KMS key are not checked. A task template returns only its ContentSha256, not a template Url.",
		"Experiments, notebook instance lifecycle configurations and Git code repositories are stored metadata: no trial or run is recorded, no notebook instance runs a lifecycle script (OnCreate and OnStart Content must be base64) and no repository is cloned or its secret read. Names must be lowercase so ARNs keep them unchanged.",
		"List filters, sorting and name filters are refused, UpdateCodeRepository is not implemented, AddTags returns no Tags member, and missing resources whose operations model no not-found exception return ResourceNotFoundException with HTTP 400.",
		"Training, processing, transform, tuning, labeling, inference and every other job, models, endpoints and endpoint configurations, notebook instances, Studio domains and apps, pipelines, feature groups, model registry, work teams and workforces return UnknownOperationException with HTTP 501.",
	},
}

// humanTaskTemplate stores the template digest that DescribeHumanTaskUi
// returns instead of the Liquid template content itself.
func humanTaskTemplate(input map[string]any) error {
	template, _ := input["UiTemplate"].(map[string]any)
	content, _ := template["Content"].(string)
	digest := sha256.Sum256([]byte(content))
	input["UiTemplate"] = map[string]any{"ContentSha256": hex.EncodeToString(digest[:])}
	return nil
}

// flowDefinitionSource applies the documented rule that activation
// conditions belong to an AWS managed human loop request source.
func flowDefinitionSource(input map[string]any) error {
	_, activation := input["HumanLoopActivationConfig"]
	_, source := input["HumanLoopRequestSource"]
	if activation && !source {
		return Failure("ValidationException", "HumanLoopActivationConfig needs an AWS managed HumanLoopRequestSource.")
	}
	return nil
}

// lowercaseName refuses uppercase letters, which SageMaker lowercases in
// ARNs; the simulator keeps names and ARNs identical.
func lowercaseName(member string) func(map[string]any) error {
	return func(input map[string]any) error {
		if name, _ := input[member].(string); name != strings.ToLower(name) {
			return Failure("ValidationException", member+" must be lowercase in the simulator so the ARN keeps the name unchanged.")
		}
		return nil
	}
}

// lifecycleScripts requires lowercase names and base64 script content, as
// notebook instance lifecycle hooks are documented to carry.
func lifecycleScripts(input map[string]any) error {
	if err := lowercaseName("NotebookInstanceLifecycleConfigName")(input); err != nil {
		return err
	}
	for _, hook := range []string{"OnCreate", "OnStart"} {
		scripts, _ := input[hook].([]any)
		for _, script := range scripts {
			content, _ := script.(map[string]any)["Content"].(string)
			if _, err := base64.StdEncoding.DecodeString(content); err != nil {
				return Failure("ValidationException", hook+" Content must be a base64-encoded script.")
			}
		}
	}
	return nil
}
