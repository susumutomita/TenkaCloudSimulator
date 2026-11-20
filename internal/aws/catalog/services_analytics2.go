package catalog

// Reviewed bounded specifications for analytics services whose bound
// resources are stored configuration only: Amazon AppFlow, AWS Clean Rooms,
// Amazon CloudSearch, Amazon DataZone, Amazon EMR, AWS Lake Formation and
// Amazon Quick. Nothing that runs a flow, a query, a job or a cluster, and
// nothing that holds a third-party credential, is bound.

// appFlow serves flows between Amazon S3 locations as stored definitions.
var appFlow = &Service{
	Model: "appflow", Title: "Amazon AppFlow", IAMPrefix: "appflow", Issues: []int{60},
	Resources: []*Resource{
		{Kind: "flow", Shape: "DescribeFlowResponse", Summary: "FlowDefinition", ID: "name",
			ARN:       "arn:{partition}:appflow:{region}:{account}:flow/{name}",
			ARNMember: "flowArn", NameMember: "flowName", Unique: true, Limit: 100,
			Generate: map[string]any{"flowStatus": "Active", "createdAt": "$now", "lastUpdatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateFlow", Kind: Create, Resource: "flow", Check: appFlowDefinition},
		{Name: "DescribeFlow", Kind: Read, Resource: "flow", Lookup: "flowName"},
		{Name: "ListFlows", Kind: List, Resource: "flow"},
		{Name: "UpdateFlow", Kind: Update, Resource: "flow", Lookup: "flowName", Touch: []string{"lastUpdatedAt"}, Accept: []string{"clientToken"}, Check: appFlowDefinition,
			Fields: []string{"description", "triggerConfig", "sourceFlowConfig", "destinationFlowConfigList", "tasks", "metadataCatalogConfig"}},
		{Name: "DeleteFlow", Kind: Delete, Resource: "flow", Lookup: "flowName", Accept: []string{"forceDelete"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Flows are stored definitions: nothing is ever transferred, so no flow runs, no execution or run history exists, lastRunExecutionDetails is never reported and a flow is Active at once. StartFlow, StopFlow, CancelFlowExecutions, DescribeFlowExecutionRecords, RegisterConnector and every other official operation return UnknownOperationException with HTTP 501.",
		"Connector profiles hold third-party credentials and are not implemented, so no connector profile name is accepted: the source and every destination must be an Amazon S3 location (connectorType S3) without connectorProfileName, exactly one destination is allowed, and an Event trigger is refused because no event source exists. Tasks, bucket names, schedules and the KMS key are stored without being resolved or checked against Amazon S3.",
		"UpdateFlow replaces the supplied members and keeps those it omits, including description and metadataCatalogConfig, and a flow is deleted whether or not forceDelete is sent. ListFlows summaries carry no sourceConnectorType, destinationConnectorType or triggerType, because the stored model has no member to hold them. Tags are stored from CreateFlow and by TagResource on the flow ARN. The limit of 100 flows per Region is local.",
	},
}

// appFlowDefinition keeps flows between Amazon S3 locations, the only
// connector that needs no connector profile, and refuses what could not run.
func appFlowDefinition(input map[string]any) error {
	if source, ok := input["sourceFlowConfig"].(map[string]any); ok {
		if err := appFlowEndpoint(source, "source"); err != nil {
			return err
		}
	}
	if destinations, ok := input["destinationFlowConfigList"].([]any); ok {
		if len(destinations) != 1 {
			return Failure("ValidationException", "A flow transfers data to exactly one destination.")
		}
		destination, _ := destinations[0].(map[string]any)
		if err := appFlowEndpoint(destination, "destination"); err != nil {
			return err
		}
	}
	if trigger, ok := input["triggerConfig"].(map[string]any); ok && trigger["triggerType"] == "Event" {
		return Failure("ValidationException", "Event triggers need a connector that emits events, and none is supported.")
	}
	return nil
}

func appFlowEndpoint(config map[string]any, role string) error {
	if config["connectorType"] != "S3" {
		return Failure("ValidationException", "The "+role+" must be an Amazon S3 location (connectorType S3): other connectors need a connector profile with credentials.")
	}
	if _, ok := config["connectorProfileName"]; ok {
		return Failure("ValidationException", "Connector profiles are not supported; leave connectorProfileName out for an Amazon S3 "+role+".")
	}
	return nil
}

// cleanRooms serves configured tables and collaborations as stored records.
var cleanRooms = &Service{
	Model: "cleanrooms", Title: "AWS Clean Rooms", IAMPrefix: "cleanrooms", Issues: []int{62},
	Resources: []*Resource{
		{Kind: "configuredTable", Shape: "ConfiguredTable", Summary: "ConfiguredTableSummary", ID: "uuid",
			ARN:       "arn:{partition}:cleanrooms:{region}:{account}:configuredtable/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 100,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "analysisRuleTypes": []any{}}},
		{Kind: "collaboration", Shape: "Collaboration", Summary: "CollaborationSummary", ID: "uuid",
			ARN:       "arn:{partition}:cleanrooms:{region}:{account}:collaboration/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 50,
			Aliases:  map[string]string{"autoApprovedChangeRequestTypes": "autoApprovedChangeTypes"},
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "creatorAccountId": "$account", "memberStatus": "ACTIVE"}},
	},
	Ops: []Op{
		{Name: "CreateConfiguredTable", Kind: Create, Resource: "configuredTable", Check: cleanRoomsTable},
		{Name: "GetConfiguredTable", Kind: Read, Resource: "configuredTable", Lookup: "configuredTableIdentifier"},
		{Name: "ListConfiguredTables", Kind: List, Resource: "configuredTable"},
		{Name: "UpdateConfiguredTable", Kind: Update, Resource: "configuredTable", Lookup: "configuredTableIdentifier", Touch: []string{"updateTime"}, Check: cleanRoomsTable,
			Fields: []string{"name", "description", "tableReference", "allowedColumns", "analysisMethod", "selectedAnalysisMethods"}},
		{Name: "DeleteConfiguredTable", Kind: Delete, Resource: "configuredTable", Lookup: "configuredTableIdentifier"},
		{Name: "CreateCollaboration", Kind: Create, Resource: "collaboration", Check: cleanRoomsCollaboration},
		{Name: "GetCollaboration", Kind: Read, Resource: "collaboration", Lookup: "collaborationIdentifier"},
		{Name: "ListCollaborations", Kind: List, Resource: "collaboration", Filters: map[string]string{"memberStatus": "memberStatus"}},
		{Name: "UpdateCollaboration", Kind: Update, Resource: "collaboration", Lookup: "collaborationIdentifier", Touch: []string{"updateTime"},
			Fields: []string{"name", "description", "analyticsEngine"}},
		{Name: "DeleteCollaboration", Kind: Delete, Resource: "collaboration", Lookup: "collaborationIdentifier"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Configured tables and collaborations are stored records: no data is read from the referenced table, no query or job is run, no result is produced, and no other account is invited. StartProtectedQuery, StartProtectedJob, PopulateIdMappingTable, PreviewPrivacyImpact, schemas and every other official operation return UnknownOperationException with HTTP 501.",
		"Memberships, members, analysis templates, privacy budget templates, configured table associations and analysis rules are not implemented. A membership is created by the invited account and the creator's own membership appears with the collaboration, which a one-account simulator cannot model, and an analysis rule would have to appear in the analysisRuleTypes of its table. A configured table therefore always reports an empty analysisRuleTypes list and a collaboration reports no membershipId or membershipArn.",
		"A collaboration has only its creator: members must be an empty list, so no invitation is sent and the member status is always ACTIVE. creatorMemberAbilities is validated as a list of the documented abilities and then dropped because nothing in the stored model can use it, and creatorMLMemberAbilities and creatorPaymentConfiguration are refused.",
		"A configured table points at an AWS Glue or Amazon Athena table by name; the reference is not resolved and Snowflake references are refused because they name a secret. selectedAnalysisMethods, when given, must list at least two methods as documented. Deleting a configured table does not check collaborations. Tags are stored from the create operations and by TagResource on the ARN. The limits of 100 configured tables and 50 collaborations per Region are local. GetCollaboration, UpdateCollaboration and DeleteCollaboration model no not-found exception, so an unknown collaboration fails with ValidationException.",
	},
}

func cleanRoomsTable(input map[string]any) error {
	if reference, ok := input["tableReference"].(map[string]any); ok {
		if _, snowflake := reference["snowflake"]; snowflake {
			return Failure("ValidationException", "Snowflake table references name a secret and are not supported.")
		}
	}
	if methods, ok := input["selectedAnalysisMethods"].([]any); ok && len(methods) < 2 {
		return Failure("ValidationException", "When selectedAnalysisMethods is configured, specify at least two analysis methods.")
	}
	return nil
}

func cleanRoomsCollaboration(input map[string]any) error {
	if members, _ := input["members"].([]any); len(members) > 0 {
		return Failure("ValidationException", "Inviting other accounts is not supported: members must be an empty list.")
	}
	for _, member := range []string{"creatorMLMemberAbilities", "creatorPaymentConfiguration"} {
		if _, ok := input[member]; ok {
			return Failure("ValidationException", member+" is not supported by the simulator.")
		}
	}
	delete(input, "members")
	delete(input, "creatorMemberAbilities")
	return nil
}

// cleanRoomsML serves training datasets and configured model algorithms as
// stored records.
var cleanRoomsML = &Service{
	Model: "cleanroomsml", Title: "AWS Clean Rooms ML", IAMPrefix: "cleanrooms-ml", Issues: []int{62},
	Resources: []*Resource{
		{Kind: "trainingDataset", Shape: "GetTrainingDatasetResponse", Summary: "TrainingDatasetSummary", ID: "uuid",
			ARN:       "arn:{partition}:cleanrooms-ml:{region}:{account}:training-dataset/{id}",
			ARNMember: "trainingDatasetArn", NameMember: "name", Limit: 50,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now", "status": "ACTIVE"}},
		{Kind: "configuredModelAlgorithm", Shape: "GetConfiguredModelAlgorithmResponse", Summary: "ConfiguredModelAlgorithmSummary", ID: "uuid",
			ARN:       "arn:{partition}:cleanrooms-ml:{region}:{account}:configured-model-algorithm/{id}",
			ARNMember: "configuredModelAlgorithmArn", NameMember: "name", Limit: 50,
			Generate: map[string]any{"createTime": "$now", "updateTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateTrainingDataset", Kind: Create, Resource: "trainingDataset"},
		{Name: "GetTrainingDataset", Kind: Read, Resource: "trainingDataset", Lookup: "trainingDatasetArn"},
		{Name: "ListTrainingDatasets", Kind: List, Resource: "trainingDataset"},
		{Name: "DeleteTrainingDataset", Kind: Delete, Resource: "trainingDataset", Lookup: "trainingDatasetArn"},
		{Name: "CreateConfiguredModelAlgorithm", Kind: Create, Resource: "configuredModelAlgorithm"},
		{Name: "GetConfiguredModelAlgorithm", Kind: Read, Resource: "configuredModelAlgorithm", Lookup: "configuredModelAlgorithmArn"},
		{Name: "ListConfiguredModelAlgorithms", Kind: List, Resource: "configuredModelAlgorithm"},
		{Name: "DeleteConfiguredModelAlgorithm", Kind: Delete, Resource: "configuredModelAlgorithm", Lookup: "configuredModelAlgorithmArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Training datasets and configured model algorithms are stored records: no data is read, no container image is pulled or run, no model is trained, no audience is generated and no inference runs. CreateTrainedModel, CreateAudienceModel, StartTrainedModelInferenceJob, StartAudienceGenerationJob, ML configurations, input channels and every other official operation return UnknownOperationException with HTTP 501.",
		"The IAM role, the AWS Glue references, the container image URI and the KMS key are stored without being resolved, assumed or checked, a training dataset is ACTIVE at once and the service offers no update operation for either resource. The limit of 50 of each kind per Region is local, and tags are stored from the create operations and by TagResource on the ARN.",
	},
}

// cloudSearch serves search domains over the Query protocol as stored
// records. The data plane (cloudsearchdomain) is not claimed.
var cloudSearch = &Service{
	Model: "cloudsearch", Title: "Amazon CloudSearch", IAMPrefix: "cloudsearch", Issues: []int{63},
	Resources: []*Resource{
		{Kind: "domain", Shape: "DomainStatus", ID: "name",
			ARN:       "arn:{partition}:cloudsearch:{region}:{account}:domain/{name}",
			ARNMember: "ARN", NameMember: "DomainName", Unique: true, Limit: 100,
			Generate: map[string]any{"DomainId": "$fmt:{account}/{name}", "Created": true, "Deleted": false, "Processing": false, "RequiresIndexDocuments": false}},
	},
	Ops: []Op{
		{Name: "CreateDomain", Kind: Create, Resource: "domain"},
		{Name: "DescribeDomains", Kind: List, Resource: "domain", Names: "DomainNames"},
		{Name: "DeleteDomain", Kind: Delete, Resource: "domain", Lookup: "DomainName", Set: map[string]any{"Deleted": true}},
	},
	Bounds: []string{
		"Search domains are stored records: no search instance, index, document or search exists, no DocService or SearchService endpoint is ever returned, and the instance type, partition count and replication limits are never reported. Every other official operation returns UnknownOperationException with HTTP 501, including all index field, expression, suggester, analysis scheme, access policy, scaling, availability and endpoint option APIs, IndexDocuments, BuildSuggesters and ListDomainNames.",
		"A domain is Created at once, never Processing and never RequiresIndexDocuments; DomainId is the account ID and the name joined by a slash as AWS reports it. DeleteDomain returns the domain with Deleted true and removes it, so DescribeDomains no longer lists it, whereas AWS keeps it listed for several minutes. CloudSearch has no tagging API, so no tags are stored.",
		"DescribeDomains with DomainNames fails for a name that does not exist, with InvalidParameterValue because the official operation models no not-found exception, instead of omitting it. DeleteDomain of an unknown name fails the same way. The limit of 100 domains per Region is local.",
	},
}

// dataZone serves domains and projects as stored records.
var dataZone = &Service{
	Model: "datazone", Title: "Amazon DataZone", IAMPrefix: "datazone", Issues: []int{67},
	Resources: []*Resource{
		{Kind: "domain", Shape: "GetDomainOutput", Summary: "DomainSummary", ID: "alnum:14:dzd_",
			ARN:       "arn:{partition}:datazone:{region}:{account}:domain/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Unique: true, Limit: 10,
			Generate: map[string]any{"status": "AVAILABLE", "createdAt": "$now", "lastUpdatedAt": "$now", "managedAccountId": "$account", "domainVersion": "V1"}},
		{Kind: "project", Shape: "GetProjectOutput", Summary: "ProjectSummary", ID: "alnum:14", Parent: "domain", ParentMember: "domainId",
			ARN:        "arn:{partition}:datazone:{region}:{account}:domain/{parent}/project/{id}",
			NameMember: "name", IDMember: "id", Unique: true, Limit: 100,
			Generate: map[string]any{"domainId": "$parentId", "createdBy": "simulator", "projectStatus": "ACTIVE", "createdAt": "$now", "lastUpdatedAt": "$now", "updatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateDomain", Kind: Create, Resource: "domain", Check: dataZoneDomain},
		{Name: "GetDomain", Kind: Read, Resource: "domain", Lookup: "identifier"},
		{Name: "ListDomains", Kind: List, Resource: "domain", Filters: map[string]string{"status": "status"}},
		{Name: "UpdateDomain", Kind: Update, Resource: "domain", Lookup: "identifier", Touch: []string{"lastUpdatedAt"}, Accept: []string{"clientToken"}, Check: dataZoneDomain,
			Fields: []string{"description", "domainExecutionRole", "name", "serviceRole", "singleSignOn"}},
		{Name: "DeleteDomain", Kind: Delete, Resource: "domain", Lookup: "identifier", Accept: []string{"clientToken", "skipDeletionCheck"}, Set: map[string]any{"status": "DELETING"}},
		{Name: "CreateProject", Kind: Create, Resource: "project", Parent: "domainIdentifier", Check: dataZoneProject},
		{Name: "GetProject", Kind: Read, Resource: "project", Parent: "domainIdentifier", Lookup: "identifier"},
		{Name: "ListProjects", Kind: List, Resource: "project", Parent: "domainIdentifier", Filters: map[string]string{"name": "name"}},
		{Name: "UpdateProject", Kind: Update, Resource: "project", Parent: "domainIdentifier", Lookup: "identifier", Touch: []string{"lastUpdatedAt", "updatedAt"}, Check: dataZoneProject,
			Fields: []string{"description", "name"}},
		{Name: "DeleteProject", Kind: Delete, Resource: "project", Parent: "domainIdentifier", Lookup: "identifier", Accept: []string{"skipDeletionCheck"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Domains and projects are stored records: no data portal, catalog, environment, data source, asset, subscription, connection or run exists, no portalUrl or rootDomainUnitId is returned, and every other official operation returns UnknownOperationException with HTTP 501. A domain is AVAILABLE at once, DeleteDomain returns the domain with status DELETING and removes it, and a project is ACTIVE at once.",
		"Business glossaries and glossary terms are not implemented: a glossary names its owning project and a term its glossary, and the stored model cannot check either reference or keep it from dangling when the project is deleted. Domain units, project profiles, form types, asset types and environments are not implemented either.",
		"Domains use no IAM Identity Center: singleSignOn with type IAM_IDC is refused. The execution role, service role and KMS key are stored without being resolved and nothing is encrypted. A domain with projects cannot be deleted even when skipDeletionCheck is sent, which is otherwise accepted without effect, and a project is deleted whether or not skipDeletionCheck is sent.",
		"Projects accept only a name and a description. domainUnitId, projectProfileId, glossaryTerms, resourceTags, userParameters and environmentDeploymentDetails refer to units, profiles, terms or environments that do not exist and are refused; project names are unique within a domain, createdBy is always simulator, and the project ARN used for authorization (domain/<domain id>/project/<project id>) is a simulator form because projects have no ARN. ListProjects accepts only the name filter, and the limits of 10 domains per Region and 100 projects per domain are local. Tags apply to domains by ARN.",
	},
}

// dataZoneDomain refuses identity-provider integration the simulator lacks.
func dataZoneDomain(input map[string]any) error {
	if sso, ok := input["singleSignOn"].(map[string]any); ok && sso["type"] == "IAM_IDC" {
		return Failure("ValidationException", "IAM Identity Center sign-in is not supported; use type DISABLED or leave singleSignOn out.")
	}
	return nil
}

// dataZoneProject keeps projects free of references to resources that do
// not exist in the stored model.
func dataZoneProject(input map[string]any) error {
	for _, member := range []string{"domainUnitId", "projectProfileId", "projectProfileVersion", "glossaryTerms", "resourceTags", "userParameters", "environmentDeploymentDetails"} {
		if _, ok := input[member]; ok {
			return Failure("ValidationException", member+" is not supported by the simulator.")
		}
	}
	return nil
}
