package catalog

import (
	"regexp"
	"sort"
	"strings"
)

var pathVariable = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// OpenAPI renders each reviewed API as an OpenAPI 3.1 document derived only
// from the embedded discovery subset.
func (e *Engine) OpenAPI() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, b := range e.order {
		out[b.model.Key] = b.openAPI()
	}
	return out
}

func (b *binding) openAPI() map[string]any {
	paths := map[string]any{}
	schemas := map[string]any{}
	ids := make([]string, 0, len(b.bindings))
	for id := range b.bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	description := "Bounded simulator subset of the official " + b.model.Title + " (" + b.model.Version + ") discovery document. Every other official method returns HTTP 501 UNIMPLEMENTED. " + strings.Join(b.api.Bounds, " ")
	for _, id := range ids {
		m := b.model.Methods[id]
		path := "/" + b.model.ServicePath + m.FlatPath
		parameters := []any{}
		for _, match := range pathVariable.FindAllStringSubmatch(m.FlatPath, -1) {
			parameters = append(parameters, map[string]any{"name": match[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		names := make([]string, 0, len(m.Parameters))
		for name, p := range m.Parameters {
			if p.Location == "query" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			p := m.Parameters[name]
			kind := p.Type
			if kind == "" {
				kind = "string"
			}
			parameters = append(parameters, map[string]any{"name": name, "in": "query", "required": p.Required, "schema": map[string]any{"type": kind}})
		}
		op := map[string]any{"operationId": b.model.Name + "." + id, "tags": []any{b.api.Title}, "summary": id, "parameters": parameters,
			"responses": map[string]any{"200": map[string]any{"description": "Stored resource, list or completed long-running operation."},
				"400": map[string]any{"description": "INVALID_ARGUMENT or FAILED_PRECONDITION google.rpc.Status."},
				"403": map[string]any{"description": "PERMISSION_DENIED for a project or subject other than the configured owner."},
				"404": map[string]any{"description": "NOT_FOUND."}, "409": map[string]any{"description": "ALREADY_EXISTS or ABORTED."}}}
		if m.Request != "" {
			op["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": b.schema(m.Request, schemas)}}}
		}
		if m.Response != "" {
			op["responses"].(map[string]any)["200"] = map[string]any{"description": "Successful response.", "content": map[string]any{"application/json": map[string]any{"schema": b.schema(m.Response, schemas)}}}
		}
		entry, _ := paths[path].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
			paths[path] = entry
		}
		entry[strings.ToLower(m.HTTPMethod)] = op
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": b.api.Title + " (simulator catalog)", "version": b.model.Version, "description": description},
		"servers": []any{map[string]any{"url": "/_simulator/providers/gcp/" + b.model.Name, "description": "This simulator only"}},
		"tags":    []any{map[string]any{"name": b.api.Title, "description": description}},
		"paths":   paths, "components": map[string]any{"schemas": schemas},
		"x-simulator-source": b.model.Source,
	}
}

func (b *binding) schema(name string, schemas map[string]any) map[string]any {
	ref := map[string]any{"$ref": "#/components/schemas/" + name}
	if _, done := schemas[name]; done {
		return ref
	}
	schemas[name] = map[string]any{}
	schemas[name] = b.property(b.model.Schemas[name], schemas)
	return ref
}

func (b *binding) property(p *Property, schemas map[string]any) map[string]any {
	if p.Ref != "" {
		return b.schema(p.Ref, schemas)
	}
	out := map[string]any{}
	switch p.Type {
	case "object":
		out["type"] = "object"
		if p.Properties != nil {
			props := map[string]any{}
			for name, child := range p.Properties {
				props[name] = b.property(child, schemas)
			}
			out["properties"] = props
			out["additionalProperties"] = false
		} else if p.Additional != nil {
			out["additionalProperties"] = b.property(p.Additional, schemas)
		}
	case "array":
		out["type"] = "array"
		out["items"] = b.property(p.Items, schemas)
	case "string", "integer", "number", "boolean":
		out["type"] = p.Type
		if p.Format == "int64" || p.Format == "uint64" {
			out["type"] = "string"
		}
		if p.Format != "" {
			out["format"] = p.Format
		}
		if len(p.Enum) > 0 {
			out["enum"] = p.Enum
		}
	default:
		out["description"] = "Any JSON value"
	}
	if p.ReadOnly {
		out["readOnly"] = true
	}
	return out
}
