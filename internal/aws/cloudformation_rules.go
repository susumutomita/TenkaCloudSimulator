package aws

import (
	"fmt"
	"maps"
	"slices"
)

type stackTruth struct {
	value  bool
	height int
}

// Structural height includes the longest named alias chain, even when cached.
type stackPredicateEvaluator struct {
	references map[string]string
	alias      func(string, int) (stackTruth, error)
	nodes      int
}

func stackConditionOperand(value any, references map[string]string) (string, error) {
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

func (e *stackPredicateEvaluator) eval(raw any, depth int) (stackTruth, error) {
	e.nodes++
	if depth > stackConditionalDepth {
		return stackTruth{}, fmt.Errorf("condition depth exceeds 32")
	}
	if e.nodes > stackConditionalNodes {
		return stackTruth{}, fmt.Errorf("condition node limit exceeded")
	}
	expr := object(raw)
	if len(expr) != 1 {
		return stackTruth{}, fmt.Errorf("condition requires one supported expression")
	}
	for fn, value := range expr {
		args, seq := value.([]any)
		switch fn {
		case "Condition":
			name, ok := value.(string)
			if !ok || e.alias == nil {
				return stackTruth{}, fmt.Errorf("invalid condition alias")
			}
			target, err := e.alias(name, depth+1)
			target.height++
			return target, err
		case "Fn::Equals":
			if !seq || len(args) != 2 {
				return stackTruth{}, fmt.Errorf("Equals requires exactly two operands")
			}
			left, err := stackConditionOperand(args[0], e.references)
			if err != nil {
				return stackTruth{}, err
			}
			right, err := stackConditionOperand(args[1], e.references)
			if err != nil {
				return stackTruth{}, err
			}
			return stackTruth{value: left == right, height: 1}, nil
		case "Fn::Not", "Fn::And":
			if fn == "Fn::Not" && (!seq || len(args) != 1) {
				return stackTruth{}, fmt.Errorf("Not requires exactly one condition")
			}
			if fn == "Fn::And" && (!seq || len(args) < 2 || len(args) > 10) {
				return stackTruth{}, fmt.Errorf("And requires 2–10 conditions")
			}
			combined := stackTruth{value: true, height: 1}
			for _, arg := range args {
				// Validate every operand, including ones after a false condition.
				v, err := e.eval(arg, depth+1)
				if err != nil {
					return stackTruth{}, err
				}
				combined.height = max(combined.height, 1+v.height)
				combined.value = combined.value && v.value
			}
			if fn == "Fn::Not" {
				combined.value = !combined.value
			}
			return combined, nil
		}
	}
	return stackTruth{}, fmt.Errorf("unsupported condition expression")
}

// Without parameter values this checks arity, operand types and references.
// With values it also enforces every active assertion before native provisioning.
func checkStackRules(rules, definitions map[string]any, values map[string]string, enforce bool) error {
	if len(rules) == 0 {
		return nil
	}
	if len(rules) > stackConditionalLimit {
		return fmt.Errorf("local rule count exceeds 100")
	}
	refs := make(map[string]string, len(definitions))
	// Rules compare scalar inputs. Reject references to SSM-backed inputs so a
	// parameter's lookup key cannot be mistaken for its resolved value.
	for name, definition := range definitions {
		kind := stringValue(object(definition)["Type"])
		if kind == "String" || kind == "Number" {
			refs[name] = values[name]
		}
	}
	evaluator := stackPredicateEvaluator{references: refs}
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		rule := object(rules[name])
		if !stackConditionalName.MatchString(name) || rule == nil {
			return fmt.Errorf("invalid rule declaration")
		}
		for k := range rule {
			if k != "RuleCondition" && k != "Assertions" {
				return fmt.Errorf("unsupported rule field in %s", name)
			}
		}
		enabled := true
		if expr, has := rule["RuleCondition"]; has {
			v, err := evaluator.eval(expr, 1)
			if err != nil {
				return err
			}
			enabled = v.value
		}
		assertions, ok := rule["Assertions"].([]any)
		if !ok || len(assertions) == 0 || len(assertions) > stackConditionalLimit {
			return fmt.Errorf("rule %s requires 1–100 assertions", name)
		}
		for i, entry := range assertions {
			assertion := object(entry)
			for k := range assertion {
				if k != "Assert" && k != "AssertDescription" {
					return fmt.Errorf("unsupported assertion field in rule %s", name)
				}
			}
			if desc, has := assertion["AssertDescription"]; has {
				if _, ok := desc.(string); !ok {
					return fmt.Errorf("invalid assertion description in rule %s", name)
				}
			}
			v, err := evaluator.eval(assertion["Assert"], 1)
			if err != nil {
				return err
			}
			if enforce && enabled && !v.value {
				return fmt.Errorf("rule %s assertion %d failed", name, i+1)
			}
		}
	}
	return nil
}
