package iaas

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ServerHandler struct {
	store   *ServerStore
	config  Config
	extract ActorExtractor
	errors  DiskHandler
}

// NewServerHandler borrows the shared DB and creates a separate metadata namespace.
func NewServerHandler(db *sql.DB, c Config, extract ActorExtractor) (*ServerHandler, error) {
	if db == nil || extract == nil || !validConfig(c) {
		return nil, ErrInvalid
	}
	for _, r := range c.Zone {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return nil, ErrInvalid
		}
	}
	store, e := NewServerStore(db, c)
	if e != nil {
		return nil, e
	}
	return &ServerHandler{store: store, config: c, extract: extract}, nil
}
func (h *ServerHandler) fail(w http.ResponseWriter, status int, code, msg string) {
	h.errors.fail(w, status, code, msg)
}
func (h *ServerHandler) storeError(w http.ResponseWriter, e error) { h.errors.storeError(w, e) }
func (h *ServerHandler) requestError(w http.ResponseWriter, e error) {
	if errors.Is(e, errUnsupported) {
		h.fail(w, 400, "bad_request", "Unsupported local server metadata option")
		return
	}
	h.errors.requestError(w, e)
}
func (h *ServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := h.extract(r.Context())
	if !ok || a.Subject != h.config.Owner || a.Environment != h.config.Environment || a.Project != h.config.Project || a.Zone != h.config.Zone {
		h.fail(w, 403, "forbidden", "Local server owner authority required")
		return
	}
	if !dummyBasic(r) {
		h.fail(w, 401, "unauthorized", "The fixed local dummy Basic marker is required")
		return
	}
	if r.Context().Err() != nil {
		h.storeError(w, r.Context().Err())
		return
	}
	// The registry preserves the literal provider tail. No cleaning or redirection.
	if r.URL.EscapedPath() != r.URL.Path {
		h.fail(w, 400, "bad_request", "Encoded path syntax is unsupported")
		return
	}
	base := "/cloud/zone/" + h.config.Zone + "/api/cloud/1.1/server"
	collection := r.URL.Path == base
	var id int64
	if !collection {
		if !strings.HasPrefix(r.URL.Path, base+"/") {
			h.fail(w, 404, "not_found", "Local route or zone not found")
			return
		}
		tail := strings.TrimPrefix(r.URL.Path, base+"/")
		if head, suffix, nested := strings.Cut(tail, "/"); nested && suffix != "" {
			if _, e := decimalID(head); e == nil {
				h.fail(w, 400, "bad_request", "Server suboperations are not implemented")
				return
			}
		}
		var e error
		id, e = decimalID(tail)
		if e != nil {
			h.fail(w, 400, "bad_request", "A canonical positive server ID is required")
			return
		}
	}
	allowed := "GET, POST"
	if !collection {
		allowed = "GET, PUT, DELETE"
	}
	if (collection && r.Method != "GET" && r.Method != "POST") || (!collection && r.Method != "GET" && r.Method != "PUT" && r.Method != "DELETE") {
		w.Header().Set("Allow", allowed)
		h.fail(w, 405, "method_not_allowed", "Method is unsupported for this route")
		return
	}
	if !collection || r.Method != "GET" {
		if r.URL.RawQuery != "" {
			h.fail(w, 400, "bad_request", "Query is unsupported for this operation")
			return
		}
	}
	if r.Method == "GET" || r.Method == "DELETE" {
		b, e := readBody(r)
		if e != nil {
			h.requestError(w, e)
			return
		}
		if len(b) != 0 {
			h.fail(w, 400, "bad_request", "This operation requires an empty body")
			return
		}
		if collection {
			h.list(w, r)
			return
		}
		if r.Method == "DELETE" {
			if e = h.store.Delete(r.Context(), id); e != nil {
				h.storeError(w, e)
				return
			}
			response(w, map[string]any{"Success": true, "is_ok": true})
			return
		}
		d, e := h.store.Get(r.Context(), id)
		if e != nil {
			h.storeError(w, e)
			return
		}
		response(w, map[string]any{"Server": projectServer(d), "is_ok": true})
		return
	}
	types := headerValues(r, "Content-Type")
	if len(types) != 1 {
		h.fail(w, 415, "unsupported_media_type", "application/json is required")
		return
	}
	media, params, e := mime.ParseMediaType(types[0])
	for key := range params {
		if key != "charset" {
			h.fail(w, 415, "unsupported_media_type", "Only UTF-8 charset is supported")
			return
		}
	}
	if e != nil || media != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) || len(params) > 1 {
		h.fail(w, 415, "unsupported_media_type", "application/json with UTF-8 is required")
		return
	}
	b, e := readBody(r)
	if e != nil {
		h.requestError(w, e)
		return
	}
	v, e := parseObject(b)
	if e != nil {
		h.requestError(w, e)
		return
	}
	if collection {
		h.create(w, r, v)
	} else {
		h.update(w, r, id, v)
	}
}
func (h *ServerHandler) update(w http.ResponseWriter, r *http.Request, id int64, m map[string]any) {
	f, e := serverFields(m, false)
	if e != nil {
		h.requestError(w, e)
		return
	}
	name, e := stringField(f, "Name", false)
	if e != nil {
		h.requestError(w, e)
		return
	}
	desc, e := stringField(f, "Description", false)
	if e != nil {
		h.requestError(w, e)
		return
	}
	d, e := h.store.UpdateNameDesc(r.Context(), id, ServerPatch{name, desc})
	if e != nil {
		h.storeError(w, e)
		return
	}
	response(w, map[string]any{"Server": projectServer(d), "Success": true, "is_ok": true})
}
func (h *ServerHandler) list(w http.ResponseWriter, r *http.Request) {
	from, count := int64(0), int64(100)
	if q := r.URL.RawQuery; q != "" {
		if len(q) > 8192 {
			h.requestError(w, errBodyLimit)
			return
		}
		data := q
		if !strings.HasPrefix(strings.TrimSpace(q), "{") {
			var e error
			data, e = url.PathUnescape(q)
			if e != nil {
				h.requestError(w, ErrInvalid)
				return
			}
		}
		m, e := parseObject([]byte(data))
		if e != nil {
			h.requestError(w, e)
			return
		}
		if e = keys(m, "From", "Count"); e != nil {
			h.requestError(w, e)
			return
		}
		if v, ok := m["From"]; ok {
			from, e = integer(v)
			if e != nil {
				h.requestError(w, e)
				return
			}
		}
		if v, ok := m["Count"]; ok {
			count, e = integer(v)
			if e != nil {
				h.requestError(w, e)
				return
			}
			if count == 0 {
				count = 100
			}
		}
	}
	if count > 100 {
		h.requestError(w, ErrInvalid)
		return
	}
	p, e := h.store.ListFromCount(r.Context(), from, int(count))
	if e != nil {
		h.storeError(w, e)
		return
	}
	servers := make([]map[string]any, 0, len(p.Servers))
	for _, d := range p.Servers {
		servers = append(servers, projectServer(d))
	}
	response(w, map[string]any{"From": p.From, "Count": len(servers), "Total": p.Total, "Servers": servers, "is_ok": true})
}

