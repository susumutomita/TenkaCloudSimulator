package simpledb

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This is a small, anchored SimpleDB grammar, not SQL. Only parsed fields are
// accepted by the store. Names and values are never interpreted as SQLite SQL.
const (
	selIdent = "(?:[A-Za-z_$][A-Za-z0-9_$]*|\x60(?:[^\x60]|\x60\x60)+\x60)"
	selItem  = `(?i:itemName)\(\)`
	selField = "(?:" + selItem + "|" + selIdent + ")"
	selValue = `(?:'(?:[^']|'')*'|"(?:[^"]|"")*")`
)

// Capture order: projection, domain, predicate field/operator/value, sort field/direction, limit.
var selectGrammar = regexp.MustCompile(`^\s*(?i:select)\s+(\*|` + selItem + `)\s+(?i:from)\s+(` + selIdent + `)` +
	`(?:\s+(?i:where)\s+(` + selField + `)\s*(<=|>=|=|<|>)\s*(` + selValue + `))?` +
	`(?:\s+(?i:order)\s+(?i:by)\s+(` + selField + `)(?:\s+((?i:asc|desc)))?)?` +
	`(?:\s+(?i:limit)\s+([0-9]+))?\s*$`)

type selectPlan struct {
	domain, attr, nameOp, value string
	namesOnly, desc             bool
	limit                       int
}

func unquote(s string) string {
	d := s[:1]
	return strings.ReplaceAll(s[1:len(s)-1], d+d, d)
}
func selectIdentifier(s string) (string, bool) {
	if s[0] == 0x60 {
		n := unquote(s)
		return n, validText(n, false)
	}
	switch strings.ToLower(s) {
	case "or", "and", "not", "from", "where", "select", "like", "null", "is", "order", "by", "asc", "desc", "in", "between", "intersection", "limit", "every":
		return "", false
	}
	return s, validText(s, false)
}
func parseSelect(s string) (p selectPlan, code string) {
	if len(s) > 8192 || !utf8.ValidString(s) {
		return p, "InvalidQueryExpression"
	}
	m := selectGrammar.FindStringSubmatch(s)
	if m == nil {
		return p, "InvalidQueryExpression"
	}
	p.namesOnly = m[1] != "*"
	var ok bool
	p.domain, ok = selectIdentifier(m[2])
	if !ok || !domainName.MatchString(p.domain) {
		return p, "InvalidQueryExpression"
	}
	if m[3] != "" {
		p.value = unquote(m[5])
		if !validText(p.value, true) {
			return p, "InvalidQueryExpression"
		}
		if strings.EqualFold(m[3], "itemName()") {
			p.nameOp = m[4]
		} else {
			p.attr, ok = selectIdentifier(m[3])
			if !ok || m[4] != "=" {
				return p, "InvalidQueryExpression"
			}
		}
	}
	if m[6] != "" {
		if !strings.EqualFold(m[6], "itemName()") || p.nameOp == "" {
			return p, "InvalidSortExpression"
		}
		p.desc = strings.EqualFold(m[7], "desc")
	}
	p.limit = 100
	if m[8] != "" {
		n, e := strconv.Atoi(m[8])
		if e != nil || n < 1 || n > 2500 {
			return p, "InvalidParameterValue"
		}
		p.limit = n
	}
	return p, ""
}
