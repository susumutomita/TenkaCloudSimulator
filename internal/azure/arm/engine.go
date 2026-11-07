package arm

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

// Config is trusted host composition: one synthetic tenant and subscription
// per environment and its local owner. Request headers never select them.
type Config struct {
	Environment  string
	Subscription string
	Tenant       string
	Owner        string
	Locations    []string
	// BasePath is the external prefix of provider-relative paths, used for
	// absolute nextLink URLs, for example "/_simulator/providers/azure-arm".
	BasePath string
	// Subject returns the authenticated exercise identity of the request.
	Subject func(ctx context.Context) (string, bool)
	Now     func() time.Time
}

type segment struct {
	literal string
	name    string
	multi   bool
}

type route struct {
	id       string
	provider *boundProvider
	method   string
	segments []segment
	literals int
	bound    *bound
	// generic routes, such as Resources_GetById, can match other providers'
	// paths; they apply only to requests that carry their own api-version.
	generic bool
}

type boundProvider struct {
	spec  *Provider
	model *Model
	ops   map[string]*bound
}

type boundType struct {
	provider *boundProvider
	spec     *ResourceType
	full     string // canonical full type, such as "Microsoft.Network/virtualNetworks/subnets"
	key      string // lower-case full type
	group    bool   // the Microsoft.Resources resource group type
	parent   *boundType
	children []*boundType
	kinds    map[Kind][]*bound
	request  string // PUT body definition
	response string // PUT 200 response definition
}

type bound struct {
	provider *boundProvider
	binding  *Binding
	op       *Operation
	rt       *boundType
	segments []segment
	group    string   // resource group variable, "" at subscription scope
	sub      string   // subscription variable
	names    []string // name variables, outermost first
	body     *Parameter
	query    map[string]*Parameter // lower-case official query parameter names
	accepted map[string]bool
}

// Engine serves every reviewed resource provider from one provider database.
type Engine struct {
	cfg       Config
	store     *store
	providers map[string]*boundProvider
	order     []*boundProvider
	types     map[string]*boundType
	routes    []*route
	locations map[string]string
}

// New validates every specification against its embedded official subset
// and binds the synthetic subscription to its persisted owner.
func New(db *sql.DB, cfg Config, providers []*Provider) (*Engine, error) {
	if cfg.Environment == "" || !uuidPattern.MatchString(cfg.Subscription) || !uuidPattern.MatchString(cfg.Tenant) || cfg.Owner == "" || len(cfg.Locations) == 0 || cfg.Subject == nil || !strings.HasPrefix(cfg.BasePath, "/") {
		return nil, errors.New("azure ARM requires an environment, subscription and tenant UUIDs, owner, locations, base path and subject extractor")
	}
	cfg.Subscription, cfg.Tenant = strings.ToLower(cfg.Subscription), strings.ToLower(cfg.Tenant)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s, err := newStore(db, cfg)
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, store: s, providers: map[string]*boundProvider{}, types: map[string]*boundType{}, locations: map[string]string{}}
	for _, l := range cfg.Locations {
		e.locations[normalizeLocation(l)] = l
	}
	for _, p := range providers {
		model, err := loadModel(p.Namespace)
		if err != nil {
			return nil, err
		}
		if err := e.add(p, model); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func normalizeLocation(l string) string { return strings.ToLower(strings.ReplaceAll(l, " ", "")) }

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func parseTemplate(path string, multi []string) ([]segment, int) {
	many := map[string]bool{}
	for _, m := range multi {
		many[m] = true
	}
	var out []segment
	literals := 0
	for _, part := range splitPath(path) {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := strings.Trim(part, "{}")
			out = append(out, segment{name: name, multi: many[name]})
			continue
		}
		out = append(out, segment{literal: part})
		literals++
	}
	return out, literals
}