// Neutral SDK defaults do not attach an Icon, private host, NIC, or Disk.
func neutralPrivateHost(v any) bool {
	if neutralObject(v) {
		return true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for k, x := range m {
		switch k {
		case "ID":
			n, ok := x.(json.Number)
			if !ok || string(n) != "0" {
				return false
			}
		case "Description":
			if x != "" {
				return false
			}
		case "Tags":
			if !neutralArray(x) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func serverFields(m map[string]any, create bool) (map[string]any, error) {
	if e := keys(m, "Server"); e != nil {
		return nil, e
	}
	f, e := object(m["Server"])
	if e != nil {
		return nil, e
	}
	allowed := []string{"Name", "Description", "Tags", "Icon", "PrivateHost", "InterfaceDriver", "WaitDiskMigration"}
	if create {
		allowed = append(allowed, "ServerPlan")
	}
	if e = keys(f, allowed...); e != nil {
		return nil, e
	}
	if v, ok := f["Tags"]; ok && !neutralArray(v) {
		return nil, errUnsupported
	}
	if v, ok := f["Icon"]; ok && !neutralIcon(v) {
		return nil, errUnsupported
	}
	if v, ok := f["PrivateHost"]; ok && !neutralPrivateHost(v) {
		return nil, errUnsupported
	}
	if v, ok := f["InterfaceDriver"]; ok && v != "virtio" {
		return nil, errUnsupported
	}
	if v, ok := f["WaitDiskMigration"]; ok && v != false {
		return nil, errUnsupported
	}
	return f, nil
}
func (h *ServerHandler) create(w http.ResponseWriter, r *http.Request, m map[string]any) {
	f, e := serverFields(m, true)
	if e != nil {
		h.requestError(w, e)
		return
	}
	name, e := stringField(f, "Name", true)
	if e != nil {
		h.requestError(w, e)
		return
	}
	desc, e := stringField(f, "Description", false)
	if e != nil {
		h.requestError(w, e)
		return
	}
	plan, e := object(f["ServerPlan"])
	if e != nil {
		h.requestError(w, e)
		return
	}
	if e = keys(plan, "CPU", "MemoryMB", "Commitment", "ConfidentialVM"); e != nil {
		h.requestError(w, e)
		return
	}
	cpu, e := integer(plan["CPU"])
	if e != nil {
		h.requestError(w, e)
		return
	}
	memory, e := integer(plan["MemoryMB"])
	if e != nil {
		h.requestError(w, e)
		return
	}
	if cpu != 2 || memory != 4096 {
		h.requestError(w, errUnsupported)
		return
	}
	if v, ok := plan["Commitment"]; ok && v != "standard" {
		h.requestError(w, errUnsupported)
		return
	}
	if v, ok := plan["ConfidentialVM"]; ok && v != false {
		h.requestError(w, errUnsupported)
		return
	}
	in := CreateServerInput{Name: *name, CPU: 2, MemoryMB: 4096}
	if desc != nil {
		in.Description = *desc
	}
	row, e := h.store.Create(r.Context(), in)
	if e != nil {
		h.storeError(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(201)
	response(w, map[string]any{"Server": projectServer(row), "Success": "Created", "is_ok": true})
}
func projectServer(s Server) map[string]any {
	return map[string]any{"ID": strconv.FormatInt(s.ID, 10), "Name": s.Name, "Description": s.Description,
		"ServerPlan": map[string]any{"CPU": s.CPU, "MemoryMB": s.MemoryMB},
		"CreatedAt":  s.Created.Format(time.RFC3339Nano), "ModifiedAt": s.Modified.Format(time.RFC3339Nano)}
}
