package catalog

import (
	"sort"
	"strings"
)

// MethodDescriptor tells first-party clients how to call one bound method.
type MethodDescriptor struct {
	ID         string   `json:"id"`
	HTTPMethod string   `json:"httpMethod"`
	Path       string   `json:"path"`
	Query      []string `json:"query,omitempty"`
	Items      string   `json:"items,omitempty"`
}

// FieldDescriptor describes one top-level request field of a create method.
type FieldDescriptor struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

// CollectionDescriptor describes one reviewed collection.
type CollectionDescriptor struct {
	ID         string                      `json:"id"`
	Schema     string                      `json:"schema"`
	Parent     string                      `json:"parent,omitempty"`
	Implicit   string                      `json:"implicit,omitempty"`
	IDParam    string                      `json:"idParam,omitempty"`
	Limit      int                         `json:"limit"`
	Mutable    []string                    `json:"mutable,omitempty"`
	Zonal      bool                        `json:"zonal,omitempty"`
	Permission string                      `json:"permission"`
	Methods    map[string]MethodDescriptor `json:"methods"`
	Fields     []FieldDescriptor           `json:"fields,omitempty"`
}

// Descriptor is the capability entry of one Google API.
type Descriptor struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Title       string                 `json:"title"`
	Issues      []int                  `json:"issues"`
	Scope       string                 `json:"scope"`
	Endpoint    string                 `json:"endpoint"`
	Methods     []string               `json:"methods"`
	Official    int                    `json:"officialMethods"`
	Collections []CollectionDescriptor `json:"collections"`
	Bounds      []string               `json:"bounds"`
	Source      Source                 `json:"source"`
}

// Describe lists every reviewed Google API for capabilities and clients.
func (e *Engine) Describe() []Descriptor {
	out := make([]Descriptor, 0, len(e.order))
	for _, b := range e.order {
		d := Descriptor{Name: b.model.Name, Version: b.model.Version, Title: b.api.Title, Issues: b.api.Issues, Scope: b.api.Scope,
			Endpoint: "/_simulator/providers/gcp/" + b.model.Name + "/" + b.model.ServicePath, Official: len(b.model.Routes), Bounds: b.api.Bounds, Source: b.model.Source}
		for id := range b.bindings {
			d.Methods = append(d.Methods, id)
		}
		sort.Strings(d.Methods)
		for _, c := range b.api.Collections {
			cd := CollectionDescriptor{ID: c.ID, Schema: c.Schema, Parent: c.Parent, Implicit: c.Implicit, IDParam: c.IDParam, Limit: c.Limit, Mutable: c.Mutable, Zonal: c.Zonal, Permission: c.Permission, Methods: map[string]MethodDescriptor{}}
			if cd.Limit == 0 {
				cd.Limit = defaultLimit
			}
			for _, bound := range b.api.Bindings {
				if bound.Collection != c.ID {
					continue
				}
				m := b.model.Methods[bound.Method]
				md := MethodDescriptor{ID: bound.Method, HTTPMethod: m.HTTPMethod, Path: m.FlatPath}
				for name, p := range m.Parameters {
					if p.Location == "query" {
						md.Query = append(md.Query, name)
					}
				}
				sort.Strings(md.Query)
				if bound.Kind == List {
					for field, p := range b.model.Schemas[m.Response].Properties {
						if p.Type == "array" && p.Items != nil && p.Items.Ref == c.Schema {
							md.Items = field
						}
					}
				}
				if bound.Kind == Create {
					cd.Fields = b.fields(m.Request, c)
				}
				cd.Methods[string(bound.Kind)] = md
			}
			d.Collections = append(d.Collections, cd)
		}
		out = append(out, d)
	}
	return out
}

// fields lists writable scalar fields a generic form can collect.
func (b *binding) fields(schema string, c *Collection) []FieldDescriptor {
	relaxed := map[string]bool{}
	for _, f := range c.Relax {
		relaxed[f] = true
	}
	s := b.model.Schemas[schema]
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []FieldDescriptor{}
	for _, name := range names {
		p := b.model.resolve(s.Properties[name])
		if s.Properties[name].ReadOnly || name == "name" || p == nil {
			continue
		}
		if p.Type == "string" || p.Type == "boolean" || p.Type == "integer" {
			out = append(out, FieldDescriptor{Name: name, Type: p.Type, Required: s.Properties[name].Required && !relaxed[name], Enum: withoutUnspecified(p.Enum)})
		}
	}
	return out
}

func withoutUnspecified(values []string) []string {
	var out []string
	for _, v := range values {
		if !strings.HasSuffix(v, "_UNSPECIFIED") {
			out = append(out, v)
		}
	}
	return out
}