func (e *Engine) add(p *Provider, model *Model) error {
	key := strings.ToLower(p.Namespace)
	if e.providers[key] != nil {
		return fmt.Errorf("azure ARM provider %s is registered twice", p.Namespace)
	}
	if p.Title == "" || len(p.Issues) == 0 || len(p.Bounds) == 0 || model.Namespace != p.Namespace || model.APIVersion != p.APIVersion {
		return fmt.Errorf("azure ARM provider %s lacks a title, Issue, bounds or matching api-version", p.Namespace)
	}
	bp := &boundProvider{spec: p, model: model, ops: map[string]*bound{}}
	types := map[string]*boundType{}
	for _, t := range p.Types {
		full := p.Namespace + "/" + t.Name
		bt := &boundType{provider: bp, spec: t, full: full, key: strings.ToLower(full), group: p.Namespace == "Microsoft.Resources" && t.Name == "resourceGroups", kinds: map[Kind][]*bound{}}
		if t.Title == "" || t.Limit <= 0 || types[bt.key] != nil || e.types[bt.key] != nil {
			return fmt.Errorf("azure ARM type %s lacks a title or limit, or is registered twice", full)
		}
		types[bt.key] = bt
	}
	for _, bt := range types {
		parts := strings.Split(bt.spec.Name, "/")
		if len(parts) > 1 {
			bt.parent = types[strings.ToLower(p.Namespace+"/"+strings.Join(parts[:len(parts)-1], "/"))]
			if bt.parent == nil {
				return fmt.Errorf("azure ARM type %s has no reviewed parent type", bt.full)
			}
			bt.parent.children = append(bt.parent.children, bt)
		}
	}
	for i := range p.Bindings {
		b := &p.Bindings[i]
		op := model.Operations[b.Operation]
		bt := types[strings.ToLower(p.Namespace+"/"+b.Type)]
		if op == nil || bp.ops[b.Operation] != nil || bt == nil {
			return fmt.Errorf("azure ARM %s operation %s is missing, bound twice or names an unknown type", p.Namespace, b.Operation)
		}
		nb, err := bindOperation(bp, b, op, bt)
		if err != nil {
			return err
		}
		bp.ops[b.Operation] = nb
		bt.kinds[b.Kind] = append(bt.kinds[b.Kind], nb)
	}
	for id := range model.Operations {
		if bp.ops[id] == nil {
			return fmt.Errorf("azure ARM %s embeds operation %s without a reviewed binding", p.Namespace, id)
		}
	}
	for _, bt := range types {
		puts := bt.kinds[Put]
		if len(puts) != 1 || len(bt.kinds[Get]) != 1 {
			return fmt.Errorf("azure ARM type %s needs exactly one create-or-update and one get operation", bt.full)
		}
		bt.request = puts[0].body.Schema
		bt.response = puts[0].op.Responses["200"]
		if bt.response == "" {
			return fmt.Errorf("azure ARM type %s create-or-update has no 200 response definition", bt.full)
		}
		for _, ref := range bt.spec.Refs {
			if ref.Path == "" || !strings.Contains(ref.Type, "/") {
				return fmt.Errorf("azure ARM type %s has a malformed reference", bt.full)
			}
		}
		for _, path := range bt.spec.Form {
			if _, err := e.field(bt, path); err != nil {
				return err
			}
		}
		e.types[bt.key] = bt
	}
	ids := make([]string, 0, len(model.Routes))
	for id := range model.Routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := model.Routes[id]
		segments, literals := parseTemplate(r.Path, r.Multi)
		// A route is generic when a multi-segment variable or a variable
		// namespace after "providers" can stand for another provider's path.
		generic := false
		for i, seg := range segments {
			if seg.multi || strings.EqualFold(seg.literal, "providers") && i+1 < len(segments) && segments[i+1].name != "" {
				generic = true
			}
		}
		e.routes = append(e.routes, &route{id: id, provider: bp, method: r.Method, segments: segments, literals: literals, bound: bp.ops[id], generic: generic})
	}
	e.providers[key] = bp
	e.order = append(e.order, bp)
	sort.Slice(e.order, func(i, j int) bool { return e.order[i].spec.Namespace < e.order[j].spec.Namespace })
	return nil
}

