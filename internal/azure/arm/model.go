// Package arm serves bounded Azure Resource Manager resource providers whose
// contracts come from documentation-free subsets of a pinned commit of the
// official azure-rest-api-specs swagger files.
//
// Requests use ARM resource IDs under one synthetic subscription, require the
// provider's api-version, are validated against the official definitions and
// are projected onto the official response definitions. Every official
// operation of a selected api-version outside a reviewed specification is
// refused with HTTP 501. Stored resources are configuration metadata in the
// provider-owned SQLite tables; no workload, network, data plane or real Azure
// call is executed or simulated. Authorization is the configured local owner,
// not Azure RBAC.
package arm

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:embed models/*.json
var modelFiles embed.FS

// Schema is a documentation-free swagger 2.0 definition fragment.
type Schema struct {
	Ref           string             `json:"$ref"`
	Type          string             `json:"type"`
	Format        string             `json:"format"`
	Enum          []any              `json:"enum"`
	ModelAsString bool               `json:"x-ms-enum-model-as-string"`
	Pattern       string             `json:"pattern"`
	MinLength     *int               `json:"minLength"`
	MaxLength     *int               `json:"maxLength"`
	Minimum       *float64           `json:"minimum"`
	Maximum       *float64           `json:"maximum"`
	ExclusiveMin  bool               `json:"exclusiveMinimum"`
	ExclusiveMax  bool               `json:"exclusiveMaximum"`
	MinItems      *int               `json:"minItems"`
	MaxItems      *int               `json:"maxItems"`
	ReadOnly      bool               `json:"readOnly"`
	Secret        bool               `json:"x-ms-secret"`
	Mutability    []string           `json:"x-ms-mutability"`
	Nullable      bool               `json:"x-nullable"`
	Required      []string           `json:"required"`
	Items         *Schema            `json:"items"`
	Additional    json.RawMessage    `json:"additionalProperties"`
	Properties    map[string]*Schema `json:"properties"`
	AllOf         []*Schema          `json:"allOf"`
	Discriminator string             `json:"discriminator"`
	Subtypes      map[string]string  `json:"x-subtypes"`

	additional *Schema // parsed additionalProperties schema
	openMap    bool    // additionalProperties: true
	pattern    *regexp.Regexp
}

// Parameter is one resolved official operation parameter.
type Parameter struct {
	Name      string   `json:"name"`
	In        string   `json:"in"`
	Required  bool     `json:"required"`
	Type      string   `json:"type"`
	Format    string   `json:"format"`
	Enum      []any    `json:"enum"`
	Pattern   string   `json:"pattern"`
	MinLength *int     `json:"minLength"`
	MaxLength *int     `json:"maxLength"`
	Minimum   *float64 `json:"minimum"`
	Maximum   *float64 `json:"maximum"`
	Skip      bool     `json:"x-ms-skip-url-encoding"`
	Schema    string   `json:"schema"`

	pattern *regexp.Regexp
}

// Pageable records the x-ms-pageable trait of a list operation.
type Pageable struct {
	NextLinkName string `json:"nextLinkName"`
	ItemName     string `json:"itemName"`
}

// Operation is one reviewed official operation.
type Operation struct {
	ID          string
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Parameters  []*Parameter      `json:"parameters"`
	Responses   map[string]string `json:"responses"`
	LongRunning bool              `json:"longRunning"`
	Pageable    *Pageable         `json:"pageable"`
}

// Route is the official HTTP binding of any operation, reviewed or not.
type Route struct {
	Method string   `json:"method"`
	Path   string   `json:"path"`
	Multi  []string `json:"multi"`
}

// SourceFile is one official swagger file and its SHA-256.
type SourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Source records the exact official specification commit and files.
type Source struct {
	Repository string       `json:"repository"`
	Commit     string       `json:"commit"`
	License    string       `json:"license"`
	Files      []SourceFile `json:"files"`
}

// Model is the embedded per-namespace subset of one stable api-version.
type Model struct {
	Namespace   string                `json:"namespace"`
	APIVersion  string                `json:"apiVersion"`
	Source      Source                `json:"source"`
	Routes      map[string]*Route     `json:"routes"`
	Operations  map[string]*Operation `json:"operations"`
	Definitions map[string]*Schema    `json:"definitions"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func loadModel(namespace string) (*Model, error) {
	data, err := modelFiles.ReadFile(path.Join("models", namespace+".json"))
	if err != nil {
		return nil, fmt.Errorf("azure ARM model %s is not embedded", namespace)
	}
	var model Model
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("azure ARM model %s is malformed: %w", namespace, err)
	}
	return &model, validateModel(&model)
}

// validateModel requires official provenance, closed references and
// compilable patterns before any request is served.
func validateModel(m *Model) error {
	if m.Source.Repository != "github.com/Azure/azure-rest-api-specs" || m.Source.License != "MIT" || !commitPattern.MatchString(m.Source.Commit) || len(m.Source.Files) == 0 || m.Namespace == "" || m.APIVersion == "" {
		return fmt.Errorf("azure ARM model %s lacks official provenance", m.Namespace)
	}
	for _, f := range m.Source.Files {
		if len(f.SHA256) != 64 || !strings.HasPrefix(f.Path, "specification/") {
			return fmt.Errorf("azure ARM model %s has an unverifiable source file", m.Namespace)
		}
	}
	for id, op := range m.Operations {
		op.ID = id
		if m.Routes[id] == nil || m.Routes[id].Method != op.Method || m.Routes[id].Path != op.Path {
			return fmt.Errorf("azure ARM model %s operation %s has no matching route", m.Namespace, id)
		}
		for _, p := range op.Parameters {
			if p.Schema != "" && m.Definitions[p.Schema] == nil {
				return fmt.Errorf("azure ARM model %s operation %s references missing definition %s", m.Namespace, id, p.Schema)
			}
			if p.Pattern != "" {
				compiled, err := compilePattern(p.Pattern)
				if err != nil {
					return fmt.Errorf("azure ARM model %s parameter %s pattern: %w", m.Namespace, p.Name, err)
				}
				p.pattern = compiled
			}
		}
		for _, ref := range op.Responses {
			if ref != "" && m.Definitions[ref] == nil {
				return fmt.Errorf("azure ARM model %s operation %s references missing definition %s", m.Namespace, id, ref)
			}
		}
	}
	var failure error
	var walk func(*Schema)
	walk = func(s *Schema) {
		if s == nil || failure != nil {
			return
		}
		if s.Ref != "" && m.Definitions[s.Ref] == nil {
			failure = fmt.Errorf("azure ARM model %s references missing definition %s", m.Namespace, s.Ref)
			return
		}
		for _, sub := range s.Subtypes {
			if m.Definitions[sub] == nil {
				failure = fmt.Errorf("azure ARM model %s references missing subtype %s", m.Namespace, sub)
				return
			}
		}
		if s.Pattern != "" && s.pattern == nil {
			compiled, err := compilePattern(s.Pattern)
			if err != nil {
				failure = fmt.Errorf("azure ARM model %s pattern: %w", m.Namespace, err)
				return
			}
			s.pattern = compiled
		}
		if len(s.Additional) > 0 && s.additional == nil && !s.openMap {
			text := strings.TrimSpace(string(s.Additional))
			switch text {
			case "true":
				s.openMap = true
			case "false":
			default:
				var extra Schema
				if err := json.Unmarshal(s.Additional, &extra); err != nil {
					failure = fmt.Errorf("azure ARM model %s has malformed additionalProperties", m.Namespace)
					return
				}
				s.additional = &extra
			}
		}
		walk(s.Items)
		walk(s.additional)
		for _, a := range s.AllOf {
			walk(a)
		}
		for _, p := range s.Properties {
			walk(p)
		}
	}
	names := make([]string, 0, len(m.Definitions))
	for name := range m.Definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		walk(m.Definitions[name])
	}
	return failure
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

// resolve follows $ref chains to a named definition.
func (m *Model) resolve(s *Schema) *Schema {
	for depth := 0; s != nil && s.Ref != "" && depth < 32; depth++ {
		s = m.Definitions[s.Ref]
	}
	return s
}

// view is the merged property set of an object definition and its allOf.
type view struct {
	properties map[string]*Schema
	required   map[string]bool
	schema     *Schema
}

// merged collects properties and required names from allOf, without
// dispatching on discriminators of the inherited bases.
func (m *Model) merged(s *Schema) view {
	v := view{properties: map[string]*Schema{}, required: map[string]bool{}, schema: s}
	var visit func(*Schema, int)
	visit = func(s *Schema, depth int) {
		s = m.resolve(s)
		if s == nil || depth > 16 {
			return
		}
		for _, parent := range s.AllOf {
			visit(parent, depth+1)
		}
		for name, p := range s.Properties {
			v.properties[name] = p
		}
		for _, name := range s.Required {
			v.required[name] = true
		}
	}
	visit(s, 0)
	return v
}

// isObject reports whether a resolved schema describes a JSON object.
func (m *Model) isObject(s *Schema) bool {
	s = m.resolve(s)
	if s == nil {
		return false
	}
	if s.Type == "object" || len(s.Properties) > 0 || s.additional != nil || s.openMap {
		return true
	}
	for _, parent := range s.AllOf {
		if m.isObject(parent) {
			return true
		}
	}
	return false
}

// readOnly reports whether a property is never accepted from clients.
func readOnly(property, resolved *Schema) bool {
	if property.ReadOnly || resolved != nil && resolved.ReadOnly {
		return true
	}
	if len(property.Mutability) > 0 {
		for _, m := range property.Mutability {
			if m == "create" || m == "update" {
				return false
			}
		}
		return true
	}
	return false
}

// writeOnly reports whether a property is accepted but never returned.
func writeOnly(property *Schema) bool {
	if len(property.Mutability) == 0 {
		return false
	}
	for _, m := range property.Mutability {
		if m == "read" {
			return false
		}
	}
	return true
}

// createOnly reports whether a property may be set only at creation.
func createOnly(property *Schema) bool {
	if len(property.Mutability) == 0 {
		return false
	}
	create, update := false, false
	for _, m := range property.Mutability {
		create = create || m == "create"
		update = update || m == "update"
	}
	return create && !update
}

// compilePattern translates the .NET word class used by ARM specifications
// to its Unicode RE2 equivalent and compiles the result.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	const word = `\p{L}\p{Mn}\p{Nd}\p{Pc}`
	var b strings.Builder
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			if pattern[i+1] == 'w' {
				if inClass {
					b.WriteString(word)
				} else {
					b.WriteString("[" + word + "]")
				}
			} else {
				b.WriteByte(c)
				b.WriteByte(pattern[i+1])
			}
			i++
			continue
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		}
		b.WriteByte(c)
	}
	return regexp.Compile(b.String())
}
