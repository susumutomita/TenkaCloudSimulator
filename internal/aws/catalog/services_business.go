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
