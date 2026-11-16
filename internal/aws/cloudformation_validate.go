package aws

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The declaration checks are shared by ValidateTemplate and CreateStack.
// Value resolution, native resource compilation and caller permissions for
// provisioning require a particular stack, its parameters and its account.
func parseStackTemplate(body string) (map[string]any, error) {
	if len(body) == 0 || len(body) > 51200 {
		return nil, fmt.Errorf("Use a TemplateBody of 1–51200 bytes.")
	}
	decoder := yaml.NewDecoder(strings.NewReader(body))
	var node, extra yaml.Node
	if decoder.Decode(&node) != nil || len(node.Content) == 0 || decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("Invalid YAML/JSON template.")
	}
	value, err := taggedYAML(&node)
	if err != nil {
		return nil, err
	}
	template := object(value)
	for _, key := range slices.Sorted(maps.Keys(template)) {
		switch key {
		case "AWSTemplateFormatVersion", "Description", "Parameters", "Rules", "Conditions", "Resources", "Outputs":
		default:
			return nil, fmt.Errorf("Unsupported template section: %s", key)
		}
	}
	if description, has := template["Description"]; has {
		if text, ok := description.(string); !ok || len(text) > 1024 {
			return nil, fmt.Errorf("Description must be a string of at most 1024 bytes.")
		}
	}
	for _, section := range []string{"Parameters", "Rules", "Conditions", "Outputs"} {
		if value, has := template[section]; has && object(value) == nil {
			return nil, fmt.Errorf("%s must be an object.", section)
		}
	}
	resources := object(template["Resources"])
	if len(resources) == 0 || len(resources) > 100 {
		return nil, fmt.Errorf("Require 1–100 supported resources.")
	}
	for _, logicalID := range slices.Sorted(maps.Keys(resources)) {
		if !stackConditionalName.MatchString(logicalID) {
			return nil, fmt.Errorf("Invalid resource logical ID.")
		}
		r := object(resources[logicalID])
		kind := stringValue(r["Type"])
		if !supportedStackResourceType(kind) {
			return nil, fmt.Errorf("Unsupported resource type: %s", kind)
		}
		for key := range r {
			if kind == stackTaskDefinition && (key == "DeletionPolicy" || key == "UpdateReplacePolicy") {
				if r[key] != "Retain" && r[key] != "Delete" {
					return nil, fmt.Errorf("Task definitions support only Retain or Delete for %s", key)
				}
				continue
			}
			if key != "Type" && key != "Properties" && key != "DependsOn" && key != "Condition" {
				return nil, fmt.Errorf("Unsupported resource attribute: %s", key)
			}
		}
		if properties, has := r["Properties"]; has && object(properties) == nil {
			return nil, fmt.Errorf("Resource Properties must be an object.")
		}
	}
	return template, nil
}

func supportedStackResourceType(kind string) bool {
	switch kind {
	case stackTaskDefinition, stackVpcOrigin, "AWS::SSM::Parameter", "AWS::IAM::Role", "AWS::IAM::InstanceProfile", "AWS::S3::Bucket", "AWS::Lambda::Function", "AWS::EC2::Instance":
		return true
	default:
		return stackAPIKind(kind) || stackNetworkKind(kind)
	}
}

func validateStackTemplate(body string) (string, error) {
	template, err := parseStackTemplate(body)
	if err != nil {
		return "", err
	}
	definitions := object(template["Parameters"])
	if _, err := checkStackParameters(definitions, nil, false); err != nil {
		return "", err
	}
	if err := checkStackRules(object(template["Rules"]), definitions, nil, false); err != nil {
		return "", err
	}
	var out strings.Builder
	if description, ok := template["Description"].(string); ok {
		out.WriteString("<Description>" + xmlText(description) + "</Description>")
	}
	out.WriteString("<Parameters>")
	for _, name := range slices.Sorted(maps.Keys(definitions)) {
		def := object(definitions[name])
		noEcho := def["NoEcho"] == true
		out.WriteString("<member><ParameterKey>" + xmlText(name) + "</ParameterKey><NoEcho>" + strconv.FormatBool(noEcho) + "</NoEcho>")
		if desc, ok := def["Description"].(string); ok {
			out.WriteString("<Description>" + xmlText(desc) + "</Description>")
		}
		if value, has := stackScalarText(def["Default"]); has && !noEcho {
			out.WriteString("<DefaultValue>" + xmlText(value) + "</DefaultValue>")
		}
		out.WriteString("</member>")
	}
	out.WriteString("</Parameters>")
	capability := "CAPABILITY_IAM"
	var iamResources []string
	resources := object(template["Resources"])
	for _, id := range slices.Sorted(maps.Keys(resources)) {
		entry := object(resources[id])
		var nameKey string
		switch entry["Type"] {
		case "AWS::IAM::Role":
			nameKey = "RoleName"
		case "AWS::IAM::InstanceProfile":
			nameKey = "InstanceProfileName"
		default:
			continue
		}
		iamResources = append(iamResources, id)
		if _, named := object(entry["Properties"])[nameKey]; named {
			capability = "CAPABILITY_NAMED_IAM"
		}
	}
	if len(iamResources) > 0 {
		out.WriteString("<Capabilities><member>" + capability + "</member></Capabilities>")
		out.WriteString("<CapabilitiesReason>" + xmlText(strings.Join(iamResources, ", ")) + "</CapabilitiesReason>")
	}
	return out.String(), nil
}
