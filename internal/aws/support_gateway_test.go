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
		{"CreateCase", `{"serviceCode":""}`},
		{"CreateCase", `{"serviceCode":null}`},
		{"CreateCase", `{"serviceCode":17}`},
		{"CreateCase", `{"serviceCode":"sentinel-report-incident","serviceCode":"sentinel-report-incident"}`},
		{"DescribeCases", `{"serviceCode":"sentinel-report-incident"}`},
		{"AddCommunicationToCase", `{"serviceCode":"sentinel-report-incident"}`},
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
