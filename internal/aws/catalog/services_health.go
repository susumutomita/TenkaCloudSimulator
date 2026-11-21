package catalog

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
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
