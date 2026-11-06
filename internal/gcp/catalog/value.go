package catalog

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDepth = 24
	maxNodes = 4096
)

// status is a google.rpc.Status failure with its canonical HTTP mapping.
type status struct {
	code    string // INVALID_ARGUMENT, NOT_FOUND, ALREADY_EXISTS, ...
	message string
}

func (s *status) Error() string { return s.message }

var httpCodes = map[string]int{
	"INVALID_ARGUMENT": 400, "FAILED_PRECONDITION": 400, "OUT_OF_RANGE": 400, "UNAUTHENTICATED": 401,
	"PERMISSION_DENIED": 403, "NOT_FOUND": 404, "ABORTED": 409, "ALREADY_EXISTS": 409,
	"RESOURCE_EXHAUSTED": 429, "INTERNAL": 500, "UNIMPLEMENTED": 501, "UNAVAILABLE": 503,
}

func invalid(format string, args ...any) *status {
	return &status{"INVALID_ARGUMENT", fmt.Sprintf(format, args...)}
}

var durationPattern = regexp.MustCompile(`^-?[0-9]+(\.[0-9]{1,9})?s$`)

// decoder validates request JSON (decoded with UseNumber) against schemas.
// Output-only fields are dropped as AIP-203 requires of servers.
type decoder struct {
	model *Model
	nodes int
}

func (d *decoder) decode(p *Property, raw any, at string, depth int) (any, error) {
	d.nodes++
	if d.nodes > maxNodes || depth > maxDepth {
		return nil, invalid("Request payload at '%s' exceeds the simulator's bounded nesting or size.", at)
	}
	p = d.model.resolve(p)
	if raw == nil {
		return nil, invalid("Invalid value at '%s': null is not accepted.", at)
	}
	switch p.Type {
	case "object":
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, invalid("Invalid value at '%s': expected an object.", at)
		}
		out := map[string]any{}
		if p.Properties == nil {
			if p.Additional == nil {
				// An open protobuf Struct or Any is stored as given JSON.
				return documentValue(raw, at, depth, d)
			}
			for key, value := range object {
				converted, err := d.decode(p.Additional, value, at+"["+key+"]", depth+1)
				if err != nil {
					return nil, err
				}
				out[key] = converted
			}
			return out, nil
		}
		for key, value := range object {
			field, known := p.Properties[key]
			if !known {
				return nil, invalid("Invalid JSON payload received. Unknown name %q at '%s': Cannot find field.", key, at)
			}
			converted, err := d.decode(field, value, joinPath(at, key), depth+1)
			if err != nil {
				return nil, err
			}
			if !field.ReadOnly {
				out[key] = converted
			}
		}
		return out, nil
	case "array":
		items, ok := raw.([]any)
		if !ok {
			return nil, invalid("Invalid value at '%s': expected a list.", at)
		}
		out := make([]any, len(items))
		for i, item := range items {
			converted, err := d.decode(p.Items, item, fmt.Sprintf("%s[%d]", at, i), depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	case "string":
		return stringValue(p, raw, at)
	case "integer":
		return integerValue(p, raw, at)
	case "number":
		return numberValue(raw, at)
	case "boolean":
		value, ok := raw.(bool)
		if !ok {
			return nil, invalid("Invalid value at '%s': expected true or false.", at)
		}
		return value, nil
	case "any", "":
		return documentValue(raw, at, depth, d)
	}
	return nil, invalid("Field '%s' uses an unsupported schema type.", at)
}

func joinPath(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func documentValue(raw any, at string, depth int, d *decoder) (any, error) {
	d.nodes++
	if d.nodes > maxNodes || depth > maxDepth {
		return nil, invalid("Request payload at '%s' exceeds the simulator's bounded nesting or size.", at)
	}
	switch value := raw.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			converted, err := documentValue(item, at, depth+1, d)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			converted, err := documentValue(item, at, depth+1, d)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	case json.Number:
		return value.Float64()
	}
	return raw, nil
}

func stringValue(p *Property, raw any, at string) (any, error) {
	text, ok := raw.(string)
	if !ok {
		return nil, invalid("Invalid value at '%s': expected a string.", at)
	}
	if !utf8.ValidString(text) || len(text) > 64<<10 {
		return nil, invalid("Invalid value at '%s': text must be valid UTF-8 up to 64 KiB.", at)
	}
	if len(p.Enum) > 0 {
		for _, allowed := range p.Enum {
			if allowed == text {
				return text, nil
			}
		}
		return nil, invalid("Invalid value at '%s': %q is not a valid enum value.", at, text)
	}
	switch p.Format {
	case "google-datetime", "date-time":
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return nil, invalid("Invalid value at '%s': expected an RFC 3339 timestamp.", at)
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	case "google-duration":
		if !durationPattern.MatchString(text) {
			return nil, invalid("Invalid value at '%s': expected a duration such as \"3.5s\".", at)
		}
	case "byte":
		if _, err := base64.StdEncoding.DecodeString(text); err != nil {
			if _, err := base64.URLEncoding.DecodeString(text); err != nil {
				return nil, invalid("Invalid value at '%s': expected base64 bytes.", at)
			}
		}
	case "int64", "uint64":
		return integerValue(p, raw, at)
	}
	return text, nil
}

func integerValue(p *Property, raw any, at string) (any, error) {
	var text string
	switch value := raw.(type) {
	case json.Number:
		text = string(value)
	case string:
		text = value
	default:
		return nil, invalid("Invalid value at '%s': expected an integer.", at)
	}
	bits := 32
	if p.Format == "int64" || p.Format == "uint64" {
		bits = 64
	}
	if strings.HasPrefix(p.Format, "uint") {
		n, err := strconv.ParseUint(text, 10, bits)
		if err != nil {
			return nil, invalid("Invalid value at '%s': expected an unsigned %d-bit integer.", at, bits)
		}
		if bits == 64 {
			return strconv.FormatUint(n, 10), nil
		}
		return int64(n), nil
	}
	n, err := strconv.ParseInt(text, 10, bits)
	if err != nil {
		return nil, invalid("Invalid value at '%s': expected a %d-bit integer.", at, bits)
	}
	if bits == 64 {
		// The proto3 JSON mapping renders 64-bit integers as strings.
		return strconv.FormatInt(n, 10), nil
	}
	return n, nil
}

func numberValue(raw any, at string) (any, error) {
	switch value := raw.(type) {
	case json.Number:
		n, err := value.Float64()
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, invalid("Invalid value at '%s': expected a finite number.", at)
		}
		return n, nil
	case string:
		if value == "NaN" || value == "Infinity" || value == "-Infinity" {
			return value, nil
		}
	}
	return nil, invalid("Invalid value at '%s': expected a number.", at)
}

// scalarParameter converts one query or path parameter to its JSON value.
func scalarParameter(p Parameter, text string) (any, error) {
	switch p.Type {
	case "boolean":
		if text != "true" && text != "false" {
			return nil, invalid("Invalid boolean parameter value %q.", text)
		}
		return text == "true", nil
	case "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, invalid("Invalid integer parameter value %q.", text)
		}
		return n, nil
	}
	if len(p.Enum) > 0 {
		for _, allowed := range p.Enum {
			if allowed == text {
				return text, nil
			}
		}
		return nil, invalid("Invalid enum parameter value %q.", text)
	}
	return text, nil
}
