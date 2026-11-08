package arm

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const identityType = "microsoft.managedidentity/userassignedidentities"

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (q *request) rt() *boundType { return q.b.rt }

// groupKey is the lower-case resource group ID of the request.
func (q *request) groupKey() string {
	return strings.ToLower("/subscriptions/" + q.e.cfg.Subscription + "/resourceGroups/" + q.group)
}

func (q *request) subscriptionKey() string { return "/subscriptions/" + q.e.cfg.Subscription }

// chain lists the type and name pairs of the request, outermost first.
func (q *request) chain() []*boundType {
	var out []*boundType
	for t := q.rt(); t != nil; t = t.parent {
		out = append([]*boundType{t}, out...)
	}
	return out
}

// keyAt is the lower-case ID of the n-th resource in the request chain.
func (q *request) keyAt(n int) string {
	if q.rt().group {
		return q.groupKey()
	}
	chain := q.chain()
	id := q.groupKey() + "/providers/" + strings.ToLower(q.b.provider.spec.Namespace)
	for i := 0; i <= n; i++ {
		id += "/" + strings.ToLower(lastSegment(chain[i].spec.Name)) + "/" + strings.ToLower(q.names[i])
	}
	return id
}

func lastSegment(name string) string { return name[strings.LastIndex(name, "/")+1:] }

// key is the lower-case ID of the addressed resource.
func (q *request) key() string { return q.keyAt(len(q.names) - 1) }

// parentKey is the lower-case ID that scopes the addressed type.
func (q *request) parentKey() string {
	switch {
	case q.rt().group:
		return q.subscriptionKey()
	case q.rt().parent == nil:
		return q.groupKey()
	}
	return q.keyAt(len(q.chain()) - 2)
}

// display names a resource as ARM errors do, such as
// "Microsoft.Network/virtualNetworks/vnet/subnets/default".
func (q *request) display() string {
	chain := q.chain()
	out := q.b.provider.spec.Namespace
	for i, t := range chain {
		out += "/" + lastSegment(t.spec.Name)
		if i < len(q.names) {
			out += "/" + q.names[i]
		}
	}
	return out
}

func (q *request) execute() (int, any, error) {
	switch q.b.binding.Kind {
	case Put:
		return q.put()
	case Get:
		r, err := q.read(q.e.store.db)
		if err != nil {
			return 0, nil, err
		}
		return 200, r.Document, nil
	case Head:
		if _, err := find(q.e.store.db, q.key()); errors.Is(err, sql.ErrNoRows) {
			return 404, nil, nil
		} else if err != nil {
			return 0, nil, err
		}
		return 204, nil, nil
	case Patch:
		return q.patch()
	case Delete:
		return q.remove()
	}
	return q.list()
}

