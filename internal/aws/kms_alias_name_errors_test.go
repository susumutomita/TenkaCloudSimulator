package aws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// CreateAlias explicitly documents InvalidAliasNameException. UpdateAlias and
// DeleteAlias do not document that exception: keep their current bounded errors.
// Source: AWS KMS API references, reviewed 2026-11-05; no live-AWS comparison.
func TestKMSCreateAliasNameErrorsAreTypedAndPreserveNativeFrame(t *testing.T) {
	keys, _, key, _ := kmsAliasFixture(t)
	before := kmsAliasFrame(t, keys)
	for _, name := range []string{"alias/", "MissingPrefix", "alias/aws/s3", "alias/with space", "alias/日本語", "alias/" + strings.Repeat("a", 251)} {
		body, err := json.Marshal(map[string]string{"AliasName": name, "TargetKeyId": key.KeyID})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "TrentService.CreateAlias")
		w := httptest.NewRecorder()
		keys.ServeHTTP(w, r, "us-east-1", func(string, string) bool { t.Fatal("Invalid alias reached authorization"); return false })
		var failure map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || failure["__type"] != "InvalidAliasNameException" || w.Header().Get("X-Amzn-Errortype") != "InvalidAliasNameException" {
			t.Fatalf("Wrong invalid alias response: status=%d code=%q", w.Code, failure["__type"])
		}
		if strings.Contains(w.Body.String(), name) {
			t.Fatal("Error echoed requested alias")
		}
		kmsAliasUnchanged(t, keys, before)
	}
}

func TestKMSAliasWireErrorsRemainGenericAndValidNameStillSucceeds(t *testing.T) {
	keys, s, key, _ := kmsAliasFixture(t)
	before := kmsAliasFrame(t, keys)
	cases := []struct{ op, body string }{
		{"CreateAlias", `{"TargetKeyId":"` + key.KeyID + `"}`},
		{"CreateAlias", `{"AliasName":"","TargetKeyId":"` + key.KeyID + `"}`},
		{"CreateAlias", `{"AliasName":null,"TargetKeyId":"` + key.KeyID + `"}`},
		{"CreateAlias", `{"AliasName":false,"TargetKeyId":"` + key.KeyID + `"}`},
		{"CreateAlias", `{"AliasName":1,"TargetKeyId":"` + key.KeyID + `"}`},
		{"CreateAlias", `{"AliasName":"alias/one","AliasName":"alias/two","TargetKeyId":"` + key.KeyID + `"}`},
		{"CreateAlias", `{"AliasName":"alias/ok","TargetKeyId":null}`},
		{"CreateAlias", `{"AliasName":"alias/ok","TargetKeyId":"` + key.KeyID + `","Unknown":true}`},
		{"CreateAlias", `{"AliasName":"alias/ok","TargetKeyId":"` + key.KeyID + `"} {}`},
		{"UpdateAlias", `{"AliasName":"alias/with space","TargetKeyId":"` + key.KeyID + `"}`},
		{"DeleteAlias", `{"AliasName":"alias/with space"}`},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "TrentService."+c.op)
		w := httptest.NewRecorder()
		keys.ServeHTTP(w, r, "us-east-1", func(string, string) bool { return true })
		var failure map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || failure["__type"] != "ValidationException" {
			t.Fatalf("%s wrong strict body/name error status=%d code=%q", c.op, w.Code, failure["__type"])
		}
		kmsAliasUnchanged(t, keys, before)
	}
	// The maximum supported suffix remains accepted; the error change must not
	// reject valid boundary names or change the empty successful native body.
	name := "alias/" + strings.Repeat("a", 250)
	body, err := json.Marshal(map[string]string{"AliasName": name, "TargetKeyId": key.KeyID})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "TrentService.CreateAlias")
	w := httptest.NewRecorder()
	keys.ServeHTTP(w, r, "us-east-1", func(string, string) bool { return true })
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("Valid alias no longer returns empty native success")
	}
	result, err := s.ListAliases(r.Context(), key.KeyID, 100, "", kmsAliasAllow)
	if err != nil || len(result.Aliases) != 1 || result.Aliases[0].AliasName != name {
		t.Fatal("Valid alias was not persisted")
	}
	if err = s.DeleteAlias(r.Context(), name, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
}
