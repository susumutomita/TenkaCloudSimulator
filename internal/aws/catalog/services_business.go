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
