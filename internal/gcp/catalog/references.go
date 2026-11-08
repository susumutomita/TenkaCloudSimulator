package catalog

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// References: a field path that names another resource of the same API must
// name an existing one and is stored in canonical form; a blocking reference
// keeps the named resource from being deleted, as Compute Engine refuses to
// delete a network that a subnetwork or firewall still uses.

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

// resolveName resolves a full URL, partial URL or resource name to a
// resource name in the configured project.
func (q *request) resolveName(value string) (string, bool) {
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

// references resolves the reference paths of document inside the writing
// transaction: every path at creation (written is nil) and, on update, the
// paths whose top-level field the update wrote, so a weak or overridden
// reference whose target has gone does not block unrelated updates. Each
// value must name an existing resource of the path's collections and is
// stored in canonical form.
func (q *request) references(tx *sql.Tx, c *Collection, document map[string]any, written map[string]bool) error {
	paths := make([]string, 0, len(c.References))
	for path := range c.References {
		if field, _, _ := strings.Cut(path, "."); written == nil || written[strings.TrimSuffix(field, "[]")] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	flat := q.b.api.SelfLink != ""
	for _, path := range paths {
		targets := c.References[path].targets()
		_, err := eachString(document, path, func(value string) (string, error) {
			name, ok := q.resolveName(value)
			if !ok && flat {
				return "", invalid("Invalid value for field '%s': '%s'. The URL is malformed.", path, value)
			}
			r, err := findName(tx, q.b.model.Name, name)
			if errors.Is(err, sql.ErrNoRows) || !ok {
				if flat {
					return "", &status{"NOT_FOUND", fmt.Sprintf("The resource '%s' was not found.", name)}
				}
				return "", &status{"NOT_FOUND", fmt.Sprintf("Referenced resource '%s' in field '%s' was not found.", value, path)}
			}
			if err != nil {
				return "", err
			}
			for _, target := range targets {
				if r.Collection == target {
					return q.canonical(name), nil
				}
			}
			// Compute Engine refuses a reference of the wrong type; AIP
			// services do not find a resource of the named collection.
			if flat {
				return "", invalid("Field '%s' must name a %s resource.", path, strings.Join(targets, " or "))
			}
			return "", &status{"NOT_FOUND", fmt.Sprintf("Referenced resource '%s' in field '%s' was not found.", name, path)}
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

// referrer returns a resource outside the deleted subtree that still names
// r through a blocking reference not lifted by its override parameter.
func (q *request) referrer(tx *sql.Tx, r row) (string, error) {
	want := q.canonical(r.Name)
	for _, c := range q.b.api.Collections {
		var paths []string
		for path, ref := range c.References {
			if !ref.Blocks || ref.Override != "" && q.params[ref.Override] == true {
				continue
			}
			for _, target := range ref.targets() {
				if target == r.Collection {
					paths = append(paths, path)
				}
			}
		}
		if len(paths) == 0 {
			continue
		}
		sort.Strings(paths)
		users, err := collectionRows(tx, q.b.model.Name, c.ID)
		if err != nil {
			return "", err
		}
		for _, user := range users {
			if user.Name == r.Name || strings.HasPrefix(user.Name, r.Name+"/") {
				continue
			}
			found := false
			for _, path := range paths {
				_, _ = eachString(user.Document, path, func(value string) (string, error) {
					found = found || value == want
					return value, nil
				})
			}
			if found {
				return user.Name, nil
			}
		}
	}
	return "", nil
}
