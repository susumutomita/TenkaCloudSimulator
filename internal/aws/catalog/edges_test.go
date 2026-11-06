package catalog

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUnsignedFirstPartyRESTRoutingAndScopeParsing(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest("GET", "/apps", nil)
	req.Header.Set("X-Simulator-Service", "amplify")
	if !h.engine.Owns(req) {
		t.Fatal("unsigned first-party REST requests are routed by X-Simulator-Service")
	}
	expect(t, h.do(req), 200, "")
	req.Header.Set("Authorization", "Bearer something")
	if h.engine.Owns(req) {
		t.Fatal("a non-SigV4 Authorization header must not fall back to the routing hint")
	}
	for _, header := range []string{"AWS4-HMAC-SHA256 Credential=TCSIM/20261106/us-east-1", "AWS4-HMAC-SHA256 SignedHeaders=host"} {
		r := httptest.NewRequest("GET", "/apps", nil)
		r.Header.Set("Authorization", header)
		if credentialService(r) != "" {
			t.Fatalf("malformed scope accepted: %s", header)
		}
	}
	noDot := httptest.NewRequest("POST", "/", nil)
	noDot.Header.Set("X-Amz-Target", "NoDelimiter")
	if h.engine.Owns(noDot) {
		t.Fatal("a target without an operation is not owned")
	}
	recorder := httptest.NewRecorder()
	unowned := httptest.NewRequest("GET", "/x", nil)
	h.engine.ServeHTTP(recorder, unowned)
	if recorder.Code != 404 {
		t.Fatalf("an unowned request reaching the engine is refused, got %d", recorder.Code)
	}
	root := httptest.NewRequest("GET", "/", nil)
	if splitEscapedPath(root) != nil {
		t.Fatal("the root path has no segments")
	}
}

func TestHTTPScalarConversions(t *testing.T) {
	m := restModel()
	if v := stringScalar(m, "Count", "7", "querystring"); v != json.Number("7") {
		t.Fatalf("integer %v", v)
	}
	if v := stringScalar(m, "Flag", "false", "querystring"); v != false {
		t.Fatalf("boolean %v", v)
	}
	if v := stringScalar(m, "Time", "Fri, 06 Nov 2026 12:00:00 GMT", "header"); v != "2026-11-06T12:00:00Z" {
		t.Fatalf("header time %v", v)
	}
	if v := stringScalar(m, "Time", "2026-11-06T12:00:00Z", "querystring"); v != "2026-11-06T12:00:00Z" {
		t.Fatalf("query time %v", v)
	}
	if v := stringScalar(m, "Texts", "a", "querystring"); v != nil {
		t.Fatalf("lists are not scalars: %v", v)
	}
}

func TestProjectionConvertsOrDropsMismatchedStoredValues(t *testing.T) {
	m := syntheticModel()
	m.Shapes["Float"] = &Shape{Type: "float"}
	enc := &encoder{model: m, protocol: "json"}
	cases := []struct {
		shape string
		in    any
		want  any
		ok    bool
	}{
		{"Size", float64(3), int64(3), true}, {"Size", int64(4), int64(4), true}, {"Size", 5, int64(5), true},
		{"Size", json.Number("6"), int64(6), true}, {"Size", "x", nil, false},
		{"Weight", int64(2), float64(2), true}, {"Weight", 3, float64(3), true}, {"Weight", json.Number("1.5"), 1.5, true}, {"Weight", "x", nil, false},
		{"Float", 1.25, 1.25, true}, {"Flag", "yes", false, false}, {"Text", 3, "", false}, {"Data", 1, "", false},
		{"Time", "not a time", nil, false}, {"Time", 12, nil, false},
		{"Labels", "x", nil, false}, {"Tags", "x", nil, false}, {"Widget", "x", nil, false}, {"Mystery", "x", nil, false},
	}
	for _, c := range cases {
		got, ok := enc.project(c.shape, c.in, 0)
		if ok != c.ok || ok && got != c.want {
			t.Fatalf("%s(%v) = %v %v", c.shape, c.in, got, ok)
		}
	}
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if got, _ := enc.project("Time", at, 0); got != json.Number("1793966400") {
		t.Fatalf("time value %v", got)
	}
	xml := &encoder{model: m, protocol: "query"}
	if got, _ := xml.project("Time", at, 0); got != "2026-11-06T12:00:00.000Z" {
		t.Fatalf("iso time %v", got)
	}
	if got, ok := enc.project("Meta", map[string]any{"a": 1}, 0); !ok || got.(map[string]any)["a"] != 1 {
		t.Fatalf("documents pass through %v", got)
	}
	if _, ok := enc.project("Text", nil, 0); ok {
		t.Fatal("nil is omitted")
	}
	if err := stringRules(&Shape{}, "\xff", "x"); err == nil {
		t.Fatal("invalid UTF-8 refused")
	}
	if _, err := floatValue("NaN", "x"); err == nil {
		t.Fatal("NaN refused")
	}
	if err := lengthRule(&Shape{Min: f(2)}, 1, "x"); err == nil {
		t.Fatal("list minimum refused")
	}
}

