package catalog

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// References: a field that names another resource of the same API must name
// an existing one, is stored in canonical form, and keeps the referenced
// resource from being deleted, as Compute Engine refuses to delete a network
// that a subnetwork or firewall still uses.

// eachString visits every string at a reference path ("network",
// "healthChecks[]", "nats[].subnetworks[].name") and stores fn's result.
func eachString(value any, path string, fn func(string) (string, error)) (any, error) {
	if path == "" {
		text, ok := value.(string)
		if !ok {
			return value, nil
		}
		return fn(text)
	}
	head, rest, _ := strings.Cut(path, ".")
	object, ok := value.(map[string]any)
	field := strings.TrimSuffix(head, "[]")
	child, present := object[field]
	if !ok || !present {
		return value, nil
	}
	if field != head {
		items, _ := child.([]any)
		for i, item := range items {
			converted, err := eachString(item, rest, fn)
			if err != nil {
				return nil, err
			}
			items[i] = converted
		}
		return value, nil
	}
	converted, err := eachString(child, rest, fn)
	if err != nil {
		return nil, err
	}
	object[field] = converted
	return value, nil
}

// referencePath reports whether a reference path names string fields of schema.
func (m *Model) referencePath(schema *Property, path string) bool {
	p := m.resolve(schema)
	for _, part := range strings.Split(path, ".") {
		field := strings.TrimSuffix(part, "[]")
		if p == nil || p.Properties[field] == nil {
			return false
		}
		p = m.resolve(p.Properties[field])
		if field != part {
			if p.Type != "array" {
				return false
			}
			p = m.resolve(p.Items)
		}
	}
	return p != nil && p.Type == "string"
}

// canonical is the stored form of a reference to the resource name.
func (q *request) canonical(name string) string {
	if q.b.api.SelfLink != "" {
		return q.link(name)
	}
	return name
}

// referenced resolves a full URL, partial URL or resource name to a
// resource name in the configured project.
func (q *request) referenced(value string) (string, bool) {
	for _, prefix := range []string{q.b.api.SelfLink, q.b.model.RootURL + q.b.model.ServicePath} {
		if prefix != "" && strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			break
		}
	}
	if q.b.api.Scope == "zonal" && (strings.HasPrefix(value, "global/") || strings.HasPrefix(value, "regions/") || strings.HasPrefix(value, "zones/")) {
		value = "projects/" + q.e.cfg.Project + "/" + value
	}
	if strings.HasPrefix(value, "projects/"+q.e.cfg.ProjectNumber+"/") {
		value = "projects/" + q.e.cfg.Project + strings.TrimPrefix(value, "projects/"+q.e.cfg.ProjectNumber)
	}
	parts := strings.Split(value, "/")
	if len(parts) < 4 || parts[0] != "projects" || strings.Contains(value, "//") {
		return "", false
	}
	return value, true
}

// all lists strong and weak reference paths.
func (c *Collection) all() map[string][]string {
	out := map[string][]string{}
	for path, targets := range c.Weak {
		out[path] = targets
	}
	for path, targets := range c.References {
		out[path] = targets
	}
	return out
}

// resolveReferences checks and canonicalizes references of document inside
// the writing transaction; weak references only when created.
func (q *request) resolveReferences(tx *sql.Tx, c *Collection, document map[string]any, created bool) error {
	refs := c.References
	if created {
		refs = c.all()
	}
	paths := make([]string, 0, len(refs))
	for path := range refs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		targets := refs[path]
		_, err := eachString(document, path, func(value string) (string, error) {
			name, ok := q.referenced(value)
			if !ok {
				return "", invalid("Invalid value for field '%s': '%s'. The URL is malformed.", path, value)
			}
			r, err := findName(tx, name)
			if errors.Is(err, sql.ErrNoRows) || err == nil && r.API != q.b.model.Name {
				return "", &status{"NOT_FOUND", fmt.Sprintf("The resource '%s' was not found.", name)}
			}
			if err != nil {
				return "", err
			}
			for _, target := range targets {
				if r.Collection == target {
					return q.canonical(name), nil
				}
			}
			return "", invalid("Field '%s' must name a %s resource.", path, strings.Join(targets, " or "))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// conflicts applies the collection's Conflict rule against every other
// resource of the collection in this API.
func (q *request) conflicts(tx *sql.Tx, c *Collection, name string, document map[string]any) error {
	if c.Conflict == nil {
		return nil
	}
	others, err := collectionRows(tx, q.b.model.Name, c.ID)
	if err != nil {
		return err
	}
	for _, other := range others {
		if other.Name != name {
			if err := c.Conflict(document, other.Document); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectionRows(tx *sql.Tx, api, collection string) ([]row, error) {
	rows, err := tx.Query("SELECT "+columns+" FROM gcp_catalog_resources WHERE api=? AND collection=? ORDER BY seq", api, collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// referrer returns a resource that still references r, if any.
func (q *request) referrer(tx *sql.Tx, r row) (string, error) {
	want := q.canonical(r.Name)
	for _, c := range q.b.api.Collections {
		var paths []string
		for path, targets := range c.References {
			for _, target := range targets {
				if target == r.Collection {
					paths = append(paths, path)
				}
			}
		}
		if len(paths) == 0 {
			continue
		}
		users, err := collectionRows(tx, q.b.model.Name, c.ID)
		if err != nil {
			return "", err
		}
		for _, user := range users {
			found := false
			for _, path := range paths {
				_, _ = eachString(user.Document, path, func(value string) (string, error) {
					found = found || value == want
					return value, nil
				})
			}
			if found && user.Name != r.Name {
				return user.Name, nil
			}
		}
	}
	return "", nil
}
