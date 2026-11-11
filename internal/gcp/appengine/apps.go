package appengine

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// handlers maps every reviewed official method to its implementation.
var handlers = map[string]func(*Engine, *request) (any, error){
	"apps.create":                            (*Engine).createApplication,
	"apps.get":                               (*Engine).getApplication,
	"apps.patch":                             (*Engine).patchApplication,
	"apps.locations.list":                    (*Engine).listLocations,
	"apps.locations.get":                     (*Engine).getLocation,
	"apps.operations.list":                   (*Engine).listOperations,
	"apps.operations.get":                    (*Engine).getOperation,
	"apps.services.list":                     (*Engine).listServices,
	"apps.services.get":                      (*Engine).getService,
	"apps.services.patch":                    (*Engine).patchService,
	"apps.services.delete":                   (*Engine).deleteService,
	"apps.services.versions.create":          (*Engine).createVersion,
	"apps.services.versions.list":            (*Engine).listVersions,
	"apps.services.versions.get":             (*Engine).getVersion,
	"apps.services.versions.patch":           (*Engine).patchVersion,
	"apps.services.versions.delete":          (*Engine).deleteVersion,
	"apps.firewall.ingressRules.create":      (*Engine).createRule,
	"apps.firewall.ingressRules.list":        (*Engine).listRules,
	"apps.firewall.ingressRules.get":         (*Engine).getRule,
	"apps.firewall.ingressRules.patch":       (*Engine).patchRule,
	"apps.firewall.ingressRules.delete":      (*Engine).deleteRule,
	"apps.firewall.ingressRules.batchUpdate": (*Engine).batchUpdateRules,
}

// validID is the shared ID rule of projects, services and versions here:
// lowercase letters, digits and inner hyphens, at most 63 characters.
var validID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Location is one supported App Engine location.
type Location struct{ ID, Region, RegionID, Display string }

// Locations are the App Engine locations this simulator accepts. RegionID is
// the code App Engine places in regional hostnames (PROJECT.REGION_ID.r.appspot.com).
var Locations = []Location{
	{"asia-northeast1", "asia-northeast1", "an", "Tokyo"},
	{"europe-west", "europe-west1", "ew", "Belgium"},
	{"us-central", "us-central1", "uc", "Iowa"},
	{"us-east1", "us-east1", "ue", "South Carolina"},
}

func findLocation(id string) (Location, bool) {
	for _, l := range Locations {
		if l.ID == id {
			return l, true
		}
	}
	return Location{}, false
}

// Hostname is the regional default hostname of the configured application.
func (e *Engine) hostname(location string) string {
	l, _ := findLocation(location)
	return e.cfg.Project + "." + l.RegionID + ".r.appspot.com"
}

func (e *Engine) applicationResource(a application) map[string]any {
	return map[string]any{"name": "apps/" + e.cfg.Project, "id": e.cfg.Project, "locationId": a.Location,
		"servingStatus": a.ServingStatus, "defaultHostname": e.hostname(a.Location)}
}

