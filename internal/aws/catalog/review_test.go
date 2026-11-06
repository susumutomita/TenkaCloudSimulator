package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Credentials nested in auto-branch settings are refused like top-level ones.
func TestAmplifyRefusesNestedBasicAuth(t *testing.T) {
	h := newHarness(t, amplify)
	for _, nested := range []map[string]any{{"basicAuthCredentials": "dXNlcjpwYXNz"}, {"enableBasicAuth": true}} {
		expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "autoBranchCreationConfig": nested}), 400, "BadRequestException")
	}
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "autoBranchCreationConfig": map[string]any{"stage": "BETA"}}), 200, "")
}

// A $name default fills an omitted member but never replaces a supplied one.
func TestNameDefaultsNeverOverrideSuppliedMembers(t *testing.T) {
	h := newHarness(t, amplify)
	appID := str(h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)["appId"])
	named := h.rest("amplify", "POST", "/apps/"+appID+"/branches", map[string]any{"branchName": "main", "displayName": "Production"})
	expect(t, named, 200, "")
	if got := named.body["branch"].(map[string]any)["displayName"]; got != "Production" {
		t.Fatalf("supplied display name replaced: %v", got)
	}
	if got := h.rest("amplify", "GET", "/apps/"+appID+"/branches/main", nil).body["branch"].(map[string]any)["displayName"]; got != "Production" {
		t.Fatalf("stored display name %v", got)
	}
}

// Tagging requests stay in their regional scope like every other lookup.
func TestTaggingIsRegionScoped(t *testing.T) {
	h := newHarness(t, amplify)
	arn := str(h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)["appArn"])
	path := "/tags/" + url.PathEscape(arn)
	for _, call := range []struct {
		method string
		body   any
	}{{"GET", nil}, {"POST", map[string]any{"tags": map[string]any{"a": "b"}}}} {
		var body []byte
		if call.body != nil {
			body, _ = json.Marshal(call.body)
		}
		req := httptest.NewRequest(call.method, path, bytes.NewReader(body))
		sign(req, "amplify", "ap-northeast-1")
		expect(t, h.do(req), 404, "ResourceNotFoundException")
	}
	expect(t, h.rest("amplify", "GET", path, nil), 200, "")
}

// The insert transaction rechecks the parent read before authorization.
func TestParentIsRecheckedInsideTheInsertTransaction(t *testing.T) {
	h := newHarness(t, amplify)
	app := h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if parentAlive(tx, nil) != nil || parentAlive(tx, &row{Service: "amplify", ARN: str(app["appArn"])}) != nil {
		t.Fatal("a missing or live parent must pass")
	}
	var f *fault
	if err := parentAlive(tx, &row{Service: "amplify", ARN: str(app["appArn"]) + "-gone"}); !errors.As(err, &f) || f.kind != "notfound" {
		t.Fatalf("a deleted parent must be refused: %v", err)
	}
}

// Replacements are charged against the aggregate budget and roll back.
func TestUpdatesRespectTheAggregateMetadataBudget(t *testing.T) {
	h := newHarness(t, amplify)
	appID := str(h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)["appId"])
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	total, err := storedBytes(tx)
	if err != nil {
		t.Fatal(err)
	}
	filler := `{"x":"` + strings.Repeat("f", int(maxStoredBytes-total)-64) + `"}`
	if _, err := tx.Exec("INSERT INTO aws_catalog_resources("+columns+") VALUES('filler','us-east-1','filler','','f','f','arn:filler',?,'{}',999999,1)", filler); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, h.rest("amplify", "POST", "/apps/"+appID, map[string]any{"description": strings.Repeat("d", 200)}), 400, "BadRequestException")
	if got := h.rest("amplify", "GET", "/apps/"+appID, nil).body["app"].(map[string]any)["description"]; got != nil && got != "" {
		t.Fatalf("a refused update must roll back, got %v", got)
	}
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": strings.Repeat("n", 200)}), 429, "LimitExceededException")
}
