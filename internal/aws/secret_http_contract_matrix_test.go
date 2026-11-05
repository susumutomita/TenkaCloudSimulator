package aws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Primary request/selector contracts: AWS Secrets Manager API_CreateSecret,
// API_PutSecretValue and API_GetSecretValue (inspected 2026-11-04). This native
// subset deliberately refuses binary/custom labels/recovery windows; no test
// interprets such a refusal as full AWS compatibility. Fixtures are dummy only.
const secretMatrixTokenA = "10000000-0000-0000-0000-000000000001"
const secretMatrixTokenB = "10000000-0000-0000-0000-000000000002"
const secretMatrixTokenC = "10000000-0000-0000-0000-000000000003"

func secretMatrixSeed(t *testing.T) (*core.Store, *SecretStore, SecretMutationResult) {
	t.Helper()
	db, s := secretTestStore(t)
	allow := func(string, string) error { return nil }
	created, err := s.Create(context.Background(), "us-east-1", "training/http-matrix", "dummy metadata", secretMatrixTokenA, newSecretMaterial("DUMMY-secret-matrix-original"), allow)
	if err != nil {
		t.Fatal("cannot seed target")
	}
	if _, err = s.Create(context.Background(), "us-east-1", "training/http-peer", "peer metadata", secretMatrixTokenC, newSecretMaterial("DUMMY-secret-peer-original"), allow); err != nil {
		t.Fatal("cannot seed peer")
	}
	return db, s, created
}

func secretMatrixError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var output struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(w.Body.Bytes(), &output) != nil || w.Code != status || output.Type != code || output.Message == "" {
		t.Fatalf("native error contract mismatch: status%d want%d type%q want%q", w.Code, status, output.Type, code)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Simulator-Encryption") != "none" || w.Header().Get("Content-Type") != "application/x-amz-json-1.1" {
		t.Fatal("error response lost local non-encryption/no-cache boundary")
	}
	if strings.Contains(w.Body.String(), "DUMMY-secret-") || strings.Contains(w.Body.String(), "SecretString") {
		t.Fatal("native failure disclosed request/stored value")
	}
}

func secretMatrixRead(t *testing.T, s *SecretStore, id, version, stage, want string) {
	t.Helper()
	input := map[string]any{"SecretId": id}
	if version != "" {
		input["VersionId"] = version
	}
	if stage != "" {
		input["VersionStage"] = stage
	}
	w := secretHTTPCall(s, "GetSecretValue", secretJSONFixture(t, input), func(string, string) error { return nil })
	var result struct {
		Value string `json:"SecretString"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Value != want {
		t.Fatal("authorized native read did not preserve exact dummy bytes")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authorized value response may be cached")
	}
}

func TestSecretHTTPDocumentAndUnicodeRefusalHasNoDurableEffects(t *testing.T) {
	db, s, _ := secretMatrixSeed(t)
	prefix := `{"Name":"training/repaired","ClientRequestToken":"` + secretMatrixTokenB + `","SecretString":`
	fixtures := []struct{ name, body string }{
		{"truncated unicode", prefix + `"DUMMY-secret-\u12"}`},
		{"nonhex unicode", prefix + `"DUMMY-secret-\u12xz"}`},
		{"high without escape", prefix + `"DUMMY-secret-\ud800x"}`},
		{"high followed by malformed low", prefix + `"DUMMY-secret-\ud800\uxxxx"}`},
		{"high followed by high", prefix + `"DUMMY-secret-\ud800\ud801"}`},
		{"lone low", prefix + `"DUMMY-secret-\udfff"}`},
		{"unterminated scalar", prefix + `"DUMMY-secret-unterminated`},
		{"unterminated escape", prefix + `"DUMMY-secret-` + "\\"},
		{"invalid utf8", prefix + `"DUMMY-secret-` + string([]byte{0xff}) + `"}`},
		{"primitive root", `"DUMMY-secret-root"`},
		{"array root", `["DUMMY-secret-root"]`},
		{"number root", `123`},
		{"concatenated objects", prefix + `"DUMMY-secret-value"} {}`},
		{"nested duplicate", prefix + `{"part":"DUMMY-secret-one","part":"DUMMY-secret-two"}}`},
		{"nested null", prefix + `[null]}`},
		{"unterminated object", prefix + `"DUMMY-secret-value"`},
		{"excessive nesting", prefix + strings.Repeat("[", 18) + `"DUMMY-secret-deep"` + strings.Repeat("]", 18) + `}`},
		{"wrong value type", prefix + `false}`},
		{"missing value", `{"Name":"training/repaired","ClientRequestToken":"` + secretMatrixTokenB + `"}`},
		{"lowercase field", `{"Name":"training/repaired","ClientRequestToken":"` + secretMatrixTokenB + `","secretString":"DUMMY-secret-value"}`},
	}
	before := secretStateSnapshot(t, db)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			calls := 0
			w := secretHTTPCall(s, "CreateSecret", fixture.body, func(string, string) error { calls++; return nil })
			secretMatrixError(t, w, 400, "InvalidParameterException")
			if calls != 0 {
				t.Fatal("ambiguous document reached storage authorization")
			}
			if secretStateSnapshot(t, db) != before {
				t.Fatal("rejected document changed target/peer/version/stage/retired identity graph")
			}
		})
	}
	// Same previously rejected name/token must succeed after input repair; Unicode
	// scalar pairs, an escaped literal backslash and quotes must survive native IO.
	valid := prefix + `"DUMMY-secret-\ud83d\ude80-\\ud800-\"quoted\""}`
	w := secretHTTPCall(s, "CreateSecret", valid, func(string, string) error { return nil })
	if w.Code != 200 {
		t.Fatal("repaired Unicode request failed")
	}
	secretMatrixRead(t, s, "training/repaired", "", "", `DUMMY-secret-🚀-\ud800-"quoted"`)
	secretMatrixRead(t, s, "training/http-matrix", "", "", "DUMMY-secret-matrix-original")
	secretMatrixRead(t, s, "training/http-peer", "", "", "DUMMY-secret-peer-original")
}

func TestSecretHTTPOptionalSelectorsRefuseAmbiguityAndAllowExactVersionRead(t *testing.T) {
	db, s, created := secretMatrixSeed(t)
	allow := func(string, string) error { return nil }
	w := secretHTTPCall(s, "PutSecretValue", secretJSONFixture(t, map[string]any{"SecretId": created.ARN, "ClientRequestToken": secretMatrixTokenB, "SecretString": "DUMMY-secret-matrix-current", "VersionStages": []string{"AWSCURRENT"}}), allow)
	if w.Code != 200 {
		t.Fatal("cannot seed second version through HTTP")
	}
	fixtures := []struct{ name, op, body string }{
		{"current stage empty", "GetSecretValue", `{"SecretId":"training/http-matrix","VersionStage":""}`},
		{"unknown stage", "GetSecretValue", `{"SecretId":"training/http-matrix","VersionStage":"AWSPENDING"}`},
		{"version empty", "GetSecretValue", `{"SecretId":"training/http-matrix","VersionId":""}`},
		{"version malformed", "GetSecretValue", `{"SecretId":"training/http-matrix","VersionId":"short"}`},
		{"typed version", "GetSecretValue", `{"SecretId":"training/http-matrix","VersionId":3}`},
		{"missing secret id", "GetSecretValue", `{}`},
		{"empty secret id", "DescribeSecret", `{"SecretId":""}`},
		{"partial ARN", "GetSecretValue", `{"SecretId":"arn:aws:secretsmanager:us-east-1:000000000000:secret:training/unqualified"}`},
		{"foreign account ARN", "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": strings.Replace(created.ARN, ":000000000000:", ":111111111111:", 1)})},
		{"foreign region ARN", "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": strings.Replace(created.ARN, ":us-east-1:", ":us-west-2:", 1)})},
		{"next token empty", "ListSecrets", `{"NextToken":""}`},
		{"sort key empty", "ListSecrets", `{"SortBy":""}`},
		{"sort direction empty", "ListSecrets", `{"SortOrder":""}`},
		{"limit fraction", "ListSecrets", `{"MaxResults":1.5}`},
		{"limit string", "ListSecrets", `{"MaxResults":"2"}`},
		{"labels empty", "PutSecretValue", `{"SecretId":"training/http-matrix","ClientRequestToken":"` + secretMatrixTokenC + `","SecretString":"DUMMY-secret-refused","VersionStages":[]}`},
		{"labels multiple", "PutSecretValue", `{"SecretId":"training/http-matrix","ClientRequestToken":"` + secretMatrixTokenC + `","SecretString":"DUMMY-secret-refused","VersionStages":["AWSCURRENT","AWSPREVIOUS"]}`},
		{"move custom label", "UpdateSecretVersionStage", `{"SecretId":"training/http-matrix","VersionStage":"AWSPREVIOUS","MoveToVersionId":"` + secretMatrixTokenA + `"}`},
		{"remove empty token", "UpdateSecretVersionStage", `{"SecretId":"training/http-matrix","VersionStage":"AWSCURRENT","MoveToVersionId":"` + secretMatrixTokenA + `","RemoveFromVersionId":""}`},
		{"remove invalid token", "UpdateSecretVersionStage", `{"SecretId":"training/http-matrix","VersionStage":"AWSCURRENT","MoveToVersionId":"` + secretMatrixTokenA + `","RemoveFromVersionId":"short"}`},
		{"delete typed flag", "DeleteSecret", `{"SecretId":"training/http-matrix","ForceDeleteWithoutRecovery":"true"}`},
	}
	before := secretStateSnapshot(t, db)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			response := secretHTTPCall(s, fixture.op, fixture.body, allow)
			secretMatrixError(t, response, 400, "InvalidParameterException")
			if secretStateSnapshot(t, db) != before {
				t.Fatal("invalid selector mutated saved graph")
			}
		})
	}
	// A version ID and label select the same immutable bytes only when consistent.
	secretMatrixRead(t, s, created.ARN, secretMatrixTokenA, "AWSPREVIOUS", "DUMMY-secret-matrix-original")
	secretMatrixRead(t, s, "training/http-matrix", secretMatrixTokenB, "AWSCURRENT", "DUMMY-secret-matrix-current")
	mismatch := secretHTTPCall(s, "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": created.ARN, "VersionId": secretMatrixTokenA, "VersionStage": "AWSCURRENT"}), allow)
	secretMatrixError(t, mismatch, 400, "InvalidRequestException")
	if secretStateSnapshot(t, db) != before {
		t.Fatal("conflicting version selectors changed saved state")
	}
	repaired := secretHTTPCall(s, "UpdateSecretVersionStage", secretJSONFixture(t, map[string]any{"SecretId": created.ARN, "VersionStage": "AWSCURRENT", "MoveToVersionId": secretMatrixTokenA, "RemoveFromVersionId": secretMatrixTokenB}), allow)
	if repaired.Code != 200 {
		t.Fatal("valid stage repair failed after rejected input")
	}
	secretMatrixRead(t, s, created.ARN, "", "", "DUMMY-secret-matrix-original")
	secretMatrixRead(t, s, "training/http-peer", "", "", "DUMMY-secret-peer-original")
}

