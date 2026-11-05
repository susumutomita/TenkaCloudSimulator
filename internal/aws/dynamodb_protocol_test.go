package aws

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const dynamoGuardTable = `{"TableName":"guard","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`

// Exercise the public provider dispatch and real SQLite state. Every refused
// request must preserve an existing item and leave no extra key/table behind.
func TestDynamoDBWireAdmissionPreservesState(t *testing.T) {
	put := `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":{"S":"replacement"}}}`
	for _, tc := range []struct {
		name, body string
		headers    map[string]string
		status     int
	}{
		{"wrong-media-type", put, map[string]string{"Content-Type": "text/plain"}, 400},
		{"missing-media-type", put, map[string]string{"Content-Type": ""}, 400},
		{"malformed-media-type", put, map[string]string{"Content-Type": "application/x-amz-json-1.0;broken"}, 400},
		{"wrong-service-hint", put, map[string]string{"X-Simulator-Service": "ec2"}, 400},
		{"wrong-signing-service", put, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261105/us-east-1/ec2/aws4_request"}, 403},
		{"wrong-signing-algorithm", put, map[string]string{"Authorization": "Other Credential=TCSIMLOCAL/20261105/us-east-1/dynamodb/aws4_request"}, 403},
		{"missing-signing-scope", put, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL"}, 403},
		{"wrong-scope-terminator", put, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261105/us-east-1/dynamodb/other"}, 403},
		{"duplicate-table", `{"TableName":"other","TableName":"guard","Item":{"id":{"S":"stable"},"value":{"S":"replacement"}}}`, nil, 400},
		{"escaped-duplicate-table", `{"TableName":"other","T\u0061bleName":"guard","Item":{"id":{"S":"stable"},"value":{"S":"replacement"}}}`, nil, 400},
		{"duplicate-attribute", `{"TableName":"guard","Item":{"id":{"S":"stable"},"id":{"S":"extra"},"value":{"S":"replacement"}}}`, nil, 400},
		{"duplicate-attribute-type", `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":{"S":"original","S":"replacement"}}}`, nil, 400},
		{"duplicate-nested-map", `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":{"M":{"key":{"S":"first"},"key":{"S":"second"}}}}}`, nil, 400},
		{"nested-null", `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":{"M":{"key":null}}}}`, nil, 400},
		{"invalid-utf8", strings.Replace(put, "replacement", "\xff", 1), nil, 400},
		{"trailing-json", put + `{}`, nil, 400},
		{"structural-depth", `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":` + strings.Repeat("[", 129) + "true" + strings.Repeat("]", 129) + `}}`, nil, 400},
		{"empty-condition", strings.TrimSuffix(put, "}") + `,"ConditionExpression":""}`, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			dynamoCall(t, g, "CreateTable", dynamoGuardTable, 200, nil)
			dynamoCall(t, g, "PutItem", strings.Replace(put, "replacement", "original", 1), 200, nil)
			key := `{"TableName":"guard","Key":{"id":{"S":"stable"}}}`
			before := dynamoCall(t, g, "GetItem", key, 200, nil)
			stats := dynamoCall(t, g, "DescribeTable", `{"TableName":"guard"}`, 200, nil)
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("X-Amz-Target", "DynamoDB_20120810.PutItem")
			r.Header.Set("Content-Type", "application/x-amz-json-1.0")
			for name, value := range tc.headers {
				r.Header.Set(name, value)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Errorf("refused request: want status %d, got %d", tc.status, w.Code)
			}
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal("expected a native JSON error", err)
			}
			wantCode := "ValidationException"
			if tc.status == 403 {
				wantCode = "AccessDeniedException"
			}
			if code, _ := out["__type"].(string); !strings.HasSuffix(code, "#"+wantCode) {
				t.Errorf("refused request: want %s, got %q", wantCode, code)
			}
			if after := dynamoCall(t, g, "GetItem", key, 200, nil); !reflect.DeepEqual(before, after) {
				t.Error("refused request overwrote an existing item")
			}
			if after := dynamoCall(t, g, "DescribeTable", `{"TableName":"guard"}`, 200, nil); !reflect.DeepEqual(stats, after) {
				t.Error("refused request changed table metadata or item statistics")
			}
			listed := dynamoCall(t, g, "ListTables", `{}`, 200, nil)["TableNames"]
			if !reflect.DeepEqual(listed, []any{"guard"}) {
				t.Error("refused request changed the table set")
			}
		})
	}
}

func TestDynamoDBCreateSchemaRefusalLeavesNoTableAndAllowsRetry(t *testing.T) {
	valid := strings.Replace(dynamoGuardTable, "guard", "newtable", 1)
	provisioned := strings.Replace(valid, `"BillingMode":"PAY_PER_REQUEST"`, `"ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1}`, 1)
	for _, tc := range []struct{ name, body string }{
		{"duplicate-key-with-unused-definition", `{"TableName":"newtable","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"},{"AttributeName":"unused","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"},{"AttributeName":"id","KeyType":"RANGE"}],"BillingMode":"PAY_PER_REQUEST"}`},
		{"unknown-definition-field", strings.Replace(valid, `"AttributeType":"S"`, `"AttributeType":"S","Unsupported":true`, 1)},
		{"definition-not-an-array", strings.Replace(valid, `[{"AttributeName":"id","AttributeType":"S"}]`, `{}`, 1)},
		{"definition-not-an-object", strings.Replace(valid, `{"AttributeName":"id","AttributeType":"S"}`, `["id","S"]`, 1)},
		{"wrong-case-definition-field", strings.Replace(valid, `"AttributeType":"S"`, `"attributetype":"S"`, 1)},
		{"duplicate-definition-field", strings.Replace(valid, `"AttributeType":"S"`, `"AttributeType":"N","AttributeType":"S"`, 1)},
		{"unknown-schema-field", strings.Replace(valid, `"KeyType":"HASH"`, `"KeyType":"HASH","Unsupported":true`, 1)},
		{"missing-schema-field", strings.Replace(valid, `,"KeyType":"HASH"`, "", 1)},
		{"wrong-case-schema-field", strings.Replace(valid, `"KeyType":"HASH"`, `"keytype":"HASH"`, 1)},
		{"unknown-throughput-field", strings.Replace(provisioned, `"WriteCapacityUnits":1`, `"WriteCapacityUnits":1,"Unsupported":true`, 1)},
		{"wrong-case-throughput-field", strings.Replace(provisioned, `"ReadCapacityUnits":1`, `"readcapacityunits":1`, 1)},
		{"duplicate-throughput-field", strings.Replace(provisioned, `"ReadCapacityUnits":1`, `"ReadCapacityUnits":0,"ReadCapacityUnits":1`, 1)},
		{"empty-billing-mode", strings.TrimSuffix(provisioned, "}") + `,"BillingMode":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("X-Amz-Target", "DynamoDB_20120810.CreateTable")
			r.Header.Set("Content-Type", "application/x-amz-json-1.0")
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Errorf("malformed schema must be refused before persistence: got %d", w.Code)
			}
			names := dynamoCall(t, g, "ListTables", `{}`, 200, nil)["TableNames"].([]any)
			if len(names) != 0 {
				t.Error("refused schema left a persisted table", names)
			}
			// The failed request must not reserve its name or poison subsequent reads.
			dynamoCall(t, g, "CreateTable", valid, 200, nil)
			dynamoCall(t, g, "PutItem", `{"TableName":"newtable","Item":{"id":{"S":"key"}}}`, 200, nil)
			dynamoCall(t, g, "DeleteTable", `{"TableName":"newtable"}`, 200, nil)
		})
	}
}

func TestDynamoDBNativeCreateRefusesUnreadableMetadata(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", dynamoGuardTable, 200, nil)
	valid, err := g.DynamoDB.GetTable("us-east-1", "guard")
	if err != nil {
		t.Fatal(err)
	}
	valid.Name = "native-retry"
	invalid := valid
	invalid.KeySchema = []DynamoDBKeyElement{{"id", "HASH"}, {"id", "RANGE"}}
	invalid.Definitions = map[string]string{"id": "S", "unused": "S"}
	if _, err := g.DynamoDB.CreateTable(invalid); err == nil {
		t.Fatal("native create accepted unreadable duplicate key metadata")
	}
	if names := dynamoCall(t, g, "ListTables", `{}`, 200, nil)["TableNames"]; !reflect.DeepEqual(names, []any{"guard"}) {
		t.Fatal("native rejected metadata changed the table set", names)
	}
	if _, err := g.DynamoDB.CreateTable(valid); err != nil {
		t.Fatal("native rejected metadata prevented a valid retry", err)
	}
	if _, err := g.DynamoDB.GetTable(valid.Region, valid.Name); err != nil {
		t.Fatal("valid native retry did not produce readable metadata", err)
	}
}

func TestDynamoDBOptionalFieldsDistinguishAbsentAndInvalid(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	for _, body := range []string{`{"Limit":0}`, `{"ExclusiveStartTableName":""}`} {
		dynamoErrorCode(t, dynamoCall(t, g, "ListTables", body, 400, nil), "ValidationException")
	}
	dynamoCall(t, g, "ListTables", `{}`, 200, nil)
	dynamoCall(t, g, "ListTables", `{"Limit":1}`, 200, nil)
}

func TestDynamoDBEmptyNamedExtraKeyCannotReadOrDeleteItem(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", dynamoGuardTable, 200, nil)
	dynamoCall(t, g, "PutItem", `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":{"S":"retained"}}}`, 200, nil)
	key := `{"TableName":"guard","Key":{"id":{"S":"stable"}}}`
	before := dynamoCall(t, g, "GetItem", key, 200, nil)
	for _, operation := range []string{"GetItem", "DeleteItem"} {
		out := dynamoCall(t, g, operation, `{"TableName":"guard","Key":{"id":{"S":"stable"},"":{"S":"ignored"}}}`, 400, nil)
		dynamoErrorCode(t, out, "ValidationException")
		if after := dynamoCall(t, g, "GetItem", key, 200, nil); !reflect.DeepEqual(before, after) {
			t.Fatal("malformed key changed the existing item", operation)
		}
	}
}

func TestDynamoDBStrictJSONRetainsSupportedNestedValues(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", dynamoGuardTable, 200, nil)
	value := any(map[string]any{"S": "日本語"})
	for range 32 {
		value = map[string]any{"M": map[string]any{"child": value}}
	}
	item := map[string]any{"id": map[string]any{"S": "nested"}, "value": value, "null": map[string]any{"NULL": true}, "value2": map[string]any{"L": []any{}}}
	body, err := json.Marshal(map[string]any{"TableName": "guard", "Item": item})
	if err != nil {
		t.Fatal(err)
	}
	dynamoCall(t, g, "PutItem", string(body), 200, map[string]string{"Content-Type": "application/x-amz-json-1.0; charset=utf-8", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261105/us-east-1/dynamodb/aws4_request, SignedHeaders=host, Signature=dummy"})
	got := dynamoCall(t, g, "GetItem", `{"TableName":"guard","Key":{"id":{"S":"nested"}}}`, 200, nil)["Item"]
	if !reflect.DeepEqual(item, got) {
		t.Fatal("native supported values changed during strict JSON admission")
	}
}
