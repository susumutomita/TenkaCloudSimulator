package aws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSecretVersionListNativeHTTPUsesOnlyListPermission(t *testing.T) {
	db, s, arn := secretContractFixture(t)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:ListSecretVersionIds","Resource":"` + arn + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	identities := core.NewIdentities(db)
	if err = identities.Set("version-reader", policy); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Secrets: s, Identities: identities}
	call := func(op, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "secretsmanager."+op)
		r.Header.Set("X-Simulator-Principal", "version-reader")
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		return w
	}
	before := secretStateSnapshot(t, db)
	w := call("ListSecretVersionIds", `{"SecretId":"`+arn+`","MaxResults":1,"IncludeDeprecated":true}`)
	if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Simulator-Encryption") != "none" {
		t.Fatal("native list admission", w.Code)
	}
	var out SecretVersionListResult
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ARN != arn || len(out.Versions) != 1 || out.NextToken == "" {
		t.Fatal("native metadata shape")
	}
	if strings.Contains(w.Body.String(), "DUMMY-") || strings.Contains(w.Body.String(), "SecretString") {
		t.Fatal("native list exposed value")
	}
	if w = call("GetSecretValue", `{"SecretId":"`+arn+`"}`); w.Code != 403 {
		t.Fatal("list permission granted secret-value retrieval", w.Code)
	}
	if w = call("ListSecretVersionIds", `{"SecretId":"`+arn+`","NextToken":"bad"}`); w.Code != 400 || w.Header().Get("X-Amzn-Errortype") != "InvalidNextTokenException" {
		t.Fatal("invalid token lost native error classification", w.Code)
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("native read/error/denial changed persistent graph")
	}
	// Revoke the same captured resource permission: malformed cursor must not
	// become a token-validity oracle for an unauthorized participant.
	denied, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"secretsmanager:*","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("version-reader", denied); err != nil {
		t.Fatal(err)
	}
	if w = call("ListSecretVersionIds", `{"SecretId":"`+arn+`","NextToken":"bad"}`); w.Code != 403 {
		t.Fatal("denied list examined token", w.Code)
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("IAM update changed secret graph")
	}
}

func TestSecretVersionListNativeHTTPStrictBodyAndReadFaultRepair(t *testing.T) {
	db, s, arn := secretContractFixture(t)
	before := secretStateSnapshot(t, db)
	for _, body := range []string{`{}`, `{"SecretId":"` + arn + `","IncludeDeprecated":null}`, `{"SecretId":"` + arn + `","IncludeDeprecated":"false"}`, `{"SecretId":"` + arn + `","MaxResults":1.0}`, `{"SecretId":"` + arn + `","SecretId":"` + arn + `"}`, `{"SecretId":"` + arn + `","NextToken":""}`, `{"SecretId":"` + arn + `","VersionId":"` + secretContractA + `"}`, `{"SecretId":"` + arn + `","SecretString":"DUMMY-do-not-reflect"}`, `{"SecretId":"` + arn + `","includeDeprecated":true}`} {
		w := secretHTTPCall(s, "ListSecretVersionIds", body, secretContractAllow)
		if w.Code != 400 || strings.Contains(w.Body.String(), "DUMMY-") {
			t.Fatal("invalid list request accepted/reflected", w.Code)
		}
		if before != secretStateSnapshot(t, db) {
			t.Fatal("invalid request changed state")
		}
	}
	if _, err := db.Database().Exec("ALTER TABLE aws_secret_versions RENAME TO secret_version_private_fault"); err != nil {
		t.Fatal(err)
	}
	w := secretHTTPCall(s, "ListSecretVersionIds", `{"SecretId":"`+arn+`"}`, secretContractAllow)
	if w.Code != 500 || w.Header().Get("X-Amzn-Errortype") != "InternalServiceError" || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "SELECT") {
		t.Fatal("storage error not sanitized", w.Code)
	}
	if _, err := db.Database().Exec("ALTER TABLE secret_version_private_fault RENAME TO aws_secret_versions"); err != nil {
		t.Fatal(err)
	}
	if before != secretStateSnapshot(t, db) {
		t.Fatal("failed read damaged persistent state")
	}
	if w = secretHTTPCall(s, "ListSecretVersionIds", `{"SecretId":"`+arn+`"}`, secretContractAllow); w.Code != 200 {
		t.Fatal("repaired list did not recover", w.Code)
	}
	if _, err := s.Get(context.Background(), "us-east-1", arn, "", "", secretContractAllow); err != nil {
		t.Fatal("list repair changed old value operation", err)
	}
}
