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
	scopePath string
	zone      bool
	// targetName is the resource a create, patch, set or delete acts on.
	targetName string
}

func (q *request) collection() *Collection { return q.b.api.collection(q.bound.Collection) }

func (q *request) permission() string {
	verbs := map[Kind]string{Create: "create", Get: "get", List: "list", Patch: "update", Replace: "update", Delete: "delete", OperationGet: "get", OperationList: "list"}
	if q.bound.Permission != "" {
		return q.bound.Permission
	}
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
	q.path = q.e.normalize(q.path)
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
	case Patch, Replace:
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

// normalize names the configured project by ID rather than number.
func (e *Engine) normalize(name string) string {
	if strings.HasPrefix(name, "projects/"+e.cfg.ProjectNumber+"/") {
		return "projects/" + e.cfg.Project + strings.TrimPrefix(name, "projects/"+e.cfg.ProjectNumber)
	}
	return name
}

func (q *request) find(name string) (row, error) {
	r, err := findName(q.e.store.db, q.b.model.Name, name)
	if errors.Is(err, sql.ErrNoRows) {
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
	if c.Implicit != "" {
		segments := strings.Split(parent, "/")
		n := len(segments)
		pattern := resourceID
		if c.ImplicitPattern != nil {
			pattern = c.ImplicitPattern
		}
		if n < 4 || segments[n-2] != c.Implicit || !pattern.MatchString(segments[n-1]) {
			return invalid("Parent %q is not a valid %s parent.", parent, c.ID)
		}
		parent = strings.Join(segments[:n-2], "/")
	}
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
	scope := q.root()
	if anchor := q.collection().Operations; anchor != "" {
		scope = ancestor(q.targetName, anchor)
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

func (q *request) longRunning() bool {
	schema := q.b.model.Schemas[q.method.Response]
	if schema == nil {
		return false
	}
	_, done := schema.Properties["done"]
	_, response := schema.Properties["response"]
	return done && response
}

// newID returns the new resource and its ID. An Unwrap hook may derive
// both from a wrapped request; without a derived key the ID comes from
// IDParam, or from IDField of the (unwrapped) resource, which carries the
// full resource name under the request parent when FullName is set.
// Server-generated IDs are opaque; client-chosen IDs follow AIP-122 unless
// IDPattern replaces it.
func (q *request) newID(parent string) (string, map[string]any, error) {
	c := q.collection()
	id, body := "", q.body
	if q.bound.Unwrap != nil {
		var err error
		if body, id, err = q.bound.Unwrap(q.body); err != nil {
			return "", nil, err
		}
	}
	if id == "" {
		switch {
		case c.IDParam != "":
			if id, _ = q.params[c.IDParam].(string); id == "" {
				return "", nil, invalid("Query parameter %q is required.", c.IDParam)
			}
		case c.IDField != "":
			if id, _ = body[c.IDField].(string); id == "" {
				return "", nil, invalid("Field '%s' is required.", c.IDField)
			}
			if c.FullName {
				full := q.e.normalize(id)
				if id = strings.TrimPrefix(full, parent+"/"+q.segment()+"/"); id == full {
					return "", nil, invalid("Field '%s' must name a resource under %s/%s.", c.IDField, parent, q.segment())
				}
			}
		case q.bound.Unwrap != nil:
			return "", nil, invalid("The request does not name the new %s ID.", c.ID)
		default:
			return randomHex(8), body, nil
		}
	}
	pattern := resourceID
	if c.IDPattern != nil {
		pattern = c.IDPattern
	}
	if !pattern.MatchString(id) {
		return "", nil, invalid("Resource ID %q must match %s.", id, pattern.String())
	}
	return id, body, nil
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
	id, body, err := q.newID(parent)
	if err != nil {
		return nil, err
	}
	if err := q.required(q.method.Request); err != nil {
		return nil, err
	}
	name := parent + "/" + segment + "/" + id
	q.targetName = name
	document := map[string]any{}
	schema := q.b.model.Schemas[c.Schema]
	defaults(schema, document)
	for k, v := range body {
		document[k] = v
	}
	if c.FullName {
		document[c.IDField] = name
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
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		// References resolve, conflicts are checked and the document is
		// encoded inside the writing transaction, so a concurrent delete
		// of a referenced resource cannot slip in between.
		if err := q.references(tx, c, document, nil); err != nil {
			return nil, err
		}
		if err := q.conflicts(tx, c, name, document); err != nil {
			return nil, err
		}
		data, err := encode(document)
		if err != nil {
			return nil, err
		}
		if _, err := findName(tx, q.b.model.Name, name); err == nil {
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
		// The parent read before authorization must still exist when the
		// child is inserted, so a concurrent delete cannot orphan it.
		if c.Parent != "" {
			if err := liveParent(tx, q.b.model.Name, q.storedParent(parent)); err != nil {
				return nil, err
			}
		}
		if q.params["validateOnly"] != true {
			seq, err := nextSeq(tx)
			if err != nil {
				return nil, err
			}
			if err := exactlyOne(tx.Exec("INSERT INTO gcp_catalog_resources VALUES(?,?,?,?,?,?,?,1)", q.b.model.Name, name, parent, c.ID, id, data, seq)); err != nil {
				return nil, err
			}
			if err := withinBudget(tx); err != nil {
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
	if q.bound.Unwrap != nil {
		if q.body, mask, err = q.bound.Unwrap(q.body); err != nil {
			return nil, err
		}
	}
	replace := q.bound.Kind == Replace
	if replace {
		// A full replacement keeps the resource ID and output fields and
		// replaces every writable field, clearing those the body omits. It
		// takes no update mask, whether as a parameter or a wrapped field.
		if mask != "" {
			return nil, invalid("A full replacement does not take an update mask.")
		}
		if given, ok := q.body[c.IDField].(string); ok && c.IDField != "" {
			if given = q.e.normalize(given); given != r.Document[c.IDField] {
				return nil, invalid("Field '%s' cannot change the resource ID.", c.IDField)
			}
		}
		// A wrapped replacement carries the resource, not the request schema.
		schema := q.method.Request
		if q.bound.Unwrap != nil {
			schema = c.Schema
		}
		if err := q.required(schema); err != nil {
			return nil, err
		}
		for key, p := range q.b.model.Schemas[c.Schema].Properties {
			if !p.ReadOnly && key != "name" && key != "etag" && key != c.IDField {
				fields = append(fields, key)
			}
		}
		sort.Strings(fields)
	} else if mask != "" {
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
	// Flat APIs merge objects of a mask-less patch (RFC 7396).
	merge := q.b.api.SelfLink != "" && mask == "" && !replace
	written := map[string]bool{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "etag" || q.bound.Fingerprint != "" && field == q.bound.Fingerprint {
			continue
		}
		if given, ok := q.body[field].(string); ok && c.IDField != "" && field == c.IDField && q.e.normalize(given) == r.Document[field] {
			// Sending the unchanged body ID back is not an update.
			continue
		}
		if merge && !mutable[field] && mustJSON(q.body[field]) == mustJSON(r.Document[field]) {
			// A merge patch may restate an immutable field unchanged.
			continue
		}
		if !replace && !mutable[field] {
			return nil, invalid("Field %q cannot be updated by the simulator.", field)
		}
		written[field] = true
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
	return q.save(c, r, written)
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
	written := map[string]bool{}
	for field, p := range q.b.model.Schemas[q.method.Request].Properties {
		if p.ReadOnly || field == q.bound.Fingerprint {
			continue
		}
		written[field] = true
		if value, ok := q.body[field]; ok {
			r.Document[field] = value
		} else {
			delete(r.Document, field)
		}
	}
	return q.save(c, r, written)
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

// save stores an updated resource and returns it or its operation. The
// written reference fields resolve, conflicts are checked, the document is
// encoded and the metadata budget is checked inside the transaction.
func (q *request) save(c *Collection, r row, written map[string]bool) (any, error) {
	if fp := q.bound.Fingerprint; fp != "" {
		r.Document[fp] = fingerprint()
	}
	if _, ok := r.Document["updateTime"]; ok || c.Generate["updateTime"] != nil {
		r.Document["updateTime"] = q.now().Format(time.RFC3339Nano)
	}
	if _, ok := r.Document["etag"]; ok {
		r.Document["etag"] = randomHex(8)
	}
	q.targetName = r.Name
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err := q.references(tx, c, r.Document, written); err != nil {
			return nil, err
		}
		if err := q.conflicts(tx, c, r.Name, r.Document); err != nil {
			return nil, err
		}
		data, err := encode(r.Document)
		if err != nil {
			return nil, err
		}
		if q.params["validateOnly"] != true {
			if err := exactlyOne(tx.Exec("UPDATE gcp_catalog_resources SET document=?, generation=generation+1 WHERE api=? AND name=? AND generation=?", data, r.API, r.Name, r.Generation)); err != nil {
				return nil, err
			}
			if err := withinBudget(tx); err != nil {
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
	r, err := q.target()
	if err != nil {
		return nil, err
	}
	if etag, ok := q.params["etag"].(string); ok && etag != "" && etag != r.Document["etag"] {
		return nil, &status{"ABORTED", "The etag does not match the current resource."}
	}
	if field := protectedBy(c, r.Document); field != "" {
		return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' is protected by '%s'; disable it before deleting.", r.Name, field)}
	}
	_, optional := q.method.Parameters["force"]
	force := q.params["force"] == true || !optional
	return q.idempotent(func() (any, error) {
		tx, err := q.e.store.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if holder, err := q.referrer(tx, r); err != nil {
			return nil, err
		} else if holder != "" && q.b.api.SelfLink != "" {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("The resource '%s' is already being used by '%s'.", r.Name, holder)}
		} else if holder != "" {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' is still referenced by '%s'.", r.Name, holder)}
		}
		var children int
		if err := tx.QueryRow("SELECT count(*) FROM gcp_catalog_resources WHERE api=? AND name LIKE ? ESCAPE '\\'", r.API, escapeLike(r.Name)+"/%").Scan(&children); err != nil {
			return nil, err
		}
		if children > 0 && !(force && c.Cascade) {
			return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' still has child resources.", r.Name)}
		}
		if children > 0 {
			if child, err := q.protectedChild(tx, r); err != nil {
				return nil, err
			} else if child != "" {
				return nil, &status{"FAILED_PRECONDITION", fmt.Sprintf("Resource '%s' has the protected child '%s'.", r.Name, child)}
			}
		}
		if q.params["validateOnly"] != true {
			if _, err := tx.Exec("DELETE FROM gcp_catalog_resources WHERE api=? AND name LIKE ? ESCAPE '\\'", r.API, escapeLike(r.Name)+"/%"); err != nil {
				return nil, err
			}
			if err := exactlyOne(tx.Exec("DELETE FROM gcp_catalog_resources WHERE api=? AND name=? AND generation=?", r.API, r.Name, r.Generation)); err != nil {
				return nil, err
			}
		}
		// Methods such as BigLake catalogs.delete return the deleted
		// resource, and flat operations name it as their target.
		deleted := map[string]any{}
		if q.method.Response == c.Schema || q.flatOperation() {
			deleted = r.Document
		}
		out, err := q.respond(deleted, "", tx)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}

// protectedBy names the field whose stored value protects a resource.
func protectedBy(c *Collection, document map[string]any) string {
	fields := make([]string, 0, len(c.Protection))
	for field := range c.Protection {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		if document[field] == c.Protection[field] {
			return field
		}
	}
	return ""
}

// protectedChild names a protected resource that a cascade would remove.
func (q *request) protectedChild(tx *sql.Tx, target row) (string, error) {
	rows, err := tx.Query("SELECT "+columns+" FROM gcp_catalog_resources WHERE api=? AND name LIKE ? ESCAPE '\\' ORDER BY seq", target.API, escapeLike(target.Name)+"/%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		child, err := scanRow(rows)
		if err != nil {
			return "", err
		}
		if c := q.b.api.collection(child.Collection); c != nil && protectedBy(c, child.Document) != "" {
			return child.Name, nil
		}
	}
	return "", rows.Err()
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

// storedParent strips an implicit, unstored collection segment such as a
// Firestore collection group, naming the stored parent resource.
func (q *request) storedParent(parent string) string {
	if c := q.collection(); c.Implicit != "" {
		if segments := strings.Split(parent, "/"); len(segments) >= 4 && segments[len(segments)-2] == c.Implicit {
			return strings.Join(segments[:len(segments)-2], "/")
		}
	}
	return parent
}

// liveParent reports whether a parent resource still exists inside tx.
func liveParent(tx *sql.Tx, api, parent string) error {
	if _, err := findName(tx, api, parent); errors.Is(err, sql.ErrNoRows) {
		return &status{"NOT_FOUND", fmt.Sprintf("Parent '%s' no longer exists.", parent)}
	} else if err != nil {
		return err
	}
	return nil
}
