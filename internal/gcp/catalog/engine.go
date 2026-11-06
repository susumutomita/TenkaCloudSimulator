package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxRequestBytes = 256 << 10

// Config is trusted host composition: one synthetic project and its owner.
// Request headers never select the project, owner or environment.
type Config struct {
	Project       string
	ProjectNumber string
	Owner         string
	Locations     []string
	// Subject returns the authenticated exercise identity of the request.
	Subject func(ctx context.Context) (string, bool)
	Now     func() time.Time
}

type route struct {
	method   string
	verb     string
	segments []string
	reviewed bool
}

type binding struct {
	api      *API
	model    *Model
	bindings map[string]*Binding
	routes   []route
	prefix   []string // servicePath segments before the method path
}

// Engine serves every reviewed Google API from one provider database.
type Engine struct {
	cfg    Config
	store  *store
	byName map[string]*binding
	order  []*binding
}

// New validates every specification against its embedded discovery subset.
func New(db *sql.DB, cfg Config, apis []*API) (*Engine, error) {
	if cfg.Project == "" || cfg.ProjectNumber == "" || cfg.Owner == "" || len(cfg.Locations) == 0 || cfg.Subject == nil {
		return nil, errors.New("the GCP catalog requires a project, project number, owner, locations and subject extractor")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s, err := newStore(db)
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, store: s, byName: map[string]*binding{}}
	for _, api := range apis {
		model, err := loadModel(api.Key)
		if err != nil {
			return nil, err
		}
		b, err := bindAPI(api, model)
		if err != nil {
			return nil, err
		}
		if err := e.add(b); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *Engine) add(b *binding) error {
	if e.byName[b.model.Name] != nil {
		return fmt.Errorf("GCP catalog API %s is registered twice", b.model.Name)
	}
	e.byName[b.model.Name] = b
	e.order = append(e.order, b)
	sort.Slice(e.order, func(i, j int) bool { return e.order[i].model.Name < e.order[j].model.Name })
	return nil
}

func bindAPI(api *API, model *Model) (*binding, error) {
	// Flat APIs return their own operation resource and need no type URL.
	typed := strings.HasPrefix(api.TypeURL, "type.googleapis.com/") || api.SelfLink != "" && api.TypeURL == ""
	if api.Title == "" || len(api.Issues) == 0 || len(api.Bounds) == 0 || !typed {
		return nil, fmt.Errorf("GCP catalog %s lacks a title, Issue, bounds or type URL", api.Key)
	}
	if api.Scope != "location" && api.Scope != "project" && api.Scope != "zonal" {
		return nil, fmt.Errorf("GCP catalog %s has an unknown scope", api.Key)
	}
	if api.SelfLink != "" && (!strings.HasPrefix(api.SelfLink, "https://") || !strings.HasSuffix(api.SelfLink, "/")) {
		return nil, fmt.Errorf("GCP catalog %s has a malformed selfLink prefix", api.Key)
	}
	b := &binding{api: api, model: model, bindings: map[string]*Binding{}}
	for _, c := range api.Collections {
		if model.Schemas[c.Schema] == nil || c.Permission == "" || c.Parent != "" && api.collection(c.Parent) == nil {
			return nil, fmt.Errorf("GCP catalog %s collection %s has a missing schema, permission or parent", api.Key, c.ID)
		}
		if c.IDParam != "" && c.IDField != "" || c.IDField != "" && model.Schemas[c.Schema].Properties[c.IDField] == nil {
			return nil, fmt.Errorf("GCP catalog %s collection %s needs one ID source that names a schema field", api.Key, c.ID)
		}
		for path, targets := range c.References {
			if !model.referencePath(model.Schemas[c.Schema], path) || len(targets) == 0 {
				return nil, fmt.Errorf("GCP catalog %s collection %s references through unknown field %s", api.Key, c.ID, path)
			}
			for _, target := range targets {
				if api.collection(target) == nil {
					return nil, fmt.Errorf("GCP catalog %s collection %s references unknown collection %s", api.Key, c.ID, target)
				}
			}
		}
	}
	for i := range api.Bindings {
		bound := &api.Bindings[i]
		method := model.Methods[bound.Method]
		if method == nil || b.bindings[bound.Method] != nil {
			return nil, fmt.Errorf("GCP catalog %s method %s is missing or bound twice", api.Key, bound.Method)
		}
		switch bound.Kind {
		case Create, Get, List, Patch, Delete, Set:
			if api.collection(bound.Collection) == nil {
				return nil, fmt.Errorf("GCP catalog %s method %s names an unknown collection", api.Key, bound.Method)
			}
		case OperationGet, OperationList:
		default:
			return nil, fmt.Errorf("GCP catalog %s method %s has an unknown kind", api.Key, bound.Method)
		}
		if err := checkBinding(api, model, bound, method); err != nil {
			return nil, err
		}
		b.bindings[bound.Method] = bound
	}
	for id := range model.Methods {
		if b.bindings[id] == nil {
			return nil, fmt.Errorf("GCP catalog %s embeds method %s without a reviewed binding", api.Key, id)
		}
	}
	b.prefix = splitPath(model.ServicePath)
	ids := make([]string, 0, len(model.Routes))
	for id := range model.Routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := model.Routes[id]
		segments := splitPath(r.FlatPath)
		verb := ""
		if n := len(segments); n > 0 {
			if before, after, ok := strings.Cut(segments[n-1], ":"); ok && strings.HasSuffix(before, "}") || ok && !strings.Contains(before, "{") {
				segments[n-1], verb = before, after
			}
		}
		b.routes = append(b.routes, route{method: id, verb: verb, segments: segments, reviewed: b.bindings[id] != nil})
		_ = r
	}
	return b, nil
}

