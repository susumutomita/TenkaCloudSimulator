package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// signedLambdaREST signs a Lambda REST call for the real Access boundary, the
// way an unchanged AWS SDK sends it. Secrets are synthetic process-local values.
func signedLambdaREST(key, secret, token, method, path string, body []byte) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, bytes.NewReader(body))
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Amz-Date", stamp)
	names := "content-type;host;x-amz-date"
	canonical := "content-type:application/json\nhost:" + r.Host + "\nx-amz-date:" + stamp + "\n"
	if token != "" {
		r.Header.Set("X-Amz-Security-Token", token)
		names += ";x-amz-security-token"
		canonical += "x-amz-security-token:" + token + "\n"
	}
	payload := sha256.Sum256(body)
	digest := sha256.Sum256([]byte(method + "\n" + path + "\n\n" + canonical + "\n" + names + "\n" + hex.EncodeToString(payload[:])))
	scope := now.Format("20060102") + "/us-east-1/lambda/aws4_request"
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(value))
		return h.Sum(nil)
	}
	signing := mac(mac(mac(mac([]byte("AWS4"+secret), now.Format("20060102")), "us-east-1"), "lambda"), "aws4_request")
	sig := mac(signing, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hex.EncodeToString(digest[:]))
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", key, scope, names, sig))
	return r
}

func TestLambdaSignedSessionUsesItsSnapshotAndTheFunctionRole(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lambdas, err := aws.NewLambdaStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lambdas.Close() })
	parameters, err := aws.NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	identities := core.NewIdentities(db)
	role, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/team/*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("executor", role); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"/team/public/hours": "weekdays", "/team/internal/token": "TC{synthetic}", "/elsewhere/secret": "other-team-dummy"} {
		if _, err = parameters.Put("us-east-1", name, "String", value, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	gateway := &aws.Gateway{Store: db, Identities: identities, Lambdas: lambdas, Parameters: parameters}
	source := "import boto3\nssm = boto3.client('ssm')\ndef handler(event, context):\n    try:\n        return {'value': ssm.get_parameter(Name=event['name'])['Parameter']['Value']}\n    except Exception as error:\n        return {'error': error.response['Error']['Code']}\n"
	for _, name := range []string{"viewer", "other-team"} {
		input, _ := json.Marshal(map[string]any{"FunctionName": name, "Runtime": "python3.12", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/executor", "Timeout": 5, "Code": map[string]any{"ZipFile": lambdaZIP(t, source)}})
		r := httptest.NewRequest("POST", functionRoot, bytes.NewReader(input))
		r.Header.Set("X-Simulator-Service", "lambda")
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		if w.Code != 201 {
			t.Fatal(name, w.Code, w.Body.String())
		}
	}
	operator := strings.Repeat("o", 64)
	a, err := NewAccess(gateway, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	viewerARN := "arn:aws:lambda:us-east-1:000000000000:function:viewer"
	issue := func(statements core.Statements) aws.STSCredentials {
		t.Helper()
		c, err := a.sdk.Issue(aws.STSSessionRequest{Principal: "participant", RoleARN: "arn:aws:iam::000000000000:role/participant", RoleID: "AROA-participant", SessionName: "player", Duration: 15 * time.Minute,
			Validate: func(context.Context) error { return nil }, Snapshot: func(context.Context) (core.Policy, error) {
				return core.Policy{Version: "2012-10-17", Statement: statements}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	useViewer := core.Statement{Effect: "Allow", Action: core.StringList{"lambda:InvokeFunction", "lambda:GetFunctionConfiguration", "lambda:UpdateFunctionCode"}, Resource: core.StringList{viewerARN}}
	// This participant may also read every parameter directly; the function must
	// still run with its own role, not with the caller's wider session.
	broad := issue(core.Statements{useViewer, {Effect: "Allow", Action: core.StringList{"ssm:*"}, Resource: core.StringList{"*"}}})
	narrow := issue(core.Statements{useViewer})
	send := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	invoke := func(c aws.STSCredentials, function, name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"name": name})
		return send(signedLambdaREST(c.AccessKeyID, c.SecretAccessKey, c.SessionToken, "POST", functionRoot+"/"+function+"/invocations", body))
	}
	if w := send(signedLambdaREST(narrow.AccessKeyID, narrow.SecretAccessKey, narrow.SessionToken, "GET", functionRoot+"/viewer/configuration", nil)); w.Code != 200 || !strings.Contains(w.Body.String(), "role/executor") {
		t.Fatal("participant could not read its function configuration", w.Code, w.Body.String())
	}
	if w := invoke(narrow, "viewer", "/team/internal/token"); w.Code != 200 || !strings.Contains(w.Body.String(), "TC{synthetic}") {
		t.Fatal("function did not use its own role for a caller without SSM access", w.Code, w.Body.String())
	}
	if w := invoke(broad, "viewer", "/elsewhere/secret"); w.Code != 200 || strings.Contains(w.Body.String(), "other-team-dummy") || !strings.Contains(w.Body.String(), "AccessDenied") {
		t.Fatal("function borrowed the caller's wider session", w.Code, w.Body.String())
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"other function": invoke(broad, "other-team", "/team/public/hours"),
		"list":           send(signedLambdaREST(broad.AccessKeyID, broad.SecretAccessKey, broad.SessionToken, "GET", functionRoot, nil)),
		"delete":         send(signedLambdaREST(broad.AccessKeyID, broad.SecretAccessKey, broad.SessionToken, "DELETE", functionRoot+"/viewer", nil)),
	} {
		if w.Code != 403 || w.Header().Get("X-Amzn-ErrorType") != "AccessDeniedException" {
			t.Fatal("session policy did not bound Lambda", name, w.Code, w.Body.String())
		}
	}
	update, _ := json.Marshal(map[string]any{"ZipFile": lambdaZIP(t, "def handler(event, context):\n    return {'fixed': True}\n")})
	if w := send(signedLambdaREST(narrow.AccessKeyID, narrow.SecretAccessKey, narrow.SessionToken, "PUT", functionRoot+"/viewer/code", update)); w.Code != 200 || !strings.Contains(w.Body.String(), "role/executor") {
		t.Fatal("participant could not redeploy its function", w.Code, w.Body.String())
	}
	if w := invoke(narrow, "viewer", "/team/internal/token"); w.Code != 200 || !strings.Contains(w.Body.String(), `"fixed": true`) {
		t.Fatal("redeployed code did not run", w.Code, w.Body.String())
	}
	if w := send(signedLambdaREST("TCSIMOPERATOR", operator, "", "GET", functionRoot, nil)); w.Code != 200 || !strings.Contains(w.Body.String(), "other-team") {
		t.Fatal("operator SDK credentials could not list functions", w.Code, w.Body.String())
	}
	tampered := signedLambdaREST(narrow.AccessKeyID, narrow.SecretAccessKey, narrow.SessionToken, "POST", functionRoot+"/viewer/invocations", []byte(`{"name":"/team/public/hours"}`))
	tampered.URL.Path = functionRoot + "/other-team/invocations"
	if w := send(tampered); w.Code != 403 || w.Header().Get("X-Amzn-ErrorType") != "UnrecognizedClientException" || w.Header().Get("Content-Type") != "application/json" || strings.Contains(w.Body.String(), narrow.SessionToken) {
		t.Fatal("re-targeted signature accepted or returned a non-Lambda envelope", w.Code, w.Header(), w.Body.String())
	}
}
