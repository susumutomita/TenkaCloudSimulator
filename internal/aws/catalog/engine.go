package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Authorizer evaluates the caller's local identity policy for one IAM action.
// It returns the simulator decision string, where only "allowed" permits.
type Authorizer func(ctx context.Context, principal, action, resource string) (string, error)

// Config is trusted composition input supplied by the AWS provider.
type Config struct {
	Regions          []string
	Account          string
	DefaultPrincipal string
	Authorize        Authorizer
	Now              func() time.Time
}

// binding joins one reviewed specification with its official model.
type binding struct {
	spec   *Service
	model  *Model
	ops    map[string]*Op
	routes []route
}

// Engine serves every reviewed catalog service from one provider database.
type Engine struct {
	cfg       Config
	store     *store
	bindings  []*binding
	byTarget  map[string]*binding
	bySigning map[string][]*binding
}

// New validates every reviewed specification against its embedded official
// model before serving. An inconsistent specification fails composition.
func New(db *sql.DB, cfg Config, services []*Service) (*Engine, error) {
	if cfg.Authorize == nil || len(cfg.Regions) == 0 || cfg.Account == "" || cfg.DefaultPrincipal == "" {
		return nil, errors.New("the AWS catalog requires regions, an account, a default principal and an authorizer")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s, err := newStore(db)
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, store: s, byTarget: map[string]*binding{}, bySigning: map[string][]*binding{}}
	seen := map[string]bool{}
	for _, spec := range services {
		if seen[spec.Model] {
			return nil, fmt.Errorf("catalog service %s is registered twice", spec.Model)
		}
		seen[spec.Model] = true
		b, err := bind(spec)
		if err != nil {
			return nil, err
		}
		if err := e.add(b); err != nil {
			return nil, err
		}
	}
	sort.Slice(e.bindings, func(i, j int) bool { return e.bindings[i].spec.Model < e.bindings[j].spec.Model })
	return e, nil
}

func (e *Engine) add(b *binding) error {
	switch b.model.Metadata.Protocol {
	case "json":
		e.byTarget[b.model.Metadata.TargetPrefix] = b
	case "rest-json":
		name := b.signingName()
		e.bySigning[name] = append(e.bySigning[name], b)
	default:
		return fmt.Errorf("catalog service %s uses unsupported protocol %s", b.spec.Model, b.model.Metadata.Protocol)
	}
	e.bindings = append(e.bindings, b)
	return nil
}

func (b *binding) signingName() string {
	if b.model.Metadata.SigningName != "" {
		return b.model.Metadata.SigningName
	}
	return b.model.Metadata.EndpointPrefix
}

func bind(spec *Service) (*binding, error) {
	model, err := loadModel(spec.Model)
	if err != nil {
		return nil, err
	}
	return bindModel(spec, model)
}

