package aws

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http/httptest"
	"strings"
	"testing"
)

func kmsHTTP(t *testing.T, g *Gateway, op, body, principal string) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "TrentService."+op)
	r.Header.Set("X-Simulator-Principal", principal)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	result := map[string]json.RawMessage{}
	if w.Body.Len() > 0 && json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("nonJSON response", w.Body.String())
	}
	return w, result
}
func TestKMSNativeHTTPRoundtripDefaultPolicyAndIAM(t *testing.T) {
	db, s := kmsTestStore(t, "")
	ids := core.NewIdentities(db)
	g := &Gateway{KMS: s, Identities: ids}
	w, result := kmsHTTP(t, g, "CreateKey", `{"Description":"Safe demo"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var key KMSKeyMetadata
	if err := json.Unmarshal(result["KeyMetadata"], &key); err != nil {
		t.Fatal(err)
	}
	if err := ids.Set("kms-reader", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"kms:DescribeKey", "kms:Encrypt", "kms:Decrypt", "kms:GetKeyPolicy"}, Resource: core.StringList{key.Arn}}}}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"DescribeKey", "GetKeyPolicy"} {
		w, _ = kmsHTTP(t, g, op, `{"KeyId":"`+key.KeyID+`"}`, "kms-reader")
		if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	w, result = kmsHTTP(t, g, "GetKeyPolicy", `{"KeyId":"`+key.KeyID+`","PolicyName":"default"}`, "kms-reader")
	var policy string
	_ = json.Unmarshal(result["Policy"], &policy)
	if w.Code != 200 || policy != kmsDefaultPolicy || string(result["PolicyName"]) != `"default"` {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, op := range []string{"CreateKey", "ListKeys", "DisableKey", "EnableKey"} {
		body := `{}`
		if strings.HasSuffix(op, "Key") && op != "CreateKey" {
			body = `{"KeyId":"` + key.KeyID + `"}`
		}
		w, _ = kmsHTTP(t, g, op, body, "kms-reader")
		if w.Code != 403 {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	w, result = kmsHTTP(t, g, "Encrypt", `{"KeyId":"`+key.Arn+`","Plaintext":"aGVsbG8=","EncryptionContext":{"team":"A"}}`, "kms-reader")
	if w.Code != 200 || string(result["KeyId"]) != `"`+key.Arn+`"` {
		t.Fatal(w.Code, w.Body.String())
	}
	ciphertext := string(result["CiphertextBlob"])
	w, result = kmsHTTP(t, g, "Decrypt", `{"CiphertextBlob":`+ciphertext+`,"EncryptionContext":{"team":"A"}}`, "kms-reader")
	if w.Code != 200 || string(result["Plaintext"]) != `"aGVsbG8="` {
		t.Fatal(w.Code, w.Body.String())
	}
	other, err := s.CreateKey(context.Background(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	w, _ = kmsHTTP(t, g, "Decrypt", `{"KeyId":"`+other.KeyID+`","CiphertextBlob":`+ciphertext+`}`, "kms-reader")
	if w.Code != 403 {
		t.Fatal("wrong selected key was read before IAM", w.Code, w.Body.String())
	}
	if err = ids.Set("kms-deny", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"kms:*"}, Resource: core.StringList{"*"}}, {Effect: "Deny", Action: core.StringList{"kms:Decrypt"}, Resource: core.StringList{key.Arn}}}}); err != nil {
		t.Fatal(err)
	}
	w, _ = kmsHTTP(t, g, "Decrypt", `{"CiphertextBlob":`+ciphertext+`,"EncryptionContext":{"team":"A"}}`, "kms-deny")
	if w.Code != 403 || w.Header().Get("X-Simulator-IAM-Decision") != "explicitDeny" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	for _, op := range []string{"DisableKey", "EnableKey"} {
		w, _ = kmsHTTP(t, g, op, `{"KeyId":"`+key.KeyID+`"}`, "")
		if w.Code != 200 || w.Body.Len() != 0 {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	w, _ = kmsHTTP(t, g, "DescribeKey", `{"KeyId":"`+key.KeyID+`"}`, "missing")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestKMSStrictNativeFieldsAndNoSecretEcho(t *testing.T) {
	_, s := kmsTestStore(t, "")
	g := &Gateway{KMS: s}
	key, err := s.CreateKey(context.Background(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ op, body string }{
		{"CreateKey", `{"Policy":"private-value"}`}, {"CreateKey", `{"Tags":[]}`}, {"CreateKey", `{"KeySpec":"RSA_2048"}`}, {"CreateKey", `{"Origin":"EXTERNAL"}`}, {"CreateKey", `{"KeyUsage":"SIGN_VERIFY"}`}, {"CreateKey", `{"MultiRegion":true}`}, {"CreateKey", `{"Description":null}`}, {"CreateKey", `{"Description":"a","Description":"b"}`}, {"CreateKey", `{"description":"lowercase"}`}, {"CreateKey", `{"Description":"\ud800"}`}, {"CreateKey", `{"Description":"\udc00"}`}, {"CreateKey", `{"Description":"\ud800\u0041"}`}, {"CreateKey", `{"Description":"\uZZZZ"}`}, {"CreateKey", `[]`}, {"CreateKey", `null`}, {"CreateKey", `{} {}`}, {"CreateKey", `{"MultiRegion":1}`}, {"CreateKey", `{"KeySpec":""}`}, {"CreateKey", `{"Description":` + strings.Repeat("[", 10) + `0` + strings.Repeat("]", 10) + `}`},
		{"ListKeys", `{"Limit":0}`}, {"ListKeys", `{"Limit":1001}`}, {"ListKeys", `{"Limit":1.5}`}, {"ListKeys", `{"Marker":""}`}, {"ListKeys", `{"Marker":"bad"}`}, {"ListKeys", `{"GrantTokens":[]}`},
		{"DescribeKey", `{}`}, {"GetKeyPolicy", `{"KeyId":"` + key.KeyID + `","PolicyName":"other"}`},
		{"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":[1,2]}`}, {"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":"invalid base64"}`}, {"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":"eA==","DryRun":false}`}, {"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":"eA==","EncryptionAlgorithm":"RSAES_OAEP_SHA_256"}`}, {"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":"eA==","EncryptionContext":{"a":"b","a":"c"}}`}, {"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":"eA==","EncryptionContext":{"a":1}}`}, {"Encrypt", `{"KeyId":"` + key.KeyID + `","Plaintext":"eA==","EncryptionContext":{"":"b"}}`},
		{"Decrypt", `{"CiphertextBlob":"eA=="}`}, {"Decrypt", `{"CiphertextBlob":""}`},
	} {
		w, _ := kmsHTTP(t, g, test.op, test.body, "")
		if w.Code != 400 || strings.Contains(w.Body.String(), "private-value") {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"Description":"\ud83d\ude00"}`, `{"Description":"日本語"}`, `{"Description":"literal \\ud800"}`} {
		w, _ := kmsHTTP(t, g, "CreateKey", body, "")
		if w.Code != 200 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	w, _ := kmsHTTP(t, g, "RotateKey", `{}`, "")
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	w, _ = kmsHTTP(t, &Gateway{}, "ListKeys", `{}`, "")
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	w, _ = kmsHTTP(t, g, "CreateKey", `{"Description":"`+strings.Repeat("x", 65<<10)+`"}`, "")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w, _ = kmsHTTP(t, g, "CreateKey", string([]byte{'{', '"', 'D', 'e', 's', 'c', 'r', 'i', 'p', 't', 'i', 'o', 'n', '"', ':', '"', 255, '"', '}'}), "")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	// nil authorization fails closed even when the fixed default key policy exists.
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "TrentService.ListKeys")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r, "us-east-1", nil)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestKMSTransportRegionServiceAndCredentialBoundaries(t *testing.T) {
	_, s := kmsTestStore(t, "")
	g := &Gateway{KMS: s}
	for _, test := range []struct {
		method, path, service, region, auth, session, contentType string
		status                                                    int
	}{
		{"POST", "/", "kms", "", "", "", "application/x-amz-json-1.1", 200},
		{"POST", "/", "", "", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-west-2/kms/aws4_request, SignedHeaders=host, Signature=dummy", "", "application/x-amz-json-1.1", 200},
		{"GET", "/", "kms", "", "", "", "application/x-amz-json-1.1", 400}, {"POST", "/wrong", "kms", "", "", "", "application/x-amz-json-1.1", 400}, {"POST", "/?X-Amz-Credential=dummy", "kms", "", "", "", "application/x-amz-json-1.1", 400}, {"POST", "/", "ssm", "", "", "", "application/x-amz-json-1.1", 400}, {"POST", "/", "kms", "bad", "", "", "application/x-amz-json-1.1", 400}, {"POST", "/", "kms", "", "", "session", "application/x-amz-json-1.1", 400}, {"POST", "/", "kms", "", "", "", "application/json", 400},
		{"POST", "/", "kms", "", "AWS4-HMAC-SHA256 Credential=REALKEY/date/us-east-1/kms/aws4_request, SignedHeaders=host", "", "application/x-amz-json-1.1", 403}, {"POST", "/", "kms", "us-east-1", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-west-2/kms/aws4_request, SignedHeaders=host", "", "application/x-amz-json-1.1", 400}, {"POST", "/", "kms", "", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/ssm/aws4_request, SignedHeaders=host", "", "application/x-amz-json-1.1", 403},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`))
		r.Header.Set("X-Amz-Target", "TrentService.ListKeys")
		r.Header.Set("X-Simulator-Service", test.service)
		r.Header.Set("X-Simulator-Region", test.region)
		r.Header.Set("Authorization", test.auth)
		r.Header.Set("X-Amz-Security-Token", test.session)
		r.Header.Set("Content-Type", test.contentType)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}
func TestKMSNativeBinaryEncoding(t *testing.T) {
	_, s := kmsTestStore(t, "")
	g := &Gateway{KMS: s}
	key, err := s.CreateKey(context.Background(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte{0, 255, 128, 1}
	input, _ := json.Marshal(map[string]any{"KeyId": key.KeyID, "Plaintext": plaintext})
	w, result := kmsHTTP(t, g, "Encrypt", string(input), "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body, _ := json.Marshal(map[string]json.RawMessage{"CiphertextBlob": result["CiphertextBlob"]})
	w, result = kmsHTTP(t, g, "Decrypt", string(body), "")
	var encoded string
	_ = json.Unmarshal(result["Plaintext"], &encoded)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if w.Code != 200 || err != nil || !bytes.Equal(decoded, plaintext) {
		t.Fatal(w.Code, err)
	}
}
