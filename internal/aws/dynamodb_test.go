package aws

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func dynamoTestGateway(t *testing.T, path string) (*Gateway, *core.Store) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := NewDynamoDBStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: db, Identities: core.NewIdentities(db), DynamoDB: tables}, db
}

func dynamoCall(t *testing.T, g *Gateway, op, body string, code int, headers map[string]string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("X-Amz-Target", "DynamoDB_20120810."+op)
	r.Header.Set("Content-Type", "application/x-amz-json-1.0")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != code {
		t.Fatalf("%s: want %d got %d %s", op, code, w.Code, w.Body.String())
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%s: non-JSON response %s", op, w.Body.String())
	}
	return out
}

func dynamoErrorCode(t *testing.T, out map[string]any, want string) {
	t.Helper()
	typ, _ := out["__type"].(string)
	if !strings.HasSuffix(typ, "#"+want) {
		t.Fatalf("want error %s, got %v", want, out)
	}
}

const dynamoCreateOrders = `{"TableName":"orders","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"},{"AttributeName":"ts","AttributeType":"N"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"},{"AttributeName":"ts","KeyType":"RANGE"}],"BillingMode":"PAY_PER_REQUEST"}`

func TestDynamoDBLifecycle(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()

	created := dynamoCall(t, g, "CreateTable", dynamoCreateOrders, 200, nil)
	desc := created["TableDescription"].(map[string]any)
	if desc["TableStatus"] != "ACTIVE" || desc["TableName"] != "orders" {
		t.Fatal(desc)
	}
	dup := dynamoCall(t, g, "CreateTable", dynamoCreateOrders, 400, nil)
	dynamoErrorCode(t, dup, "ResourceInUseException")

	listed := dynamoCall(t, g, "ListTables", `{}`, 200, nil)
	if names := listed["TableNames"].([]any); len(names) != 1 || names[0] != "orders" {
		t.Fatal(listed)
	}

	put := `{"TableName":"orders","Item":{"id":{"S":"a1"},"ts":{"N":"2"},"name":{"S":"widget"},"price":{"N":"19.5"},"ok":{"BOOL":true},"none":{"NULL":true},"tags":{"SS":["x","y"]},"nums":{"NS":["1","2"]},"blob":{"B":"aGk="},"hist":{"L":[{"S":"z"}]},"meta":{"M":{"k":{"S":"v"}}}}}`
	dynamoCall(t, g, "PutItem", put, 200, nil)

	// Numeric key equivalence: 2 and 2.0 are the same sort key.
	got := dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"a1"},"ts":{"N":"2.0"}}}`, 200, nil)
	item := got["Item"].(map[string]any)
	if item["name"].(map[string]any)["S"] != "widget" {
		t.Fatal(item)
	}
	described := dynamoCall(t, g, "DescribeTable", `{"TableName":"orders"}`, 200, nil)
	if described["Table"].(map[string]any)["ItemCount"] != float64(1) {
		t.Fatal(described)
	}

	// Conditional put on an existing item must fail; on the same key with
	// attribute_exists it must succeed (overwrite).
	failed := dynamoCall(t, g, "PutItem", `{"TableName":"orders","Item":{"id":{"S":"a1"},"ts":{"N":"2"}},"ConditionExpression":"attribute_not_exists(id)"}`, 400, nil)
	dynamoErrorCode(t, failed, "ConditionalCheckFailedException")
	dynamoCall(t, g, "PutItem", `{"TableName":"orders","Item":{"id":{"S":"a1"},"ts":{"N":"2"},"name":{"S":"v2"}},"ConditionExpression":"attribute_exists( id )"}`, 200, nil)
	badCond := dynamoCall(t, g, "PutItem", `{"TableName":"orders","Item":{"id":{"S":"a1"},"ts":{"N":"2"}},"ConditionExpression":"price > 3"}`, 400, nil)
	dynamoErrorCode(t, badCond, "ValidationException")

	// Key type and schema errors.
	wrongType := dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"N":"1"},"ts":{"N":"2"}}}`, 400, nil)
	dynamoErrorCode(t, wrongType, "ValidationException")
	missing := dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"a1"}}}`, 400, nil)
	dynamoErrorCode(t, missing, "ValidationException")
	extra := dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"a1"},"ts":{"N":"2"},"zz":{"S":"q"}}}`, 400, nil)
	dynamoErrorCode(t, extra, "ValidationException")
	putWrong := dynamoCall(t, g, "PutItem", `{"TableName":"orders","Item":{"id":{"S":"a1"},"ts":{"S":"2"}}}`, 400, nil)
	dynamoErrorCode(t, putWrong, "ValidationException")

	// Missing item: GetItem succeeds with no Item; DeleteItem succeeds;
	// conditional delete of a missing item fails.
	absent := dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"nope"},"ts":{"N":"9"}}}`, 200, nil)
	if _, ok := absent["Item"]; ok {
		t.Fatal(absent)
	}
	dynamoCall(t, g, "DeleteItem", `{"TableName":"orders","Key":{"id":{"S":"nope"},"ts":{"N":"9"}}}`, 200, nil)
	condDel := dynamoCall(t, g, "DeleteItem", `{"TableName":"orders","Key":{"id":{"S":"nope"},"ts":{"N":"9"}},"ConditionExpression":"attribute_exists(id)"}`, 400, nil)
	dynamoErrorCode(t, condDel, "ConditionalCheckFailedException")

	dynamoCall(t, g, "DeleteItem", `{"TableName":"orders","Key":{"id":{"S":"a1"},"ts":{"N":"2"}}}`, 200, nil)
	absent = dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"a1"},"ts":{"N":"2"}}}`, 200, nil)
	if _, ok := absent["Item"]; ok {
		t.Fatal(absent)
	}

	deleted := dynamoCall(t, g, "DeleteTable", `{"TableName":"orders"}`, 200, nil)
	if deleted["TableDescription"].(map[string]any)["TableStatus"] != "DELETING" {
		t.Fatal(deleted)
	}
	gone := dynamoCall(t, g, "DescribeTable", `{"TableName":"orders"}`, 400, nil)
	dynamoErrorCode(t, gone, "ResourceNotFoundException")
	noTable := dynamoCall(t, g, "PutItem", `{"TableName":"orders","Item":{"id":{"S":"x"}}}`, 400, nil)
	dynamoErrorCode(t, noTable, "ResourceNotFoundException")
}

