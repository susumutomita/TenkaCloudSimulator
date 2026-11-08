package aws

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func stackConditionTestCopy(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var copied any
	if err = json.Unmarshal(raw, &copied); err != nil {
		t.Fatal(err)
	}
	return copied
}

func TestStackConditionalCryptoParameterContract(t *testing.T) {
	definitions := map[string]any{
		"ScoreStealEnabled":   map[string]any{"Type": "String", "Default": "false", "AllowedValues": []any{"false", "true"}},
		"ScoreStealKey":       map[string]any{"Type": "Number", "Default": "3", "MinValue": "1", "MaxValue": "9"},
		"ScoreStealSeed":      map[string]any{"Type": "String", "NoEcho": true, "MinLength": "32", "MaxLength": "32", "AllowedPattern": "^[A-Za-z0-9]+$"},
		"NamePrefix":          map[string]any{"Type": "String", "MinLength": "5", "MaxLength": "80", "AllowedPattern": "^tc-[a-z0-9]+(-[a-z0-9]+)+$"},
		"TenkaCloudAccountId": map[string]any{"Type": "String", "AllowedPattern": "^[0-9]{12}$"},
		"ExternalId":          map[string]any{"Type": "String", "NoEcho": true, "MinLength": "16"},
	}
	supplied := map[string]string{"ScoreStealSeed": strings.Repeat("D", 32), "NamePrefix": "tc-crypto-team", "TenkaCloudAccountId": "000000000000", "ExternalId": strings.Repeat("L", 16)}
	before := stackConditionTestCopy(t, definitions)
	values, err := stackParameterValues(definitions, supplied)
	if err != nil {
		t.Fatal(err)
	}
	if values["ScoreStealEnabled"] != "false" || values["ScoreStealKey"] != "3" || len(values) != 6 {
		t.Fatal("original parameter defaults were not preserved")
	}
	values["NamePrefix"] = "changed"
	if !reflect.DeepEqual(before, definitions) || supplied["NamePrefix"] != "tc-crypto-team" {
		t.Fatal("parameter preparation mutated caller state")
	}
	for _, key := range []string{"1", "9", "1.5", "9e0"} {
		supplied["ScoreStealKey"] = key
		if _, err := stackParameterValues(definitions, supplied); err != nil {
			t.Fatalf("valid Number rejected: %v", err)
		}
	}
	for _, key := range []string{"0", "10", "NaN", "Inf", "1/2", "1e309", "01", "1e-309"} {
		supplied["ScoreStealKey"] = key
		if result, err := stackParameterValues(definitions, supplied); err == nil || result != nil {
			t.Fatal("invalid Number produced parameter values")
		}
	}
	delete(supplied, "ScoreStealKey")
	supplied["ScoreStealEnabled"] = "TRUE"
	if _, err := stackParameterValues(definitions, supplied); err == nil {
		t.Fatal("String AllowedValues admitted a case alias")
	}
	supplied["ScoreStealEnabled"] = "true"
	supplied["ScoreStealSeed"] = strings.Repeat("-", 32)
	if _, err := stackParameterValues(definitions, supplied); err == nil {
		t.Fatal("original Seed character constraint was omitted")
	}
	supplied["ScoreStealSeed"] = "DUMMY-NOT-AN-ACTUAL-SECRET"
	if _, err := stackParameterValues(definitions, supplied); err == nil || strings.Contains(err.Error(), supplied["ScoreStealSeed"]) {
		t.Fatal("invalid private parameter was accepted or reflected")
	}
}

