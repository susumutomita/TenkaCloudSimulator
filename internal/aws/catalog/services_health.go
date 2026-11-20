package catalog

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
