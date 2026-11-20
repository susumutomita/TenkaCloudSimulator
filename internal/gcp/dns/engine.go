// Package dns serves a finite native Cloud DNS v1 control plane from SQLite.
// There is no DNS listener, propagation, resolver, Google IAM or outbound call.
// Hosts provide one process/database/token authority per team. This leaf does
// not authenticate host tokens or register itself with the server gateway.
package dns

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Config contains trusted host composition, never request-selected authority.
// Owner migration on an existing project is rejected.
type Config struct {
	Project, Owner string
	Subject        func(context.Context) (string, bool)
	Now            func() time.Time
}
type Engine struct {
	db       *sql.DB
	cfg      Config
	tokenKey []byte
}
type failure struct {
	code            int
	reason, message string
}

func (e *failure) Error() string { return e.message }
func bad(message string) error   { return &failure{400, "invalid", message} }
func absent() error              { return &failure{404, "notFound", "Requested resource not found."} }
func duplicate() error           { return &failure{409, "alreadyExists", "Requested resource already exists."} }
func precondition() error {
	return &failure{412, "preconditionFailed", "Deletion must match the existing RRset exactly."}
}
func budget() error {
	return &failure{429, "quotaExceeded", "The finite local Cloud DNS storage budget is exhausted."}
}

var namePattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// New validates composition and initializes provider-owned SQLite tables.
func New(db *sql.DB, cfg Config) (*Engine, error) {
	if db == nil || !namePattern.MatchString(cfg.Project) || cfg.Owner == "" || len(cfg.Owner) > 256 || cfg.Subject == nil {
		return nil, errors.New("Cloud DNS requires SQLite, a project, an owner and a subject extractor")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		return nil, errors.New("Cloud DNS requires SQLite foreign keys")
	}
	e := &Engine{db: db, cfg: cfg}
	if err := e.initialize(); err != nil {
		return nil, err
	}
	return e, nil
}
func respondError(w http.ResponseWriter, err error) {
	var f *failure
	if !errors.As(err, &f) {
		f = &failure{500, "internalError", "Local Cloud DNS state operation failed."}
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(f.code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": f.code, "message": f.message, "errors": []any{map[string]string{"domain": "global", "reason": f.reason, "message": f.message}}}})
}

// jsonObject rejects unknown fields, nulls, duplicate keys and trailing values.
// Explicit null optional arrays are accepted by the typed decoder, as SDKs may
// encode an absent additions/deletions array this way; null objects are refused.
func jsonObject(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return bad("One JSON object is required.")
	}
	scan := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(scan); err != nil {
		return bad("Malformed JSON or duplicate member.")
	}
	if _, err := scan.Token(); err != io.EOF {
		return bad("Trailing JSON value.")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return bad("Malformed JSON or unsupported field.")
	}
	return nil
}
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate key")
			}
			seen[s] = true
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected closing token")
	}
	_, err = d.Token()
	return err
}

type call struct {
	id    string
	path  map[string]string
	query url.Values
	data  []byte
}

// ServeHTTP handles /dns/v1/... relative to a separately composed GCP endpoint.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	subject, ok := e.cfg.Subject(r.Context())
	if !ok || subject == "" {
		respondError(w, &failure{401, "required", "An authenticated local actor is required."})
		return
	}
	if subject != e.cfg.Owner {
		respondError(w, &failure{403, "forbidden", "Only the configured local project owner may access Cloud DNS; this is not Google IAM."})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	for i, p := range parts {
		v, err := url.PathUnescape(p)
		if err != nil || v == "" || strings.Contains(v, "/") {
			respondError(w, bad("Invalid resource path."))
			return
		}
		parts[i] = v
	}
	// Project admission also precedes unsupported routing/existence checks.
	if len(parts) >= 5 && parts[0] == "dns" && parts[1] == "v1" && parts[2] == "projects" && parts[3] != e.cfg.Project {
		respondError(w, &failure{403, "forbidden", "Project is outside the configured local scope."})
		return
	}
	id, path := resolve(r.Method, parts)
	if id == "" {
		respondError(w, &failure{501, "notImplemented", "This Cloud DNS method/path is not supported by the local simulator."})
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		respondError(w, bad("Malformed query."))
		return
	}
	c := call{id, path, query, nil}
	if err = validateQuery(c); err != nil {
		respondError(w, err)
		return
	}
	c.data, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		respondError(w, bad("Request exceeds the 256 KiB body budget."))
		return
	}
	if r.Method != "POST" && len(bytes.TrimSpace(c.data)) != 0 {
		respondError(w, bad("This method requires an empty body."))
		return
	}
	// Decode/validate outside the transaction: a slow request body cannot hold it.
	input, err := decodeInput(c)
	if err != nil {
		respondError(w, err)
		return
	}
	tx, err := e.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	var result any
	if err == nil {
		defer tx.Rollback()
		result, err = e.execute(tx, c, input)
		if err == nil {
			err = tx.Commit()
		}
	}
	if err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if id != "managedZones.delete" {
		_ = json.NewEncoder(w).Encode(result)
	}
}
func validateQuery(c call) error {
	allowed := map[string]bool{"alt": true, "prettyPrint": true}
	switch c.id {
	case "managedZones.list":
		for _, k := range []string{"dnsName", "maxResults", "pageToken"} {
			allowed[k] = true
		}
	case "changes.list":
		for _, k := range []string{"sortBy", "sortOrder", "maxResults", "pageToken"} {
			allowed[k] = true
		}
	case "resourceRecordSets.list":
		for _, k := range []string{"name", "type", "maxResults", "pageToken"} {
			allowed[k] = true
		}
	}
	for k, v := range c.query {
		if !allowed[k] || len(v) != 1 {
			return bad("Unsupported or repeated query parameter: " + k)
		}
		if k == "alt" && v[0] != "json" || k == "prettyPrint" && v[0] != "true" && v[0] != "false" {
			return bad("Unsupported system parameter value.")
		}
	}
	if s := c.query.Get("sortBy"); s != "" && s != "changeSequence" {
		return bad("Only changeSequence sorting is supported.")
	}
	if s := c.query.Get("sortOrder"); s != "" && s != "ascending" && s != "descending" {
		return bad("Invalid sortOrder.")
	}
	if _, ok := c.query["dnsName"]; ok && !validDomain(c.query.Get("dnsName")) {
		return bad("dnsName must be an absolute lowercase domain.")
	}
	if _, ok := c.query["name"]; ok && !validDomain(c.query.Get("name")) {
		return bad("name must be an absolute lowercase domain.")
	}
	if v, ok := c.query["type"]; ok && (c.query.Get("name") == "" || !knownType(v[0])) {
		return bad("type requires name and a supported RRset type.")
	}
	return nil
}