// mask parses a required update mask and checks it against supported fields.
func mask(q *request, supported ...string) ([]string, error) {
	raw := q.param("updateMask")
	if raw == "" {
		return nil, invalid("An update mask is required (updateMask).")
	}
	allowed := map[string]bool{}
	for _, s := range supported {
		allowed[s] = true
	}
	var paths []string
	seen := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if !allowed[p] {
			return nil, invalid("Update mask field " + p + " is not supported by the local App Engine simulator; supported: " + strings.Join(supported, ", ") + ".")
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// patchBody refuses unknown fields; official fields outside the mask are
// ignored, as AIP-134 asks of fields that are not in the update mask.
func (e *Engine) patchBody(body map[string]json.RawMessage, schema string, paths []string) (map[string]json.RawMessage, error) {
	keep := map[string]json.RawMessage{}
	masked := map[string]bool{}
	for _, p := range paths {
		masked[p] = true
	}
	for name, value := range body {
		if official, _ := e.model.field(schema, name); !official {
			return nil, invalid("Unknown field " + name + " in " + schema + ".")
		}
		if masked[name] {
			keep[name] = value
		}
	}
	return keep, nil
}

func (e *Engine) createApplication(q *request) (any, error) {
	body, err := e.fields(q.body, "Application", "id", "locationId")
	if err != nil {
		return nil, err
	}
	id := ""
	if raw, ok := body["id"]; ok {
		if id, err = decodeString(raw, "id"); err != nil {
			return nil, err
		}
	}
	if id == "" {
		return nil, invalid("Application id is required; it is the project ID.")
	}
	if id != e.cfg.Project {
		return nil, errDenied
	}
	location := "us-central"
	if raw, ok := body["locationId"]; ok {
		if location, err = decodeString(raw, "locationId"); err != nil {
			return nil, err
		}
	}
	if _, ok := findLocation(location); !ok {
		return nil, invalid("Location " + location + " is not supported; use one of " + locationIDs() + ".")
	}
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err == nil {
			return nil, exists("An App Engine application already exists for project " + e.cfg.Project + "; it cannot be created again or deleted.")
		} else if !isNotFound(err) {
			return nil, err
		}
		now := timestamp(e.cfg.Now())
		if _, err := tx.Exec("INSERT INTO gcp_appengine_applications(project, location, serving_status, created, updated) VALUES(?, ?, 'SERVING', ?, ?)", e.cfg.Project, location, now, now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec("INSERT INTO gcp_appengine_firewall(project, priority, action, source_range, description) VALUES(?, ?, 'ALLOW', '*', 'The default action.')", e.cfg.Project, defaultRule); err != nil {
			return nil, err
		}
		a := application{Location: location, ServingStatus: "SERVING", Created: now, Updated: now}
		return e.operation(tx, q.subject, "Applications.CreateApplication", "apps/"+e.cfg.Project, typed("appengine.v1.Application", e.applicationResource(a)))
	})
}

func locationIDs() string {
	ids := []string{}
	for _, l := range Locations {
		ids = append(ids, l.ID)
	}
	return strings.Join(ids, ", ")
}

func isNotFound(err error) bool {
	var s *status
	return errors.As(err, &s) && s.code == "NOT_FOUND"
}

func (e *Engine) getApplication(q *request) (any, error) {
	switch q.param("includeExtraData") {
	case "", "INCLUDE_EXTRA_DATA_UNSPECIFIED", "INCLUDE_EXTRA_DATA_NONE":
	default:
		return nil, invalid("includeExtraData is not supported by the local App Engine simulator.")
	}
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		return e.applicationResource(a), nil
	})
}

func (e *Engine) patchApplication(q *request) (any, error) {
	paths, err := mask(q, "servingStatus")
	if err != nil {
		return nil, err
	}
	body, err := e.patchBody(q.body, "Application", paths)
	if err != nil {
		return nil, err
	}
	value := ""
	if raw, ok := body["servingStatus"]; ok {
		if value, err = decodeString(raw, "servingStatus"); err != nil {
			return nil, err
		}
	}
	if value != "SERVING" && value != "USER_DISABLED" {
		return nil, invalid("servingStatus must be SERVING or USER_DISABLED.")
	}
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		a.ServingStatus, a.Updated = value, timestamp(e.cfg.Now())
		if _, err = tx.Exec("UPDATE gcp_appengine_applications SET serving_status = ?, updated = ? WHERE project = ?", a.ServingStatus, a.Updated, e.cfg.Project); err != nil {
			return nil, err
		}
		return e.operation(tx, q.subject, "Applications.UpdateApplication", "apps/"+e.cfg.Project, typed("appengine.v1.Application", e.applicationResource(a)))
	})
}

func (e *Engine) locationResource(l Location) map[string]any {
	return map[string]any{"name": "apps/" + e.cfg.Project + "/locations/" + l.ID, "locationId": l.ID, "displayName": l.Display,
		"labels": map[string]any{"cloud.googleapis.com/region": l.Region},
		"metadata": map[string]any{"@type": "type.googleapis.com/google.appengine.v1.LocationMetadata", "standardEnvironmentAvailable": true,
			"flexibleEnvironmentAvailable": false, "searchApiAvailable": false}}
}

