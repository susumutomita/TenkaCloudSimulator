package catalog

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
)

type sqlTx = sql.Tx

func f(v float64) *float64 { return &v }

// syntheticModel exercises every scalar type and binding trait that the
// embedded official subsets may use, independent of any one AWS service.
func syntheticModel() *Model {
	str := &Shape{Type: "string", Min: f(1), Max: f(16), Pattern: "[a-z0-9-]+"}
	return &Model{
		Name:     "synthetic",
		Source:   Source{Package: "botocore", Version: "test", SHA256: "test", License: "Apache-2.0"},
		Metadata: Metadata{Protocol: "json", JSONVersion: "1.1", TargetPrefix: "Synthetic_2026", SigningName: "synthetic"},
		Routes:   map[string]Route{"CreateWidget": {}, "GetWidget": {}, "DeleteWidget": {}, "ListWidgets": {}, "UpdateWidget": {}, "RebootWidget": {}},
		Operations: map[string]*Operation{
			"CreateWidget": {Name: "CreateWidget", Input: "CreateInput", Output: "WidgetOutput", Errors: []string{"ConflictException", "IdempotentParameterMismatchException"}},
			"GetWidget":    {Name: "GetWidget", Input: "IDInput", Output: "WidgetOutput"},
			"UpdateWidget": {Name: "UpdateWidget", Input: "UpdateInput", Output: "WidgetOutput"},
			"DeleteWidget": {Name: "DeleteWidget", Input: "IDInput"},
			"ListWidgets":  {Name: "ListWidgets", Input: "ListInput", Output: "ListOutput", Paginator: &Paginator{InputToken: "Next", OutputToken: "Next", LimitKey: "Limit"}},
		},
		Shapes: map[string]*Shape{
			"Name":    str,
			"Text":    {Type: "string"},
			"Size":    {Type: "integer", Min: f(1), Max: f(10)},
			"Weight":  {Type: "double", Min: f(0)},
			"Long":    {Type: "long"},
			"Flag":    {Type: "boolean"},
			"Time":    {Type: "timestamp"},
			"Data":    {Type: "blob", Max: f(8)},
			"Meta":    {Type: "structure", Document: true},
			"Choice":  {Type: "structure", Union: true, Members: map[string]Member{"A": {Shape: "Text"}, "B": {Shape: "Size"}}},
			"Labels":  {Type: "map", Key: &Member{Shape: "Name"}, Value: &Member{Shape: "Text"}, Max: f(3)},
			"Tag":     {Type: "structure", Members: map[string]Member{"Key": {Shape: "Text"}, "Value": {Shape: "Text"}}, Required: []string{"Key", "Value"}},
			"Tags":    {Type: "list", Member: &Member{Shape: "Tag"}, Max: f(60)},
			"Names":   {Type: "list", Member: &Member{Shape: "Text"}},
			"Mystery": {Type: "event"},
			"CreateInput": {Type: "structure", Required: []string{"Name"}, Members: map[string]Member{
				"Name": {Shape: "Name"}, "ClientToken": {Shape: "Text", IdempotencyTok: true}, "Size": {Shape: "Size"},
				"Weight": {Shape: "Weight"}, "Count": {Shape: "Long"}, "On": {Shape: "Flag"}, "Created": {Shape: "Time"},
				"Data": {Shape: "Data"}, "Meta": {Shape: "Meta"}, "Choice": {Shape: "Choice"}, "Labels": {Shape: "Labels"},
				"Tags": {Shape: "Tags"}, "Mystery": {Shape: "Mystery"}}},
			"Widget": {Type: "structure", Members: map[string]Member{
				"WidgetId": {Shape: "Text"}, "WidgetArn": {Shape: "Text"}, "Name": {Shape: "Name"}, "Size": {Shape: "Size"},
				"Weight": {Shape: "Weight"}, "Count": {Shape: "Long"}, "On": {Shape: "Flag"}, "Created": {Shape: "Time"},
				"Data": {Shape: "Data"}, "Meta": {Shape: "Meta"}, "Choice": {Shape: "Choice"}, "Labels": {Shape: "Labels"},
				"Mystery": {Shape: "Mystery"}, "Tags": {Shape: "Tags"}}},
			"WidgetOutput":                         {Type: "structure", Members: map[string]Member{"Widget": {Shape: "Widget"}, "WidgetArn": {Shape: "Text"}, "WidgetId": {Shape: "Text"}, "Tags": {Shape: "Tags"}}},
			"IDInput":                              {Type: "structure", Required: []string{"WidgetId"}, Members: map[string]Member{"WidgetId": {Shape: "Text"}}},
			"UpdateInput":                          {Type: "structure", Required: []string{"WidgetId"}, Members: map[string]Member{"WidgetId": {Shape: "Text"}, "Size": {Shape: "Size"}}},
			"ListInput":                            {Type: "structure", Members: map[string]Member{"Next": {Shape: "Text"}, "Limit": {Shape: "Size"}, "Names": {Shape: "Names"}}},
			"ListOutput":                           {Type: "structure", Members: map[string]Member{"Widgets": {Shape: "WidgetList"}, "Ids": {Shape: "Names"}, "Next": {Shape: "Text"}}},
			"WidgetList":                           {Type: "list", Member: &Member{Shape: "Widget"}},
			"ConflictException":                    {Type: "structure", Exception: true, Error: &ErrorTrait{HTTPStatus: 409}, Members: map[string]Member{"Message": {Shape: "Text"}}},
			"IdempotentParameterMismatchException": {Type: "structure", Exception: true, Members: map[string]Member{"message": {Shape: "Text"}}},
		},
	}
}

