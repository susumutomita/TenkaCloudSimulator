package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dynamoSizeTable = `{"TableName":"sizes","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`

func dynamoSizePut(t *testing.T, g *Gateway, item map[string]any, condition string, code int) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"TableName": "sizes", "Item": item, "ConditionExpression": condition})
	if err != nil {
		t.Fatal(err)
	}
	out := dynamoCall(t, g, "PutItem", string(b), code, nil)
	if code == 400 {
		dynamoErrorCode(t, out, "ValidationException")
	}
}
func dynamoSizeStats(t *testing.T, g *Gateway, count, size int64) {
	t.Helper()
	out := dynamoCall(t, g, "DescribeTable", `{"TableName":"sizes"}`, 200, nil)["Table"].(map[string]any)
	if out["ItemCount"] != float64(count) || out["TableSizeBytes"] != float64(size) {
		t.Fatalf("stats count=%v size=%v want=%d/%d", out["ItemCount"], out["TableSizeBytes"], count, size)
	}
}
func TestDynamoDBSemanticSizeBoundaries(t *testing.T) {
	for _, testKind := range []string{"S", "B", "S-UTF8"} {
		kind := testKind
		if kind == "S-UTF8" {
			kind = "S"
		}
		t.Run(testKind, func(t *testing.T) {
			g, db := dynamoTestGateway(t, ":memory:")
			defer db.Close()
			dynamoCall(t, g, "CreateTable", dynamoSizeTable, 200, nil)
			for _, size := range []int{maxDynamoDBItemBytes - 1, maxDynamoDBItemBytes, maxDynamoDBItemBytes + 1} {
				raw := strings.Repeat("x", size-10) // id(2)+k(1)+payload(7)
				if testKind == "S-UTF8" {
					raw = strings.Repeat("あ", (size-10)/3) + strings.Repeat("x", (size-10)%3)
				}
				value := raw
				if kind == "B" {
					value = base64.StdEncoding.EncodeToString([]byte(raw))
				}
				code := 200
				if size > maxDynamoDBItemBytes {
					code = 400
				}
				dynamoSizePut(t, g, map[string]any{"id": map[string]any{"S": "k"}, "payload": map[string]any{kind: value}}, "", code)
				expected := size
				if code == 400 {
					expected--
				}
				dynamoSizeStats(t, g, 1, int64(expected))
				got := dynamoCall(t, g, "GetItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`, 200, nil)["Item"].(map[string]any)["payload"].(map[string]any)[kind].(string)
				if kind == "B" {
					decoded, err := base64.StdEncoding.DecodeString(got)
					if err != nil {
						t.Fatal(err)
					}
					got = string(decoded)
				}
				if len(got) != expected-10 {
					t.Fatal("oversized write changed item", len(got), expected)
				}
			}
		})
	}
}
func TestDynamoDBSemanticSizeFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		item map[string]any
		want int64
	}{
		{"utf8-names", map[string]any{"名": map[string]any{"S": "あ😀"}}, 10},
		{"escaped", map[string]any{"s": map[string]any{"S": strings.Repeat("<", 70000)}}, 70001},
		{"binary", map[string]any{"b": map[string]any{"B": base64.StdEncoding.EncodeToString(make([]byte, 350000))}}, 350001},
		{"empty-containers", map[string]any{"l": map[string]any{"L": []any{}}, "m": map[string]any{"M": map[string]any{}}}, 8},
		// m-name1 + map3 + (child1+name3+list3 + (child1+S3)+(child1+BOOL1)) =17
		{"nested", map[string]any{"m": map[string]any{"M": map[string]any{"名": map[string]any{"L": []any{map[string]any{"S": "あ"}, map[string]any{"BOOL": false}}}}}}, 17},
		{"sets", map[string]any{"s": map[string]any{"SS": []any{"あ", "<"}}, "b": map[string]any{"BS": []any{"YQ==", "YWJj"}}, "n": map[string]any{"NS": []any{"12", "123"}}, "z": map[string]any{"NULL": true}}, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dynamoItemSize(tc.item)
			if err != nil || got != tc.want {
				t.Fatal(got, tc.want, err)
			}
		})
	}
	for _, bad := range []map[string]any{nil, {"s": map[string]any{"S": 1}}, {"b": map[string]any{"B": "!"}}, {"n": map[string]any{"N": "bad"}}} {
		if _, err := dynamoItemSize(bad); err == nil {
			t.Fatal("invalid stored item accepted")
		}
	}
}
func TestDynamoDBSemanticStatsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	g, db := dynamoTestGateway(t, path)
	dynamoCall(t, g, "CreateTable", dynamoSizeTable, 200, nil)
	item := map[string]any{"id": map[string]any{"S": "k"}, "payload": map[string]any{"S": strings.Repeat("あ", 130000)}}
	dynamoSizePut(t, g, item, "", 200)
	dynamoSizeStats(t, g, 1, 390010)
	body := `{"TableName":"sizes","Item":{"id":{"S":"k"},"payload":{"S":"changed"}},"ConditionExpression":"attribute_not_exists(id)"}`
	dynamoErrorCode(t, dynamoCall(t, g, "PutItem", body, 400, nil), "ConditionalCheckFailedException")
	dynamoSizeStats(t, g, 1, 390010)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	g, db = dynamoTestGateway(t, path)
	defer db.Close()
	dynamoSizeStats(t, g, 1, 390010)
	dynamoSizePut(t, g, map[string]any{"id": map[string]any{"S": "k"}, "payload": map[string]any{"S": "<"}}, "", 200)
	dynamoSizeStats(t, g, 1, 11)
	// An aborted DeleteTable must roll back the earlier DELETE of its items.
	_, err := db.Database().Exec(`CREATE TRIGGER size_delete_abort BEFORE DELETE ON aws_dynamodb_tables BEGIN SELECT RAISE(ABORT,'fault'); END`)
	if err != nil {
		t.Fatal(err)
	}
	dynamoCall(t, g, "DeleteTable", `{"TableName":"sizes"}`, 500, nil)
	dynamoSizeStats(t, g, 1, 11)
	if _, err = db.Database().Exec(`DROP TRIGGER size_delete_abort`); err != nil {
		t.Fatal(err)
	}
	dynamoCall(t, g, "DeleteItem", `{"TableName":"sizes","Key":{"id":{"S":"k"}}}`, 200, nil)
	dynamoSizeStats(t, g, 0, 0)
	dynamoSizePut(t, g, item, "", 200)
	if _, err = db.Database().Exec(`UPDATE aws_dynamodb_items SET item='{"id":{"N":"invalid"}}'`); err != nil {
		t.Fatal(err)
	}
	dynamoCall(t, g, "DescribeTable", `{"TableName":"sizes"}`, 500, nil)
	if _, err = db.Database().Exec(`UPDATE aws_dynamodb_items SET item='not JSON'`); err != nil {
		t.Fatal(err)
	}
	dynamoCall(t, g, "DescribeTable", `{"TableName":"sizes"}`, 500, nil)
}

// Optional developer SDK conformance test; normal tests above need no Python.
// Explicitly use an already installed boto3 interpreter, never install at runtime.
func TestDynamoDBSemanticSizeSDKHTTP(t *testing.T) {
	python := os.Getenv("DYNAMODB_SIZE_SDK_PYTHON")
	if python == "" {
		t.Skip("set DYNAMODB_SIZE_SDK_PYTHON to an installed boto3 interpreter")
	}
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	server := httptest.NewServer(g)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "testdata/dynamodb_size_sdk.py", server.URL)
	cmd.Env = append(os.Environ(), "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_EC2_METADATA_DISABLED=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("boto3 HTTP failed: %v %s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
