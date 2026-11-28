package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupportAccessChecksEachActionAgainstLocalIdentity(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeState() })
	operator := strings.Repeat("a", 64)
	access, err := NewAccess(h, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, target string, body any, want int) map[string]any {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:1234"
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		r.Header.Set("Content-Type", "application/json")
		if target != "" {
			r.Header.Set("Content-Type", "application/x-amz-json-1.1")
			r.Header.Set("X-Amz-Target", "AWSSupport_20130415."+target)
			r.Header.Set("X-Simulator-Region", "us-east-1")
		}
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s %s: want %d, got %d %s", method, path, target, want, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	policy := func(resource string) map[string]any {
		return map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "support:DescribeCases", "Resource": resource}}}
	}
	const policyPath = "/_simulator/v1/iam/principals/reader/policy"
	request("PUT", policyPath, operator, "", policy("*"), 200)
	issued := request("POST", "/_simulator/v1/access", operator, "", map[string]any{"Principal": "reader"}, 200)
	reader, _ := issued["AccessToken"].(string)
	if reader == "" {
		t.Fatal("missing participant token")
	}
	input := map[string]any{"subject": "Local test", "communicationBody": "First note"}
	id := request("POST", "/", operator, "CreateCase", input, 200)["caseId"].(string)
	selected := map[string]any{"caseIdList": []string{id}}
	owner := request("POST", "/", operator, "DescribeCases", selected, 200)
	got := request("POST", "/", reader, "DescribeCases", selected, 200)
	if len(got["cases"].([]any)) != 1 || len(owner["cases"].([]any)) != 1 {
		t.Fatal("wrong selected case")
	}
	for _, target := range []string{"CreateCase", "AddCommunicationToCase", "DescribeCommunications", "ResolveCase", "AddAttachmentsToSet", "DescribeAttachment"} {
		output := request("POST", "/", reader, target, map[string]any{}, http.StatusForbidden)
		if output["__type"] != "AccessDeniedException" {
			t.Fatal(target, output)
		}
	}
	request("PUT", policyPath, operator, "", policy("arn:aws:support::000000000000:case/"+id), 200)
	request("POST", "/", reader, "DescribeCases", selected, 403)
	request("POST", "/", "", "DescribeCases", selected, 401)
	if output := request("POST", "/", operator, "ResolveCase", map[string]string{"caseId": id}, 200); output["initialCaseStatus"] != "opened" || output["finalCaseStatus"] != "resolved" {
		t.Fatal(output)
	}
	if rows := request("POST", "/", operator, "DescribeCases", selected, 200)["cases"].([]any); len(rows) != 0 {
		t.Fatal("resolved case shown by default")
	}
	request("POST", "/", operator, "DescribeSeverityLevels", map[string]any{}, 501)
}