func syntheticSpec() *Service {
	return &Service{
		Model: "synthetic", Title: "Synthetic", IAMPrefix: "synthetic", Issues: []int{1}, Bounds: []string{"test only"},
		Resources: []*Resource{{Kind: "widget", Shape: "Widget", ID: "alnum:8:w-", ARN: "arn:{partition}:synthetic:{region}:{account}:widget/{id}",
			ARNMember: "WidgetArn", NameMember: "Name", IDMember: "WidgetId", Limit: 3,
			Generate: map[string]any{"WidgetArn": "$arn", "WidgetId": "$id", "Size": 1}}},
		Ops: []Op{
			{Name: "CreateWidget", Kind: Create, Resource: "widget"},
			{Name: "GetWidget", Kind: Read, Resource: "widget", Lookup: "WidgetId"},
			{Name: "UpdateWidget", Kind: Update, Resource: "widget", Lookup: "WidgetId", Fields: []string{"Size"}},
			{Name: "DeleteWidget", Kind: Delete, Resource: "widget", Lookup: "WidgetId"},
			{Name: "ListWidgets", Kind: List, Resource: "widget", Names: "Names", ListValue: "WidgetId"},
		},
	}
}

func syntheticHarness(t *testing.T) *harness {
	h := newHarness(t, ecr)
	b, err := bindModel(syntheticSpec(), syntheticModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestEveryScalarTypeRoundTripsThroughStorage(t *testing.T) {
	h := syntheticHarness(t)
	data := base64.StdEncoding.EncodeToString([]byte("bytes"))
	created := h.json1("synthetic", "CreateWidget", map[string]any{
		"Name": "full", "Size": 4, "Weight": 2.5, "Count": "9007199254740993", "On": true, "Created": 1700000000.25,
		"Data": data, "Meta": map[string]any{"nested": []any{1, "x", map[string]any{"y": true}}},
		"Choice": map[string]any{"B": 2}, "Labels": map[string]any{"env": "dev"},
	})
	expect(t, created, 200, "")
	widget := created.body["Widget"].(map[string]any)
	if widget["Size"] != "4" && widget["Size"].(interface{ String() string }).String() != "4" {
		t.Fatalf("size %v", widget["Size"])
	}
	got := h.json1("synthetic", "GetWidget", map[string]any{"WidgetId": str(created.body["WidgetId"])}).body["Widget"].(map[string]any)
	for key, want := range map[string]string{"Weight": "2.5", "Count": "9007199254740993", "Created": "1700000000.25", "Data": data} {
		if got := strings.Trim(strings.TrimSpace(asText(got[key])), `"`); got != want {
			t.Fatalf("%s = %s, want %s", key, got, want)
		}
	}
	if got["On"] != true || got["Choice"].(map[string]any)["B"] == nil || got["Labels"].(map[string]any)["env"] != "dev" || got["Meta"] == nil {
		t.Fatalf("round trip %v", got)
	}
	if !strings.HasPrefix(str(created.body["WidgetId"]), "w-") || len(str(created.body["WidgetId"])) != 10 {
		t.Fatalf("generated id %v", created.body["WidgetId"])
	}
	if h.json1("synthetic", "CreateWidget", map[string]any{"Name": "defaulted"}).body["Widget"].(map[string]any)["Size"] == nil {
		t.Fatal("literal default not applied")
	}
}

func asText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case interface{ String() string }:
		return x.String()
	}
	return ""
}

