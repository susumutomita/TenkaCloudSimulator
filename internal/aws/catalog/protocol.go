package catalog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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
			if si != len(r.segments)-1 || i >= len(escaped) {
				return nil, false
			}
			rest := strings.Join(escaped[i:], "/")
			decoded, err := url.PathUnescape(rest)
			if err != nil || decoded == "" {
				return nil, false
			}
			labels[strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "+}")] = decoded
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
		allowed[name] = true
	}
	for key, value := range object {
		if !allowed[key] {
			if _, member := shape.Members[key]; member {
				return nil, invalid("Member %s must be sent in its official HTTP location.", key)
			}
			return nil, invalid("Unknown member %s is not part of the official input shape.", key)
		}
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
				body[name] = value
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
				if n, ok := value.(int64); ok {
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
					body[name] = value
				}
			}
		}
	}
	if protocol == "json" {
		w.Header().Set("Content-Type", "application/x-amz-json-"+model.Metadata.JSONVersion)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if raw == nil {
		raw, _ = json.Marshal(body)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// writeError serializes a typed error with its documented status.
func writeError(w http.ResponseWriter, model *Model, code string, status int, message string) {
	body := map[string]any{"message": message}
	if model != nil {
		if shape := model.Shapes[code]; shape != nil {
			if _, upper := shape.Members["Message"]; upper {
				body = map[string]any{"Message": message}
			}
		}
	}
	w.Header().Set("X-Amzn-ErrorType", code)
	if model != nil && model.Metadata.Protocol == "json" {
		body["__type"] = code
		w.Header().Set("Content-Type", "application/x-amz-json-"+model.Metadata.JSONVersion)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	raw, _ := json.Marshal(body)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