// Real Reader failure; no SQL state or fake adapter is installed.
type secretMatrixBodyFailure struct{}

func (secretMatrixBodyFailure) Read([]byte) (int, error) {
	return 0, errors.New("DUMMY-secret-body-read-failure")
}

func TestSecretHTTPTransportBoundariesAndReadFailurePreserveState(t *testing.T) {
	db, s, _ := secretMatrixSeed(t)
	input := `{"SecretId":"training/http-matrix"}`
	before := secretStateSnapshot(t, db)
	cases := []struct {
		name   string
		change func(*http.Request)
		region string
		status int
		code   string
	}{
		{"method", func(r *http.Request) { r.Method = "GET" }, "us-east-1", 400, "InvalidParameterException"},
		{"path", func(r *http.Request) { r.URL.Path = "/secret" }, "us-east-1", 400, "InvalidParameterException"},
		{"query", func(r *http.Request) { r.URL.RawQuery = "SecretId=training/http-matrix" }, "us-east-1", 400, "InvalidParameterException"},
		{"wrong media", func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, "us-east-1", 400, "InvalidParameterException"},
		{"malformed media", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-amz-json-1.1; charset=\"") }, "us-east-1", 400, "InvalidParameterException"},
		{"invalid region", func(*http.Request) {}, "no-such-region", 400, "InvalidParameterException"},
		{"service conflict", func(r *http.Request) { r.Header.Set("X-Simulator-Service", "kms") }, "us-east-1", 400, "InvalidParameterException"},
		{"session credentials", func(r *http.Request) { r.Header.Set("X-Amz-Security-Token", "dummy-session") }, "us-east-1", 400, "InvalidParameterException"},
		{"foreign credentials", func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=NOTLOCAL/20261104/us-east-1/secretsmanager/aws4_request, Signature=dummy")
		}, "us-east-1", 403, "AccessDeniedException"},
		{"read failure", func(r *http.Request) { r.Body = io.NopCloser(secretMatrixBodyFailure{}); r.ContentLength = -1 }, "us-east-1", 400, "InvalidParameterException"},
		{"oversized body", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(input + strings.Repeat(" ", (512<<10)+1-len(input))))
			r.ContentLength = -1
		}, "us-east-1", 400, "InvalidParameterException"},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(input))
			request.Header.Set("Content-Type", "application/x-amz-json-1.1")
			request.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
			fixture.change(request)
			calls := 0
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request, fixture.region, func(string, string) error { calls++; return nil })
			secretMatrixError(t, response, fixture.status, fixture.code)
			if calls != 0 || secretStateSnapshot(t, db) != before {
				t.Fatal("transport refusal reached authorization or changed durable state")
			}
		})
	}
	// Exactly512KiB is valid transport (only whitespace padding); one byte more
	// above was refused by the real MaxBytesReader, not a fake ContentLength gate.
	exact := secretHTTPCall(s, "GetSecretValue", input+strings.Repeat(" ", (512<<10)-len(input)), func(string, string) error { return nil })
	var value struct{ SecretString string }
	if exact.Code != 200 || json.Unmarshal(exact.Body.Bytes(), &value) != nil || value.SecretString != "DUMMY-secret-matrix-original" {
		t.Fatal("exact transport boundary failed or changed actual value")
	}
	if secretStateSnapshot(t, db) != before {
		t.Fatal("bounded read changed stored state")
	}
}

