package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// OpenAPI renders one service's implemented operations as an OpenAPI 3.1
// document derived only from the embedded official shapes. JSON protocol
// operations share POST / and are distinguished by the X-Amz-Target header.
func (e *Engine) OpenAPI() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, b := range e.bindings {
		out[b.spec.Model] = b.openAPI()
	}
	return out
}

func (b *binding) openAPI() map[string]any {
	m := b.model
	schemas := map[string]any{}
	paths := map[string]map[string]any{}
	ops := append([]Op(nil), b.spec.Ops...)
	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	tag := b.spec.Title
	description := "Bounded simulator subset of the official " + m.Metadata.FullName + " API (" + m.Metadata.APIVersion + "). " +
		"Every other official operation returns HTTP 501 UnknownOperationException. " + strings.Join(b.spec.Bounds, " ")
	if m.Metadata.Protocol == "json" {
		targets := []any{}
		examples := map[string]any{}
		for _, op := range ops {
			official := m.Operations[op.Name]
			target := m.Metadata.TargetPrefix + "." + op.Name
			targets = append(targets, target)
			examples[op.Name] = map[string]any{"summary": "X-Amz-Target: " + target, "value": map[string]any{}}
			if official.Input != "" {
				b.schema(official.Input, schemas, 0)
			}
			if official.Output != "" {
				b.schema(official.Output, schemas, 0)
			}
		}
		media := "application/x-amz-json-" + m.Metadata.JSONVersion
		paths["/"] = map[string]any{"post": map[string]any{
			"operationId": b.spec.Model + "JSONOperation", "tags": []any{tag}, "summary": tag + " JSON " + m.Metadata.JSONVersion + " operations",
			"description": "Choose the X-Amz-Target of one implemented operation. Request and response members follow the linked official input/output schemas.",
			"parameters":  []any{map[string]any{"name": "X-Amz-Target", "in": "header", "required": true, "schema": map[string]any{"type": "string", "enum": targets}}},
			"requestBody": map[string]any{"required": true, "content": map[string]any{media: map[string]any{"schema": map[string]any{"type": "object"}, "examples": examples}}},
			"responses": map[string]any{
				"200": map[string]any{"description": "Projected official output shape.", "content": map[string]any{media: map[string]any{"schema": map[string]any{"type": "object"}}}},
				"400": map[string]any{"description": "Modeled exception in __type."},
				"403": map[string]any{"description": "AccessDeniedException from local identity policy evaluation."},
				"501": map[string]any{"description": "Official operation not implemented by the simulator."},
			},
		}}
	} else {
		for _, op := range ops {
			official := m.Operations[op.Name]
			pathPart, _, _ := strings.Cut(official.HTTP.RequestURI, "?")
			pathPart = strings.ReplaceAll(pathPart, "+}", "}")
			entry := paths[pathPart]
			if entry == nil {
				entry = map[string]any{}
				paths[pathPart] = entry
			}
			operation := map[string]any{"operationId": b.spec.Model + op.Name, "tags": []any{tag}, "summary": op.Name,
				"parameters": []any{map[string]any{"name": "X-Simulator-Service", "in": "header", "required": false, "description": "Routes an unsigned first-party request; SDKs use the SigV4 credential scope instead.", "schema": map[string]any{"type": "string", "enum": []any{b.signingName()}}}},
				"responses":  map[string]any{"200": map[string]any{"description": "Projected official output shape."}, "400": map[string]any{"description": "Modeled exception named by X-Amzn-ErrorType."}, "403": map[string]any{"description": "AccessDeniedException."}, "404": map[string]any{"description": "Not found or no matching official route."}}}
			if official.Input != "" {
				shape := m.shape(official.Input)
				params := operation["parameters"].([]any)
				body := map[string]any{"type": "object", "properties": map[string]any{}}
				var required []any
				for _, name := range shape.sortedMembers() {
					member := shape.Members[name]
					wire := member.LocationName
					if wire == "" {
						wire = name
					}
					switch member.Location {
					case "uri":
						params = append(params, map[string]any{"name": strings.TrimSuffix(wire, "+"), "in": "path", "required": true, "schema": b.schema(member.Shape, schemas, 0)})
					case "querystring":
						params = append(params, map[string]any{"name": wire, "in": "query", "schema": b.schema(member.Shape, schemas, 0)})
					case "header":
						params = append(params, map[string]any{"name": wire, "in": "header", "schema": b.schema(member.Shape, schemas, 0)})
					case "", "body":
						body["properties"].(map[string]any)[wire] = b.schema(member.Shape, schemas, 0)
						for _, r := range shape.Required {
							if r == name {
								required = append(required, wire)
							}
						}
					}
				}
				operation["parameters"] = params
				if len(body["properties"].(map[string]any)) > 0 {
					if required != nil {
						body["required"] = required
					}
					operation["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": body}}}
				}
			}
			if official.Output != "" {
				operation["responses"].(map[string]any)["200"] = map[string]any{"description": "Projected official output shape.",
					"content": map[string]any{"application/json": map[string]any{"schema": b.schema(official.Output, schemas, 0)}}}
			}
			entry[strings.ToLower(official.HTTP.Method)] = operation
		}
	}
	pathsAny := map[string]any{}
	for k, v := range paths {
		pathsAny[k] = v
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": b.spec.Title + " (simulator catalog)", "version": m.Metadata.APIVersion, "description": description},
		"servers": []any{map[string]any{"url": "/", "description": "This simulator only"}},
		"tags":    []any{map[string]any{"name": tag, "description": description}},
		"paths":   pathsAny, "components": map[string]any{"schemas": schemas},
		"x-simulator-source": m.Source,
	}
}

