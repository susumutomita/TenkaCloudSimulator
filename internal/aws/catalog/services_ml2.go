package catalog

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Reviewed bounded specifications for four more machine learning services:
// Amazon Forecast, Amazon Fraud Detector, Amazon Personalize and Amazon
// CodeGuru Profiler. Only stored configuration is bound. Nothing that
// imports data, trains, infers, evaluates rules or produces findings is.

// ml2RefuseNonEmpty refuses a list member that would reference or hold data
// the stored model cannot resolve, and drops it when it is empty.
func ml2RefuseNonEmpty(input map[string]any, code, message string, members ...string) error {
	for _, member := range members {
		if values, ok := input[member].([]any); ok {
			if len(values) > 0 {
				return Failure(code, message)
			}
			delete(input, member)
		}
	}
	return nil
}

// ml2ReservedTagKeys refuses the aws: key prefix the Forecast tagging
// documentation reserves, in any letter case.
func ml2ReservedTagKeys(input map[string]any, member, keyMember, code string) error {
	tags, _ := input[member].([]any)
	for _, tag := range tags {
		pair, _ := tag.(map[string]any)
		if key, _ := pair[keyMember].(string); strings.HasPrefix(strings.ToLower(key), "aws:") {
			return Failure(code, "Tag keys must not start with aws:, which is reserved.")
		}
	}
	return nil
}

var (
	forecastFrequency = regexp.MustCompile(`^([1-9][0-9]?)(Y|M|W|D|H|min)$`)
	// forecastFrequencyMax are the documented valid values of each unit.
	forecastFrequencyMax = map[string]int{"min": 59, "H": 23, "D": 6, "W": 4, "M": 11, "Y": 1}
)

