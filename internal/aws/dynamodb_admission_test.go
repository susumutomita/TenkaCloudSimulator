package aws

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Independent literal native oracles. These execute Go, not the finite TLA model.
func TestDynamoDBAdmissionLiteralKeysAndSemanticBytes(t *testing.T) {
	for _, tc := range []struct{ kind, raw, want string }{
		{"N", "1", "N\x001"}, {"N", "1.0", "N\x001"}, {"N", "1e0", "N\x001"},
		{"N", "2", "N\x002"}, {"S", "1", "S\x001"}, {"B", "MQ==", "B\x001"},
	} {
		got, err := dynamoKeyComponent(map[string]any{tc.kind: tc.raw}, tc.kind)
		if err != nil || got != tc.want {
			t.Fatalf("literal typed-key oracle failed: kind=%s", tc.kind)
		}
	}
	for _, raw := range []string{"1/2", strings.Repeat("9", 39), "1e126"} {
		if _, err := dynamoKeyComponent(map[string]any{"N": raw}, "N"); err == nil {
			t.Error("invalid numeric key admitted")
		}
	}
	for _, tc := range []struct {
		name  string
		value map[string]any
		want  int64
	}{
		{"small", map[string]any{"S": "x"}, 2},
		{"utf8", map[string]any{"S": "あ"}, 4},
		{"binary", map[string]any{"B": "YWI="}, 3},
		{"escaped", map[string]any{"S": "<<"}, 3},
		{"nested", map[string]any{"L": []any{}}, 4},
		{"over", map[string]any{"S": "xxxx"}, 5},
	} {
		item := map[string]any{"v": tc.value}
		before, _ := json.Marshal(item)
		size, err := dynamoItemSize(item)
		if err != nil || size != tc.want {
			t.Errorf("%s got semantic bytes=%d want=%d err=%v", tc.name, size, tc.want, err)
		}
		after, _ := json.Marshal(item)
		if string(before) != string(after) {
			t.Error("measurement rewrote input")
		}
	}
}

func TestDynamoDBStoreAdmissionRejectsOversizedReplacement(t *testing.T) {
	g, table, closeDB := dynamoGenerationFixture(t)
	defer closeDB()
	original := `{"id":{"S":"k"},"payload":{"S":"original"}}`
	if err := g.DynamoDB.PutItem(table, "S\x00k", "", original, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": map[string]any{"S": "k"}, "payload": map[string]any{"S": strings.Repeat("x", maxDynamoDBItemBytes)}})
	err := g.DynamoDB.PutItem(table, "S\x00k", "", string(raw), "")
	var failure dynamoFailure
	if !errors.As(err, &failure) || failure.code != "ValidationException" {
		t.Fatal("oversized Store request was not refused")
	}
	item, ok, err := g.DynamoDB.GetItem(table, "S\x00k", "")
	if err != nil || !ok || item["payload"].(map[string]any)["S"] != "original" {
		t.Fatal("refused replacement changed native item")
	}
	count, size, err := g.DynamoDB.TableStats(table)
	if err != nil || count != 1 || size != 18 {
		t.Fatal("refused replacement changed literal stats", count, size, err)
	}
}