// scopeExists distinguishes a missing resource group or parent from a
// missing resource, as ARM error codes do.
func (q *request) scopeExists(db querier, items int) error {
	if q.rt().group {
		return nil
	}
	if _, err := find(db, q.groupKey()); errors.Is(err, sql.ErrNoRows) {
		return fail(404, "ResourceGroupNotFound", "Resource group '%s' could not be found.", q.group)
	} else if err != nil {
		return err
	}
	for i := 0; i < items; i++ {
		if _, err := find(db, q.keyAt(i)); errors.Is(err, sql.ErrNoRows) {
			return fail(404, "ParentResourceNotFound", "Can not perform requested operation on nested resource. Parent resource '%s' not found.", q.names[i])
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (q *request) notFound() error {
	if q.rt().group {
		return fail(404, "ResourceGroupNotFound", "Resource group '%s' could not be found.", q.group)
	}
	return fail(404, "ResourceNotFound", "The Resource '%s' under resource group '%s' was not found.", q.display(), q.group)
}

// read returns the addressed resource or a scope-aware 404.
func (q *request) read(db querier) (row, error) {
	r, err := find(db, q.key())
	if errors.Is(err, sql.ErrNoRows) {
		if err := q.scopeExists(db, len(q.names)-1); err != nil {
			return row{}, err
		}
		return row{}, q.notFound()
	}
	return r, err
}

// identityFields requires writable id and name members to name the target.
func (q *request) identityFields(body map[string]any) error {
	if id, ok := body["id"].(string); ok {
		if !strings.EqualFold(id, q.key()) {
			return invalidContent("member 'id' does not match the request URI.")
		}
		delete(body, "id")
	}
	if name, ok := body["name"].(string); ok {
		if !strings.EqualFold(name, q.names[len(q.names)-1]) {
			return fail(400, "InvalidRequestContent", "Renaming a resource is not supported; member 'name' must match the request URI.")
		}
		delete(body, "name")
	}
	return nil
}

func present(body map[string]any, dotted string) bool {
	_, ok := lookup(body, dotted)
	return ok
}

// admitted refuses unsupported and secret members of a request body.
func (q *request) admitted(body map[string]any) error {
	t := q.rt().spec
	fields := make([]string, 0, len(t.Unsupported))
	for field := range t.Unsupported {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		if present(body, field) {
			return fail(400, "InvalidRequestContent", "Member '%s' is not supported by the simulator: %s", field, t.Unsupported[field])
		}
	}
	for _, field := range t.Secrets {
		if present(body, field) {
			return fail(400, "InvalidRequestContent", "Member '%s' would carry a secret; the simulator never accepts or stores secret values.", field)
		}
	}
	if tags, ok := body["tags"].(map[string]any); ok {
		if len(tags) > 50 {
			return fail(400, "InvalidTagCount", "A resource can have at most 50 tags.")
		}
		for name, value := range tags {
			text, _ := value.(string)
			if name == "" || len(name) > 512 || len(text) > 256 || strings.ContainsAny(name, "<>%&\\?/") {
				return fail(400, "InvalidTagNameCharacters", "Tag names must be 1-512 characters without <>%%&\\?/ and values at most 256 characters.")
			}
		}
	}
	return nil
}

func (q *request) put() (int, any, error) {
	if err := q.identityFields(q.body); err != nil {
		return 0, nil, err
	}
	if err := q.admitted(q.body); err != nil {
		return 0, nil, err
	}
	tx, err := q.e.store.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	existing, err := find(tx, q.key())
	creating := errors.Is(err, sql.ErrNoRows)
	if err != nil && !creating {
		return 0, nil, err
	}
	if creating {
		if p := q.rt().spec.NamePattern; p != nil && !p.MatchString(q.names[len(q.names)-1]) {
			return 0, nil, fail(400, "InvalidResourceName", "Resource name '%s' is invalid. It must match %s.", q.names[len(q.names)-1], p.String())
		}
	}
	var previous map[string]any
	if !creating {
		previous = existing.Document
	}
	status, out, err := q.write(tx, q.body, previous, existing)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return status, out, nil
}

// write builds, checks and stores the next document inside tx. previous is
// nil on create.
func (q *request) write(tx *sql.Tx, content, previous map[string]any, existing row) (int, any, error) {
	t := q.rt()
	parentKey := q.parentKey()
	var parent map[string]any
	if !t.group {
		group, err := find(tx, q.groupKey())
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, fail(404, "ResourceGroupNotFound", "Resource group '%s' could not be found.", q.group)
		} else if err != nil {
			return 0, nil, err
		}
		parent = group.Document
		// The parent is re-read inside the write transaction, so a
		// concurrent deletion cannot leave an orphaned child.
		if t.parent != nil {
			p, err := find(tx, parentKey)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil, fail(404, "ParentResourceNotFound", "Can not perform requested operation on nested resource. Parent resource '%s' not found.", q.names[len(q.names)-2])
			} else if err != nil {
				return 0, nil, err
			}
			parent = p.Document
		}
	}
	if previous == nil {
		if err := q.capacity(tx, parentKey); err != nil {
			return 0, nil, err
		}
	}
	document, err := q.build(content, previous, parent)
	if err != nil {
		return 0, nil, err
	}
	targets, err := q.references(tx, document)
	if err != nil {
		return 0, nil, err
	}
	if err := q.identity(tx, document, previous); err != nil {
		return 0, nil, err
	}
	if t.spec.Check != nil {
		change := &Change{Document: document, Previous: previous, Tenant: q.e.cfg.Tenant, Name: q.names[len(q.names)-1], Children: map[string][]map[string]any{}}
		for _, l := range q.e.cfg.Locations {
			change.Locations = append(change.Locations, l)
		}
		if t.parent != nil {
			change.Parent = parent
		}
		siblings, err := rows(tx, "parent=? AND type=? AND key<>? ORDER BY seq", parentKey, t.key, q.key())
		if err != nil {
			return 0, nil, err
		}
		for _, s := range siblings {
			change.Siblings = append(change.Siblings, s.Document)
		}
		for _, child := range t.children {
			list, err := rows(tx, "parent=? AND type=? ORDER BY seq", q.key(), child.key)
			if err != nil {
				return 0, nil, err
			}
			for _, c := range list {
				change.Children[child.spec.Name] = append(change.Children[child.spec.Name], c.Document)
			}
		}
		if err := t.spec.Check(change); err != nil {
			return 0, nil, err
		}
	}
	id, _ := document["id"].(string)
	document = q.project(document)
	data, err := encode(document)
	if err != nil {
		return 0, nil, err
	}
	status := 200
	if previous == nil {
		seq, err := nextSeq(tx)
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec("INSERT INTO azure_arm_resources VALUES(?,?,?,?,?,?,?,1)", q.key(), id, q.e.cfg.Subscription, parentKey, t.key, data, seq); err != nil {
			return 0, nil, err
		}
		if q.b.op.Responses["201"] != "" {
			status = 201
		}
	} else if err := exactlyOne(tx.Exec("UPDATE azure_arm_resources SET document=?, generation=generation+1 WHERE key=? AND generation=?", data, existing.Key, existing.Generation)); err != nil {
		return 0, nil, err
	}
	if _, err := tx.Exec("DELETE FROM azure_arm_references WHERE source=?", q.key()); err != nil {
		return 0, nil, err
	}
	for _, target := range targets {
		if _, err := tx.Exec("INSERT OR IGNORE INTO azure_arm_references VALUES(?,?,?,?)", q.key(), target.key, target.path, target.inUse); err != nil {
			return 0, nil, err
		}
	}
	if err := withinBudget(tx); err != nil {
		return 0, nil, err
	}
	return status, document, nil
}

// capacity enforces the per-parent, global-name and whole-catalog limits.
func (q *request) capacity(tx *sql.Tx, parentKey string) error {
	t := q.rt()
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM azure_arm_resources WHERE parent=? AND type=?", parentKey, t.key).Scan(&count); err != nil {
		return err
	}
	if count >= t.spec.Limit {
		return fail(409, "QuotaExceeded", "The simulator allows at most %d %s per parent scope.", t.spec.Limit, t.full)
	}
	if err := tx.QueryRow("SELECT count(*) FROM azure_arm_resources").Scan(&count); err != nil {
		return err
	}
	if count >= maxResources {
		return fail(409, "QuotaExceeded", "The simulator allows at most %d Azure Resource Manager resources.", maxResources)
	}
	if t.spec.GlobalName {
		suffix := "/" + strings.ToLower(lastSegment(t.spec.Name)) + "/" + strings.ToLower(q.names[len(q.names)-1])
		if err := tx.QueryRow("SELECT count(*) FROM azure_arm_resources WHERE type=? AND key LIKE ? ESCAPE '\\'", t.key, "%"+escapeLike(suffix)).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fail(409, "Conflict", "The name '%s' is already in use by another %s in this environment.", q.names[len(q.names)-1], t.full)
		}
	}
	return nil
}

