package arm

import (
	"regexp"
	"strings"
)

// Providers returns every reviewed bounded resource provider specification.
func Providers() []*Provider {
	return []*Provider{resources, keyVault, network, managedIdentity, operationalInsights, appConfiguration, containerRegistry, eventHub, serviceBus, documentDB, web}
}

// catalogIssue is the official product catalog plan index (#316).
var catalogIssue = []int{316}

// standard binds the six usual operations of a resource-group-scoped type.
func standard(typ, put, get, patch, del, listGroup, listAll string, query map[string][]string) []Binding {
	out := []Binding{{Operation: put, Kind: Put, Type: typ}, {Operation: get, Kind: Get, Type: typ}, {Operation: del, Kind: Delete, Type: typ},
		{Operation: listGroup, Kind: List, Type: typ}, {Operation: listAll, Kind: List, Type: typ}}
	if patch != "" {
		out = append(out, Binding{Operation: patch, Kind: Patch, Type: typ})
	}
	for i := range out {
		out[i].Query = query[out[i].Operation]
	}
	return out
}

// name builds a documented Azure naming rule with explicit length bounds.
func name(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }

func refuse(format string, args ...any) error {
	return fail(400, "InvalidRequestContent", format, args...)
}

func text(document map[string]any, dotted string) string {
	value, _ := lookup(document, dotted)
	s, _ := value.(string)
	return s
}

func strings_(document map[string]any, dotted string) []string {
	value, _ := lookup(document, dotted)
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func number(document map[string]any, dotted string) (int64, bool) {
	value, ok := lookup(document, dotted)
	n, isInt := value.(int64)
	return n, ok && isInt
}

func equalsAny(value string, allowed ...string) bool {
	for _, a := range allowed {
		if strings.EqualFold(a, value) {
			return true
		}
	}
	return false
}

// sameTenant requires a tenant member to name the configured tenant.
func sameTenant(c *Change, dotted string) error {
	if value := text(c.Document, dotted); value != "" && !strings.EqualFold(value, c.Tenant) {
		return refuse("Member '%s' must be the simulator tenant %s.", dotted, c.Tenant)
	}
	return nil
}

func rangeCheck(c *Change, dotted string, low, high int64) error {
	if n, ok := number(c.Document, dotted); ok && (n < low || n > high) {
		return refuse("Member '%s' must be between %d and %d.", dotted, low, high)
	}
	return nil
}
