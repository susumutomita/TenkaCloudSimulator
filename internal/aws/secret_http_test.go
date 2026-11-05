package aws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func secretHTTPCall(s *SecretStore, operation, body string, authorize SecretAuthorizer) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "secretsmanager."+operation)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r, "us-east-1", authorize)
	return w
}

func secretJSONFixture(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSecretHTTPSevenOperationLifecycleRevealsOnlyAuthorizedGet(t *testing.T) {
	_, s := secretTestStore(t)
	const a = "00000000-0000-0000-0000-000000000001"
	const b = "00000000-0000-0000-0000-000000000002"
	allow := func(string, string) error { return nil }
	call := func(op string, input any) *httptest.ResponseRecorder {
		w := secretHTTPCall(s, op, secretJSONFixture(t, input), allow)
		if w.Code != 200 {
			t.Fatal("native protocol operation failed", op, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Simulator-Encryption") != "none" {
			t.Fatal("protocol hid the local storage boundary")
		}
		if op != "GetSecretValue" && (strings.Contains(w.Body.String(), "DUMMY-secret-") || strings.Contains(w.Body.String(), "SecretString") || strings.Contains(w.Body.String(), "KmsKeyId")) {
			t.Fatal("metadata operation exposed a value or fake KMS metadata", op)
		}
		return w
	}
	created := call("CreateSecret", map[string]any{"Name": "training/http", "ClientRequestToken": a, "SecretString": "DUMMY-secret-A"})
	var identity SecretMutationResult
	if err := json.Unmarshal(created.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	call("DescribeSecret", map[string]any{"SecretId": identity.ARN})
	call("ListSecrets", map[string]any{"MaxResults": 1, "SortBy": "name", "SortOrder": "asc"})
	call("PutSecretValue", map[string]any{"SecretId": identity.ARN, "ClientRequestToken": b, "SecretString": "DUMMY-secret-B", "VersionStages": []string{"AWSCURRENT"}})
	value := call("GetSecretValue", map[string]any{"SecretId": identity.ARN, "VersionStage": "AWSPREVIOUS"})
	var revealed map[string]any
	if err := json.Unmarshal(value.Body.Bytes(), &revealed); err != nil || revealed["SecretString"] != "DUMMY-secret-A" {
		t.Fatal("authorized selector did not return the actual stored version")
	}
	call("UpdateSecretVersionStage", map[string]any{"SecretId": identity.ARN, "VersionStage": "AWSCURRENT", "MoveToVersionId": a, "RemoveFromVersionId": b})
	value = call("GetSecretValue", map[string]any{"SecretId": "training/http"})
	if err := json.Unmarshal(value.Body.Bytes(), &revealed); err != nil || revealed["SecretString"] != "DUMMY-secret-A" {
		t.Fatal("current-version rollback did not affect actual retrieval")
	}
	call("DeleteSecret", map[string]any{"SecretId": identity.ARN, "ForceDeleteWithoutRecovery": true})
	call("DeleteSecret", map[string]any{"SecretId": identity.ARN, "ForceDeleteWithoutRecovery": true})
	list := call("ListSecrets", map[string]any{})
	if strings.Contains(list.Body.String(), identity.ARN) {
		t.Fatal("deleted secret remained in metadata listing")
	}
	missing := secretHTTPCall(s, "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": identity.ARN}), allow)
	if missing.Code != 400 || !strings.Contains(missing.Body.String(), "ResourceNotFoundException") {
		t.Fatal("deleted version remained available")
	}
}

func TestSecretHTTPRejectsUnsupportedOrAmbiguousWritesWithoutDisclosure(t *testing.T) {
	_, s := secretTestStore(t)
	const token = "00000000-0000-0000-0000-000000000001"
	allow := func(string, string) error { return nil }
	create := `{"Name":"training/strict","ClientRequestToken":"` + token + `","SecretString":"DUMMY-secret-sentinel"}`
	w := secretHTTPCall(s, "CreateSecret", create, allow)
	if w.Code != 200 {
		t.Fatal("strict fixture failed")
	}
	cases := []struct{ op, body string }{
		{"CreateSecret", `{"Name":"other","ClientRequestToken":"` + token + `","SecretString":"DUMMY-secret-sentinel","KmsKeyId":"alias/aws/secretsmanager"}`},
		{"CreateSecret", `{"Name":"other","ClientRequestToken":"` + token + `","SecretString":"DUMMY-secret-sentinel","SecretBinary":"ZHVtbXk="}`},
		{"CreateSecret", `{"Name":"other","ClientRequestToken":"` + token + `","SecretString":"DUMMY-\ud800"}`},
		{"CreateSecret", `{"Name":"other","Name":"duplicate","ClientRequestToken":"` + token + `","SecretString":"DUMMY-secret-sentinel"}`},
		{"CreateSecret", `{"Name":"other","ClientRequestToken":"` + token + `","SecretString":null}`},
		{"PutSecretValue", `{"SecretId":"training/strict","ClientRequestToken":"` + token + `","SecretString":"DUMMY-secret-sentinel","VersionStages":["AWSPENDING"]}`},
		{"DeleteSecret", `{"SecretId":"training/strict","ForceDeleteWithoutRecovery":true}`},
		{"DeleteSecret", `{"SecretId":"training/strict","RecoveryWindowInDays":7}`},
		{"DeleteSecret", `{"SecretId":"training/strict"}`},
		{"DeleteSecret", `{"SecretId":"training/strict","ForceDeleteWithoutRecovery":false}`},
	}
	for _, fixture := range cases {
		w = secretHTTPCall(s, fixture.op, fixture.body, allow)
		if w.Code != 400 || strings.Contains(w.Body.String(), "DUMMY-") {
			t.Fatal("unsupported request mutated or disclosed secret data", fixture.op, w.Code)
		}
	}
	w = secretHTTPCall(s, "RotateSecret", `{}`, allow)
	if w.Code != 501 {
		t.Fatal("unsupported rotation pretended success")
	}
	value, err := s.Get(context.Background(), "us-east-1", "training/strict", "", "", allow)
	if err != nil || value.material.plaintext() != "DUMMY-secret-sentinel" {
		t.Fatal("rejected writes changed the live secret")
	}
	for _, authorize := range []SecretAuthorizer{nil, func(string, string) error { return errors.New("DUMMY-secret-sensitive-error") }} {
		w = secretHTTPCall(s, "GetSecretValue", `{"SecretId":"training/strict"}`, authorize)
		if w.Code != 500 || strings.Contains(w.Body.String(), "DUMMY-") {
			t.Fatal("infrastructure error became denial or disclosed details")
		}
	}
}

func TestSecretGatewayCapturesPolicyBeforeSingleConnectionTransaction(t *testing.T) {
	db, s := secretTestStore(t)
	ids := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:*","Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:training/*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = ids.Set("secret-author", policy); err != nil {
		t.Fatal(err)
	}
	call := func(op, body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "secretsmanager."+op)
		r.Header.Set("X-Simulator-Principal", "secret-author")
		w := httptest.NewRecorder()
		serveSecretGateway(w, r, s, ids)
		return w
	}
	w := call("CreateSecret", `{"Name":"training/pure-policy","ClientRequestToken":"00000000-0000-0000-0000-000000000001","SecretString":"DUMMY-pure-policy"}`)
	if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatal("policy snapshot entered a nested DB lookup or denied the native action", w.Code)
	}
	policy, err = core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = ids.Set("secret-author", policy); err != nil {
		t.Fatal(err)
	}
	w = call("GetSecretValue", `{"SecretId":"training/pure-policy"}`)
	if w.Code != 403 || w.Header().Get("X-Simulator-IAM-Decision") != "explicitDeny" || strings.Contains(w.Body.String(), "DUMMY-") {
		t.Fatal("updated identity policy failed to deny retrieval")
	}
	if _, err = db.Database().Exec("UPDATE identity_policies SET document='invalid' WHERE name='secret-author'"); err != nil {
		t.Fatal(err)
	}
	w = call("GetSecretValue", `{"SecretId":"training/pure-policy"}`)
	if w.Code != 500 || strings.Contains(w.Body.String(), "invalid") {
		t.Fatal("policy storage failure collapsed into IAM Deny or exposed internals")
	}
}
