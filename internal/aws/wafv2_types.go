package aws

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// WAFv2 supports regional API Gateway stages, not managed groups or CloudFront.
// SearchString is the native API's base64 binary field; CFN must encode text.
type WAFEmpty struct{}
type WAFAction struct {
	Allow *WAFEmpty `json:"Allow,omitempty"`
	Block *WAFEmpty `json:"Block,omitempty"`
	Count *WAFEmpty `json:"Count,omitempty"`
}
type WAFVisibility struct {
	SampledRequestsEnabled   bool   `json:"SampledRequestsEnabled"`
	CloudWatchMetricsEnabled bool   `json:"CloudWatchMetricsEnabled"`
	MetricName               string `json:"MetricName"`
}
type WAFTransformation struct {
	Priority int    `json:"Priority"`
	Type     string `json:"Type"`
}
type WAFBody struct {
	OversizeHandling string `json:"OversizeHandling"`
}
type WAFField struct {
	UriPath *WAFEmpty `json:"UriPath,omitempty"`
	Body    *WAFBody  `json:"Body,omitempty"`
}
type WAFByteMatch struct {
	SearchString         string              `json:"SearchString"`
	FieldToMatch         WAFField            `json:"FieldToMatch"`
	PositionalConstraint string              `json:"PositionalConstraint"`
	TextTransformations  []WAFTransformation `json:"TextTransformations"`
}
type WAFSQLiMatch struct {
	FieldToMatch        WAFField            `json:"FieldToMatch"`
	TextTransformations []WAFTransformation `json:"TextTransformations"`
	SensitivityLevel    string              `json:"SensitivityLevel,omitempty"`
}
type WAFAnd struct {
	Statements []WAFStatement `json:"Statements"`
}
type WAFStatement struct {
	AndStatement       *WAFAnd       `json:"AndStatement,omitempty"`
	ByteMatchStatement *WAFByteMatch `json:"ByteMatchStatement,omitempty"`
	SqliMatchStatement *WAFSQLiMatch `json:"SqliMatchStatement,omitempty"`
}
type WAFRule struct {
	Name             string        `json:"Name"`
	Priority         int           `json:"Priority"`
	Statement        WAFStatement  `json:"Statement"`
	Action           WAFAction     `json:"Action"`
	VisibilityConfig WAFVisibility `json:"VisibilityConfig"`
}
type WAFTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}
type WAFWebACL struct {
	Tags             []WAFTag      `json:"-"`
	Name             string        `json:"Name"`
	ID               string        `json:"Id"`
	ARN              string        `json:"ARN"`
	Description      string        `json:"Description,omitempty"`
	DefaultAction    WAFAction     `json:"DefaultAction"`
	Rules            []WAFRule     `json:"Rules"`
	VisibilityConfig WAFVisibility `json:"VisibilityConfig"`
	Capacity         int           `json:"Capacity"`
}
type WAFSummary struct {
	Name        string `json:"Name"`
	ID          string `json:"Id"`
	ARN         string `json:"ARN"`
	Description string `json:"Description,omitempty"`
	LockToken   string `json:"LockToken"`
}
type WAFDecision struct {
	Action            string
	CountedRules      []string
	TerminatingRuleID string
	WebACLARN         string
}

var wafName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var wafUUID = regexp.MustCompile(`^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$`)