func (e *Engine) listLocations(q *request) (any, error) {
	if q.param("filter") != "" || q.param("extraLocationTypes") != "" {
		return nil, invalid("filter and extraLocationTypes are not supported by the local App Engine simulator.")
	}
	size, after, err := pageParams(q)
	if err != nil {
		return nil, err
	}
	items, token := page(Locations, func(l Location) string { return l.ID }, size, after)
	out := []any{}
	for _, l := range items {
		out = append(out, e.locationResource(l))
	}
	return listResponse("locations", out, token), nil
}

func (e *Engine) getLocation(q *request) (any, error) {
	l, ok := findLocation(q.path["locationsId"])
	if !ok {
		return nil, notFound("Location " + q.path["locationsId"] + " is not available to this application.")
	}
	return e.locationResource(l), nil
}

func (e *Engine) listOperations(q *request) (any, error) {
	if q.param("filter") != "" || q.param("returnPartialSuccess") == "true" {
		return nil, invalid("filter and returnPartialSuccess are not supported by the local App Engine simulator.")
	}
	size, after, err := pageParams(q)
	if err != nil {
		return nil, err
	}
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		start := int64(0)
		if after != "" {
			if start, err = strconv.ParseInt(after, 10, 64); err != nil {
				return nil, invalid("Invalid page token.")
			}
		}
		rows, err := tx.Query("SELECT seq, document FROM gcp_appengine_operations WHERE project = ? AND seq > ? ORDER BY seq LIMIT ?", e.cfg.Project, start, size+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out, last := []any{}, int64(0)
		for rows.Next() {
			var seq int64
			var document string
			if err := rows.Scan(&seq, &document); err != nil {
				return nil, err
			}
			if len(out) == size {
				return listResponse("operations", out, pageToken(strconv.FormatInt(last, 10))), rows.Err()
			}
			var op map[string]any
			if err := json.Unmarshal([]byte(document), &op); err != nil {
				return nil, err
			}
			out, last = append(out, op), seq
		}
		return listResponse("operations", out, ""), rows.Err()
	})
}

func (e *Engine) getOperation(q *request) (any, error) {
	name := "apps/" + e.cfg.Project + "/operations/" + q.path["operationsId"]
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		var document string
		err := tx.QueryRow("SELECT document FROM gcp_appengine_operations WHERE project = ? AND id = ?", e.cfg.Project, name).Scan(&document)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("Operation " + name + " was not found or has been pruned.")
		}
		if err != nil {
			return nil, err
		}
		var op map[string]any
		return op, json.Unmarshal([]byte(document), &op)
	})
}

// pageParams reads pageSize (default 50, capped at 1,000) and an opaque
// pageToken that carries the last key of the previous page.
func pageParams(q *request) (int, string, error) {
	size := defaultPageSize
	if raw := q.param("pageSize"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 0, "", invalid("pageSize must be a non-negative integer.")
		}
		if n > 0 {
			size = min(n, maxPageSize)
		}
	}
	after := ""
	if token := q.param("pageToken"); token != "" {
		data, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(data) == 0 || len(data) > 256 {
			return 0, "", invalid("Invalid page token.")
		}
		after = string(data)
	}
	return size, after, nil
}

func pageToken(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) }

// page returns the items after the token key, in key order.
func page[T any](items []T, key func(T) string, size int, after string) ([]T, string) {
	sorted := append([]T(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return key(sorted[i]) < key(sorted[j]) })
	out := []T{}
	for _, item := range sorted {
		if key(item) <= after {
			continue
		}
		if len(out) == size {
			return out, pageToken(key(out[len(out)-1]))
		}
		out = append(out, item)
	}
	return out, ""
}

func listResponse(field string, items []any, token string) map[string]any {
	out := map[string]any{field: items}
	if token != "" {
		out["nextPageToken"] = token
	}
	return out
}

func priorityKey(p int64) string { return fmt.Sprintf("%010d", p) }