func TestStackConditionalParameterSchemaAndExactNumbers(t *testing.T) {
	cases := []map[string]any{
		{"Type": "Number", "Default": "3", "MinValue": "9", "MaxValue": "1"},
		{"Type": "String", "Default": "x", "MinLength": "2", "MaxLength": "1"},
		{"Type": "Number", "Default": "3", "AllowedPattern": ".*"},
		{"Type": "String", "Default": "x", "MinValue": "1"},
		{"Type": "String", "Default": false},
		{"Type": "String", "Default": "x", "AllowedValues": []any{false}},
		{"Type": "String", "Default": "x", "AllowedValues": []any{}},
		{"Type": "String", "Default": "x", "AllowedPattern": "["},
		{"Type": "String", "Default": "x", "MinLength": "-1"},
		{"Type": "String", "Default": "x", "NoEcho": "true"},
		{"Type": "String", "Default": "x", "Unsupported": "ignored"},
		{"Type": "CommaDelimitedList", "Default": "x"},
		{"Type": "Number", "Default": "0", "MinValue": "1"},
	}
	for i, d := range cases {
		if value, err := stackParameterValues(map[string]any{"Value": d}, map[string]string{"Value": "3"}); err == nil || value != nil {
			t.Fatalf("invalid definition %d was silently normalized", i)
		}
	}
	if _, err := stackParameterValues(map[string]any{"Value": map[string]any{"Type": "String"}}, nil); err == nil {
		t.Fatal("missing required value accepted")
	}
	if _, err := stackParameterValues(nil, map[string]string{"Unknown": "dummy"}); err == nil {
		t.Fatal("unknown supplied parameter accepted")
	}
	// Exact rational comparison must not merge adjacent integers through float64.
	d := map[string]any{"Value": map[string]any{"Type": "Number", "Default": json.Number("9007199254740993"), "MinValue": "9007199254740993", "MaxValue": "9007199254740993"}}
	if _, err := stackParameterValues(d, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := stackParameterValues(d, map[string]string{"Value": "9007199254740992"}); err == nil {
		t.Fatal("numeric rounding widened allowed value")
	}
	if _, err := stackParameterValues(map[string]any{"Value": map[string]any{"Type": "Number", "Default": "1.0", "AllowedValues": []any{"1e0"}}}, nil); err != nil {
		t.Fatal("equal numeric AllowedValues rejected")
	}
}

func TestStackConditionalDefinitionsRejectUnknownAndCycles(t *testing.T) {
	refs := map[string]string{"ScoreStealEnabled": "false", "AWS::Region": "us-east-1"}
	defs := map[string]any{"Enabled": map[string]any{"Fn::Equals": []any{map[string]any{"Ref": "ScoreStealEnabled"}, "true"}}, "Alias": map[string]any{"Condition": "Enabled"}}
	values, err := stackConditionValues(defs, refs)
	if err != nil || values["Enabled"] || values["Alias"] {
		t.Fatal("false condition/alias changed")
	}
	refs["ScoreStealEnabled"] = "true"
	values, err = stackConditionValues(defs, refs)
	if err != nil || !values["Enabled"] || !values["Alias"] {
		t.Fatal("true condition/alias changed")
	}
	bad := []map[string]any{
		{"A": map[string]any{"Fn::Equals": []any{map[string]any{"Ref": "Resource"}, "true"}}},
		{"A": map[string]any{"Condition": "B"}},
		{"A": map[string]any{"Condition": "B"}, "B": map[string]any{"Condition": "A"}},
		{"A": map[string]any{"Fn::Equals": []any{"a"}}},
		{"A": map[string]any{"Fn::Equals": []any{true, "true"}}},
		{"A": map[string]any{"Fn::Equals": []any{map[string]any{"Ref": "AWS::NoValue"}, "x"}}},
		{"A": map[string]any{"Fn::Or": []any{true, false}}},
		{"A": true},
	}
	for i, d := range bad {
		if result, err := stackConditionValues(d, refs); err == nil || result != nil {
			t.Fatalf("malformed/unknown condition %d accepted", i)
		}
	}
	if enabled, err := stackConditionalEnabled(map[string]any{"Condition": "Enabled"}, values); err != nil || !enabled {
		t.Fatal("existing enabled condition refused")
	}
	if enabled, err := stackConditionalEnabled(map[string]any{"Condition": "Unknown"}, values); err == nil || enabled {
		t.Fatal("unknown resource/output condition admitted")
	}
	if enabled, err := stackConditionalEnabled(map[string]any{"Condition": true}, values); err == nil || enabled {
		t.Fatal("Boolean attribute treated as a named condition")
	}
}

func TestStackConditionalPrunesOnlyChosenNoValueAndCopiesFrames(t *testing.T) {
	noValue := map[string]any{"Ref": "AWS::NoValue"}
	statement := map[string]any{"Sid": "ReadScoreItem", "Effect": "Allow", "Action": []any{"ssm:GetParameter", "ssm:GetParameters"}, "Resource": map[string]any{"Fn::Sub": "arn:${AWS::Partition}:ssm:${AWS::Region}:${AWS::AccountId}:parameter/${NamePrefix}/score-item"}}
	input := map[string]any{"Statements": []any{map[string]any{"Fn::If": []any{"Enabled", statement, noValue}}, map[string]any{"Sid": "Always", "Effect": "Allow", "Action": "cloudshell:CreateSession", "Resource": "*"}}, "Optional": map[string]any{"Fn::If": []any{"Enabled", "present", noValue}}, "Untouched": []any{nil, true, "literal"}}
	before := stackConditionTestCopy(t, input)
	disabled, omit, err := stackSelectConditional(input, map[string]bool{"Enabled": false})
	if err != nil || omit {
		t.Fatal("disabled property projection failed")
	}
	d := disabled.(map[string]any)
	if _, ok := d["Optional"]; ok || len(d["Statements"].([]any)) != 1 {
		t.Fatal("NoValue failed to remove property/list entry")
	}
	enabled, omit, err := stackSelectConditional(input, map[string]bool{"Enabled": true})
	if err != nil || omit {
		t.Fatal("enabled property projection failed")
	}
	e := enabled.(map[string]any)
	if e["Optional"] != "present" || len(e["Statements"].([]any)) != 2 {
		t.Fatal("selected optional statement not preserved")
	}
	e["Statements"].([]any)[0].(map[string]any)["Sid"] = "changed"
	if !reflect.DeepEqual(input, before) {
		t.Fatal("projection mutated source policy or reused nested carriers")
	}
	if out, omit, err := stackSelectConditional(map[string]any{"Fn::If": []any{"Enabled", "x", noValue}}, map[string]bool{"Enabled": false}); err != nil || !omit || out != nil {
		t.Fatal("root NoValue not explicit")
	}
	// Inactive resource references are preserved syntactically, not resolved or used
	// to invent physical values. The real compiler remains the symbol linker.
	if out, omit, err := stackSelectConditional(map[string]any{"Fn::If": []any{"Enabled", map[string]any{"Ref": "ConditionalResource"}, "safe"}}, map[string]bool{"Enabled": false}); err != nil || omit || out != "safe" {
		t.Fatal("inactive resource was wrongly resolved")
	}
}

func TestStackConditionalInactiveShapesAndBudgetsFailClosed(t *testing.T) {
	bad := []any{
		map[string]any{"Fn::If": []any{"Missing", "x", "y"}},
		map[string]any{"Fn::If": []any{"Enabled", "x"}},
		map[string]any{"Ref": "AWS::NoValue"},
		map[string]any{"Fn::If": []any{"Enabled", "safe", map[string]any{"Fn::ImportValue": "foreign"}}},
		map[string]any{"Fn::If": []any{"Enabled", "safe", map[string]any{"Ref": false}}},
		map[string]any{"Fn::If": []any{"Enabled", "safe", map[string]any{"Fn::GetAtt": ".Arn"}}},
		map[string]any{"Fn::If": []any{"Enabled", "safe", map[string]any{"Ref": "X", "Other": "ignored"}}},
		map[string]any{"Fn::Sub": []any{"value", map[string]any{}}},
		float64(1), json.Number("NaN"),
		map[string]any{"Ref": strings.Repeat("R", 51201)},
		map[string]any{"Fn::Sub": strings.Repeat("S", 51201)},
		map[string]any{"Fn::GetAtt": strings.Repeat("R", 51200) + ".Arn"},
		map[string]any{"Fn::GetAtt": []any{strings.Repeat("R", 30000), strings.Repeat("A", 30000)}},
		map[string]any{"Fn::If": []any{"Enabled", "safe", map[string]any{"Fn::Sub": strings.Repeat("S", 51201)}}},
	}
	for i, v := range bad {
		if result, omit, err := stackSelectConditional(v, map[string]bool{"Enabled": true}); err == nil || result != nil || omit {
			t.Fatalf("invalid inactive/value shape %d admitted", i)
		}
	}
	// A maximum-size literal Sub is preserved exactly; one extra byte above
	// was refused even in an inactive arm. No clipping or invented value.
	limitSub := map[string]any{"Fn::Sub": strings.Repeat("S", 51200)}
	selected, omit, err := stackSelectConditional(limitSub, nil)
	if err != nil || omit || !reflect.DeepEqual(selected, limitSub) {
		t.Fatal("bounded intrinsic payload changed")
	}
	var deep any = "leaf"
	for range 33 {
		deep = []any{deep}
	}
	if _, _, err := stackSelectConditional(deep, nil); err == nil {
		t.Fatal("depth budget not enforced")
	}
	wide := make([]any, 10000)
	if _, _, err := stackSelectConditional(wide, nil); err == nil {
		t.Fatal("node budget not enforced")
	}
	defs := map[string]any{}
	for i := 0; i < 101; i++ {
		defs["P"+strings.Repeat("x", i)] = map[string]any{"Type": "String", "Default": "safe"}
	}
	if _, err := stackParameterValues(defs, nil); err == nil {
		t.Fatal("parameter count bound not enforced")
	}
	// A cyclic caller-owned carrier must fail by the depth bound, not panic/hang.
	cyclic := map[string]any{}
	cyclic["again"] = cyclic
	if _, _, err := stackSelectConditional(cyclic, nil); err == nil {
		t.Fatal("cyclic carrier not refused")
	}
}
