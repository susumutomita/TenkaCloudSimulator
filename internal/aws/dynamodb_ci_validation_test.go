package aws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Additional protocol/negative/rollback acceptance cases for PR309's coverage
// follow-up. Keep both coverage floors unchanged; run against the owner-updated
// main integration before treating this unexecuted draft as a CI fix.
func TestDynamoDBRejectsMalformedAttributeValuesWithoutMutation(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", `{"TableName":"typed","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, 200, nil)
	invalid := map[string]any{
		"scalar": 123, "empty": map[string]any{}, "string-type": map[string]any{"S": 123}, "number-type": map[string]any{"N": true}, "binary-type": map[string]any{"B": 123}, "bool-type": map[string]any{"BOOL": "true"}, "null-false": map[string]any{"NULL": false},
		"string-set-type": map[string]any{"SS": "value"}, "empty-set": map[string]any{"SS": []any{}}, "mixed-set": map[string]any{"SS": []any{"x", 2}}, "number-set": map[string]any{"NS": []any{"invalid"}}, "binary-set": map[string]any{"BS": []any{"!!!"}}, "list-type": map[string]any{"L": map[string]any{}}, "map-type": map[string]any{"M": []any{}}, "nested-list": map[string]any{"L": []any{map[string]any{"BOOL": 1}}}, "nested-map": map[string]any{"M": map[string]any{"bad": map[string]any{"NULL": false}}},
	}
	deep := any(map[string]any{"S": "value"})
	for i := 0; i < 33; i++ {
		deep = map[string]any{"L": []any{deep}}
	}
	invalid["depth-limit"] = deep
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"TableName": "typed", "Item": map[string]any{"id": map[string]any{"S": "stable-key"}, "value": value}})
			if err != nil {
				t.Fatal(err)
			}
			dynamoErrorCode(t, dynamoCall(t, g, "PutItem", string(body), 400, nil), "ValidationException")
			if _, found := dynamoCall(t, g, "GetItem", `{"TableName":"typed","Key":{"id":{"S":"stable-key"}}}`, 200, nil)["Item"]; found {
				t.Fatal("failed validation wrote an item")
			}
		})
	}
	// Successful values in the same documented subset must still round-trip.
	good := `{"TableName":"typed","Item":{"id":{"S":"good"},"empty-string":{"S":""},"empty-binary":{"B":""},"binary-set":{"BS":["YQ==","Yg=="]},"false":{"BOOL":false},"list":{"L":[]},"map":{"M":{}}}}`
	dynamoCall(t, g, "PutItem", good, 200, nil)
	if _, found := dynamoCall(t, g, "GetItem", `{"TableName":"typed","Key":{"id":{"S":"good"}}}`, 200, nil)["Item"]; !found {
		t.Fatal("supported attribute values did not persist")
	}
}

func TestDynamoDBRejectsMalformedWireAndSelection(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	for _, test := range []struct {
		name, method, path, body string
		headers                  map[string]string
	}{
		{"method", "GET", "/", "{}", nil}, {"path", "POST", "/other", "{}", nil}, {"query", "POST", "/?ignored=1", "{}", nil},
		{"array", "POST", "/", "[]", nil}, {"null", "POST", "/", "null", nil}, {"trailing-json", "POST", "/", "{}{}", nil}, {"field-type", "POST", "/", `{"Limit":"1"}`, nil}, {"negative-limit", "POST", "/", `{"Limit":-1}`, nil}, {"bad-marker", "POST", "/", `{"ExclusiveStartTableName":"bad/name"}`, nil}, {"oversize", "POST", "/", strings.Repeat(" ", maxDynamoDBRequestBytes+1), nil},
		{"scope-region-mismatch", "POST", "/", "{}", map[string]string{"X-Simulator-Region": "us-east-1", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261104/us-west-2/dynamodb/aws4_request"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.Header.Set("X-Amz-Target", "DynamoDB_20120810.ListTables")
			for name, value := range test.headers {
				r.Header.Set(name, value)
			}
			w := httptest.NewRecorder()
			g.serveDynamoDB(w, r)
			if w.Code != 400 {
				t.Fatalf("malformed request status: %d", w.Code)
			}
			var out map[string]any
			if json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Fatal("expected JSON protocol error")
			}
			dynamoErrorCode(t, out, "ValidationException")
		})
	}
	for _, op := range []string{"DescribeTable", "DeleteTable", "GetItem", "DeleteItem"} {
		t.Run("missing-"+op, func(t *testing.T) {
			body := `{"TableName":"absent"}`
			if op == "GetItem" || op == "DeleteItem" {
				body = `{"TableName":"absent","Key":{"id":{"S":"x"}}}`
			}
			dynamoErrorCode(t, dynamoCall(t, g, op, body, 400, nil), "ResourceNotFoundException")
		})
	}
}

func TestDynamoDBStorageFailureRollsBackAndHidesInternalDetails(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	create := `{"TableName":"rollback","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Database().Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	failed := func(op, body string) {
		t.Helper()
		out := dynamoCall(t, g, op, body, 500, nil)
		dynamoErrorCode(t, out, "InternalServerError")
		if strings.Contains(out["message"].(string), "fixture-private-detail") {
			t.Fatal("database internals leaked")
		}
	}
	exec(`CREATE TRIGGER reject_table_insert BEFORE INSERT ON aws_dynamodb_tables BEGIN SELECT RAISE(ABORT,'fixture-private-detail'); END`)
	failed("CreateTable", create)
	exec(`DROP TRIGGER reject_table_insert`)
	dynamoErrorCode(t, dynamoCall(t, g, "DescribeTable", `{"TableName":"rollback"}`, 400, nil), "ResourceNotFoundException")
	dynamoCall(t, g, "CreateTable", create, 200, nil)
	put := `{"TableName":"rollback","Item":{"id":{"S":"key"},"value":{"S":"retained"}}}`
	key := `{"TableName":"rollback","Key":{"id":{"S":"key"}}}`
	exec(`CREATE TRIGGER reject_item_insert BEFORE INSERT ON aws_dynamodb_items BEGIN SELECT RAISE(ABORT,'fixture-private-detail'); END`)
	failed("PutItem", put)
	exec(`DROP TRIGGER reject_item_insert`)
	if _, found := dynamoCall(t, g, "GetItem", key, 200, nil)["Item"]; found {
		t.Fatal("failed insertion left an item")
	}
	dynamoCall(t, g, "PutItem", put, 200, nil)
	exec(`CREATE TRIGGER reject_item_delete BEFORE DELETE ON aws_dynamodb_items BEGIN SELECT RAISE(ABORT,'fixture-private-detail'); END`)
	failed("DeleteItem", key)
	failed("DeleteTable", `{"TableName":"rollback"}`)
	exec(`DROP TRIGGER reject_item_delete`)
	exec(`CREATE TRIGGER reject_table_delete BEFORE DELETE ON aws_dynamodb_tables BEGIN SELECT RAISE(ABORT,'fixture-private-detail'); END`)
	failed("DeleteTable", `{"TableName":"rollback"}`)
	exec(`DROP TRIGGER reject_table_delete`)
	item := dynamoCall(t, g, "GetItem", key, 200, nil)["Item"].(map[string]any)
	if item["value"].(map[string]any)["S"] != "retained" {
		t.Fatal("failed table deletion lost the retained item")
	}
	dynamoCall(t, g, "DeleteTable", `{"TableName":"rollback"}`, 200, nil)
}
