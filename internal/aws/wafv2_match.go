package aws

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/url"
	"slices"
	"strings"

	libinjection "github.com/corazawaf/libinjection-go"
)

// SQLi uses a general token/fingerprint detector, not AWS's proprietary engine.
// Only the inspected prefix is transformed; original request bytes stay unchanged.
const wafBodyLimit = 16384

func wafComponent(field WAFField, path string, body []byte) ([]byte, *bool) {
	if field.UriPath != nil {
		return []byte(path), nil
	}
	if len(body) > wafBodyLimit {
		switch field.Body.OversizeHandling {
		case "MATCH":
			v := true
			return nil, &v
		case "NO_MATCH":
			v := false
			return nil, &v
		}
		body = body[:wafBodyLimit]
	}
	return body, nil
}
func wafTransform(data []byte, ts []WAFTransformation) []byte {
	ts = slices.Clone(ts)
	slices.SortFunc(ts, func(a, b WAFTransformation) int { return a.Priority - b.Priority })
	out := string(data)
	for _, t := range ts {
		if t.Type == "URL_DECODE" { // Preserve malformed escapes, as WAF does; decode valid escapes once.
			var b strings.Builder
			for i := 0; i < len(out); i++ {
				if out[i] == '+' {
					b.WriteByte(' ')
				} else if out[i] == '%' && i+2 < len(out) {
					if s, e := url.PathUnescape(out[i : i+3]); e == nil {
						b.WriteString(s)
						i += 2
					} else {
						b.WriteByte(out[i])
					}
				} else {
					b.WriteByte(out[i])
				}
			}
			out = b.String()
		}
	}
	return []byte(out)
}
func wafWord(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}
func wafMatch(s WAFStatement, path string, body []byte) bool {
	if a := s.AndStatement; a != nil {
		for _, c := range a.Statements {
			if !wafMatch(c, path, body) {
				return false
			}
		}
		return true
	}
	var field WAFField
	var ts []WAFTransformation
	if s.ByteMatchStatement != nil {
		field = s.ByteMatchStatement.FieldToMatch
		ts = s.ByteMatchStatement.TextTransformations
	} else {
		field = s.SqliMatchStatement.FieldToMatch
		ts = s.SqliMatchStatement.TextTransformations
	}
	data, oversize := wafComponent(field, path, body)
	if oversize != nil {
		return *oversize
	}
	data = wafTransform(data, ts)
	if b := s.ByteMatchStatement; b != nil {
		search, _ := base64.StdEncoding.DecodeString(b.SearchString)
		switch b.PositionalConstraint {
		case "EXACTLY":
			return bytes.Equal(data, search)
		case "STARTS_WITH":
			return bytes.HasPrefix(data, search)
		case "ENDS_WITH":
			return bytes.HasSuffix(data, search)
		case "CONTAINS":
			return bytes.Contains(data, search)
		case "CONTAINS_WORD":
			for offset := 0; offset <= len(data)-len(search); {
				i := bytes.Index(data[offset:], search)
				if i < 0 {
					return false
				}
				i += offset
				end := i + len(search)
				if (i == 0 || !wafWord(data[i-1])) && (end == len(data) || !wafWord(data[end])) {
					return true
				}
				offset = i + 1
			}
		}
		return false
	}
	matched, _ := libinjection.IsSQLi(string(data))
	return matched
}
func (s *WAFStore) Evaluate(ctx context.Context, region, resourceARN, uriPath string, body []byte) (WAFDecision, error) {
	result := WAFDecision{Action: "ALLOW", CountedRules: []string{}}
	acl, e := s.ForResource(ctx, region, resourceARN)
	if e != nil {
		return result, e
	}
	if acl == nil {
		return result, nil
	}
	result.WebACLARN = acl.ARN
	rules := slices.Clone(acl.Rules)
	slices.SortFunc(rules, func(a, b WAFRule) int {
		if a.Priority < b.Priority {
			return -1
		}
		if a.Priority > b.Priority {
			return 1
		}
		return 0
	})
	for _, r := range rules {
		if !wafMatch(r.Statement, uriPath, body) {
			continue
		}
		action, _ := wafAction(r.Action, false)
		if action == "COUNT" {
			result.CountedRules = append(result.CountedRules, r.Name)
			continue
		}
		result.Action = action
		result.TerminatingRuleID = r.Name
		return result, nil
	}
	result.Action, _ = wafAction(acl.DefaultAction, true)
	result.TerminatingRuleID = "Default_Action"
	return result, nil
}