// build assembles the next stored document from validated content and the
// server-owned members of the previous document.
func (q *request) build(content, previous, parent map[string]any) (map[string]any, error) {
	t := q.rt()
	m := t.provider.model
	response := m.merged(m.Definitions[t.response])
	document := map[string]any{}
	for k, v := range content {
		document[k] = v
	}
	if previous != nil {
		if err := q.immutable(document, previous, m, &Schema{Ref: t.request}, ""); err != nil {
			return nil, err
		}
	}
	name := q.names[len(q.names)-1]
	id := ""
	switch {
	case t.group:
		id = "/subscriptions/" + q.e.cfg.Subscription + "/resourceGroups/" + name
	case t.parent == nil:
		id = parent["id"].(string) + "/providers/" + t.provider.spec.Namespace + "/" + t.spec.Name + "/" + name
	default:
		id = parent["id"].(string) + "/" + lastSegment(t.spec.Name) + "/" + name
	}
	if prior, ok := previous["id"].(string); ok {
		id = prior
	}
	if prior, ok := previous["name"].(string); ok {
		name = prior
	}
	document["id"], document["name"] = id, name
	if _, ok := response.properties["type"]; ok {
		document["type"] = t.full
	}
	if location, ok := response.properties["location"]; ok {
		if readOnly(location, m.resolve(location)) {
			if parent != nil && parent["location"] != nil {
				document["location"] = parent["location"]
			}
		} else if t.parent == nil {
			text, _ := document["location"].(string)
			if text == "" {
				return nil, fail(400, "LocationRequired", "The location property is required for this definition.")
			}
			canonical, ok := q.e.locations[normalizeLocation(text)]
			if !ok {
				return nil, fail(400, "LocationNotAvailableForResourceType", "The provided location '%s' is not available for resource type '%s'. List of available regions for the resource type is '%s'.", text, t.full, strings.Join(q.e.cfg.Locations, ","))
			}
			document["location"] = canonical
			if previous != nil && t.group && !strings.EqualFold(fmt.Sprint(previous["location"]), canonical) {
				return nil, fail(409, "InvalidResourceGroupLocation", "Invalid resource group location '%s'. The resource group '%s' already exists in location '%s'.", canonical, name, previous["location"])
			}
			if previous != nil && !strings.EqualFold(fmt.Sprint(previous["location"]), canonical) {
				return nil, fail(409, "InvalidResourceLocation", "The resource '%s' already exists in location '%s'. A resource with the same name cannot be created in location '%s'.", name, previous["location"], canonical)
			}
		}
	}
	if _, ok := response.properties["systemData"]; ok {
		created, createdBy := q.now.Format(time.RFC3339Nano), q.subject
		if prior, ok := previous["systemData"].(map[string]any); ok {
			created, _ = prior["createdAt"].(string)
			createdBy, _ = prior["createdBy"].(string)
		}
		document["systemData"] = map[string]any{"createdBy": createdBy, "createdByType": "User", "createdAt": created,
			"lastModifiedBy": q.subject, "lastModifiedByType": "User", "lastModifiedAt": q.now.Format(time.RFC3339Nano)}
	}
	if etag, ok := response.properties["etag"]; ok && readOnly(etag, m.resolve(etag)) {
		document["etag"] = `W/"` + newUUID() + `"`
	}
	if props, ok := response.properties["properties"]; ok {
		if _, has := m.merged(props).properties["provisioningState"]; has {
			inner, _ := document["properties"].(map[string]any)
			if inner == nil {
				inner = map[string]any{}
				document["properties"] = inner
			}
			inner["provisioningState"] = "Succeeded"
		}
	}
	for _, field := range sortedKeys(t.spec.Generate) {
		value := t.spec.Generate[field]
		prior, hadPrior := lookup(previous, field)
		current, supplied := lookup(document, field)
		switch value {
		case "$uuid":
			if !supplied {
				if hadPrior {
					current = prior
				} else {
					current = newUUID()
				}
			}
		case "$tenant":
			current = q.e.cfg.Tenant
		case "$now":
			current = q.now.Format(time.RFC3339Nano)
		case "$created":
			current = q.now.Format(time.RFC3339Nano)
			if hadPrior {
				current = prior
			}
		default:
			if !supplied {
				current = value
			}
		}
		assign(document, field, current)
	}
	return document, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// immutable refuses changes to create-only members and keeps their stored
// values when an update omits them.
func (q *request) immutable(document, previous map[string]any, m *Model, s *Schema, at string) error {
	v := m.merged(s)
	for name, field := range v.properties {
		path := joinPath(at, name)
		if path == "location" {
			// build compares normalized locations and reports InvalidResourceLocation.
			continue
		}
		old, had := previous[name]
		value, has := document[name]
		if createOnly(field) {
			if has && had && !equalJSON(old, value) {
				return fail(400, "PropertyChangeNotAllowed", "Changing property '%s' is not allowed.", path)
			}
			if !has && had {
				document[name] = old
			}
			continue
		}
		inner, ok := value.(map[string]any)
		prior, okPrior := old.(map[string]any)
		if !okPrior || !m.isObject(field) || len(m.merged(field).properties) == 0 || has && !ok {
			continue
		}
		if !has {
			inner = map[string]any{}
		}
		if err := q.immutable(inner, prior, m, field, path); err != nil {
			return err
		}
		if !has && len(inner) > 0 {
			document[name] = inner
		}
	}
	return nil
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return strings.EqualFold(string(x), string(y))
}

// project keeps only the members of the official response definition and
// drops write-only members, so responses never echo what ARM would not.
func (q *request) project(document map[string]any) map[string]any {
	m := q.rt().provider.model
	out, _ := projectValue(m, &Schema{Ref: q.rt().response}, document, 0).(map[string]any)
	return out
}

func projectValue(m *Model, property *Schema, value any, depth int) any {
	s := m.resolve(property)
	if s == nil || depth > maxDepth {
		return value
	}
	switch typed := value.(type) {
	case map[string]any:
		if !m.isObject(s) {
			return value
		}
		v := m.merged(s)
		if s.Discriminator != "" {
			if kind, ok := typed[s.Discriminator].(string); ok {
				for key, sub := range s.Subtypes {
					if strings.EqualFold(key, kind) {
						v = m.merged(m.Definitions[sub])
					}
				}
			}
		}
		if len(v.properties) == 0 {
			if s.additional != nil {
				out := map[string]any{}
				for k, item := range typed {
					out[k] = projectValue(m, s.additional, item, depth+1)
				}
				return out
			}
			return value
		}
		out := map[string]any{}
		for k, item := range typed {
			field, ok := v.properties[k]
			switch {
			case ok && writeOnly(field):
			case ok:
				out[k] = projectValue(m, field, item, depth+1)
			case s.additional != nil:
				out[k] = projectValue(m, s.additional, item, depth+1)
			case s.openMap:
				out[k] = item
			}
		}
		return out
	case []any:
		if s.Items == nil {
			return value
		}
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = projectValue(m, s.Items, item, depth+1)
		}
		return out
	}
	return value
}

