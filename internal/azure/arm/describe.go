package arm

import (
	"fmt"
	"sort"
	"strings"
)

// OperationDescriptor tells first-party clients how to call one bound operation.
type OperationDescriptor struct {
	ID       string   `json:"id"`
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	Query    []string `json:"query,omitempty"`
	Items    string   `json:"items,omitempty"`
	NextLink string   `json:"nextLink,omitempty"`
}

// FieldDescriptor describes one request field the console form offers.
type FieldDescriptor struct {
	Path     string   `json:"path"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Default  any      `json:"default,omitempty"`
}

// TypeDescriptor describes one reviewed resource type.
type TypeDescriptor struct {
	Type       string                         `json:"type"`
	Name       string                         `json:"name"`
	Title      string                         `json:"title"`
	Parent     string                         `json:"parent,omitempty"`
	Limit      int                            `json:"limit"`
	Location   bool                           `json:"location"`
	GlobalName bool                           `json:"globalName,omitempty"`
	Operations map[string]OperationDescriptor `json:"operations"`
	Fields     []FieldDescriptor              `json:"fields"`
	Defaults   map[string]any                 `json:"defaults,omitempty"`
	References []string                       `json:"references,omitempty"`
}

// Descriptor is the capability entry of one resource provider.
type Descriptor struct {
	Namespace  string           `json:"namespace"`
	APIVersion string           `json:"apiVersion"`
	Title      string           `json:"title"`
	Issues     []int            `json:"issues"`
	Operations []string         `json:"operations"`
	Official   int              `json:"officialOperations"`
	Types      []TypeDescriptor `json:"types"`
	Bounds     []string         `json:"bounds"`
	Source     Source           `json:"source"`
}

// Describe lists every reviewed provider for capabilities and clients.
func (e *Engine) Describe() []Descriptor {
	out := make([]Descriptor, 0, len(e.order))
	for _, p := range e.order {
		d := Descriptor{Namespace: p.spec.Namespace, APIVersion: p.spec.APIVersion, Title: p.spec.Title, Issues: p.spec.Issues,
			Official: len(p.model.Routes), Bounds: p.spec.Bounds, Source: p.model.Source}
		for id := range p.ops {
			d.Operations = append(d.Operations, id)
		}
		sort.Strings(d.Operations)
		for _, t := range p.spec.Types {
			bt := e.types[strings.ToLower(p.spec.Namespace+"/"+t.Name)]
			td := TypeDescriptor{Type: bt.full, Name: t.Name, Title: t.Title, Limit: t.Limit, GlobalName: t.GlobalName, Operations: map[string]OperationDescriptor{}, Fields: []FieldDescriptor{}}
			if bt.parent != nil {
				td.Parent = bt.parent.full
			}
			m := p.model
			if location, ok := m.merged(m.Definitions[bt.response]).properties["location"]; ok && bt.parent == nil {
				td.Location = !readOnly(location, m.resolve(location))
			}
			for _, b := range p.spec.Bindings {
				if b.Type != t.Name {
					continue
				}
				nb := p.ops[b.Operation]
				od := OperationDescriptor{ID: b.Operation, Method: nb.op.Method, Path: nb.op.Path, Query: b.Query}
				key := string(b.Kind)
				if b.Kind == List {
					od.Items, od.NextLink = nb.op.Pageable.ItemName, nb.op.Pageable.NextLinkName
					if nb.group == "" && !bt.group {
						key = "listAll"
					}
				}
				td.Operations[key] = od
			}
			for _, path := range t.Form {
				fd, _ := e.field(bt, path)
				td.Fields = append(td.Fields, fd)
			}
			if len(t.Defaults) > 0 {
				td.Defaults = map[string]any{}
				for k, value := range t.Defaults {
					if value == "$tenant" {
						value = e.cfg.Tenant
					}
					td.Defaults[k] = value
				}
			}
			for _, ref := range t.Refs {
				td.References = append(td.References, ref.Path+" -> "+ref.Type)
			}
			d.Types = append(d.Types, td)
		}
		out = append(out, d)
	}
	return out
}

// field resolves a dotted request path to its official type and enum.
func (e *Engine) field(bt *boundType, dotted string) (FieldDescriptor, error) {
	m := bt.provider.model
	s := &Schema{Ref: bt.request}
	required := true
	for _, part := range strings.Split(dotted, ".") {
		v := m.merged(s)
		next := v.properties[part]
		if next == nil {
			return FieldDescriptor{}, fmt.Errorf("azure ARM type %s form field %s is not in the official request definition", bt.full, dotted)
		}
		required = required && v.required[part]
		s = next
	}
	resolved := m.resolve(s)
	fd := FieldDescriptor{Path: dotted, Type: resolved.Type, Required: required}
	if resolved.Type == "array" {
		fd.Type = "array:" + m.resolve(resolved.Items).Type
	}
	for _, value := range resolved.Enum {
		if text, ok := value.(string); ok {
			fd.Enum = append(fd.Enum, text)
		}
	}
	if value, ok := bt.spec.Defaults[dotted]; ok {
		if value == "$tenant" {
			value = e.cfg.Tenant
		}
		fd.Default = value
	}
	return fd, nil
}
