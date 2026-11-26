package queue

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type referenceParameter struct {
	Name     string
	In       string
	Required bool
	Schema   struct {
		Default string
		Enum    []any
	}
}

type referenceOperation struct {
	OperationID string
	Parameters  []referenceParameter
	RequestBody *struct {
		Required bool
		Content  map[string]struct{ Example string }
	}
	Responses map[string]struct {
		Content map[string]any
		Headers map[string]any
	}
}

type queueReference struct {
	OpenAPI string
	Servers []struct{ URL string }
	Paths   map[string]map[string]referenceOperation
}

func loadQueueReference(t *testing.T, account string) queueReference {
	t.Helper()
	raw, err := json.Marshal(OpenAPI(account))
	if err != nil {
		t.Fatal(err)
	}
	var doc queueReference
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	const prefix = "/_simulator/providers/azure-queue"
	if doc.OpenAPI != "3.1.0" || len(doc.Servers) != 1 || doc.Servers[0].URL != prefix {
		t.Fatal("reference must use the relative, local Queue endpoint only")
	}
	return doc
}

// Counts, methods, defaults and modes are an independent, wire-level oracle.
// Azure Get and Peek share one OpenAPI method; Peek's query switches the mode.
func TestQueueReferenceContainsOnlyBoundedOperations(t *testing.T) {
	doc := loadQueueReference(t, "fixedaccount")
	const q = "/{account}/{queue}"
	expected := map[string]string{
		"PUT " + q:                              "CreateQueue",
		"DELETE " + q:                           "DeleteQueue",
		"POST " + q + "/messages":               "PutMessage",
		"GET " + q + "/messages":                "GetOrPeekMessages",
		"DELETE " + q + "/messages":             "ClearMessages",
		"PUT " + q + "/messages/{messageId}":    "UpdateMessage",
		"DELETE " + q + "/messages/{messageId}": "DeleteMessage",
	}
	count := 0
	for path, item := range doc.Paths {
		for method, op := range item {
			count++
			key := strings.ToUpper(method) + " " + path
			if want := expected[key]; want == "" || want != op.OperationID {
				t.Fatalf("unexpected method/path/id: %s %s", key, op.OperationID)
			}
			p := map[string]referenceParameter{}
			for _, parameter := range op.Parameters {
				key := parameter.In + ":" + parameter.Name
				if _, duplicate := p[key]; duplicate {
					t.Fatalf("duplicate parameter on %s: %s", op.OperationID, key)
				}
				p[key] = parameter
			}
			account, version := p["path:account"], p["header:x-ms-version"]
			if !account.Required || account.Schema.Default != "fixedaccount" ||
				len(account.Schema.Enum) != 1 || account.Schema.Enum[0] != "fixedaccount" ||
				!version.Required || version.Schema.Default != "2023-11-03" ||
				len(version.Schema.Enum) != 1 || version.Schema.Enum[0] != "2023-11-03" ||
				!p["path:queue"].Required || p["header:X-Simulator-Token"].Name == "" ||
				p["header:X-Simulator-Token"].Required || p["header:X-Simulator-Token"].Schema.Default != "" {
				t.Fatalf("scope, version or local-session contract drifted on %s", op.OperationID)
			}
			if strings.Contains(path, "{messageId}") && (!p["path:messageId"].Required || !p["query:popreceipt"].Required) {
				t.Fatalf("named-message operations must require ID and receipt: %s", op.OperationID)
			}
			switch op.OperationID {
			case "PutMessage":
				if op.RequestBody == nil || !op.RequestBody.Required || op.RequestBody.Content["application/xml"].Example == "" {
					t.Fatal("send needs a runnable XML body example")
				}
			case "UpdateMessage":
				if op.RequestBody == nil || op.RequestBody.Required || !p["query:visibilitytimeout"].Required {
					t.Fatal("update needs visibility and permits a missing body")
				}
			case "GetOrPeekMessages":
				if p["query:peekonly"].Name == "" || p["query:peekonly"].Required || p["query:visibilitytimeout"].Schema.Default != "" {
					t.Fatal("reference must allow Peek without implicit visibilitytimeout")
				}
			}
		}
	}
	if count != len(expected) {
		t.Fatalf("got %d method/path entries, want %d", count, len(expected))
	}
}

