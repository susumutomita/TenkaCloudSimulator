package aws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func dynamoOwnedRowSnapshot(t *testing.T, g *Gateway) [][]any {
	t.Helper()
	var state [][]any
	for _, query := range []string{
		"SELECT region,name,table_id,schema,billing,read_units,write_units,created FROM aws_dynamodb_tables ORDER BY region,name",
		"SELECT region,table_name,pk,sk,item FROM aws_dynamodb_items ORDER BY region,table_name,pk,sk",
	} {
		rows, err := g.Store.Database().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		state = append(state, []any{query})
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			state = append(state, values)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	return state
}

func TestDynamoDBIgnoredNativeWriteIsRejectedAtomically(t *testing.T) {
	for _, tc := range []struct {
		name, trigger, operation, body string
		seed                           bool
	}{
		{"create-table", "BEFORE INSERT ON aws_dynamodb_tables", "CreateTable", dynamoSizeTable, false},
		{"put-new", "BEFORE INSERT ON aws_dynamodb_items", "PutItem", `{"TableName":"sizes","Item":{"id":{"S":"new"},"payload":{"S":"changed"}}}`, true},
		{"put-replace", "BEFORE UPDATE ON aws_dynamodb_items", "PutItem", `{"TableName":"sizes","Item":{"id":{"S":"k"},"payload":{"S":"changed"}}}`, true},
		{"delete-item", "BEFORE DELETE ON aws_dynamodb_items", "DeleteItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`, true},
		{"delete-table-child", "BEFORE DELETE ON aws_dynamodb_items", "DeleteTable", `{"TableName":"sizes"}`, true},
		{"delete-table-parent", "BEFORE DELETE ON aws_dynamodb_tables", "DeleteTable", `{"TableName":"sizes"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			if tc.seed {
				dynamoCall(t, g, "CreateTable", dynamoSizeTable, 200, nil)
				dynamoCall(t, g, "PutItem", `{"TableName":"sizes","Item":{"id":{"S":"k"},"payload":{"S":"original"}}}`, 200, nil)
			}
			before := dynamoOwnedRowSnapshot(t, g)
			if _, err := db.Database().Exec("CREATE TRIGGER ddb_ignore " + tc.trigger + " BEGIN SELECT RAISE(IGNORE); END"); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/x-amz-json-1.0")
			request.Header.Set("X-Amz-Target", "DynamoDB_20120810."+tc.operation)
			response := httptest.NewRecorder()
			g.ServeHTTP(response, request)
			after := dynamoOwnedRowSnapshot(t, g)
			preserved := reflect.DeepEqual(before, after)
			var output map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
				t.Fatal("non-JSON native response")
			}
			t.Logf("operation=%s HTTP=%d framePreserved=%t nativeType=%v", tc.operation, response.Code, preserved, output["__type"])
			if response.Code != 500 {
				t.Errorf("ignored write acknowledged: HTTP=%d; want bounded500", response.Code)
			}
			if response.Code == 500 && output["__type"] != "com.amazonaws.dynamodb.v20120810#InternalServerError" {
				t.Error("unexpected native error type")
			}
			if !preserved {
				t.Error("failed write changed an owned table/item row")
			}
			if tc.name == "delete-item" && response.Code == 500 {
				// Zero affected rows is correct for a genuinely missing, unconditional item.
				dynamoCall(t, g, "DeleteItem", `{"TableName":"sizes","Key":{"id":{"S":"absent"}}}`, 200, nil)
				if !reflect.DeepEqual(before, dynamoOwnedRowSnapshot(t, g)) {
					t.Error("missing-item delete changed existing state")
				}
			}
		})
	}
}