func TestDynamoDBCreateValidation(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	cases := []string{
		`{"TableName":"ab","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`,                                                                         // name too short
		`{"TableName":"t1x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[],"BillingMode":"PAY_PER_REQUEST"}`,                                                                                                               // no HASH
		`{"TableName":"t2x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"zz","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`,                                                                        // undefined key
		`{"TableName":"t3x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"},{"AttributeName":"x","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`,                              // extra definition
		`{"TableName":"t4x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"BOOL"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`,                                                                     // bad key type
		`{"TableName":"t5x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST","ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1}}`, // throughput with on-demand
		`{"TableName":"t6x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}]}`,                                                                                                        // provisioned without throughput
		`{"TableName":"t7x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PROVISIONED","ProvisionedThroughput":{"ReadCapacityUnits":0,"WriteCapacityUnits":1}}`,     // zero units
		`{"TableName":"t8x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"AUTOPAY"}`,                                                                                // bad billing mode
		`{"TableName":"t9x","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST","StreamSpecification":{}}`,                                               // unsupported field
	}
	for _, body := range cases {
		out := dynamoCall(t, g, "CreateTable", body, 400, nil)
		dynamoErrorCode(t, out, "ValidationException")
	}
	ok := dynamoCall(t, g, "CreateTable", `{"TableName":"prov","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"N"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PROVISIONED","ProvisionedThroughput":{"ReadCapacityUnits":5,"WriteCapacityUnits":7}}`, 200, nil)
	desc := ok["TableDescription"].(map[string]any)
	if desc["ProvisionedThroughput"].(map[string]any)["ReadCapacityUnits"] != float64(5) {
		t.Fatal(desc)
	}
	// ListTables pagination.
	dynamoCall(t, g, "CreateTable", `{"TableName":"aaa","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, 200, nil)
	page := dynamoCall(t, g, "ListTables", `{"Limit":1}`, 200, nil)
	if page["LastEvaluatedTableName"] != "aaa" {
		t.Fatal(page)
	}
	rest := dynamoCall(t, g, "ListTables", `{"ExclusiveStartTableName":"aaa"}`, 200, nil)
	if names := rest["TableNames"].([]any); len(names) != 1 || names[0] != "prov" {
		t.Fatal(rest)
	}
	badLimit := dynamoCall(t, g, "ListTables", `{"Limit":101}`, 400, nil)
	dynamoErrorCode(t, badLimit, "ValidationException")
}

func TestDynamoDBItemValidationAndProtocol(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", `{"TableName":"kvxx","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, 200, nil)
	badItems := []string{
		`{"TableName":"kvxx","Item":{"id":{"S":"a"},"v":{"N":"abc"}}}`,
		`{"TableName":"kvxx","Item":{"id":{"S":"a"},"v":{"B":"!!!"}}}`,
		`{"TableName":"kvxx","Item":{"id":{"S":"a"},"v":{"SS":["x","x"]}}}`,
		`{"TableName":"kvxx","Item":{"id":{"S":"a"},"v":{"X":"1"}}}`,
		`{"TableName":"kvxx","Item":{"id":{"S":"a"},"v":{"S":"1","N":"2"}}}`,
		`{"TableName":"kvxx","Item":{"id":{"S":""}}}`,
		`{"TableName":"kvxx","Item":{"name":{"S":"no key"}}}`,
		`{"TableName":"kvxx","Item":{}}`,
	}
	for _, body := range badItems {
		out := dynamoCall(t, g, "PutItem", body, 400, nil)
		dynamoErrorCode(t, out, "ValidationException")
	}
	unknown := dynamoCall(t, g, "Scan", `{"TableName":"kvxx"}`, 501, nil)
	dynamoErrorCode(t, unknown, "UnknownOperationException")
	nullField := dynamoCall(t, g, "GetItem", `{"TableName":null,"Key":{"id":{"S":"a"}}}`, 400, nil)
	dynamoErrorCode(t, nullField, "ValidationException")
	notJSON := dynamoCall(t, g, "GetItem", `{oops`, 400, nil)
	dynamoErrorCode(t, notJSON, "ValidationException")
	session := dynamoCall(t, g, "GetItem", `{"TableName":"kvxx","Key":{"id":{"S":"a"}}}`, 400, map[string]string{"X-Amz-Security-Token": "x"})
	dynamoErrorCode(t, session, "ValidationException")
	badCred := dynamoCall(t, g, "GetItem", `{"TableName":"kvxx","Key":{"id":{"S":"a"}}}`, 403, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261104/us-east-1/dynamodb/aws4_request"})
	dynamoErrorCode(t, badCred, "AccessDeniedException")
	badRegion := dynamoCall(t, g, "GetItem", `{"TableName":"kvxx","Key":{"id":{"S":"a"}}}`, 400, map[string]string{"X-Simulator-Region": "eu-central-1"})
	dynamoErrorCode(t, badRegion, "ValidationException")
}

func TestDynamoDBIAMAndRegionIsolation(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", `{"TableName":"allowed","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, 200, nil)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:GetItem","Resource":"arn:aws:dynamodb:us-east-1:000000000000:table/allowed"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	reader := map[string]string{"X-Simulator-Principal": "reader"}
	dynamoCall(t, g, "GetItem", `{"TableName":"allowed","Key":{"id":{"S":"x"}}}`, 200, reader)
	denied := dynamoCall(t, g, "PutItem", `{"TableName":"allowed","Item":{"id":{"S":"x"}}}`, 403, reader)
	dynamoErrorCode(t, denied, "AccessDeniedException")
	deniedList := dynamoCall(t, g, "ListTables", `{}`, 403, reader)
	dynamoErrorCode(t, deniedList, "AccessDeniedException")

	tokyo := map[string]string{"X-Simulator-Region": "ap-northeast-1"}
	isolated := dynamoCall(t, g, "DescribeTable", `{"TableName":"allowed"}`, 400, tokyo)
	dynamoErrorCode(t, isolated, "ResourceNotFoundException")
}

func TestDynamoDBRestartPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	g, db := dynamoTestGateway(t, path)
	dynamoCall(t, g, "CreateTable", `{"TableName":"persist","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"B"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, 200, nil)
	dynamoCall(t, g, "PutItem", `{"TableName":"persist","Item":{"id":{"B":"a2V5"},"v":{"S":"still-here"}}}`, 200, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	g2, db2 := dynamoTestGateway(t, path)
	defer db2.Close()
	got := dynamoCall(t, g2, "GetItem", `{"TableName":"persist","Key":{"id":{"B":"a2V5"}}}`, 200, nil)
	if got["Item"].(map[string]any)["v"].(map[string]any)["S"] != "still-here" {
		t.Fatal(got)
	}
	listed := dynamoCall(t, g2, "ListTables", `{}`, 200, nil)
	if names := listed["TableNames"].([]any); len(names) != 1 || names[0] != "persist" {
		t.Fatal(listed)
	}
}