// bindModel checks one specification against an already validated model.
func bindModel(spec *Service, model *Model) (*binding, error) {
	b := &binding{spec: spec, model: model, ops: map[string]*Op{}}
	if spec.Title == "" || spec.IAMPrefix == "" || len(spec.Issues) == 0 {
		return nil, fmt.Errorf("catalog service %s lacks a title, IAM prefix or Issue", spec.Model)
	}
	kinds := map[string]bool{}
	for _, res := range spec.Resources {
		if kinds[res.Kind] || model.Shapes[res.Shape] == nil || model.shape(res.Shape).Type != "structure" {
			return nil, fmt.Errorf("catalog %s resource %s has a duplicate kind or missing official shape", spec.Model, res.Kind)
		}
		if res.Summary != "" && model.Shapes[res.Summary] == nil {
			return nil, fmt.Errorf("catalog %s resource %s summary shape is missing", spec.Model, res.Kind)
		}
		if res.Parent != "" && spec.resource(res.Parent) == nil {
			return nil, fmt.Errorf("catalog %s resource %s has an unknown parent", spec.Model, res.Kind)
		}
		if !strings.HasPrefix(res.ARN, "arn:{partition}:") || res.ARNMember == "" && res.NameMember == "" && res.IDMember == "" {
			return nil, fmt.Errorf("catalog %s resource %s has no ARN template or identity member", spec.Model, res.Kind)
		}
		if _, err := newID(res.ID, "name"); err != nil {
			return nil, fmt.Errorf("catalog %s resource %s: %w", spec.Model, res.Kind, err)
		}
		kinds[res.Kind] = true
	}
	for i := range spec.Ops {
		op := &spec.Ops[i]
		official := model.Operations[op.Name]
		if official == nil {
			return nil, fmt.Errorf("catalog %s operation %s is not in the embedded official subset", spec.Model, op.Name)
		}
		if b.ops[op.Name] != nil {
			return nil, fmt.Errorf("catalog %s operation %s is bound twice", spec.Model, op.Name)
		}
		switch op.Kind {
		case Create, Read, List, Update, Delete, Put:
			if spec.resource(op.Resource) == nil {
				return nil, fmt.Errorf("catalog %s operation %s names an unknown resource", spec.Model, op.Name)
			}
		case Tag, Untag, ListTags:
		default:
			return nil, fmt.Errorf("catalog %s operation %s has an unknown kind", spec.Model, op.Name)
		}
		input := model.Shapes[official.Input]
		for _, member := range append([]string{strings.Split(op.Lookup, ".")[0], op.Parent}, op.LookupAlt...) {
			if member != "" && (input == nil || input.Members[member].Shape == "") {
				return nil, fmt.Errorf("catalog %s operation %s names missing input member %s", spec.Model, op.Name, member)
			}
		}
		singleton := spec.resource(op.Resource) != nil && spec.resource(op.Resource).Singleton
		if (op.Kind == Read || op.Kind == Update || op.Kind == Delete || op.Kind == Put || op.Kind == Tag || op.Kind == Untag || op.Kind == ListTags) && op.Lookup == "" && !singleton {
			return nil, fmt.Errorf("catalog %s operation %s needs an identifying input member", spec.Model, op.Name)
		}
		if op.HasMore != "" && (model.Shapes[official.Output] == nil || model.shape(official.Output).Members[op.HasMore].Shape == "") {
			return nil, fmt.Errorf("catalog %s operation %s reports more pages in a missing output member", spec.Model, op.Name)
		}
		if op.Failures != "" && (model.Shapes[official.Output] == nil || model.shape(official.Output).Members[op.Failures].Shape == "" || op.Names == "") {
			return nil, fmt.Errorf("catalog %s operation %s reports failures in a missing output member", spec.Model, op.Name)
		}
		if res := spec.resource(op.Resource); res != nil && res.Parent != "" && op.Parent == "" {
			return nil, fmt.Errorf("catalog %s operation %s needs the parent identifier member", spec.Model, op.Name)
		}
		b.ops[op.Name] = op
	}
	for name := range model.Operations {
		if b.ops[name] == nil {
			return nil, fmt.Errorf("catalog %s embeds operation %s without a reviewed binding", spec.Model, name)
		}
	}
	if model.Metadata.Protocol == "rest-json" {
		names := make([]string, 0, len(model.Routes))
		for name := range model.Routes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			b.routes = append(b.routes, parseRoute(name, model.Routes[name].HTTP))
		}
	}
	return b, nil
}

// Owns reports whether the request addresses a catalog service: a JSON
// protocol target prefix, or a REST signing scope owned by the catalog.
func (e *Engine) Owns(r *http.Request) bool {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		cut := strings.LastIndex(target, ".")
		if cut < 1 {
			return false
		}
		_, owned := e.byTarget[target[:cut]]
		return owned
	}
	_, owned := e.bySigning[restService(r)]
	return owned
}

// restService names the REST service of a request: the SigV4 credential
// scope for SDKs, or X-Simulator-Service for unsigned first-party console
// and API-reference requests. The header is routing only, never authority.
func restService(r *http.Request) string {
	if service := credentialService(r); service != "" {
		return service
	}
	if r.Header.Get("Authorization") != "" {
		return ""
	}
	return r.Header.Get("X-Simulator-Service")
}

// credentialService extracts the SigV4 credential-scope service name.
func credentialService(r *http.Request) string {
	_, credential, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	if !ok {
		return ""
	}
	credential, _, _ = strings.Cut(credential, ",")
	parts := strings.Split(strings.TrimSpace(credential), "/")
	if len(parts) != 5 {
		return ""
	}
	return parts[3]
}

// ServeHTTP decodes, authorizes, executes and encodes one catalog request.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, op, labels, failure := e.resolve(r)
	if failure != nil {
		var model *Model
		if b != nil {
			model = b.model
		}
		writeError(w, model, failure.code, failure.status, failure.message)
		return
	}
	region, err := e.region(r, b)
	if err != nil {
		e.fail(w, b, op, err)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		e.fail(w, b, op, err)
		return
	}
	official := b.model.Operations[op.Name]
	var raw map[string]any
	if b.model.Metadata.Protocol == "json" {
		raw, err = decodeJSONObject(body)
	} else {
		raw, err = restInput(b.model, official, r, labels, body)
	}
	if err != nil {
		e.fail(w, b, op, err)
		return
	}
	input := map[string]any{}
	if official.Input != "" {
		d := &decoder{model: b.model}
		decoded, err := d.decode(official.Input, raw, official.Input, 0)
		if err != nil {
			e.fail(w, b, op, err)
			return
		}
		input = decoded.(map[string]any)
	} else if len(raw) > 0 {
		e.fail(w, b, op, invalid("This operation accepts no input members."))
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = e.cfg.DefaultPrincipal
	}
	q := &request{e: e, b: b, op: op, official: official, region: region, principal: principal, input: input, ctx: r.Context(), w: w}
	output, err := q.execute()
	if err != nil {
		e.fail(w, b, op, err)
		return
	}
	writeOutput(w, b.model, official, output)
}

type resolution struct {
	code, message string
	status        int
}

func (e *Engine) resolve(r *http.Request) (*binding, *Op, map[string]string, *resolution) {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		cut := strings.LastIndex(target, ".")
		b := e.byTarget[target[:cut]]
		name := target[cut+1:]
		if r.Method != http.MethodPost || r.URL.Path != "/" {
			return b, nil, nil, &resolution{"SerializationException", "Use POST on the regional root for JSON protocol operations.", 400}
		}
		if _, official := b.model.Routes[name]; !official {
			return b, nil, nil, &resolution{"UnknownOperationException", "Unknown operation " + name + ".", 400}
		}
		op := b.ops[name]
		if op == nil {
			return b, nil, nil, &resolution{"UnknownOperationException", "The simulator does not implement " + b.spec.Title + " " + name + ".", 501}
		}
		return b, op, nil, nil
	}
	candidates := e.bySigning[restService(r)]
	escaped := splitEscapedPath(r)
	query := r.URL.Query()
	var best *binding
	var bestRoute route
	var bestLabels map[string]string
	for _, b := range candidates {
		for _, rt := range b.routes {
			labels, ok := rt.match(r.Method, escaped, query)
			if ok && (best == nil || rt.literals > bestRoute.literals) {
				best, bestRoute, bestLabels = b, rt, labels
			}
		}
	}
	if best == nil {
		if len(candidates) == 0 {
			return nil, nil, nil, &resolution{"UnknownOperationException", "No catalog service owns this request.", 404}
		}
		return candidates[0], nil, nil, &resolution{"UnknownOperationException", "No official operation matches this method and path.", 404}
	}
	op := best.ops[bestRoute.op]
	if op == nil {
		return best, nil, nil, &resolution{"UnknownOperationException", "The simulator does not implement " + best.spec.Title + " " + bestRoute.op + ".", 501}
	}
	return best, op, bestLabels, nil
}

func (e *Engine) region(r *http.Request, b *binding) (string, error) {
	if a := r.Header.Get("Authorization"); a != "" {
		_, credential, ok := strings.Cut(a, "Credential=")
		key, _, _ := strings.Cut(credential, "/")
		if !ok || !strings.HasPrefix(key, "TCSIM") {
			return "", &fault{kind: "denied", message: "Use unsigned requests or dummy TCSIM credentials."}
		}
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", invalid("Session tokens are not supported.")
	}
	region := r.Header.Get("X-Simulator-Region")
	_, credential, _ := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	credential, _, _ = strings.Cut(credential, ",")
	if parts := strings.Split(strings.TrimSpace(credential), "/"); len(parts) == 5 {
		if region != "" && region != parts[2] {
			return "", invalid("Region header and credential scope disagree.")
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	for _, supported := range e.cfg.Regions {
		if region == supported {
			if b.spec.Global {
				return "aws-global", nil
			}
			return region, nil
		}
	}
	return "", invalid("Unsupported simulator region.")
}

// fail maps a typed failure onto the operation's modeled exception.
func (e *Engine) fail(w http.ResponseWriter, b *binding, op *Op, err error) {
	var f *fault
	if !errors.As(err, &f) {
		f = &fault{kind: "internal", message: "Simulator catalog state operation failed."}
	}
	code, status := b.errorCode(op.Name, f.kind)
	if f.code != "" {
		for _, name := range b.model.Operations[op.Name].Errors {
			if name == f.code {
				code, status = name, b.status(name, f.kind)
			}
		}
	}
	writeError(w, b.model, code, status, f.message)
}

// errorFamilies lists, in priority order, exact names or documented name
// fragments of modeled exceptions for each failure family.
var errorFamilies = map[string][]string{
	"validation":    {"ValidationException", "InvalidParameterException", "InvalidParameterValueException", "InvalidParametersException", "InvalidInputException", "InvalidRequestException", "BadRequestException", "InvalidArgumentException", "InvalidParameterCombinationException", "ClientException"},
	"unsupported":   {"ValidationException", "InvalidParameterException", "InvalidParameterValueException", "InvalidParametersException", "InvalidInputException", "InvalidRequestException", "BadRequestException", "InvalidArgumentException", "ClientException"},
	"notfound":      {"ResourceNotFoundException", "NotFoundException", "NotFound", "NoSuch", "DoesNotExist", "UnknownResource"},
	"conflict":      {"ResourceAlreadyExistsException", "AlreadyExistsException", "AlreadyExists", "NameExists", "Duplicate", "NameInUse", "ResourceInUseException"},
	"dependents":    {"NotEmpty", "ResourceInUseException", "InUse", "DependencyViolation"},
	"limit":         {"ServiceQuotaExceededException", "LimitExceededException", "QuotaExceeded", "LimitExceeded"},
	"tags":          {"TooManyTagsException", "TagsLimitExceededException", "TagLimitExceededException", "TooManyTags", "TagLimit", "TagsLimit", "LimitExceededException"},
	"mismatch":      {"IdempotentParameterMismatchException", "IdempotentParameterMismatch", "Idempotency"},
	"denied":        {"AccessDeniedException"},
	"internal":      {"InternalServerException", "InternalFailure", "InternalServiceError", "InternalServerError", "ServiceException", "ServerException"},
	"serialization": {"SerializationException"},
	"token":         {"InvalidNextTokenException", "InvalidNextToken", "InvalidToken", "InvalidPaginationToken", "ValidationException", "InvalidParameterException", "InvalidParameterValueException", "InvalidInputException", "InvalidRequestException", "BadRequestException"},
}

// genericFamilies are tried only after every specific candidate failed.
var genericFamilies = map[string][]string{
	"conflict": {"ConflictException", "Conflict"}, "dependents": {"ConflictException", "Conflict"},
	"mismatch": {"ConflictException"},
}

// fallbackToValidation lists families whose unmodeled failures use the
// operation's own validation exception rather than an invented name.
var fallbackToValidation = map[string]bool{"conflict": true, "dependents": true, "mismatch": true, "token": true, "notfound": true}

var familyDefaults = map[string][2]any{
	"validation": {"ValidationException", 400}, "unsupported": {"ValidationException", 400},
	"notfound": {"ResourceNotFoundException", 404}, "conflict": {"ConflictException", 409},
	"dependents": {"ConflictException", 409},
	"limit":      {"LimitExceededException", 400}, "tags": {"LimitExceededException", 400},
	"mismatch": {"IdempotentParameterMismatchException", 400},
	"denied":   {"AccessDeniedException", 403}, "internal": {"InternalFailure", 500},
	"serialization": {"SerializationException", 400},
	"token":         {"ValidationException", 400},
}

// unrelatedFragment reports modeled exceptions that only look like a family
// member: errors about dependent keys, tag counts or request throttling.
func unrelatedFragment(name, kind string) bool {
	lower := strings.ToLower(name)
	if kind == "denied" || strings.HasPrefix(lower, "kms") || strings.HasPrefix(lower, "encryptionkey") {
		return true
	}
	if kind != "tags" && strings.Contains(lower, "tag") {
		return true
	}
	return strings.Contains(lower, "request") && kind != "validation" && kind != "unsupported" && kind != "token" || strings.Contains(lower, "throttl")
}

// errorCode chooses the modeled exception of this operation for a failure
// family: every exact candidate first, then documented name fragments.
func (b *binding) errorCode(opName, kind string) (string, int) {
	if official := b.model.Operations[opName]; official != nil {
		for _, candidates := range [][]string{errorFamilies[kind], genericFamilies[kind]} {
			if name, ok := pick(official.Errors, candidates, kind); ok {
				return name, b.status(name, kind)
			}
		}
		if fallbackToValidation[kind] {
			for _, candidate := range errorFamilies["validation"] {
				for _, name := range official.Errors {
					if name == candidate {
						return name, b.status(name, "validation")
					}
				}
			}
		}
	}
	code := familyDefaults[kind][0].(string)
	return code, b.status(code, kind)
}

// pick prefers any exact candidate, then documented name fragments.
func pick(errors, candidates []string, kind string) (string, bool) {
	for _, candidate := range candidates {
		for _, name := range errors {
			if name == candidate {
				return name, true
			}
		}
	}
	for _, candidate := range candidates {
		for _, name := range errors {
			if !unrelatedFragment(name, kind) && strings.Contains(name, candidate) {
				return name, true
			}
		}
	}
	return "", false
}

func (b *binding) status(code, kind string) int {
	if shape := b.model.Shapes[code]; shape != nil && shape.Error != nil && shape.Error.HTTPStatus != 0 {
		return shape.Error.HTTPStatus
	}
	if b.model.Metadata.Protocol == "json" && kind != "denied" && kind != "internal" {
		return 400
	}
	return familyDefaults[kind][1].(int)
}
