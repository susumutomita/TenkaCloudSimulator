package catalog

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var resourceID = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

type request struct {
	e        *Engine
	b        *binding
	bound    *Binding
	method   *Method
	subject  string
	w        http.ResponseWriter
	path     string
	project  string
	location string
	params   map[string]any
	body     map[string]any
	// scopePath is global, regions/{region} or zones/{zone} for the
	// "zonal" scope; zone marks a zones/{zone} path.
	scopePath  string
	zone       bool
	targetName string
}

func (q *request) collection() *Collection { return q.b.api.collection(q.bound.Collection) }

func (q *request) permission() string {
	verbs := map[Kind]string{Create: "create", Get: "get", List: "list", Patch: "update", Delete: "delete", OperationGet: "get", OperationList: "list"}
	if q.bound.Kind == OperationGet || q.bound.Kind == OperationList {
		// The operations resource names the permission, such as
		// "operations" (AIP) or "zoneOperations" (Compute Engine).
		parts := strings.Split(q.method.ID, ".")
		return q.b.model.Name + "." + parts[len(parts)-2] + "." + verbs[q.bound.Kind]
	}
	if q.bound.Kind == Set {
		return q.collection().Permission + "." + q.verb()
	}
	return q.collection().Permission + "." + verbs[q.bound.Kind]
}