// Executes the reference's relative route, documented header value, and real
// XML example against the actual Queue leaf with SQLite and a fixture owner.
// This does not exercise the Scalar UI, token gateway, or an official SDK.
func TestQueueReferenceExamplesMatchNativeLifecycle(t *testing.T) {
	h, _, _ := fixture(t)
	doc := loadQueueReference(t, "localaccount")
	base := doc.Servers[0].URL
	var id, receipt string
	const q = "/{account}/{queue}"
	cases := []struct {
		method, path, query string
		status              int
		withExample         bool
	}{
		{"PUT", q, "", 201, false},
		{"PUT", q, "", 204, false},
		{"POST", q + "/messages", "?messagettl=120", 201, true},
		{"GET", q + "/messages", "?peekonly=true", 200, false},
		{"GET", q + "/messages", "?numofmessages=1", 200, false},
		{"PUT", q + "/messages/{messageId}", "?visibilitytimeout=0", 204, false},
		{"DELETE", q + "/messages/{messageId}", "", 204, false},
		{"POST", q + "/messages", "", 201, true},
		{"DELETE", q + "/messages", "", 204, false},
		{"DELETE", q, "", 204, false},
	}
	for i, step := range cases {
		op := doc.Paths[step.path][strings.ToLower(step.method)]
		result, documented := op.Responses[strconv.Itoa(step.status)]
		if !documented {
			t.Fatalf("step %d (%s) does not document %d", i, op.OperationID, step.status)
		}
		path := strings.NewReplacer("{account}", "localaccount", "{queue}", "tasks", "{messageId}", id).Replace(step.path)
		query := step.query
		if strings.Contains(step.path, "{messageId}") {
			prefix := "?"
			if query != "" {
				prefix = "&"
			}
			query += prefix + "popreceipt=" + url.QueryEscape(receipt)
		}
		var sample string
		if step.withExample {
			sample = op.RequestBody.Content["application/xml"].Example
		}
		r := httptest.NewRequest(step.method, base+path+query, strings.NewReader(sample))
		for _, p := range op.Parameters {
			if p.In == "header" && p.Schema.Default != "" {
				r.Header.Set(p.Name, p.Schema.Default)
			}
		}
		if sample != "" {
			r.Header.Set("Content-Type", "application/xml")
		}
		w := httptest.NewRecorder()
		http.StripPrefix(base, h).ServeHTTP(w, r)
		if w.Code != step.status || w.Header().Get("x-ms-version") != "2023-11-03" {
			t.Fatalf("step %d (%s) status/version: %d %s", i, op.OperationID, w.Code, w.Header().Get("x-ms-version"))
		}
		if _, isXML := result.Content["application/xml"]; isXML != (w.Header().Get("Content-Type") == "application/xml") {
			t.Fatalf("step %d (%s) XML content type drifted", i, op.OperationID)
		}
		switch i {
		case 2:
			sent := messages(t, w)
			if len(sent) != 1 || sent[0].ID == "" || sent[0].Receipt == "" || sent[0].Text != nil {
				t.Fatal("send response shape drifted")
			}
			id = sent[0].ID
		case 3, 4:
			got := messages(t, w)
			if len(got) != 1 || got[0].ID != id || got[0].Text == nil ||
				*got[0].Text != "local example" || (i == 3 && (got[0].Receipt != "" || got[0].Count != 0)) ||
				(i == 4 && (got[0].Receipt == "" || got[0].Count != 1)) {
				t.Fatalf("get/peek response mode drifted at step %d", i)
			}
			receipt = got[0].Receipt
		case 5:
			for _, header := range []string{"x-ms-popreceipt", "x-ms-time-next-visible"} {
				if _, ok := result.Headers[header]; !ok || w.Header().Get(header) == "" {
					t.Fatalf("update missing runtime or reference header: %s", header)
				}
			}
			if next := w.Header().Get("x-ms-popreceipt"); next == receipt {
				t.Fatal("update did not return a new receipt")
			} else {
				receipt = next
			}
		}
	}
}