type target struct {
	key   string
	path  string
	inUse string
}

// parseID returns the lower-case key and type key of an ARM resource ID in
// the configured subscription.
func (q *request) parseID(id string) (string, string, bool) {
	parts := splitPath(strings.ToLower(id))
	if len(parts) < 4 || parts[0] != "subscriptions" || parts[1] != q.e.cfg.Subscription || parts[2] != "resourcegroups" || !strings.HasPrefix(id, "/") {
		return "", "", false
	}
	if len(parts) == 4 {
		return "/" + strings.Join(parts, "/"), "microsoft.resources/resourcegroups", true
	}
	if len(parts) < 8 || parts[4] != "providers" || len(parts)%2 != 0 {
		return "", "", false
	}
	types := []string{parts[5]}
	for i := 6; i < len(parts); i += 2 {
		types = append(types, parts[i])
	}
	return "/" + strings.Join(parts, "/"), strings.Join(types, "/"), true
}

// references resolves every reviewed reference to a live catalog resource
// inside tx.
func (q *request) references(tx *sql.Tx, document map[string]any) ([]target, error) {
	var out []target
	for _, ref := range q.rt().spec.Refs {
		for _, id := range collect(document, splitRef(ref.Path)) {
			key, kind, ok := q.parseID(id)
			if ok && kind == strings.ToLower(ref.Type) {
				var stored string
				err := tx.QueryRow("SELECT type FROM azure_arm_resources WHERE key=?", key).Scan(&stored)
				if err == nil && stored == kind {
					out = append(out, target{key, ref.Path, ref.InUse})
					continue
				}
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
			}
			return nil, fail(400, "InvalidResourceReference", "Resource '%s' referenced by member '%s' was not found as a %s in this subscription.", id, ref.Path, ref.Type)
		}
	}
	return out, nil
}

