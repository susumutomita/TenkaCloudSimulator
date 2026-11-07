package arm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDepth       = 32
	maxNodes       = 4096
	maxStringBytes = 32 << 10
)

// armError is an ARM error envelope with its HTTP status.
type armError struct {
	status  int
	code    string
	message string
}

func (e *armError) Error() string { return e.message }

func fail(status int, code, format string, args ...any) *armError {
	return &armError{status, code, fmt.Sprintf(format, args...)}
}

func invalidContent(format string, args ...any) *armError {
	return fail(400, "InvalidRequestContent", "The request content was invalid and could not be deserialized: "+format, args...)
}

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	durationPattern = regexp.MustCompile(`^-?P(?:\d+(?:\.\d+)?Y)?(?:\d+(?:\.\d+)?M)?(?:\d+(?:\.\d+)?W)?(?:\d+(?:\.\d+)?D)?(?:T(?:\d+(?:\.\d+)?H)?(?:\d+(?:\.\d+)?M)?(?:\d+(?:\.\d+)?S)?)?$`)
)

// decoder validates request JSON (decoded with UseNumber) against the
// official definitions. Read-only members are dropped, unknown members and
// secrets are refused, and write-only members are retained for the caller.
type decoder struct {
	model *Model
	nodes int
}

func joinPath(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func (d *decoder) count(at string, depth int) error {
	d.nodes++
	if d.nodes > maxNodes || depth > maxDepth {
		return invalidContent("the payload at '%s' exceeds the simulator's bounded nesting or size.", at)
	}
	return nil
}

func (d *decoder) decode(property *Schema, raw any, at string, depth int) (any, error) {
	if err := d.count(at, depth); err != nil {
		return nil, err
	}
	s := d.model.resolve(property)
	if s == nil {
		return nil, invalidContent("'%s' has no official definition.", at)
	}
	if property.Secret || s.Secret || s.Format == "password" {
		return nil, invalidContent("'%s' is a secret; the simulator never accepts or stores secret values.", at)
	}
	if d.model.isObject(s) {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, invalidContent("'%s' must be a JSON object.", at)
		}
		return d.object(s, object, at, depth)
	}
	switch s.Type {
	case "array":
		items, ok := raw.([]any)
		if !ok {
			return nil, invalidContent("'%s' must be a JSON array.", at)
		}
		if s.MinItems != nil && len(items) < *s.MinItems || s.MaxItems != nil && len(items) > *s.MaxItems {
			return nil, invalidContent("'%s' has %d items outside the official bounds.", at, len(items))
		}
		out := make([]any, 0, len(items))
		for i, item := range items {
			if item == nil {
				return nil, invalidContent("'%s[%d]' must not be null.", at, i)
			}
			converted, err := d.decode(s.Items, item, fmt.Sprintf("%s[%d]", at, i), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	case "string":
		return stringValue(s, raw, at)
	case "integer":
		return integerValue(s, raw, at)
	case "number":
		return numberValue(s, raw, at)
	case "boolean":
		value, ok := raw.(bool)
		if !ok {
			return nil, invalidContent("'%s' must be true or false.", at)
		}
		return value, nil
	case "":
		return d.document(raw, at, depth)
	}
	return nil, invalidContent("'%s' uses an unsupported definition type.", at)
}

func (d *decoder) object(s *Schema, object map[string]any, at string, depth int) (any, error) {
	v := d.model.merged(s)
	if s.Discriminator != "" && len(s.Subtypes) > 0 {
		if value, ok := object[s.Discriminator].(string); ok {
			sub, known := "", false
			for key, name := range s.Subtypes {
				if strings.EqualFold(key, value) {
					sub, known = name, true
				}
			}
			if !known {
				return nil, invalidContent("'%s' has unknown %s '%s'.", joinPath(at, s.Discriminator), s.Discriminator, value)
			}
			v = d.model.merged(d.model.Definitions[sub])
		}
	}
	if len(v.properties) == 0 && s.additional == nil && (s.openMap || len(s.AllOf) == 0 && len(s.Additional) == 0) {
		// An open map or a property-less object is free-form JSON.
		return d.document(object, at, depth)
	}
	out := map[string]any{}
	for key, value := range object {
		field, known := v.properties[key]
		if !known {
			if s.additional == nil && !s.openMap {
				return nil, invalidContent("could not find member '%s' on object of type '%s'.", joinPath(at, key), definitionName(at))
			}
			if value == nil {
				return nil, invalidContent("'%s' must not be null.", joinPath(at, key))
			}
			if s.openMap {
				converted, err := d.document(value, joinPath(at, key), depth+1)
				if err != nil {
					return nil, err
				}
				out[key] = converted
				continue
			}
			converted, err := d.decode(s.additional, value, joinPath(at, key), depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = converted
			continue
		}
		resolved := d.model.resolve(field)
		if readOnly(field, resolved) {
			continue
		}
		if value == nil {
			if field.Nullable || resolved != nil && resolved.Nullable {
				continue
			}
			return nil, invalidContent("'%s' must not be null.", joinPath(at, key))
		}
		converted, err := d.decode(field, value, joinPath(at, key), depth+1)
		if err != nil {
			return nil, err
		}
		out[key] = converted
	}
	for key := range v.required {
		field := v.properties[key]
		if field == nil || readOnly(field, d.model.resolve(field)) {
			continue
		}
		if _, ok := out[key]; !ok {
			return nil, invalidContent("required member '%s' is missing.", joinPath(at, key))
		}
	}
	return out, nil
}

// definitionName describes a position for error text without a type name.
func definitionName(at string) string {
	if at == "" {
		return "the request body"
	}
	return at
}

// document copies free-form JSON within the node and depth budgets.
func (d *decoder) document(raw any, at string, depth int) (any, error) {
	if err := d.count(at, depth); err != nil {
		return nil, err
	}
	switch value := raw.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			converted, err := d.document(item, joinPath(at, key), depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			converted, err := d.document(item, at, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	case json.Number:
		return numberValue(&Schema{}, value, at)
	case string:
		if !utf8.ValidString(value) || len(value) > maxStringBytes {
			return nil, invalidContent("'%s' must be valid UTF-8 up to 32 KiB.", at)
		}
	}
	return raw, nil
}

func enumValue(enum []any, text string) (string, bool) {
	for _, allowed := range enum {
		if candidate, ok := allowed.(string); ok && strings.EqualFold(candidate, text) {
			return candidate, true
		}
	}
	return "", false
}

func stringValue(s *Schema, raw any, at string) (any, error) {
	text, ok := raw.(string)
	if !ok {
		return nil, invalidContent("'%s' must be a string.", at)
	}
	if !utf8.ValidString(text) || len(text) > maxStringBytes {
		return nil, invalidContent("'%s' must be valid UTF-8 up to 32 KiB.", at)
	}
	if len(s.Enum) > 0 {
		canonical, ok := enumValue(s.Enum, text)
		if !ok {
			return nil, invalidContent("'%s' value '%s' is not one of the official values.", at, text)
		}
		text = canonical
	}
	length := utf8.RuneCountInString(text)
	if s.MinLength != nil && length < *s.MinLength || s.MaxLength != nil && length > *s.MaxLength {
		return nil, invalidContent("'%s' has a length outside the official bounds.", at)
	}
	if s.pattern != nil && !s.pattern.MatchString(text) {
		return nil, invalidContent("'%s' does not match the official pattern %s.", at, s.Pattern)
	}
	if err := formatted(s.Format, text); err != nil {
		return nil, invalidContent("'%s' %s", at, err.Error())
	}
	return text, nil
}

// formatted checks the swagger string formats the selected models use.
func formatted(format, text string) error {
	switch format {
	case "date-time":
		if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
			return fmt.Errorf("must be an RFC 3339 timestamp.")
		}
	case "date":
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return fmt.Errorf("must be a date such as 2026-01-31.")
		}
	case "uuid":
		if !uuidPattern.MatchString(text) {
			return fmt.Errorf("must be a UUID.")
		}
	case "duration":
		if !durationPattern.MatchString(text) || text == "P" || strings.HasSuffix(text, "T") {
			return fmt.Errorf("must be an ISO 8601 duration such as PT5M.")
		}
	case "byte":
		if _, err := base64.StdEncoding.DecodeString(text); err != nil {
			return fmt.Errorf("must be base64 bytes.")
		}
	case "uri", "url":
		if u, err := url.Parse(text); err != nil || !u.IsAbs() {
			return fmt.Errorf("must be an absolute URI.")
		}
	case "arm-id":
		if !strings.HasPrefix(text, "/") {
			return fmt.Errorf("must be an ARM resource ID.")
		}
	}
	return nil
}

func integerValue(s *Schema, raw any, at string) (any, error) {
	number, ok := raw.(json.Number)
	if !ok {
		return nil, invalidContent("'%s' must be an integer.", at)
	}
	bits := 64
	if s.Format == "int32" {
		bits = 32
	}
	n, err := strconv.ParseInt(string(number), 10, bits)
	if err != nil {
		return nil, invalidContent("'%s' must be a %d-bit integer.", at, bits)
	}
	if outside(s, float64(n)) {
		return nil, invalidContent("'%s' value %d is outside the official range.", at, n)
	}
	return n, nil
}

func numberValue(s *Schema, raw any, at string) (any, error) {
	number, ok := raw.(json.Number)
	if !ok {
		return nil, invalidContent("'%s' must be a number.", at)
	}
	n, err := number.Float64()
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return nil, invalidContent("'%s' must be a finite number.", at)
	}
	if outside(s, n) {
		return nil, invalidContent("'%s' value %v is outside the official range.", at, n)
	}
	return n, nil
}

func outside(s *Schema, n float64) bool {
	if s.Minimum != nil && (n < *s.Minimum || s.ExclusiveMin && n == *s.Minimum) {
		return true
	}
	return s.Maximum != nil && (n > *s.Maximum || s.ExclusiveMax && n == *s.Maximum)
}

// parameterValue converts one path or query parameter to its JSON value.
func parameterValue(p *Parameter, text string) (any, error) {
	bad := func(reason string) error {
		code := "InvalidQueryParameterValue"
		if p.In == "path" {
			code = "InvalidResourceName"
		}
		return fail(400, code, "The value '%s' of parameter '%s' is invalid: %s", text, p.Name, reason)
	}
	switch p.Type {
	case "boolean":
		switch strings.ToLower(text) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, bad("expected true or false.")
	case "integer":
		n, err := strconv.ParseInt(text, 10, 32)
		if err != nil || p.Minimum != nil && float64(n) < *p.Minimum || p.Maximum != nil && float64(n) > *p.Maximum {
			return nil, bad("expected an integer in the official range.")
		}
		return n, nil
	}
	if !utf8.ValidString(text) || len(text) > 4096 {
		return nil, bad("expected valid UTF-8 up to 4 KiB.")
	}
	if len(p.Enum) > 0 {
		canonical, ok := enumValue(p.Enum, text)
		if !ok {
			return nil, bad("expected one of the official values.")
		}
		text = canonical
	}
	length := utf8.RuneCountInString(text)
	if p.MinLength != nil && length < *p.MinLength || p.MaxLength != nil && length > *p.MaxLength {
		return nil, bad("its length is outside the official bounds.")
	}
	if p.pattern != nil && !p.pattern.MatchString(text) {
		return nil, bad("it does not match the official pattern " + p.Pattern + ".")
	}
	if p.Format == "uuid" && !uuidPattern.MatchString(text) {
		return nil, bad("expected a UUID.")
	}
	return text, nil
}

// lookup returns the value at a dotted path of a document.
func lookup(document map[string]any, dotted string) (any, bool) {
	var current any = document
	for _, part := range strings.Split(dotted, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// assign sets a value at a dotted path, creating intermediate objects.
func assign(document map[string]any, dotted string, value any) {
	parts := strings.Split(dotted, ".")
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

// collect returns every string addressed by a Ref path with "[]" and "{}".
func collect(value any, parts []string) []string {
	if len(parts) == 0 {
		if text, ok := value.(string); ok {
			return []string{text}
		}
		return nil
	}
	switch parts[0] {
	case "[]":
		items, _ := value.([]any)
		var out []string
		for _, item := range items {
			out = append(out, collect(item, parts[1:])...)
		}
		return out
	case "{}":
		object, _ := value.(map[string]any)
		var out []string
		for key := range object {
			out = append(out, key)
		}
		return out
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	next, ok := object[parts[0]]
	if !ok {
		return nil
	}
	return collect(next, parts[1:])
}

func splitRef(path string) []string {
	var parts []string
	for _, part := range strings.Split(path, ".") {
		if strings.HasSuffix(part, "[]") && part != "[]" {
			parts = append(parts, strings.TrimSuffix(part, "[]"), "[]")
			continue
		}
		parts = append(parts, part)
	}
	return parts
}
