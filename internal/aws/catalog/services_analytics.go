package catalog

import "strings"

// Reviewed bounded specifications for analytics services. Each binds only
// official operations whose effect is configuration metadata: no query,
// crawl, job run, record, shard, search cluster or data movement is executed.

var athena = &Service{
	Model: "athena", Title: "Amazon Athena", IAMPrefix: "athena", Issues: []int{61},
	Resources: []*Resource{
		{Kind: "workGroup", Shape: "WorkGroup", Summary: "WorkGroupSummary", ID: "name",
			ARN:        "arn:{partition}:athena:{region}:{account}:workgroup/{name}",
			NameMember: "Name", Unique: true, Limit: 50,
			Generate: map[string]any{"State": "ENABLED", "CreationTime": "$now"}},
		{Kind: "dataCatalog", Shape: "DataCatalog", Summary: "DataCatalogSummary", ID: "name",
			ARN:        "arn:{partition}:athena:{region}:{account}:datacatalog/{name}",
			NameMember: "Name", Unique: true, Limit: 50,
			Generate: map[string]any{"CatalogName": "$name"}},
	},
	Ops: []Op{
		{Name: "CreateWorkGroup", Kind: Create, Resource: "workGroup"},
		{Name: "GetWorkGroup", Kind: Read, Resource: "workGroup", Lookup: "WorkGroup"},
		{Name: "ListWorkGroups", Kind: List, Resource: "workGroup"},
		{Name: "UpdateWorkGroup", Kind: Update, Resource: "workGroup", Lookup: "WorkGroup", Fields: []string{"Description", "State"}},
		{Name: "DeleteWorkGroup", Kind: Delete, Resource: "workGroup", Lookup: "WorkGroup", Accept: []string{"RecursiveDeleteOption"}},
		{Name: "CreateDataCatalog", Kind: Create, Resource: "dataCatalog", Check: athenaCatalogType},
		{Name: "GetDataCatalog", Kind: Read, Resource: "dataCatalog", Lookup: "Name"},
		{Name: "ListDataCatalogs", Kind: List, Resource: "dataCatalog"},
		{Name: "UpdateDataCatalog", Kind: Update, Resource: "dataCatalog", Lookup: "Name", Fields: []string{"Type", "Description", "Parameters"}, Check: athenaCatalogType},
		{Name: "DeleteDataCatalog", Kind: Delete, Resource: "dataCatalog", Lookup: "Name", Accept: []string{"DeleteCatalogOnly"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Workgroups and data catalogs are stored configuration: no query, calculation, notebook, session or capacity reservation runs, and StartQueryExecution and other execution APIs return UnknownOperationException with HTTP 501.",
		"The default primary workgroup and AwsDataCatalog are not pre-created. UpdateWorkGroup changes only Description and State; ConfigurationUpdates is refused. Workgroups hold no named queries, prepared statements or notebooks, so RecursiveDeleteOption has no further effect.",
		"FEDERATED data catalogs are refused because they provision connectors; Lambda, Glue and Hive catalog parameters are stored without contacting the named functions or metastores.",
	},
}

// athenaCatalogType refuses federated catalogs, whose creation provisions a
// Glue connection and a Lambda connector stack the simulator does not run.
func athenaCatalogType(input map[string]any) error {
	if input["Type"] == "FEDERATED" {
		return Failure("InvalidRequestException", "FEDERATED data catalogs provision connectors, which the simulator does not run.")
	}
	return nil
}

var glue = &Service{
	Model: "glue", Title: "AWS Glue", IAMPrefix: "glue", Issues: []int{70},
	AccountMembers: []string{"CatalogId"},
	Resources: []*Resource{
		{Kind: "database", Shape: "Database", ID: "name", ARN: "arn:{partition}:glue:{region}:{account}:database/{name}",
			NameMember: "Name", Unique: true, Limit: 100, Cascade: true,
			Generate: map[string]any{"CreateTime": "$now", "CatalogId": "$account"}},
		{Kind: "table", Shape: "Table", Parent: "database", ParentMember: "DatabaseName", ID: "name",
			ARN:        "arn:{partition}:glue:{region}:{account}:table/{parent}/{name}",
			NameMember: "Name", Unique: true, Limit: 200,
			Generate: map[string]any{"DatabaseName": "$parentName", "CatalogId": "$account", "CreateTime": "$now", "UpdateTime": "$now", "IsRegisteredWithLakeFormation": false}},
		{Kind: "job", Shape: "Job", ID: "name", ARN: "arn:{partition}:glue:{region}:{account}:job/{name}",
			NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"CreatedOn": "$now", "LastModifiedOn": "$now", "JobName": "$name"}},
		{Kind: "crawler", Shape: "Crawler", ID: "name", ARN: "arn:{partition}:glue:{region}:{account}:crawler/{name}",
			NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"State": "READY", "CreationTime": "$now", "LastUpdated": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateDatabase", Kind: Create, Resource: "database", From: "DatabaseInput", Check: glueDatabase},
		{Name: "GetDatabase", Kind: Read, Resource: "database", Lookup: "Name"},
		{Name: "GetDatabases", Kind: List, Resource: "database"},
		{Name: "UpdateDatabase", Kind: Update, Resource: "database", Lookup: "Name", From: "DatabaseInput",
			Fields: []string{"Description", "LocationUri", "Parameters"}, Check: glueDatabase},
		{Name: "DeleteDatabase", Kind: Delete, Resource: "database", Lookup: "Name"},
		{Name: "CreateTable", Kind: Create, Resource: "table", Parent: "DatabaseName", From: "TableInput", Check: glueTable},
		{Name: "GetTable", Kind: Read, Resource: "table", Parent: "DatabaseName", Lookup: "Name"},
		{Name: "GetTables", Kind: List, Resource: "table", Parent: "DatabaseName"},
		{Name: "UpdateTable", Kind: Update, Resource: "table", Parent: "DatabaseName", Lookup: "Name", From: "TableInput",
			Fields: []string{"Description", "Owner", "LastAccessTime", "LastAnalyzedTime", "Retention", "StorageDescriptor", "PartitionKeys", "ViewOriginalText", "ViewExpandedText", "TableType", "Parameters"},
			Touch:  []string{"UpdateTime"}, Accept: []string{"SkipArchive"}, Check: glueTable},
		{Name: "DeleteTable", Kind: Delete, Resource: "table", Parent: "DatabaseName", Lookup: "Name"},
		{Name: "CreateJob", Kind: Create, Resource: "job", Check: glueJob},
		{Name: "GetJob", Kind: Read, Resource: "job", Lookup: "JobName"},
		{Name: "ListJobs", Kind: List, Resource: "job", ListValue: "Name"},
		{Name: "GetJobs", Kind: List, Resource: "job"},
		{Name: "UpdateJob", Kind: Update, Resource: "job", Lookup: "JobName", From: "JobUpdate", Touch: []string{"LastModifiedOn"},
			Fields: []string{"JobMode", "JobRunQueuingEnabled", "Description", "LogUri", "Role", "ExecutionProperty", "Command", "DefaultArguments", "NonOverridableArguments", "Connections", "MaxRetries", "AllocatedCapacity", "Timeout", "MaxCapacity", "WorkerType", "NumberOfWorkers", "SecurityConfiguration", "NotificationProperty", "GlueVersion", "CodeGenConfigurationNodes", "ExecutionClass", "MaintenanceWindow"},
			Check:  glueJob},
		{Name: "DeleteJob", Kind: Delete, Resource: "job", Lookup: "JobName"},
		{Name: "CreateCrawler", Kind: Create, Resource: "crawler", Check: glueCrawler},
		{Name: "GetCrawler", Kind: Read, Resource: "crawler", Lookup: "Name"},
		{Name: "ListCrawlers", Kind: List, Resource: "crawler", ListValue: "Name"},
		{Name: "GetCrawlers", Kind: List, Resource: "crawler"},
		{Name: "UpdateCrawler", Kind: Update, Resource: "crawler", Lookup: "Name", Touch: []string{"LastUpdated"}, Check: glueCrawler,
			Fields: []string{"Role", "DatabaseName", "Description", "Targets", "Classifiers", "TablePrefix", "SchemaChangePolicy", "RecrawlPolicy", "LineageConfiguration", "LakeFormationConfiguration", "Configuration", "CrawlerSecurityConfiguration"}},
		{Name: "DeleteCrawler", Kind: Delete, Resource: "crawler", Lookup: "Name"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "GetTags", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Data Catalog databases and tables, job definitions and crawler definitions are stored metadata: no job run, crawl, schedule, trigger, partition, statistics, Lake Formation permission or data access is performed, and StartJobRun, StartCrawler and other run APIs return UnknownOperationException with HTTP 501.",
		"Names are not folded to lowercase, so database and table names with uppercase letters are refused. Resource links (TargetDatabase, TargetTable), federation, default Lake Formation permissions, multi-dialect views, transactions, Iceberg inputs, crawler schedules and job source control are refused.",
		"Updates change only the supplied members instead of replacing the whole definition, renames are refused, and table versions are not kept. Deleting a database deletes its tables. DeleteJob of a missing job fails instead of succeeding.",
		"Referenced roles, S3 locations, scripts, connections, classifiers, security configurations and crawler target databases are stored without checking that they exist; tags are accepted for every stored Glue resource.",
	},
}

// glueNames applies rules the official shapes cannot express for one
// catalog definition: the simulator keeps names exactly as given, so it
// refuses names that Glue would fold, and refuses members that link to
// other accounts, federated sources or Lake Formation authority.
func glueNames(definition map[string]any, refused ...string) error {
	if name, ok := definition["Name"].(string); ok && name != strings.ToLower(name) {
		return Failure("InvalidInputException", "Use lowercase names: the simulator does not fold Data Catalog names to lowercase.")
	}
	for _, member := range refused {
		if _, ok := definition[member]; ok {
			return Failure("InvalidInputException", member+" is not supported by the simulator.")
		}
	}
	return nil
}

func glueDatabase(input map[string]any) error {
	definition, _ := input["DatabaseInput"].(map[string]any)
	if name, ok := input["Name"].(string); ok && definition["Name"] != name {
		return Failure("InvalidInputException", "Renaming a database is not supported by the simulator.")
	}
	return glueNames(definition, "TargetDatabase", "FederatedDatabase", "CreateTableDefaultPermissions")
}

// glueTable identifies the table by Name or, as the official contract
// allows, by TableInput.Name; both must agree when both are supplied.
func glueTable(input map[string]any) error {
	definition, ok := input["TableInput"].(map[string]any)
	if !ok {
		return Failure("InvalidInputException", "TableInput is required: open table format inputs are not supported by the simulator.")
	}
	if name, ok := input["Name"].(string); ok && definition["Name"] != name {
		return Failure("InvalidInputException", "Name must equal TableInput.Name; renaming a table is not supported by the simulator.")
	}
	if _, ok := input["Name"]; !ok {
		input["Name"] = definition["Name"]
	}
	return glueNames(definition, "TargetTable", "ViewDefinition")
}

func glueJob(input map[string]any) error {
	update, _ := input["JobUpdate"].(map[string]any)
	for _, definition := range []map[string]any{input, update} {
		if _, ok := definition["SourceControlDetails"]; ok {
			return Failure("InvalidInputException", "Job source control integration is not supported by the simulator.")
		}
	}
	return nil
}

func glueCrawler(input map[string]any) error {
	if _, ok := input["Schedule"]; ok {
		return Failure("InvalidInputException", "Crawler schedules are not supported: the simulator never runs crawlers.")
	}
	return nil
}

var kinesis = &Service{
	Model: "kinesis", Title: "Amazon Kinesis Data Streams", IAMPrefix: "kinesis", Issues: []int{71},
	Resources: []*Resource{
		{Kind: "stream", Shape: "StreamDescriptionSummary", Summary: "StreamSummary", ID: "name",
			ARN:       "arn:{partition}:kinesis:{region}:{account}:stream/{name}",
			ARNMember: "StreamARN", NameMember: "StreamName", Unique: true, Limit: 50,
			Aliases: map[string]string{"ShardCount": "OpenShardCount"},
			Generate: map[string]any{"StreamARN": "$arn", "StreamStatus": "ACTIVE", "RetentionPeriodHours": 24,
				"StreamCreationTimestamp": "$now", "EnhancedMonitoring": []any{map[string]any{"ShardLevelMetrics": []any{}}},
				"EncryptionType": "NONE", "OpenShardCount": 4, "ConsumerCount": 0, "MaxRecordSizeInKiB": 1024,
				"StreamModeDetails": map[string]any{"StreamMode": "PROVISIONED"}}},
	},
	Ops: []Op{
		{Name: "CreateStream", Kind: Create, Resource: "stream", Check: kinesisCapacity},
		{Name: "DescribeStreamSummary", Kind: Read, Resource: "stream", Lookup: "StreamName", Check: kinesisStream},
		{Name: "ListStreams", Kind: List, Resource: "stream", ListValue: "StreamName", HasMore: "HasMoreStreams"},
		{Name: "DeleteStream", Kind: Delete, Resource: "stream", Lookup: "StreamName", Accept: []string{"EnforceConsumerDeletion"}, Check: kinesisStream},
		{Name: "IncreaseStreamRetentionPeriod", Kind: Update, Resource: "stream", Lookup: "StreamName", Fields: []string{"RetentionPeriodHours"}, Check: kinesisRetention},
		{Name: "DecreaseStreamRetentionPeriod", Kind: Update, Resource: "stream", Lookup: "StreamName", Fields: []string{"RetentionPeriodHours"}, Check: kinesisRetention},
		{Name: "UpdateStreamMode", Kind: Update, Resource: "stream", Lookup: "StreamARN", Fields: []string{"StreamModeDetails"}},
		{Name: "StartStreamEncryption", Kind: Update, Resource: "stream", Lookup: "StreamName", Fields: []string{"EncryptionType", "KeyId"}, Check: kinesisEncryption},
		{Name: "StopStreamEncryption", Kind: Update, Resource: "stream", Lookup: "StreamName", Fields: []string{"EncryptionType", "KeyId"}, Check: kinesisEncryption,
			Set: map[string]any{"EncryptionType": "NONE", "KeyId": nil}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"Streams are stored configuration that becomes ACTIVE immediately: no shard, record, sequence number, iterator, consumer or enhanced fan-out exists, and PutRecord, GetRecords, DescribeStream, ListShards and resharding APIs return UnknownOperationException with HTTP 501.",
		"CreateStream needs ShardCount for a provisioned stream or StreamModeDetails ON_DEMAND (stored with 4 open shards); WarmThroughputMiBps is refused. Retention changes accept 24 to 8760 hours without comparing them with the current period. Encryption stores the KMS key identifier without contacting KMS.",
		"Name-based tag APIs (AddTagsToStream, RemoveTagsFromStream, ListTagsForStream) are not implemented; use TagResource, UntagResource and ListTagsForResource with the stream ARN.",
	},
}

// kinesisStream lets a request name its stream by StreamName or StreamARN,
// as the official operations allow; both must agree when both are given.
func kinesisStream(input map[string]any) error {
	arn, ok := input["StreamARN"].(string)
	if !ok {
		return nil
	}
	if name, named := input["StreamName"].(string); named && !strings.HasSuffix(arn, ":stream/"+name) {
		return Failure("InvalidArgumentException", "StreamName and StreamARN must identify the same stream.")
	}
	input["StreamName"] = arn
	delete(input, "StreamARN")
	return nil
}

// kinesisCapacity requires an explicit capacity mode: a shard count for a
// provisioned stream, or on-demand mode without one.
func kinesisCapacity(input map[string]any) error {
	mode := ""
	if details, ok := input["StreamModeDetails"].(map[string]any); ok {
		mode, _ = details["StreamMode"].(string)
	}
	_, sharded := input["ShardCount"]
	switch {
	case mode == "ON_DEMAND" && sharded:
		return Failure("InvalidArgumentException", "ShardCount is not accepted for an ON_DEMAND stream.")
	case mode != "ON_DEMAND" && !sharded:
		return Failure("InvalidArgumentException", "Specify ShardCount for a provisioned stream or StreamModeDetails ON_DEMAND.")
	}
	return nil
}

func kinesisRetention(input map[string]any) error {
	if hours, ok := input["RetentionPeriodHours"].(int64); ok && (hours < 24 || hours > 8760) {
		return Failure("InvalidArgumentException", "RetentionPeriodHours must be between 24 and 8760.")
	}
	return kinesisStream(input)
}

func kinesisEncryption(input map[string]any) error {
	if input["EncryptionType"] != "KMS" {
		return Failure("InvalidArgumentException", "EncryptionType must be KMS.")
	}
	return kinesisStream(input)
}

var openSearch = &Service{
	Model: "opensearch", Title: "Amazon OpenSearch Service", IAMPrefix: "es", Issues: []int{75},
	Resources: []*Resource{
		{Kind: "domain", Shape: "DomainStatus", Summary: "DomainInfo", ID: "name",
			ARN:       "arn:{partition}:es:{region}:{account}:domain/{name}",
			ARNMember: "ARN", NameMember: "DomainName", Unique: true, Limit: 20,
			Generate: map[string]any{"DomainId": "$fmt:{account}/{name}", "Created": true, "Deleted": false, "Processing": false,
				"UpgradeProcessing": false, "DomainProcessingStatus": "Active", "EngineType": "OpenSearch"}},
	},
	Ops: []Op{
		{Name: "CreateDomain", Kind: Create, Resource: "domain", Check: openSearchDomain},
		{Name: "DescribeDomain", Kind: Read, Resource: "domain", Lookup: "DomainName"},
		{Name: "ListDomainNames", Kind: List, Resource: "domain", Filters: map[string]string{"EngineType": "EngineType"}},
		{Name: "DeleteDomain", Kind: Delete, Resource: "domain", Lookup: "DomainName", Set: map[string]any{"Deleted": true, "DomainProcessingStatus": "Deleting"}},
	},
	Bounds: []string{
		"Domains are configuration records: no cluster, node, storage, endpoint, index, document or search exists, Endpoint is never returned, and UpdateDomainConfig, index, package, connection and VPC endpoint APIs return UnknownOperationException with HTTP 501.",
		"Only OpenSearch engine versions are accepted. VPC options, fine-grained access control and master-user credentials, Cognito and IAM Identity Center authentication, Auto-Tune and AI/ML options are refused; access policies are stored but not enforced.",
		"CreateDomain stores TagList, but AddTags, RemoveTags and ListTags are not implemented.",
	},
}

// openSearchDomain refuses members that would attach networks or
// authentication the simulator does not provide.
func openSearchDomain(input map[string]any) error {
	if version, _ := input["EngineVersion"].(string); strings.HasPrefix(version, "Elasticsearch_") {
		return Failure("ValidationException", "Legacy Elasticsearch engine versions are not supported by the simulator.")
	}
	for _, member := range []string{"VPCOptions", "AdvancedSecurityOptions", "CognitoOptions", "IdentityCenterOptions", "AutoTuneOptions", "AIMLOptions"} {
		if _, ok := input[member]; ok {
			return Failure("ValidationException", member+" is not supported by the simulator.")
		}
	}
	return nil
}

var dataExchange = &Service{
	Model: "dataexchange", Title: "AWS Data Exchange", IAMPrefix: "dataexchange", Issues: []int{64},
	Resources: []*Resource{
		{Kind: "dataSet", Shape: "GetDataSetResponse", Summary: "DataSetEntry", ID: "hex:32",
			ARN:       "arn:{partition}:dataexchange:{region}:{account}:data-sets/{id}",
			ARNMember: "Arn", NameMember: "Name", IDMember: "Id", Limit: 100,
			Generate: map[string]any{"CreatedAt": "$now", "UpdatedAt": "$now", "Origin": "OWNED"}},
		{Kind: "revision", Shape: "GetRevisionResponse", Summary: "RevisionEntry", Parent: "dataSet", ParentMember: "DataSetId", ID: "hex:32",
			ARN:       "arn:{partition}:dataexchange:{region}:{account}:data-sets/{parent}/revisions/{id}",
			ARNMember: "Arn", IDMember: "Id", Limit: 100,
			Generate: map[string]any{"DataSetId": "$parentId", "CreatedAt": "$now", "UpdatedAt": "$now", "Finalized": false, "Revoked": false}},
	},
	Ops: []Op{
		{Name: "CreateDataSet", Kind: Create, Resource: "dataSet"},
		{Name: "GetDataSet", Kind: Read, Resource: "dataSet", Lookup: "DataSetId"},
		{Name: "ListDataSets", Kind: List, Resource: "dataSet", Filters: map[string]string{"Origin": "Origin"}},
		{Name: "UpdateDataSet", Kind: Update, Resource: "dataSet", Lookup: "DataSetId", Fields: []string{"Description", "Name"}, Touch: []string{"UpdatedAt"}},
		{Name: "DeleteDataSet", Kind: Delete, Resource: "dataSet", Lookup: "DataSetId"},
		{Name: "CreateRevision", Kind: Create, Resource: "revision", Parent: "DataSetId"},
		{Name: "GetRevision", Kind: Read, Resource: "revision", Parent: "DataSetId", Lookup: "RevisionId"},
		{Name: "ListDataSetRevisions", Kind: List, Resource: "revision", Parent: "DataSetId"},
		{Name: "UpdateRevision", Kind: Update, Resource: "revision", Parent: "DataSetId", Lookup: "RevisionId", Fields: []string{"Comment", "Finalized"}, Touch: []string{"UpdatedAt"}},
		{Name: "DeleteRevision", Kind: Delete, Resource: "revision", Parent: "DataSetId", Lookup: "RevisionId"},
	},
	Bounds: []string{
		"Owned data sets and their revisions are stored metadata: no asset, import or export job, event action, data grant, product, subscription or API asset call exists, and CreateJob, StartJob, SendApiAsset and other asset APIs return UnknownOperationException with HTTP 501.",
		"Revisions stay empty and are never revoked; a data set with revisions cannot be deleted until its revisions are deleted.",
		"Tags are stored from CreateDataSet and CreateRevision and returned by the resource reads; TagResource, UntagResource and ListTagsForResource are not implemented.",
	},
}

var entityResolution = &Service{
	Model: "entityresolution", Title: "AWS Entity Resolution", IAMPrefix: "entityresolution", Issues: []int{69},
	Resources: []*Resource{
		{Kind: "schemaMapping", Shape: "GetSchemaMappingOutput", Summary: "SchemaMappingSummary", ID: "name",
			ARN:       "arn:{partition}:entityresolution:{region}:{account}:schemamapping/{name}",
			ARNMember: "schemaArn", NameMember: "schemaName", Unique: true, Limit: 50,
			Generate: map[string]any{"createdAt": "$now", "updatedAt": "$now", "hasWorkflows": false}},
	},
	Ops: []Op{
		{Name: "CreateSchemaMapping", Kind: Create, Resource: "schemaMapping"},
		{Name: "GetSchemaMapping", Kind: Read, Resource: "schemaMapping", Lookup: "schemaName"},
		{Name: "ListSchemaMappings", Kind: List, Resource: "schemaMapping"},
		{Name: "UpdateSchemaMapping", Kind: Update, Resource: "schemaMapping", Lookup: "schemaName", Fields: []string{"description", "mappedInputFields"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteSchemaMapping", Kind: Delete, Resource: "schemaMapping", Lookup: "schemaName"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Only schema mappings are stored; matching and ID mapping workflows, ID namespaces, provider services, policies and match jobs are not implemented and return UnknownOperationException with HTTP 501, so no record is read, matched or written and hasWorkflows is always false.",
	},
}