// schema converts an official shape to JSON Schema, registering named
// structures as components so recursive shapes stay finite.
func (b *binding) schema(name string, schemas map[string]any, depth int) map[string]any {
	shape := b.model.shape(name)
	switch shape.Type {
	case "structure":
		ref := map[string]any{"$ref": "#/components/schemas/" + name}
		if _, done := schemas[name]; done {
			return ref
		}
		if shape.Document {
			schemas[name] = map[string]any{"description": "Arbitrary JSON document"}
			return ref
		}
		props := map[string]any{}
		schemas[name] = map[string]any{"type": "object"}
		// Properties use the serialized (locationName) keys clients send.
		for _, member := range shape.sortedMembers() {
			props[shape.Members[member].wire(member)] = b.schema(shape.Members[member].Shape, schemas, depth+1)
		}
		entry := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(shape.Required) > 0 {
			required := []any{}
			for _, r := range shape.Required {
				if member, ok := shape.Members[r]; ok {
					r = member.wire(r)
				}
				required = append(required, r)
			}
			entry["required"] = required
		}
		if shape.Union {
			entry["minProperties"], entry["maxProperties"] = 1, 1
		}
		schemas[name] = entry
		return ref
	case "list":
		out := map[string]any{"type": "array", "items": b.schema(shape.Member.Shape, schemas, depth+1)}
		bounds(out, shape, "minItems", "maxItems")
		return out
	case "map":
		out := map[string]any{"type": "object", "additionalProperties": b.schema(shape.Value.Shape, schemas, depth+1)}
		bounds(out, shape, "minProperties", "maxProperties")
		return out
	case "string":
		out := map[string]any{"type": "string"}
		if len(shape.Enum) > 0 {
			enum := []any{}
			for _, v := range shape.Enum {
				enum = append(enum, v)
			}
			out["enum"] = enum
		}
		bounds(out, shape, "minLength", "maxLength")
		return out
	case "integer", "long":
		out := map[string]any{"type": "integer"}
		bounds(out, shape, "minimum", "maximum")
		return out
	case "float", "double":
		out := map[string]any{"type": "number"}
		bounds(out, shape, "minimum", "maximum")
		return out
	case "boolean":
		return map[string]any{"type": "boolean"}
	case "timestamp":
		return map[string]any{"description": "Epoch seconds in JSON protocols", "type": []any{"number", "string"}}
	case "blob":
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	}
	return map[string]any{"description": fmt.Sprintf("%s shape (not accepted by the simulator)", shape.Type)}
}

func bounds(out map[string]any, shape *Shape, minKey, maxKey string) {
	if shape.Min != nil {
		out[minKey] = *shape.Min
	}
	if shape.Max != nil {
		out[maxKey] = *shape.Max
	}
}
