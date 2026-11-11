package appengine

import (
	"regexp"
	"sort"
	"strings"
)

var pathVariable = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// Bounds summarize the local contract for the API reference and capabilities.
var Bounds = []string{
	"Methods follow the official App Engine Admin API v1 paths; long-running operations complete synchronously and stay readable through apps.operations.get/list.",
	"Only the configured project owner may call a method; this is not Google IAM. The application cannot be deleted, as in App Engine.",
	"Versions run python312 in the standard environment only. Deployment copies Python sources from Cloud Storage objects of the configured project (deployment.zip or deployment.files); main.py must define the WSGI callable app.",
	"Requests reach versions through the stored traffic split (IP, COOKIE or RANDOM) and firewall at /_simulator/providers/gcp/appspot/<hostname>/; each request runs once in a fresh sandbox with a 10 second deadline and no network.",
}

// OpenAPI renders the reviewed methods as an OpenAPI 3.1 document derived
// only from the embedded discovery subset.
func (e *Engine) OpenAPI() map[string]any {
	paths, schemas := map[string]any{}, map[string]any{}
	description := "Bounded native simulator subset of the official App Engine Admin API (v1) discovery document. Every other official method returns HTTP 501 UNIMPLEMENTED. " + strings.Join(Bounds, " ")
	ids := e.Methods()
	sort.Strings(ids)
	for _, id := range ids {
		m := e.model.Methods[id]
		route := e.model.Routes[id]
		parameters := []any{}
		for _, match := range pathVariable.FindAllStringSubmatch(route.FlatPath, -1) {
			parameters = append(parameters, map[string]any{"name": match[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		names := []string{}
		for name, p := range m.Parameters {
			if p.Location == "query" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			kind := m.Parameters[name].Type
			if kind == "" {
				kind = "string"
			}
			parameters = append(parameters, map[string]any{"name": name, "in": "query", "required": m.Parameters[name].Required, "schema": map[string]any{"type": kind}})
		}
		responses := map[string]any{
			"400": map[string]any{"description": "INVALID_ARGUMENT or FAILED_PRECONDITION google.rpc.Status."},
			"403": map[string]any{"description": "PERMISSION_DENIED for a project or subject other than the configured owner."},
			"404": map[string]any{"description": "NOT_FOUND."}, "409": map[string]any{"description": "ALREADY_EXISTS."},
			"200": map[string]any{"description": "Successful response.", "content": map[string]any{"application/json": map[string]any{"schema": e.schema(m.Response, schemas)}}},
		}
		op := map[string]any{"operationId": "appengine." + id, "tags": []any{"App Engine"}, "summary": id, "parameters": parameters, "responses": responses}
		if m.Request != "" {
			op["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": e.schema(m.Request, schemas)}}}
		}
		path := "/" + route.FlatPath
		entry, _ := paths[path].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
			paths[path] = entry
		}
		entry[strings.ToLower(route.HTTPMethod)] = op
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "App Engine Admin API (simulator)", "version": e.model.Version, "description": description},
		"servers": []any{map[string]any{"url": "/_simulator/providers/gcp/appengine", "description": "This simulator only"}},
		"tags":    []any{map[string]any{"name": "App Engine", "description": description}},
		"paths":   paths, "components": map[string]any{"schemas": schemas},
		"x-simulator-source": e.model.Source,
	}
}

func (e *Engine) schema(name string, schemas map[string]any) map[string]any {
	ref := map[string]any{"$ref": "#/components/schemas/" + name}
	if _, done := schemas[name]; done {
		return ref
	}
	schemas[name] = map[string]any{}
	schemas[name] = e.property(e.model.Schemas[name], schemas)
	return ref
}

func (e *Engine) property(p *property, schemas map[string]any) map[string]any {
	if p.Ref != "" {
		return e.schema(p.Ref, schemas)
	}
	out := map[string]any{}
	switch p.Type {
	case "object":
		out["type"] = "object"
		if p.Properties != nil {
			props := map[string]any{}
			for name, child := range p.Properties {
				props[name] = e.property(child, schemas)
			}
			out["properties"] = props
			out["additionalProperties"] = false
		} else if p.Additional != nil {
			out["additionalProperties"] = e.property(p.Additional, schemas)
		}
	case "array":
		out["type"] = "array"
		out["items"] = e.property(p.Items, schemas)
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
