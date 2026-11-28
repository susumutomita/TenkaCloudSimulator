package catalog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxRequestBytes = 256 << 10

// route is one parsed official REST binding.
type route struct {
	op       string
	method   string
	segments []string // literal, "{Label}" or "{Label+}"
	query    map[string]string
	literals int
}

func parseRoute(op string, h HTTP) route {
	pathPart, queryPart, _ := strings.Cut(h.RequestURI, "?")
	r := route{op: op, method: h.Method, query: map[string]string{}}
	for _, segment := range strings.Split(strings.Trim(pathPart, "/"), "/") {
		if segment == "" {
			continue
		}
		r.segments = append(r.segments, segment)
		if !strings.HasPrefix(segment, "{") {
			r.literals++
		}
	}
	// An official trailing slash is part of the path: "/tags/" ≠ "/tags".
	if len(pathPart) > 1 && strings.HasSuffix(pathPart, "/") {
		r.segments = append(r.segments, "")
	}
	if queryPart != "" {
		for _, item := range strings.Split(queryPart, "&") {
			key, value, _ := strings.Cut(item, "=")
			r.query[key] = value
			r.literals++
		}
	}
	return r
}

// match returns decoded label values when the request path fits the route.
func (r route) match(method string, escaped []string, query url.Values) (map[string]string, bool) {
	if method != r.method {
		return nil, false
	}
	for key, value := range r.query {
		values, ok := query[key]
		if !ok || value != "" && (len(values) != 1 || values[0] != value) {
			return nil, false
		}
	}
	labels := map[string]string{}
	i := 0
	for si, segment := range r.segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "+}") {
			// A greedy label takes every segment except those the rest of
			// the route needs, so "/portals/{arn+}/browserSettings" matches.
			suffix := r.segments[si+1:]
			end := len(escaped) - len(suffix)
			if end <= i {
				return nil, false
			}
			decoded, err := url.PathUnescape(strings.Join(escaped[i:end], "/"))
			if err != nil || decoded == "" {
				return nil, false
			}
			labels[strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "+}")] = decoded
			tail := route{method: r.method, segments: suffix}
			rest, ok := tail.match(method, escaped[end:], nil)
			if !ok {
				return nil, false
			}
			for k, v := range rest {
				labels[k] = v
			}
			return labels, true
		}
		if i >= len(escaped) {
			return nil, false
		}
		decoded, err := url.PathUnescape(escaped[i])
		if err != nil {
			return nil, false
		}
		if strings.HasPrefix(segment, "{") {
			if decoded == "" {
				return nil, false
			}
			labels[strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")] = decoded
		} else if decoded != segment {
			return nil, false
		}
		i++
	}
	return labels, i == len(escaped)
}

// splitEscapedPath keeps empty segments so "/a//b" never matches "/a/b".
func splitEscapedPath(r *http.Request) []string {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if escaped == "" {
		return nil
	}
	return strings.Split(escaped, "/")
}

// readBody reads a bounded request body.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var buffer bytes.Buffer
	reader := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if _, err := buffer.ReadFrom(reader); err != nil {
		return nil, invalid("Catalog requests are limited to 256 KiB.")
	}
	return buffer.Bytes(), nil
}

func decodeJSONObject(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil || decoder.More() {
		return nil, &fault{kind: "serialization", message: "Expected one JSON request object."}
	}
	return object, nil
}

// stringScalar converts an HTTP-bound string into a JSON-like raw value.
func stringScalar(model *Model, shapeName, text, location string) any {
	shape := model.shape(shapeName)
	switch shape.Type {
	case "integer", "long", "float", "double":
		return json.Number(text)
	case "boolean":
		switch text {
		case "true":
			return true
		case "false":
			return false
		}
		return text
	case "timestamp":
		if t, err := http.ParseTime(text); err == nil && location == "header" {
			return t.UTC().Format(time.RFC3339Nano)
		}
		return text
	case "list":
		return nil
	}
	return text
}

