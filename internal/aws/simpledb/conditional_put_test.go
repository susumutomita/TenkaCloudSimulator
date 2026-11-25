package simpledb

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// Amazon SimpleDB's API reference documents exactly one Expected condition on
// PutAttributes. Conditional errors must not apply *any* attribute in the write.
// https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_PutAttributes.html
// https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/APIError.html
func TestConditionalPutWire(t *testing.T) {
	store, _ := fixture(t)
	scope := Scope{"local", "us-east-1"}
	allow := true
	h := NewHTTPHandler(store, scope, func(_ context.Context, action, resource string) bool {
		if resource != "arn:aws:sdb:us-east-1:local:domain/versions" {
			t.Fatalf("unexpected resource %q", resource)
		}
		switch action {
		case "sdb:CreateDomain", "sdb:PutAttributes", "sdb:GetAttributes":
		default:
			t.Fatalf("unexpected action %q", action)
		}
		return allow
	})
	if w := callHTTP(h, params("CreateDomain", map[string]string{"DomainName": "versions"})); w.Code != 200 {
		t.Fatal(w.Body)
	}
	write := func(item, version string, cond map[string]string) url.Values {
		v := params("PutAttributes", map[string]string{"DomainName": "versions", "ItemName": item, "Attribute.1.Name": "version", "Attribute.1.Value": version, "Attribute.1.Replace": "true", "Attribute.2.Name": "value", "Attribute.2.Value": version, "Attribute.2.Replace": "true"})
		for k, s := range cond {
			v.Set(k, s)
		}
		return v
	}
	check := func(t *testing.T, v url.Values, status int, code string) {
		t.Helper()
		w := callHTTP(h, v)
		var body struct {
			Code string `xml:"Errors>Error>Code"`
		}
		if e := xml.Unmarshal(w.Body.Bytes(), &body); e != nil || w.Code != status || body.Code != code {
			t.Fatalf("%s: HTTP %d %v %s; want %d %q", v.Encode(), w.Code, e, w.Body, status, code)
		}
	}
	read := func(t *testing.T, item string) map[string][]string {
		t.Helper()
		w := callHTTP(h, params("GetAttributes", map[string]string{"DomainName": "versions", "ItemName": item, "ConsistentRead": "true"}))
		var body struct {
			Attrs []queryAttribute `xml:"GetAttributesResult>Attribute"`
		}
		if e := xml.Unmarshal(w.Body.Bytes(), &body); e != nil || w.Code != 200 {
			t.Fatalf("read: %v %d %s", e, w.Code, w.Body)
		}
		out := map[string][]string{}
		for _, a := range body.Attrs {
			out[a.Name] = append(out[a.Name], a.Value)
		}
		return out
	}
	check(t, write("empty", "0", map[string]string{"Expected.Name": "version", "Expected.Value": "n/a"}), 404, "AttributeDoesNotExist")
	if got := read(t, "empty"); len(got) != 0 {
		t.Fatalf("failed write created attributes: %v", got)
	}
	absent := map[string]string{"Expected.Name": "version", "Expected.Exists": "false"}
	check(t, write("row", "", absent), 200, "")
	check(t, write("row", "1", map[string]string{"Expected.Name": "version", "Expected.Value": ""}), 200, "")
	check(t, write("row", "2", map[string]string{"Expected.Name": "version", "Expected.Value": "1", "Expected.Exists": "true"}), 200, "")
	want := map[string][]string{"version": {"2"}, "value": {"2"}}
	for _, tc := range []struct {
		label  string
		cond   map[string]string
		status int
		code   string
	}{
		{"old version", map[string]string{"Expected.Name": "version", "Expected.Value": "1"}, 409, "ConditionalCheckFailed"},
		{"not absent", absent, 409, "ConditionalCheckFailed"},
		{"attribute not found", map[string]string{"Expected.Name": "other", "Expected.Value": "missing"}, 404, "AttributeDoesNotExist"},
		{"value without name", map[string]string{"Expected.Value": "2"}, 400, "MissingParameter"},
		{"exists without name", map[string]string{"Expected.Exists": "false"}, 400, "MissingParameter"},
		{"empty name", map[string]string{"Expected.Name": "", "Expected.Value": "2"}, 400, "InvalidParameterValue"},
		{"missing value", map[string]string{"Expected.Name": "version"}, 400, "IncompleteExpectedExpression"},
		{"no value for true", map[string]string{"Expected.Name": "version", "Expected.Exists": "true"}, 400, "IncompleteExpectedExpression"},
		{"value for false", map[string]string{"Expected.Name": "version", "Expected.Exists": "false", "Expected.Value": ""}, 400, "ExistsAndExpectedValue"},
		{"invalid bool", map[string]string{"Expected.Name": "version", "Expected.Exists": "1"}, 400, "InvalidParameterValue"},
		{"overlength name", map[string]string{"Expected.Name": strings.Repeat("n", 1025), "Expected.Value": "2"}, 400, "InvalidParameterValue"},
		{"overlength value", map[string]string{"Expected.Name": "version", "Expected.Value": strings.Repeat("v", 1025)}, 400, "InvalidParameterValue"},
		{"legacy indexed condition", map[string]string{"Expected.1.Name": "version", "Expected.1.Value": "2"}, 400, "InvalidParameterValue"},
		{"unknown condition", map[string]string{"Expected.Name": "version", "Expected.Unknown": "x"}, 400, "InvalidParameterValue"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			check(t, write("row", "unexpected", tc.cond), tc.status, tc.code)
			if got := read(t, "row"); !reflect.DeepEqual(got, want) {
				t.Fatalf("failure altered data: %v", got)
			}
		})
	}
	for _, tc := range []struct{ key, extra, code string }{
		{"Expected.Name", "version", "MultipleExpectedNames"},
		{"Expected.Value", "2", "MultipleExpectedValues"},
		{"Expected.Exists", "true", "MultipleExistsConditions"},
	} {
		v := write("row", "unexpected", map[string]string{"Expected.Name": "version", "Expected.Value": "2", "Expected.Exists": "true"})
		v.Add(tc.key, tc.extra)
		check(t, v, 400, tc.code)
		if got := read(t, "row"); !reflect.DeepEqual(got, want) {
			t.Fatalf("duplicate %s altered data: %v", tc.key, got)
		}
	}
	// The exact action/resource check runs before accessing the stored condition.
	allow = false
	check(t, write("row", "unauthorized", map[string]string{"Expected.Name": "missing", "Expected.Value": "x"}), 403, "AccessDenied")
	allow = true
	if got := read(t, "row"); !reflect.DeepEqual(got, want) {
		t.Fatalf("denied write changed state: %v", got)
	}
	extra := params("PutAttributes", map[string]string{"DomainName": "versions", "ItemName": "row", "Attribute.1.Name": "version", "Attribute.1.Value": "another"})
	check(t, extra, 200, "")
	snapshot := read(t, "row")
	check(t, write("row", "unexpected", map[string]string{"Expected.Name": "version", "Expected.Value": "2"}), 409, "MultiValuedAttribute")
	if got := read(t, "row"); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("multivalue condition changed state: %v", got)
	}
}

