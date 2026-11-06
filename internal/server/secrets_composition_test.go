package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSecretsCompositionNativeRootAndS3LiteralNeighbors(t *testing.T) {
	handler, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	call := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	require := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("want %d got %d", code, w.Code)
		}
	}
	cap := call("GET", "/_simulator/v1/capabilities", "", nil)
	require(cap, 200)
	var caps struct {
		Services map[string]struct {
			Operations []string `json:"operations"`
		} `json:"services"`
	}
	if err = json.Unmarshal(cap.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	wantOperations := map[string]bool{"CreateSecret": true, "DescribeSecret": true, "ListSecrets": true, "ListSecretVersionIds": true, "PutSecretValue": true, "GetSecretValue": true, "UpdateSecretVersionStage": true, "DeleteSecret": true}
	for _, operation := range caps.Services["secretsmanager"].Operations {
		if !wantOperations[operation] {
			t.Fatal("canonical capabilities included an unexpected or duplicate Secrets operation")
		}
		delete(wantOperations, operation)
	}
	if len(wantOperations) != 0 {
		t.Fatal("canonical capabilities omitted bounded Secrets operations")
	}
	require(call("PUT", "/protocol-neighbors", "", nil), 200)
	keys := []string{"secretsmanager.CreateSecret", "secretsmanager.ListSecrets", "AmazonSSM.PutParameter", "X-Amz-Target", "secretsmanager/a/../b", "secretsmanager/a//b"}
	for i, key := range keys {
		require(call("PUT", "/protocol-neighbors/"+url.PathEscape(key), strings.Repeat("x", i+1), nil), 200)
	}
	headers := map[string]string{"Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": "secretsmanager.CreateSecret"}
	created := call("POST", "/", `{"Name":"training/composed","ClientRequestToken":"00000000-0000-0000-0000-000000000001","SecretString":"DUMMY-secret-composed"}`, headers)
	require(created, 200)
	var identity struct{ ARN string }
	if err = json.Unmarshal(created.Body.Bytes(), &identity); err != nil || identity.ARN == "" {
		t.Fatal("canonical server did not initialize native Secrets store")
	}
	headers["X-Amz-Target"] = "secretsmanager.DescribeSecret"
	described := call("POST", "/", `{"SecretId":"training/composed"}`, headers)
	require(described, 200)
	if strings.Contains(described.Body.String(), "DUMMY-secret-") {
		t.Fatal("composition metadata leaked value")
	}
	for i, key := range keys {
		got := call("GET", "/protocol-neighbors/"+url.PathEscape(key), "", nil)
		require(got, 200)
		if got.Body.String() != strings.Repeat("x", i+1) {
			t.Fatal("S3 literal key stolen or altered by native dispatch")
		}
		require(call("HEAD", "/protocol-neighbors/"+url.PathEscape(key), "", nil), 200)
	}
	// Protocol-specific native requests cannot be mistaken for an S3 list even
	// when the target is absent or belongs to a different root protocol.
	signed := map[string]string{"Content-Type": "application/x-amz-json-1.1", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261104/us-east-1/secretsmanager/aws4_request, SignedHeaders=host, Signature=dummy"}
	require(call(http.MethodPost, "/", "{}", signed), 501)
	signed["X-Amz-Target"] = "AmazonSSM.PutParameter"
	require(call(http.MethodPost, "/", "{}", signed), 501)
	headers["X-Amz-Target"] = "secretsmanager.GetSecretValue"
	value := call("POST", "/", `{"SecretId":"training/composed"}`, headers)
	require(value, 200)
	var got struct{ SecretString string }
	if err = json.Unmarshal(value.Body.Bytes(), &got); err != nil || got.SecretString != "DUMMY-secret-composed" {
		t.Fatal("failed protocol requests changed saved secret")
	}
	headers["X-Amz-Target"] = "secretsmanager.DeleteSecret"
	body, _ := json.Marshal(map[string]any{"SecretId": identity.ARN, "ForceDeleteWithoutRecovery": true})
	require(call("POST", "/", string(body), headers), 200)
	for _, key := range keys {
		require(call("DELETE", "/protocol-neighbors/"+url.PathEscape(key), "", nil), 204)
	}
	require(call("DELETE", "/protocol-neighbors", "", nil), 204)
}
