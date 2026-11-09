package aws

import (
	"fmt"
	"strings"
)

// Validate symbols in BOTH arms without substituting values. Called only after
// stackSelectConditional has bounded and validated all intrinsic carrier shapes.
// The selected=true pass additionally refuses active links into disabled nodes.
func stackConditionalLinks(value any, resources map[string]any, refs map[string]string, enabled map[string]bool, selected bool) error {
	reference := func(name string, attribute bool) error {
		if !attribute && name == "AWS::NoValue" {
			return nil
		}
		logical, field, hasField := strings.Cut(name, ".")
		if !attribute {
			if hasField {
				return fmt.Errorf("Ref requires a logical ID, not an attribute")
			}
			if _, resource := resources[name]; !resource {
				if _, parameter := refs[name]; parameter {
					return nil
				}
				return fmt.Errorf("unknown template reference")
			}
		} else {
			if !hasField || logical == "" || field == "" {
				return fmt.Errorf("invalid attribute reference")
			}
			resource, ok := resources[logical]
			if !ok {
				return fmt.Errorf("unknown attribute resource")
			}
			kind := stringValue(object(resource)["Type"])
			valid := field == "Arn" && (kind == "AWS::IAM::Role" || kind == "AWS::IAM::InstanceProfile" || kind == "AWS::S3::Bucket" || kind == "AWS::Lambda::Function") || field == "Value" && kind == "AWS::SSM::Parameter" || field == "RootResourceId" && kind == "AWS::ApiGateway::RestApi"
			if !valid {
				return fmt.Errorf("unsupported resource attribute reference")
			}
		}
		if selected && !enabled[logical] {
			return fmt.Errorf("active declaration references a disabled resource")
		}
		return nil
	}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			if raw, ok := v["Ref"]; ok {
				return reference(raw.(string), false)
			}
			if raw, ok := v["Fn::GetAtt"]; ok {
				if pair, ok := raw.([]any); ok {
					return reference(pair[0].(string)+"."+pair[1].(string), true)
				}
				return reference(raw.(string), true)
			}
			if raw, ok := v["Fn::Sub"]; ok {
				parts, err := stackSubParts(raw.(string))
				if err != nil {
					return err
				}
				for _, part := range parts {
					if part.name != "" && !part.escaped {
						if err := reference(part.name, strings.Contains(part.name, ".")); err != nil {
							return err
						}
					}
				}
				return nil
			}
			for _, item := range v {
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

// DependsOn is not a value intrinsic; keep its original strict logical-ID form.
func stackConditionalDependency(value any, resources map[string]any, enabled map[string]bool, active bool) error {
	if value == nil {
		return nil
	}
	var list []any
	switch v := value.(type) {
	case string:
		list = []any{v}
	case []any:
		list = v
	default:
		return fmt.Errorf("invalid DependsOn")
	}
	if len(list) == 0 {
		return fmt.Errorf("empty DependsOn")
	}
	for _, v := range list {
		name, ok := v.(string)
		if !ok {
			return fmt.Errorf("invalid DependsOn logical ID")
		}
		if _, ok := resources[name]; !ok {
			return fmt.Errorf("unknown DependsOn logical ID")
		}
		if active && !enabled[name] {
			return fmt.Errorf("active declaration depends on a disabled resource")
		}
	}
	return nil
}