// identity assigns system-assigned principals and resolves user-assigned
// identities to live Microsoft.ManagedIdentity resources.
func (q *request) identity(tx *sql.Tx, document, previous map[string]any) error {
	identity, ok := document["identity"].(map[string]any)
	if !ok {
		return nil
	}
	kind, _ := identity["type"].(string)
	lower := strings.ToLower(kind)
	system, user := strings.Contains(lower, "systemassigned"), strings.Contains(lower, "userassigned")
	assigned, _ := identity["userAssignedIdentities"].(map[string]any)
	if user != (len(assigned) > 0) {
		return fail(400, "InvalidIdentity", "Identity type '%s' must list user-assigned identities exactly when it includes UserAssigned.", kind)
	}
	if system {
		principal := newUUID()
		if prior, ok := previous["identity"].(map[string]any); ok {
			if id, ok := prior["principalId"].(string); ok {
				principal = id
			}
		}
		identity["principalId"], identity["tenantId"] = principal, q.e.cfg.Tenant
	} else {
		delete(identity, "principalId")
		delete(identity, "tenantId")
	}
	for id := range assigned {
		key, kindKey, ok := q.parseID(id)
		var stored row
		var err error
		if ok && kindKey == identityType {
			stored, err = find(tx, key)
		}
		if !ok || kindKey != identityType || errors.Is(err, sql.ErrNoRows) {
			return fail(400, "InvalidIdentity", "User-assigned identity '%s' was not found in this subscription.", id)
		} else if err != nil {
			return err
		}
		props, _ := stored.Document["properties"].(map[string]any)
		assigned[id] = map[string]any{"principalId": props["principalId"], "clientId": props["clientId"]}
	}
	return nil
}

