package arm

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// OpenAPI renders each reviewed provider as an OpenAPI 3.1 document derived
// only from its embedded official subset.
func (e *Engine) OpenAPI() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, p := range e.order {
		out[p.spec.Namespace] = e.openAPI(p)
	}
	return out
}

var errorResponses = map[string]string{
	"400": "ARM error envelope: InvalidRequestContent, InvalidApiVersionParameter, MissingApiVersionParameter, LocationNotAvailableForResourceType, PropertyChangeNotAllowed or a reviewed semantic code.",
	"401": "AuthenticationFailed: a valid simulator access token is required.",
	"403": "AuthorizationFailed for a subscription or subject other than the configured local owner.",
	"404": "ResourceNotFound, ResourceGroupNotFound or ParentResourceNotFound.",
	"409": "Conflict, QuotaExceeded, InvalidResourceLocation or an in-use reference.",
}

func (e *Engine) openAPI(p *boundProvider) map[string]any {
	paths := map[string]any{}
	schemas := map[string]any{}
	ids := make([]string, 0, len(p.ops))
	for id := range p.ops {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	description := "Bounded simulator subset of the official " + p.spec.Namespace + " api-version " + p.spec.APIVersion +
		" swagger. Every other official operation of this api-version returns HTTP 501 NotImplemented. " + strings.Join(p.spec.Bounds, " ")
	for _, id := range ids {
		b := p.ops[id]
		op := b.op
		parameters := []any{}
		for _, param := range op.Parameters {
			switch param.In {
			case "path":
				schema := map[string]any{"type": "string"}
				if param.Name == b.sub {
					schema["default"] = e.cfg.Subscription
				}
				parameters = append(parameters, map[string]any{"name": param.Name, "in": "path", "required": true, "schema": schema})
			case "query":
				if param.Name == "api-version" {
					parameters = append(parameters, map[string]any{"name": "api-version", "in": "query", "required": true, "schema": map[string]any{"type": "string", "enum": []any{p.spec.APIVersion}, "default": p.spec.APIVersion}})
					continue
				}
				if !b.accepted[strings.ToLower(param.Name)] {
					continue
				}
				kind := param.Type
				if kind == "" {
					kind = "string"
				}
				parameters = append(parameters, map[string]any{"name": param.Name, "in": "query", "required": param.Required, "schema": map[string]any{"type": kind}})
			}
		}
		responses := map[string]any{}
		for code, ref := range op.Responses {
			if code == "default" {
				continue
			}
			response := map[string]any{"description": "Official " + code + " response."}
			if ref != "" {
				response["content"] = map[string]any{"application/json": map[string]any{"schema": e.schema(p.model, ref, schemas)}}
			}
			responses[code] = response
		}
		for code, text := range errorResponses {
			if _, declared := responses[code]; !declared {
				responses[code] = map[string]any{"description": text}
			}
		}
		entry := map[string]any{"operationId": id, "tags": []any{p.spec.Title}, "summary": id, "parameters": parameters, "responses": responses}
		if op.LongRunning {
			entry["description"] = "Long-running in Azure; the simulator completes it synchronously with provisioningState Succeeded."
		}
		if b.body != nil {
			entry["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": e.schema(p.model, b.body.Schema, schemas)}}}
		}
		item, _ := paths[op.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[op.Path] = item
		}
		item[strings.ToLower(op.Method)] = entry
	}
	return map[string]any{
		"openapi":            "3.1.0",
		"info":               map[string]any{"title": p.spec.Title + " (" + p.spec.Namespace + ", simulator catalog)", "version": p.spec.APIVersion, "description": description},
		"servers":            []any{map[string]any{"url": e.cfg.BasePath, "description": "This simulator only"}},
		"tags":               []any{map[string]any{"name": p.spec.Title, "description": description}},
		"paths":              paths,
		"components":         map[string]any{"schemas": schemas},
		"x-simulator-source": p.model.Source,
	}
}

func (e *Engine) schema(m *Model, name string, schemas map[string]any) map[string]any {
	ref := map[string]any{"$ref": "#/components/schemas/" + name}
	if _, done := schemas[name]; done {
		return ref
	}
	schemas[name] = map[string]any{}
	schemas[name] = e.property(m, m.Definitions[name], schemas)
	return ref
}

func (e *Engine) property(m *Model, s *Schema, schemas map[string]any) map[string]any {
	if s.Ref != "" {
		ref := e.schema(m, s.Ref, schemas)
		if s.ReadOnly {
			return map[string]any{"allOf": []any{ref}, "readOnly": true}
		}
		return ref
	}
	out := map[string]any{}
	if s.Type != "" {
		out["type"] = s.Type
	}
	if s.Format != "" {
		out["format"] = s.Format
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	for key, value := range map[string]*int{"minLength": s.MinLength, "maxLength": s.MaxLength, "minItems": s.MinItems, "maxItems": s.MaxItems} {
		if value != nil {
			out[key] = *value
		}
	}
	for key, value := range map[string]*float64{"minimum": s.Minimum, "maximum": s.Maximum} {
		if value != nil {
			out[key] = json.Number(strconv.FormatFloat(*value, 'f', -1, 64))
		}
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}
	if s.ReadOnly {
		out["readOnly"] = true
	}
	if writeOnly(s) {
		out["writeOnly"] = true
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	if s.Items != nil {
		out["items"] = e.property(m, s.Items, schemas)
	}
	if s.additional != nil {
		out["additionalProperties"] = e.property(m, s.additional, schemas)
	} else if s.openMap {
		out["additionalProperties"] = true
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, child := range s.Properties {
			props[name] = e.property(m, child, schemas)
		}
		out["properties"] = props
	}
	if len(s.AllOf) > 0 {
		var parts []any
		for _, part := range s.AllOf {
			parts = append(parts, e.property(m, part, schemas))
		}
		out["allOf"] = parts
	}
	if s.Discriminator != "" {
		mapping := map[string]any{}
		for value, sub := range s.Subtypes {
			mapping[value] = "#/components/schemas/" + sub
			e.schema(m, sub, schemas)
		}
		out["discriminator"] = map[string]any{"propertyName": s.Discriminator, "mapping": mapping}
	}
	return out
}
