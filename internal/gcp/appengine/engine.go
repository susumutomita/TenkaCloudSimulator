// Package appengine serves a bounded native App Engine Admin API v1 subset and
// a local data plane that runs deployed Python 3.12 WSGI versions in the
// embedded CPython/WASI sandbox.
//
// The admin API mirrors the official resource names (apps/{app}/services/
// {service}/versions/{version}), request fields and google.rpc.Status
// errors. State lives in provider-owned SQLite tables. Version code is copied
// from Cloud Storage objects of the configured project at deployment, as App
// Engine copies deployment files, and requests reach a version only through
// the stored traffic split and firewall. Every other official method is
// refused with UNIMPLEMENTED. No Google account, network, real App Engine or
// billing is involved; this is not Google IAM.
package appengine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

const maxRequestBytes = 256 << 10

// Source errors returned by Config.Source.
var (
	ErrSourceNotFound = errors.New("deployment source object not found")
	ErrSourceTooLarge = errors.New("deployment source object too large")
)

// Runner executes one bounded Python invocation. The embedded runtime
// implements it; tests may substitute a recorder.
type Runner interface {
	Run(context.Context, pythonruntime.Invocation) (pythonruntime.Result, error)
}

// Config is trusted host composition: one synthetic project and its owner.
// Request headers never select the project, owner or deployment source.
type Config struct {
	Project string
	Owner   string
	// Subject returns the authenticated exercise identity of the request.
	Subject func(ctx context.Context) (string, bool)
	// Source reads a Cloud Storage object of the configured project, at most
	// limit bytes. It returns ErrSourceNotFound or ErrSourceTooLarge.
	Source func(ctx context.Context, bucket, object string, limit int64) ([]byte, error)
	Now    func() time.Time
	// RequestTimeout bounds one data-plane request (default 10 seconds).
	RequestTimeout time.Duration
	// Runner replaces the lazily started embedded runtime.
	Runner Runner
}

// Engine serves the admin API and the data plane from one database.
type Engine struct {
	cfg      Config
	db       *sql.DB
	model    *model
	routes   []route
	mu       sync.Mutex
	runner   Runner
	owned    *pythonruntime.Runtime
	starting chan struct{}
	startErr error
	closed   bool
	lifetime context.Context
	cancel   context.CancelFunc
}

type status struct{ code, message string }

func (s *status) Error() string { return s.message }

func invalid(message string) error   { return &status{"INVALID_ARGUMENT", message} }
func failed(message string) error    { return &status{"FAILED_PRECONDITION", message} }
func notFound(message string) error  { return &status{"NOT_FOUND", message} }
func exists(message string) error    { return &status{"ALREADY_EXISTS", message} }
func exhausted(message string) error { return &status{"RESOURCE_EXHAUSTED", message} }

var errDenied = &status{"PERMISSION_DENIED", "The caller does not have permission. Only the configured local project owner may manage this App Engine application; this is not Google IAM."}

var httpCodes = map[string]int{"INVALID_ARGUMENT": 400, "FAILED_PRECONDITION": 400, "UNAUTHENTICATED": 401, "PERMISSION_DENIED": 403, "NOT_FOUND": 404,
	"ALREADY_EXISTS": 409, "ABORTED": 409, "RESOURCE_EXHAUSTED": 429, "INTERNAL": 500, "UNIMPLEMENTED": 501, "UNAVAILABLE": 503}

// New validates the embedded official model and creates provider tables.
func New(db *sql.DB, cfg Config) (*Engine, error) {
	if db == nil || !validID.MatchString(cfg.Project) || cfg.Owner == "" || cfg.Subject == nil || cfg.Source == nil {
		return nil, errors.New("App Engine requires SQLite, a project, an owner, a subject extractor and a Cloud Storage source")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	if cfg.RequestTimeout > 30*time.Second {
		return nil, errors.New("App Engine request timeout exceeds the sandbox limit")
	}
	reviewed := map[string]bool{}
	for id := range handlers {
		reviewed[id] = true
	}
	m, routes, err := loadModel(modelJSON, reviewed)
	if err != nil {
		return nil, err
	}
	if err := createTables(db); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Engine{cfg: cfg, db: db, model: m, routes: routes, runner: cfg.Runner, lifetime: lifetime, cancel: cancel}, nil
}

// Close stops the embedded runtime the engine started.
func (e *Engine) Close() error {
	e.cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	owned := e.owned
	e.owned, e.runner = nil, nil
	if owned != nil {
		return owned.Close(context.Background())
	}
	return nil
}

// warm starts the embedded runtime in the background, so the first request
// to a newly deployed version does not wait for interpreter compilation.
func (e *Engine) warm() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.runner != nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	if e.starting == nil {
		e.starting = make(chan struct{})
		go e.start(e.starting)
	}
	return e.starting
}

func (e *Engine) start(done chan struct{}) {
	ctx, cancel := context.WithTimeout(e.lifetime, 2*time.Minute)
	defer cancel()
	runtime, err := pythonruntime.New(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil && e.closed {
		_ = runtime.Close(context.Background())
		err = errors.New("App Engine runtime closed")
	}
	if err == nil {
		e.owned, e.runner = runtime, runtime
	}
	e.startErr, e.starting = err, nil
	close(done)
}

// python waits for the runtime. Waiting is bounded by the request, not by
// the execution deadline of the version.
func (e *Engine) python(ctx context.Context) (Runner, error) {
	done := e.warm()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("App Engine runtime closed")
	}
	if e.runner == nil {
		return nil, e.startErr
	}
	return e.runner, nil
}

