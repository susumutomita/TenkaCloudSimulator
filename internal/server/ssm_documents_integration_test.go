package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSSMDocumentIntegratedAccessBoundaries(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	token := strings.Repeat("d", 64)
	a, err := NewAccess(h, token, false)
	if err != nil {
		t.Fatal(err)
	}
	content := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"echo","inputs":{"runCommand":["echo not-executed"]}}]}`
	body, _ := json.Marshal(map[string]string{"Name": "local-doc", "Content": content})
	send := func(method, path, op, body, token string, extra map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM."+op)
		for k, v := range extra {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	for _, badToken := range []string{"", strings.Repeat("e", 64)} {
		if w := send("POST", "/", "CreateDocument", string(body), badToken, nil); w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		method, path string
		headers      map[string]string
	}{
		{"GET", "/", nil}, {"POST", "/?unknown=1", nil},
		{"POST", "/", map[string]string{"X-Simulator-Region": "invalid"}},
		{"POST", "/", map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIA_NOT_REAL/20261111/us-east-1/ssm/aws4_request"}},
		{"POST", "/", map[string]string{"X-Amz-Security-Token": "invalid"}},
	} {
		if w := send(tc.method, tc.path, "CreateDocument", string(body), token, tc.headers); w.Code == 200 {
			t.Fatal("invalid envelope succeeded", tc)
		}
	}
	if w := send("POST", "/", "CreateDocument", string(body), token, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A participant token binds its IAM identity; selecting administrator cannot
	// turn permission to read into permission to create or delete a definition.
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetDocument","Resource":"arn:aws:ssm:us-east-1:000000000000:document/local-doc"}]}`
	if w := send("PUT", "/_simulator/v1/iam/principals/doc-reader/policy", "", policy, token, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := send("POST", "/_simulator/v1/access", "", `{"Principal":"doc-reader"}`, token, nil)
	var issued struct{ AccessToken string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("POST", "/", "GetDocument", `{"Name":"local-doc"}`, issued.AccessToken, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("POST", "/", "DeleteDocument", `{"Name":"local-doc"}`, issued.AccessToken, map[string]string{"X-Simulator-Principal": "simulator-admin"}); w.Code != 403 {
		t.Fatal("participant elevated", w.Code, w.Body.String())
	}
	if w := send("POST", "/", "GetDocument", `{"Name":"local-doc"}`, token, nil); w.Code != 200 {
		t.Fatal("denied deletion changed state")
	}
}
func TestSSMDocumentParameterHandoffCannotBroadenScope(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	a, credentials, _, policy := newHandoffFixture(t)
	a.next = h
	policy.Statement = core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:*"}, Resource: core.StringList{"*"}}}
	code := issueHandoffCode(t, a, credentials)
	login := handoffRedeem(a, code)
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no handoff session")
	}
	for _, op := range []string{"CreateDocument", "GetDocument", "DescribeDocument", "ListDocuments", "UpdateDocument", "DeleteDocument"} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", strings.NewReader(`{"Name":"local-doc"}`))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM."+op)
		r.AddCookie(cookies[0])
		if w := handoffRequest(a, r); w.Code != 403 {
			t.Fatal("parameter handoff broadened", op, w.Code, w.Body.String())
		}
	}
}
