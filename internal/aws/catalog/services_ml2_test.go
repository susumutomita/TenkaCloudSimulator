package catalog

import (
	"net/url"
	"testing"
)

func ml2Items(t *testing.T, r reply, member string) []any {
	t.Helper()
	items, _ := r.body[member].([]any)
	return items
}

func tagsOf(t *testing.T, r reply, member string, key string) map[string]string {
	t.Helper()
	tags := map[string]string{}
	for _, item := range ml2Items(t, r, member) {
		pair := item.(map[string]any)
		tags[str(pair[key])] = str(pair[map[string]string{"Key": "Value", "key": "value", "tagKey": "tagValue"}[key]])
	}
	return tags
}

var forecastSchema = map[string]any{"Attributes": []any{
	map[string]any{"AttributeName": "timestamp", "AttributeType": "timestamp"},
	map[string]any{"AttributeName": "item_id", "AttributeType": "string"},
	map[string]any{"AttributeName": "demand", "AttributeType": "float"},
}}

func forecastDatasetInput(extra map[string]any) map[string]any {
	input := map[string]any{"DatasetName": "sales", "Domain": "RETAIL", "DatasetType": "TARGET_TIME_SERIES", "Schema": forecastSchema}
	for k, v := range extra {
		input[k] = v
	}
	return input
}