// Owns reports whether a provider-relative path addresses App Engine.
func (e *Engine) Owns(r *http.Request) bool {
	first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return first == "appengine" || first == "appspot"
}

// Methods lists the reviewed official method IDs.
func (e *Engine) Methods() []string {
	ids := []string{}
	for _, rt := range e.routes {
		if rt.reviewed {
			ids = append(ids, rt.id)
		}
	}
	return ids
}

// OfficialMethods is the number of official App Engine Admin API v1 methods.
func (e *Engine) OfficialMethods() int { return len(e.routes) }

func fail(w http.ResponseWriter, err error) {
	var s *status
	if !errors.As(err, &s) {
		s = &status{"INTERNAL", "Simulator App Engine state operation failed."}
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
var systemParameters = map[string]bool{"alt": true, "prettyPrint": true, "$.xgafv": true, "key": true, "quotaUser": true, "access_token": true, "oauth_token": true, "upload_protocol": true, "uploadType": true, "callback": true, "fields": true}

// request is one admin API call after routing, authorization and decoding.
type request struct {
	ctx     context.Context
	subject string
	path    map[string]string
	query   url.Values
	body    map[string]json.RawMessage
	method  *method
}

func (q *request) param(name string) string { return q.query.Get(name) }

// ServeHTTP handles a provider-relative path: /appengine/v1/... or /appspot/<host>/...
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	if escaped[0] == "appspot" {
		e.serve(w, r, escaped[1:])
		return
	}
	if escaped[0] != "appengine" || len(escaped) < 2 {
		fail(w, notFound("The requested URL was not found on this simulator."))
		return
	}
	decoded := make([]string, len(escaped)-1)
	for i, s := range escaped[1:] {
		value, err := url.PathUnescape(s)
		if err != nil || value == "" {
			fail(w, notFound("The requested URL was not found on this simulator."))
			return
		}
		decoded[i] = value
	}
	rt, values, ok := match(e.routes, r.Method, decoded)
	if !ok {
		fail(w, notFound("The requested URL was not found on this simulator."))
		return
	}
	if !rt.reviewed {
		fail(w, &status{"UNIMPLEMENTED", "Method appengine." + rt.id + " is not implemented by the simulator."})
		return
	}
	subject, ok := e.cfg.Subject(r.Context())
	if !ok || subject == "" {
		fail(w, &status{"UNAUTHENTICATED", "An authenticated exercise actor is required by this provider."})
		return
	}
	// Authorization precedes validation and existence checks, so another
	// subject or project learns nothing about this application.
	if subject != e.cfg.Owner || values["appsId"] != "" && values["appsId"] != e.cfg.Project {
		fail(w, errDenied)
		return
	}
	q := &request{ctx: r.Context(), subject: subject, path: values, query: r.URL.Query(), method: e.model.Methods[rt.id]}
	for name, list := range q.query {
		p, known := q.method.Parameters[name]
		if !systemParameters[name] && (!known || p.Location != "query") {
			fail(w, invalid("Unknown query parameter "+name+"."))
			return
		}
		if len(list) != 1 {
			fail(w, invalid("Query parameter "+name+" must appear once."))
			return
		}
	}
	if q.method.Request != "" {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			fail(w, invalid("Request bodies are limited to 256 KiB."))
			return
		}
		if len(bytes.TrimSpace(data)) == 0 {
			data = []byte("{}")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err = decoder.Decode(&q.body); err != nil || q.body == nil || decoder.Decode(new(any)) != io.EOF {
			fail(w, invalid("The request body must be one JSON object."))
			return
		}
	} else if r.ContentLength > 0 {
		fail(w, invalid("This method takes no request body."))
		return
	}
	out, err := handlers[rt.id](e, q)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// fields checks the top-level fields of a request body against the official
// schema: supported fields are returned, output-only fields are ignored as
// AIP-203 asks, and every other official or unknown field is refused.
func (e *Engine) fields(body map[string]json.RawMessage, schema string, supported ...string) (map[string]json.RawMessage, error) {
	keep := map[string]json.RawMessage{}
	allowed := map[string]bool{}
	for _, name := range supported {
		allowed[name] = true
	}
	for name, value := range body {
		official, readOnly := e.model.field(schema, name)
		switch {
		case allowed[name]:
			if string(value) != "null" {
				keep[name] = value
			}
		case readOnly || official && outputOnly[schema+"."+name]:
		case official:
			return nil, invalid("Field " + name + " of " + schema + " is not supported by the local App Engine simulator.")
		default:
			return nil, invalid("Unknown field " + name + " in " + schema + ".")
		}
	}
	return keep, nil
}

// outputOnly lists fields the discovery document marks only with a trailing
// "@OutputOnly" or a server-assigned identifier, which clients may echo back.
var outputOnly = map[string]bool{"Version.createTime": true, "Version.name": true, "Application.name": true, "Service.id": true, "Service.name": true}

func decodeString(raw json.RawMessage, name string) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", invalid("Field " + name + " must be a string.")
	}
	return s, nil
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }
