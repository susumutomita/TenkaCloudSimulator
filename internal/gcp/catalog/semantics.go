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
	target   string // the resource a create, patch or delete acts on
}

func (q *request) collection() *Collection { return q.b.api.collection(q.bound.Collection) }

func (q *request) permission() string {
	verbs := map[Kind]string{Create: "create", Get: "get", List: "list", Patch: "update", Delete: "delete", OperationGet: "get", OperationList: "list"}
	if q.bound.Kind == OperationGet || q.bound.Kind == OperationList {
		return q.b.model.Name + ".operations." + verbs[q.bound.Kind]
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
	q.path = q.e.normalize(q.path)
	switch q.bound.Kind {
	case Create:
		return q.create()
	case Get:
		r, err := q.find(q.path)
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
	case OperationGet:
		return q.operation()
	}
	return q.operations()
}

// normalize names the configured project by ID rather than number.
func (e *Engine) normalize(name string) string {
	if strings.HasPrefix(name, "projects/"+e.cfg.ProjectNumber+"/") {
		return "projects/" + e.cfg.Project + strings.TrimPrefix(name, "projects/"+e.cfg.ProjectNumber)
	}
	return name
}

func (q *request) find(name string) (row, error) {
	r, err := findName(q.e.store.db, name)
	if errors.Is(err, sql.ErrNoRows) || err == nil && r.API != q.b.model.Name {
		return row{}, &status{"NOT_FOUND", fmt.Sprintf("Resource '%s' was not found.", name)}
	}
	return r, err
}

// parentName validates the parent of a collection path and that it exists.
func (q *request) parentName(parent string) error {
	c := q.collection()
	if c.Parent == "" {
		root := "projects/" + q.e.cfg.Project
		if q.b.api.Scope == "location" {
			root += "/locations/" + q.location
		}
		if parent != root {
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
		if p.Required && !p.ReadOnly && key != "name" && !relaxed[key] {
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
	if !q.longRunning() {
		return result, nil
	}
	scope := "projects/" + q.e.cfg.Project
	if q.location != "" {
		scope += "/locations/" + q.location
	}
	if anchor := q.collection().Operations; anchor != "" {
		scope = ancestor(q.target, anchor)
	}
	opName := scope + "/operations/operation-" + randomHex(10)
	response := map[string]any{"@type": "type.googleapis.com/google.protobuf.Empty"}
	if typeName != "" {
		response = map[string]any{"@type": q.b.api.TypeURL + typeName}
		if strings.HasPrefix(typeName, "type.googleapis.com/") {
			response["@type"] = typeName
		}
		for k, v := range result {
			response[k] = v
		}
	}
	op := map[string]any{"name": opName, "done": true, "response": response}
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

// ancestor returns the prefix of a resource name that ends with an ID of
// the named collection; collection segments sit at even positions.
func ancestor(name, collection string) string {
	segments := strings.Split(name, "/")
	for i := len(segments) - 2; i >= 0; i -= 2 {
		if segments[i] == collection {
			return strings.Join(segments[:i+2], "/")
		}
	}
	return name
}

// references requires every written reference field (all when written is
// nil) to name an existing resource of its collection, normalizing project
// numbers to the project ID.
func (q *request) references(tx *sql.Tx, document map[string]any, written map[string]bool) error {
	fields := make([]string, 0, len(q.collection().References))
	for field := range q.collection().References {
		if written == nil || written[field] {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	for _, field := range fields {
		value, ok := document[field].(string)
		if !ok {
			continue
		}
		value = q.e.normalize(value)
		document[field] = value
		r, err := findName(tx, value)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (r.API != q.b.model.Name || r.Collection != q.collection().References[field].Collection) {
			return &status{"NOT_FOUND", fmt.Sprintf("Referenced resource '%s' in field '%s' was not found.", value, field)}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// referenced finds a resource outside the deleted subtree that still names
// the target through a blocking reference.
func (q *request) referenced(tx *sql.Tx, target row) (string, error) {
	for _, c := range q.b.api.Collections {
		fields := make([]string, 0, len(c.References))
		for field, ref := range c.References {
			if ref.Blocks && ref.Collection == target.Collection && !(ref.Override != "" && q.params[ref.Override] == true) {
				fields = append(fields, field)
			}
		}
		sort.Strings(fields)
		for _, field := range fields {
			var name string
			err := tx.QueryRow("SELECT name FROM gcp_catalog_resources WHERE api=? AND collection=? AND json_extract(document, '$.' || ?)=? AND name NOT LIKE ? ESCAPE '\\' ORDER BY seq LIMIT 1",
				q.b.model.Name, c.ID, field, target.Name, escapeLike(target.Name)+"/%").Scan(&name)
			if err == nil {
				return name, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
	}
	return "", nil
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
	parent := q.path
	if !strings.HasSuffix(parent, "/"+c.ID) {
		return nil, invalid("The create path must name the %s collection.", c.ID)
	}
	parent = strings.TrimSuffix(parent, "/"+c.ID)
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
	name := parent + "/" + c.ID + "/" + id
	document := map[string]any{}
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
	q.target = name
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err := q.references(tx, document, nil); err != nil {
			return nil, err
		}
		data, err := encode(document)
		if err != nil {
			return nil, err
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
		out, err := q.respond(document, c.message(), tx)
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
	parent := strings.TrimSuffix(q.path, "/"+c.ID)
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
	size := int64(defaultPageSize)
	if n, ok := q.params["pageSize"].(int64); ok {
		if n < 0 {
			return nil, invalid("pageSize must not be negative.")
		}
		if n > 0 {
			size = min(n, maxPageSize)
		}
	}
	after := int64(0)
	if text, _ := q.params["pageToken"].(string); text != "" {
		raw, err := base64.RawURLEncoding.DecodeString(text)
		var token pageToken
		if err != nil || json.Unmarshal(raw, &token) != nil || token.Parent != parent || token.Kind != c.ID {
			return nil, invalid("Invalid page token.")
		}
		after = token.After
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

func (q *request) patch() (any, error) {
	c := q.collection()
	r, err := q.find(q.path)
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
	if mask, _ := q.params["updateMask"].(string); mask != "" {
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
	written := map[string]bool{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "etag" {
			continue
		}
		if !mutable[field] {
			return nil, invalid("Field %q cannot be updated by the simulator.", field)
		}
		written[field] = true
		if value, ok := q.body[field]; ok {
			r.Document[field] = value
		} else {
			delete(r.Document, field)
		}
	}
	if _, ok := r.Document["updateTime"]; ok || c.Generate["updateTime"] != nil {
		r.Document["updateTime"] = q.now().Format(time.RFC3339Nano)
	}
	if _, ok := r.Document["etag"]; ok {
		r.Document["etag"] = randomHex(8)
	}
	q.target = r.Name
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err := q.references(tx, r.Document, written); err != nil {
			return nil, err
		}
		data, err := encode(r.Document)
		if err != nil {
			return nil, err
		}
		if q.params["validateOnly"] != true {
			if err := exactlyOne(tx.Exec("UPDATE gcp_catalog_resources SET document=?, generation=generation+1 WHERE name=? AND generation=?", data, r.Name, r.Generation)); err != nil {
				return nil, err
			}
		}
		out, err := q.respond(r.Document, c.message(), tx)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}

func (q *request) remove() (any, error) {
	c := q.collection()
	r, err := q.find(q.path)
	if err != nil {
		return nil, err
	}
	if etag, ok := q.params["etag"].(string); ok && etag != "" && etag != r.Document["etag"] {
		return nil, &status{"ABORTED", "The etag does not match the current resource."}
	}
	for field, value := range c.Protection {
		if r.Document[field] == value {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' is protected by '%s'; disable it before deleting.", r.Name, field)}
		}
	}
	_, optional := q.method.Parameters["force"]
	force := q.params["force"] == true || !optional
	q.target = r.Name
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if holder, err := q.referenced(tx, r); err != nil {
			return nil, err
		} else if holder != "" {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' is still referenced by '%s'.", r.Name, holder)}
		}
		var children int
		if err := tx.QueryRow("SELECT count(*) FROM gcp_catalog_resources WHERE name LIKE ? ESCAPE '\\'", escapeLike(r.Name)+"/%").Scan(&children); err != nil {
			return nil, err
		}
		if children > 0 && !(force && c.Cascade) {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' still has child resources.", r.Name)}
		}
		if q.params["validateOnly"] != true {
			if _, err := tx.Exec("DELETE FROM gcp_catalog_resources WHERE name LIKE ? ESCAPE '\\'", escapeLike(r.Name)+"/%"); err != nil {
				return nil, err
			}
			if err := exactlyOne(tx.Exec("DELETE FROM gcp_catalog_resources WHERE name=? AND generation=?", r.Name, r.Generation)); err != nil {
				return nil, err
			}
		}
		out, err := q.respond(map[string]any{}, "", tx)
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
	if text, _ := q.params["filter"].(string); text != "" {
		return nil, invalid("Parameter \"filter\" is not supported by the simulator.")
	}
	rows, err := q.e.store.db.Query("SELECT document FROM gcp_catalog_operations WHERE api=? AND scope=? ORDER BY seq", q.b.model.Name, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var document string
		var op any
		if err := rows.Scan(&document); err != nil {
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
	return map[string]any{"operations": items}, nil
}
