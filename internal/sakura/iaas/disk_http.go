package iaas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Actor is a trusted host mapping, never a value extracted from HTTP headers.
// Subject is the exercise identity; host operator status grants no disk access.
type Actor struct{ Subject, Environment, Project, Zone string }
type ActorExtractor func(context.Context) (Actor, bool)

type DiskHandler struct {
	store   *DiskStore
	config  Config
	extract ActorExtractor
	serial  atomic.Uint64
}

// NewDiskHandler constructs a metadata-only leaf. It neither registers a provider
// nor authenticates host tokens. The enclosing host must establish trusted actors.
func NewDiskHandler(store *DiskStore, c Config, extract ActorExtractor) (*DiskHandler, error) {
	if store == nil || extract == nil || !validConfig(c) {
		return nil, ErrInvalid
	}
	if store.config != c {
		return nil, ErrScope
	}
	for _, r := range c.Zone {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return nil, ErrInvalid
		}
	}
	return &DiskHandler{store: store, config: c, extract: extract}, nil
}
func (h *DiskHandler) fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"is_ok": false, "is_fatal": true, "serial": fmt.Sprintf("local-%016x", h.serial.Add(1)), "status": fmt.Sprintf("%d %s", status, http.StatusText(status)), "error_code": code, "error_msg": msg})
}
func (h *DiskHandler) storeError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrInvalid):
		h.fail(w, 400, "bad_request", "Invalid local disk configuration")
	case errors.Is(e, ErrNotFound):
		h.fail(w, 404, "not_found", "Disk not found")
	case errors.Is(e, ErrConflict):
		h.fail(w, 409, "conflict", "Disk generation mismatch")
	case errors.Is(e, ErrQuota):
		h.fail(w, 406, "simulator_quota", "Local disk metadata limit reached")
	default:
		h.fail(w, 500, "simulator_internal", "Local disk state unavailable")
	}
}
func response(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
func headerValues(r *http.Request, key string) []string {
	var v []string
	for k, values := range r.Header {
		if strings.EqualFold(k, key) {
			v = append(v, values...)
		}
	}
	return v
}
func dummyBasic(r *http.Request) bool {
	v := headerValues(r, "Authorization")
	if len(v) != 1 {
		return false
	}
	parts := strings.Split(v[0], " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return false
	}
	b, e := base64.StdEncoding.Strict().DecodeString(parts[1])
	return e == nil && base64.StdEncoding.EncodeToString(b) == parts[1] && string(b) == "TCSIMSAKURA:dummy-local-only"
}
func (h *DiskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := h.extract(r.Context())
	if !ok || a.Subject != h.config.Owner || a.Environment != h.config.Environment || a.Project != h.config.Project || a.Zone != h.config.Zone {
		h.fail(w, 403, "forbidden", "Local disk owner authority required")
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
	base := "/cloud/zone/" + h.config.Zone + "/api/cloud/1.1/disk"
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
				h.fail(w, 501, "simulator_unsupported", "Disk suboperations are not implemented")
				return
			}
		}
		var e error
		id, e = decimalID(tail)
		if e != nil {
			h.fail(w, 400, "bad_request", "A canonical positive disk ID is required")
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
		response(w, map[string]any{"Disk": projectDisk(d), "is_ok": true})
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

var errBodyLimit = errors.New("request exceeds local limit")
var errUnsupported = errors.New("unsupported field")

func (h *DiskHandler) requestError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, errBodyLimit):
		h.fail(w, 413, "simulator_request_limit", "Request exceeds the local bound")
	case errors.Is(e, errUnsupported):
		h.fail(w, 501, "simulator_unsupported", "Requested option is not implemented")
	case errors.Is(e, ErrInvalid):
		h.fail(w, 400, "bad_request", "Invalid or ambiguous JSON request")
	default:
		h.storeError(w, e)
	}
}
func readBody(r *http.Request) ([]byte, error) {
	if e := r.Context().Err(); e != nil {
		return nil, e
	}
	if r.Body == nil {
		return nil, nil
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil {
		return nil, e
	}
	if len(b) > 65536 {
		return nil, errBodyLimit
	}
	if e = r.Context().Err(); e != nil {
		return nil, e
	}
	return b, nil
}
func decimalID(s string) (int64, error) {
	if len(s) == 0 || s[0] == '0' {
		return 0, ErrInvalid
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, ErrInvalid
		}
	}
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil || v <= 0 {
		return 0, ErrInvalid
	}
	return v, nil
}
func numericID(v any) (int64, error) {
	switch n := v.(type) {
	case string:
		return decimalID(n)
	case json.Number:
		return decimalID(string(n))
	default:
		return 0, ErrInvalid
	}
}
func integer(v any) (int64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, ErrInvalid
	}
	s := string(n)
	if s == "0" {
		return 0, nil
	}
	return decimalID(s)
}
func object(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	return m, nil
}
func keys(m map[string]any, allowed ...string) error {
	for k := range m {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
			if strings.EqualFold(k, a) {
				return ErrInvalid
			}
		}
		if !found {
			return errUnsupported
		}
	}
	return nil
}
func neutralArray(v any) bool {
	if v == nil {
		return true
	}
	a, ok := v.([]any)
	return ok && len(a) == 0
}
func neutralObject(v any) bool {
	if v == nil {
		return true
	}
	m, ok := v.(map[string]any)
	return ok && len(m) == 0
}

// neutralIcon accepts the existing absent representation and the pinned
// community SDK's no-icon default. It does not attach or persist an Icon.
func neutralIcon(v any) bool {
	if neutralObject(v) {
		return true
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) > 2 {
		return false
	}
	id, ok := m["ID"].(json.Number)
	if !ok || string(id) != "0" {
		return false
	}
	for key := range m {
		if key != "ID" && key != "Tags" {
			return false
		}
	}
	if tags, exists := m["Tags"]; exists {
		values, valid := tags.([]any)
		if !valid || len(values) != 0 {
			return false
		}
	}
	return true
}

func diskFields(m map[string]any, create bool) (map[string]any, error) {
	allowed := []string{"Disk"}
	if create {
		allowed = append(allowed, "DistantFrom", "KMSKey")
	}
	if e := keys(m, allowed...); e != nil {
		return nil, e
	}
	if v, ok := m["DistantFrom"]; ok && !neutralArray(v) {
		return nil, errUnsupported
	}
	if v, ok := m["KMSKey"]; ok && !neutralObject(v) {
		return nil, errUnsupported
	}
	d, e := object(m["Disk"])
	if e != nil {
		return nil, e
	}
	allowed = []string{"Name", "Description", "Connection", "Tags", "Icon"}
	if create {
		allowed = append(allowed, "Plan", "SizeMB")
	}
	if e = keys(d, allowed...); e != nil {
		return nil, e
	}
	if v, ok := d["Tags"]; ok && !neutralArray(v) {
		return nil, errUnsupported
	}
	if v, ok := d["Icon"]; ok && !neutralIcon(v) {
		return nil, errUnsupported
	}
	if v, ok := d["Connection"]; ok {
		str, valid := v.(string)
		if !valid {
			return nil, ErrInvalid
		}
		if str != "virtio" {
			return nil, errUnsupported
		}
	}
	return d, nil
}
func stringField(m map[string]any, key string, required bool) (*string, error) {
	v, ok := m[key]
	if !ok {
		if required {
			return nil, ErrInvalid
		}
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, ErrInvalid
	}
	return &s, nil
}
func (h *DiskHandler) create(w http.ResponseWriter, r *http.Request, m map[string]any) {
	f, e := diskFields(m, true)
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
	plan, e := object(f["Plan"])
	if e == nil {
		e = keys(plan, "ID")
	}
	if e != nil {
		h.requestError(w, e)
		return
	}
	pid, e := numericID(plan["ID"])
	if e != nil {
		h.requestError(w, e)
		return
	}
	size, e := integer(f["SizeMB"])
	if e != nil {
		h.requestError(w, e)
		return
	}
	if pid != 4 || size != 20480 {
		h.requestError(w, errUnsupported)
		return
	}
	in := CreateDiskInput{Name: *name, SizeMB: 20480, PlanID: 4, Connection: "virtio"}
	if desc != nil {
		in.Description = *desc
	}
	d, e := h.store.Create(r.Context(), in)
	if e != nil {
		h.storeError(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(201)
	response(w, map[string]any{"Disk": projectDisk(d), "Success": "Created", "is_ok": true})
}
func (h *DiskHandler) update(w http.ResponseWriter, r *http.Request, id int64, m map[string]any) {
	f, e := diskFields(m, false)
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
	d, e := h.store.UpdateNameDesc(r.Context(), id, DiskPatch{name, desc})
	if e != nil {
		h.storeError(w, e)
		return
	}
	response(w, map[string]any{"Disk": projectDisk(d), "Success": true, "is_ok": true})
}
func (h *DiskHandler) list(w http.ResponseWriter, r *http.Request) {
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
	disks := make([]map[string]any, 0, len(p.Disks))
	for _, d := range p.Disks {
		disks = append(disks, projectDisk(d))
	}
	response(w, map[string]any{"From": p.From, "Count": len(disks), "Total": p.Total, "Disks": disks, "is_ok": true})
}
func projectDisk(d Disk) map[string]any {
	return map[string]any{"ID": strconv.FormatInt(d.ID, 10), "Name": d.Name, "Description": d.Description, "SizeMB": d.SizeMB, "Plan": map[string]any{"ID": d.PlanID}, "Connection": d.Connection, "CreatedAt": d.Created.Format(time.RFC3339Nano), "ModifiedAt": d.Modified.Format(time.RFC3339Nano)}
}

// parseObject preserves number spelling and rejects duplicate keys recursively.
// A preliminary lexical guard rejects the unpaired surrogates encoding/json replaces.
func parseObject(b []byte) (map[string]any, error) {
	if !utf8.Valid(b) || !pairedSurrogates(b) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	v, e := jsonValue(d, 0)
	if e != nil {
		return nil, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	return object(v)
}
func jsonValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delimiter {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, exists := m[key]; exists {
				return nil, ErrInvalid
			}
			v, e := jsonValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[key] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return m, nil
	case '[':
		a := []any{}
		for d.More() {
			v, e := jsonValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return a, nil
	default:
		return nil, ErrInvalid
	}
}
func pairedSurrogates(b []byte) bool {
	quoted := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		v, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if v >= 0xDC00 && v <= 0xDFFF {
			return false
		}
		if v >= 0xD800 && v <= 0xDBFF {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
