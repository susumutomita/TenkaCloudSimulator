package aws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Exercise the provider's regional HTTP entry point, not a decoder double.
func TestSSMDocumentVersionsLifecycle(t *testing.T) {
	g, s := documentFixture(t)
	g.Documents = s
	const op = "ListDocumentVersions"
	const east = "us-east-1"
	const west = "us-west-2"
	const name = "release-guide"
	request := func(operation, region, principal string, body map[string]any, want int) map[string]any {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
		r.Header.Set("X-Simulator-Region", region)
		if principal != "" {
			r.Header.Set("X-Simulator-Principal", principal)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d %s, want %d", region, operation, w.Code, w.Body.String(), want)
		}
		reply := map[string]any{}
		if w.Body.Len() > 0 {
			if err = json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
		}
		return reply
	}
	save := func(region, name string, i int) {
		t.Helper()
		method := "CreateDocument"
		body := map[string]any{"Name": name, "Content": strings.Replace(documentTestContent, "first", strconv.Itoa(i), 1)}
		if i > 1 {
			method = "UpdateDocument"
			body["DocumentVersion"] = "$LATEST"
		}
		request(method, region, "", body, 200)
	}
	for _, n := range []string{name, "other-guide"} {
		save(east, n, 1)
		save(east, n, 2)
	}
	save(east, name, 3)
	save(west, name, 1)
	ownerARN := "arn:aws:ssm:" + east + ":000000000000:document/" + name
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:" + op}, Resource: core.StringList{ownerARN}}}}
	for _, who := range []string{"reader", "second-reader"} {
		if err := g.Identities.Set(who, policy); err != nil {
			t.Fatal(err)
		}
	}
	first := request(op, east, "reader", map[string]any{"Name": name, "MaxResults": 2}, 200)
	check := func(page map[string]any, numbers ...int) {
		t.Helper()
		items, ok := page["DocumentVersions"].([]any)
		if !ok || len(items) != len(numbers) {
			t.Fatalf("unexpected versions: %#v", page)
		}
		for i, n := range numbers {
			row := items[i].(map[string]any)
			d := request("GetDocument", east, "", map[string]any{"Name": name, "DocumentVersion": strconv.Itoa(n)}, 200)
			if len(row) != 6 || row["Name"] != name || row["DocumentVersion"] != strconv.Itoa(n) || row["IsDefaultVersion"] != (n == 1) || row["DocumentFormat"] != "JSON" || row["Status"] != "Active" || row["CreatedDate"] != d["CreatedDate"] {
				t.Fatalf("version %d: %#v, get: %#v", n, row, d)
			}
		}
	}
	check(first, 1, 2)
	token, ok := first["NextToken"].(string)
	if !ok || token == "" {
		t.Fatal("missing cursor")
	}
	params := map[string]any{"Name": name, "MaxResults": 2, "NextToken": token}
	save(east, "other-guide", 3) // An independent document must not invalidate this page.
	last := request(op, east, "reader", params, 200)
	check(last, 3)
	if _, ok := last["NextToken"]; ok {
		t.Fatal("unexpected trailing cursor")
	}
	check(request(op, east, "reader", map[string]any{"Name": name}, 200), 1, 2, 3)
	// Existing document permissions are rechecked on every request; a page token
	// grants no authority and another reader's valid page belongs to its own query.
	denied := request(op, east, "reader", map[string]any{"Name": "other-guide"}, 403)
	if denied["__type"] != "AccessDeniedException" {
		t.Fatal(denied)
	}
	if got := request(op, west, "reader", params, 403)["__type"]; got != "AccessDeniedException" {
		t.Fatal(got)
	}
	if got := request(op, east, "second-reader", params, 400)["__type"]; got != "InvalidNextToken" {
		t.Fatal(got)
	}
	if got := request(op, east, "reader", map[string]any{"Name": name, "MaxResults": 1, "NextToken": token}, 400)["__type"]; got != "InvalidNextToken" {
		t.Fatal(got)
	}
	for _, max := range []int{0, 51} {
		if got := request(op, east, "", map[string]any{"Name": name, "MaxResults": max}, 400)["__type"]; got != "ValidationException" {
			t.Fatal(got)
		}
	}
	revoked := core.Policy{Version: policy.Version, Statement: append(core.Statements{}, policy.Statement...)}
	revoked.Statement = append(revoked.Statement, core.Statement{Effect: "Deny", Action: core.StringList{"ssm:" + op}, Resource: core.StringList{ownerARN}})
	if err := g.Identities.Set("reader", revoked); err != nil {
		t.Fatal(err)
	}
	if got := request(op, east, "reader", params, 403)["__type"]; got != "AccessDeniedException" {
		t.Fatal(got)
	}
	if err := g.Identities.Set("reader", policy); err != nil {
		t.Fatal(err)
	}
	adminFirst := request(op, east, "", map[string]any{"Name": name, "MaxResults": 2}, 200)
	if got := request(op, east, "", map[string]any{"Name": "other-guide", "MaxResults": 2, "NextToken": adminFirst["NextToken"]}, 400)["__type"]; got != "InvalidNextToken" {
		t.Fatal(got)
	}
	save(east, name, 4)
	if got := request(op, east, "reader", params, 400)["__type"]; got != "InvalidNextToken" {
		t.Fatal(got)
	}
	request("DeleteDocument", east, "", map[string]any{"Name": name}, 200)
	if got := request(op, east, "reader", map[string]any{"Name": name}, 400)["__type"]; got != "InvalidDocument" {
		t.Fatal(got)
	}
	westPage := request(op, west, "", map[string]any{"Name": name}, 200)
	if items := westPage["DocumentVersions"].([]any); len(items) != 1 || items[0].(map[string]any)["DocumentVersion"] != "1" {
		t.Fatal(westPage)
	}
}

// Reopen the actual SQLite file, rather than reusing one in-memory store.
// Crossing from 9 to 10 catches accidental lexical version ordering.
func TestSSMDocumentVersionsReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	populate := func() error {
		defer db.Close()
		s, err := NewSSMDocumentStore(db.Database())
		if err != nil {
			return err
		}
		for i := 1; i <= 12; i++ {
			selector := "$LATEST"
			if i == 1 {
				selector = ""
			}
			if _, err = s.Save(t.Context(), "us-east-1", "release-guide", strings.Replace(documentTestContent, "first", strconv.Itoa(i), 1), selector); err != nil {
				return err
			}
		}
		return nil
	}
	if err = populate(); err != nil {
		t.Fatal(err)
	}
	db, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewSSMDocumentStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: db, Identities: core.NewIdentities(db), Documents: s}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"release-guide"}`))
	r.Header.Set("X-Amz-Target", "AmazonSSM.ListDocumentVersions")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	var result struct {
		DocumentVersions []struct {
			DocumentVersion  string
			IsDefaultVersion bool
		}
		NextToken string
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.DocumentVersions) != 12 || result.NextToken != "" {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, v := range result.DocumentVersions {
		if v.DocumentVersion != strconv.Itoa(i+1) || v.IsDefaultVersion != (i == 0) {
			t.Fatal(i, v)
		}
	}
}
