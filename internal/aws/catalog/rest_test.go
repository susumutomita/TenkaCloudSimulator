package catalog

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
)

// restModel covers REST-JSON binding traits: labels, greedy labels, query
// lists, headers, status codes, payload members and literal query routes.
func restModel() *Model {
	text := &Shape{Type: "string"}
	return &Model{
		Name:     "restsynthetic",
		Source:   Source{Package: "botocore", Version: "test", SHA256: "test", License: "Apache-2.0"},
		Metadata: Metadata{Protocol: "rest-json", SigningName: "restsynthetic"},
		Routes: map[string]Route{
			"CreateBox": {HTTP: HTTP{Method: "PUT", RequestURI: "/boxes/{Name}", ResponseCode: 201}},
			"GetBox":    {HTTP: HTTP{Method: "GET", RequestURI: "/boxes/{Name}"}},
			"ListBoxes": {HTTP: HTTP{Method: "GET", RequestURI: "/boxes"}},
			"PutNote":   {HTTP: HTTP{Method: "POST", RequestURI: "/boxes/{Name}/note?kind=text"}},
			"GetFile":   {HTTP: HTTP{Method: "GET", RequestURI: "/files/{Key+}"}},
			"Shake":     {HTTP: HTTP{Method: "POST", RequestURI: "/boxes/{Name}/shake"}},
		},
		Operations: map[string]*Operation{
			"CreateBox": {Name: "CreateBox", HTTP: HTTP{Method: "PUT", RequestURI: "/boxes/{Name}", ResponseCode: 201}, Input: "CreateBoxInput", Output: "BoxOutput"},
			"GetBox":    {Name: "GetBox", HTTP: HTTP{Method: "GET", RequestURI: "/boxes/{Name}"}, Input: "GetBoxInput", Output: "BoxOutput"},
			"ListBoxes": {Name: "ListBoxes", HTTP: HTTP{Method: "GET", RequestURI: "/boxes"}, Input: "ListBoxesInput", Output: "ListBoxesOutput"},
			"PutNote":   {Name: "PutNote", HTTP: HTTP{Method: "POST", RequestURI: "/boxes/{Name}/note?kind=text"}, Input: "PutNoteInput", Output: "BoxOutput"},
			"GetFile":   {Name: "GetFile", HTTP: HTTP{Method: "GET", RequestURI: "/files/{Key+}"}, Input: "GetFileInput", Output: "BoxOutput"},
		},
		Shapes: map[string]*Shape{
			"Text":  text,
			"Count": {Type: "integer"},
			"Flag":  {Type: "boolean"},
			"Time":  {Type: "timestamp"},
			"Bytes": {Type: "blob"},
			"Texts": {Type: "list", Member: &Member{Shape: "Text"}},
			"Map":   {Type: "map", Key: &Member{Shape: "Text"}, Value: &Member{Shape: "Text"}},
			"Box": {Type: "structure", Members: map[string]Member{"Name": {Shape: "Text"}, "Color": {Shape: "Text"}, "Sizes": {Shape: "Texts"},
				"Fragile": {Shape: "Flag"}, "Count": {Shape: "Count"}, "Since": {Shape: "Time"}, "Note": {Shape: "Text"}, "Owner": {Shape: "Text"}, "Arn": {Shape: "Text"}}},
			"Spec": {Type: "structure", Members: map[string]Member{"Color": {Shape: "Text"}}},
			"CreateBoxInput": {Type: "structure", Required: []string{"Name"}, Members: map[string]Member{
				"Name":    {Shape: "Text", Location: "uri", LocationName: "Name"},
				"Sizes":   {Shape: "Texts", Location: "querystring", LocationName: "size"},
				"Fragile": {Shape: "Flag", Location: "querystring", LocationName: "fragile"},
				"Count":   {Shape: "Count", Location: "header", LocationName: "X-Count"},
				"Since":   {Shape: "Time", Location: "header", LocationName: "X-Since"},
				"Owner":   {Shape: "Text", Location: "header", LocationName: "X-Owner", JSONValue: true},
				"Labels":  {Shape: "Texts", Location: "header", LocationName: "X-Labels"},
				"Meta":    {Shape: "Map", Location: "headers", LocationName: "x-meta-"},
				"Spec":    {Shape: "Spec"}}, Payload: "Spec"},
			"GetBoxInput":    {Type: "structure", Required: []string{"Name"}, Members: map[string]Member{"Name": {Shape: "Text", Location: "uri", LocationName: "Name"}}},
			"ListBoxesInput": {Type: "structure", Members: map[string]Member{"Filter": {Shape: "Map", Location: "querystring", LocationName: "filter"}, "Color": {Shape: "Text"}}},
			"PutNoteInput": {Type: "structure", Required: []string{"Name"}, Members: map[string]Member{
				"Name": {Shape: "Text", Location: "uri", LocationName: "Name"}, "Note": {Shape: "Text"}}, Payload: "Note"},
			"GetFileInput": {Type: "structure", Required: []string{"Key"}, Members: map[string]Member{"Key": {Shape: "Text", Location: "uri", LocationName: "Key"}}},
			"BoxOutput": {Type: "structure", Members: map[string]Member{"Box": {Shape: "Box"}, "Arn": {Shape: "Text", Location: "header", LocationName: "X-Box-Arn"},
				"Count": {Shape: "Count", Location: "statusCode"}}},
			"ListBoxesOutput": {Type: "structure", Members: map[string]Member{"Boxes": {Shape: "BoxList"}}},
			"BoxList":         {Type: "list", Member: &Member{Shape: "Box"}},
		},
	}
}

