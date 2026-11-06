// Package catalog serves bounded AWS control-plane resources whose wire
// contracts come from the pinned official botocore service models.
//
// Every request is decoded and validated against the official input shape,
// every response is projected onto the official output shape, and every
// operation outside the reviewed per-service specification is refused with
// an explicit error. Stored resources are configuration metadata in the
// provider-owned SQLite database; no workload, data plane, network or real
// AWS call is executed or simulated.
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

// Member is one structure member with its HTTP/JSON binding traits.
type Member struct {
	Shape          string `json:"shape"`
	Location       string `json:"location"`
	LocationName   string `json:"locationName"`
	IdempotencyTok bool   `json:"idempotencyToken"`
	JSONValue      bool   `json:"jsonvalue"`
	TimestampFmt   string `json:"timestampFormat"`
	QueryName      string `json:"queryName"`
	Flattened      bool   `json:"flattened"`
	XMLName        string `json:"xmlName"`
	Streaming      bool   `json:"streaming"`
	HostLabel      bool   `json:"hostLabel"`
	EventPayload   bool   `json:"eventpayload"`
}

// ErrorTrait carries the documented HTTP status of a modeled exception.
type ErrorTrait struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"httpStatusCode"`
	Sender     bool   `json:"senderFault"`
}

// Shape is a documentation-free copy of one official botocore shape.
type Shape struct {
	Type         string            `json:"type"`
	Members      map[string]Member `json:"members"`
	Member       *Member           `json:"member"`
	Key          *Member           `json:"key"`
	Value        *Member           `json:"value"`
	Required     []string          `json:"required"`
	Enum         []string          `json:"enum"`
	Min          *float64          `json:"min"`
	Max          *float64          `json:"max"`
	Pattern      string            `json:"pattern"`
	Payload      string            `json:"payload"`
	Error        *ErrorTrait       `json:"error"`
	Exception    bool              `json:"exception"`
	Document     bool              `json:"document"`
	Union        bool              `json:"union"`
	Sensitive    bool              `json:"sensitive"`
	Streaming    bool              `json:"streaming"`
	Event        bool              `json:"eventstream"`
	TimestampFmt string            `json:"timestampFormat"`
	Flattened    bool              `json:"flattened"`
}

// HTTP is the official REST binding of an operation.
type HTTP struct {
	Method       string `json:"method"`
	RequestURI   string `json:"requestUri"`
	ResponseCode int    `json:"responseCode"`
}

// Paginator is the official paginator configuration of one operation.
type Paginator struct {
	InputToken  any    `json:"input_token"`
	OutputToken any    `json:"output_token"`
	LimitKey    string `json:"limit_key"`
	ResultKey   any    `json:"result_key"`
}

// Route is the official HTTP binding of any operation, implemented or not.
type Route struct {
	HTTP HTTP `json:"http"`
}

// Operation is one implemented official operation.
type Operation struct {
	Name      string
	HTTP      HTTP       `json:"http"`
	Input     string     `json:"input"`
	Output    string     `json:"output"`
	Errors    []string   `json:"errors"`
	Paginator *Paginator `json:"paginator"`
}

// Metadata is the official service metadata used for routing and signing.
type Metadata struct {
	APIVersion     string `json:"apiVersion"`
	EndpointPrefix string `json:"endpointPrefix"`
	JSONVersion    string `json:"jsonVersion"`
	Protocol       string `json:"protocol"`
	FullName       string `json:"serviceFullName"`
	ServiceID      string `json:"serviceId"`
	SigningName    string `json:"signingName"`
	TargetPrefix   string `json:"targetPrefix"`
	GlobalEndpoint string `json:"globalEndpoint"`
	XMLNamespace   any    `json:"xmlNamespace"`
}

// Source records the exact official model the subset was extracted from.
type Source struct {
	Package string `json:"package"`
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	License string `json:"license"`
}

// Model is the embedded per-service subset.
type Model struct {
	Name       string
	Source     Source                `json:"source"`
	Metadata   Metadata              `json:"metadata"`
	Routes     map[string]Route      `json:"routes"`
	Operations map[string]*Operation `json:"operations"`
	Shapes     map[string]*Shape     `json:"shapes"`
}

func loadModel(name string) (*Model, error) {
	data, err := modelFiles.ReadFile(path.Join("models", name+".json"))
	if err != nil {
		return nil, fmt.Errorf("catalog model %s is not embedded", name)
	}
	var model Model
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("catalog model %s is malformed: %w", name, err)
	}
	model.Name = name
	if err := validateModel(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// validateModel requires official provenance and closed shape references.
func validateModel(model *Model) error {
	name := model.Name
	if model.Source.Package != "botocore" || model.Source.SHA256 == "" || model.Metadata.Protocol == "" {
		return fmt.Errorf("catalog model %s lacks official provenance", name)
	}
	for opName, op := range model.Operations {
		op.Name = opName
		for _, shape := range append([]string{op.Input, op.Output}, op.Errors...) {
			if shape != "" && model.Shapes[shape] == nil {
				return fmt.Errorf("catalog model %s operation %s references missing shape %s", name, opName, shape)
			}
		}
	}
	for shapeName, shape := range model.Shapes {
		refs := []*Member{shape.Member, shape.Key, shape.Value}
		for _, m := range shape.Members {
			member := m
			refs = append(refs, &member)
		}
		for _, ref := range refs {
			if ref != nil && model.Shapes[ref.Shape] == nil {
				return fmt.Errorf("catalog model %s shape %s references missing shape %s", name, shapeName, ref.Shape)
			}
		}
	}
	return nil
}

// embeddedModels lists the embedded model names in sorted order.
func embeddedModels() []string {
	entries, _ := modelFiles.ReadDir("models")
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

// shape returns a shape that loadModel proved to exist.
func (m *Model) shape(name string) *Shape { return m.Shapes[name] }

// sortedMembers returns structure member names in a deterministic order.
func (s *Shape) sortedMembers() []string {
	names := make([]string, 0, len(s.Members))
	for name := range s.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
