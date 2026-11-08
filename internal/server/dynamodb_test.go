package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func dynamoHTTPCall(t *testing.T, s *httptest.Server, op string, input any, code int, headers map[string]string) map[string]any {
	t.Helper()
	h := map[string]string{"X-Amz-Target": "DynamoDB_20120810." + op, "Content-Type": "application/x-amz-json-1.0"}
	for k, v := range headers {
		h[k] = v
	}
	body, _ := json.Marshal(input)
	data := status(t, s, "POST", "/", string(body), code, h)
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		t.Fatal(string(data))
	}
	return out
}

func TestDynamoDBLifecycleThroughServer(t *testing.T) {
	s := instance(t)
	create := map[string]any{
		"TableName":            "events",
		"AttributeDefinitions": []map[string]any{{"AttributeName": "pk", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "pk", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	}
	dynamoHTTPCall(t, s, "CreateTable", create, 200, nil)
	dynamoHTTPCall(t, s, "PutItem", map[string]any{"TableName": "events", "Item": map[string]any{"pk": map[string]any{"S": "e1"}, "msg": map[string]any{"S": "hello"}}}, 200, nil)
	got := dynamoHTTPCall(t, s, "GetItem", map[string]any{"TableName": "events", "Key": map[string]any{"pk": map[string]any{"S": "e1"}}}, 200, nil)
	if got["Item"].(map[string]any)["msg"].(map[string]any)["S"] != "hello" {
		t.Fatal(got)
	}
	dynamoHTTPCall(t, s, "DeleteItem", map[string]any{"TableName": "events", "Key": map[string]any{"pk": map[string]any{"S": "e1"}}}, 200, nil)
	dynamoHTTPCall(t, s, "DeleteTable", map[string]any{"TableName": "events"}, 200, nil)

	caps := status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil)
	var decoded map[string]any
	if json.Unmarshal(caps, &decoded) != nil {
		t.Fatal(string(caps))
	}
	services := decoded["services"].(map[string]any)
	if _, ok := services["dynamodb"]; !ok {
		t.Fatalf("capabilities missing dynamodb: %s", caps)
	}
}

func TestDynamoDBServerRestartPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sim.db")
	open := func() (*httptest.Server, func() error) {
		h, closeState, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(h), closeState
	}
	s1, close1 := open()
	dynamoHTTPCall(t, s1, "CreateTable", map[string]any{
		"TableName":            "kept",
		"AttributeDefinitions": []map[string]any{{"AttributeName": "pk", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "pk", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	}, 200, nil)
	dynamoHTTPCall(t, s1, "PutItem", map[string]any{"TableName": "kept", "Item": map[string]any{"pk": map[string]any{"S": "k"}, "v": map[string]any{"N": "42"}}}, 200, nil)
	s1.Close()
	if err := close1(); err != nil {
		t.Fatal(err)
	}
	s2, close2 := open()
	defer s2.Close()
	defer close2()
	got := dynamoHTTPCall(t, s2, "GetItem", map[string]any{"TableName": "kept", "Key": map[string]any{"pk": map[string]any{"S": "k"}}}, 200, nil)
	if got["Item"].(map[string]any)["v"].(map[string]any)["N"] != "42" {
		t.Fatal(got)
	}
}