func restSpec() *Service {
	return &Service{
		Model: "restsynthetic", Title: "REST Synthetic", IAMPrefix: "restsynthetic", Issues: []int{1}, Bounds: []string{"test only"}, Global: true,
		Resources: []*Resource{{Kind: "box", Shape: "Box", ID: "name", ARN: "arn:{partition}:restsynthetic:{region}:{account}:box/{name}", ARNMember: "Arn", NameMember: "Name", Unique: true,
			Generate: map[string]any{"Arn": "$arn", "Owner": "$account", "Note": "$region"}}},
		Ops: []Op{
			{Name: "CreateBox", Kind: Create, Resource: "box", From: "Spec", Accept: []string{"Labels"}},
			{Name: "GetBox", Kind: Read, Resource: "box", Lookup: "Name"},
			{Name: "ListBoxes", Kind: List, Resource: "box", Filters: map[string]string{"Color": "Color"}},
			{Name: "PutNote", Kind: Update, Resource: "box", Lookup: "Name", Fields: []string{"Note"}},
			{Name: "GetFile", Kind: Read, Resource: "box", Lookup: "Key"},
		},
	}
}

func restHarness(t *testing.T) *harness {
	h := newHarness(t, ecr)
	b, err := bindModel(restSpec(), restModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRESTBindingsDecodeEveryHTTPLocation(t *testing.T) {
	h := restHarness(t)
	req := httptest.NewRequest("PUT", "/boxes/red?size=s&size=m&fragile=true", strings.NewReader(`{"Color":"red"}`))
	sign(req, "restsynthetic", "ap-northeast-1")
	req.Header.Set("X-Count", "3")
	req.Header.Set("X-Since", "Fri, 06 Nov 2026 12:00:00 GMT")
	req.Header.Set("X-Owner", base64.StdEncoding.EncodeToString([]byte(`"team"`)))
	req.Header.Set("X-Labels", "a, b")
	created := h.do(req)
	expect(t, created, 201, "")
	box := created.body["Box"].(map[string]any)
	if box["Color"] != "red" || box["Owner"] != "000000000000" || box["Note"] != "" || box["Arn"] != "arn:aws:restsynthetic::000000000000:box/red" {
		t.Fatalf("global create %v", box)
	}
	tokyo := httptest.NewRequest("GET", "/boxes/red", nil)
	sign(tokyo, "restsynthetic", "us-east-1")
	if h.do(tokyo).status != 200 {
		t.Fatal("a global service shares one scope across regions")
	}
	bad := map[string]func() reply{
		"query repeated scalar": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue?fragile=true&fragile=false", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"unknown query": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue?zzz=1", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"jsonvalue base64": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			r.Header.Set("X-Owner", "%%%")
			return h.do(r)
		},
		"prefixed headers": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			r.Header.Set("X-Meta-Color", "x")
			return h.do(r)
		},
		"payload json": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue", strings.NewReader(`[`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"header integer": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			r.Header.Set("X-Count", "many")
			return h.do(r)
		},
		"query boolean": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/blue?fragile=maybe", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"query map": func() reply {
			r := httptest.NewRequest("GET", "/boxes?filter=x", nil)
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"body member in wrong location": func() reply {
			r := httptest.NewRequest("GET", "/boxes", strings.NewReader(`{"Filter":{}}`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"unknown body member": func() reply {
			r := httptest.NewRequest("GET", "/boxes", strings.NewReader(`{"Zzz":1}`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
		"duplicate": func() reply {
			r := httptest.NewRequest("PUT", "/boxes/red", strings.NewReader(`{}`))
			sign(r, "restsynthetic", "us-east-1")
			return h.do(r)
		},
	}
	for name, send := range bad {
		t.Run(name, func(t *testing.T) {
			if got := send(); got.status < 400 {
				t.Fatalf("%s accepted: %d %s", name, got.status, got.raw)
			}
		})
	}
	note := httptest.NewRequest("POST", "/boxes/red/note?kind=text", strings.NewReader("hello"))
	sign(note, "restsynthetic", "us-east-1")
	updated := h.do(note)
	expect(t, updated, 200, "")
	if updated.body["Box"].(map[string]any)["Note"] != "hello" {
		t.Fatalf("string payload %v", updated.body)
	}
	missingLiteral := httptest.NewRequest("POST", "/boxes/red/note", strings.NewReader("x"))
	sign(missingLiteral, "restsynthetic", "us-east-1")
	expect(t, h.do(missingLiteral), 404, "UnknownOperationException")
	file := httptest.NewRequest("GET", "/files/red", nil)
	sign(file, "restsynthetic", "us-east-1")
	expect(t, h.do(file), 200, "")
	nested := httptest.NewRequest("GET", "/files/a/b%2Fc", nil)
	sign(nested, "restsynthetic", "us-east-1")
	expect(t, h.do(nested), 404, "ResourceNotFoundException")
	shake := httptest.NewRequest("POST", "/boxes/red/shake", nil)
	sign(shake, "restsynthetic", "us-east-1")
	expect(t, h.do(shake), 501, "UnknownOperationException")
	empty := httptest.NewRequest("GET", "/boxes//", nil)
	sign(empty, "restsynthetic", "us-east-1")
	expect(t, h.do(empty), 404, "UnknownOperationException")
	filtered := httptest.NewRequest("GET", "/boxes", strings.NewReader(`{"Color":"blue"}`))
	sign(filtered, "restsynthetic", "us-east-1")
	if boxes := h.do(filtered).body["Boxes"].([]any); len(boxes) != 0 {
		t.Fatalf("filter %v", boxes)
	}
}

func TestRESTOutputBindsHeadersStatusAndPayload(t *testing.T) {
	model := restModel()
	model.Shapes["BoxOutput"].Payload = "Box"
	recorder := httptest.NewRecorder()
	writeOutput(recorder, model, model.Operations["GetBox"], map[string]any{"Box": map[string]any{"Name": "x"}, "Arn": "arn:x", "Count": int64(202)})
	if recorder.Code != 202 || recorder.Header().Get("X-Box-Arn") != "arn:x" || strings.TrimSpace(recorder.Body.String()) != `{"Name":"x"}` {
		t.Fatalf("rest output %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	model.Shapes["BoxOutput"].Members["Raw"] = Member{Shape: "Text"}
	model.Shapes["BoxOutput"].Payload = "Raw"
	recorder = httptest.NewRecorder()
	writeOutput(recorder, model, model.Operations["GetBox"], map[string]any{"Raw": "plain"})
	if recorder.Body.String() != "plain" {
		t.Fatalf("string payload %q", recorder.Body.String())
	}
}