// checkBinding fails closed on custom-method fields, fingerprints and
// refused parameters that the official method does not have.
func checkBinding(api *API, model *Model, bound *Binding, method *Method) error {
	request := model.Schemas[method.Request]
	if bound.Kind == Set {
		resource := model.Schemas[api.collection(bound.Collection).Schema]
		if request == nil {
			return fmt.Errorf("GCP catalog %s method %s sets no request fields", api.Key, bound.Method)
		}
		for field, p := range request.Properties {
			if !p.ReadOnly && resource.Properties[field] == nil {
				return fmt.Errorf("GCP catalog %s method %s sets %s, which %s lacks", api.Key, bound.Method, field, api.collection(bound.Collection).Schema)
			}
		}
	}
	if bound.Fingerprint != "" && (request == nil || request.Properties[bound.Fingerprint] == nil || bound.Kind != Patch && bound.Kind != Set) {
		return fmt.Errorf("GCP catalog %s method %s has no request field %s to guard", api.Key, bound.Method, bound.Fingerprint)
	}
	for _, name := range bound.Refuse {
		if method.Parameters[name].Location != "query" {
			return fmt.Errorf("GCP catalog %s method %s refuses unknown parameter %s", api.Key, bound.Method, name)
		}
	}
	return nil
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Owns reports whether the provider-relative path addresses a catalog API.
func (e *Engine) Owns(r *http.Request) bool {
	first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return e.byName[first] != nil
}

// fail writes a google.rpc.Status error body.
func fail(w http.ResponseWriter, err error) {
	var s *status
	if !errors.As(err, &s) {
		s = &status{"INTERNAL", "Simulator catalog state operation failed."}
	}
	code := httpCodes[s.code]
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": s.message, "status": s.code}})
}

func writeJSON(w http.ResponseWriter, value any) {
	data, _ := json.Marshal(value)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// systemParameters are Google API system parameters accepted by every method.
var systemParameters = map[string]bool{"alt": true, "prettyPrint": true, "$.xgafv": true, "key": true, "quotaUser": true, "access_token": true, "oauth_token": true, "upload_protocol": true, "uploadType": true, "callback": true}

// ServeHTTP handles one request on a provider-relative path /<api>/...
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := splitPath(r.URL.EscapedPath())
	if len(escaped) == 0 || e.byName[escaped[0]] == nil {
		fail(w, &status{"NOT_FOUND", "The requested URL was not found on this simulator."})
		return
	}
	b := e.byName[escaped[0]]
	segments := escaped[1:]
	if len(segments) < len(b.prefix) {
		fail(w, &status{"NOT_FOUND", "The requested URL was not found on this simulator."})
		return
	}
	for i, p := range b.prefix {
		if segments[i] != p {
			fail(w, &status{"NOT_FOUND", "The requested URL was not found on this simulator."})
			return
		}
	}
	segments = segments[len(b.prefix):]
	decoded := make([]string, len(segments))
	for i, s := range segments {
		value, err := url.PathUnescape(s)
		if err != nil || value == "" {
			fail(w, &status{"NOT_FOUND", "The requested URL was not found on this simulator."})
			return
		}
		decoded[i] = value
	}
	rt, ok := b.match(r.Method, decoded)
	if !ok {
		fail(w, &status{"NOT_FOUND", "The requested URL was not found on this simulator."})
		return
	}
	if !rt.reviewed {
		fail(w, &status{"UNIMPLEMENTED", "Method " + b.model.Name + "." + rt.method + " is not implemented by the simulator."})
		return
	}
	q, err := e.prepare(w, r, b, rt, decoded)
	if err != nil {
		fail(w, err)
		return
	}
	out, err := q.execute()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// match returns the most specific official route: where templates overlap,
// as Compute Engine backendServices/listUsable and backendServices/{name}
// do, the earliest literal segment wins.
func (b *binding) match(method string, segments []string) (route, bool) {
	best, score := route{}, -1
	for _, rt := range b.routes {
		if b.model.Routes[rt.method].HTTPMethod != method || len(rt.segments) != len(segments) {
			continue
		}
		matched := true
		for i, template := range rt.segments {
			value := segments[i]
			if i == len(segments)-1 {
				base, verb, hasVerb := strings.Cut(value, ":")
				if rt.verb != "" {
					if !hasVerb || verb != rt.verb || base == "" {
						matched = false
						break
					}
					value = base
				} else if hasVerb {
					matched = false
					break
				}
			}
			if strings.HasPrefix(template, "{") {
				if value == "" {
					matched = false
					break
				}
			} else if template != value {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		literal := 0
		for i, template := range rt.segments {
			if !strings.HasPrefix(template, "{") {
				literal |= 1 << (len(rt.segments) - 1 - i)
			}
		}
		if literal > score {
			best, score = rt, literal
		}
	}
	return best, score >= 0
}

// prepare authenticates, authorizes the project and decodes the request.
func (e *Engine) prepare(w http.ResponseWriter, r *http.Request, b *binding, rt route, segments []string) (*request, error) {
	subject, ok := e.cfg.Subject(r.Context())
	if !ok || subject == "" {
		return nil, &status{"UNAUTHENTICATED", "Request had invalid authentication credentials."}
	}
	bound := b.bindings[rt.method]
	method := b.model.Methods[rt.method]
	q := &request{e: e, b: b, bound: bound, method: method, subject: subject, w: w, params: map[string]any{}}
	// The resource name follows the version segment, as in {+name}/{+parent}.
	start := -1
	for i, s := range segments {
		if s == "projects" {
			start = i
			break
		}
	}
	if start < 0 || start+1 >= len(segments) {
		return nil, invalid("Resource names must start with projects/{project}.")
	}
	q.path = strings.Join(segments[start:], "/")
	if rt.verb != "" {
		q.path = strings.TrimSuffix(q.path, ":"+rt.verb)
	}
	q.project = segments[start+1]
	if n := len(rt.segments); rt.verb == "" && (bound.Kind == Set || bound.Kind == OperationGet) && n > 1 &&
		!strings.HasPrefix(rt.segments[n-1], "{") && strings.HasPrefix(rt.segments[n-2], "{") {
		// A trailing literal after the resource is a slash-style custom
		// verb, such as Compute Engine .../disks/{disk}/resize or
		// .../operations/{operation}/wait.
		q.path = q.path[:strings.LastIndex(q.path, "/")]
	}
	if b.api.Scope == "zonal" {
		if start+2 >= len(segments) {
			return nil, invalid("Resource paths must name global, regions/{region} or zones/{zone}.")
		}
		switch scope := strings.Join(segments[start+2:min(start+4, len(segments))], "/"); {
		case segments[start+2] == "global":
			q.scopePath = "global"
		case strings.HasPrefix(scope, "regions/") && start+4 < len(segments):
			q.scopePath, q.location = scope, segments[start+3]
		case strings.HasPrefix(scope, "zones/") && start+4 < len(segments):
			q.scopePath, q.location, q.zone = scope, segments[start+3], true
		default:
			return nil, invalid("Resource paths must name global, regions/{region} or zones/{zone}.")
		}
	} else if b.api.Scope == "location" && bound.Kind != OperationGet && bound.Kind != OperationList || strings.Contains(q.path, "/locations/") {
		if start+3 >= len(segments) || segments[start+2] != "locations" {
			return nil, invalid("Resource names must include locations/{location}.")
		}
		q.location = segments[start+3]
	}
	for key, values := range r.URL.Query() {
		if systemParameters[key] {
			if key == "alt" && (len(values) != 1 || values[0] != "json") || key == "$.xgafv" && (len(values) != 1 || values[0] != "2" && values[0] != "1") {
				return nil, invalid("Only alt=json responses are supported.")
			}
			continue
		}
		p, known := method.Parameters[key]
		if !known || p.Location != "query" {
			return nil, invalid("Invalid JSON payload received. Unknown name %q: Cannot bind query parameter.", key)
		}
		if len(values) != 1 && !p.Repeated {
			return nil, invalid("Query parameter %q must appear once.", key)
		}
		value, err := scalarParameter(p, values[0])
		if err != nil {
			return nil, err
		}
		q.params[key] = value
	}
	if err := e.authorize(q); err != nil {
		return nil, err
	}
	for _, name := range bound.Refuse {
		if _, ok := q.params[name]; ok {
			return nil, invalid("Parameter %q is not supported by the simulator.", name)
		}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		return nil, invalid("Requests are limited to 256 KiB.")
	}
	q.body = map[string]any{}
	if method.Request != "" {
		if len(bytes.TrimSpace(data)) == 0 {
			data = []byte("{}")
		}
		raw := map[string]any{}
		parser := json.NewDecoder(bytes.NewReader(data))
		parser.UseNumber()
		if parser.Decode(&raw) != nil || parser.More() {
			return nil, invalid("Invalid JSON payload received.")
		}
		d := &decoder{model: b.model}
		decodedBody, err := d.decode(b.model.Schemas[method.Request], raw, "", 0)
		if err != nil {
			return nil, err
		}
		q.body = decodedBody.(map[string]any)
	} else if len(bytes.TrimSpace(data)) > 0 {
		return nil, invalid("This method does not accept a request body.")
	}
	if bound.Check != nil {
		if err := bound.Check(q.body); err != nil {
			return nil, err
		}
	}
	return q, nil
}

// authorize binds the request to the configured synthetic project, its
// owner and the supported locations. A denied caller learns nothing about
// whether the named resource exists.
func (e *Engine) authorize(q *request) error {
	permission := q.permission()
	q.w.Header().Set("X-Simulator-GCP-Permission", permission)
	if (q.project != e.cfg.Project && q.project != e.cfg.ProjectNumber) || q.subject != e.cfg.Owner {
		return &status{"PERMISSION_DENIED", fmt.Sprintf("Permission '%s' denied on resource '%s' (or it may not exist).", permission, q.path)}
	}
	if q.location != "" {
		region, known := q.location, !q.zone
		if q.zone {
			region, known = zoneRegion(q.location)
		}
		if q.scopePath != "" && region == "global" {
			known = false
		}
		for _, l := range e.cfg.Locations {
			if l == region && known {
				return nil
			}
		}
		return invalid("Location %q is not supported by the simulator.", q.location)
	}
	return nil
}

func (b *binding) requiredFor(p *Property, method string) bool {
	if p.Required {
		return true
	}
	for _, id := range p.RequiredBy {
		if id == b.model.Name+"."+method {
			return true
		}
	}
	return false
}