func TestReferenceSchemaCoversEveryShapeType(t *testing.T) {
	m := syntheticModel()
	b := &binding{spec: syntheticSpec(), model: m}
	schemas := map[string]any{}
	for name, want := range map[string]string{"Weight": "number", "Data": "string", "Flag": "boolean", "Labels": "object", "Tags": "array"} {
		if got := b.schema(name, schemas, 0)["type"]; got != want {
			t.Fatalf("%s schema type %v", name, got)
		}
	}
	if _, ok := b.schema("Time", schemas, 0)["type"].([]any); !ok {
		t.Fatal("timestamps accept numbers and strings")
	}
	b.schema("Meta", schemas, 0)
	b.schema("Choice", schemas, 0)
	if schemas["Choice"].(map[string]any)["maxProperties"] != 1 || schemas["Meta"].(map[string]any)["description"] == nil {
		t.Fatalf("union/document schemas %v", schemas)
	}
	if !strings.Contains(b.schema("Mystery", schemas, 0)["description"].(string), "not accepted") {
		t.Fatal("unsupported shape types are labeled")
	}
	b.schema("Widget", schemas, 0)
	if b.schema("Widget", schemas, 0)["$ref"] != "#/components/schemas/Widget" {
		t.Fatal("named structures are referenced once")
	}
}

func TestResourceBoundsReferencesAndTagEdges(t *testing.T) {
	h := syntheticHarness(t)
	huge := strings.Repeat("x", maxDocumentBytes)
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "big", "Meta": map[string]any{"text": huge}}), 400, "LimitExceededException")
	created := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "ok"})
	expect(t, created, 200, "")
	h2 := newHarness(t)
	cc := h2.json1("codeconnections", "CreateConnection", map[string]any{"ConnectionName": "c", "ProviderType": "GitHub"})
	arn := str(cc.body["ConnectionArn"])
	// A reference must name a resource of the declared kind.
	b := h2.binding("codeconnections")
	q := &request{e: h2.engine, b: b, op: b.ops["CreateConnection"], official: b.model.Operations["CreateConnection"]}
	var wrong *fault
	if err := q.checkRefs(b.spec.resource("connection"), map[string]any{"HostArn": arn}); !errors.As(err, &wrong) || wrong.kind != "notfound" {
		t.Fatalf("a reference to another kind must be refused: %v", err)
	}
	expect(t, h2.json1("codeconnections", "UntagResource", map[string]any{"ResourceArn": arn, "TagKeys": []any{"a"}}), 200, "")
	tags := []any{}
	for i := 0; i < maxTagsPerResource+1; i++ {
		tags = append(tags, map[string]any{"Key": "k" + string(rune('a'+i%26)) + string(rune('a'+i/26)), "Value": "v"})
	}
	expect(t, h2.json1("codeconnections", "TagResource", map[string]any{"ResourceArn": arn, "Tags": tags}), 400, "LimitExceededException")
	h2.policy.deny["codeconnections:ListTagsForResource"] = true
	expect(t, h2.json1("codeconnections", "ListTagsForResource", map[string]any{"ResourceArn": arn}), 403, "AccessDeniedException")
	inject(t, h2.db, "DROP TABLE aws_catalog_resources")
	expect(t, h2.json1("codeconnections", "TagResource", map[string]any{"ResourceArn": arn, "Tags": []any{}}), 500, "InternalFailure")
	if id, err := newID("hex:8:h-", ""); err != nil || len(id) != 10 || strings.Trim(id[2:], "0123456789abcdef") != "" {
		t.Fatalf("hex identifier %q %v", id, err)
	}
}

func TestNamelessIdentifiersAndUnexpectedInputAreRefused(t *testing.T) {
	spec := syntheticSpec()
	spec.Resources[0].ID = "name"
	spec.Resources[0].NameMember = "Missing"
	h := newHarness(t, ecr)
	b, err := bindModel(spec, syntheticModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "x"}), 400, "ValidationException")
	f := featureHarness(t)
	g := f.binding("features")
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"Mode":"x"}`))
	req.Header.Set("X-Amz-Target", g.model.Metadata.TargetPrefix+".GetSettings")
	expect(t, f.do(req), 400, "ValidationException")
	q := &request{e: f.engine, b: g, op: g.ops["PutRule"], official: g.model.Operations["PutRule"], region: "us-east-1", input: map[string]any{}, ctx: t.Context(), w: httptest.NewRecorder()}
	err = q.commit(func(*sqlTx) error { return errors.New("disk unavailable") })
	if err == nil || err.Error() != "disk unavailable" {
		t.Fatalf("storage errors are returned unchanged: %v", err)
	}
}

func TestExceptionNamesNeverBorrowDependentKMSErrors(t *testing.T) {
	m := syntheticModel()
	m.Shapes["KmsAccessDeniedException"] = &Shape{Type: "structure", Exception: true}
	m.Shapes["KmsKeyNotFoundException"] = &Shape{Type: "structure", Exception: true}
	m.Operations["GetWidget"].Errors = []string{"KmsAccessDeniedException", "KmsKeyNotFoundException"}
	b := &binding{spec: syntheticSpec(), model: m}
	if code, status := b.errorCode("GetWidget", "denied"); code != "AccessDeniedException" || status != 403 {
		t.Fatalf("denied mapped to %s %d", code, status)
	}
	if code, _ := b.errorCode("GetWidget", "notfound"); code != "ResourceNotFoundException" {
		t.Fatalf("not found mapped to %s", code)
	}
}
