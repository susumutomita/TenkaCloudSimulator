package catalog

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Bounds on decoded request trees; they are local resource limits, not AWS quotas.
const (
	maxDepth = 24
	maxNodes = 4096
)

// fault is a typed engine error mapped onto modeled per-service exceptions.
type fault struct {
	kind    string // validation, notfound, conflict, limit, denied, unsupported, internal, mismatch
	message string
	code    string // optional preferred modeled exception
}

func (f *fault) Error() string { return f.message }

func invalid(format string, args ...any) *fault {
	return &fault{kind: "validation", message: fmt.Sprintf(format, args...)}
}

var patternCache sync.Map // string -> *regexp.Regexp or nil when RE2 cannot express it

func compiledPattern(pattern string) *regexp.Regexp {
	if cached, ok := patternCache.Load(pattern); ok {
		re, _ := cached.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		re = nil
	}
	patternCache.Store(pattern, re)
	return re
}

// decoder validates a JSON-decoded tree against official shapes.
type decoder struct {
	model *Model
	nodes int
}

// decode converts raw (decoded with UseNumber) into canonical values:
// map[string]any, []any, string, int64, float64, bool, time.Time, []byte.
func (d *decoder) decode(shapeName string, raw any, at string, depth int) (any, error) {
	d.nodes++
	if d.nodes > maxNodes || depth > maxDepth {
		return nil, invalid("Request structure exceeds the simulator's bounded nesting or size at %s.", at)
	}
	shape := d.model.shape(shapeName)
	if raw == nil {
		return nil, invalid("Null is not accepted for %s.", at)
	}
	switch shape.Type {
	case "structure":
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, invalid("Expected an object for %s.", at)
		}
		if shape.Document {
			return d.document(raw, at, depth)
		}
		out := make(map[string]any, len(object))
		for name, value := range object {
			member, known := shape.Members[name]
			if !known {
				return nil, invalid("Unknown member %s.%s is not part of the official input shape.", at, name)
			}
			converted, err := d.decode(member.Shape, value, at+"."+name, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = converted
		}
		if err := d.structureRules(shape, out, at); err != nil {
			return nil, err
		}
		return out, nil
	case "list":
		items, ok := raw.([]any)
		if !ok {
			return nil, invalid("Expected a list for %s.", at)
		}
		if err := lengthRule(shape, len(items), at); err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, item := range items {
			converted, err := d.decode(shape.Member.Shape, item, fmt.Sprintf("%s[%d]", at, i), depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	case "map":
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, invalid("Expected a map for %s.", at)
		}
		if err := lengthRule(shape, len(object), at); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(object))
		for key, value := range object {
			if _, err := d.decode(shape.Key.Shape, key, at+" key", depth+1); err != nil {
				return nil, err
			}
			converted, err := d.decode(shape.Value.Shape, value, at+"."+key, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case "string":
		text, ok := raw.(string)
		if !ok {
			return nil, invalid("Expected a string for %s.", at)
		}
		return text, stringRules(shape, text, at)
	case "integer", "long":
		number, err := integerValue(raw, at)
		if err != nil {
			return nil, err
		}
		return number, rangeRule(shape, float64(number), at)
	case "float", "double":
		number, err := floatValue(raw, at)
		if err != nil {
			return nil, err
		}
		return number, rangeRule(shape, number, at)
	case "boolean":
		value, ok := raw.(bool)
		if !ok {
			return nil, invalid("Expected a boolean for %s.", at)
		}
		return value, nil
	case "timestamp":
		return timestampValue(raw, at)
	case "blob":
		text, ok := raw.(string)
		if !ok {
			return nil, invalid("Expected base64 text for %s.", at)
		}
		data, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return nil, invalid("Expected base64 text for %s.", at)
		}
		return data, lengthRule(shape, len(data), at)
	}
	return nil, &fault{kind: "unsupported", message: fmt.Sprintf("Shape type %s at %s is not supported by the simulator.", shape.Type, at)}
}

func (d *decoder) document(raw any, at string, depth int) (any, error) {
	d.nodes++
	if d.nodes > maxNodes || depth > maxDepth {
		return nil, invalid("Document %s exceeds the simulator's bounded nesting or size.", at)
	}
	switch value := raw.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			converted, err := d.document(item, at, depth+1)
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
		return value.Float64()
	default:
		return value, nil
	}
}

func (d *decoder) structureRules(shape *Shape, out map[string]any, at string) error {
	for _, name := range shape.Required {
		if _, ok := out[name]; !ok {
			return invalid("Missing required member %s.%s.", at, name)
		}
	}
	if shape.Union && len(out) != 1 {
		return invalid("Exactly one member of union %s must be set.", at)
	}
	return nil
}

func lengthRule(shape *Shape, n int, at string) error {
	if shape.Min != nil && float64(n) < *shape.Min {
		return invalid("%s must have at least %v elements or bytes.", at, *shape.Min)
	}
	if shape.Max != nil && float64(n) > *shape.Max {
		return invalid("%s must have at most %v elements or bytes.", at, *shape.Max)
	}
	return nil
}