// bindOperation checks that the official path has the ARM shape of the
// reviewed kind and type, and records its variables and parameters.
func bindOperation(bp *boundProvider, b *Binding, op *Operation, bt *boundType) (*bound, error) {
	nb := &bound{provider: bp, binding: b, op: op, rt: bt, query: map[string]*Parameter{}, accepted: map[string]bool{}}
	nb.segments, _ = parseTemplate(op.Path, nil)
	broken := func(reason string) error {
		return fmt.Errorf("azure ARM %s operation %s %s", bp.spec.Namespace, op.ID, reason)
	}
	s := nb.segments
	if len(s) < 2 || !strings.EqualFold(s[0].literal, "subscriptions") || s[1].name == "" {
		return nil, broken("does not start with /subscriptions/{subscriptionId}")
	}
	nb.sub = s[1].name
	i := 2
	if i+1 < len(s) && strings.EqualFold(s[i].literal, "resourceGroups") && s[i+1].name != "" {
		nb.group = s[i+1].name
		i += 2
	}
	var types []string
	if bt.group {
		switch {
		case b.Kind == List && nb.group == "" && len(s) == 3 && strings.EqualFold(s[2].literal, "resourceGroups"):
		case b.Kind != List && nb.group != "" && i == len(s):
			nb.names = []string{nb.group}
		default:
			return nil, broken("is not a resource group path of its kind")
		}
	} else {
		if i+1 >= len(s) || !strings.EqualFold(s[i].literal, "providers") || s[i+1].literal != bp.spec.Namespace {
			return nil, broken("does not name its provider namespace")
		}
		for j := i + 2; j < len(s); j++ {
			if s[j].literal == "" {
				return nil, broken("has a variable type segment")
			}
			types = append(types, s[j].literal)
			if j+1 < len(s) {
				if s[j+1].name == "" {
					return nil, broken("has consecutive type segments")
				}
				nb.names = append(nb.names, s[j+1].name)
				j++
			}
		}
		if !strings.EqualFold(strings.Join(types, "/"), bt.spec.Name) {
			return nil, broken("does not address type " + bt.full)
		}
		item := len(nb.names) == len(types)
		switch {
		case b.Kind == List && item, b.Kind != List && !item:
			return nil, broken("has the wrong shape for its kind")
		case nb.group == "" && (b.Kind != List || len(types) != 1):
			return nil, broken("is subscription-scoped but not a top-level list")
		}
	}
	wantMethod := map[Kind]string{Put: "PUT", Get: "GET", Head: "HEAD", Patch: "PATCH", Delete: "DELETE", List: "GET"}[b.Kind]
	if wantMethod == "" || op.Method != wantMethod {
		return nil, broken("has an unknown kind or a method that does not match it")
	}
	if b.Kind == List && op.Pageable == nil {
		return nil, broken("lists without x-ms-pageable")
	}
	for _, p := range op.Parameters {
		switch p.In {
		case "body":
			nb.body = p
		case "query":
			nb.query[strings.ToLower(p.Name)] = p
		}
	}
	if (b.Kind == Put || b.Kind == Patch) && (nb.body == nil || !nb.body.Required) {
		return nil, broken("writes without a required body")
	}
	for _, name := range b.Query {
		if nb.query[strings.ToLower(name)] == nil {
			return nil, broken("accepts an unofficial query parameter " + name)
		}
		nb.accepted[strings.ToLower(name)] = true
	}
	return nb, nil
}

// match finds the most specific official route for a request path. Generic
// routes without a namespace literal only match their own api-version.
func (e *Engine) match(method string, parts []string, version string) (*route, map[string]string, bool) {
	var best *route
	var bestValues map[string]string
	pathMatched := false
	for _, r := range e.routes {
		if r.generic && version != r.provider.spec.APIVersion {
			continue
		}
		values := map[string]string{}
		if !matchSegments(r.segments, parts, values) {
			continue
		}
		pathMatched = true
		if r.method != method {
			continue
		}
		if best == nil || r.literals > best.literals {
			best, bestValues = r, values
		}
	}
	return best, bestValues, pathMatched
}