// restInput assembles a raw input tree from labels, query, headers and body.
func restInput(model *Model, op *Operation, r *http.Request, labels map[string]string, body []byte) (map[string]any, error) {
	raw := map[string]any{}
	if op.Input == "" {
		if len(bytes.TrimSpace(body)) > 0 {
			return nil, invalid("This operation does not accept a request body.")
		}
		return raw, nil
	}
	shape := model.shape(op.Input)
	query := r.URL.Query()
	consumedQuery := map[string]bool{}
	var bodyMembers []string
	for _, name := range shape.sortedMembers() {
		member := shape.Members[name]
		wire := member.LocationName
		if wire == "" {
			wire = name
		}
		switch member.Location {
		case "uri":
			if value, ok := labels[wire]; ok {
				raw[name] = stringScalar(model, member.Shape, value, "uri")
			}
		case "querystring":
			target := model.shape(member.Shape)
			values, ok := query[wire]
			if !ok {
				continue
			}
			consumedQuery[wire] = true
			if target.Type == "list" {
				items := make([]any, len(values))
				for i, v := range values {
					items[i] = stringScalar(model, target.Member.Shape, v, "querystring")
				}
				raw[name] = items
			} else if target.Type == "map" {
				return nil, &fault{kind: "unsupported", message: "Query-string maps are not supported by the simulator."}
			} else {
				if len(values) != 1 {
					return nil, invalid("Query parameter %s must appear once.", wire)
				}
				raw[name] = stringScalar(model, member.Shape, values[0], "querystring")
			}
		case "header":
			if value := r.Header.Get(wire); value != "" {
				target := model.shape(member.Shape)
				if target.Type == "list" {
					parts := strings.Split(value, ",")
					items := make([]any, len(parts))
					for i, part := range parts {
						items[i] = stringScalar(model, target.Member.Shape, strings.TrimSpace(part), "header")
					}
					raw[name] = items
				} else if member.JSONValue {
					decoded, err := base64.StdEncoding.DecodeString(value)
					if err != nil {
						return nil, invalid("Header %s must be base64 JSON.", wire)
					}
					raw[name] = string(decoded)
				} else {
					raw[name] = stringScalar(model, member.Shape, value, "header")
				}
			}
		case "headers":
			for header := range r.Header {
				if wire != "" && strings.HasPrefix(strings.ToLower(header), strings.ToLower(wire)) {
					return nil, &fault{kind: "unsupported", message: "Prefixed header maps are not supported by the simulator."}
				}
			}
		case "statusCode":
		default:
			bodyMembers = append(bodyMembers, name)
		}
	}
	for key := range query {
		if !consumedQuery[key] {
			if _, literal := literalQuery(op)[key]; !literal {
				return nil, invalid("Unknown query parameter %s.", key)
			}
		}
	}
	if shape.Payload != "" {
		member := shape.Members[shape.Payload]
		target := model.shape(member.Shape)
		if len(bytes.TrimSpace(body)) == 0 {
			return raw, nil
		}
		switch target.Type {
		case "structure":
			object, err := decodeJSONObject(body)
			if err != nil {
				return nil, err
			}
			raw[shape.Payload] = object
		case "string":
			raw[shape.Payload] = string(body)
		case "blob":
			raw[shape.Payload] = base64.StdEncoding.EncodeToString(body)
		default:
			return nil, &fault{kind: "unsupported", message: "This payload type is not supported by the simulator."}
		}
		return raw, nil
	}
	object, err := decodeJSONObject(body)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range bodyMembers {
		allowed[shape.Members[name].wire(name)] = true
	}
	for key, value := range object {
		if !allowed[key] {
			if _, member := shape.Members[key]; member {
				return nil, invalid("Member %s must be sent in its official HTTP location.", key)
			}
			return nil, invalid("Unknown member %s is not part of the official input shape.", key)
		}
		// The decoder maps official wire names back to member names.
		raw[key] = value
	}
	return raw, nil
}

func literalQuery(op *Operation) map[string]string {
	return parseRoute(op.Name, op.HTTP).query
}

