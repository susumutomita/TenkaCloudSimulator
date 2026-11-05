package aws

// Pure, bounded CloudFormation preparation. These functions do not parse YAML,
// open a transaction, allocate resources, grant policy or accept whole templates.
// The compiler must retain its section/resource/property allowlists, extend only
// the explicitly supported shorthand tags, and resolve selected Ref/GetAtt/Sub
// values through its existing symbol/ownership checks before any state mutation.

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const stackConditionalLimit = 100
const stackConditionalDepth = 32
const stackConditionalNodes = 10000

var stackConditionalName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,254}$`)
var stackDecimal = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// Native taggedYAML preserves number text as string. json.Number also supports a
// lossless explicit JSON decoder. Floats, booleans and null are not stringified.
func stackScalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return string(x), true
	}
	return "", false
}
func stackFiniteNumber(v any) (*big.Rat, error) {
	s, ok := stackScalarText(v)
	if !ok || len(s) > 128 || !stackDecimal.MatchString(s) {
		return nil, fmt.Errorf("expected bounded finite decimal number")
	}
	if pos := strings.IndexAny(s, "eE"); pos >= 0 {
		exponent, err := strconv.Atoi(s[pos+1:])
		if err != nil || exponent < -308 || exponent > 308 {
			return nil, fmt.Errorf("numeric exponent exceeds local bound")
		}
	}
	n, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid decimal number")
	}
	return n, nil
}
func stackConstraintLength(v any) (int, error) {
	s, ok := stackScalarText(v)
	if !ok || len(s) > 6 {
		return 0, fmt.Errorf("invalid length constraint")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 51200 {
		return 0, fmt.Errorf("length constraint outside local bound")
	}
	return n, nil
}

// stackParameterValues returns a fresh map. It validates every definition and
// default, even when a supplied value overrides that default. Values and
// ConstraintDescription are never interpolated into failure text. Existing
// byte-length String behavior is preserved; this is not Unicode-length parity.
func stackParameterValues(definitions map[string]any, supplied map[string]string) (map[string]string, error) {
	fail := func(name, why string) (map[string]string, error) {
		return nil, fmt.Errorf("parameter %s: %s", name, why)
	}
	if len(definitions) > stackConditionalLimit || len(supplied) > stackConditionalLimit {
		return nil, fmt.Errorf("local parameter count exceeds 100")
	}
	for name := range supplied {
		if _, ok := definitions[name]; !ok {
			return fail(name, "unknown parameter")
		}
	}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	result := map[string]string{}
	for _, name := range names {
		if !stackConditionalName.MatchString(name) {
			return fail("definition", "invalid logical name")
		}
		d, ok := definitions[name].(map[string]any)
		if !ok {
			return fail(name, "definition must be an object")
		}
		kind, ok := d["Type"].(string)
		if !ok || kind != "String" && kind != "Number" {
			return fail(name, "only String and Number are supported")
		}
		for field, v := range d {
			switch field {
			case "Type", "Default", "AllowedValues":
			case "Description", "ConstraintDescription":
				if text, ok := v.(string); !ok || len(text) > 4000 {
					return fail(name, "invalid description")
				}
			case "NoEcho":
				if _, ok := v.(bool); !ok {
					return fail(name, "NoEcho must be Boolean")
				}
			case "AllowedPattern":
				if kind != "String" {
					return fail(name, "AllowedPattern requires String")
				}
				if text, ok := v.(string); !ok || len(text) > 1024 {
					return fail(name, "invalid AllowedPattern")
				}
			case "MinLength", "MaxLength":
				if kind != "String" {
					return fail(name, "length requires String")
				}
				if _, err := stackConstraintLength(v); err != nil {
					return fail(name, "invalid length constraint")
				}
			case "MinValue", "MaxValue":
				if kind != "Number" {
					return fail(name, "numeric constraint requires Number")
				}
				if _, err := stackFiniteNumber(v); err != nil {
					return fail(name, "invalid numeric constraint")
				}
			default:
				return fail(name, "unsupported definition field")
			}
		}
		var pattern *regexp.Regexp
		if raw, exists := d["AllowedPattern"]; exists {
			var err error
			pattern, err = regexp.Compile("^(?:" + raw.(string) + ")$")
			if err != nil {
				return fail(name, "invalid AllowedPattern")
			}
		}
		minLen, maxLen := 0, 51200
		if raw, ok := d["MinLength"]; ok {
			minLen, _ = stackConstraintLength(raw)
		}
		if raw, ok := d["MaxLength"]; ok {
			maxLen, _ = stackConstraintLength(raw)
		}
		if minLen > maxLen {
			return fail(name, "inverted length constraints")
		}
		var minNum, maxNum *big.Rat
		if raw, ok := d["MinValue"]; ok {
			minNum, _ = stackFiniteNumber(raw)
		}
		if raw, ok := d["MaxValue"]; ok {
			maxNum, _ = stackFiniteNumber(raw)
		}
		if minNum != nil && maxNum != nil && minNum.Cmp(maxNum) > 0 {
			return fail(name, "inverted numeric constraints")
		}
		allowed := []string(nil)
		if raw, exists := d["AllowedValues"]; exists {
			entries, ok := raw.([]any)
			if !ok || len(entries) == 0 || len(entries) > 100 {
				return fail(name, "AllowedValues requires 1–100 scalar values")
			}
			for _, entry := range entries {
				text, ok := stackScalarText(entry)
				if !ok || len(text) > 51200 {
					return fail(name, "invalid AllowedValues member")
				}
				if kind == "Number" {
					if _, err := stackFiniteNumber(entry); err != nil {
						return fail(name, "invalid numeric AllowedValues")
					}
				}
				allowed = append(allowed, text)
			}
		}
		validate := func(text string) error {
			if len(text) > 51200 {
				return fmt.Errorf("value exceeds local byte limit")
			}
			if kind == "String" {
				if len(text) < minLen || len(text) > maxLen || pattern != nil && !pattern.MatchString(text) {
					return fmt.Errorf("String constraint failed")
				}
			} else {
				n, err := stackFiniteNumber(text)
				if err != nil {
					return fmt.Errorf("invalid Number")
				}
				if minNum != nil && n.Cmp(minNum) < 0 || maxNum != nil && n.Cmp(maxNum) > 0 {
					return fmt.Errorf("Number constraint failed")
				}
			}
			if allowed != nil {
				found := false
				for _, a := range allowed {
					if kind == "String" {
						found = found || text == a
					} else {
						x, _ := stackFiniteNumber(text)
						y, _ := stackFiniteNumber(a)
						found = found || x.Cmp(y) == 0
					}
				}
				if !found {
					return fmt.Errorf("value not in AllowedValues")
				}
			}
			return nil
		}
		fallback, hasDefault := "", false
		if raw, exists := d["Default"]; exists {
			fallback, hasDefault = stackScalarText(raw)
			if !hasDefault {
				return fail(name, "invalid default type")
			}
			if err := validate(fallback); err != nil {
				return fail(name, "default violates constraints")
			}
		}
		value, suppliedValue := supplied[name]
		if !suppliedValue {
			if !hasDefault {
				return fail(name, "missing required value")
			}
			value = fallback
		}
		if err := validate(value); err != nil {
			return fail(name, err.Error())
		}
		result[name] = value
	}
	return result, nil
}

// Condition operands resolve only through immutable parameter/pseudo references,
// never resources. Alias conditions are supported solely as {Condition: name};
// And/Or/Not and other condition languages remain explicit unsupported errors.
func stackConditionValues(definitions map[string]any, references map[string]string) (map[string]bool, error) {
	if len(definitions) > stackConditionalLimit {
		return nil, fmt.Errorf("local condition count exceeds 100")
	}
	result, state := map[string]bool{}, map[string]uint8{}
	var evaluate func(string, int) (bool, error)
	operand := func(value any) (string, error) {
		if text, ok := stackScalarText(value); ok {
			if len(text) > 51200 {
				return "", fmt.Errorf("condition operand exceeds local byte bound")
			}
			return text, nil
		}
		ref, ok := value.(map[string]any)
		if !ok || len(ref) != 1 {
			return "", fmt.Errorf("condition operand must be scalar or parameter Ref")
		}
		name, ok := ref["Ref"].(string)
		if !ok || name == "AWS::NoValue" {
			return "", fmt.Errorf("invalid condition Ref")
		}
		text, exists := references[name]
		if !exists {
			return "", fmt.Errorf("unknown condition Ref")
		}
		return text, nil
	}
	evaluate = func(name string, depth int) (bool, error) {
		if depth > stackConditionalDepth {
			return false, fmt.Errorf("condition depth exceeds 32")
		}
		if !stackConditionalName.MatchString(name) {
			return false, fmt.Errorf("invalid condition name")
		}
		if state[name] == 2 {
			return result[name], nil
		}
		if state[name] == 1 {
			return false, fmt.Errorf("condition cycle")
		}
		raw, exists := definitions[name]
		if !exists {
			return false, fmt.Errorf("unknown condition")
		}
		expression, ok := raw.(map[string]any)
		if !ok || len(expression) != 1 {
			return false, fmt.Errorf("condition requires one supported expression")
		}
		state[name] = 1
		var value bool
		if alias, exists := expression["Condition"]; exists {
			target, ok := alias.(string)
			if !ok {
				return false, fmt.Errorf("invalid condition alias")
			}
			var err error
			value, err = evaluate(target, depth+1)
			if err != nil {
				return false, err
			}
		} else if raw, exists := expression["Fn::Equals"]; exists {
			args, ok := raw.([]any)
			if !ok || len(args) != 2 {
				return false, fmt.Errorf("Equals requires exactly two operands")
			}
			left, err := operand(args[0])
			if err != nil {
				return false, err
			}
			right, err := operand(args[1])
			if err != nil {
				return false, err
			}
			value = left == right
		} else {
			return false, fmt.Errorf("unsupported condition expression")
		}
		result[name], state[name] = value, 2
		return value, nil
	}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := evaluate(name, 1); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Enabled is an attribute gate only. The caller must still validate the entire
// resource/output declaration against existing allowlists, including disabled
// declarations. This does not delete the attribute or authorize a resource.
func stackConditionalEnabled(record map[string]any, conditions map[string]bool) (bool, error) {
	raw, exists := record["Condition"]
	if !exists {
		return true, nil
	}
	name, ok := raw.(string)
	if !ok {
		return false, fmt.Errorf("Condition attribute requires a name")
	}
	enabled, exists := conditions[name]
	if !exists {
		return false, fmt.Errorf("unknown Condition attribute")
	}
	return enabled, nil
}

// Select validates BOTH branch shapes but resolves only selected condition arms.
// Unknown resource Ref/GetAtt symbols are deliberately left to the compiler's
// existing linker; this helper must not be used as whole-template validation.
// AWS::NoValue is accepted only as a direct If arm and prunes that map property
// or list element. The returned omit bit makes root removal explicit to callers.
func stackSelectConditional(value any, conditions map[string]bool) (any, bool, error) {
	nodes := 0
	var check func(any, int, bool) error
	check = func(value any, depth int, noValueArm bool) error {
		nodes++
		if depth > stackConditionalDepth || nodes > stackConditionalNodes {
			return fmt.Errorf("conditional value exceeds local depth/node bound")
		}
		switch x := value.(type) {
		case nil, bool:
			return nil
		case string:
			if len(x) > 51200 {
				return fmt.Errorf("conditional string exceeds local byte bound")
			}
			return nil
		case json.Number:
			_, err := stackFiniteNumber(x)
			return err
		case []any:
			for _, v := range x {
				if err := check(v, depth+1, false); err != nil {
					return err
				}
			}
			return nil
		case map[string]any:
			for key := range x {
				if key == "Ref" || key == "Fn::If" || strings.HasPrefix(key, "Fn::") {
					if len(x) != 1 {
						return fmt.Errorf("intrinsic must have exactly one key")
					}
					switch key {
					case "Fn::If":
						args, ok := x[key].([]any)
						if !ok || len(args) != 3 {
							return fmt.Errorf("If requires exactly three arguments")
						}
						name, ok := args[0].(string)
						if !ok {
							return fmt.Errorf("If requires a condition name")
						}
						if _, ok := conditions[name]; !ok {
							return fmt.Errorf("If names unknown condition")
						}
						for _, arm := range args[1:] {
							if err := check(arm, depth+1, true); err != nil {
								return err
							}
						}
						return nil
					case "Ref":
						name, ok := x[key].(string)
						if !ok || name == "" || len(name) > 51200 {
							return fmt.Errorf("invalid Ref")
						}
						if name == "AWS::NoValue" && !noValueArm {
							return fmt.Errorf("NoValue requires a direct If arm")
						}
						return nil
					case "Fn::Sub":
						if text, ok := x[key].(string); !ok || len(text) > 51200 {
							return fmt.Errorf("only bounded scalar Sub supported")
						}
						return nil
					case "Fn::GetAtt":
						if name, ok := x[key].(string); ok && len(name) <= 51200 && strings.Count(name, ".") == 1 {
							left, right, _ := strings.Cut(name, ".")
							if left != "" && right != "" {
								return nil
							}
						}
						if pair, ok := x[key].([]any); ok && len(pair) == 2 {
							a, aok := pair[0].(string)
							b, bok := pair[1].(string)
							if aok && bok && a != "" && b != "" && len(a)+len(b)+1 <= 51200 {
								return nil
							}
						}
						return fmt.Errorf("invalid GetAtt")
					default:
						return fmt.Errorf("unsupported conditional value intrinsic")
					}
				}
			}
			for _, v := range x {
				if err := check(v, depth+1, false); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unsupported conditional value type")
		}
	}
	if err := check(value, 1, false); err != nil {
		return nil, false, err
	}
	var selectValue func(any) (any, bool)
	selectValue = func(v any) (any, bool) {
		switch x := v.(type) {
		case map[string]any:
			if raw, ok := x["Fn::If"]; ok {
				args := raw.([]any)
				arm := args[2]
				if conditions[args[0].(string)] {
					arm = args[1]
				}
				if ref, ok := arm.(map[string]any); ok && len(ref) == 1 && ref["Ref"] == "AWS::NoValue" {
					return nil, true
				}
				return selectValue(arm)
			}
			out := map[string]any{}
			for k, v := range x {
				selected, omit := selectValue(v)
				if !omit {
					out[k] = selected
				}
			}
			return out, false
		case []any:
			out := []any{}
			for _, v := range x {
				selected, omit := selectValue(v)
				if !omit {
					out = append(out, selected)
				}
			}
			return out, false
		default:
			return v, false
		}
	}
	result, omit := selectValue(value)
	return result, omit, nil
}