func matchSegments(template []segment, parts []string, values map[string]string) bool {
	if len(template) == 0 {
		return len(parts) == 0
	}
	t := template[0]
	if t.multi {
		for n := len(parts) - len(template) + 1; n >= 1; n-- {
			if matchSegments(template[1:], parts[n:], values) {
				values[t.name] = strings.Join(parts[:n], "/")
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if t.literal != "" {
		if !strings.EqualFold(t.literal, parts[0]) {
			return false
		}
	} else {
		values[t.name] = parts[0]
	}
	return matchSegments(template[1:], parts[1:], values)
}

func writeError(w http.ResponseWriter, err error) {
	var e *armError
	if !errors.As(err, &e) {
		e = &armError{500, "InternalServerError", "Simulator ARM state operation failed."}
	}
	writeJSON(w, e.status, map[string]any{"error": map[string]any{"code": e.code, "message": e.message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if value == nil {
		w.WriteHeader(status)
		return
	}
	data, _ := json.Marshal(value)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// ServeHTTP handles one request on a provider-relative ARM path.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-ms-request-id", newUUID())
	parts := splitPath(r.URL.EscapedPath())
	decoded := make([]string, len(parts))
	for i, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "" || strings.Contains(value, "/") {
			writeError(w, fail(400, "InvalidRequestUri", "The request URI is not a valid ARM path."))
			return
		}
		decoded[i] = value
	}
	rt, values, pathMatched := e.match(r.Method, decoded, r.URL.Query().Get("api-version"))
	if rt == nil {
		writeError(w, e.unmatched(decoded, pathMatched))
		return
	}
	// Names the matched official operation so SDK harnesses can prove which
	// operation a client call reached; it grants nothing.
	w.Header().Set("X-Simulator-Azure-Operation", rt.provider.spec.Namespace+"/"+rt.id)
	if err := e.apiVersion(r, rt.provider); err != nil {
		writeError(w, err)
		return
	}
	if rt.bound == nil {
		writeError(w, fail(501, "NotImplemented", "Operation %s of %s api-version %s is not implemented by the simulator.", rt.id, rt.provider.spec.Namespace, rt.provider.spec.APIVersion))
		return
	}
	q, err := e.prepare(w, r, rt.bound, values)
	if err != nil {
		writeError(w, err)
		return
	}
	status, out, err := q.execute()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, out)
}

func (e *Engine) unmatched(parts []string, pathMatched bool) error {
	if pathMatched {
		return fail(405, "MethodNotAllowed", "The HTTP method is not allowed for this ARM path.")
	}
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "providers") {
			if e.providers[strings.ToLower(parts[i+1])] == nil {
				return fail(400, "InvalidResourceNamespace", "The resource namespace '%s' is invalid or not served by the simulator.", parts[i+1])
			}
			return fail(404, "InvalidResourceType", "The resource type could not be found in the namespace '%s' for the simulator's api-version.", parts[i+1])
		}
	}
	return fail(404, "NotFound", "The requested ARM path was not found on this simulator.")
}

// apiVersion requires exactly the provider's reviewed stable api-version.
func (e *Engine) apiVersion(r *http.Request, p *boundProvider) error {
	values := r.URL.Query()["api-version"]
	if len(values) == 0 || values[0] == "" {
		return fail(400, "MissingApiVersionParameter", "The api-version query parameter (?api-version=) is required for all requests.")
	}
	if len(values) != 1 || values[0] != p.spec.APIVersion {
		return fail(400, "InvalidApiVersionParameter", "The api-version '%s' is invalid. The supported version for %s is '%s'.", strings.Join(values, ","), p.spec.Namespace, p.spec.APIVersion)
	}
	return nil
}

type request struct {
	e       *Engine
	b       *bound
	r       *http.Request
	subject string
	values  map[string]string
	names   []string
	group   string
	params  map[string]any
	token   string
	body    map[string]any
	now     time.Time
}

// prepare authenticates, validates parameters, authorizes the subscription
// owner and decodes the request body.
func (e *Engine) prepare(w http.ResponseWriter, r *http.Request, b *bound, values map[string]string) (*request, error) {
	subject, ok := e.cfg.Subject(r.Context())
	if !ok || subject == "" {
		return nil, fail(401, "AuthenticationFailed", "Authentication failed. A valid simulator access token is required.")
	}
	q := &request{e: e, b: b, r: r, subject: subject, values: values, params: map[string]any{}, now: e.cfg.Now().UTC().Truncate(time.Millisecond)}
	for _, p := range b.op.Parameters {
		if p.In != "path" {
			continue
		}
		if _, err := parameterValue(p, values[p.Name]); err != nil {
			return nil, err
		}
	}
	q.group = values[b.group]
	for _, name := range b.names {
		q.names = append(q.names, values[name])
	}
	if err := e.authorize(q); err != nil {
		return nil, err
	}
	for key, list := range r.URL.Query() {
		lower := strings.ToLower(key)
		if lower == "api-version" {
			continue
		}
		if len(list) != 1 {
			return nil, fail(400, "InvalidQueryParameterValue", "Query parameter '%s' must appear once.", key)
		}
		if b.binding.Kind == List && lower == "$skiptoken" {
			q.token = list[0]
			continue
		}
		p := b.query[lower]
		if p == nil {
			return nil, fail(400, "InvalidQueryParameterValue", "Query parameter '%s' is not an official parameter of %s.", key, b.op.ID)
		}
		if !b.accepted[lower] {
			return nil, fail(400, "InvalidQueryParameterValue", "Query parameter '%s' of %s is not supported by the simulator.", key, b.op.ID)
		}
		value, err := parameterValue(p, list[0])
		if err != nil {
			return nil, err
		}
		q.params[lower] = value
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		return nil, fail(413, "RequestEntityTooLarge", "Requests are limited to 256 KiB by the simulator.")
	}
	if b.body == nil {
		if len(bytes.TrimSpace(data)) > 0 {
			return nil, invalidContent("operation %s does not accept a request body.", b.op.ID)
		}
		return q, nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, invalidContent("the request content was empty.")
	}
	raw := map[string]any{}
	parser := json.NewDecoder(bytes.NewReader(data))
	parser.UseNumber()
	if parser.Decode(&raw) != nil || parser.More() {
		return nil, invalidContent("the body must be one JSON object.")
	}
	d := &decoder{model: b.provider.model}
	body, err := d.decode(&Schema{Ref: b.body.Schema}, raw, "", 0)
	if err != nil {
		return nil, err
	}
	q.body = body.(map[string]any)
	return q, nil
}

// action names the descriptive RBAC-style action of a request. It is shown
// in errors only; authorization is the configured owner, not Azure RBAC.
func (q *request) action() string {
	verb := map[Kind]string{Put: "write", Patch: "write", Delete: "delete"}[q.b.binding.Kind]
	if verb == "" {
		verb = "read"
	}
	if q.b.rt.group {
		return "Microsoft.Resources/subscriptions/resourceGroups/" + verb
	}
	return q.b.rt.full + "/" + verb
}

// scope is the ARM scope named by the request path, for error text.
func (q *request) scope() string {
	return q.e.cfg.BasePath + "/" + strings.Join(splitPath(q.r.URL.EscapedPath()), "/")
}

// authorize binds the request to the configured subscription and its owner.
// A denied caller learns nothing about whether the named resource exists.
func (e *Engine) authorize(q *request) error {
	if !strings.EqualFold(q.values[q.b.sub], e.cfg.Subscription) || q.subject != e.cfg.Owner {
		scope := strings.TrimPrefix(q.scope(), e.cfg.BasePath)
		return fail(403, "AuthorizationFailed", "The client '%s' does not have authorization to perform action '%s' over scope '%s' or the scope is invalid.", q.subject, q.action(), scope)
	}
	return nil
}