func stringRules(shape *Shape, text, at string) error {
	if !utf8.ValidString(text) {
		return invalid("%s must be valid UTF-8.", at)
	}
	n := utf8.RuneCountInString(text)
	if shape.Min != nil && float64(n) < *shape.Min {
		return invalid("%s must be at least %v characters.", at, *shape.Min)
	}
	if shape.Max != nil && float64(n) > *shape.Max {
		return invalid("%s must be at most %v characters.", at, *shape.Max)
	}
	if len(shape.Enum) > 0 {
		found := false
		for _, allowed := range shape.Enum {
			if allowed == text {
				found = true
				break
			}
		}
		if !found {
			return invalid("%s must be one of %s.", at, strings.Join(shape.Enum, ", "))
		}
	}
	if shape.Pattern != "" {
		if re := compiledPattern(shape.Pattern); re != nil && !re.MatchString(text) {
			return invalid("%s does not match the official pattern.", at)
		}
	}
	return nil
}

func rangeRule(shape *Shape, n float64, at string) error {
	if shape.Min != nil && n < *shape.Min {
		return invalid("%s must be at least %v.", at, *shape.Min)
	}
	if shape.Max != nil && n > *shape.Max {
		return invalid("%s must be at most %v.", at, *shape.Max)
	}
	return nil
}

func integerValue(raw any, at string) (int64, error) {
	switch value := raw.(type) {
	case json.Number:
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return 0, invalid("Expected an integer for %s.", at)
		}
		return n, nil
	case string:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, invalid("Expected an integer for %s.", at)
		}
		return n, nil
	}
	return 0, invalid("Expected an integer for %s.", at)
}

func floatValue(raw any, at string) (float64, error) {
	var text string
	switch value := raw.(type) {
	case json.Number:
		text = string(value)
	case string:
		text = value
	default:
		return 0, invalid("Expected a number for %s.", at)
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, invalid("Expected a finite number for %s.", at)
	}
	return n, nil
}

func timestampValue(raw any, at string) (time.Time, error) {
	switch value := raw.(type) {
	case json.Number:
		seconds, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 253402300799 {
			return time.Time{}, invalid("Expected epoch seconds for %s.", at)
		}
		whole, frac := math.Modf(seconds)
		return time.Unix(int64(whole), int64(math.Round(frac*1e3))*1e6).UTC(), nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC1123, "2006-01-02T15:04:05Z0700"} {
			if t, err := time.Parse(layout, value); err == nil {
				return t.UTC(), nil
			}
		}
		if seconds, err := strconv.ParseFloat(value, 64); err == nil {
			return timestampValue(json.Number(strconv.FormatFloat(seconds, 'f', -1, 64)), at)
		}
	}
	return time.Time{}, invalid("Expected a timestamp for %s.", at)
}

// stored converts canonical values to the JSON stored in SQLite.
func stored(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = stored(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = stored(item)
		}
		return out
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano)
	case []byte:
		return base64.StdEncoding.EncodeToString(v)
	}
	return value
}

// encoder projects stored JSON onto an official output shape.
type encoder struct {
	model    *Model
	protocol string
}

// project keeps only members declared by the shape and converts scalar types.
// Unknown stored keys are dropped; absent members are omitted.
func (e *encoder) project(shapeName string, value any, depth int) (any, bool) {
	if value == nil || depth > maxDepth*2 {
		return nil, false
	}
	shape := e.model.shape(shapeName)
	switch shape.Type {
	case "structure":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if shape.Document {
			return object, true
		}
		out := map[string]any{}
		for name, member := range shape.Members {
			if item, present := object[name]; present {
				if converted, ok := e.project(member.Shape, item, depth+1); ok {
					out[name] = converted
				}
			}
		}
		return out, true
	case "list":
		items, ok := value.([]any)
		if !ok {
			return nil, false
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			if converted, ok := e.project(shape.Member.Shape, item, depth+1); ok {
				out = append(out, converted)
			}
		}
		return out, true
	case "map":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		out := make(map[string]any, len(object))
		for key, item := range object {
			if converted, ok := e.project(shape.Value.Shape, item, depth+1); ok {
				out[key] = converted
			}
		}
		return out, true
	case "string":
		text, ok := value.(string)
		return text, ok
	case "integer", "long":
		switch n := value.(type) {
		case float64:
			return int64(n), true
		case int64:
			return n, true
		case int:
			return int64(n), true
		case json.Number:
			i, err := n.Int64()
			return i, err == nil
		}
		return nil, false
	case "float", "double":
		switch n := value.(type) {
		case float64:
			return n, true
		case int64:
			return float64(n), true
		case int:
			return float64(n), true
		case json.Number:
			f, err := n.Float64()
			return f, err == nil
		}
		return nil, false
	case "boolean":
		b, ok := value.(bool)
		return b, ok
	case "timestamp":
		var t time.Time
		switch v := value.(type) {
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, false
			}
			t = parsed
		case time.Time:
			t = v
		default:
			return nil, false
		}
		if e.protocol == "query" || e.protocol == "rest-xml" || shape.TimestampFmt == "iso8601" {
			return t.UTC().Format("2006-01-02T15:04:05.000Z"), true
		}
		return json.Number(strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', -1, 64)), true
	case "blob":
		text, ok := value.(string)
		return text, ok
	}
	return nil, false
}