func (q *request) now() time.Time { return q.e.cfg.Now().UTC().Truncate(time.Millisecond) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func uuid() string {
	h := randomHex(16)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

func (q *request) execute() (any, error) {
	if strings.HasPrefix(q.path, "projects/"+q.e.cfg.ProjectNumber+"/") {
		q.path = "projects/" + q.e.cfg.Project + strings.TrimPrefix(q.path, "projects/"+q.e.cfg.ProjectNumber)
	}
	switch q.bound.Kind {
	case Create:
		return q.create()
	case Get:
		r, err := q.target()
		if err != nil {
			return nil, err
		}
		return r.Document, nil
	case List:
		return q.list()
	case Patch:
		return q.patch()
	case Delete:
		return q.remove()
	case Set:
		return q.set()
	case OperationGet:
		return q.operation()
	}
	return q.operations()
}

func (q *request) find(name string) (row, error) {
	r, err := findName(q.e.store.db, name)
	if errors.Is(err, sql.ErrNoRows) || err == nil && r.API != q.b.model.Name {
		return row{}, &status{"NOT_FOUND", fmt.Sprintf("Resource '%s' was not found.", name)}
	}
	return r, err
}

// target finds the resource the request path names in the bound collection.
func (q *request) target() (row, error) {
	r, err := q.find(q.path)
	if err == nil && r.Collection != q.bound.Collection {
		return row{}, &status{"NOT_FOUND", fmt.Sprintf("Resource '%s' was not found.", q.path)}
	}
	q.targetName = r.Name
	return r, err
}

// parentName validates the parent of a collection path and that it exists.
func (q *request) parentName(parent string) error {
	c := q.collection()
	if c.Parent == "" {
		if root := q.root(); parent != root {
			return invalid("Parent %q is not a valid %s parent.", parent, c.ID)
		}
		return nil
	}
	p, err := q.find(parent)
	if err != nil {
		return err
	}
	if p.Collection != c.Parent {
		return &status{"NOT_FOUND", fmt.Sprintf("Parent '%s' is not a %s resource.", parent, c.Parent)}
	}
	return nil
}

func (q *request) required(schema string) error {
	relaxed := map[string]bool{}
	for _, field := range q.collection().Relax {
		relaxed[field] = true
	}
	s := q.b.model.Schemas[schema]
	keys := make([]string, 0, len(s.Properties))
	for key := range s.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := s.Properties[key]
		if q.b.requiredFor(p, q.method.ID) && !p.ReadOnly && key != "name" && !relaxed[key] {
			if _, ok := q.body[key]; !ok {
				return invalid("Field '%s' is required.", key)
			}
		}
	}
	return nil
}

func (q *request) generated(value any, name, id, parent string) any {
	text, ok := value.(string)
	if !ok || !strings.HasPrefix(text, "$") {
		return value
	}
	switch text {
	case "$name":
		return name
	case "$now":
		return q.now().Format(time.RFC3339Nano)
	case "$uid":
		return uuid()
	case "$etag":
		return randomHex(8)
	case "$fingerprint":
		return fingerprint()
	case "$id":
		return id
	case "$project":
		return q.e.cfg.Project
	case "$location":
		return q.location
	case "$parent":
		return parent
	}
	return nil
}

func (q *request) idempotent(response func() (any, error)) (any, error) {
	requestID, _ := q.params["requestId"].(string)
	if requestID == "" || q.params["validateOnly"] == true {
		return response()
	}
	sum := sha256.Sum256([]byte(fmt.Sprint(q.path, q.params, mustJSON(q.body))))
	fingerprint := hex.EncodeToString(sum[:])
	var previous, stored string
	err := q.e.store.db.QueryRow("SELECT fingerprint, response FROM gcp_catalog_requests WHERE api=? AND method=? AND request_id=? AND created>?",
		q.b.model.Name, q.method.ID, requestID, q.now().Unix()-requestIDLifetime).Scan(&previous, &stored)
	if err == nil {
		if previous != fingerprint {
			return nil, invalid("requestId %q was already used with a different request.", requestID)
		}
		var out any
		_ = json.Unmarshal([]byte(stored), &out)
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	out, err := response()
	if err != nil {
		return nil, err
	}
	_, err = q.e.store.db.Exec("INSERT OR REPLACE INTO gcp_catalog_requests VALUES(?,?,?,?,?,?)", q.b.model.Name, requestID, q.method.ID, fingerprint, mustJSON(out), q.now().Unix())
	return out, err
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// respond wraps a result in a completed long-running operation when the
// official method returns one.
func (q *request) respond(result map[string]any, typeName string, tx *sql.Tx) (any, error) {
	if q.flatOperation() {
		return q.completed(result, tx)
	}
	if !q.longRunning() {
		return result, nil
	}
	scope := "projects/" + q.e.cfg.Project
	if q.location != "" {
		scope += "/locations/" + q.location
	}
	opName := scope + "/operations/operation-" + randomHex(10)
	response := map[string]any{"@type": "type.googleapis.com/google.protobuf.Empty"}
	if typeName != "" {
		response = map[string]any{"@type": q.b.api.TypeURL + typeName}
		for k, v := range result {
			response[k] = v
		}
	}
	op := map[string]any{"name": opName, "done": true, "response": response}
	return q.storeOperation(tx, opName, scope, op)
}

// storeOperation keeps a completed operation readable under its scope,
// pruning the oldest beyond the operation budget.
func (q *request) storeOperation(tx *sql.Tx, opName, scope string, op map[string]any) (any, error) {
	if q.params["validateOnly"] == true {
		return op, nil
	}
	seq, err := nextSeq(tx)
	if err != nil {
		return nil, err
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM gcp_catalog_operations").Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxOperations {
		if _, err := tx.Exec("DELETE FROM gcp_catalog_operations WHERE seq=(SELECT min(seq) FROM gcp_catalog_operations)"); err != nil {
			return nil, err
		}
	}
	if err := exactlyOne(tx.Exec("INSERT INTO gcp_catalog_operations VALUES(?,?,?,?,?)", q.b.model.Name, opName, scope, mustJSON(op), seq)); err != nil {
		return nil, err
	}
	return op, nil
}

func (q *request) longRunning() bool {
	schema := q.b.model.Schemas[q.method.Response]
	if schema == nil {
		return false
	}
	_, done := schema.Properties["done"]
	_, response := schema.Properties["response"]
	return done && response
}

func (q *request) create() (any, error) {
	c := q.collection()
	parent, segment := q.path, q.segment()
	if !strings.HasSuffix(parent, "/"+segment) {
		return nil, invalid("The create path must name the %s collection.", c.ID)
	}
	parent = strings.TrimSuffix(parent, "/"+segment)
	if err := q.parentName(parent); err != nil {
		return nil, err
	}
	// Server-generated IDs are opaque; client-chosen IDs follow AIP-122.
	id := randomHex(8)
	if c.IDParam != "" || c.IDField != "" {
		if c.IDParam != "" {
			id, _ = q.params[c.IDParam].(string)
			if id == "" {
				return nil, invalid("Query parameter %q is required.", c.IDParam)
			}
		} else if id, _ = q.body[c.IDField].(string); id == "" {
			return nil, invalid("Field '%s' is required.", c.IDField)
		}
		pattern := resourceID
		if c.IDPattern != nil {
			pattern = c.IDPattern
		}
		if !pattern.MatchString(id) {
			return nil, invalid("Resource ID %q must match %s.", id, pattern.String())
		}
	}
	if err := q.required(q.method.Request); err != nil {
		return nil, err
	}
	name := parent + "/" + segment + "/" + id
	q.targetName = name
	document := map[string]any{}
	schema := q.b.model.Schemas[c.Schema]
	defaults(schema, document)
	for k, v := range q.body {
		document[k] = v
	}
	document["name"] = name
	if c.ShortName {
		document["name"] = id
	}
	for key, value := range c.Generate {
		if _, supplied := document[key]; supplied && !strings.HasPrefix(fmt.Sprint(value), "$") {
			continue
		}
		if generated := q.generated(value, name, id, parent); generated != nil {
			document[key] = generated
		}
	}
	q.flatFields(schema, document, name)
	data, err := encode(document)
	if err != nil {
		return nil, err
	}
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if len(c.References)+len(c.Weak) > 0 || c.Conflict != nil {
			if err := q.resolveReferences(tx, c, document, true); err != nil {
				return nil, err
			}
			if err := q.conflicts(tx, c, name, document); err != nil {
				return nil, err
			}
			if data, err = encode(document); err != nil {
				return nil, err
			}
		}
		if _, err := findName(tx, name); err == nil {
			return nil, &status{"ALREADY_EXISTS", fmt.Sprintf("Resource '%s' already exists.", name)}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		limit := c.Limit
		if limit == 0 {
			limit = defaultLimit
		}
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM gcp_catalog_resources WHERE api=? AND parent=? AND collection=?", q.b.model.Name, parent, c.ID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= limit {
			return nil, &status{"RESOURCE_EXHAUSTED", fmt.Sprintf("The simulator allows at most %d %s per parent.", limit, c.ID)}
		}
		total, err := storedBytes(tx)
		if err != nil {
			return nil, err
		}
		if total > maxStoredBytes {
			return nil, &status{"RESOURCE_EXHAUSTED", "The simulator catalog metadata budget is exhausted."}
		}
		if q.params["validateOnly"] != true {
			seq, err := nextSeq(tx)
			if err != nil {
				return nil, err
			}
			if err := exactlyOne(tx.Exec("INSERT INTO gcp_catalog_resources VALUES(?,?,?,?,?,?,?,1)", q.b.model.Name, name, parent, c.ID, id, data, seq)); err != nil {
				return nil, err
			}
		}
		out, err := q.respond(document, c.Schema, tx)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}

type pageToken struct {
	Parent string `json:"p"`
	Kind   string `json:"k"`
	After  int64  `json:"a"`
}

func (q *request) list() (any, error) {
	c := q.collection()
	parent := strings.TrimSuffix(q.path, "/"+q.segment())
	if parent == q.path {
		return nil, invalid("The list path must name the %s collection.", c.ID)
	}
	for _, unsupported := range []string{"filter", "orderBy"} {
		if text, _ := q.params[unsupported].(string); text != "" {
			return nil, invalid("Parameter %q is not supported by the simulator.", unsupported)
		}
	}
	if err := q.parentName(parent); err != nil {
		return nil, err
	}
	size, after, err := q.page(parent, c.ID)
	if err != nil {
		return nil, err
	}
	rows, err := listChildren(q.e.store.db, q.b.model.Name, parent, c.ID)
	if err != nil {
		return nil, err
	}
	items := []any{}
	next := ""
	for _, r := range rows {
		if r.Seq <= after {
			continue
		}
		if int64(len(items)) == size {
			data, _ := json.Marshal(pageToken{parent, c.ID, items[len(items)-1].(map[string]any)["__seq"].(int64)})
			next = base64.RawURLEncoding.EncodeToString(data)
			break
		}
		doc := map[string]any{"__seq": r.Seq}
		for k, v := range r.Document {
			doc[k] = v
		}
		items = append(items, doc)
	}
	for _, item := range items {
		delete(item.(map[string]any), "__seq")
	}
	out := map[string]any{}
	response := q.b.model.Schemas[q.method.Response]
	defaults(response, out)
	if q.b.api.SelfLink != "" && response.Properties["selfLink"] != nil {
		out["selfLink"] = q.link(q.path)
	}
	for field, p := range response.Properties {
		if p.Type == "array" && p.Items != nil && p.Items.Ref == c.Schema {
			out[field] = items
		}
	}
	if next != "" {
		out["nextPageToken"] = next
	}
	return out, nil
}

// page reads pageSize (AIP) or maxResults (Compute Engine) and pageToken.
func (q *request) page(parent, kind string) (int64, int64, error) {
	size := int64(defaultPageSize)
	for _, key := range []string{"pageSize", "maxResults"} {
		if n, ok := q.params[key].(int64); ok {
			if n < 0 {
				return 0, 0, invalid("%s must not be negative.", key)
			}
			if n > 0 {
				size = min(n, maxPageSize)
			}
		}
	}
	after := int64(0)
	if text, _ := q.params["pageToken"].(string); text != "" {
		raw, err := base64.RawURLEncoding.DecodeString(text)
		var token pageToken
		if err != nil || json.Unmarshal(raw, &token) != nil || token.Parent != parent || token.Kind != kind {
			return 0, 0, invalid("Invalid page token.")
		}
		after = token.After
	}
	return size, after, nil
}

func (q *request) patch() (any, error) {
	c := q.collection()
	r, err := q.target()
	if err != nil {
		return nil, err
	}
	if q.params["allowMissing"] == true {
		return nil, invalid("allowMissing is not supported by the simulator.")
	}
	mutable := map[string]bool{}
	for _, f := range c.Mutable {
		mutable[f] = true
	}
	fields := []string{}
	mask, _ := q.params["updateMask"].(string)
	if mask != "" {
		fields = strings.Split(mask, ",")
	} else {
		for key := range q.body {
			fields = append(fields, key)
		}
		sort.Strings(fields)
	}
	if etag, ok := q.body["etag"].(string); ok && r.Document["etag"] != nil && etag != r.Document["etag"] {
		return nil, &status{"ABORTED", "The etag does not match the current resource."}
	}
	if err := q.guard(r.Document); err != nil {
		return nil, err
	}
	merge := q.b.api.SelfLink != "" && mask == ""
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "etag" || q.bound.Fingerprint != "" && field == q.bound.Fingerprint {
			continue
		}
		if c.IDField != "" && field == c.IDField && q.body[field] == r.ID {
			// Sending the unchanged body ID back is not an update.
			continue
		}
		if merge && !mutable[field] && mustJSON(q.body[field]) == mustJSON(r.Document[field]) {
			// A merge patch may restate an immutable field unchanged.
			continue
		}
		if !mutable[field] {
			return nil, invalid("Field %q cannot be updated by the simulator.", field)
		}
		value, ok := q.body[field]
		patch, isObject := value.(map[string]any)
		existing, wasObject := r.Document[field].(map[string]any)
		switch {
		case !ok:
			delete(r.Document, field)
		case merge && isObject && wasObject:
			mergePatch(existing, patch)
		default:
			r.Document[field] = value
		}
	}
	return q.save(c, r)
}

// set writes every writable request field of a custom method onto the
// resource; an omitted field is cleared, as setLabels clears labels.
func (q *request) set() (any, error) {
	c := q.collection()
	r, err := q.target()
	if err != nil {
		return nil, err
	}
	if err := q.guard(r.Document); err != nil {
		return nil, err
	}
	for field, p := range q.b.model.Schemas[q.method.Request].Properties {
		if p.ReadOnly || field == q.bound.Fingerprint {
			continue
		}
		if value, ok := q.body[field]; ok {
			r.Document[field] = value
		} else {
			delete(r.Document, field)
		}
	}
	return q.save(c, r)
}

// guard applies the binding's fingerprint and transition checks.
func (q *request) guard(current map[string]any) error {
	if fp := q.bound.Fingerprint; fp != "" {
		if value, ok := q.body[fp]; ok && value != current[fp] {
			return &status{"ABORTED", "The fingerprint does not match the current resource; read it again before retrying."}
		}
	}
	if q.bound.Transition != nil {
		return q.bound.Transition(current, q.body)
	}
	return nil
}

// save stores an updated resource and returns it or its operation.
func (q *request) save(c *Collection, r row) (any, error) {
	if fp := q.bound.Fingerprint; fp != "" {
		r.Document[fp] = fingerprint()
	}
	if _, ok := r.Document["updateTime"]; ok || c.Generate["updateTime"] != nil {
		r.Document["updateTime"] = q.now().Format(time.RFC3339Nano)
	}
	if _, ok := r.Document["etag"]; ok {
		r.Document["etag"] = randomHex(8)
	}
	data, err := encode(r.Document)
	if err != nil {
		return nil, err
	}
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if len(c.References) > 0 || c.Conflict != nil {
			if err := q.resolveReferences(tx, c, r.Document, false); err != nil {
				return nil, err
			}
			if err := q.conflicts(tx, c, r.Name, r.Document); err != nil {
				return nil, err
			}
			if data, err = encode(r.Document); err != nil {
				return nil, err
			}
		}
		if q.params["validateOnly"] != true {
			if err := exactlyOne(tx.Exec("UPDATE gcp_catalog_resources SET document=?, generation=generation+1 WHERE name=? AND generation=?", data, r.Name, r.Generation)); err != nil {
				return nil, err
			}
		}
		out, err := q.respond(r.Document, c.Schema, tx)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}

func (q *request) remove() (any, error) {
	c := q.collection()
	r, err := q.target()
	if err != nil {
		return nil, err
	}
	if etag, ok := q.params["etag"].(string); ok && etag != "" && etag != r.Document["etag"] {
		return nil, &status{"ABORTED", "The etag does not match the current resource."}
	}
	force := q.params["force"] == true
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		var children int
		if err := tx.QueryRow("SELECT count(*) FROM gcp_catalog_resources WHERE name LIKE ? ESCAPE '\\'", escapeLike(r.Name)+"/%").Scan(&children); err != nil {
			return nil, err
		}
		if children > 0 && !(force && c.Cascade) {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' still has child resources.", r.Name)}
		}
		if user, err := q.referrer(tx, r); err != nil {
			return nil, err
		} else if user != "" {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("The resource '%s' is already being used by '%s'.", r.Name, user)}
		}
		if q.params["validateOnly"] != true {
			if _, err := tx.Exec("DELETE FROM gcp_catalog_resources WHERE name LIKE ? ESCAPE '\\'", escapeLike(r.Name)+"/%"); err != nil {
				return nil, err
			}
			if err := exactlyOne(tx.Exec("DELETE FROM gcp_catalog_resources WHERE name=? AND generation=?", r.Name, r.Generation)); err != nil {
				return nil, err
			}
		}
		target := map[string]any{}
		if q.flatOperation() {
			target = r.Document
		}
		out, err := q.respond(target, "", tx)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (q *request) operation() (any, error) {
	var document string
	err := q.e.store.db.QueryRow("SELECT document FROM gcp_catalog_operations WHERE api=? AND name=?", q.b.model.Name, q.path).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &status{"NOT_FOUND", fmt.Sprintf("Operation '%s' was not found.", q.path)}
	}
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal([]byte(document), &out)
}

func (q *request) operations() (any, error) {
	scope := strings.TrimSuffix(q.path, "/operations")
	if scope == q.path {
		return nil, invalid("The list path must name the operations collection.")
	}
	for _, unsupported := range []string{"filter", "orderBy"} {
		if text, _ := q.params[unsupported].(string); text != "" {
			return nil, invalid("Parameter %q is not supported by the simulator.", unsupported)
		}
	}
	// Flat operation lists page with maxResults; AIP lists return every
	// operation of the scope, as before.
	size, after := int64(-1), int64(0)
	if q.b.api.SelfLink != "" {
		var err error
		if size, after, err = q.page(scope, "operations"); err != nil {
			return nil, err
		}
	}
	rows, err := q.e.store.db.Query("SELECT document, seq FROM gcp_catalog_operations WHERE api=? AND scope=? AND seq>? ORDER BY seq", q.b.model.Name, scope, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, next, last := []any{}, "", int64(0)
	for rows.Next() {
		if int64(len(items)) == size {
			data, _ := json.Marshal(pageToken{scope, "operations", last})
			next = base64.RawURLEncoding.EncodeToString(data)
			break
		}
		var document string
		var op any
		if err := rows.Scan(&document, &last); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(document), &op); err != nil {
			return nil, err
		}
		items = append(items, op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	response := q.b.model.Schemas[q.method.Response]
	field := "operations"
	for name, p := range response.Properties {
		if p.Type == "array" && p.Items != nil && p.Items.Ref != "" {
			field = name
		}
	}
	out := map[string]any{field: items}
	defaults(response, out)
	if next != "" {
		out["nextPageToken"] = next
	}
	return out, nil
}
