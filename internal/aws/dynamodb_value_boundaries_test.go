package aws

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDynamoDBSetUniquenessUsesValuesAndPreservesRejectedReplacement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value map[string]any
	}{
		{"decimal-alias", map[string]any{"NS": []any{"1", "1.0"}}},
		{"exponent-alias", map[string]any{"NS": []any{"100", "1e2"}}},
		{"signed-zero", map[string]any{"NS": []any{"0", "-0.0"}}},
		{"binary-padding-alias", map[string]any{"BS": []any{"YQ==", "YR=="}}},
		{"binary-line-break-alias", map[string]any{"BS": []any{"YQ==", "YQ==\n"}}},
		{"nested-number-set", map[string]any{"M": map[string]any{"set": map[string]any{"NS": []any{"0.01", "1e-2"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			dynamoCall(t, g, "CreateTable", dynamoGuardTable, 200, nil)
			dynamoCall(t, g, "PutItem", `{"TableName":"guard","Item":{"id":{"S":"stable"},"value":{"S":"retained"}}}`, 200, nil)
			key := `{"TableName":"guard","Key":{"id":{"S":"stable"}}}`
			before := dynamoCall(t, g, "GetItem", key, 200, nil)
			stats := dynamoCall(t, g, "DescribeTable", `{"TableName":"guard"}`, 200, nil)
			request := map[string]any{"TableName": "guard", "Item": map[string]any{"id": map[string]any{"S": "stable"}, "value": tc.value}}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			dynamoErrorCode(t, dynamoCall(t, g, "PutItem", string(body), 400, nil), "ValidationException")
			if after := dynamoCall(t, g, "GetItem", key, 200, nil); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected set replaced the existing item")
			}
			if after := dynamoCall(t, g, "DescribeTable", `{"TableName":"guard"}`, 200, nil); !reflect.DeepEqual(stats, after) {
				t.Fatal("rejected set changed table statistics")
			}
		})
	}
	// Numeric aliases remain distinct string values; valid sets preserve their
	// supplied value representation rather than rewriting persisted documents.
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", dynamoGuardTable, 200, nil)
	item := map[string]any{
		"id": map[string]any{"S": "valid"},
		"ss": map[string]any{"SS": []any{"1", "1.0", ""}},
		"ns": map[string]any{"NS": []any{"1.0", "2e0", "-0"}},
		"bs": map[string]any{"BS": []any{"YQ==", "Yg==", ""}},
	}
	body, err := json.Marshal(map[string]any{"TableName": "guard", "Item": item})
	if err != nil {
		t.Fatal(err)
	}
	dynamoCall(t, g, "PutItem", string(body), 200, nil)
	got := dynamoCall(t, g, "GetItem", `{"TableName":"guard","Key":{"id":{"S":"valid"}}}`, 200, nil)["Item"]
	if !reflect.DeepEqual(item, got) {
		t.Fatal("valid set representations were changed")
	}
}

func TestDynamoDBKeyBytesUseDecodedContentAndPreserveRejectedState(t *testing.T) {
	for _, kind := range []string{"S", "UTF8", "B"} {
		for _, part := range []string{"partition", "sort"} {
			t.Run(kind+"/"+part, func(t *testing.T) {
				g, db := dynamoTestGateway(t, ":memory:")
				defer db.Close()
				typeName, keyName, limit := kind, "id", 2048
				if kind == "UTF8" {
					typeName = "S"
				}
				definitions := []map[string]string{{"AttributeName": "id", "AttributeType": typeName}}
				schema := []map[string]string{{"AttributeName": "id", "KeyType": "HASH"}}
				if part == "sort" {
					keyName, limit = "sort", 1024
					definitions[0]["AttributeType"] = "S"
					definitions = append(definitions, map[string]string{"AttributeName": "sort", "AttributeType": typeName})
					schema = append(schema, map[string]string{"AttributeName": "sort", "KeyType": "RANGE"})
				}
				create, err := json.Marshal(map[string]any{"TableName": "key-limits", "AttributeDefinitions": definitions, "KeySchema": schema, "BillingMode": "PAY_PER_REQUEST"})
				if err != nil {
					t.Fatal(err)
				}
				dynamoCall(t, g, "CreateTable", string(create), 200, nil)
				keyForBytes := func(length int) (map[string]any, string) {
					value := strings.Repeat("x", length)
					if kind == "UTF8" {
						value = strings.Repeat("あ", length/3) + strings.Repeat("x", length%3)
					}
					decoded := value
					if kind == "B" {
						value = base64.StdEncoding.EncodeToString([]byte(value))
					}
					key := map[string]any{keyName: map[string]any{typeName: value}}
					if part == "sort" {
						key["id"] = map[string]any{"S": "fixed"}
					}
					return key, decoded
				}
				request := func(field string, key map[string]any) string {
					t.Helper()
					body, err := json.Marshal(map[string]any{"TableName": "key-limits", field: key})
					if err != nil {
						t.Fatal(err)
					}
					return string(body)
				}
				for _, size := range []int{limit - 1, limit} {
					key, _ := keyForBytes(size)
					dynamoCall(t, g, "PutItem", request("Item", key), 200, nil)
					if got := dynamoCall(t, g, "GetItem", request("Key", key), 200, nil)["Item"]; !reflect.DeepEqual(key, got) {
						t.Fatal("accepted exact-boundary key did not round-trip")
					}
				}
				stats := dynamoCall(t, g, "DescribeTable", `{"TableName":"key-limits"}`, 200, nil)
				oversize, decoded := keyForBytes(limit + 1)
				for _, operation := range []string{"PutItem", "GetItem", "DeleteItem"} {
					field := "Key"
					if operation == "PutItem" {
						field = "Item"
					}
					dynamoErrorCode(t, dynamoCall(t, g, operation, request(field, oversize), 400, nil), "ValidationException")
				}
				// The native writer must apply the same limit even when the caller
				// bypasses HTTP and supplies a matching canonical key directly.
				table, err := g.DynamoDB.GetTable("us-east-1", "key-limits")
				if err != nil {
					t.Fatal(err)
				}
				partitionKey, sortKey := typeName+"\x00"+decoded, ""
				if part == "sort" {
					partitionKey, sortKey = "S\x00fixed", typeName+"\x00"+decoded
				}
				encoded, err := json.Marshal(oversize)
				if err != nil {
					t.Fatal(err)
				}
				err = g.DynamoDB.PutItem(table, partitionKey, sortKey, string(encoded), "")
				var failure dynamoFailure
				if !errors.As(err, &failure) || failure.code != "ValidationException" {
					t.Fatal("native writer did not reject an oversized key with ValidationException", err)
				}
				if after := dynamoCall(t, g, "DescribeTable", `{"TableName":"key-limits"}`, 200, nil); !reflect.DeepEqual(stats, after) {
					t.Fatal("oversized key changed table statistics")
				}
				for _, size := range []int{limit - 1, limit} {
					key, _ := keyForBytes(size)
					if got := dynamoCall(t, g, "GetItem", request("Key", key), 200, nil)["Item"]; !reflect.DeepEqual(key, got) {
						t.Fatal("rejected key changed an existing boundary item")
					}
					dynamoCall(t, g, "DeleteItem", request("Key", key), 200, nil)
				}
				if count := dynamoCall(t, g, "DescribeTable", `{"TableName":"key-limits"}`, 200, nil)["Table"].(map[string]any)["ItemCount"]; count != float64(0) {
					t.Fatal("exact-boundary items could not be cleaned up")
				}
			})
		}
	}
}
