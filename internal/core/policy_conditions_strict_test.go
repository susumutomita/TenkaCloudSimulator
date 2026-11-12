package core

import (
	"strings"
	"testing"
)

// The condition decoder is reachable without the policy-level duplicate scan
// (typed decoding of stored documents), so it must refuse every malformed or
// ambiguous shape by itself.
func TestPolicyConditionDecoderRefusesMalformedShapes(t *testing.T) {
	for _, raw := range []string{
		`[]`,
		`{"StringEquals":{"aws:userid":"a"},"StringEquals":{"aws:userid":"b"}}`,
		`{"StringEquals":"aws:userid"}`,
		`{"StringEquals":{"aws:userid":"a","AWS:UserId":"b"}}`,
		`{"StringEquals":{"aws:userid":"a"}`,
		`{"StringEquals":{"aws:userid":"a"}} {}`,
		`{"StringEquals":{"aws:userid":"a"}}]`,
		`{"StringEquals":{"aws:userid":`,
		`{"StringEquals":{"aws:userid":"a"`,
		`{"StringEquals":{"aws:userid":"a","StringLike":`,
		`{"StringEquals"`,
		`{"StringEquals":{"aws:userid":"` + strings.Repeat("x", 1025) + `"}}`,
		`{"StringEquals":{"aws:userid":"a\u0000"}}`,
		`{"StringEquals":{"aws:userid":"a"},"StringLike":{"aws:userid":"b"},"StringNotEquals":{"aws:userid":"c"}}`,
	} {
		var c PolicyConditions
		if err := c.UnmarshalJSON([]byte(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	var c PolicyConditions
	if err := c.UnmarshalJSON([]byte(`{"StringEquals":{"aws:userid":"a"},"StringLike":{"aws:ResourceTag/team":["blue","red*"]}}`)); err != nil || len(c) != 2 {
		t.Fatalf("two-operator condition refused: %v %v", c, err)
	}
}

func TestPolicyConditionsMatchOnlyWhenValidAndUnambiguous(t *testing.T) {
	ctx := PolicyContext{"aws:userid": "alice"}
	if !(PolicyConditions{"StringLike": {"aws:userid": {"${aws:userid}"}}}).matches(ctx) || (PolicyConditions{"StringLike": {"aws:userid": {"x-*"}}}).matches(ctx) {
		t.Fatal("StringLike variable substitution mismatched")
	}
	if !(PolicyConditions{"StringEquals": {"aws:userid": {"alice"}}}).matches(ctx) {
		t.Fatal("exact condition did not match")
	}
	for name, c := range map[string]PolicyConditions{
		"empty":            {},
		"too many":         {"StringEquals": {"aws:userid": {"a"}}, "StringLike": {"aws:userid": {"a"}}, "Other": {"aws:userid": {"a"}}},
		"oversized value":  {"StringEquals": {"aws:userid": {strings.Repeat("a", 1025)}}},
		"nul value":        {"StringEquals": {"aws:userid": {"ali\x00ce"}}},
		"unknown variable": {"StringEquals": {"aws:userid": {"${aws:username}"}}},
	} {
		if c.matches(ctx) {
			t.Errorf("%s condition matched", name)
		}
	}
	match := PolicyConditions{"StringEquals": {"aws:userid": {"alice"}}}
	for name, values := range map[string]PolicyContext{
		"case alias":     {"aws:userid": "alice", "AWS:UserId": "alice"},
		"oversized":      {"aws:userid": strings.Repeat("a", 4097)},
		"nul":            {"aws:userid": "alice\x00"},
		"missing":        {},
		"different user": {"aws:userid": "bob"},
	} {
		if match.matches(values) {
			t.Errorf("%s context matched", name)
		}
	}
	if policyValueMatch("${aws:userid", "alice", ctx, false) || policyValueMatch("${aws:userid}", "", PolicyContext{}, false) {
		t.Fatal("unterminated variable or missing identity matched")
	}
	if !policyValueMatch("a?c*", "abcde", ctx, true) || policyValueMatch("a?c*", "abcde", ctx, false) {
		t.Fatal("glob semantics differ between StringLike and StringEquals")
	}
}

func TestPolicyDuplicateScanBoundsDepthAndTrailingData(t *testing.T) {
	deep := strings.Repeat("[", 40) + strings.Repeat("]", 40)
	for name, raw := range map[string]string{
		"deep":           `{"Version":"2012-10-17","Statement":` + deep + `}`,
		"trailing":       `{"Version":"2012-10-17","Statement":[]} {}`,
		"duplicate case": `{"Version":"2012-10-17","version":"2012-10-17","Statement":[]}`,
		"truncated":      `{"Version":"2012-10-17","Statement":[{"Effect"`,
		"bad array":      `{"Version":"2012-10-17","Statement":[1,}`,
	} {
		if err := rejectPolicyDuplicateKeys([]byte(raw)); err == nil {
			t.Errorf("%s policy accepted", name)
		}
	}
	if err := rejectPolicyDuplicateKeys([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["a","b"],"Resource":"*"}]}`)); err != nil {
		t.Fatal(err)
	}
}
