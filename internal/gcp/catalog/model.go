// Package catalog serves bounded Google Cloud resource APIs whose contracts
// come from pinned official discovery documents.
//
// Request bodies are validated against the official schemas, responses are
// projections of stored resources, standard methods follow the resource-name
// conventions of the discovery paths, and every method outside a reviewed
// specification is refused with UNIMPLEMENTED. Stored resources are
// configuration metadata in the provider-owned SQLite database; no workload,
// network, data plane or real Google Cloud call is executed or simulated.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed models/*.json
var modelFiles embed.FS

// Property is a documentation-free discovery schema fragment.
type Property struct {
	Type       string               `json:"type"`
	Format     string               `json:"format"`
	Enum       []string             `json:"enum"`
	Ref        string               `json:"$ref"`
	Pattern    string               `json:"pattern"`
	ReadOnly   bool                 `json:"readOnly"`
	Required   bool                 `json:"required"`
	RequiredBy []string             `json:"requiredBy"`
	Default    string               `json:"default"`
	Items      *Property            `json:"items"`
	Additional *Property            `json:"additionalProperties"`
	Properties map[string]*Property `json:"properties"`
}

// Parameter is one official method parameter.
type Parameter struct {
	Type     string   `json:"type"`
	Location string   `json:"location"`
	Required bool     `json:"required"`
	Repeated bool     `json:"repeated"`
	Format   string   `json:"format"`
	Enum     []string `json:"enum"`
	Pattern  string   `json:"pattern"`
}

// Method is one reviewed official discovery method.
type Method struct {
	ID         string
	HTTPMethod string               `json:"httpMethod"`
	Path       string               `json:"path"`
	FlatPath   string               `json:"flatPath"`
	Parameters map[string]Parameter `json:"parameters"`
	Order      []string             `json:"parameterOrder"`
	Request    string               `json:"request"`
	Response   string               `json:"response"`
}

// Route is the official HTTP binding of any method, reviewed or not.
type Route struct {
	HTTPMethod string `json:"httpMethod"`
	FlatPath   string `json:"flatPath"`
}

// Source records the exact official discovery document.
type Source struct {
	Package  string `json:"package"`
	Version  string `json:"version"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	License  string `json:"license"`
	Revision string `json:"revision"`
}

// Model is the embedded per-API subset.
type Model struct {
	Key         string
	Source      Source               `json:"source"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Title       string               `json:"title"`
	RootURL     string               `json:"rootUrl"`
	ServicePath string               `json:"servicePath"`
	Routes      map[string]Route     `json:"routes"`
	Methods     map[string]*Method   `json:"methods"`
	Schemas     map[string]*Property `json:"schemas"`
}

func loadModel(key string) (*Model, error) {
	data, err := modelFiles.ReadFile(path.Join("models", key+".json"))
	if err != nil {
		return nil, fmt.Errorf("GCP catalog model %s is not embedded", key)
	}
	var model Model
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("GCP catalog model %s is malformed: %w", key, err)
	}
	model.Key = key
	return &model, validateModel(&model)
}

// validateModel requires official provenance and closed schema references.
func validateModel(m *Model) error {
	if m.Source.Package != "google-api-python-client" || m.Source.SHA256 == "" || m.Name == "" || m.Version == "" {
		return fmt.Errorf("GCP catalog model %s lacks official provenance", m.Key)
	}
	for id, method := range m.Methods {
		method.ID = id
		if method.FlatPath == "" {
			// Some discovery methods carry only "path".
			method.FlatPath = method.Path
		}
		for _, ref := range []string{method.Request, method.Response} {
			if ref != "" && m.Schemas[ref] == nil {
				return fmt.Errorf("GCP catalog model %s method %s references missing schema %s", m.Key, id, ref)
			}
		}
	}
	var missing string
	var walk func(*Property)
	walk = func(p *Property) {
		if p == nil {
			return
		}
		if p.Ref != "" && m.Schemas[p.Ref] == nil {
			missing = p.Ref
		}
		walk(p.Items)
		walk(p.Additional)
		for _, child := range p.Properties {
			walk(child)
		}
	}
	for _, schema := range m.Schemas {
		walk(schema)
	}
	if missing != "" {
		return fmt.Errorf("GCP catalog model %s references missing schema %s", m.Key, missing)
	}
	return nil
}

func embeddedModels() []string {
	entries, _ := modelFiles.ReadDir("models")
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

// resolve follows a $ref to its named schema.
func (m *Model) resolve(p *Property) *Property {
	for p != nil && p.Ref != "" {
		p = m.Schemas[p.Ref]
	}
	return p
}