func wafInvalid(message string) error { return ssmFailure{"WAFInvalidParameterException", message} }
func wafAction(action WAFAction, defaultAction bool) (string, error) {
	count := 0
	result := ""
	if action.Allow != nil {
		count++
		result = "ALLOW"
	}
	if action.Block != nil {
		count++
		result = "BLOCK"
	}
	if action.Count != nil {
		count++
		result = "COUNT"
	}
	if count != 1 || defaultAction && result == "COUNT" {
		return "", wafInvalid("Specify exactly one supported action (Allow/Block; rules also support Count).")
	}
	return result, nil
}
func wafVisibility(v WAFVisibility) error {
	if !wafName.MatchString(v.MetricName) || v.MetricName == "All" || v.MetricName == "Default_Action" {
		return wafInvalid("Invalid metric name.")
	}
	return nil
}
func wafTransforms(ts []WAFTransformation) error {
	if len(ts) < 1 || len(ts) > 10 {
		return wafInvalid("Specify 1–10 text transformations.")
	}
	seen := map[int]bool{}
	for _, t := range ts {
		if t.Priority < 0 || seen[t.Priority] || t.Type != "NONE" && t.Type != "URL_DECODE" {
			return wafInvalid("Unsupported or duplicate text transformation.")
		}
		seen[t.Priority] = true
	}
	return nil
}
func wafField(v WAFField) error {
	if (v.Body == nil) == (v.UriPath == nil) {
		return wafInvalid("Specify exactly one of Body or UriPath.")
	}
	if v.Body != nil && !slices.Contains([]string{"CONTINUE", "MATCH", "NO_MATCH"}, v.Body.OversizeHandling) {
		return wafInvalid("Body requires CONTINUE, MATCH or NO_MATCH oversize handling.")
	}
	return nil
}
func wafStatement(s WAFStatement, depth int) (int, error) {
	if depth > 4 {
		return 0, wafInvalid("Statement nesting exceeds simulator limit.")
	}
	n := 0
	if s.AndStatement != nil {
		n++
	}
	if s.ByteMatchStatement != nil {
		n++
	}
	if s.SqliMatchStatement != nil {
		n++
	}
	if n != 1 {
		return 0, wafInvalid("Specify exactly one supported statement.")
	}
	if s.AndStatement != nil {
		if len(s.AndStatement.Statements) < 2 || len(s.AndStatement.Statements) > 10 {
			return 0, wafInvalid("AND requires 2–10 statements.")
		}
		cost := 0
		for _, child := range s.AndStatement.Statements {
			c, e := wafStatement(child, depth+1)
			if e != nil {
				return 0, e
			}
			cost += c
		}
		return cost, nil
	}
	var field WAFField
	var ts []WAFTransformation
	cost := 20
	if b := s.ByteMatchStatement; b != nil {
		data, e := base64.StdEncoding.Strict().DecodeString(b.SearchString)
		if e != nil || len(data) < 1 || len(data) > 200 {
			return 0, wafInvalid("SearchString must be base64 encoding of 1–200 bytes.")
		}
		if !slices.Contains([]string{"EXACTLY", "STARTS_WITH", "ENDS_WITH", "CONTAINS", "CONTAINS_WORD"}, b.PositionalConstraint) {
			return 0, wafInvalid("Unsupported positional constraint.")
		}
		if b.PositionalConstraint == "CONTAINS_WORD" {
			for _, c := range data {
				if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
					return 0, wafInvalid("CONTAINS_WORD search must use ASCII word characters.")
				}
			}
		}
		field = b.FieldToMatch
		ts = b.TextTransformations
		cost = 2
		if b.PositionalConstraint == "CONTAINS" || b.PositionalConstraint == "CONTAINS_WORD" {
			cost = 10
		}
	} else {
		b := s.SqliMatchStatement
		if b.SensitivityLevel != "" && b.SensitivityLevel != "LOW" {
			return 0, wafInvalid("Only LOW SQLi sensitivity is supported; AWS detection equivalence is not claimed.")
		}
		field = b.FieldToMatch
		ts = b.TextTransformations
	}
	if e := wafField(field); e != nil {
		return 0, e
	}
	if e := wafTransforms(ts); e != nil {
		return 0, e
	}
	for _, t := range ts {
		if t.Type != "NONE" {
			cost += 10
		}
	}
	return cost, nil
}
func wafValidate(region string, acl *WAFWebACL) error {
	if !slices.Contains(Regions, region) || !wafName.MatchString(acl.Name) || !utf8.ValidString(acl.Description) || len(acl.Description) > 256 {
		return wafInvalid("Invalid region, name or description.")
	}
	if _, e := wafAction(acl.DefaultAction, true); e != nil {
		return e
	}
	if e := wafVisibility(acl.VisibilityConfig); e != nil {
		return e
	}
	if len(acl.Tags) > 50 {
		return wafInvalid("At most 50 tags are supported.")
	}
	tagKeys := map[string]bool{}
	for _, t := range acl.Tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 || !utf8.ValidString(t.Key+t.Value) || strings.HasPrefix(strings.ToLower(t.Key), "aws:") || tagKeys[t.Key] {
			return wafInvalid("Invalid or duplicate tag.")
		}
		tagKeys[t.Key] = true
	}
	if len(acl.Rules) > 100 {
		return ssmFailure{"WAFLimitsExceededException", "At most 100 rules are supported."}
	}
	priorities := map[int]bool{}
	names := map[string]bool{}
	acl.Capacity = 0
	for _, r := range acl.Rules {
		if !wafName.MatchString(r.Name) || names[r.Name] || r.Priority < 0 || priorities[r.Priority] {
			return wafInvalid("Rule names and nonnegative priorities must be unique.")
		}
		names[r.Name] = true
		priorities[r.Priority] = true
		if _, e := wafAction(r.Action, false); e != nil {
			return e
		}
		if e := wafVisibility(r.VisibilityConfig); e != nil {
			return e
		}
		cost, e := wafStatement(r.Statement, 0)
		if e != nil {
			return e
		}
		acl.Capacity += cost
	}
	if acl.Capacity > 1500 {
		return ssmFailure{"WAFLimitsExceededException", "Rule capacity exceeds simulator limit."}
	}
	return nil
}
func wafARN(region, name, id string) string {
	return fmt.Sprintf("arn:aws:wafv2:%s:000000000000:regional/webacl/%s/%s", region, name, id)
}