// forecast serves datasets and dataset groups. Nothing is imported or
// trained, so no import job, predictor or forecast is bound.
var forecast = &Service{
	Model: "forecast", Title: "Amazon Forecast", IAMPrefix: "forecast", Issues: []int{169},
	Resources: []*Resource{
		{Kind: "dataset", Shape: "DescribeDatasetResponse", Summary: "DatasetSummary", ID: "name",
			ARN:       "arn:{partition}:forecast:{region}:{account}:dataset/{name}",
			ARNMember: "DatasetArn", NameMember: "DatasetName", Unique: true, Limit: 100,
			Generate: map[string]any{"Status": "ACTIVE", "CreationTime": "$now", "LastModificationTime": "$now"}},
		{Kind: "datasetGroup", Shape: "DescribeDatasetGroupResponse", Summary: "DatasetGroupSummary", ID: "name",
			ARN:       "arn:{partition}:forecast:{region}:{account}:dataset-group/{name}",
			ARNMember: "DatasetGroupArn", NameMember: "DatasetGroupName", Unique: true, Limit: 100,
			Generate: map[string]any{"Status": "ACTIVE", "CreationTime": "$now", "LastModificationTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateDataset", Kind: Create, Resource: "dataset", Check: forecastDataset},
		{Name: "DescribeDataset", Kind: Read, Resource: "dataset", Lookup: "DatasetArn"},
		{Name: "ListDatasets", Kind: List, Resource: "dataset"},
		{Name: "DeleteDataset", Kind: Delete, Resource: "dataset", Lookup: "DatasetArn"},
		{Name: "CreateDatasetGroup", Kind: Create, Resource: "datasetGroup", Check: forecastDatasetGroup},
		{Name: "DescribeDatasetGroup", Kind: Read, Resource: "datasetGroup", Lookup: "DatasetGroupArn"},
		{Name: "ListDatasetGroups", Kind: List, Resource: "datasetGroup"},
		{Name: "DeleteDatasetGroup", Kind: Delete, Resource: "datasetGroup", Lookup: "DatasetGroupArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn", Check: forecastTagResource},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn", Check: forecastUntagResource},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Datasets and dataset groups are stored ACTIVE configuration records: no data is imported, nothing is stored in a dataset, and no predictor, forecast, explainability, monitor, what-if analysis, export or import job exists, so the Create, Describe, List and Delete operations of all of those, StopResource, ResumeResource, GetAccuracyMetrics and DeleteResourceTree return UnknownOperationException with HTTP 501. QueryForecast belongs to the separate forecastquery service and is not served.",
		"UpdateDatasetGroup is not implemented and CreateDatasetGroup refuses a non-empty DatasetArns, because a dataset group would have to resolve each dataset and compare its Domain with the group's, which the stored model cannot express. A dataset group therefore holds no datasets and reports no DatasetArns, and deleting a dataset never needs to update a group.",
		"A dataset is stored as given: the Domain and DatasetType combination, the fields the schema must contain for them (for example item_id, timestamp and demand for RETAIL and TARGET_TIME_SERIES) and the encryption role and key are not checked. DataFrequency is required for RELATED_TIME_SERIES datasets and must be an integer followed by min, H, D, W, M or Y within the documented range of that unit (min 1-59, H 1-23, D 1-6, W 1-4, M 1-11, Y 1). The engine first applies the official shape pattern, which as published lists only the literals Y, M, W, D, H, 30min, 15min, 10min, 5min and 1min, so only 1min, 5min, 10min, 15min and 30min are accepted here and values such as 1D or 2W that AWS accepts are refused with InvalidInputException.",
		"Dataset and dataset group names are unique per Region and Forecast limits are local: 100 datasets and 100 dataset groups per Region. Deleting a resource never fails with ResourceInUseException because nothing can use one.",
		"Tags follow the documented rules that keys must not start with aws: (any letter case) and that a resource holds at most 50; the length limits come from the official shapes. TagResource and UntagResource refuse keys with the reserved prefix.",
	},
}

func forecastDataset(input map[string]any) error {
	if input["DatasetType"] == "RELATED_TIME_SERIES" {
		if _, ok := input["DataFrequency"]; !ok {
			return Failure("InvalidInputException", "DataFrequency is required for RELATED_TIME_SERIES datasets.")
		}
	}
	if frequency, ok := input["DataFrequency"].(string); ok {
		match := forecastFrequency.FindStringSubmatch(frequency)
		if match == nil {
			return Failure("InvalidInputException", "DataFrequency must be an integer followed by min, H, D, W, M or Y, such as 1D or 15min.")
		}
		if count, _ := strconv.Atoi(match[1]); count > forecastFrequencyMax[match[2]] {
			return Failure("InvalidInputException", "DataFrequency "+frequency+" is outside the valid range of its unit: min 1-59, H 1-23, D 1-6, W 1-4, M 1-11, Y 1.")
		}
	}
	return ml2ReservedTagKeys(input, "Tags", "Key", "InvalidInputException")
}

func forecastDatasetGroup(input map[string]any) error {
	if err := ml2RefuseNonEmpty(input, "InvalidInputException",
		"DatasetArns is not supported: the simulator does not resolve datasets or compare their Domain with the dataset group.", "DatasetArns"); err != nil {
		return err
	}
	return ml2ReservedTagKeys(input, "Tags", "Key", "InvalidInputException")
}

func forecastTagResource(input map[string]any) error {
	return ml2ReservedTagKeys(input, "Tags", "Key", "InvalidInputException")
}

func forecastUntagResource(input map[string]any) error {
	keys, _ := input["TagKeys"].([]any)
	for _, key := range keys {
		if text, _ := key.(string); strings.HasPrefix(strings.ToLower(text), "aws:") {
			return Failure("InvalidInputException", "Tag keys that start with aws: are reserved and cannot be removed.")
		}
	}
	return nil
}

// fraudVariableTypes are the documented valid values of a variable type.
var fraudVariableTypes = []string{"AUTH_CODE", "AVS", "BILLING_ADDRESS_L1", "BILLING_ADDRESS_L2", "BILLING_CITY", "BILLING_COUNTRY",
	"BILLING_NAME", "BILLING_PHONE", "BILLING_STATE", "BILLING_ZIP", "CARD_BIN", "CATEGORICAL", "CURRENCY_CODE", "EMAIL_ADDRESS",
	"FINGERPRINT", "FRAUD_LABEL", "FREE_FORM_TEXT", "IP_ADDRESS", "NUMERIC", "ORDER_ID", "PAYMENT_TYPE", "PHONE_NUMBER", "PRICE",
	"PRODUCT_CATEGORY", "SHIPPING_ADDRESS_L1", "SHIPPING_ADDRESS_L2", "SHIPPING_CITY", "SHIPPING_COUNTRY", "SHIPPING_NAME",
	"SHIPPING_PHONE", "SHIPPING_STATE", "SHIPPING_ZIP", "USERAGENT"}

// fraudNamed describes the three Put resources that carry only a name and a
// description; each is written with a Put and read with a Get by name.
func fraudNamed(kind, shape, path string) *Resource {
	return &Resource{Kind: kind, Shape: shape, ID: "name", ARN: "arn:{partition}:frauddetector:{region}:{account}:" + path + "/{name}",
		ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
		Generate: map[string]any{"createdTime": "$now", "lastUpdatedTime": "$now"}}
}

// fraudDetector serves entity types, labels, outcomes, variables and list
// metadata. Resources that other resources reference by name (event types
// and detectors), everything that is trained and everything that evaluates
// an event stay unbound.
var fraudDetector = &Service{
	Model: "frauddetector", Title: "Amazon Fraud Detector", IAMPrefix: "frauddetector", Issues: []int{170},
	Resources: []*Resource{
		fraudNamed("entityType", "EntityType", "entity-type"),
		fraudNamed("label", "Label", "label"),
		fraudNamed("outcome", "Outcome", "outcome"),
		{Kind: "variable", Shape: "Variable", ID: "name", ARN: "arn:{partition}:frauddetector:{region}:{account}:variable/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"createdTime": "$now", "lastUpdatedTime": "$now"}},
		{Kind: "list", Shape: "AllowDenyList", ID: "name", ARN: "arn:{partition}:frauddetector:{region}:{account}:list/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"createdTime": "$now", "updatedTime": "$now"}},
	},
	Ops: []Op{
		{Name: "PutEntityType", Kind: Put, Resource: "entityType", Lookup: "name", Touch: []string{"lastUpdatedTime"}},
		{Name: "GetEntityTypes", Kind: List, Resource: "entityType", Names: "name"},
		{Name: "DeleteEntityType", Kind: Delete, Resource: "entityType", Lookup: "name"},
		{Name: "PutLabel", Kind: Put, Resource: "label", Lookup: "name", Touch: []string{"lastUpdatedTime"}},
		{Name: "GetLabels", Kind: List, Resource: "label", Names: "name"},
		{Name: "DeleteLabel", Kind: Delete, Resource: "label", Lookup: "name"},
		{Name: "PutOutcome", Kind: Put, Resource: "outcome", Lookup: "name", Touch: []string{"lastUpdatedTime"}},
		{Name: "GetOutcomes", Kind: List, Resource: "outcome", Names: "name"},
		{Name: "DeleteOutcome", Kind: Delete, Resource: "outcome", Lookup: "name"},
		{Name: "CreateVariable", Kind: Create, Resource: "variable", Check: fraudVariable},
		{Name: "UpdateVariable", Kind: Update, Resource: "variable", Lookup: "name", Fields: []string{"defaultValue", "description", "variableType"},
			Touch: []string{"lastUpdatedTime"}, Check: fraudVariable},
		{Name: "GetVariables", Kind: List, Resource: "variable", Names: "name"},
		{Name: "DeleteVariable", Kind: Delete, Resource: "variable", Lookup: "name"},
		{Name: "CreateList", Kind: Create, Resource: "list", Check: fraudList},
		{Name: "UpdateList", Kind: Update, Resource: "list", Lookup: "name", Fields: []string{"description"}, Touch: []string{"updatedTime"}, Check: fraudList},
		{Name: "GetListsMetadata", Kind: List, Resource: "list", Names: "name"},
		{Name: "DeleteList", Kind: Delete, Resource: "list", Lookup: "name"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceARN"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceARN"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceARN"},
	},
	Bounds: []string{
		"Entity types, labels, outcomes, variables and list metadata are stored configuration records: nothing is trained, no event is received, scored or stored, and no rule or detector version is evaluated. Models, model versions, external models, batch import and prediction jobs, GetEventPrediction, SendEvent, GetEvent and every other data-plane operation return UnknownOperationException with HTTP 501.",
		"Event types, detectors, rules, detector versions and the KMS encryption key are not implemented. An event type and a detector reference variables, labels, entity types and event types by name, not by ARN, so the stored model can neither check that they exist nor refuse the deletion of one that is in use (ConflictException in AWS); because none of them can reference a record, deleting an entity type, label, outcome, variable or list never fails with ConflictException.",
		"Entity types, labels and outcomes are written with PutEntityType, PutLabel and PutOutcome, which create the record or replace its description and add tags, and are read with the matching Get operation, which returns one record by name or all of them. Names are 1 to 64 lowercase letters, digits, underscores or hyphens as the official shapes state; a Get with an unknown name fails with ResourceNotFoundException, and a Delete of a missing record fails with ValidationException because the delete operations model no not-found fault. Resource counts per Region (100 of each kind) are local limits.",
		"Variables must use the EVENT data source because model output variables are created by models, which do not exist here; the data type is checked against the documented enumeration and the default value is stored as a string without checking it against the data type. The variable type must be one of the documented values. UpdateVariable changes the default value, description and variable type; a variable is never reported as used by an event type.",
		"Lists are stored as metadata only: CreateList and UpdateList accept no elements (an empty list is ignored, so UpdateList with REPLACE and no elements clears nothing because nothing is stored), GetListElements is not implemented and a list is never used in a rule. A list variable type is assigned only by CreateList, because UpdateList cannot compare it with the stored one and is limited to the description.",
		"Tags apply to the five resource kinds by ARN (arn:aws:frauddetector:region:account:entity-type/name, label/name, outcome/name, variable/name and list/name). ListTagsForResource returns all tags without pagination, and tags given to a Put of an existing record are added to its tags.",
	},
}

func fraudVariable(input map[string]any) error {
	if source, ok := input["dataSource"].(string); ok && source != "EVENT" {
		return Failure("ValidationException", "dataSource must be EVENT: model and external model score variables are created by models, which the simulator does not run.")
	}
	if kind, ok := input["variableType"].(string); ok && !slices.Contains(fraudVariableTypes, kind) {
		return Failure("ValidationException", "variableType must be one of "+strings.Join(fraudVariableTypes, ", ")+".")
	}
	return nil
}

func fraudList(input map[string]any) error {
	if err := ml2RefuseNonEmpty(input, "ValidationException",
		"List elements are not supported: the simulator stores list metadata only.", "elements"); err != nil {
		return err
	}
	if kind, ok := input["variableType"].(string); ok && !slices.Contains(fraudVariableTypes, kind) {
		return Failure("ValidationException", "variableType must be one of "+strings.Join(fraudVariableTypes, ", ")+".")
	}
	delete(input, "updateMode")
	return nil
}

var personalizeDatasetTypes = []string{"interactions", "items", "users", "actions", "action_interactions"}

// personalize serves schemas, dataset groups, empty datasets and filters.
// Nothing is imported, trained, deployed or queried.
var personalize = &Service{
	Model: "personalize", Title: "Amazon Personalize", IAMPrefix: "personalize", Issues: []int{179},
	Resources: []*Resource{
		{Kind: "schema", Shape: "DatasetSchema", Summary: "DatasetSchemaSummary", ID: "name",
			ARN:       "arn:{partition}:personalize:{region}:{account}:schema/{name}",
			ARNMember: "schemaArn", NameMember: "name", Unique: true, Limit: 100, InUse: "ResourceInUseException",
			Generate: map[string]any{"creationDateTime": "$now", "lastUpdatedDateTime": "$now"}},
		{Kind: "datasetGroup", Shape: "DatasetGroup", Summary: "DatasetGroupSummary", ID: "name",
			ARN:       "arn:{partition}:personalize:{region}:{account}:dataset-group/{name}",
			ARNMember: "datasetGroupArn", NameMember: "name", Unique: true, Limit: 100, InUse: "ResourceInUseException",
			Generate: map[string]any{"status": "ACTIVE", "creationDateTime": "$now", "lastUpdatedDateTime": "$now"}},
		{Kind: "dataset", Shape: "Dataset", Summary: "DatasetSummary", ID: "uuid",
			ARN:       "arn:{partition}:personalize:{region}:{account}:dataset/{id}",
			ARNMember: "datasetArn", NameMember: "name", Limit: 500,
			Refs:     map[string]string{"schemaArn": "schema", "datasetGroupArn": "datasetGroup"},
			Generate: map[string]any{"status": "ACTIVE", "creationDateTime": "$now", "lastUpdatedDateTime": "$now"}},
		{Kind: "filter", Shape: "Filter", Summary: "FilterSummary", ID: "name",
			ARN:       "arn:{partition}:personalize:{region}:{account}:filter/{name}",
			ARNMember: "filterArn", NameMember: "name", Unique: true, Limit: 100,
			Refs:     map[string]string{"datasetGroupArn": "datasetGroup"},
			Generate: map[string]any{"status": "ACTIVE", "creationDateTime": "$now", "lastUpdatedDateTime": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateSchema", Kind: Create, Resource: "schema", Check: personalizeSchema},
		{Name: "DescribeSchema", Kind: Read, Resource: "schema", Lookup: "schemaArn"},
		{Name: "ListSchemas", Kind: List, Resource: "schema"},
		{Name: "DeleteSchema", Kind: Delete, Resource: "schema", Lookup: "schemaArn"},
		{Name: "CreateDatasetGroup", Kind: Create, Resource: "datasetGroup", Check: personalizeDatasetGroup},
		{Name: "DescribeDatasetGroup", Kind: Read, Resource: "datasetGroup", Lookup: "datasetGroupArn"},
		{Name: "ListDatasetGroups", Kind: List, Resource: "datasetGroup"},
		{Name: "DeleteDatasetGroup", Kind: Delete, Resource: "datasetGroup", Lookup: "datasetGroupArn"},
		{Name: "CreateDataset", Kind: Create, Resource: "dataset", Check: personalizeDataset},
		{Name: "DescribeDataset", Kind: Read, Resource: "dataset", Lookup: "datasetArn"},
		{Name: "ListDatasets", Kind: List, Resource: "dataset", Filters: map[string]string{"datasetGroupArn": "datasetGroupArn"}},
		{Name: "UpdateDataset", Kind: Update, Resource: "dataset", Lookup: "datasetArn", Fields: []string{"schemaArn"}, Touch: []string{"lastUpdatedDateTime"}},
		{Name: "DeleteDataset", Kind: Delete, Resource: "dataset", Lookup: "datasetArn"},
		{Name: "CreateFilter", Kind: Create, Resource: "filter"},
		{Name: "DescribeFilter", Kind: Read, Resource: "filter", Lookup: "filterArn"},
		{Name: "ListFilters", Kind: List, Resource: "filter", Filters: map[string]string{"datasetGroupArn": "datasetGroupArn"}},
		{Name: "DeleteFilter", Kind: Delete, Resource: "filter", Lookup: "filterArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Schemas, dataset groups, empty datasets and filters are stored ACTIVE configuration records: no data is imported into a dataset, nothing is trained or deployed, and no recommendation is made. Dataset import, export, batch inference, batch segment and data deletion jobs, solutions, solution versions, campaigns, recommenders, metric attributions, event trackers and the AWS-owned recipe, algorithm and feature transformation catalogs return UnknownOperationException with HTTP 501; personalize-runtime and personalize-events are separate services and are not served.",
		"Solutions are not bound because creating one starts automatic training by default, and event trackers are not bound because their tracking ID exists only to send events to a dataset group.",
		"A schema must be a JSON object in Avro record form with a name and a non-empty list of fields that each have a name and a type; the field names and types the dataset type requires, and the schema domain against the dataset group domain, are not checked. A dataset group may carry a role only together with a KMS key and a KMS key only together with a role; neither is used because no data is encrypted.",
		"A dataset is stored with the dataset type as given (Interactions, Items, Users, Actions or Action_Interactions, in any letter case), and its ARN is arn:aws:personalize:region:account:dataset/id with a generated identifier, unlike the AWS form that holds the dataset group name and dataset type; a dataset group holds at most one dataset of each type in AWS, which is not enforced. A dataset and a filter must reference an existing dataset group and a dataset an existing schema; UpdateDataset replaces the schema ARN and never reports a latestDatasetUpdate or a trackingId.",
		"Deleting a schema that a dataset uses, or a dataset group that a dataset or filter uses, fails with ResourceInUseException and changes nothing. Filters keep their expression without evaluating it.",
		"Schema, dataset group and filter names are unique per Region and Personalize limits are local: 100 schemas, 100 dataset groups, 500 datasets and 100 filters per Region. Tags apply to the four resource kinds by ARN and keep the official key and value shapes.",
	},
}

func personalizeSchema(input map[string]any) error {
	var avro struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Fields []struct {
			Name string `json:"name"`
			Type any    `json:"type"`
		} `json:"fields"`
	}
	text, _ := input["schema"].(string)
	if err := json.Unmarshal([]byte(text), &avro); err != nil || avro.Type != "record" || avro.Name == "" || len(avro.Fields) == 0 {
		return Failure("InvalidInputException", "The schema must be Avro JSON: an object of type record with a name and a list of fields.")
	}
	for _, field := range avro.Fields {
		if field.Name == "" || field.Type == nil {
			return Failure("InvalidInputException", "Every schema field needs a name and a type.")
		}
	}
	return nil
}

func personalizeDatasetGroup(input map[string]any) error {
	_, role := input["roleArn"]
	_, key := input["kmsKeyArn"]
	if role != key {
		return Failure("InvalidInputException", "roleArn is valid only together with kmsKeyArn, and kmsKeyArn needs the role that may use the key.")
	}
	return nil
}

func personalizeDataset(input map[string]any) error {
	if kind, _ := input["datasetType"].(string); !slices.Contains(personalizeDatasetTypes, strings.ToLower(kind)) {
		return Failure("InvalidInputException", "datasetType must be Interactions, Items, Users, Actions or Action_Interactions.")
	}
	return nil
}

// codeGuruProfiler serves profiling groups. No agent configuration, profile
// or finding is produced.
var codeGuruProfiler = &Service{
	Model: "codeguruprofiler", Title: "Amazon CodeGuru Profiler", IAMPrefix: "codeguru-profiler", Issues: []int{165},
	Resources: []*Resource{
		{Kind: "profilingGroup", Shape: "ProfilingGroupDescription", ID: "name",
			ARN:       "arn:{partition}:codeguru-profiler:{region}:{account}:profilingGroup/{name}",
			ARNMember: "arn", NameMember: "name", Unique: true, Limit: 100,
			Aliases:  map[string]string{"profilingGroupName": "name"},
			Generate: map[string]any{"computePlatform": "Default", "createdAt": "$now", "updatedAt": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateProfilingGroup", Kind: Create, Resource: "profilingGroup"},
		{Name: "DescribeProfilingGroup", Kind: Read, Resource: "profilingGroup", Lookup: "profilingGroupName"},
		{Name: "ListProfilingGroups", Kind: List, Resource: "profilingGroup", ListValue: "name", Check: profilingList},
		{Name: "UpdateProfilingGroup", Kind: Update, Resource: "profilingGroup", Lookup: "profilingGroupName",
			Fields: []string{"agentOrchestrationConfig"}, Touch: []string{"updatedAt"}},
		{Name: "DeleteProfilingGroup", Kind: Delete, Resource: "profilingGroup", Lookup: "profilingGroupName"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Profiling groups are stored configuration records: no agent ever connects, so ConfigureAgent, PostAgentProfile, GetProfile, ListProfileTimes, BatchGetFrameMetricData, GetRecommendations, ListFindingsReports, GetFindingsReportAccountSummary and SubmitFeedback return UnknownOperationException with HTTP 501, and a group never reports a profilingStatus. The agent orchestration setting is stored without any agent reading it.",
		"Notification channels (AddNotificationChannels, GetNotificationConfiguration, RemoveNotificationChannel) and resource-based permissions (PutPermission, GetPolicy, RemovePermission) are not implemented: they are stored beside the group with revision tokens the engine does not model, so those operations return UnknownOperationException with HTTP 501.",
		"ListProfilingGroups returns both profilingGroupNames and profilingGroups in every response, whatever includeDescription says, where AWS returns only the one that matches it. The compute platform defaults to Default, the agent orchestration setting is absent unless supplied, and the local limit is 100 profiling groups per Region.",
		"Tags apply to profiling groups by ARN (arn:aws:codeguru-profiler:region:account:profilingGroup/name). CodeGuru Reviewer and CodeGuru Security are separate services and are not served.",
	},
}

// profilingList accepts includeDescription, which only selects which of the
// two lists AWS returns; the simulator returns both.
func profilingList(input map[string]any) error {
	delete(input, "includeDescription")
	return nil
}