// Datasets and dataset groups are stored records; a dataset group holds no
// datasets because they cannot be resolved.
func TestForecastDatasetsAndGroups(t *testing.T) {
	h := newHarness(t, forecast)

	created := h.json1("forecast", "CreateDataset", forecastDatasetInput(map[string]any{
		"DataFrequency": "30min", "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}}))
	expect(t, created, 200, "")
	arn := str(created.body["DatasetArn"])
	if arn != "arn:aws:forecast:us-east-1:000000000000:dataset/sales" {
		t.Fatalf("dataset ARN %s", arn)
	}
	described := h.json1("forecast", "DescribeDataset", map[string]any{"DatasetArn": arn})
	expect(t, described, 200, "")
	if described.body["Status"] != "ACTIVE" || described.body["Domain"] != "RETAIL" || described.body["DataFrequency"] != "30min" ||
		described.body["CreationTime"] == nil || described.body["LastModificationTime"] == nil {
		t.Fatalf("dataset %v", described.body)
	}
	expect(t, h.json1("forecast", "CreateDataset", forecastDatasetInput(nil)), 400, "ResourceAlreadyExistsException")
	listed := h.json1("forecast", "ListDatasets", map[string]any{})
	if items := ml2Items(t, listed, "Datasets"); len(items) != 1 || items[0].(map[string]any)["DatasetName"] != "sales" {
		t.Fatalf("datasets %s", listed.raw)
	}
	expect(t, h.json1("forecast", "DescribeDataset", map[string]any{"DatasetArn": "arn:aws:forecast:us-east-1:000000000000:dataset/none"}), 400, "ResourceNotFoundException")

	// DataFrequency: required for related series. The engine applies the
	// official pattern, which lists only literal values, so daily and
	// weekly frequencies are refused before the unit ranges are reached.
	noFrequency := forecastDatasetInput(map[string]any{"DatasetName": "related", "DatasetType": "RELATED_TIME_SERIES"})
	expect(t, h.json1("forecast", "CreateDataset", noFrequency), 400, "InvalidInputException")
	for _, frequency := range []string{"1D", "2W", "D", "M", "bad"} {
		expect(t, h.json1("forecast", "CreateDataset", forecastDatasetInput(map[string]any{"DatasetName": "f", "DataFrequency": frequency})), 400, "InvalidInputException")
	}
	for _, frequency := range []string{"1min", "5min", "10min", "15min"} {
		expect(t, h.json1("forecast", "CreateDataset", forecastDatasetInput(map[string]any{"DatasetName": "ok" + frequency, "DatasetType": "RELATED_TIME_SERIES", "DataFrequency": frequency})), 200, "")
	}
	for frequency, valid := range map[string]bool{"1min": true, "59min": true, "60min": false, "1H": true, "23H": true, "24H": false, "6D": true, "7D": false, "4W": true, "5W": false,
		"11M": true, "12M": false, "1Y": true, "2Y": false, "0D": false, "D": false, "1d": false} {
		err := forecastDataset(map[string]any{"DatasetType": "TARGET_TIME_SERIES", "DataFrequency": frequency})
		if (err == nil) != valid {
			t.Fatalf("DataFrequency %s: %v", frequency, err)
		}
	}
	expect(t, h.json1("forecast", "CreateDataset", forecastDatasetInput(map[string]any{"DatasetName": "reserved", "Tags": []any{map[string]any{"Key": "AWS:x", "Value": "v"}}})), 400, "InvalidInputException")
	expect(t, h.json1("forecast", "CreateDataset", forecastDatasetInput(map[string]any{"DatasetName": "1bad"})), 400, "InvalidInputException")

	group := h.json1("forecast", "CreateDatasetGroup", map[string]any{"DatasetGroupName": "retail", "Domain": "RETAIL", "DatasetArns": []any{}})
	expect(t, group, 200, "")
	groupArn := str(group.body["DatasetGroupArn"])
	if groupArn != "arn:aws:forecast:us-east-1:000000000000:dataset-group/retail" {
		t.Fatalf("group ARN %s", groupArn)
	}
	expect(t, h.json1("forecast", "CreateDatasetGroup", map[string]any{"DatasetGroupName": "retail", "Domain": "RETAIL"}), 400, "ResourceAlreadyExistsException")
	expect(t, h.json1("forecast", "CreateDatasetGroup", map[string]any{"DatasetGroupName": "other", "Domain": "RETAIL", "DatasetArns": []any{arn}}), 400, "InvalidInputException")
	expect(t, h.json1("forecast", "CreateDatasetGroup", map[string]any{"DatasetGroupName": "other", "Domain": "RETAIL", "Tags": []any{map[string]any{"Key": "aws:x", "Value": "v"}}}), 400, "InvalidInputException")
	if g := h.json1("forecast", "DescribeDatasetGroup", map[string]any{"DatasetGroupArn": groupArn}); g.body["Status"] != "ACTIVE" || g.body["DatasetArns"] != nil || g.body["DatasetGroupName"] != "retail" {
		t.Fatalf("group %s", g.raw)
	}
	if items := ml2Items(t, h.json1("forecast", "ListDatasetGroups", map[string]any{}), "DatasetGroups"); len(items) != 1 {
		t.Fatalf("groups %v", items)
	}

	// Tags.
	expect(t, h.json1("forecast", "TagResource", map[string]any{"ResourceArn": arn, "Tags": []any{map[string]any{"Key": "env", "Value": "dev"}}}), 200, "")
	expect(t, h.json1("forecast", "TagResource", map[string]any{"ResourceArn": arn, "Tags": []any{map[string]any{"Key": "aws:env", "Value": "dev"}}}), 400, "InvalidInputException")
	expect(t, h.json1("forecast", "UntagResource", map[string]any{"ResourceArn": arn, "TagKeys": []any{"AWS:team"}}), 400, "InvalidInputException")
	expect(t, h.json1("forecast", "UntagResource", map[string]any{"ResourceArn": arn, "TagKeys": []any{"team"}}), 200, "")
	if tags := tagsOf(t, h.json1("forecast", "ListTagsForResource", map[string]any{"ResourceArn": arn}), "Tags", "Key"); len(tags) != 1 || tags["env"] != "dev" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("forecast", "ListTagsForResource", map[string]any{"ResourceArn": "arn:aws:forecast:us-east-1:000000000000:dataset/none"}), 400, "ResourceNotFoundException")

	// Everything that imports, trains or predicts is unbound.
	for _, op := range []string{"CreatePredictor", "CreateAutoPredictor", "CreateForecast", "CreateDatasetImportJob", "UpdateDatasetGroup", "StopResource", "GetAccuracyMetrics", "DeleteResourceTree", "ListPredictors"} {
		expect(t, h.json1("forecast", op, map[string]any{}), 501, "UnknownOperationException")
	}

	expect(t, h.json1("forecast", "DeleteDatasetGroup", map[string]any{"DatasetGroupArn": groupArn}), 200, "")
	expect(t, h.json1("forecast", "DeleteDataset", map[string]any{"DatasetArn": arn}), 200, "")
	expect(t, h.json1("forecast", "DeleteDataset", map[string]any{"DatasetArn": arn}), 400, "ResourceNotFoundException")
}

func TestForecastDatasetPaginatesAndLimits(t *testing.T) {
	h := newHarness(t, forecast)
	for _, name := range []string{"a", "b", "c"} {
		expect(t, h.json1("forecast", "CreateDataset", forecastDatasetInput(map[string]any{"DatasetName": name})), 200, "")
	}
	first := h.json1("forecast", "ListDatasets", map[string]any{"MaxResults": 2})
	if len(ml2Items(t, first, "Datasets")) != 2 || str(first.body["NextToken"]) == "" {
		t.Fatalf("page %s", first.raw)
	}
	if next := h.json1("forecast", "ListDatasets", map[string]any{"MaxResults": 2, "NextToken": first.body["NextToken"]}); len(ml2Items(t, next, "Datasets")) != 1 {
		t.Fatalf("next %s", next.raw)
	}
	expect(t, h.json1("forecast", "ListDatasets", map[string]any{"NextToken": "bogus"}), 400, "InvalidNextTokenException")
}

func fraudTags(pairs ...string) []any {
	var tags []any
	for i := 0; i+1 < len(pairs); i += 2 {
		tags = append(tags, map[string]any{"key": pairs[i], "value": pairs[i+1]})
	}
	return tags
}

// Entity types, labels and outcomes are written with Put and read by name.
func TestFraudDetectorPutResources(t *testing.T) {
	h := newHarness(t, fraudDetector)
	for _, c := range []struct{ put, get, del, list, path string }{
		{"PutEntityType", "GetEntityTypes", "DeleteEntityType", "entityTypes", "entity-type"},
		{"PutLabel", "GetLabels", "DeleteLabel", "labels", "label"},
		{"PutOutcome", "GetOutcomes", "DeleteOutcome", "outcomes", "outcome"},
	} {
		expect(t, h.json1("frauddetector", c.put, map[string]any{"name": "customer", "description": "first", "tags": fraudTags("team", "a")}), 200, "")
		got := h.json1("frauddetector", c.get, map[string]any{"name": "customer"})
		expect(t, got, 200, "")
		items := ml2Items(t, got, c.list)
		if len(items) != 1 {
			t.Fatalf("%s %s", c.get, got.raw)
		}
		item := items[0].(map[string]any)
		if item["arn"] != "arn:aws:frauddetector:us-east-1:000000000000:"+c.path+"/customer" || item["description"] != "first" || item["createdTime"] == nil {
			t.Fatalf("%s %v", c.get, item)
		}
		// A second Put replaces the description and adds tags.
		expect(t, h.json1("frauddetector", c.put, map[string]any{"name": "customer", "description": "second", "tags": fraudTags("env", "dev")}), 200, "")
		expect(t, h.json1("frauddetector", c.put, map[string]any{"name": "other"}), 200, "")
		all := h.json1("frauddetector", c.get, map[string]any{})
		if len(ml2Items(t, all, c.list)) != 2 {
			t.Fatalf("%s all %s", c.get, all.raw)
		}
		again := ml2Items(t, h.json1("frauddetector", c.get, map[string]any{"name": "customer"}), c.list)[0].(map[string]any)
		if again["description"] != "second" {
			t.Fatalf("%s put %v", c.get, again)
		}
		arn := str(item["arn"])
		if tags := tagsOf(t, h.json1("frauddetector", "ListTagsForResource", map[string]any{"resourceARN": arn}), "tags", "key"); len(tags) != 2 || tags["team"] != "a" || tags["env"] != "dev" {
			t.Fatalf("%s tags %v", c.put, tags)
		}
		expect(t, h.json1("frauddetector", c.get, map[string]any{"name": "missing"}), 400, "ResourceNotFoundException")
		expect(t, h.json1("frauddetector", c.put, map[string]any{"name": "Bad Name"}), 400, "ValidationException")
		expect(t, h.json1("frauddetector", c.del, map[string]any{"name": "customer"}), 200, "")
		expect(t, h.json1("frauddetector", c.del, map[string]any{"name": "customer"}), 400, "ValidationException")
	}
	expect(t, h.json1("frauddetector", "GetEntityTypes", map[string]any{"maxResults": 4}), 400, "ValidationException")
}

func TestFraudDetectorVariablesAndLists(t *testing.T) {
	h := newHarness(t, fraudDetector)
	variable := map[string]any{"name": "email", "dataType": "STRING", "dataSource": "EVENT", "defaultValue": "unknown", "variableType": "EMAIL_ADDRESS", "tags": fraudTags("team", "a")}
	expect(t, h.json1("frauddetector", "CreateVariable", variable), 200, "")
	expect(t, h.json1("frauddetector", "CreateVariable", variable), 400, "ValidationException")
	for _, bad := range []map[string]any{{"dataSource": "MODEL_SCORE"}, {"variableType": "NOT_A_TYPE"}} {
		input := map[string]any{"name": "score", "dataType": "FLOAT", "dataSource": "EVENT", "defaultValue": "0"}
		for k, v := range bad {
			input[k] = v
		}
		expect(t, h.json1("frauddetector", "CreateVariable", input), 400, "ValidationException")
	}
	expect(t, h.json1("frauddetector", "UpdateVariable", map[string]any{"name": "email", "defaultValue": "none", "description": "mail", "variableType": "FREE_FORM_TEXT"}), 200, "")
	expect(t, h.json1("frauddetector", "UpdateVariable", map[string]any{"name": "email", "variableType": "NOT_A_TYPE"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "UpdateVariable", map[string]any{"name": "missing", "description": "x"}), 400, "ResourceNotFoundException")
	got := h.json1("frauddetector", "GetVariables", map[string]any{"name": "email"})
	item := ml2Items(t, got, "variables")[0].(map[string]any)
	if item["defaultValue"] != "none" || item["description"] != "mail" || item["variableType"] != "FREE_FORM_TEXT" || item["dataSource"] != "EVENT" ||
		item["arn"] != "arn:aws:frauddetector:us-east-1:000000000000:variable/email" {
		t.Fatalf("variable %v", item)
	}
	expect(t, h.json1("frauddetector", "GetVariables", map[string]any{"name": "missing"}), 400, "ResourceNotFoundException")
	if len(ml2Items(t, h.json1("frauddetector", "GetVariables", map[string]any{}), "variables")) != 1 {
		t.Fatal("variables")
	}

	list := h.json1("frauddetector", "CreateList", map[string]any{"name": "blocked_ips", "description": "ips", "variableType": "IP_ADDRESS", "elements": []any{}, "tags": fraudTags("team", "a")})
	expect(t, list, 200, "")
	expect(t, h.json1("frauddetector", "CreateList", map[string]any{"name": "blocked_ips"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "CreateList", map[string]any{"name": "with_elements", "elements": []any{"10.0.0.1"}}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "CreateList", map[string]any{"name": "bad_type", "variableType": "NOT_A_TYPE"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "CreateList", map[string]any{"name": "has-dash"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "UpdateList", map[string]any{"name": "blocked_ips", "description": "new", "elements": []any{}, "updateMode": "REPLACE"}), 200, "")
	expect(t, h.json1("frauddetector", "UpdateList", map[string]any{"name": "blocked_ips", "elements": []any{"10.0.0.1"}, "updateMode": "APPEND"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "UpdateList", map[string]any{"name": "blocked_ips", "variableType": "NOT_A_TYPE"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "UpdateList", map[string]any{"name": "blocked_ips", "variableType": "IP_ADDRESS"}), 400, "ValidationException")
	expect(t, h.json1("frauddetector", "UpdateList", map[string]any{"name": "missing", "description": "x"}), 400, "ResourceNotFoundException")
	meta := ml2Items(t, h.json1("frauddetector", "GetListsMetadata", map[string]any{"name": "blocked_ips"}), "lists")[0].(map[string]any)
	if meta["description"] != "new" || meta["variableType"] != "IP_ADDRESS" || meta["arn"] != "arn:aws:frauddetector:us-east-1:000000000000:list/blocked_ips" || meta["updatedTime"] == nil {
		t.Fatalf("list %v", meta)
	}

	arn := str(meta["arn"])
	expect(t, h.json1("frauddetector", "TagResource", map[string]any{"resourceARN": arn, "tags": fraudTags("env", "dev")}), 200, "")
	expect(t, h.json1("frauddetector", "UntagResource", map[string]any{"resourceARN": arn, "tagKeys": []any{"team"}}), 200, "")
	if tags := tagsOf(t, h.json1("frauddetector", "ListTagsForResource", map[string]any{"resourceARN": arn}), "tags", "key"); len(tags) != 1 || tags["env"] != "dev" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("frauddetector", "TagResource", map[string]any{"resourceARN": "arn:aws:frauddetector:us-east-1:000000000000:list/none", "tags": fraudTags("a", "b")}), 400, "ResourceNotFoundException")

	// Event types and detectors reference records by name, so they, models
	// and every data-plane operation are unbound.
	for _, op := range []string{"PutEventType", "GetEventTypes", "PutDetector", "GetDetectors", "CreateModel", "GetEventPrediction", "SendEvent", "GetListElements", "CreateRule", "CreateDetectorVersion"} {
		expect(t, h.json1("frauddetector", op, map[string]any{}), 501, "UnknownOperationException")
	}
	expect(t, h.json1("frauddetector", "DeleteList", map[string]any{"name": "blocked_ips"}), 200, "")
	expect(t, h.json1("frauddetector", "DeleteVariable", map[string]any{"name": "email"}), 200, "")
	expect(t, h.json1("frauddetector", "DeleteVariable", map[string]any{"name": "email"}), 400, "ValidationException")
}

const personalizeAvro = `{"type":"record","name":"Interactions","namespace":"com.amazonaws.personalize.schema","fields":[{"name":"USER_ID","type":"string"},{"name":"ITEM_ID","type":"string"},{"name":"TIMESTAMP","type":"long"}],"version":"1.0"}`

func personalizeTags(pairs ...string) []any {
	var tags []any
	for i := 0; i+1 < len(pairs); i += 2 {
		tags = append(tags, map[string]any{"tagKey": pairs[i], "tagValue": pairs[i+1]})
	}
	return tags
}

func TestPersonalizeSchemasGroupsDatasetsAndFilters(t *testing.T) {
	h := newHarness(t, personalize)
	for _, bad := range []string{"not json", `{"type":"enum","name":"x","fields":[{"name":"a","type":"string"}]}`, `{"type":"record","fields":[{"name":"a","type":"string"}]}`,
		`{"type":"record","name":"x","fields":[]}`, `{"type":"record","name":"x","fields":[{"name":"a"}]}`, `{"type":"record","name":"x","fields":[{"type":"string"}]}`} {
		expect(t, h.json1("personalize", "CreateSchema", map[string]any{"name": "bad", "schema": bad}), 400, "InvalidInputException")
	}
	schema := h.json1("personalize", "CreateSchema", map[string]any{"name": "interactions", "schema": personalizeAvro, "domain": "ECOMMERCE"})
	expect(t, schema, 200, "")
	schemaArn := str(schema.body["schemaArn"])
	if schemaArn != "arn:aws:personalize:us-east-1:000000000000:schema/interactions" {
		t.Fatalf("schema ARN %s", schemaArn)
	}
	expect(t, h.json1("personalize", "CreateSchema", map[string]any{"name": "interactions", "schema": personalizeAvro}), 400, "ResourceAlreadyExistsException")
	described := h.json1("personalize", "DescribeSchema", map[string]any{"schemaArn": schemaArn}).body["schema"].(map[string]any)
	if described["schema"] != personalizeAvro || described["domain"] != "ECOMMERCE" || described["creationDateTime"] == nil {
		t.Fatalf("schema %v", described)
	}
	if items := ml2Items(t, h.json1("personalize", "ListSchemas", map[string]any{}), "schemas"); len(items) != 1 {
		t.Fatalf("schemas %v", items)
	}
	expect(t, h.json1("personalize", "DescribeSchema", map[string]any{"schemaArn": "arn:aws:personalize:us-east-1:000000000000:schema/none"}), 400, "ResourceNotFoundException")

	expect(t, h.json1("personalize", "CreateDatasetGroup", map[string]any{"name": "bad", "roleArn": "arn:aws:iam::000000000000:role/p"}), 400, "InvalidInputException")
	expect(t, h.json1("personalize", "CreateDatasetGroup", map[string]any{"name": "bad", "kmsKeyArn": "arn:aws:kms:us-east-1:000000000000:key/abc"}), 400, "InvalidInputException")
	group := h.json1("personalize", "CreateDatasetGroup", map[string]any{"name": "shop", "domain": "ECOMMERCE", "tags": personalizeTags("team", "a"),
		"roleArn": "arn:aws:iam::000000000000:role/p", "kmsKeyArn": "arn:aws:kms:us-east-1:000000000000:key/abc"})
	expect(t, group, 200, "")
	groupArn := str(group.body["datasetGroupArn"])
	if groupArn != "arn:aws:personalize:us-east-1:000000000000:dataset-group/shop" || group.body["domain"] != "ECOMMERCE" {
		t.Fatalf("group %s", group.raw)
	}
	expect(t, h.json1("personalize", "CreateDatasetGroup", map[string]any{"name": "shop"}), 400, "ResourceAlreadyExistsException")
	g := h.json1("personalize", "DescribeDatasetGroup", map[string]any{"datasetGroupArn": groupArn}).body["datasetGroup"].(map[string]any)
	if g["status"] != "ACTIVE" || g["name"] != "shop" || g["kmsKeyArn"] == nil {
		t.Fatalf("group %v", g)
	}
	if items := ml2Items(t, h.json1("personalize", "ListDatasetGroups", map[string]any{}), "datasetGroups"); len(items) != 1 {
		t.Fatalf("groups %v", items)
	}

	// Datasets reference an existing group and schema, by type.
	input := map[string]any{"name": "events", "schemaArn": schemaArn, "datasetGroupArn": groupArn, "datasetType": "Interactions", "tags": personalizeTags("team", "a")}
	expect(t, h.json1("personalize", "CreateDataset", map[string]any{"name": "x", "schemaArn": schemaArn, "datasetGroupArn": groupArn, "datasetType": "Events"}), 400, "InvalidInputException")
	missingSchema := map[string]any{"name": "x", "schemaArn": "arn:aws:personalize:us-east-1:000000000000:schema/none", "datasetGroupArn": groupArn, "datasetType": "Items"}
	expect(t, h.json1("personalize", "CreateDataset", missingSchema), 400, "ResourceNotFoundException")
	missingGroup := map[string]any{"name": "x", "schemaArn": schemaArn, "datasetGroupArn": "arn:aws:personalize:us-east-1:000000000000:dataset-group/none", "datasetType": "Items"}
	expect(t, h.json1("personalize", "CreateDataset", missingGroup), 400, "ResourceNotFoundException")
	dataset := h.json1("personalize", "CreateDataset", input)
	expect(t, dataset, 200, "")
	datasetArn := str(dataset.body["datasetArn"])
	d := h.json1("personalize", "DescribeDataset", map[string]any{"datasetArn": datasetArn}).body["dataset"].(map[string]any)
	if d["status"] != "ACTIVE" || d["datasetGroupArn"] != groupArn || d["schemaArn"] != schemaArn || d["datasetType"] != "Interactions" || d["trackingId"] != nil {
		t.Fatalf("dataset %v", d)
	}
	other := h.json1("personalize", "CreateDatasetGroup", map[string]any{"name": "other"})
	expect(t, h.json1("personalize", "CreateDataset", map[string]any{"name": "o", "schemaArn": schemaArn, "datasetGroupArn": other.body["datasetGroupArn"], "datasetType": "users"}), 200, "")
	if items := ml2Items(t, h.json1("personalize", "ListDatasets", map[string]any{"datasetGroupArn": groupArn}), "datasets"); len(items) != 1 {
		t.Fatalf("datasets %v", items)
	}
	if items := ml2Items(t, h.json1("personalize", "ListDatasets", map[string]any{}), "datasets"); len(items) != 2 {
		t.Fatalf("all datasets %v", items)
	}
	second := h.json1("personalize", "CreateSchema", map[string]any{"name": "other", "schema": personalizeAvro})
	updated := h.json1("personalize", "UpdateDataset", map[string]any{"datasetArn": datasetArn, "schemaArn": second.body["schemaArn"]})
	expect(t, updated, 200, "")
	if updated.body["datasetArn"] != datasetArn {
		t.Fatalf("update %s", updated.raw)
	}
	expect(t, h.json1("personalize", "UpdateDataset", map[string]any{"datasetArn": datasetArn, "schemaArn": "arn:aws:personalize:us-east-1:000000000000:schema/none"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("personalize", "UpdateDataset", map[string]any{"datasetArn": "arn:aws:personalize:us-east-1:000000000000:dataset/none", "schemaArn": schemaArn}), 400, "ResourceNotFoundException")

	// Filters keep their expression without evaluating it.
	filter := h.json1("personalize", "CreateFilter", map[string]any{"name": "no-seen", "datasetGroupArn": groupArn, "filterExpression": "EXCLUDE ItemID WHERE Interactions.event_type IN ('watch')"})
	expect(t, filter, 200, "")
	filterArn := str(filter.body["filterArn"])
	f := h.json1("personalize", "DescribeFilter", map[string]any{"filterArn": filterArn}).body["filter"].(map[string]any)
	if f["status"] != "ACTIVE" || f["datasetGroupArn"] != groupArn || f["filterExpression"] == nil {
		t.Fatalf("filter %v", f)
	}
	expect(t, h.json1("personalize", "CreateFilter", map[string]any{"name": "x", "datasetGroupArn": "arn:aws:personalize:us-east-1:000000000000:dataset-group/none", "filterExpression": "EXCLUDE ItemID"}), 400, "ResourceNotFoundException")
	expect(t, h.json1("personalize", "CreateFilter", map[string]any{"name": "no-seen", "datasetGroupArn": groupArn, "filterExpression": "EXCLUDE ItemID"}), 400, "ResourceAlreadyExistsException")
	if items := ml2Items(t, h.json1("personalize", "ListFilters", map[string]any{"datasetGroupArn": groupArn}), "Filters"); len(items) != 1 {
		t.Fatalf("filters %v", items)
	}
	if items := ml2Items(t, h.json1("personalize", "ListFilters", map[string]any{"datasetGroupArn": other.body["datasetGroupArn"]}), "Filters"); len(items) != 0 {
		t.Fatalf("filters %v", items)
	}

	// Tags.
	expect(t, h.json1("personalize", "TagResource", map[string]any{"resourceArn": datasetArn, "tags": personalizeTags("env", "dev")}), 200, "")
	expect(t, h.json1("personalize", "UntagResource", map[string]any{"resourceArn": datasetArn, "tagKeys": []any{"team"}}), 200, "")
	if tags := tagsOf(t, h.json1("personalize", "ListTagsForResource", map[string]any{"resourceArn": datasetArn}), "tags", "tagKey"); len(tags) != 1 || tags["env"] != "dev" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("personalize", "ListTagsForResource", map[string]any{"resourceArn": "arn:aws:personalize:us-east-1:000000000000:dataset/none"}), 400, "ResourceNotFoundException")

	// Training, deployment, ingestion and AWS-owned catalogs are unbound.
	for _, op := range []string{"CreateSolution", "CreateSolutionVersion", "CreateCampaign", "CreateRecommender", "CreateEventTracker", "CreateDatasetImportJob",
		"CreateBatchInferenceJob", "DescribeRecipe", "ListRecipes", "DescribeAlgorithm", "CreateMetricAttribution", "StartRecommender"} {
		expect(t, h.json1("personalize", op, map[string]any{}), 501, "UnknownOperationException")
	}

	expect(t, h.json1("personalize", "DeleteFilter", map[string]any{"filterArn": filterArn}), 200, "")
	expect(t, h.json1("personalize", "DeleteDataset", map[string]any{"datasetArn": datasetArn}), 200, "")
	expect(t, h.json1("personalize", "DeleteDatasetGroup", map[string]any{"datasetGroupArn": groupArn}), 200, "")
	expect(t, h.json1("personalize", "DeleteSchema", map[string]any{"schemaArn": schemaArn}), 200, "")
	expect(t, h.json1("personalize", "DeleteSchema", map[string]any{"schemaArn": schemaArn}), 400, "ResourceNotFoundException")
}

func TestCodeGuruProfilerGroups(t *testing.T) {
	h := newHarness(t, codeGuruProfiler)
	created := h.rest("codeguruprofiler", "POST", "/profilingGroups?clientToken=token-1", map[string]any{"profilingGroupName": "svc",
		"agentOrchestrationConfig": map[string]any{"profilingEnabled": true}, "tags": map[string]any{"team": "a"}})
	expect(t, created, 201, "")
	group := created.body
	if group["name"] != "svc" || group["arn"] != "arn:aws:codeguru-profiler:us-east-1:000000000000:profilingGroup/svc" || group["computePlatform"] != "Default" ||
		group["profilingStatus"] != nil || group["createdAt"] == nil || group["tags"].(map[string]any)["team"] != "a" {
		t.Fatalf("group %v", group)
	}
	// The same client token replays the response; a different group with a duplicate name conflicts.
	if replay := h.rest("codeguruprofiler", "POST", "/profilingGroups?clientToken=token-1", map[string]any{"profilingGroupName": "svc",
		"agentOrchestrationConfig": map[string]any{"profilingEnabled": true}, "tags": map[string]any{"team": "a"}}); replay.status != 201 {
		t.Fatalf("replay %s", replay.raw)
	}
	expect(t, h.rest("codeguruprofiler", "POST", "/profilingGroups?clientToken=token-2", map[string]any{"profilingGroupName": "svc"}), 409, "ConflictException")
	expect(t, h.rest("codeguruprofiler", "POST", "/profilingGroups?clientToken=token-3", map[string]any{"profilingGroupName": "lambda", "computePlatform": "AWSLambda"}), 201, "")

	described := h.rest("codeguruprofiler", "GET", "/profilingGroups/svc", nil).body
	if described["agentOrchestrationConfig"].(map[string]any)["profilingEnabled"] != true {
		t.Fatalf("described %v", described)
	}
	expect(t, h.rest("codeguruprofiler", "GET", "/profilingGroups/none", nil), 404, "ResourceNotFoundException")

	listed := h.rest("codeguruprofiler", "GET", "/profilingGroups?includeDescription=true", nil)
	expect(t, listed, 200, "")
	if names := ml2Items(t, listed, "profilingGroupNames"); len(names) != 2 || names[0] != "svc" {
		t.Fatalf("names %s", listed.raw)
	}
	if groups := ml2Items(t, listed, "profilingGroups"); len(groups) != 2 {
		t.Fatalf("groups %s", listed.raw)
	}
	first := h.rest("codeguruprofiler", "GET", "/profilingGroups?maxResults=1", nil)
	if len(ml2Items(t, first, "profilingGroupNames")) != 1 || str(first.body["nextToken"]) == "" {
		t.Fatalf("page %s", first.raw)
	}

	updated := h.rest("codeguruprofiler", "PUT", "/profilingGroups/svc", map[string]any{"agentOrchestrationConfig": map[string]any{"profilingEnabled": false}})
	expect(t, updated, 200, "")
	if updated.body["agentOrchestrationConfig"].(map[string]any)["profilingEnabled"] != false {
		t.Fatalf("updated %s", updated.raw)
	}
	expect(t, h.rest("codeguruprofiler", "PUT", "/profilingGroups/none", map[string]any{"agentOrchestrationConfig": map[string]any{"profilingEnabled": false}}), 404, "ResourceNotFoundException")

	arn := url.PathEscape(str(group["arn"]))
	expect(t, h.rest("codeguruprofiler", "POST", "/tags/"+arn, map[string]any{"tags": map[string]any{"env": "dev"}}), 204, "")
	expect(t, h.rest("codeguruprofiler", "DELETE", "/tags/"+arn+"?tagKeys=team", nil), 204, "")
	if tags := h.rest("codeguruprofiler", "GET", "/tags/"+arn, nil).body["tags"].(map[string]any); len(tags) != 1 || tags["env"] != "dev" {
		t.Fatalf("tags %v", tags)
	}

	// Agents, profiles, findings, notifications and permissions are unbound.
	for _, c := range [][2]string{{"POST", "/profilingGroups/svc/agentProfile"}, {"POST", "/profilingGroups/svc/configureAgent"}, {"GET", "/profilingGroups/svc/profile"},
		{"GET", "/internal/profilingGroups/svc/recommendations"}, {"POST", "/profilingGroups/svc/notificationConfiguration"}, {"GET", "/profilingGroups/svc/policy"},
		{"PUT", "/profilingGroups/svc/policy/agentPermission"}} {
		expect(t, h.rest("codeguruprofiler", c[0], c[1], map[string]any{}), 501, "UnknownOperationException")
	}
	expect(t, h.rest("codeguruprofiler", "DELETE", "/profilingGroups/svc", nil), 204, "")
	expect(t, h.rest("codeguruprofiler", "DELETE", "/profilingGroups/svc", nil), 404, "ResourceNotFoundException")
}
