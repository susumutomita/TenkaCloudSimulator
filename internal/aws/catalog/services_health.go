package catalog

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Health and life-science data stores and workspaces served as stored
// configuration: AWS HealthImaging, AWS HealthLake, AWS HealthOmics, Amazon
// Keyspaces and Amazon Managed Grafana. Nothing that imports, exports,
// queries, runs or stores data is bound.

// healthImaging serves HealthImaging data stores as configuration records.
var healthImaging = &Service{
	Model: "medical-imaging", Title: "AWS HealthImaging", IAMPrefix: "medical-imaging", Issues: []int{171},
	Resources: []*Resource{
		{Kind: "datastore", Shape: "DatastoreProperties", Summary: "DatastoreSummary", ID: "alnum:32",
			ARN:       "arn:{partition}:medical-imaging:{region}:{account}:datastore/{id}",
			ARNMember: "datastoreArn", IDMember: "datastoreId", NameMember: "datastoreName", Limit: 10,
			Generate: map[string]any{"datastoreName": "$name", "datastoreStatus": "CREATING", "createdAt": "$now", "updatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateDatastore", Kind: Create, Resource: "datastore"},
		{Name: "GetDatastore", Kind: Read, Resource: "datastore", Lookup: "datastoreId"},
		{Name: "ListDatastores", Kind: List, Resource: "datastore", Filters: map[string]string{"datastoreStatus": "datastoreStatus"}},
		{Name: "DeleteDatastore", Kind: Delete, Resource: "datastore", Lookup: "datastoreId", Set: map[string]any{"datastoreStatus": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Data stores are stored configuration records that stay CREATING: no DICOM storage, image set, endpoint or data-plane service exists, and StartDICOMImportJob, GetDICOMImportJob, ListDICOMImportJobs, SearchImageSets, GetImageSet, GetImageSetMetadata, GetImageFrame, ListImageSetVersions, UpdateImageSetMetadata, CopyImageSet and DeleteImageSet return UnknownOperationException with HTTP 501.",
		"kmsKeyArn and lambdaAuthorizerArn are stored without contacting KMS or Lambda, and the authorizer is never invoked; when kmsKeyArn is omitted no AWS owned key is reported. losslessStorageFormat is stored without being applied. A data store name defaults to its identifier when omitted, names need not be unique, and DeleteDatastore returns the identifier with status DELETING and removes the record.",
		"Tags apply to data store ARNs (arn:aws:medical-imaging:region:account:datastore/id); image set ARNs are not stored, so tagging one fails with ResourceNotFoundException. Resource counts per Region (10 data stores) and the 50 tags per resource are local limits, and ListDatastores accepts a datastoreStatus filter on the stored status.",
	},
}

// healthLake serves HealthLake FHIR data stores as configuration records.
var healthLake = &Service{
	Model: "healthlake", Title: "Amazon HealthLake", IAMPrefix: "healthlake", Issues: []int{172},
	Resources: []*Resource{
		{Kind: "datastore", Shape: "DatastoreProperties", ID: "alnum:32",
			ARN:       "arn:{partition}:healthlake:{region}:{account}:datastore/fhir/{id}",
			ARNMember: "DatastoreArn", IDMember: "DatastoreId", NameMember: "DatastoreName", Limit: 10,
			Generate: map[string]any{"DatastoreStatus": "CREATING", "CreatedAt": "$now",
				"SseConfiguration": map[string]any{"KmsEncryptionConfig": map[string]any{"CmkType": "AWS_OWNED_KMS_KEY"}}}},
	},
	Ops: []Op{
		{Name: "CreateFHIRDatastore", Kind: Create, Resource: "datastore", Check: healthLakeCreate},
		{Name: "DescribeFHIRDatastore", Kind: Read, Resource: "datastore", Lookup: "DatastoreId"},
		{Name: "ListFHIRDatastores", Kind: List, Resource: "datastore", Check: healthLakeList,
			Filters: map[string]string{"DatastoreName": "DatastoreName", "DatastoreStatus": "DatastoreStatus"}},
		{Name: "DeleteFHIRDatastore", Kind: Delete, Resource: "datastore", Lookup: "DatastoreId", Set: map[string]any{"DatastoreStatus": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceARN"},
	},
	Bounds: []string{
		"FHIR data stores are stored configuration records that stay CREATING: no FHIR store, endpoint or data plane exists, DatastoreEndpoint is never returned, and StartFHIRImportJob, DescribeFHIRImportJob, ListFHIRImportJobs, StartFHIRExportJob, DescribeFHIRExportJob and ListFHIRExportJobs return UnknownOperationException with HTTP 501. No import, export or FHIR request is ever processed.",
		"PreloadDataConfig is refused because the Synthea sample data it loads is AWS-owned data the simulator does not hold. Without SseConfiguration the record reports the AWS_OWNED_KMS_KEY type; a customer managed key needs its KmsKeyId, which is stored without contacting KMS and is refused with the AWS owned type. IdentityProviderConfiguration is stored but never consulted: AWS_AUTH takes no metadata, Lambda function or fine-grained flag, SMART_ON_FHIR strategies accept Metadata only as a JSON object whose grant_types_supported lists authorization_code or client_credentials and whose code_challenge_methods_supported includes S256, and IdpLambdaArn is not invoked.",
		"ListFHIRDatastores accepts the Filter members DatastoreName and DatastoreStatus as equality filters on the stored values and refuses CreatedBefore and CreatedAfter. Data store names need not be unique and are optional. DeleteFHIRDatastore returns the identifier, ARN and status DELETING and removes the record, even though AWS refuses to delete a data store that is not ACTIVE. Quota failures use the operation's ValidationException because the official operations model no quota exception; resource counts per Region (10 data stores) are local limits.",
	},
}

// healthLakeCreate applies the documented rules of data store creation.
func healthLakeCreate(input map[string]any) error {
	if _, ok := input["PreloadDataConfig"]; ok {
		return Failure("ValidationException", "PreloadDataConfig is not supported: the Synthea sample data is AWS-owned data.")
	}
	if sse, ok := input["SseConfiguration"].(map[string]any); ok {
		config, _ := sse["KmsEncryptionConfig"].(map[string]any)
		_, keyed := config["KmsKeyId"]
		switch config["CmkType"] {
		case "CUSTOMER_MANAGED_KMS_KEY":
			if !keyed {
				return Failure("ValidationException", "A customer managed key needs its KmsKeyId.")
			}
		default:
			if keyed {
				return Failure("ValidationException", "KmsKeyId applies only to the CUSTOMER_MANAGED_KMS_KEY type.")
			}
		}
	}
	provider, ok := input["IdentityProviderConfiguration"].(map[string]any)
	if !ok {
		return nil
	}
	if provider["AuthorizationStrategy"] == "AWS_AUTH" {
		_, metadata := provider["Metadata"]
		_, lambda := provider["IdpLambdaArn"]
		if metadata || lambda || provider["FineGrainedAuthorizationEnabled"] == true {
			return Failure("ValidationException", "The AWS_AUTH strategy takes no metadata, Lambda function or fine-grained authorization.")
		}
		return nil
	}
	text, ok := provider["Metadata"].(string)
	if !ok {
		return nil
	}
	var metadata map[string]any
	if json.Unmarshal([]byte(text), &metadata) != nil {
		return Failure("ValidationException", "Metadata must be a JSON object.")
	}
	if grants, present := metadata["grant_types_supported"]; present {
		list, _ := grants.([]any)
		if len(list) == 0 {
			return Failure("ValidationException", "grant_types_supported needs at least one grant type.")
		}
		for _, grant := range list {
			if grant != "authorization_code" && grant != "client_credentials" {
				return Failure("ValidationException", "grant_types_supported accepts authorization_code and client_credentials.")
			}
		}
	}
	if methods, present := metadata["code_challenge_methods_supported"]; present {
		if list, _ := methods.([]any); !slices.Contains(list, any("S256")) {
			return Failure("ValidationException", "code_challenge_methods_supported must include S256.")
		}
	}
	return nil
}

// healthLakeList flattens the equality members of Filter onto the stored
// members they compare and refuses the creation time bounds.
func healthLakeList(input map[string]any) error {
	filter, ok := input["Filter"].(map[string]any)
	if !ok {
		return nil
	}
	for _, member := range []string{"CreatedBefore", "CreatedAfter"} {
		if _, present := filter[member]; present {
			return Failure("ValidationException", "Filter "+member+" is not supported by the simulator.")
		}
	}
	delete(input, "Filter")
	for _, member := range []string{"DatastoreName", "DatastoreStatus"} {
		if value, present := filter[member]; present {
			input[member] = value
		}
	}
	return nil
}

// omics serves HealthOmics run groups, run caches, sequence stores and
// reference stores as configuration records. Identifiers are numeric, as the
// official patterns require, and come from the Check hooks below.
var omics = &Service{
	Model: "omics", Title: "AWS HealthOmics", IAMPrefix: "omics", Issues: []int{173},
	Resources: []*Resource{
		{Kind: "runGroup", Shape: "GetRunGroupResponse", Summary: "RunGroupListItem", ID: "name", NameMember: "id", IDMember: "id", Limit: 25,
			ARN: "arn:{partition}:omics:{region}:{account}:runGroup/{id}", ARNMember: "arn", Generate: map[string]any{"creationTime": "$now"}},
		{Kind: "runCache", Shape: "GetRunCacheResponse", Summary: "RunCacheListItem", ID: "name", NameMember: "id", IDMember: "id", Limit: 25,
			ARN: "arn:{partition}:omics:{region}:{account}:runCache/{id}", ARNMember: "arn", Aliases: map[string]string{"cacheS3Location": "cacheS3Uri"},
			Generate: map[string]any{"status": "ACTIVE", "cacheBehavior": "CACHE_ON_FAILURE", "creationTime": "$now"}},
		{Kind: "sequenceStore", Shape: "GetSequenceStoreResponse", Summary: "SequenceStoreDetail", ID: "name", NameMember: "id", IDMember: "id", Limit: 15,
			ARN: "arn:{partition}:omics:{region}:{account}:sequenceStore/{id}", ARNMember: "arn",
			Generate: map[string]any{"status": "CREATING", "eTagAlgorithmFamily": "MD5up", "creationTime": "$now", "updateTime": "$now"}},
		{Kind: "referenceStore", Shape: "GetReferenceStoreResponse", Summary: "ReferenceStoreDetail", ID: "name", NameMember: "id", IDMember: "id", Limit: 5,
			ARN: "arn:{partition}:omics:{region}:{account}:referenceStore/{id}", ARNMember: "arn", Generate: map[string]any{"creationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateRunGroup", Kind: Create, Resource: "runGroup", Check: omicsIdentity("CreateRunGroup", "requestId")},
		{Name: "GetRunGroup", Kind: Read, Resource: "runGroup", Lookup: "id"},
		{Name: "ListRunGroups", Kind: List, Resource: "runGroup", Filters: map[string]string{"name": "name"}},
		{Name: "UpdateRunGroup", Kind: Update, Resource: "runGroup", Lookup: "id", Fields: []string{"name", "maxCpus", "maxRuns", "maxDuration", "maxGpus"}},
		{Name: "DeleteRunGroup", Kind: Delete, Resource: "runGroup", Lookup: "id"},
		{Name: "CreateRunCache", Kind: Create, Resource: "runCache", Check: omicsCreate("CreateRunCache", "requestId", "cacheS3Location")},
		{Name: "GetRunCache", Kind: Read, Resource: "runCache", Lookup: "id"},
		{Name: "ListRunCaches", Kind: List, Resource: "runCache"},
		{Name: "UpdateRunCache", Kind: Update, Resource: "runCache", Lookup: "id", Fields: []string{"cacheBehavior", "description", "name"}},
		{Name: "DeleteRunCache", Kind: Delete, Resource: "runCache", Lookup: "id"},
		{Name: "CreateSequenceStore", Kind: Create, Resource: "sequenceStore", Check: omicsCreate("CreateSequenceStore", "clientToken", "fallbackLocation")},
		{Name: "GetSequenceStore", Kind: Read, Resource: "sequenceStore", Lookup: "id"},
		{Name: "ListSequenceStores", Kind: List, Resource: "sequenceStore", Check: omicsStoreFilter(true),
			Filters: map[string]string{"name": "name", "status": "status"}},
		{Name: "UpdateSequenceStore", Kind: Update, Resource: "sequenceStore", Lookup: "id", Accept: []string{"clientToken"}, Touch: []string{"updateTime"}, Check: omicsLocation("fallbackLocation"),
			Fields: []string{"name", "description", "fallbackLocation", "propagatedSetLevelTags"}},
		{Name: "DeleteSequenceStore", Kind: Delete, Resource: "sequenceStore", Lookup: "id"},
		{Name: "CreateReferenceStore", Kind: Create, Resource: "referenceStore", Accept: []string{"clientToken"}, Check: omicsIdentity("CreateReferenceStore", "")},
		{Name: "GetReferenceStore", Kind: Read, Resource: "referenceStore", Lookup: "id"},
		{Name: "ListReferenceStores", Kind: List, Resource: "referenceStore", Check: omicsStoreFilter(false), Filters: map[string]string{"name": "name"}},
		{Name: "DeleteReferenceStore", Kind: Delete, Resource: "referenceStore", Lookup: "id"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Run groups, run caches, sequence stores and reference stores are stored configuration records: no workflow runs, no run task, cache entry, read set, reference genome or S3 access point exists, nothing is imported, exported, activated or uploaded, and every other official operation returns UnknownOperationException with HTTP 501. A sequence store stays CREATING and holds no sequence data; run caches are ACTIVE records whose S3 location is stored without being read or written.",
		"Workflows and workflow versions are not implemented: their definitions are ZIP archives or repository references whose content the official output only returns as presigned URLs, which the simulator does not hold. Annotation stores, variant stores and their versions are not implemented: a store needs a reference genome that only an import job can create. Shares, S3 access policies, multipart read set uploads and every start, cancel and job operation are not implemented either.",
		"Identifiers are numeric as the official patterns require (10 digits). When the request carries its idempotency token (requestId, clientToken) the identifier is derived from the token and operation, so replaying the same request returns the same record and reusing a token with different parameters fails with ConflictException; a token reused after its 24-hour replay window can recreate an identifier. CreateReferenceStore models no idempotency token: its clientToken is accepted but not recorded. Names need not be unique. Resource counts per Region (25 run groups, 25 run caches, 15 sequence stores, 5 reference stores) are local limits.",
		"s3AccessConfig of a sequence store is refused because it would create an S3 access point. sseConfig, fallbackLocation, propagatedSetLevelTags and eTagAlgorithmFamily (MD5up when omitted, as documented) are stored without contacting KMS or S3, and S3 locations (cacheS3Location, fallbackLocation) must be s3://bucket with an optional prefix, which the official pattern states but RE2 cannot enforce. ListSequenceStores filters by name and status and ListReferenceStores by name, with the created and updated time bounds refused. Deleting a store or a run group deletes the record without a read set, reference or run check because none can exist.",
	},
}

// omicsNumericID returns the 10 digit identifier the official patterns
// require, derived from the idempotency token when there is one so that a
// replayed request keeps its identity and fingerprint.
func omicsNumericID(operation, token string) string {
	seed := make([]byte, 8)
	if token != "" {
		sum := sha256.Sum256([]byte(operation + "\x00" + token))
		copy(seed, sum[:8])
	} else {
		_, _ = rand.Read(seed) // never fails since Go 1.24
	}
	return strconv.FormatUint(binary.BigEndian.Uint64(seed)%9_000_000_000+1_000_000_000, 10)
}

// omicsIdentity assigns the numeric identifier a create operation stores.
func omicsIdentity(operation, tokenMember string) func(map[string]any) error {
	return func(input map[string]any) error {
		token, _ := input[tokenMember].(string)
		input["id"] = omicsNumericID(operation, token)
		return nil
	}
}

// omicsS3Location is the documented S3 location of run caches and fallback
// locations: a bucket name, optionally followed by a prefix. The official
// pattern repeats a count RE2 cannot compile, so the shape does not enforce it.
var omicsS3Location = regexp.MustCompile(`^s3://[a-z0-9][a-z0-9.-]{1,61}[a-z0-9](/.*)?$`)

// omicsLocation requires the named members, when supplied, to be S3 locations.
func omicsLocation(members ...string) func(map[string]any) error {
	return func(input map[string]any) error {
		for _, member := range members {
			if text, ok := input[member].(string); ok && text != "" && !omicsS3Location.MatchString(text) {
				return Failure("ValidationException", member+" must be an S3 location such as s3://bucket/prefix.")
			}
		}
		return nil
	}
}

// omicsCreate assigns the numeric identifier and checks the S3 locations.
func omicsCreate(operation, tokenMember string, locations ...string) func(map[string]any) error {
	identity, check := omicsIdentity(operation, tokenMember), omicsLocation(locations...)
	return func(input map[string]any) error {
		if err := check(input); err != nil {
			return err
		}
		return identity(input)
	}
}

// omicsStoreFilter flattens the equality members of a store list Filter onto
// the stored members they compare and refuses the time bounds.
func omicsStoreFilter(withStatus bool) func(map[string]any) error {
	return func(input map[string]any) error {
		filter, ok := input["filter"].(map[string]any)
		if !ok {
			return nil
		}
		delete(input, "filter")
		for member, value := range filter {
			if member != "name" && !(withStatus && member == "status") {
				return Failure("ValidationException", "Filter "+member+" is not supported by the simulator.")
			}
			input[member] = value
		}
		return nil
	}
}

// keyspaces serves Amazon Keyspaces keyspaces, tables and user-defined types
// as configuration records. No Cassandra data plane exists.
var keyspaces = &Service{
	Model: "keyspaces", Title: "Amazon Keyspaces (for Apache Cassandra)", IAMPrefix: "cassandra", Issues: []int{126},
	Resources: []*Resource{
		{Kind: "keyspace", Shape: "GetKeyspaceResponse", Summary: "KeyspaceSummary", ID: "name", Unique: true, Limit: 50, Cascade: true,
			ARN:       "arn:{partition}:cassandra:{region}:{account}:/keyspace/{name}/",
			ARNMember: "resourceArn", NameMember: "keyspaceName", Generate: map[string]any{"replicationStrategy": "SINGLE_REGION"}},
		{Kind: "table", Shape: "GetTableResponse", Summary: "TableSummary", Parent: "keyspace", ParentMember: "keyspaceName", ID: "name", Unique: true, Limit: 50,
			ARN:       "arn:{partition}:cassandra:{region}:{account}:/keyspace/{parentName}/table/{name}",
			ARNMember: "resourceArn", NameMember: "tableName",
			Generate: map[string]any{"keyspaceName": "$parentName", "status": "ACTIVE", "creationTimestamp": "$now",
				"capacitySpecification":   map[string]any{"throughputMode": "PAY_PER_REQUEST"},
				"encryptionSpecification": map[string]any{"type": "AWS_OWNED_KMS_KEY"},
				"pointInTimeRecovery":     map[string]any{"status": "DISABLED"}}},
		{Kind: "type", Shape: "GetTypeResponse", Parent: "keyspace", ParentMember: "keyspaceName", ID: "name", Unique: true, Limit: 50,
			ARN:        "arn:{partition}:cassandra:{region}:{account}:/keyspace/{parentName}/type/{name}",
			NameMember: "typeName",
			Generate: map[string]any{"keyspaceName": "$parentName", "keyspaceArn": "$parentArn", "status": "ACTIVE", "lastModifiedTimestamp": "$now",
				"directReferringTables": []any{}, "directParentTypes": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateKeyspace", Kind: Create, Resource: "keyspace", Action: "Create", From: "replicationSpecification", Check: keyspacesCreateKeyspace},
		{Name: "GetKeyspace", Kind: Read, Resource: "keyspace", Action: "Select", Lookup: "keyspaceName"},
		{Name: "ListKeyspaces", Kind: List, Resource: "keyspace", Action: "Select"},
		{Name: "DeleteKeyspace", Kind: Delete, Resource: "keyspace", Action: "Drop", Lookup: "keyspaceName"},
		{Name: "CreateTable", Kind: Create, Resource: "table", Action: "Create", Parent: "keyspaceName", Check: keyspacesCreateTable},
		{Name: "GetTable", Kind: Read, Resource: "table", Action: "Select", Parent: "keyspaceName", Lookup: "tableName"},
		{Name: "ListTables", Kind: List, Resource: "table", Action: "Select", Parent: "keyspaceName"},
		{Name: "UpdateTable", Kind: Update, Resource: "table", Action: "Alter", Parent: "keyspaceName", Lookup: "tableName", Check: keyspacesUpdateTable,
			Fields: []string{"capacitySpecification", "encryptionSpecification", "pointInTimeRecovery", "ttl", "defaultTimeToLive", "clientSideTimestamps"}},
		{Name: "DeleteTable", Kind: Delete, Resource: "table", Action: "Drop", Parent: "keyspaceName", Lookup: "tableName"},
		{Name: "CreateType", Kind: Create, Resource: "type", Action: "Create", Parent: "keyspaceName", Check: keyspacesCreateType},
		{Name: "GetType", Kind: Read, Resource: "type", Action: "Select", Parent: "keyspaceName", Lookup: "typeName", Check: keyspacesTypeName},
		{Name: "ListTypes", Kind: List, Resource: "type", Action: "Select", Parent: "keyspaceName", ListValue: "typeName"},
		{Name: "DeleteType", Kind: Delete, Resource: "type", Action: "Drop", Parent: "keyspaceName", Lookup: "typeName", Check: keyspacesTypeName},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Action: "Select", Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Keyspaces, tables and user-defined types are stored configuration records: no Cassandra data plane, CQL endpoint, row, partition or capacity exists, tables are ACTIVE at once, and RestoreTable, GetTableAutoScalingSettings, UpdateKeyspace and UntagResource return UnknownOperationException with HTTP 501. The AWS-owned system keyspaces are not listed, and creating a keyspace with one of their names (system, system_schema, system_schema_mcs, system_multiregion_info) fails with ConflictException.",
		"Only single-Region keyspaces exist: replicationSpecification must be SINGLE_REGION (the default) without a regionList, so UpdateKeyspace, which can only add a Region, is not implemented, replicaSpecifications are refused and no replication status is reported. CreateTable and UpdateTable refuse cdcSpecification and replicaSpecifications; autoScalingSpecification and addColumns are refused because their effects cannot be stored, so a table schema cannot change after creation. UntagResource is not implemented because it takes key and value pairs and the catalog removes tags by key.",
		"Table schemas must name at least one partition key, define every column once (names compare case-insensitively), list partition, clustering and static columns among the columns without reusing a column as both a key and a static column, and use CQL types: the scalar types of the Keyspaces data type list, or list, set, map, tuple and frozen collections of them with user-defined types only inside frozen; user-defined type names are not resolved. Capacity defaults to PAY_PER_REQUEST and PROVISIONED needs readCapacityUnits and writeCapacityUnits, a customer managed key needs kmsKeyIdentifier (stored without contacting KMS), and encryption defaults to AWS_OWNED_KMS_KEY and point-in-time recovery to DISABLED; no earliest restorable time, streams or table size are reported.",
		"User-defined type names are 1 to 48 letters, digits and underscores starting with a letter and are stored in lowercase as documented (double-quoted names are refused); fields need distinct names and CQL types, references between types are not resolved, so directReferringTables and directParentTypes stay empty and maxNestingDepth is not reported. Keyspace and table names are stored as given, are unique per Region and keyspace, and a keyspace deletes its tables and types with it. Resource counts (50 keyspaces per Region, 50 tables and 50 types per keyspace) are local limits.",
		"Operations are authorized as the Keyspaces IAM actions cassandra:Create, Select, Alter, Drop and TagResource on the keyspace, table or type ARN (arn:aws:cassandra:region:account:/keyspace/name/table/name), without the additional Select on the system keyspace that AWS requires for the read operations. ListTagsForResource refuses nextToken and maxResults and returns all tags.",
	},
}

// keyspacesSystem are the AWS-owned keyspaces every account has.
var keyspacesSystem = []string{"system", "system_schema", "system_schema_mcs", "system_multiregion_info"}

// keyspacesTypeNamePattern is the documented user-defined type name rule.
var keyspacesTypeNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,47}$`)

// keyspacesIdentifier is an unquoted CQL identifier naming a user-defined type.
var keyspacesIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var (
	// keyspacesScalars are the scalar types of the Keyspaces data type list.
	keyspacesScalars = []string{"ascii", "bigint", "blob", "boolean", "counter", "date", "decimal", "double", "float", "inet", "int", "smallint",
		"text", "time", "timestamp", "timeuuid", "tinyint", "uuid", "varchar", "varint"}
	keyspacesCollections = []string{"list", "set", "map", "tuple", "frozen"}
)

// keyspacesCQLType checks a CQL type: a scalar type, or a collection of
// them where user-defined types are allowed only inside frozen.
func keyspacesCQLType(text string, frozen bool) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	head, rest, generic := strings.Cut(text, "<")
	head = strings.TrimSpace(head)
	if !generic {
		return slices.Contains(keyspacesScalars, text) || frozen && keyspacesIdentifier.MatchString(text)
	}
	if !slices.Contains(keyspacesCollections, head) || !strings.HasSuffix(rest, ">") {
		return false
	}
	rest = strings.TrimSuffix(rest, ">")
	depth, start := 0, 0
	arguments := []string{}
	for i, c := range rest {
		switch c {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				arguments = append(arguments, rest[start:i])
				start = i + 1
			}
		}
	}
	arguments = append(arguments, rest[start:])
	if depth != 0 || head == "frozen" && len(arguments) != 1 || head == "map" && len(arguments) != 2 || (head == "list" || head == "set") && len(arguments) != 1 {
		return false
	}
	for _, argument := range arguments {
		if !keyspacesCQLType(argument, head == "frozen") {
			return false
		}
	}
	return true
}

func keyspacesCreateKeyspace(input map[string]any) error {
	if name, _ := input["keyspaceName"].(string); slices.Contains(keyspacesSystem, strings.ToLower(name)) {
		return Failure("ConflictException", "The keyspace "+name+" is an AWS-owned system keyspace.")
	}
	if spec, ok := input["replicationSpecification"].(map[string]any); ok {
		if spec["replicationStrategy"] != "SINGLE_REGION" || spec["regionList"] != nil {
			return Failure("ValidationException", "Only single-Region keyspaces are supported: use SINGLE_REGION without regionList.")
		}
	}
	return nil
}

// keyspacesSchema applies the documented rules of a table schema.
func keyspacesSchema(schema map[string]any) error {
	columns := map[string]bool{}
	all, _ := schema["allColumns"].([]any)
	for _, item := range all {
		column := item.(map[string]any)
		name := strings.ToLower(column["name"].(string))
		if columns[name] {
			return Failure("ValidationException", "The column "+name+" is defined twice.")
		}
		columns[name] = true
		if !keyspacesCQLType(column["type"].(string), false) {
			return Failure("ValidationException", "The type of column "+name+" is not a supported CQL data type.")
		}
	}
	keys := map[string]bool{}
	for _, member := range []string{"partitionKeys", "clusteringKeys", "staticColumns"} {
		entries, _ := schema[member].([]any)
		if member == "partitionKeys" && len(entries) == 0 {
			return Failure("ValidationException", "A table needs at least one partition key.")
		}
		for _, item := range entries {
			name := strings.ToLower(item.(map[string]any)["name"].(string))
			if !columns[name] {
				return Failure("ValidationException", "The "+member+" entry "+name+" is not one of allColumns.")
			}
			if member != "staticColumns" && keys[name] {
				return Failure("ValidationException", "The key column "+name+" is listed twice.")
			}
			if member == "staticColumns" && keys[name] {
				return Failure("ValidationException", "The key column "+name+" cannot be static.")
			}
			if member != "staticColumns" {
				keys[name] = true
			}
		}
	}
	return nil
}

// keyspacesTableOptions checks the capacity and encryption settings both
// table operations accept, and refuses the settings that need a second Region
// or a change stream.
func keyspacesTableOptions(input map[string]any) error {
	for _, member := range []string{"replicaSpecifications", "cdcSpecification"} {
		if _, ok := input[member]; ok {
			return Failure("ValidationException", member+" is not supported: the simulator has one Region and no change streams.")
		}
	}
	if spec, ok := input["capacitySpecification"].(map[string]any); ok {
		_, read := spec["readCapacityUnits"]
		_, write := spec["writeCapacityUnits"]
		switch {
		case spec["throughputMode"] == "PROVISIONED" && !(read && write):
			return Failure("ValidationException", "Provisioned capacity mode requires readCapacityUnits and writeCapacityUnits.")
		case spec["throughputMode"] == "PAY_PER_REQUEST" && (read || write):
			return Failure("ValidationException", "On-demand capacity mode takes no capacity units.")
		}
	}
	if spec, ok := input["encryptionSpecification"].(map[string]any); ok {
		_, key := spec["kmsKeyIdentifier"]
		switch {
		case spec["type"] == "CUSTOMER_MANAGED_KMS_KEY" && !key:
			return Failure("ValidationException", "A customer managed key requires kmsKeyIdentifier.")
		case spec["type"] == "AWS_OWNED_KMS_KEY" && key:
			return Failure("ValidationException", "kmsKeyIdentifier applies only to a customer managed key.")
		}
	}
	return nil
}

func keyspacesCreateTable(input map[string]any) error {
	if err := keyspacesTableOptions(input); err != nil {
		return err
	}
	schema, _ := input["schemaDefinition"].(map[string]any)
	return keyspacesSchema(schema)
}

func keyspacesUpdateTable(input map[string]any) error {
	if _, ok := input["addColumns"]; ok {
		return Failure("ValidationException", "addColumns is not supported: a table schema cannot change after creation.")
	}
	return keyspacesTableOptions(input)
}

// keyspacesTypeName lowercases a type name, as Keyspaces does for unquoted names.
func keyspacesTypeName(input map[string]any) error {
	lowerMember(input, "typeName")
	return nil
}

func keyspacesCreateType(input map[string]any) error {
	if name, _ := input["typeName"].(string); !keyspacesTypeNamePattern.MatchString(name) {
		return Failure("ValidationException", "Type names are 1 to 48 letters, digits and underscores starting with a letter; double-quoted names are not supported.")
	}
	lowerMember(input, "typeName")
	seen := map[string]bool{}
	fields, _ := input["fieldDefinitions"].([]any)
	if len(fields) == 0 {
		return Failure("ValidationException", "A type needs at least one field.")
	}
	for _, item := range fields {
		field := item.(map[string]any)
		name := strings.ToLower(field["name"].(string))
		if seen[name] {
			return Failure("ValidationException", "The field "+name+" is defined twice.")
		}
		seen[name] = true
		if !keyspacesCQLType(field["type"].(string), false) {
			return Failure("ValidationException", "The type of field "+name+" is not a supported CQL data type.")
		}
	}
	return nil
}

// grafana serves Amazon Managed Grafana workspaces as configuration records.
var grafana = &Service{
	Model: "grafana", Title: "Amazon Managed Grafana", IAMPrefix: "grafana", Issues: []int{201},
	Resources: []*Resource{
		{Kind: "workspace", Shape: "WorkspaceDescription", Summary: "WorkspaceSummary", ID: "hex:10:g-", IDMember: "id", Limit: 10,
			ARN: "arn:{partition}:grafana:{region}:{account}:/workspaces/{id}", ARNMember: "",
			Aliases: map[string]string{"workspaceName": "name", "workspaceDescription": "description", "workspaceDataSources": "dataSources",
				"workspaceNotificationDestinations": "notificationDestinations", "workspaceOrganizationalUnits": "organizationalUnits",
				"removeVpcConfiguration": "vpcConfiguration", "removeNetworkAccessConfiguration": "networkAccessControl"},
			Generate: map[string]any{"status": "ACTIVE", "created": "$now", "modified": "$now", "grafanaVersion": grafanaDefaultVersion, "dataSources": []any{}}},
	},
	Ops: []Op{
		{Name: "CreateWorkspace", Kind: Create, Resource: "workspace", Check: grafanaCreate},
		{Name: "DescribeWorkspace", Kind: Read, Resource: "workspace", Lookup: "workspaceId"},
		{Name: "ListWorkspaces", Kind: List, Resource: "workspace"},
		{Name: "UpdateWorkspace", Kind: Update, Resource: "workspace", Lookup: "workspaceId", Touch: []string{"modified"}, Check: grafanaUpdate, Merged: grafanaMerged,
			Fields: []string{"accountAccessType", "networkAccessControl", "organizationRoleName", "permissionType", "removeNetworkAccessConfiguration",
				"removeVpcConfiguration", "stackSetName", "vpcConfiguration", "workspaceDataSources", "workspaceDescription", "workspaceName",
				"workspaceNotificationDestinations", "workspaceOrganizationalUnits", "workspaceRoleArn"}},
		{Name: "DeleteWorkspace", Kind: Delete, Resource: "workspace", Lookup: "workspaceId", Set: map[string]any{"status": "DELETING"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Workspaces are stored configuration records that are ACTIVE at once: no Grafana server, workspace URL, dashboard, data source connection, notification channel or license exists, endpoint is never returned, and the Grafana API, console and every other official operation return UnknownOperationException with HTTP 501. No VPC endpoint, network interface or IAM role is created or checked.",
		"Workspace authentication, configuration and permissions are not implemented: DescribeWorkspaceAuthentication, UpdateWorkspaceAuthentication, DescribeWorkspaceConfiguration, UpdateWorkspaceConfiguration, ListPermissions, UpdatePermissions, ListVersions and the license operations return HTTP 501, and the configuration member of CreateWorkspace is refused. API keys, service accounts and their tokens are secrets that Amazon Managed Grafana generates, so CreateWorkspaceApiKey, DeleteWorkspaceApiKey and the service account and token operations return HTTP 501 and no key or token is ever stored or returned.",
		"CreateWorkspace stores authenticationProviders as the authentication summary, with samlConfigurationStatus NOT_CONFIGURED when SAML is listed because SAML cannot be configured. Only CUSTOMER_MANAGED permissions are accepted, as the documentation prescribes for the API, and they need a workspaceRoleArn (an IAM role ARN stored without checking that the role exists); ORGANIZATION account access needs workspaceOrganizationalUnits. grafanaVersion is stored as given when it looks like 10.4 and defaults to 10.4, the example the documentation gives for the latest version; it is never upgraded and no version list is served.",
		"networkAccessControl prefix list IDs must look like pl-1a2b3c4d and VPC configurations need 2 to 6 distinct subnet IDs and 1 to 5 distinct security group IDs, none of which are resolved. UpdateWorkspace keeps omitted settings, removeVpcConfiguration and removeNetworkAccessConfiguration clear a stored setting and are refused together with a replacement, and the merged workspace must still satisfy the rules above. Workspace names need not be unique, ARNs are arn:aws:grafana:region:account:/workspaces/id, and 10 workspaces per Region is a local limit. Deleting a workspace removes the record and returns it with status DELETING.",
	},
}

// grafanaDefaultVersion is the example the documentation gives for the
// latest Grafana version, used when CreateWorkspace names none.
const grafanaDefaultVersion = "10.4"

var (
	grafanaVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	grafanaPrefix  = regexp.MustCompile(`^pl-[0-9a-f]{8}([0-9a-f]{9})?$`)
)

// grafanaDistinct requires the identifiers of a list member to differ.
func grafanaDistinct(config map[string]any, member string) error {
	seen := map[string]bool{}
	ids, _ := config[member].([]any)
	for _, id := range ids {
		text, _ := id.(string)
		if seen[text] {
			return Failure("ValidationException", member+" must not contain duplicates.")
		}
		seen[text] = true
	}
	return nil
}

// grafanaSettings applies the documented rules of the network, VPC, role and permission members.
func grafanaSettings(input map[string]any) error {
	if access, ok := input["networkAccessControl"].(map[string]any); ok {
		ids, _ := access["prefixListIds"].([]any)
		for _, id := range ids {
			if text, _ := id.(string); !grafanaPrefix.MatchString(text) {
				return Failure("ValidationException", "Prefix list IDs look like pl-1a2b3c4d.")
			}
		}
	}
	if vpc, ok := input["vpcConfiguration"].(map[string]any); ok {
		for _, member := range []string{"subnetIds", "securityGroupIds"} {
			if err := grafanaDistinct(vpc, member); err != nil {
				return err
			}
		}
	}
	if arn, ok := input["workspaceRoleArn"].(string); ok && !iamRoleARN.MatchString(arn) {
		return Failure("ValidationException", "workspaceRoleArn must be an IAM role ARN.")
	}
	if kind, ok := input["permissionType"].(string); ok && kind != "CUSTOMER_MANAGED" {
		return Failure("ValidationException", "Only the CUSTOMER_MANAGED permission type is supported: the simulator creates no IAM roles.")
	}
	return nil
}

func grafanaCreate(input map[string]any) error {
	if err := grafanaSettings(input); err != nil {
		return err
	}
	if version, ok := input["grafanaVersion"].(string); ok && !grafanaVersion.MatchString(version) {
		return Failure("ValidationException", "grafanaVersion looks like 10.4.")
	}
	if _, ok := input["workspaceRoleArn"]; !ok {
		return Failure("ValidationException", "CUSTOMER_MANAGED permissions require workspaceRoleArn.")
	}
	if units, _ := input["workspaceOrganizationalUnits"].([]any); input["accountAccessType"] == "ORGANIZATION" && len(units) == 0 {
		return Failure("ValidationException", "ORGANIZATION account access requires workspaceOrganizationalUnits.")
	}
	providers, _ := input["authenticationProviders"].([]any)
	summary := map[string]any{"providers": providers}
	if slices.Contains(providers, any("SAML")) {
		summary["samlConfigurationStatus"] = "NOT_CONFIGURED"
	}
	delete(input, "authenticationProviders")
	input["authentication"] = summary
	return nil
}

// grafanaUpdate drops the removal flags set to false and refuses a flag
// combined with the replacement it removes.
func grafanaUpdate(input map[string]any) error {
	for flag, member := range map[string]string{"removeVpcConfiguration": "vpcConfiguration", "removeNetworkAccessConfiguration": "networkAccessControl"} {
		switch value, present := input[flag]; {
		case !present:
		case value != true:
			delete(input, flag)
		case input[member] != nil:
			return Failure("ValidationException", flag+" cannot be combined with "+member+".")
		}
	}
	return grafanaSettings(input)
}

// grafanaMerged clears the settings the removal flags name and requires the
// merged workspace to keep a role for customer managed permissions and
// organizational units for organization access.
func grafanaMerged(document, supplied map[string]any) error {
	for flag, member := range map[string]string{"removeVpcConfiguration": "vpcConfiguration", "removeNetworkAccessConfiguration": "networkAccessControl"} {
		if supplied[flag] == true {
			delete(document, member)
		}
	}
	if units, _ := document["organizationalUnits"].([]any); document["accountAccessType"] == "ORGANIZATION" && len(units) == 0 {
		return Failure("ValidationException", "ORGANIZATION account access requires workspaceOrganizationalUnits.")
	}
	return nil
}
