package aws

import (
	"reflect"
	"strings"
	"testing"
)

// These are native refusal/recovery contracts, not coverage-line or SQL mock
// assertions. Fixtures change only dummy provider data; every failed request
// must preserve the complete owner+other-region table/item frame.
func dynamo416Frame(t *testing.T, g *Gateway) [][]any {
	t.Helper()
	var frame [][]any
	for _, query := range []string{"SELECT * FROM aws_dynamodb_tables ORDER BY 1,2,3", "SELECT * FROM aws_dynamodb_items ORDER BY 1,2,3,4"} {
		rows, err := g.Store.Database().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			frame = append(frame, values)
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
	return frame
}
func dynamo416Exec(t *testing.T, g *Gateway, query string, args ...any) {
	t.Helper()
	if _, err := g.Store.Database().Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func dynamo416Seed(t *testing.T, g *Gateway) {
	t.Helper()
	for _, headers := range []map[string]string{nil, {"X-Simulator-Region": "us-west-2"}} {
		dynamoCall(t, g, "CreateTable", dynamoSizeTable, 200, headers)
		dynamoCall(t, g, "PutItem", `{"TableName":"sizes","Item":{"id":{"S":"k"},"payload":{"S":"dummy-retained"}}}`, 200, headers)
	}
}
func dynamo416Internal(t *testing.T, g *Gateway, operation, body string) {
	t.Helper()
	before := dynamo416Frame(t, g)
	out := dynamoCall(t, g, operation, body, 500, nil)
	dynamoErrorCode(t, out, "InternalServerError")
	if out["message"] != "Simulator DynamoDB state operation failed." || len(out) != 2 {
		t.Fatal("native infrastructure error disclosed details or partial data")
	}
	if !reflect.DeepEqual(before, dynamo416Frame(t, g)) {
		t.Fatal("failed native operation changed owned or other-region state")
	}
}
func dynamo416Read(t *testing.T, g *Gateway, headers map[string]string, want string) {
	t.Helper()
	out := dynamoCall(t, g, "GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`, 200, headers)
	item, ok := out["Item"].(map[string]any)
	if !ok {
		t.Fatal("repaired native read omitted item")
	}
	value, ok := item["payload"].(map[string]any)
	if !ok || value["S"] != want {
		t.Fatal("repaired read returned wrong persisted value")
	}
}

func TestDynamoDB416CorruptMetadataFailsClosedAndRepairs(t *testing.T) {
	// Shape, generation and key roles are durable authority metadata. Corruption
	// must never be reinterpreted as a client400/missing table or delete its data.
	for _, tc := range []struct{ name, column, value string }{
		{"short-generation", "table_id", "short"},
		{"nonhex-generation", "table_id", strings.Repeat("z", 32)},
		{"three-key-roles", "schema", `{"keySchema":[{"AttributeName":"id","KeyType":"HASH"},{"AttributeName":"a","KeyType":"RANGE"},{"AttributeName":"b","KeyType":"RANGE"}],"definitions":{"id":"S","a":"S","b":"S"}}`},
		{"definition-cardinality", "schema", `{"keySchema":[{"AttributeName":"id","KeyType":"HASH"}],"definitions":{"id":"S","extra":"S"}}`},
		{"duplicate-key-name", "schema", `{"keySchema":[{"AttributeName":"id","KeyType":"HASH"},{"AttributeName":"id","KeyType":"RANGE"}],"definitions":{"id":"S","extra":"S"}}`},
		{"unknown-key-role", "schema", `{"keySchema":[{"AttributeName":"id","KeyType":"OTHER"}],"definitions":{"id":"S"}}`},
		{"range-without-hash", "schema", `{"keySchema":[{"AttributeName":"id","KeyType":"RANGE"}],"definitions":{"id":"S"}}`},
		{"two-hash-roles", "schema", `{"keySchema":[{"AttributeName":"id","KeyType":"HASH"},{"AttributeName":"extra","KeyType":"HASH"}],"definitions":{"id":"S","extra":"N"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			dynamo416Seed(t, g)
			var saved string
			if err := db.Database().QueryRow("SELECT " + tc.column + " FROM aws_dynamodb_tables WHERE region='us-east-1' AND name='sizes'").Scan(&saved); err != nil {
				t.Fatal(err)
			}
			dynamo416Exec(t, g, "UPDATE aws_dynamodb_tables SET "+tc.column+"=? WHERE region='us-east-1' AND name='sizes'", tc.value)
			corrupted := dynamo416Frame(t, g)
			// An unauthorised reader must not learn whether authority metadata is bad.
			denied := dynamoCall(t, g, "GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`, 403, map[string]string{"X-Simulator-Principal": "no-grant"})
			dynamoErrorCode(t, denied, "AccessDeniedException")
			if !reflect.DeepEqual(corrupted, dynamo416Frame(t, g)) {
				t.Fatal("denial modified corrupt or peer state")
			}
			for _, op := range []struct{ operation, body string }{
				{"DescribeTable", `{"TableName":"sizes"}`}, {"DeleteTable", `{"TableName":"sizes"}`},
				{"GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`},
				{"PutItem", `{"TableName":"sizes","Item":{"id":{"S":"k"},"payload":{"S":"must-not-save"}}}`},
				{"DeleteItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`},
			} {
				dynamo416Internal(t, g, op.operation, op.body)
			}
			dynamo416Exec(t, g, "UPDATE aws_dynamodb_tables SET "+tc.column+"=? WHERE region='us-east-1' AND name='sizes'", saved)
			dynamo416Read(t, g, nil, "dummy-retained")
			dynamo416Read(t, g, map[string]string{"X-Simulator-Region": "us-west-2"}, "dummy-retained")
			stats := dynamoCall(t, g, "DescribeTable", `{"TableName":"sizes"}`, 200, nil)["Table"].(map[string]any)
			if stats["ItemCount"] != float64(1) {
				t.Fatal("repair changed item accounting")
			}
		})
	}
}

func TestDynamoDB416CorruptItemReadRefusalAndRepair(t *testing.T) {
	for _, bad := range []string{`not JSON`, `[]`, `{}`, `{"id":{"S":"k"},"payload":{"BOOL":"not-a-bool"}}`} {
		t.Run("stored-document-"+strings.ReplaceAll(bad[:1], "{", "object"), func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			dynamo416Seed(t, g)
			var original string
			if err := db.Database().QueryRow("SELECT item FROM aws_dynamodb_items WHERE region='us-east-1'").Scan(&original); err != nil {
				t.Fatal(err)
			}
			dynamo416Exec(t, g, "UPDATE aws_dynamodb_items SET item=? WHERE region='us-east-1'", bad)
			dynamo416Internal(t, g, "GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`)
			dynamo416Internal(t, g, "DescribeTable", `{"TableName":"sizes"}`)
			dynamo416Exec(t, g, "UPDATE aws_dynamodb_items SET item=? WHERE region='us-east-1'", original)
			dynamo416Read(t, g, nil, "dummy-retained")
			dynamo416Read(t, g, map[string]string{"X-Simulator-Region": "us-west-2"}, "dummy-retained")
		})
	}
}

func TestDynamoDB416UnavailableStorageColumnsRejectAndRecover(t *testing.T) {
	for _, tc := range []struct {
		table, column string
		operations    []struct{ operation, body string }
	}{
		{"aws_dynamodb_tables", "schema", []struct{ operation, body string }{
			{"DescribeTable", `{"TableName":"sizes"}`}, {"DeleteTable", `{"TableName":"sizes"}`},
			{"GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`},
		}},
		{"aws_dynamodb_tables", "name", []struct{ operation, body string }{
			{"ListTables", `{}`}, {"CreateTable", strings.Replace(dynamoSizeTable, "sizes", "newtable", 1)},
		}},
		{"aws_dynamodb_items", "table_name", []struct{ operation, body string }{
			{"DescribeTable", `{"TableName":"sizes"}`}, {"DeleteTable", `{"TableName":"sizes"}`},
			{"GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`},
			{"PutItem", `{"TableName":"sizes","Item":{"id":{"S":"new"},"payload":{"S":"must-not-save"}}}`},
			{"DeleteItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`},
		}},
	} {
		t.Run(tc.table+"-"+tc.column, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			dynamo416Seed(t, g)
			dynamo416Exec(t, g, "ALTER TABLE "+tc.table+" RENAME COLUMN "+tc.column+" TO fixture_unavailable")
			for _, op := range tc.operations {
				dynamo416Internal(t, g, op.operation, op.body)
			}
			dynamo416Exec(t, g, "ALTER TABLE "+tc.table+" RENAME COLUMN fixture_unavailable TO "+tc.column)
			dynamo416Read(t, g, nil, "dummy-retained")
			dynamo416Read(t, g, map[string]string{"X-Simulator-Region": "us-west-2"}, "dummy-retained")
			// Recovery is a real new write/read/delete lifecycle, not merely absence of
			// an error. It must not affect the independent region using the same names.
			dynamoCall(t, g, "PutItem", `{"TableName":"sizes","Item":{"id":{"S":"k"},"payload":{"S":"repaired"}}}`, 200, nil)
			dynamo416Read(t, g, nil, "repaired")
			dynamoCall(t, g, "DeleteTable", `{"TableName":"sizes"}`, 200, nil)
			dynamoErrorCode(t, dynamoCall(t, g, "DescribeTable", `{"TableName":"sizes"}`, 400, nil), "ResourceNotFoundException")
			dynamo416Read(t, g, map[string]string{"X-Simulator-Region": "us-west-2"}, "dummy-retained")
			// Recreating the name must yield a genuinely fresh, empty generation.
			dynamoCall(t, g, "CreateTable", dynamoSizeTable, 200, nil)
			if _, found := dynamoCall(t, g, "GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`, 200, nil)["Item"]; found {
				t.Fatal("recreated table adopted prior items")
			}
		})
	}
}
