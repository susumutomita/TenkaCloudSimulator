package catalog

import (
	"encoding/json"
	"regexp"
	"time"
)

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
		if err := appFlowEndpoint(source, "source", "sourceConnectorProperties"); err != nil {
			return err
		}
	}
	if destinations, ok := input["destinationFlowConfigList"].([]any); ok {
		if len(destinations) != 1 {
			return Failure("ValidationException", "A flow transfers data to exactly one destination.")
		}
		destination, _ := destinations[0].(map[string]any)
		if err := appFlowEndpoint(destination, "destination", "destinationConnectorProperties"); err != nil {
			return err
		}
	}
	if trigger, ok := input["triggerConfig"].(map[string]any); ok && trigger["triggerType"] == "Event" {
		return Failure("ValidationException", "Event triggers need a connector that emits events, and none is supported.")
	}
	return nil
}

func appFlowEndpoint(config map[string]any, role, propertiesMember string) error {
	if config["connectorType"] != "S3" {
		return Failure("ValidationException", "The "+role+" must be an Amazon S3 location (connectorType S3): other connectors need a connector profile with credentials.")
	}
	if _, ok := config["connectorProfileName"]; ok {
		return Failure("ValidationException", "Connector profiles are not supported; leave connectorProfileName out for an Amazon S3 "+role+".")
	}
	properties, _ := config[propertiesMember].(map[string]any)
	if _, ok := properties["S3"].(map[string]any); len(properties) != 1 || !ok {
		return Failure("ValidationException", "The "+role+" connector properties must contain only the S3 configuration.")
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

// emr serves security configurations and the account's block public access
// setting. No cluster, step or notebook is bound.
var emr = &Service{
	Model: "emr", Title: "Amazon EMR", IAMPrefix: "elasticmapreduce", Issues: []int{68},
	Resources: []*Resource{
		{Kind: "securityConfiguration", Shape: "DescribeSecurityConfigurationOutput", Summary: "SecurityConfigurationSummary", ID: "name",
			ARN:        "arn:{partition}:elasticmapreduce:{region}:{account}:securityconfiguration/{name}",
			NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreationDateTime": "$now"}},
		{Kind: "blockPublicAccess", Shape: "GetBlockPublicAccessConfigurationOutput", Singleton: true, ID: "name",
			ARN: "arn:{partition}:elasticmapreduce:{region}:{account}:blockpublicaccessconfiguration", NameMember: "BlockPublicAccessConfiguration",
			Generate: map[string]any{
				"BlockPublicAccessConfiguration": map[string]any{"BlockPublicSecurityGroupRules": true,
					"PermittedPublicSecurityGroupRuleRanges": []any{map[string]any{"MinRange": 22, "MaxRange": 22}}},
				"BlockPublicAccessConfigurationMetadata": map[string]any{"CreationDateTime": "1970-01-01T00:00:00Z", "CreatedByArn": "arn:aws:iam::000000000000:root"}}},
	},
	Ops: []Op{
		{Name: "CreateSecurityConfiguration", Kind: Create, Resource: "securityConfiguration", Check: emrSecurityConfiguration},
		{Name: "DescribeSecurityConfiguration", Kind: Read, Resource: "securityConfiguration", Lookup: "Name"},
		{Name: "ListSecurityConfigurations", Kind: List, Resource: "securityConfiguration"},
		{Name: "DeleteSecurityConfiguration", Kind: Delete, Resource: "securityConfiguration", Lookup: "Name"},
		{Name: "PutBlockPublicAccessConfiguration", Kind: Put, Resource: "blockPublicAccess", Check: emrBlockPublicAccess},
		{Name: "GetBlockPublicAccessConfiguration", Kind: Read, Resource: "blockPublicAccess"},
	},
	Bounds: []string{
		"Security configurations and the Regional block public access setting are stored records: no cluster, instance group, step, notebook, studio or session exists, nothing is launched and no security group rule is checked or changed. RunJobFlow and every other official operation return UnknownOperationException with HTTP 501, including Studio operations because a Studio returns a web address.",
		"A security configuration is a JSON document that must parse as an object; its contents are stored without being checked against EMR's encryption, authentication and authorization schema, and no certificate, key or role it names is resolved. It cannot be updated and has no tags, and its ARN is a simulator form used only for authorization, because AWS gives security configurations none.",
		"The block public access setting defaults to blocking public security group rules with port 22 permitted, as AWS does. After PutBlockPublicAccessConfiguration, the metadata reports the time of the change and the account root ARN as the creator because the simulator does not authenticate callers, and the default reports a creation time of 0. Port ranges must have MinRange not above MaxRange and must not repeat. The limit of 100 security configurations per Region is local.",
	},
}

// emrSecurityConfiguration requires the document to be a JSON object.
func emrSecurityConfiguration(input map[string]any) error {
	document, _ := input["SecurityConfiguration"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(document), &parsed); err != nil || parsed == nil {
		return Failure("InvalidRequestException", "SecurityConfiguration must be a JSON object.")
	}
	return nil
}

// emrBlockPublicAccess validates the permitted port ranges and records the
// change in the metadata AWS reports.
func emrBlockPublicAccess(input map[string]any) error {
	config, _ := input["BlockPublicAccessConfiguration"].(map[string]any)
	ranges, _ := config["PermittedPublicSecurityGroupRuleRanges"].([]any)
	seen := map[[2]int64]bool{}
	for _, value := range ranges {
		entry, _ := value.(map[string]any)
		low, _ := entry["MinRange"].(int64)
		high := low
		if upper, ok := entry["MaxRange"].(int64); ok {
			high = upper
		}
		if low > high || seen[[2]int64{low, high}] {
			return Failure("InvalidRequestException", "Port ranges need MinRange not above MaxRange and must not repeat.")
		}
		seen[[2]int64{low, high}] = true
	}
	input["BlockPublicAccessConfigurationMetadata"] = map[string]any{
		"CreationDateTime": time.Now().UTC().Format(time.RFC3339Nano), "CreatedByArn": "arn:aws:iam::000000000000:root"}
	return nil
}

// emrServerless serves applications as stored configuration.
var emrServerless = &Service{
	Model: "emr-serverless", Title: "Amazon EMR Serverless", IAMPrefix: "emr-serverless", Issues: []int{68},
	Resources: []*Resource{
		{Kind: "application", Shape: "Application", Summary: "ApplicationSummary", ID: "alnum:16",
			ARN:       "arn:{partition}:emr-serverless:{region}:{account}:/applications/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "applicationId", Limit: 50,
			Generate: map[string]any{"id": "$id", "state": "CREATED", "createdAt": "$now", "updatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateApplication", Kind: Create, Resource: "application"},
		{Name: "GetApplication", Kind: Read, Resource: "application", Lookup: "applicationId"},
		{Name: "ListApplications", Kind: List, Resource: "application"},
		{Name: "UpdateApplication", Kind: Update, Resource: "application", Lookup: "applicationId", Touch: []string{"updatedAt"}, Accept: []string{"clientToken"},
			Fields: []string{"initialCapacity", "maximumCapacity", "autoStartConfiguration", "autoStopConfiguration", "networkConfiguration", "architecture",
				"imageConfiguration", "workerTypeSpecifications", "interactiveConfiguration", "releaseLabel", "runtimeConfiguration", "monitoringConfiguration",
				"schedulerConfiguration", "identityCenterConfiguration"}},
		{Name: "DeleteApplication", Kind: Delete, Resource: "application", Lookup: "applicationId"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Applications are stored records: no capacity is provisioned, no Spark or Hive driver exists and no job runs. An application stays CREATED, never starts or stops, and has no stateDetails. StartApplication, StopApplication, StartJobRun, CancelJobRun, job run listings, GetDashboardForJobRun and every other official operation return UnknownOperationException with HTTP 501.",
		"The release label, network configuration, image, runtime configuration, monitoring settings, KMS key and IAM Identity Center application are stored without being resolved or checked, and the application type is stored as given. UpdateApplication replaces the supplied members and keeps the rest, and an application is deleted in any state. The limit of 50 applications per Region is local, and tags are stored from CreateApplication and by TagResource on the ARN.",
	},
}

// emrContainers serves virtual clusters, job templates and security
// configurations as stored records.
var emrContainers = &Service{
	Model: "emr-containers", Title: "Amazon EMR on EKS", IAMPrefix: "emr-containers", Issues: []int{68},
	Resources: []*Resource{
		{Kind: "virtualCluster", Shape: "VirtualCluster", ID: "alnum:25",
			ARN:       "arn:{partition}:emr-containers:{region}:{account}:/virtualclusters/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 50,
			Generate: map[string]any{"state": "RUNNING", "createdAt": "$now"}},
		{Kind: "jobTemplate", Shape: "JobTemplate", ID: "alnum:25",
			ARN:       "arn:{partition}:emr-containers:{region}:{account}:/jobtemplates/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 100,
			Generate: map[string]any{"createdAt": "$now"}},
		{Kind: "securityConfiguration", Shape: "SecurityConfiguration", ID: "alnum:25",
			ARN:       "arn:{partition}:emr-containers:{region}:{account}:/securityconfigurations/{id}",
			ARNMember: "arn", NameMember: "name", IDMember: "id", Limit: 50,
			Generate: map[string]any{"createdAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateVirtualCluster", Kind: Create, Resource: "virtualCluster"},
		{Name: "DescribeVirtualCluster", Kind: Read, Resource: "virtualCluster", Lookup: "id"},
		{Name: "ListVirtualClusters", Kind: List, Resource: "virtualCluster"},
		{Name: "DeleteVirtualCluster", Kind: Delete, Resource: "virtualCluster", Lookup: "id"},
		{Name: "CreateJobTemplate", Kind: Create, Resource: "jobTemplate"},
		{Name: "DescribeJobTemplate", Kind: Read, Resource: "jobTemplate", Lookup: "id"},
		{Name: "ListJobTemplates", Kind: List, Resource: "jobTemplate"},
		{Name: "DeleteJobTemplate", Kind: Delete, Resource: "jobTemplate", Lookup: "id"},
		{Name: "CreateSecurityConfiguration", Kind: Create, Resource: "securityConfiguration"},
		{Name: "DescribeSecurityConfiguration", Kind: Read, Resource: "securityConfiguration", Lookup: "id"},
		{Name: "ListSecurityConfigurations", Kind: List, Resource: "securityConfiguration"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Virtual clusters, job templates and security configurations are stored records: the EKS cluster and namespace a virtual cluster names are not contacted or checked, no job runs, and no managed endpoint exists. StartJobRun, CancelJobRun, DescribeJobRun, ListJobRuns, managed endpoint operations, GetManagedEndpointSessionCredentials and every other official operation return UnknownOperationException with HTTP 501.",
		"A virtual cluster is RUNNING at once and DeleteVirtualCluster removes it, whereas AWS keeps it listed as TERMINATED; ListVirtualClusters offers no filters: containerProviderId, containerProviderType and the other official filters are refused with ValidationException rather than answered with an empty list, because the stored record keeps the container provider nested and the filters could not match it. securityConfigurationId on a virtual cluster, the execution role, KMS key and everything inside jobTemplateData and securityConfigurationData are stored without being resolved, so a virtual cluster may name a missing security configuration, and createdBy and decryptionError are never reported.",
		"The service offers no operation to delete a security configuration, so one lasts until the Region's records are reset, and a job template is deleted whether or not a job uses it. The limits of 50 virtual clusters, 100 job templates and 50 security configurations per Region are local, and tags are stored from the create operations and by TagResource on the ARN.",
	},
}

// lakeFormation serves LF-tags, LF-tag expressions, data lake settings and
// registered locations as stored configuration. Permissions are not
// bound, so nothing here changes what any principal may do.
var lakeFormation = &Service{
	Model: "lakeformation", Title: "AWS Lake Formation", IAMPrefix: "lakeformation", Issues: []int{72},
	AccountMembers: []string{"CatalogId"},
	Resources: []*Resource{
		{Kind: "lfTag", Shape: "LFTagPair", ID: "name",
			ARN:        "arn:{partition}:lakeformation:{region}:{account}:lftag/{name}",
			NameMember: "TagKey", Unique: true, Limit: 1000,
			Generate: map[string]any{"CatalogId": "$account"}},
		{Kind: "lfTagExpression", Shape: "LFTagExpression", ID: "name",
			ARN:        "arn:{partition}:lakeformation:{region}:{account}:lftagexpression/{name}",
			NameMember: "Name", Unique: true, Limit: 1000,
			Generate: map[string]any{"CatalogId": "$account"}},
		{Kind: "dataLakeSettings", Shape: "GetDataLakeSettingsResponse", Singleton: true, ID: "name",
			ARN: "arn:{partition}:lakeformation:{region}:{account}:datalakesettings", NameMember: "DataLakeSettings",
			Generate: map[string]any{"DataLakeSettings": map[string]any{
				"DataLakeAdmins": []any{}, "ReadOnlyAdmins": []any{}, "TrustedResourceOwners": []any{},
				"CreateDatabaseDefaultPermissions": []any{map[string]any{"Principal": map[string]any{"DataLakePrincipalIdentifier": "IAM_ALLOWED_PRINCIPALS"}, "Permissions": []any{"ALL"}}},
				"CreateTableDefaultPermissions":    []any{map[string]any{"Principal": map[string]any{"DataLakePrincipalIdentifier": "IAM_ALLOWED_PRINCIPALS"}, "Permissions": []any{"ALL"}}},
				"Parameters":                       map[string]any{"CROSS_ACCOUNT_VERSION": "4", "SET_CONTEXT": "TRUE"}}}},
		{Kind: "registeredResource", Shape: "ResourceInfo", ID: "name",
			ARN:        "arn:{partition}:lakeformation:{region}:{account}:registeredresource/{name}",
			NameMember: "ResourceArn", Unique: true, Limit: 100,
			Generate: map[string]any{"LastModified": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateLFTag", Kind: Create, Resource: "lfTag", Check: lakeFormationTag},
		{Name: "GetLFTag", Kind: Read, Resource: "lfTag", Lookup: "TagKey"},
		{Name: "ListLFTags", Kind: List, Resource: "lfTag"},
		{Name: "DeleteLFTag", Kind: Delete, Resource: "lfTag", Lookup: "TagKey"},
		{Name: "CreateLFTagExpression", Kind: Create, Resource: "lfTagExpression"},
		{Name: "GetLFTagExpression", Kind: Read, Resource: "lfTagExpression", Lookup: "Name"},
		{Name: "ListLFTagExpressions", Kind: List, Resource: "lfTagExpression"},
		{Name: "UpdateLFTagExpression", Kind: Update, Resource: "lfTagExpression", Lookup: "Name", Fields: []string{"Description", "Expression"}},
		{Name: "DeleteLFTagExpression", Kind: Delete, Resource: "lfTagExpression", Lookup: "Name"},
		{Name: "GetDataLakeSettings", Kind: Read, Resource: "dataLakeSettings"},
		{Name: "PutDataLakeSettings", Kind: Put, Resource: "dataLakeSettings", Check: lakeFormationSettings},
		{Name: "RegisterResource", Kind: Create, Resource: "registeredResource", Check: lakeFormationRegister},
		{Name: "DescribeResource", Kind: Read, Resource: "registeredResource", Lookup: "ResourceArn"},
		{Name: "ListResources", Kind: List, Resource: "registeredResource"},
		{Name: "UpdateResource", Kind: Update, Resource: "registeredResource", Lookup: "ResourceArn", Touch: []string{"LastModified"},
			Fields: []string{"RoleArn", "WithFederation", "HybridAccessEnabled"}},
		{Name: "DeregisterResource", Kind: Delete, Resource: "registeredResource", Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"LF-tags, LF-tag expressions, the Regional data lake settings and registered Amazon S3 locations are stored records, and Lake Formation permissions are not evaluated: GrantPermissions, RevokePermissions, ListPermissions, LF-tag assignment to resources, data cells filters, opt-ins, transactions, query planning, temporary credentials and every other official operation return UnknownOperationException with HTTP 501. Data lake administrators, default permissions and tags therefore change what no principal may do.",
		"An LF-tag holds 1 to 50 values that cannot change after creation: UpdateLFTag is not implemented because the stored model cannot apply TagValuesToAdd and TagValuesToDelete to the values already held, so a tag is deleted and created again to change them. An LF-tag expression is stored without checking that the LF-tags it names exist, and ListLFTags does not support the ResourceShareType filter because no tag is shared. LF-tags and expressions have no ARN in AWS: the ARN used for authorization is a simulator form. A duplicate LF-tag or expression fails with InvalidInputException (HTTP 400) because the official operations model no already-exists exception, and the missing-resource and already-registered failures use the modeled EntityNotFoundException and AlreadyExistsException.",
		"Data lake settings start as AWS documents them for an account that never changed them: no administrators and IAM_ALLOWED_PRINCIPALS holding ALL on new databases and tables. PutDataLakeSettings replaces the whole document, accepts at most 30 administrators and 30 read-only administrators, and stores everything else without resolving principals.",
		"RegisterResource stores an Amazon S3 location and its IAM role without reading the location or assuming the role: ResourceArn must be an Amazon S3 ARN, and either RoleArn or UseServiceLinkedRole true must be given, the latter being stored as the Lake Formation service-linked role ARN. WithFederation, HybridAccessEnabled and WithPrivilegedAccess are stored as flags with no effect, and registering a location nested inside or around a registered one is allowed although AWS refuses it. CatalogId, when sent, must be the simulator account. The limits of 1000 LF-tags, 1000 expressions and 100 registered locations per Region are local.",
	},
}

var lakeFormationS3 = regexp.MustCompile(`^arn:[a-z-]+:s3:::[a-z0-9][a-z0-9.-]{1,61}[a-z0-9](/.*)?$`)

const lakeFormationServiceRole = "arn:aws:iam::000000000000:role/aws-service-role/lakeformation.amazonaws.com/AWSServiceRoleForLakeFormationDataAccess"

// lakeFormationTag bounds the values of a new LF-tag.
func lakeFormationTag(input map[string]any) error {
	if values, _ := input["TagValues"].([]any); len(values) > 50 {
		return Failure("InvalidInputException", "An LF-tag holds at most 50 values.")
	}
	return nil
}

// lakeFormationSettings bounds the administrator lists as documented.
func lakeFormationSettings(input map[string]any) error {
	settings, _ := input["DataLakeSettings"].(map[string]any)
	for _, member := range []string{"DataLakeAdmins", "ReadOnlyAdmins"} {
		if list, _ := settings[member].([]any); len(list) > 30 {
			return Failure("InvalidInputException", member+" holds at most 30 principals.")
		}
	}
	return nil
}

// lakeFormationRegister keeps registered locations to Amazon S3 and to a
// role the simulator can store.
func lakeFormationRegister(input map[string]any) error {
	if arn, _ := input["ResourceArn"].(string); !lakeFormationS3.MatchString(arn) {
		return Failure("InvalidInputException", "ResourceArn must be an Amazon S3 bucket or prefix ARN.")
	}
	linked, _ := input["UseServiceLinkedRole"].(bool)
	_, role := input["RoleArn"]
	if linked == role {
		return Failure("InvalidInputException", "Specify either RoleArn or UseServiceLinkedRole true.")
	}
	if linked {
		input["RoleArn"] = lakeFormationServiceRole
	}
	delete(input, "UseServiceLinkedRole")
	return nil
}

// quickSight serves folders and namespaces as stored records.
var quickSight = &Service{
	Model: "quicksight", Title: "Amazon Quick Sight", IAMPrefix: "quicksight", Issues: []int{76},
	AccountMembers: []string{"AwsAccountId"},
	Resources: []*Resource{
		{Kind: "folder", Shape: "Folder", Summary: "FolderSummary", ID: "name",
			ARN:       "arn:{partition}:quicksight:{region}:{account}:folder/{name}",
			ARNMember: "Arn", NameMember: "FolderId", Unique: true, Limit: 100,
			Generate: map[string]any{"FolderType": "SHARED", "CreatedTime": "$now", "LastUpdatedTime": "$now"}},
		{Kind: "namespace", Shape: "NamespaceInfoV2", ID: "name",
			ARN:       "arn:{partition}:quicksight:{region}:{account}:namespace/{name}",
			ARNMember: "Arn", NameMember: "Name", Unique: true, Limit: 100,
			Aliases:  map[string]string{"Namespace": "Name"},
			Generate: map[string]any{"CapacityRegion": "$region", "CreationStatus": "CREATED"}},
	},
	Ops: []Op{
		{Name: "CreateFolder", Kind: Create, Resource: "folder", Check: quickSightFolder},
		{Name: "DescribeFolder", Kind: Read, Resource: "folder", Lookup: "FolderId"},
		{Name: "ListFolders", Kind: List, Resource: "folder"},
		{Name: "UpdateFolder", Kind: Update, Resource: "folder", Lookup: "FolderId", Touch: []string{"LastUpdatedTime"}, Fields: []string{"Name"}},
		{Name: "DeleteFolder", Kind: Delete, Resource: "folder", Lookup: "FolderId"},
		{Name: "CreateNamespace", Kind: Create, Resource: "namespace"},
		{Name: "DescribeNamespace", Kind: Read, Resource: "namespace", Lookup: "Namespace"},
		{Name: "ListNamespaces", Kind: List, Resource: "namespace"},
		{Name: "DeleteNamespace", Kind: Delete, Resource: "namespace", Lookup: "Namespace"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Folders and namespaces are stored records: there is no Quick Sight account subscription, user, group, data source, data set, analysis, dashboard, template, theme, topic or embedded session, and no data is read. Every other official operation, including folder membership and permissions, returns UnknownOperationException with HTTP 501.",
		"A folder is a flat container with a name: ParentFolderArn is refused, so FolderPath is never reported, and Permissions are refused because nothing evaluates them. FolderType is SHARED, and SharingModel is stored when sent. UpdateFolder changes only the name. The FolderId is the identity and must be new; a duplicate fails with ConflictException where AWS answers ResourceExistsException, as it does for a duplicate namespace.",
		"A namespace is CREATED at once, with CapacityRegion set to the Region, and has no members. The account's default namespace is not pre-created, so DescribeNamespace default fails with ResourceNotFoundException and ListNamespaces shows only namespaces that were created. Deleting a namespace removes it without checking for users or groups, because none can exist.",
		"AwsAccountId must be the simulator account. Responses carry Status and RequestId as the official shapes require. Tags are stored from the create operations and by TagResource on the ARN. The limits of 100 folders and 100 namespaces per Region are local.",
	},
}

// quickSightFolder refuses folder nesting and permissions the stored model
// cannot hold.
func quickSightFolder(input map[string]any) error {
	for _, member := range []string{"ParentFolderArn", "Permissions"} {
		if _, ok := input[member]; ok {
			return Failure("InvalidParameterValueException", member+" is not supported by the simulator.")
		}
	}
	return nil
}