// writeOutput serializes a projected output according to the protocol.
func writeOutput(w http.ResponseWriter, model *Model, op *Operation, output map[string]any) {
	protocol := model.Metadata.Protocol
	if protocol == "query" {
		writeQueryOutput(w, model, op, output)
		return
	}
	status := http.StatusOK
	if op.HTTP.ResponseCode != 0 {
		status = op.HTTP.ResponseCode
	}
	body := map[string]any{}
	var raw []byte
	if op.Output != "" {
		shape := model.shape(op.Output)
		for name, value := range output {
			member := shape.Members[name]
			if protocol != "rest-json" {
				body[member.wire(name)] = value
				continue
			}
			switch member.Location {
			case "header":
				wire := member.LocationName
				if wire == "" {
					wire = name
				}
				w.Header().Set(wire, fmt.Sprint(value))
			case "statusCode":
				if n, ok := value.(int64); ok && n >= 100 && n <= 599 {
					status = int(n)
				}
			default:
				if shape.Payload == name {
					if text, ok := value.(string); ok && model.shape(member.Shape).Type != "structure" {
						raw = []byte(text)
					} else {
						encoded, _ := json.Marshal(value)
						raw = encoded
					}
				} else {
					body[member.wire(name)] = value
				}
			}
		}
	}
	w.Header().Set("Content-Type", contentType(model))
	if raw == nil {
		raw, _ = json.Marshal(body)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// writeError serializes a typed error with its documented status.
func writeError(w http.ResponseWriter, model *Model, code string, status int, message string) {
	if model != nil && model.Metadata.Protocol == "query" {
		writeQueryError(w, model, code, status, message)
		return
	}
	body := map[string]any{"message": message}
	if model != nil {
		if shape := model.Shapes[code]; shape != nil {
			if member, upper := shape.Members["Message"]; upper {
				key := "Message"
				if model.Metadata.EndpointPrefix == "kafka" && member.LocationName != "" {
					key = member.LocationName
				}
				body = map[string]any{key: message}
			}
		}
	}
	w.Header().Set("X-Amzn-ErrorType", code)
	if model != nil && model.Metadata.Protocol == "json" {
		body["__type"] = code
	}
	w.Header().Set("Content-Type", contentType(model))
	raw, _ := json.Marshal(body)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// contentType returns a constant non-HTML media type for every response.
func contentType(model *Model) string {
	if model != nil && model.Metadata.Protocol == "json" {
		if model.Metadata.JSONVersion == "1.0" {
			return "application/x-amz-json-1.0"
		}
		return "application/x-amz-json-1.1"
	}
	return "application/json"
}

// The AWS Query protocol (botocore "query"): a form-encoded POST to / with
// Action and Version, answered with an XML <Action>Response document.

// queryCompatible refuses Query models that need wire behavior the engine
// does not implement, so such a service fails composition instead of
// silently mis-serializing: document shapes, XML attributes, HTTP-bound
// members and streams.
func queryCompatible(model *Model) error {
	names := make([]string, 0, len(model.Shapes))
	for name := range model.Shapes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		shape := model.Shapes[name]
		if shape.Document || shape.Streaming || shape.Event {
			return fmt.Errorf("query shape %s is a document or stream", name)
		}
		for member, ref := range shape.Members {
			if ref.XMLAttribute || ref.Location != "" || ref.Streaming || ref.EventPayload {
				return fmt.Errorf("query member %s.%s uses an unsupported XML or HTTP binding", name, member)
			}
		}
	}
	return nil
}

// queryBinding returns the Query catalog service a request addresses by its
// SigV4 credential scope (or X-Simulator-Service when unsigned).
func (e *Engine) queryBinding(r *http.Request) *binding {
	if r.Header.Get("X-Amz-Target") != "" {
		return nil
	}
	return e.byQuery[restService(r)]
}

// serveQuery resolves the Action of one Query request and runs it.
func (e *Engine) serveQuery(w http.ResponseWriter, r *http.Request, b *binding) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != http.MethodPost || r.URL.Path != "/" || r.URL.RawQuery != "" || media != "application/x-www-form-urlencoded" {
		writeError(w, b.model, "MalformedQueryString", 400, "Use a form-encoded POST to / without a query string for Query protocol operations.")
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		e.fail(w, b, nil, err)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeError(w, b.model, "MalformedQueryString", 400, "The request body is not valid form encoding.")
		return
	}
	actions := form["Action"]
	if len(actions) == 0 || actions[0] == "" {
		writeError(w, b.model, "MissingAction", 400, "The request must contain the parameter Action.")
		return
	}
	if len(actions) != 1 {
		writeError(w, b.model, "MalformedQueryString", 400, "The parameter Action must appear once.")
		return
	}
	name := actions[0]
	if _, official := b.model.Routes[name]; !official {
		writeError(w, b.model, "InvalidAction", 400, "Unknown operation "+name+".")
		return
	}
	op := b.ops[name]
	if op == nil {
		writeError(w, b.model, "UnknownOperationException", 501, "The simulator does not implement "+b.spec.Title+" "+name+".")
		return
	}
	switch versions := form["Version"]; {
	case len(versions) == 0:
		writeError(w, b.model, "MissingParameter", 400, "The request must contain the parameter Version.")
		return
	case len(versions) != 1 || versions[0] != b.model.Metadata.APIVersion:
		writeError(w, b.model, "InvalidParameterValue", 400, "Version must be "+b.model.Metadata.APIVersion+".")
		return
	}
	delete(form, "Action")
	delete(form, "Version")
	region, err := e.region(r, b)
	if err != nil {
		e.fail(w, b, op, err)
		return
	}
	raw, err := queryInput(b.model, b.model.Operations[name], form)
	if err != nil {
		e.fail(w, b, op, err)
		return
	}
	e.run(w, r, b, op, region, raw)
}

// Bounds on a Query form, checked before any tree is built. Each list or
// map level adds two segments, so a parameter name never needs more than
// maxFormSegments to reach maxDepth. These are local limits, not AWS quotas.
const (
	maxFormSegments = 2*maxDepth + 2
	maxFormNodes    = maxNodes
	maxFormNameText = 256
)

// formNode is one dotted segment of a form parameter name. Nodes keep only
// their own segment and parent; the dotted path is built for error text.
type formNode struct {
	parent   *formNode
	segment  string
	value    *string
	used     bool
	children map[string]*formNode
}

func (n *formNode) path() string {
	var parts []string
	for node := n; node != nil && node.parent != nil; node = node.parent {
		parts = append(parts, node.segment)
	}
	slices.Reverse(parts)
	return clip(strings.Join(parts, "."))
}

// clip bounds a client-supplied name echoed in an error message.
func clip(text string) string {
	if len(text) > maxFormNameText {
		return text[:maxFormNameText] + "..."
	}
	return text
}

// unused returns the first parameter, in name order, that no member consumed.
func (n *formNode) unused() string {
	if n.value != nil && !n.used {
		return n.path()
	}
	segments := make([]string, 0, len(n.children))
	for segment := range n.children {
		segments = append(segments, segment)
	}
	sort.Strings(segments)
	for _, segment := range segments {
		if path := n.children[segment].unused(); path != "" {
			return path
		}
	}
	return ""
}

// xmlCharacters reports whether XML 1.0 can carry text unchanged, so a
// stored value is never silently altered when a response echoes it.
func xmlCharacters(text string) bool {
	for _, r := range text {
		if !(r == 0x09 || r == 0x0A || r == 0x0D || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF) {
			return false
		}
	}
	return utf8.ValidString(text)
}

// queryInput assembles a raw input tree from form parameters the way
// botocore's Query serializer flattens them: structure members joined with
// ".", list elements as <member name>.N from 1 (or N directly when
// flattened), map entries as entry.N.key / entry.N.value, scalars as text and
// empty lists as an empty value. Every parameter must be consumed.
func queryInput(model *Model, op *Operation, form url.Values) (map[string]any, error) {
	root := &formNode{children: map[string]*formNode{}}
	keys := make([]string, 0, len(form))
	for key := range form {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	nodes := 0
	for _, key := range keys {
		if len(form[key]) != 1 {
			return nil, invalid("Parameter %s must appear once.", clip(key))
		}
		if strings.Count(key, ".") >= maxFormSegments {
			return nil, invalid("Parameter %s exceeds the simulator's bounded nesting.", clip(key))
		}
		if !xmlCharacters(key) || !xmlCharacters(form[key][0]) {
			return nil, invalid("Parameter %s contains characters XML 1.0 cannot carry.", clip(strings.ToValidUTF8(key, "?")))
		}
		node := root
		for _, segment := range strings.Split(key, ".") {
			if segment == "" {
				return nil, invalid("Malformed parameter name %q.", clip(key))
			}
			child := node.children[segment]
			if child == nil {
				if nodes++; nodes > maxFormNodes {
					return nil, invalid("The request has more parameters than the simulator's bounded size.")
				}
				child = &formNode{parent: node, segment: segment, children: map[string]*formNode{}}
				node.children[segment] = child
			}
			node = child
		}
		node.value = &form[key][0]
	}
	raw := map[string]any{}
	if op.Input != "" {
		d := &formDecoder{model: model}
		value, err := d.value(op.Input, root, false, 0)
		if err != nil {
			return nil, err
		}
		raw = value.(map[string]any)
	}
	if path := root.unused(); path != "" {
		return nil, invalid("Unknown parameter %s is not part of the official input shape.", path)
	}
	return raw, nil
}

type formDecoder struct {
	model *Model
}

// serialName is the Query/XML name of a shape reference: the reference's
// locationName, the target shape's locationName, or the default.
func (m *Model) serialName(ref *Member, fallback string) string {
	if ref.LocationName != "" {
		return ref.LocationName
	}
	if name := m.shape(ref.Shape).LocationName; name != "" {
		return name
	}
	return fallback
}

// flattened reports a list or map serialized without its wrapper element.
func (m *Model) flattened(ref *Member) bool {
	return ref.Flattened || m.shape(ref.Shape).Flattened
}

// memberKey is the Query parameter segment and XML element name of a
// structure member; a flattened list uses its element's name, as botocore
// does.
func (m *Model) memberKey(name string, ref Member) string {
	if target := m.shape(ref.Shape); target.Type == "list" && m.flattened(&ref) {
		if element := m.serialName(target.Member, ""); element != "" {
			return element
		}
	}
	return m.serialName(&ref, name)
}

// indexed returns the children of n numbered 1..N, refusing gaps, zero,
// leading zeros and non-numeric segments.
func indexed(n *formNode) ([]*formNode, error) {
	if n.value != nil {
		return nil, invalid("Parameter %s must not carry a value.", n.path())
	}
	items := make([]*formNode, len(n.children))
	for segment, child := range n.children {
		i, err := strconv.Atoi(segment)
		if err != nil || i < 1 || i > len(items) || strconv.Itoa(i) != segment {
			return nil, invalid("Parameter %s must be numbered consecutively from 1.", child.path())
		}
		items[i-1] = child
	}
	return items, nil
}

// value converts the form subtree n to a raw value of the named shape, or
// returns nil when n carries nothing this shape consumes.
func (d *formDecoder) value(shapeName string, n *formNode, flat bool, depth int) (any, error) {
	if depth > maxDepth {
		return nil, invalid("Request structure exceeds the simulator's bounded nesting at %s.", n.path())
	}
	shape := d.model.shape(shapeName)
	switch shape.Type {
	case "structure":
		if n.value != nil {
			return nil, invalid("Parameter %s names a structure and must not carry a value.", n.path())
		}
		out := map[string]any{}
		for _, name := range shape.sortedMembers() {
			member := shape.Members[name]
			key := d.model.memberKey(name, member)
			child := n.children[key]
			if own := d.model.serialName(&member, name); own != key {
				// botocore names a flattened list's elements after its
				// element but sends an empty one under the member's own name.
				if empty := n.children[own]; empty != nil {
					if child != nil || empty.value == nil || *empty.value != "" || len(empty.children) > 0 {
						return nil, invalid("Parameter %s must be an empty value for an empty list.", empty.path())
					}
					empty.used = true
					out[member.wire(name)] = []any{}
					continue
				}
				if child != nil && child.value != nil {
					return nil, invalid("Parameter %s names list elements and must not carry a value.", child.path())
				}
			}
			if child == nil {
				continue
			}
			value, err := d.value(member.Shape, child, d.model.flattened(&member), depth+1)
			if err != nil {
				return nil, err
			}
			if value != nil {
				out[member.wire(name)] = value
			}
		}
		return out, nil
	case "list":
		if n.value != nil && *n.value == "" && len(n.children) == 0 {
			n.used = true
			return []any{}, nil
		}
		container := n
		if !flat {
			if n.value != nil {
				return nil, invalid("Parameter %s names a list and must not carry a value.", n.path())
			}
			if container = n.children[d.model.serialName(shape.Member, "member")]; container == nil {
				return nil, nil
			}
		}
		items, err := indexed(container)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			value, err := d.value(shape.Member.Shape, item, d.model.flattened(shape.Member), depth+1)
			if err != nil {
				return nil, err
			}
			if value == nil {
				return nil, invalid("Parameter %s does not name a list element.", item.path())
			}
			out = append(out, value)
		}
		return out, nil
	case "map":
		container := n
		if !flat {
			if n.value != nil {
				return nil, invalid("Parameter %s names a map and must not carry a value.", n.path())
			}
			if container = n.children["entry"]; container == nil {
				return nil, nil
			}
		}
		entries, err := indexed(container)
		if err != nil {
			return nil, err
		}
		keyName, valueName := d.model.serialName(shape.Key, "key"), d.model.serialName(shape.Value, "value")
		out := make(map[string]any, len(entries))
		for _, entry := range entries {
			if entry.value != nil {
				return nil, invalid("Parameter %s names a map entry and must not carry a value.", entry.path())
			}
			keyNode, valueNode := entry.children[keyName], entry.children[valueName]
			// botocore sends nothing for an empty structure value.
			emptyStructure := valueNode == nil && d.model.shape(shape.Value.Shape).Type == "structure"
			if keyNode == nil || keyNode.value == nil || len(keyNode.children) > 0 || valueNode == nil && !emptyStructure {
				return nil, invalid("Map entry %s needs one %s and one %s.", entry.path(), keyName, valueName)
			}
			keyNode.used = true
			if _, duplicate := out[*keyNode.value]; duplicate {
				return nil, invalid("Map entry %s repeats key %q.", entry.path(), clip(*keyNode.value))
			}
			if emptyStructure {
				out[*keyNode.value] = map[string]any{}
				continue
			}
			value, err := d.value(shape.Value.Shape, valueNode, d.model.flattened(shape.Value), depth+1)
			if err != nil {
				return nil, err
			}
			if value == nil {
				return nil, invalid("Map entry %s has no value.", entry.path())
			}
			out[*keyNode.value] = value
		}
		return out, nil
	}
	if n.value == nil {
		return nil, nil
	}
	n.used = true
	return stringScalar(d.model, shapeName, *n.value, "query"), nil
}

// xmlNamespace is the service's official XML namespace.
func xmlNamespace(model *Model) string {
	switch ns := model.Metadata.XMLNamespace.(type) {
	case string:
		return ns
	case map[string]any:
		uri, _ := ns["uri"].(string)
		return uri
	}
	return ""
}

func xmlText(buffer *bytes.Buffer, text string) {
	_ = xml.EscapeText(buffer, []byte(text))
}

// queryRequestID identifies one Query response, as AWS RequestId values do.
func queryRequestID() string {
	id, _ := newID("uuid", "")
	return id
}

// writeQueryOutput serializes a projected output as <Action>Response with
// the operation's result wrapper and ResponseMetadata/RequestId.
func writeQueryOutput(w http.ResponseWriter, model *Model, op *Operation, output map[string]any) {
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	buffer.WriteString("<" + op.Name + "Response xmlns=\"")
	xmlText(&buffer, xmlNamespace(model))
	buffer.WriteString("\">")
	if op.Output != "" {
		shape := model.shape(op.Output)
		wire := map[string]any{}
		for name, value := range output {
			wire[shape.Members[name].wire(name)] = value
		}
		if op.ResultWrapper != "" {
			buffer.WriteString("<" + op.ResultWrapper + ">")
		}
		xmlMembers(&buffer, model, shape, wire)
		if op.ResultWrapper != "" {
			buffer.WriteString("</" + op.ResultWrapper + ">")
		}
	}
	id := queryRequestID()
	buffer.WriteString("<ResponseMetadata><RequestId>" + id + "</RequestId></ResponseMetadata></" + op.Name + "Response>")
	status := http.StatusOK
	if op.HTTP.ResponseCode != 0 {
		status = op.HTTP.ResponseCode
	}
	writeXML(w, status, id, buffer.Bytes())
}

func writeXML(w http.ResponseWriter, status int, id string, body []byte) {
	w.Header().Set("Content-Type", "text/xml")
	w.Header().Set("X-Amzn-RequestId", id)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// xmlMembers writes the members of a structure in name order; flattened
// lists and maps repeat their elements directly inside the structure.
func xmlMembers(buffer *bytes.Buffer, model *Model, shape *Shape, object map[string]any) {
	for _, name := range shape.sortedMembers() {
		member := shape.Members[name]
		value, ok := object[member.wire(name)]
		if !ok {
			continue
		}
		key := model.memberKey(name, member)
		target := model.shape(member.Shape)
		switch {
		case target.Type == "list" && model.flattened(&member):
			items, _ := value.([]any)
			for _, item := range items {
				xmlElement(buffer, model, key, target.Member.Shape, item)
			}
		case target.Type == "map" && model.flattened(&member):
			xmlEntries(buffer, model, key, target, value)
		default:
			xmlElement(buffer, model, key, member.Shape, value)
		}
	}
}

func xmlEntries(buffer *bytes.Buffer, model *Model, tag string, shape *Shape, value any) {
	object, _ := value.(map[string]any)
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	keyName, valueName := model.serialName(shape.Key, "key"), model.serialName(shape.Value, "value")
	for _, key := range keys {
		buffer.WriteString("<" + tag + ">")
		xmlElement(buffer, model, keyName, shape.Key.Shape, key)
		xmlElement(buffer, model, valueName, shape.Value.Shape, object[key])
		buffer.WriteString("</" + tag + ">")
	}
}

// xmlElement writes one projected value as the element tag.
func xmlElement(buffer *bytes.Buffer, model *Model, tag, shapeName string, value any) {
	shape := model.shape(shapeName)
	switch shape.Type {
	case "structure":
		object, _ := value.(map[string]any)
		buffer.WriteString("<" + tag + ">")
		xmlMembers(buffer, model, shape, object)
		buffer.WriteString("</" + tag + ">")
		return
	case "list":
		items, _ := value.([]any)
		element := model.serialName(shape.Member, "member")
		buffer.WriteString("<" + tag + ">")
		for _, item := range items {
			xmlElement(buffer, model, element, shape.Member.Shape, item)
		}
		buffer.WriteString("</" + tag + ">")
		return
	case "map":
		buffer.WriteString("<" + tag + ">")
		xmlEntries(buffer, model, "entry", shape, value)
		buffer.WriteString("</" + tag + ">")
		return
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case bool:
		text = strconv.FormatBool(v)
	case int64:
		text = strconv.FormatInt(v, 10)
	case float64:
		text = strconv.FormatFloat(v, 'g', -1, 64)
	default:
		text = fmt.Sprint(v)
	}
	buffer.WriteString("<" + tag + ">")
	xmlText(buffer, text)
	buffer.WriteString("</" + tag + ">")
}

// writeQueryError serializes ErrorResponse/Error with the modeled error's
// official wire code (for example DBParameterGroupNotFound for the
// DBParameterGroupNotFoundFault shape), so botocore raises that exception.
func writeQueryError(w http.ResponseWriter, model *Model, code string, status int, message string) {
	if shape := model.Shapes[code]; shape != nil && shape.Error != nil && shape.Error.Code != "" {
		code = shape.Error.Code
	}
	party := "Sender"
	if status >= 500 {
		party = "Receiver"
	}
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	buffer.WriteString("<ErrorResponse xmlns=\"")
	xmlText(&buffer, xmlNamespace(model))
	buffer.WriteString("\"><Error><Type>" + party + "</Type><Code>")
	xmlText(&buffer, code)
	buffer.WriteString("</Code><Message>")
	xmlText(&buffer, message)
	id := queryRequestID()
	buffer.WriteString("</Message></Error><RequestId>" + id + "</RequestId></ErrorResponse>")
	writeXML(w, status, id, buffer.Bytes())
}
