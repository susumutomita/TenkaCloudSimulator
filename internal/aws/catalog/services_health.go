package catalog

import (
	"encoding/json"
	"slices"
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