func TestScalarConstraintsAreRejected(t *testing.T) {
	h := syntheticHarness(t)
	cases := map[string]map[string]any{
		"pattern":          {"Name": "UPPER"},
		"int min":          {"Name": "a", "Size": 0},
		"int max":          {"Name": "a", "Size": 11},
		"int type":         {"Name": "a", "Size": 1.5},
		"double min":       {"Name": "a", "Weight": -1},
		"double type":      {"Name": "a", "Weight": "heavy"},
		"double nan":       {"Name": "a", "Weight": "NaN"},
		"long type":        {"Name": "a", "Count": true},
		"bool type":        {"Name": "a", "On": "yes"},
		"timestamp":        {"Name": "a", "Created": "yesterday"},
		"timestamp range":  {"Name": "a", "Created": -5},
		"timestamp type":   {"Name": "a", "Created": true},
		"blob base64":      {"Name": "a", "Data": "%%%"},
		"blob type":        {"Name": "a", "Data": 1},
		"blob max":         {"Name": "a", "Data": base64.StdEncoding.EncodeToString([]byte("123456789"))},
		"union none":       {"Name": "a", "Choice": map[string]any{}},
		"union two":        {"Name": "a", "Choice": map[string]any{"A": "x", "B": 1}},
		"map type":         {"Name": "a", "Labels": []any{}},
		"map key":          {"Name": "a", "Labels": map[string]any{"BAD KEY": "x"}},
		"map size":         {"Name": "a", "Labels": map[string]any{"a": "1", "b": "2", "c": "3", "d": "4"}},
		"map value":        {"Name": "a", "Labels": map[string]any{"a": 1}},
		"structure type":   {"Name": "a", "Choice": "x"},
		"unsupported type": {"Name": "a", "Mystery": "x"},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			expect(t, h.json1("synthetic", "CreateWidget", input), 400, "ValidationException")
		})
	}
	deep := map[string]any{}
	cursor := deep
	for i := 0; i < maxDepth+2; i++ {
		next := map[string]any{}
		cursor["x"] = next
		cursor = next
	}
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "a", "Meta": deep}), 400, "ValidationException")
	wide := make([]any, maxNodes+1)
	for i := range wide {
		wide[i] = 1
	}
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "a", "Meta": map[string]any{"w": wide}}), 400, "ValidationException")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "a", "Created": "2026-11-06T12:00:00Z"}), 200, "")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "b", "Created": "1700000000"}), 200, "")
}

func TestQuotasNamesAndListValues(t *testing.T) {
	h := syntheticHarness(t)
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		created := h.json1("synthetic", "CreateWidget", map[string]any{"Name": name})
		expect(t, created, 200, "")
		ids = append(ids, str(created.body["WidgetId"]))
	}
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "d"}), 400, "LimitExceededException")
	listed := h.json1("synthetic", "ListWidgets", map[string]any{"Names": []any{ids[2], ids[0]}})
	expect(t, listed, 200, "")
	if got := listed.body["Ids"].([]any); len(got) != 2 || got[0] != ids[2] || got[1] != ids[0] {
		t.Fatalf("names order %v", listed.body)
	}
	expect(t, h.json1("synthetic", "ListWidgets", map[string]any{"Names": []any{"w-missing"}}), 400, "ResourceNotFoundException")
	expect(t, h.json1("synthetic", "UpdateWidget", map[string]any{"WidgetId": ids[0], "Size": 5}), 200, "")
	expect(t, h.json1("synthetic", "RebootWidget", map[string]any{}), 501, "UnknownOperationException")
	many := []any{}
	for i := 0; i < maxTagsPerResource+1; i++ {
		many = append(many, map[string]any{"Key": strings.Repeat("k", i+1), "Value": "v"})
	}
	expect(t, h.json1("synthetic", "DeleteWidget", map[string]any{"WidgetId": ids[1]}), 200, "")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "tagged", "Tags": many}), 400, "LimitExceededException")
}

func TestConcurrentChangeIsDetectedByGeneration(t *testing.T) {
	h := syntheticHarness(t)
	created := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "cas"})
	id := str(created.body["WidgetId"])
	b := h.binding("synthetic")
	q := &request{e: h.engine, b: b, op: b.ops["UpdateWidget"], official: b.model.Operations["UpdateWidget"], region: "us-east-1", principal: "p", input: map[string]any{"WidgetId": id, "Size": int64(2)}, ctx: t.Context(), w: httptest.NewRecorder()}
	r, _, err := q.lookup()
	if err != nil {
		t.Fatal(err)
	}
	expect(t, h.json1("synthetic", "UpdateWidget", map[string]any{"WidgetId": id, "Size": 3}), 200, "")
	err = q.commit(func(tx *sqlTx) error { return updateRow(tx, r) })
	if f, ok := err.(*fault); !ok || f.kind != "conflict" {
		t.Fatalf("stale generation must conflict, got %v", err)
	}
	err = q.commit(func(tx *sqlTx) error { return deleteRow(tx, r) })
	if f, ok := err.(*fault); !ok || f.kind != "conflict" {
		t.Fatalf("stale delete must conflict, got %v", err)
	}
	if got := h.json1("synthetic", "GetWidget", map[string]any{"WidgetId": id}).body["Widget"].(map[string]any)["Size"]; asText(got) != "3" {
		t.Fatalf("stale write overwrote state: %v", got)
	}
}

func TestErrorBodiesUseModeledMessageMembers(t *testing.T) {
	h := syntheticHarness(t)
	recorder := httptest.NewRecorder()
	writeError(recorder, h.binding("synthetic").model, "ConflictException", 409, "boom")
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"Message":"boom"`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(`"__type":"ConflictException"`)) {
		t.Fatalf("modeled Message member: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	writeError(recorder, nil, "UnknownOperationException", 404, "none")
	if recorder.Header().Get("X-Amzn-ErrorType") != "UnknownOperationException" || !bytes.Contains(recorder.Body.Bytes(), []byte(`"message":"none"`)) {
		t.Fatalf("unowned error: %s", recorder.Body.String())
	}
}
