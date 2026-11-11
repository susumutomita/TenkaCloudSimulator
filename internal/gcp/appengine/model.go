package appengine

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// model.json is the documentation-free App Engine Admin API v1 subset
// extracted by tools/gcpmodels/appengine.py from the discovery document that
// google-api-python-client 2.199.0 ships. It keeps the HTTP binding of every
// official method, so unreviewed methods are refused with UNIMPLEMENTED, and
// the parameters and schemas of the reviewed methods only.
//
//go:embed model.json
var modelJSON []byte

type property struct {
	Type       string               `json:"type"`
	Format     string               `json:"format"`
	Enum       []string             `json:"enum"`
	Ref        string               `json:"$ref"`
	ReadOnly   bool                 `json:"readOnly"`
	Items      *property            `json:"items"`
	Additional *property            `json:"additionalProperties"`
	Properties map[string]*property `json:"properties"`
}

type parameter struct {
	Type     string   `json:"type"`
	Location string   `json:"location"`
	Required bool     `json:"required"`
	Format   string   `json:"format"`
	Enum     []string `json:"enum"`
}

type method struct {
	HTTPMethod string               `json:"httpMethod"`
	FlatPath   string               `json:"flatPath"`
	Path       string               `json:"path"`
	Parameters map[string]parameter `json:"parameters"`
	Request    string               `json:"request"`
	Response   string               `json:"response"`
}

type source struct {
	Package  string `json:"package"`
	Version  string `json:"version"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	License  string `json:"license"`
	Revision string `json:"revision"`
}

type model struct {
	Source  source `json:"source"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title"`
	Routes  map[string]struct {
		HTTPMethod string `json:"httpMethod"`
		FlatPath   string `json:"flatPath"`
	} `json:"routes"`
	Methods map[string]*method   `json:"methods"`
	Schemas map[string]*property `json:"schemas"`
}

type route struct {
	id       string
	method   string
	segments []string
	verb     string
	reviewed bool
}

func loadModel(data []byte, reviewed map[string]bool) (*model, []route, error) {
	var m model
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, fmt.Errorf("App Engine model is malformed: %w", err)
	}
	if m.Source.Package != "google-api-python-client" || m.Source.SHA256 == "" || m.Name != "appengine" || m.Version != "v1" {
		return nil, nil, errors.New("App Engine model lacks official provenance")
	}
	if len(m.Methods) != len(reviewed) {
		return nil, nil, errors.New("App Engine model and reviewed handlers differ")
	}
	for id, entry := range m.Methods {
		if !reviewed[id] || m.Routes[id].FlatPath == "" {
			return nil, nil, fmt.Errorf("App Engine model method %s has no reviewed handler", id)
		}
		for _, ref := range []string{entry.Request, entry.Response} {
			if ref != "" && m.Schemas[ref] == nil {
				return nil, nil, fmt.Errorf("App Engine model method %s references a missing schema", id)
			}
		}
	}
	ids := make([]string, 0, len(m.Routes))
	for id := range m.Routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	routes := make([]route, 0, len(ids))
	for _, id := range ids {
		r := m.Routes[id]
		segments := strings.Split(strings.Trim(r.FlatPath, "/"), "/")
		verb := ""
		last := segments[len(segments)-1]
		if before, after, ok := strings.Cut(last, ":"); ok {
			segments[len(segments)-1], verb = before, after
		}
		routes = append(routes, route{id: id, method: r.HTTPMethod, segments: segments, verb: verb, reviewed: m.Methods[id] != nil})
	}
	return &m, routes, nil
}

// match finds the official binding of a decoded path. Templates with more
// literal segments win, so firewall/ingressRules:batchUpdate never matches a
// variable template.
func match(routes []route, httpMethod string, segments []string) (route, map[string]string, bool) {
	best, bestLiterals, found := route{}, -1, false
	var bestValues map[string]string
	for _, rt := range routes {
		if rt.method != httpMethod || len(rt.segments) != len(segments) {
			continue
		}
		values, literals, ok := map[string]string{}, 0, true
		for i, template := range rt.segments {
			value := segments[i]
			if i == len(segments)-1 {
				base, verb, hasVerb := strings.Cut(value, ":")
				if rt.verb != "" {
					if !hasVerb || verb != rt.verb || base == "" {
						ok = false
						break
					}
					value = base
				} else if hasVerb {
					ok = false
					break
				}
			}
			if strings.HasPrefix(template, "{") && strings.HasSuffix(template, "}") {
				values[strings.Trim(template, "{}")] = value
				continue
			}
			if template != value {
				ok = false
				break
			}
			literals++
		}
		if ok && literals > bestLiterals {
			best, bestLiterals, bestValues, found = rt, literals, values, true
		}
	}
	return best, bestValues, found
}

// field classifies an official request field of a schema.
func (m *model) field(schema, name string) (official, readOnly bool) {
	s := m.Schemas[schema]
	if s == nil {
		return false, false
	}
	p, ok := s.Properties[name]
	if !ok {
		return false, false
	}
	return true, p.ReadOnly
}