func TestConditionalPutWriters(t *testing.T) {
	store, _ := fixture(t)
	h := NewHTTPHandler(store, Scope{"local", "us-east-1"}, func(context.Context, string, string) bool { return true })
	if w := callHTTP(h, params("CreateDomain", map[string]string{"DomainName": "versions"})); w.Code != 200 {
		t.Fatal(w.Body)
	}
	init := params("PutAttributes", map[string]string{"DomainName": "versions", "ItemName": "row", "Attribute.1.Name": "version", "Attribute.1.Value": "0"})
	if w := callHTTP(h, init); w.Code != 200 {
		t.Fatal(w.Body)
	}
	const n = 8
	type result struct {
		candidate string
		status    int
		code      string
	}
	start := make(chan struct{})
	results := make(chan result, n)
	for i := 1; i <= n; i++ {
		version := fmt.Sprintf("%d", i)
		go func() {
			<-start
			v := params("PutAttributes", map[string]string{"DomainName": "versions", "ItemName": "row", "Attribute.1.Name": "version", "Attribute.1.Value": version, "Attribute.1.Replace": "true", "Expected.Name": "version", "Expected.Value": "0"})
			w := callHTTP(h, v)
			var b struct {
				Code string `xml:"Errors>Error>Code"`
			}
			_ = xml.Unmarshal(w.Body.Bytes(), &b)
			results <- result{version, w.Code, b.Code}
		}()
	}
	close(start)
	var winner string
	var successes int
	for i := 0; i < n; i++ {
		r := <-results
		if r.status == http.StatusOK {
			winner = r.candidate
			successes++
		} else if r.status != 409 || r.code != "ConditionalCheckFailed" {
			t.Fatalf("unexpected writer result %+v", r)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successes, want 1", successes)
	}
	values, err := store.Get(context.Background(), "local", "us-east-1", "versions", "row", nil)
	if err != nil || !reflect.DeepEqual(values["version"], []string{winner}) {
		t.Fatalf("winner not persisted: want %s, got %v, %v", winner, values, err)
	}
}
