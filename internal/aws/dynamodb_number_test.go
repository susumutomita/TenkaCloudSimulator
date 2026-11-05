package aws

import (
	"math/big"
	"strings"
	"testing"
)

func TestDynamoDBDecimalCompatibility(t *testing.T) {
	// Published valid decimal keys retain the exact old persisted pk/sk spelling.
	for _, input := range []string{"2", "2.0", "1e2", "100", "0.1", "-19.5", "0.00001", "1e-130", "9.9999999999999999999999999999999999999e125", "-0.0", "+001.2300"} {
		rat, ok := new(big.Rat).SetString(input)
		if !ok {
			t.Fatal(input)
		}
		old := new(big.Float).SetPrec(512).SetRat(rat).Text('f', -1)
		got, _, ok := parseDynamoDecimal(input)
		if !ok || got != old {
			t.Fatalf("%q canonical=%q old=%q accepted=%v", input, got, old, ok)
		}
	}
	for _, input := range []string{"2.0", "2e0", "0002.000"} {
		if got, ok := canonicalDynamoNumber(input); !ok || got != "2" {
			t.Fatal(input, got, ok)
		}
	}
}

func TestDynamoDBDecimalBounds(t *testing.T) {
	for _, input := range []string{"0", "-0", "0e999999999", "1e-130", "-1e-130", "9.9999999999999999999999999999999999999e125", "-9.9999999999999999999999999999999999999e125", strings.Repeat("9", 38)} {
		if _, _, ok := parseDynamoDecimal(input); !ok {
			t.Fatalf("rejected %q", input)
		}
	}
	for _, input := range []string{"1/2", "0x10", "NaN", "Infinity", " 1", "1 ", "1e", "1e-131", "-1e126", "1e126", strings.Repeat("9", 39), "1e9999999999"} {
		if _, _, ok := parseDynamoDecimal(input); ok {
			t.Fatalf("accepted %q", input)
		}
	}
	for input, want := range map[string]int64{"0": 2, "1": 2, "12": 2, "123": 3, "-123.00": 3, "1000": 2, strings.Repeat("9", 38): 20} {
		if _, got, ok := parseDynamoDecimal(input); !ok || got != want {
			t.Fatal(input, got, want, ok)
		}
	}
}

func TestDynamoDBDecimalSavedKeyAndOriginalJSON(t *testing.T) {
	g, db := dynamoTestGateway(t, ":memory:")
	defer db.Close()
	dynamoCall(t, g, "CreateTable", dynamoCreateOrders, 200, nil)
	dynamoCall(t, g, "PutItem", `{"TableName":"orders","Item":{"id":{"S":"k"},"ts":{"N":"1.2300e2"}}}`, 200, nil)
	// Stored AttributeValue spelling is retained, only the separate key is normalized.
	got := dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"k"},"ts":{"N":"123"}}}`, 200, nil)
	if got["Item"].(map[string]any)["ts"].(map[string]any)["N"] != "1.2300e2" {
		t.Fatal("stored JSON rewritten")
	}
	for _, number := range []string{"1/2", "1e126", "1e-131", strings.Repeat("9", 39)} {
		body := `{"TableName":"orders","Item":{"id":{"S":"k"},"ts":{"N":"123"},"bad":{"N":"` + number + `"}}}`
		dynamoErrorCode(t, dynamoCall(t, g, "PutItem", body, 400, nil), "ValidationException")
	}
	got = dynamoCall(t, g, "GetItem", `{"TableName":"orders","Key":{"id":{"S":"k"},"ts":{"N":"123"}}}`, 200, nil)
	if len(got["Item"].(map[string]any)) != 2 {
		t.Fatal("invalid numeric write mutated item")
	}
}