// merge applies an ARM PATCH body: objects merge member by member, while
// maps such as tags, arrays and scalars replace the stored value.
func merge(m *Model, s *Schema, stored, patch map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range stored {
		out[k] = v
	}
	v := m.merged(s)
	for k, value := range patch {
		field := v.properties[k]
		inner, isObject := value.(map[string]any)
		prior, hadObject := out[k].(map[string]any)
		if field != nil && isObject && hadObject && len(m.merged(field).properties) > 0 {
			out[k] = merge(m, field, prior, inner)
			continue
		}
		out[k] = value
	}
	return out
}

func (q *request) patch() (int, any, error) {
	if err := q.identityFields(q.body); err != nil {
		return 0, nil, err
	}
	if err := q.admitted(q.body); err != nil {
		return 0, nil, err
	}
	tx, err := q.e.store.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	existing, err := q.read(tx)
	if err != nil {
		return 0, nil, err
	}
	m := q.rt().provider.model
	if err := q.immutable(q.body, existing.Document, m, &Schema{Ref: q.b.body.Schema}, ""); err != nil {
		return 0, nil, err
	}
	if location, ok := q.body["location"].(string); ok && normalizeLocation(location) != normalizeLocation(fmt.Sprint(existing.Document["location"])) {
		return 0, nil, fail(400, "PropertyChangeNotAllowed", "Changing property 'location' is not allowed.")
	}
	merged := merge(m, &Schema{Ref: q.rt().request}, existing.Document, q.body)
	_, out, err := q.write(tx, merged, existing.Document, existing)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return 200, out, nil
}

