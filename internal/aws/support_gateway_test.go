package aws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/supportcases"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestSupportRejectsAmbiguousOrUnimplementedFields(t *testing.T) {
	for _, tc := range []struct{ operation, body string }{
		{"CreateCase", `{"subject":"a","subject":"b"}`},
		{"CreateCase", `{"communicationBody":"\ud800"}`},
		{"CreateCase", `{"subject":null}`},
		{"CreateCase", `{} {}`},
		{"CreateCase", `[]`},
		{"CreateCase", `{"subject":1}`},
		{"CreateCase", `{"ccEmailAddresses":["synthetic@example.invalid"]}`},
		{"CreateCase", `{"SUBJECT":"a"}`},
		{"DescribeCases", `{"maxResults":0}`},
		{"DescribeCases", `{"maxResults":101}`},
		{"DescribeCases", `{"afterTime":"2026-03-02T00:00:00Z"}`},
		{"DescribeCases", `{"nextToken":""}`},
		{"AddAttachmentsToSet", `{"attachments":"a"}`},
		{"AddAttachmentsToSet", `{"attachments":[{"fileName":"a","Data":"YQ=="}]}`},
		{"AddAttachmentsToSet", `{"attachments":[{"fileName":"a","data":[97]}]}`},
		{"AddAttachmentsToSet", `{"attachments":[{"fileName":"a","data":"YQ==\n"}]}`},
		{"AddAttachmentsToSet", `{"attachments":[{"fileName":"a","data":"!"}]}`},
	} {
		if _, err := supportInput([]byte(tc.body), supportFields[tc.operation]); err == nil {
			t.Errorf("%s accepted %s", tc.operation, tc.body)
		}
	}
	for _, s := range []string{`{"subject":"\ud83c\udf31","communicationBody":"text"}`, `{"subject":"\\ud800","communicationBody":"text"}`} {
		if _, err := supportInput([]byte(s), supportFields["CreateCase"]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSupportGatewayRejectsWrongProtocolBeforeMutation(t *testing.T) {
	db, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := supportcases.New(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := Gateway{Identities: core.NewIdentities(db), Support: service}
	for _, tc := range []struct {
		method, path, header, value string
		repeat                      bool
		status                      int
	}{
		{"GET", "/", "", "", false, 400}, {"POST", "/?a=b", "", "", false, 400},
		{"POST", "/wrong", "", "", false, 400}, {"POST", "/", "Content-Type", "application/json", false, 400},
		{"POST", "/", "X-Amz-Target", supportTarget + "ListCases", false, 501},
		{"POST", "/", "X-Simulator-Region", "us-west-2", false, 400},
		{"POST", "/", "X-Simulator-Service", "kms", false, 400},
		{"POST", "/", "Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261128/us-west-2/support/aws4_request", false, 403},
		{"POST", "/", "X-Amz-Security-Token", "unsupported", false, 403},
		{"POST", "/", "X-Amz-Target", supportTarget + "CreateCase", true, 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"subject":"one","communicationBody":"a note"}`))
		r = r.WithContext(provider.WithActor(r.Context(), provider.Actor{CredentialRole: provider.CredentialOperator, CredentialIdentity: core.Administrator, ExerciseIdentity: core.Administrator}))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", supportTarget+"CreateCase")
		if tc.header != "" {
			if tc.repeat {
				r.Header.Add(tc.header, tc.value)
			} else {
				r.Header.Set(tc.header, tc.value)
			}
		}
		w := httptest.NewRecorder()
		g.serveSupport(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s %s: %d %s", tc.method, tc.path, tc.header, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
	r.Header.Set("X-Amz-Target", supportTarget+"DescribeCases")
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r = r.WithContext(provider.WithActor(r.Context(), provider.Actor{CredentialRole: provider.CredentialOperator, CredentialIdentity: core.Administrator, ExerciseIdentity: core.Administrator}))
	w := httptest.NewRecorder()
	g.serveSupport(w, r)
	var result struct {
		Cases []any `json:"cases"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Cases == nil || len(result.Cases) != 0 {
		t.Fatalf("failed requests must preserve empty state: %d %s", w.Code, w.Body.String())
	}
}

func TestSupportGatewayReturnsOnlyAuthorizedCompleteResponses(t *testing.T) {
	team, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { team.Close() })
	service, err := supportcases.New(team.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Identities: core.NewIdentities(team), Support: service}
	editor := "case-author"
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["support:CreateCase","support:DescribeCases"],"Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Identities.Set(editor, policy); err != nil {
		t.Fatal(err)
	}
	request := func(gateway *Gateway, actor, op, body string, want int, code string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", supportTarget+op)
		if actor != "" {
			r = r.WithContext(provider.WithActor(r.Context(), provider.Actor{CredentialRole: provider.CredentialParticipant, CredentialIdentity: actor, ExerciseIdentity: actor}))
		}
		w := httptest.NewRecorder()
		gateway.serveSupport(w, r)
		var out map[string]any
		if w.Code != want || json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Header().Get("X-Amzn-Errortype") != code {
			t.Fatalf("%s status=%d type=%q", op, w.Code, w.Header().Get("X-Amzn-Errortype"))
		}
		if code != "" && (out["__type"] != code || out["message"] == "" || out["cases"] != nil || out["caseId"] != nil || strings.Contains(w.Body.String(), "sqlite")) {
			t.Fatal("partial data or internal error details in public response")
		}
		return out
	}
	create := `{"subject":"Target","communicationBody":"Created by the exercise identity"}`
	request(g, "", "CreateCase", create, 403, "AccessDeniedException")
	request(g, "unprivileged", "CreateCase", `{"subject":null}`, 403, "AccessDeniedException")
	request(g, editor, "CreateCase", `{"subject":"wrong","subject":"second"}`, 400, "ValidationException")
	request(g, editor, "CreateCase", strings.Repeat(" ", (288<<10)+1), 400, "ValidationException")
	request(g, editor, "CreateCase", `{"subject":"missing communication"}`, 400, "ValidationException")
	request(g, core.Administrator, "DescribeAttachment", "{}", 400, "ValidationException")
	request(g, core.Administrator, "DescribeCommunications", "{}", 400, "ValidationException")
	request(&Gateway{Identities: g.Identities}, editor, "CreateCase", create, 500, "InternalServerError")
	if cases := request(g, editor, "DescribeCases", "{}", 200, "")["cases"].([]any); len(cases) != 0 {
		t.Fatal("failed submissions created cases")
	}
	id := request(g, editor, "CreateCase", create, 200, "")["caseId"].(string)
	cases := request(g, editor, "DescribeCases", "{}", 200, "")["cases"].([]any)
	if len(cases) != 1 {
		t.Fatal("expected one authorized case")
	}
	saved := cases[0].(map[string]any)
	reply := saved["recentCommunications"].(map[string]any)["communications"].([]any)[0].(map[string]any)
	if saved["caseId"] != id || reply["submittedBy"] != editor || reply["body"] != "Created by the exercise identity" {
		t.Fatal("identity or authored data changed")
	}
	if err := team.Close(); err != nil {
		t.Fatal(err)
	}
	request(g, editor, "DescribeCases", "{}", 500, "InternalServerError")
}
