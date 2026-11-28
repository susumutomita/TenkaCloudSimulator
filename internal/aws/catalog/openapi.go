package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// OpenAPI renders one service's implemented operations as an OpenAPI 3.1
// document derived from the embedded official shapes and reviewed local bounds. JSON protocol
// operations share POST / and are distinguished by the X-Amz-Target header;
// Query protocol operations share a form-encoded POST / distinguished by
// Action and answer in XML.
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
	if m.Metadata.Protocol == "query" {
		paths = b.queryPaths(ops, schemas, tag)
	} else if m.Metadata.Protocol == "json" {
		targets := []any{}
		examples := map[string]any{}
		for _, op := range ops {
			official := m.Operations[op.Name]
			target := m.Metadata.TargetPrefix + "." + op.Name
			targets = append(targets, target)
			body := op.Fixed
			if body == nil {
				body = map[string]any{}
			}
			examples[op.Name] = map[string]any{"summary": "X-Amz-Target: " + target, "value": body}
			if official.Input != "" {
				b.schema(official.Input, schemas, 0)
			}
			if official.Output != "" {
				b.schema(official.Output, schemas, 0)
			}
		}
		media := "application/x-amz-json-" + m.Metadata.JSONVersion
		responses := map[string]any{
			"200": map[string]any{"description": "Projected official output shape.", "content": map[string]any{media: map[string]any{"schema": map[string]any{"type": "object"}}}},
			"400": map[string]any{"description": "Modeled exception in __type."},
			"403": map[string]any{"description": "AccessDeniedException from local identity policy evaluation."},
			"501": map[string]any{"description": "Official operation not implemented by the simulator."},
		}
		if b.spec.Model == "service-quotas" {
			delete(responses, "403")
			responses["400"] = map[string]any{"description": "Modeled Service Quotas exception in __type, including AccessDeniedException, IllegalArgumentException, NoSuchResourceException, InvalidPaginationTokenException, ResourceAlreadyExistsException and QuotaExceededException."}
			responses["500"] = map[string]any{"description": "ServiceException when the local quota operation cannot complete."}
		}
		paths["/"] = map[string]any{"post": map[string]any{
			"operationId": b.spec.Model + "JSONOperation", "tags": []any{tag}, "summary": tag + " JSON " + m.Metadata.JSONVersion + " operations",
			"description": "Choose the X-Amz-Target of one implemented operation. Request and response members follow the linked official input/output schemas.",
			"parameters":  []any{map[string]any{"name": "X-Amz-Target", "in": "header", "required": true, "schema": map[string]any{"type": "string", "enum": targets}}},
			"requestBody": map[string]any{"required": true, "content": map[string]any{media: map[string]any{"schema": map[string]any{"type": "object"}, "examples": examples}}},
			"responses":   responses,
		}}
	} else {
		service := b.signingName()
		routeSchema := map[string]any{"type": "string", "enum": []any{service}}
		routeHeader := map[string]any{"name": "X-Simulator-Service", "in": "header", "required": false,
			"description": "Routes an unsigned first-party request; SDKs use the SigV4 credential scope instead.", "schema": routeSchema}
		if b.spec.Model == "kafka" || b.spec.Model == "sesv2" {
			// Unsigned requests need the signing service on these shared REST paths.
			routeHeader["required"] = true
			routeHeader["example"] = service
			routeSchema["default"] = service
		}
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
				"parameters": []any{routeHeader},
				"responses":  map[string]any{"200": map[string]any{"description": "Projected official output shape."}, "400": map[string]any{"description": "Modeled exception named by X-Amzn-ErrorType."}, "403": map[string]any{"description": "AccessDeniedException."}, "404": map[string]any{"description": "Not found or no matching official route."}}}
			if b.usesModeledForbidden(op.Name) {
				operation["responses"].(map[string]any)["403"] = map[string]any{"description": "ForbiddenException."}
			}
			if official.Input != "" {
				shape := m.shape(official.Input)
				params := operation["parameters"].([]any)
				body := map[string]any{"type": "object", "properties": map[string]any{}}
				var required []any
				var payload map[string]any
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
						// A payload member is the whole HTTP body, not an envelope key.
						if name == shape.Payload {
							payload = b.payloadSchema(member.Shape, schemas)
							continue
						}
						body["properties"].(map[string]any)[wire] = b.schema(member.Shape, schemas, 0)
						for _, r := range shape.Required {
							if r == name {
								required = append(required, wire)
							}
						}
					}
				}
				operation["parameters"] = params
				if payload != nil {
					operation["requestBody"] = map[string]any{"content": payload}
				} else if len(body["properties"].(map[string]any)) > 0 {
					if required != nil {
						body["required"] = required
					}
					media := map[string]any{"schema": body}
					if b.spec.Model == "sesv2" && op.Name == "SendEmail" {
						operation["description"] = "Verify the local owned.test domain in this region first. Accepted messages addressed to inbox@inbox.ses.test can be downloaded from the team operator's local inbox."
						media["example"] = map[string]any{
							"FromEmailAddress": "sender@owned.test",
							"Destination":      map[string]any{"ToAddresses": []string{"inbox@inbox.ses.test"}},
							"Content": map[string]any{"Simple": map[string]any{
								"Subject": map[string]string{"Data": "Local greeting"},
								"Body":    map[string]any{"Text": map[string]string{"Data": "Hello from the local inbox."}},
							}},
						}
					}
					operation["requestBody"] = map[string]any{"content": map[string]any{"application/json": media}}
				}
			}
			if official.Output != "" {
				operation["responses"].(map[string]any)["200"] = map[string]any{"description": "Projected official output shape.", "content": b.outputContent(official.Output, schemas)}
			}
			entry[strings.ToLower(official.HTTP.Method)] = operation
		}
	}
	if b.spec.Model == "kinesisvideo" {
		addKinesisVideoMediaReference(paths, tag)
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
		// Properties use the serialized (locationName) keys clients send;
		// Query responses use the XML element names.
		for _, member := range shape.sortedMembers() {
			ref := shape.Members[member]
			key := ref.wire(member)
			if b.model.Metadata.Protocol != "query" {
				props[key] = b.schema(ref.Shape, schemas, depth+1)
				continue
			}
			key = b.model.memberKey(member, ref)
			property := b.schema(ref.Shape, schemas, depth+1)
			// A flattened list (member- or shape-level) repeats its
			// elements directly inside the structure under this key.
			if b.model.shape(ref.Shape).Type == "list" && b.model.flattened(&ref) {
				property["xml"] = map[string]any{"wrapped": false}
				property["items"].(map[string]any)["xml"] = map[string]any{"name": key}
			}
			props[key] = property
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
		if b.model.Metadata.Protocol == "query" {
			items := map[string]any{"xml": map[string]any{"name": b.model.serialName(shape.Member, "member")}}
			for k, v := range out["items"].(map[string]any) {
				items[k] = v
			}
			out["items"], out["xml"] = items, map[string]any{"wrapped": true}
		}
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
		if b.model.Metadata.Protocol == "query" {
			return map[string]any{"type": "string", "format": "date-time"}
		}
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

// payloadSchema describes a payload member as the entire HTTP body.
func (b *binding) payloadSchema(shapeName string, schemas map[string]any) map[string]any {
	switch b.model.shape(shapeName).Type {
	case "blob":
		return map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}
	case "string":
		return map[string]any{"text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}
	}
	return map[string]any{"application/json": map[string]any{"schema": b.schema(shapeName, schemas, 0)}}
}

// outputContent describes a REST response body: the payload member's shape
// when the output declares one, otherwise only the members carried in the
// body (header and status-code members are not body keys).
func (b *binding) outputContent(shapeName string, schemas map[string]any) map[string]any {
	shape := b.model.shape(shapeName)
	if shape.Payload != "" {
		return b.payloadSchema(shape.Members[shape.Payload].Shape, schemas)
	}
	props := map[string]any{}
	for _, name := range shape.sortedMembers() {
		if member := shape.Members[name]; member.Location == "" || member.Location == "body" {
			props[member.wire(name)] = b.schema(member.Shape, schemas, 1)
		}
	}
	return map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": props}}}
}

// queryPaths describes the Query endpoint with one entry per Action so each
// is individually runnable. Every Action is the same form-encoded POST /;
// the path fragment "#Action=<Name>" only distinguishes them in the
// document and is never sent. Fields follow botocore's Query serializer and
// responses are XML <Action>Response documents or an XML ErrorResponse.
func (b *binding) queryPaths(ops []Op, schemas map[string]any, tag string) map[string]map[string]any {
	m := b.model
	namespace := xmlNamespace(m)
	schemas["ResponseMetadata"] = map[string]any{"type": "object", "properties": map[string]any{"RequestId": map[string]any{"type": "string"}}}
	schemas["ErrorResponse"] = map[string]any{"type": "object", "xml": map[string]any{"name": "ErrorResponse", "namespace": namespace}, "properties": map[string]any{
		"Error": map[string]any{"type": "object", "properties": map[string]any{
			"Type": map[string]any{"type": "string", "enum": []any{"Sender", "Receiver"}}, "Code": map[string]any{"type": "string"}, "Message": map[string]any{"type": "string"}}},
		"RequestId": map[string]any{"type": "string"}}}
	failure := func(description string) map[string]any {
		return map[string]any{"description": description, "content": map[string]any{"text/xml": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/ErrorResponse"}}}}
	}
	service := b.signingName()
	paths := map[string]map[string]any{}
	for _, op := range ops {
		official := m.Operations[op.Name]
		form, example := b.formSchema(op.Name, official), b.formExample(op.Name, official)
		var localHelp string
		if b.spec.Model == "redshift" && op.Name == "DescribeTags" {
			localHelp = redshiftDescribeTagsReference(form, example)
		}
		schemas[op.Name+"Request"] = form
		properties := map[string]any{"ResponseMetadata": map[string]any{"$ref": "#/components/schemas/ResponseMetadata"}}
		response := map[string]any{"type": "object", "title": op.Name + "Response", "properties": properties,
			"xml": map[string]any{"name": op.Name + "Response", "namespace": namespace}}
		if official.Output != "" {
			b.schema(official.Output, schemas, 0)
			if official.ResultWrapper != "" {
				properties[official.ResultWrapper] = map[string]any{"$ref": "#/components/schemas/" + official.Output, "xml": map[string]any{"name": official.ResultWrapper}}
			} else {
				// Without a result wrapper the output members sit directly
				// in <Action>Response, as writeQueryOutput writes them.
				response["allOf"] = []any{map[string]any{"$ref": "#/components/schemas/" + official.Output}}
			}
		}
		schemas[op.Name+"Response"] = response
		holds := "ResponseMetadata/RequestId"
		if official.ResultWrapper != "" {
			holds = "the " + official.ResultWrapper + " element and " + holds
		}
		paths["/#Action="+op.Name] = map[string]any{"post": map[string]any{
			"operationId": b.spec.Model + op.Name, "tags": []any{tag}, "summary": op.Name,
			"description": "POST / with a form-encoded body (the #Action fragment only names this entry and is not sent). Form fields follow the official Query serialization: structure members joined with \".\", list elements numbered from 1 under their element name (for example Tags.Tag.1.Key), map entries as entry.N.key and entry.N.value, and an empty list as an empty field. " +
				"The response is an XML " + op.Name + "Response holding " + holds + "; errors are ErrorResponse/Error/Code with the official error code." + localHelp,
			"parameters": []any{map[string]any{"name": "X-Simulator-Service", "in": "header", "required": true,
				"description": "Routes this unsigned first-party request to " + b.spec.Title + "; without it an unsigned form POST is not routed here. SDKs use the SigV4 credential scope instead.",
				"schema":      map[string]any{"type": "string", "enum": []any{service}, "default": service}, "example": service}},
			"requestBody": map[string]any{"required": true, "content": map[string]any{"application/x-www-form-urlencoded": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/" + op.Name + "Request"}, "example": example}}},
			"responses": map[string]any{
				"200": map[string]any{"description": "XML " + op.Name + "Response with the projected official output shape.", "content": map[string]any{"text/xml": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + op.Name + "Response"}}}},
				"400": failure("Modeled fault or common Query error (InvalidParameterValue, InvalidAction, MissingAction, MissingParameter, MalformedQueryString)."),
				"403": failure("AccessDenied from local identity policy evaluation."),
				"404": failure("Modeled not-found fault."),
				"501": failure("UnknownOperationException: official operation not implemented by the simulator."),
			},
		}}
	}
	return paths
}

// formExample fills Action, Version and each required top-level scalar
// with a placeholder; list and structure members are added by the reader.
func (b *binding) formExample(action string, official *Operation) map[string]any {
	example := map[string]any{"Action": action, "Version": b.model.Metadata.APIVersion}
	if official.Input == "" {
		return example
	}
	shape := b.model.shape(official.Input)
	for _, name := range shape.Required {
		member := shape.Members[name]
		target := b.model.shape(member.Shape)
		switch {
		case len(target.Enum) > 0:
			example[b.model.memberKey(name, member)] = target.Enum[0]
		case target.Type == "string":
			example[b.model.memberKey(name, member)] = "example"
		case target.Type == "integer" || target.Type == "long":
			example[b.model.memberKey(name, member)] = "1"
		case target.Type == "boolean":
			example[b.model.memberKey(name, member)] = "false"
		}
	}
	return example
}

// formSchema describes one Action's form fields. Scalars at the top level
// are named properties; nested and numbered fields are pattern properties.
func (b *binding) formSchema(action string, official *Operation) map[string]any {
	props := map[string]any{
		"Action":  map[string]any{"type": "string", "const": action},
		"Version": map[string]any{"type": "string", "const": b.model.Metadata.APIVersion},
	}
	patterns := map[string]any{}
	required := []any{"Action", "Version"}
	var nested []string
	if official.Input != "" {
		shape := b.model.shape(official.Input)
		for _, name := range shape.sortedMembers() {
			member := shape.Members[name]
			key := b.model.memberKey(name, member)
			b.memberFields(name, member, "", "", props, patterns, 0)
			if slices.Contains(shape.Required, name) {
				if _, scalar := props[key]; scalar && b.model.shape(member.Shape).Type != "list" {
					required = append(required, key)
				} else {
					nested = append(nested, key)
				}
			}
		}
	}
	out := map[string]any{"type": "object", "title": action, "properties": props, "required": required, "additionalProperties": false}
	if len(patterns) > 0 {
		out["patternProperties"] = patterns
	}
	if len(nested) > 0 {
		out["description"] = "Required list or structure members: " + strings.Join(nested, ", ") + "."
	}
	return out
}

// memberFields registers one structure member's fields below a prefix
// (empty at the top level). A renamed flattened list sends its elements
// under the element name but an empty list under the member's own name.
func (b *binding) memberFields(name string, member Member, literal, pattern string, props, patterns map[string]any, depth int) {
	join := func(key string) (string, string) {
		if pattern == "" {
			return key, regexp.QuoteMeta(key)
		}
		next := ""
		if literal != "" {
			next = literal + "." + key
		}
		return next, pattern + `\.` + regexp.QuoteMeta(key)
	}
	key, own := b.model.memberKey(name, member), b.model.serialName(&member, name)
	keyLiteral, keyPattern := join(key)
	b.formFields(member.Shape, b.model.flattened(&member), keyLiteral, keyPattern, props, patterns, depth)
	if own == key {
		return
	}
	ownLiteral, ownPattern := join(own)
	empty := map[string]any{"type": "string", "const": "", "description": "An empty list."}
	if keyLiteral != "" {
		delete(props, keyLiteral)
		props[ownLiteral] = empty
	} else {
		delete(patterns, "^"+keyPattern+"$")
		patterns["^"+ownPattern+"$"] = empty
	}
}

// formFields registers the form fields of one shape at a dotted prefix.
// literal is the exact field name while no list index has been added.
func (b *binding) formFields(shapeName string, flat bool, literal, pattern string, props, patterns map[string]any, depth int) {
	if depth > 8 {
		return
	}
	shape := b.model.shape(shapeName)
	field := func(schema map[string]any) {
		if literal != "" {
			props[literal] = schema
		} else {
			patterns["^"+pattern+"$"] = schema
		}
	}
	index := `\.[1-9][0-9]*`
	switch shape.Type {
	case "structure":
		for _, name := range shape.sortedMembers() {
			b.memberFields(name, shape.Members[name], literal, pattern, props, patterns, depth+1)
		}
	case "list":
		field(map[string]any{"type": "string", "const": "", "description": "An empty list."})
		prefix := pattern
		if !flat {
			prefix += `\.` + regexp.QuoteMeta(b.model.serialName(shape.Member, "member"))
		}
		b.formFields(shape.Member.Shape, b.model.flattened(shape.Member), "", prefix+index, props, patterns, depth+1)
	case "map":
		prefix := pattern
		if !flat {
			prefix += `\.entry`
		}
		prefix += index + `\.`
		patterns["^"+prefix+regexp.QuoteMeta(b.model.serialName(shape.Key, "key"))+"$"] = b.schema(shape.Key.Shape, map[string]any{}, 0)
		b.formFields(shape.Value.Shape, b.model.flattened(shape.Value), "", prefix+regexp.QuoteMeta(b.model.serialName(shape.Value, "value")), props, patterns, depth+1)
	default:
		field(b.schema(shapeName, map[string]any{}, 0))
	}
}