func TestSecretHTTPAuthorizationFailuresExposeNoMaterialAndAllowRepairedRetry(t *testing.T) {
	db, s, created := secretMatrixSeed(t)
	before := secretStateSnapshot(t, db)
	denied := func(action, resource string) error {
		if action != "secretsmanager:GetSecretValue" || resource != created.ARN {
			t.Fatal("authorization did not use persisted exact secret identity")
		}
		return ssmFailure{"AccessDeniedException", "No exercise grant"}
	}
	w := secretHTTPCall(s, "GetSecretValue", secretJSONFixture(t, map[string]any{"SecretId": created.ARN}), denied)
	secretMatrixError(t, w, 403, "AccessDeniedException")
	// A denied write must not reserve a version token or move labels. The exact
	// same request is retried successfully below after authority is repaired.
	putBody := secretJSONFixture(t, map[string]any{"SecretId": created.ARN, "ClientRequestToken": secretMatrixTokenB, "SecretString": "DUMMY-secret-repaired-value"})
	w = secretHTTPCall(s, "PutSecretValue", putBody, func(action, resource string) error {
		if action != "secretsmanager:PutSecretValue" || resource != created.ARN {
			t.Fatal("write authorization used wrong saved identity")
		}
		return ssmFailure{"AccessDeniedException", "No exercise write grant"}
	})
	secretMatrixError(t, w, 403, "AccessDeniedException")
	if secretStateSnapshot(t, db) != before {
		t.Fatal("denied native write reserved a token or changed saved versions/labels")
	}
	w = secretHTTPCall(s, "GetSecretValue", `{"SecretId":"training/http-matrix"}`, func(string, string) error { return errors.New("DUMMY-secret-sensitive-policy-failure") })
	secretMatrixError(t, w, 500, "InternalServiceError")
	if secretStateSnapshot(t, db) != before {
		t.Fatal("authority failure changed peer or version graph")
	}
	allow := func(string, string) error { return nil }
	// Retry exact failed native operation with working authority and actual bytes.
	secretMatrixRead(t, s, created.ARN, "", "", "DUMMY-secret-matrix-original")
	updated := secretHTTPCall(s, "PutSecretValue", putBody, allow)
	if updated.Code != 200 {
		t.Fatal("repaired native write failed")
	}
	secretMatrixRead(t, s, created.ARN, "", "", "DUMMY-secret-repaired-value")
	secretMatrixRead(t, s, "training/http-peer", "", "", "DUMMY-secret-peer-original")
	ids := core.NewIdentities(db)
	for _, cfg := range []struct {
		name       string
		store      *SecretStore
		identities *core.Identities
	}{{"no store", nil, ids}, {"no identities", s, nil}, {"both missing", nil, nil}} {
		t.Run(cfg.name, func(t *testing.T) {
			snapshot := secretStateSnapshot(t, db)
			request := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/x-amz-json-1.1")
			request.Header.Set("X-Amz-Target", "secretsmanager.ListSecrets")
			response := httptest.NewRecorder()
			serveSecretGateway(response, request, cfg.store, cfg.identities)
			secretMatrixError(t, response, 500, "InternalServiceError")
			if secretStateSnapshot(t, db) != snapshot {
				t.Fatal("unconfigured gateway mutated native secrets")
			}
		})
	}
}
