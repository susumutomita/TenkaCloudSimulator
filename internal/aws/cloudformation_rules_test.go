package aws

import (
	"net/url"
	"strings"
	"testing"
)

const stackRulesExample = `Parameters:
  Copies: {Type: Number, Default: 1}
  First: {Type: String}
  Second: {Type: String, Default: ''}
Rules:
  SeparateCopies:
    RuleCondition: !Equals [!Ref Copies, '2']
    Assertions:
      - Assert: !And [!Not [!Equals [!Ref Second, '']], !Not [!Equals [!Ref First, !Ref Second]]]
        AssertDescription: Place the two copies in different locations.
Conditions:
  Two: !Equals [!Ref Copies, '2']
  IncludeSecond: !And [!Condition Two, !Not [!Equals [!Ref Second, '']]]
Resources:
  FirstCopy: {Type: 'AWS::SSM::Parameter', Properties: {Name: /rules/first, Type: String, Value: !Ref First}}
  OtherCopy: {Type: 'AWS::SSM::Parameter', Condition: IncludeSecond, Properties: {Name: /rules/second, Type: String, Value: !Sub 'copy-${Second}'}}
`

func TestStackRulesNativeAdmission(t *testing.T) {
	g := ssmValueGateway(t)
	if w := validationRequest(g, stackRulesExample, "", nil); w.Code != 200 {
		t.Fatalf("validate %d: %s", w.Code, w.Body.String())
	}
	params := url.Values{"Parameters.member.1.ParameterKey": {"First"}, "Parameters.member.1.ParameterValue": {"local-primary"}, "Parameters.member.2.ParameterKey": {"Copies"}, "Parameters.member.2.ParameterValue": {"2"}}
	before := stackConditionalDBFrame(t, g.Store.Database())
	for _, second := range []string{"", "local-primary"} {
		params.Set("Parameters.member.3.ParameterKey", "Second")
		params.Set("Parameters.member.3.ParameterValue", second)
		w := conditionBoundarySend(g, "CreateStack", stackRulesExample, "", params)
		conditionBoundaryError(t, w, 400, "ValidationError", "rule SeparateCopies assertion 1 failed")
		if strings.Contains(w.Body.String(), "local-primary") || stackConditionalDBFrame(t, g.Store.Database()) != before {
			t.Fatal("invalid rule changed state or disclosed input")
		}
	}
	params.Set("Parameters.member.3.ParameterValue", "local-secondary")
	if w := conditionBoundarySend(g, "CreateStack", stackRulesExample, "", params); w.Code != 200 {
		t.Fatalf("create %d: %s", w.Code, w.Body.String())
	}
	rows, err := g.Stacks.Records("ap-northeast-1", "boundary")
	if err != nil || len(rows) != 1 || len(rows[0].Resources) != 2 {
		t.Fatal(rows, err)
	}
	r, err := compileStack("us-east-1", "single", stackRulesExample, map[string]string{"First": "only"}, "")
	if err != nil || len(r.Resources) != 1 || r.Resources[0].LogicalID != "FirstCopy" {
		t.Fatal(r, err)
	}
}

func TestStackRulesRejectMalformedExpressionsBeforeProvisioning(t *testing.T) {
	const badResource = "!Ref Missing"
	for _, tc := range []struct{ from, to string }{
		{"!Not [!Equals [!Ref Second, '']]", "!Not []"},
		{"!And [!Not [!Equals [!Ref Second, '']], !Not [!Equals [!Ref First, !Ref Second]]]", "!And [!Equals [x,x]]"},
		{"!Ref First, !Ref Second", "!Ref First, " + badResource},
		{"!Ref First, !Ref Second", "!Ref First, !Ref AWS::Region"},
		{"!Not [!Equals [!Ref First, !Ref Second]]", "!Condition Two"},
		{"AssertDescription: Place the two copies in different locations.", "AssertDescription: [wrong]"},
		{"RuleCondition:", "Unknown:"},
		{"      - Assert:", "      - Unsupported:"},
	} {
		body := strings.Replace(stackRulesExample, tc.from, tc.to, 1)
		if body == stackRulesExample {
			t.Fatal("bad fixture", tc)
		}
		_, decErr := validateStackTemplate(body)
		if decErr == nil {
			t.Fatal("accepted malformed rule", tc)
		}
		if _, e := compileStack("us-east-1", "example", body, map[string]string{"First": "a"}, ""); e == nil {
			t.Fatal("inactive condition hid invalid declaration", tc)
		}
	}
}

func TestStackRulesRequireEachAssertionWhenEnabled(t *testing.T) {
	for _, tc := range []struct{ second, expected string }{{"b", "assertion 1 failed"}, {"a", "assertion 2 failed"}} {
		body := strings.Replace(stackRulesExample, "      - Assert:", "      - Assert: !Equals [!Ref Second, a]\n      - Assert:", 1)
		if _, err := validateStackTemplate(body); err != nil {
			t.Fatal(err)
		}
		inputs := map[string]string{"First": "a", "Second": tc.second, "Copies": "2"}
		if _, err := compileStack("us-east-1", "example", body, inputs, ""); err == nil || !strings.Contains(err.Error(), tc.expected) {
			t.Fatalf("expected %s, got %v", tc.expected, err)
		}
	}
}

func TestStackRulesRequireBooleanSyntaxAndEnforceBounds(t *testing.T) {
	eq := map[string]any{"Fn::Equals": []any{"a", "a"}}
	for _, node := range []any{
		map[string]any{"Fn::And": make([]any, 11)}, map[string]any{"Fn::And": []any{eq}}, map[string]any{"Fn::Not": []any{eq, eq}}, map[string]any{"Fn::Or": []any{eq, eq}}, true,
	} {
		r := map[string]any{"Limit": map[string]any{"Assertions": []any{map[string]any{"Assert": node}}}}
		if err := checkStackRules(r, nil, nil, false); err == nil {
			t.Fatal("accepted node", node)
		}
	}
	deep := any(eq)
	for i := 0; i < stackConditionalDepth; i++ {
		deep = map[string]any{"Fn::Not": []any{deep}}
	}
	if _, err := stackConditionValues(map[string]any{"Deep": deep}, nil); err == nil {
		t.Fatal("condition depth bound exceeded")
	}
	defs := map[string]any{"A": eq, "B": map[string]any{"Fn::Not": []any{map[string]any{"Condition": "A"}}}}
	got, err := stackConditionValues(defs, nil)
	if err != nil || !got["A"] || got["B"] {
		t.Fatal(got, err)
	}
	defs["A"] = map[string]any{"Fn::Not": []any{map[string]any{"Condition": "B"}}}
	if _, err = stackConditionValues(defs, nil); err == nil {
		t.Fatal("nested cycle admitted")
	}
}
