package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// PolicyContext is an immutable request snapshot assembled by the owning provider
// from authenticated identity and persisted resource/session metadata. It is not
// a credential and must not be populated directly from request headers or JSON.
type PolicyContext map[string]string

type PolicyConditions map[string]map[string]StringList

// Strict parsing matters here: duplicate condition keys must never erase a
// restrictive clause. Only a bounded positive string subset is implemented.
func (c *PolicyConditions) UnmarshalJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return fmt.Errorf("Condition must be an object")
	}
	out := PolicyConditions{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return e
		}
		op, ok := k.(string)
		if !ok {
			return fmt.Errorf("invalid condition operator")
		}
		if _, exists := out[op]; exists {
			return fmt.Errorf("duplicate condition operator")
		}
		tok, e = d.Token()
		if e != nil || tok != json.Delim('{') {
			return fmt.Errorf("condition operator requires an object")
		}
		terms := map[string]StringList{}
		seen := map[string]bool{}
		for d.More() {
			k, e = d.Token()
			if e != nil {
				return e
			}
			key, ok := k.(string)
			if !ok || seen[strings.ToLower(key)] {
				return fmt.Errorf("duplicate condition key")
			}
			seen[strings.ToLower(key)] = true
			var value json.RawMessage
			if e = d.Decode(&value); e != nil {
				return e
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("null condition value")
			}
			var list StringList
			if e = json.Unmarshal(value, &list); e != nil {
				return e
			}
			terms[key] = list
		}
		if _, e = d.Token(); e != nil {
			return e
		}
		out[op] = terms
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return fmt.Errorf("trailing condition data")
	}
	if len(out) == 0 {
		return fmt.Errorf("empty Condition is unsupported")
	}
	if err = out.validate(); err != nil {
		return err
	}
	*c = out
	return nil
}
func conditionKeySupported(k string) bool {
	k = strings.ToLower(k)
	if k == "aws:userid" {
		return true
	}
	for _, prefix := range []string{"aws:resourcetag/", "ssm:resourcetag/"} {
		if strings.HasPrefix(k, prefix) && len(k) > len(prefix) && len(k) <= 256 {
			return true
		}
	}
	return false
}
func (c PolicyConditions) validate() error {
	if c == nil {
		return nil
	}
	if len(c) == 0 || len(c) > 2 {
		return fmt.Errorf("unsupported condition count")
	}
	for op, terms := range c {
		if op != "StringEquals" && op != "StringLike" {
			return fmt.Errorf("unsupported condition operator %s", op)
		}
		if len(terms) == 0 || len(terms) > 16 {
			return fmt.Errorf("invalid condition key count")
		}
		seen := map[string]bool{}
		for key, list := range terms {
			lower := strings.ToLower(key)
			if seen[lower] || !conditionKeySupported(key) {
				return fmt.Errorf("unsupported or repeated condition key")
			}
			seen[lower] = true
			if len(list) == 0 || len(list) > 16 {
				return fmt.Errorf("invalid condition values")
			}
			for _, v := range list {
				if len(v) > 1024 || strings.ContainsRune(v, 0) {
					return fmt.Errorf("invalid condition value")
				}
				if strings.Contains(strings.ReplaceAll(strings.ToLower(v), "${aws:userid}", ""), "${") {
					return fmt.Errorf("unsupported policy variable")
				}
			}
		}
	}
	return nil
}
func contextValue(values PolicyContext, key string) (string, bool) {
	var result string
	found := false
	for k, v := range values {
		if strings.EqualFold(k, key) {
			if found {
				return "", false
			}
			result = v
			found = true
		}
	}
	if len(result) > 4096 || strings.ContainsRune(result, 0) {
		return "", false
	}
	return result, found
}
func (c PolicyConditions) matches(values PolicyContext) bool {
	if c.validate() != nil {
		return false
	}
	for op, terms := range c {
		for key, wants := range terms {
			actual, ok := contextValue(values, key)
			if !ok {
				return false
			}
			matched := false
			for _, want := range wants {
				if policyValueMatch(want, actual, values, op == "StringLike") {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

// Variables are literal data, not wildcard syntax. A missing identity cannot
// turn a conditional grant into an unconditional one. Unsupported variables deny.
func policyValueMatch(pattern, value string, values PolicyContext, glob bool) bool {
	var b strings.Builder
	b.WriteString("(?s)^")
	for {
		at := strings.Index(pattern, "${")
		literal := pattern
		if at >= 0 {
			literal = pattern[:at]
		}
		quoted := regexp.QuoteMeta(literal)
		if glob {
			quoted = strings.ReplaceAll(quoted, `\*`, ".*")
			quoted = strings.ReplaceAll(quoted, `\?`, ".")
		}
		b.WriteString(quoted)
		if at < 0 {
			break
		}
		pattern = pattern[at:]
		end := strings.IndexByte(pattern, '}')
		if end < 0 || !strings.EqualFold(pattern[:end+1], "${aws:userid}") {
			return false
		}
		id, ok := contextValue(values, "aws:userid")
		if !ok || id == "" {
			return false
		}
		b.WriteString(regexp.QuoteMeta(id))
		pattern = pattern[end+1:]
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(value)
}

func supportedPolicyVariables(pattern string) bool {
	for {
		at := strings.Index(pattern, "${")
		if at < 0 {
			return true
		}
		pattern = pattern[at:]
		end := strings.IndexByte(pattern, '}')
		if end < 0 || !strings.EqualFold(pattern[:end+1], "${aws:userid}") {
			return false
		}
		pattern = pattern[end+1:]
	}
}

// encoding/json otherwise accepts repeated keys and case aliases, letting a
// later Condition replace an earlier restrictive clause before typed decoding.
func rejectPolicyDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("policy nesting exceeds limit")
		}
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[strings.ToLower(key)] {
					return fmt.Errorf("duplicate policy field")
				}
				seen[strings.ToLower(key)] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("invalid policy JSON")
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing policy data")
	}
	return nil
}