func (q *request) remove() (int, any, error) {
	tx, err := q.e.store.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	responses := q.b.op.Responses
	_, err = find(tx, q.key())
	if errors.Is(err, sql.ErrNoRows) {
		if err := q.scopeExists(tx, len(q.names)-1); err != nil {
			return 0, nil, err
		}
		if _, ok := responses["204"]; ok && !q.rt().group {
			return 204, nil, nil
		}
		return 0, nil, q.notFound()
	} else if err != nil {
		return 0, nil, err
	}
	key := q.key()
	below := escapeLike(key) + "/%"
	blocked := tx.QueryRow(`SELECT source, path, in_use FROM azure_arm_references
 WHERE in_use<>'' AND (target=? OR target LIKE ? ESCAPE '\') AND NOT (source=? OR source LIKE ? ESCAPE '\') ORDER BY source, path LIMIT 1`, key, below, key, below)
	var source, path, inUse string
	if err := blocked.Scan(&source, &path, &inUse); err == nil {
		return 0, nil, fail(409, inUse, "The resource cannot be deleted because '%s' references it through member '%s'.", source, path)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	// Non-blocking references (such as identity assignments) are released;
	// the referring document keeps the ID, as Azure does.
	if _, err := tx.Exec(`DELETE FROM azure_arm_references WHERE target=? OR target LIKE ? ESCAPE '\' OR source=? OR source LIKE ? ESCAPE '\'`, key, below, key, below); err != nil {
		return 0, nil, err
	}
	if _, err := tx.Exec(`DELETE FROM azure_arm_resources WHERE key LIKE ? ESCAPE '\'`, below); err != nil {
		return 0, nil, err
	}
	if err := exactlyOne(tx.Exec("DELETE FROM azure_arm_resources WHERE key=?", key)); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	for _, code := range []string{"200", "204", "202"} {
		if _, ok := responses[code]; ok {
			status, _ := strconv.Atoi(code)
			return status, nil, nil
		}
	}
	return 200, nil, nil
}

type pageToken struct {
	Scope string `json:"s"`
	Type  string `json:"t"`
	After int64  `json:"a"`
}

func (q *request) list() (int, any, error) {
	t := q.rt()
	db := q.e.store.db
	var column, scope string
	switch {
	case t.group:
		column, scope = "parent", q.subscriptionKey()
	case q.b.group == "":
		column, scope = "subscription", q.e.cfg.Subscription
	default:
		if err := q.scopeExists(db, len(q.names)); err != nil {
			return 0, nil, err
		}
		column, scope = "parent", q.parentKey()
	}
	where := column + "=? AND type=? AND seq>? ORDER BY seq"
	size := int64(defaultPageSize)
	if top, ok := q.params["$top"].(int64); ok {
		if top < 1 {
			return 0, nil, fail(400, "InvalidQueryParameterValue", "Query parameter '$top' must be positive.")
		}
		size = min(top, maxPageSize)
	}
	after := int64(0)
	if q.token != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.token)
		var token pageToken
		if err != nil || json.Unmarshal(raw, &token) != nil || token.Scope != column+":"+scope || token.Type != t.key {
			return 0, nil, fail(400, "InvalidSkipToken", "The $skiptoken is invalid for this list operation.")
		}
		after = token.After
	}
	list, err := rows(db, where, scope, t.key, after)
	if err != nil {
		return 0, nil, err
	}
	if skip, ok := q.params["$skip"].(int64); ok && skip > 0 {
		list = list[min(int(skip), len(list)):]
	}
	pageable := q.b.op.Pageable
	items := []any{}
	next := ""
	for i, r := range list {
		if int64(len(items)) == size && pageable.NextLinkName != "" {
			data, _ := json.Marshal(pageToken{column + ":" + scope, t.key, list[i-1].Seq})
			next = q.nextLink(base64.RawURLEncoding.EncodeToString(data))
			break
		}
		items = append(items, r.Document)
	}
	out := map[string]any{pageable.ItemName: items}
	if next != "" {
		out[pageable.NextLinkName] = next
	}
	return 200, out, nil
}

// nextLink is the absolute URL of the following page, as ARM returns it.
func (q *request) nextLink(token string) string {
	scheme := "http"
	if q.r.TLS != nil {
		scheme = "https"
	}
	query := url.Values{"api-version": {q.b.provider.spec.APIVersion}, "$skiptoken": {token}}
	if top, ok := q.params["$top"].(int64); ok {
		query.Set("$top", strconv.FormatInt(top, 10))
	}
	return scheme + "://" + q.r.Host + q.e.cfg.BasePath + "/" + strings.Join(splitPath(q.r.URL.EscapedPath()), "/") + "?" + query.Encode()
}
