package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

func TestKMSRegularServerUsesLocalAccessAndPersistsRegionalKeys(t *testing.T) {
	state := filepath.Join(t.TempDir(), "kms.sqlite")
	start := func(path, token string) (*httptest.Server, func()) {
		handler, closeDB, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		access, err := NewAccess(handler, token, false)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(access)
		return srv, func() {
			srv.Close()
			if err := closeDB(); err != nil {
				t.Error(err)
			}
		}
	}
	token := "KMS_TEST_OPERATOR_A_12345678901234567890"
	peerToken := "KMS_TEST_OPERATOR_B_12345678901234567890"
	srv, stop := start(state, token)
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	call := func(target *httptest.Server, path, method, credential, op string, input any, want int) map[string]json.RawMessage {
		t.Helper()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequest(method, target.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Simulator-Token", credential)
		if op != "" {
			r.Header.Set("Content-Type", "application/x-amz-json-1.1")
			r.Header.Set("X-Amz-Target", "TrentService."+op)
		} else {
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := target.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s %s got%d want%d: %s", method, path, op, response.StatusCode, want, body)
		}
		result := map[string]json.RawMessage{}
		if len(body) > 0 {
			if err = json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	call(srv, "/", "POST", "", "CreateKey", map[string]any{}, 401)
	created := call(srv, "/", "POST", token, "CreateKey", map[string]any{"Description": "dummy persisted key"}, 200)
	metadata := map[string]json.RawMessage{}
	if err := json.Unmarshal(created["KeyMetadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	var id, arn string
	json.Unmarshal(metadata["KeyId"], &id)
	json.Unmarshal(metadata["Arn"], &arn)
	viaAlias := call(srv, "/_simulator/providers/aws/", "POST", token, "DescribeKey", map[string]any{"KeyId": id}, 200)
	aliasMetadata := map[string]json.RawMessage{}
	if err := json.Unmarshal(viaAlias["KeyMetadata"], &aliasMetadata); err != nil {
		t.Fatal(err)
	}
	if string(aliasMetadata["Arn"]) != string(metadata["Arn"]) {
		t.Fatal("provider alias read different saved key")
	}
	caps := call(srv, "/_simulator/v1/capabilities", "GET", token, "", nil, 200)
	services := map[string]json.RawMessage{}
	if err := json.Unmarshal(caps["services"], &services); err != nil {
		t.Fatal(err)
	}
	var kms struct {
		Operations  []string
		Unsupported []string
	}
	if err := json.Unmarshal(services["kms"], &kms); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kms.Operations, []string{"CreateKey", "DescribeKey", "ListKeys", "EnableKey", "DisableKey", "GetKeyPolicy", "Encrypt", "Decrypt", "CreateAlias", "ListAliases", "UpdateAlias", "DeleteAlias"}) {
		t.Fatal("capabilities advertised incorrect operation set", kms.Operations)
	}
	// Real default-server alias operations share this existing key/DB. No
	// additional key or crypto counter changes disturb the original assertions.
	call(srv, "/", "POST", token, "CreateAlias", map[string]any{"AliasName": "alias/server-check", "TargetKeyId": arn}, 200)
	aliasPage := call(srv, "/_simulator/providers/aws/", "POST", token, "ListAliases", map[string]any{"KeyId": id}, 200)
	var aliases []struct{ AliasName, AliasArn, TargetKeyId string }
	if err := json.Unmarshal(aliasPage["Aliases"], &aliases); err != nil || len(aliases) != 1 || aliases[0].AliasName != "alias/server-check" || aliases[0].AliasArn != "arn:aws:kms:us-east-1:000000000000:alias/server-check" || aliases[0].TargetKeyId != id {
		t.Fatal("regular server alias did not use the existing key", err)
	}
	call(srv, "/", "POST", token, "UpdateAlias", map[string]any{"AliasName": "alias/server-check", "TargetKeyId": id}, 200)
	call(srv, "/_simulator/providers/aws/", "POST", token, "DeleteAlias", map[string]any{"AliasName": "alias/server-check"}, 200)
	for _, service := range []string{"s3", "ssm", "sqs", "iam", "lambda", "cloudformation", "apigateway"} {
		if len(services[service]) == 0 {
			t.Fatal("existing service disappeared", service)
		}
	}
	policy := map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"kms:DescribeKey"}, "Resource": arn}}}
	call(srv, "/_simulator/v1/iam/principals/kms-reader/policy", "PUT", token, "", policy, 200)
	session := call(srv, "/_simulator/v1/access", "POST", token, "", map[string]any{"Principal": "kms-reader"}, 200)
	var participant string
	if err := json.Unmarshal(session["AccessToken"], &participant); err != nil {
		t.Fatal(err)
	}
	call(srv, "/", "POST", participant, "DescribeKey", map[string]any{"KeyId": id}, 200)
	call(srv, "/", "POST", participant, "DisableKey", map[string]any{"KeyId": id}, 403)
	call(srv, "/", "POST", participant, "ListKeys", map[string]any{}, 403)
	peer, peerStop := start(filepath.Join(t.TempDir(), "peer.sqlite"), peerToken)
	defer peerStop()
	call(peer, "/", "POST", token, "DescribeKey", map[string]any{"KeyId": id}, 401)
	call(peer, "/", "POST", peerToken, "DescribeKey", map[string]any{"KeyId": id}, 400)
	stop()
	stop = nil
	srv, stop = start(state, token)
	resumed := call(srv, "/", "POST", token, "DescribeKey", map[string]any{"KeyId": id}, 200)
	var saved struct{ Description, KeyState string }
	if err := json.Unmarshal(resumed["KeyMetadata"], &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Description != "dummy persisted key" || saved.KeyState != "Enabled" {
		t.Fatal("restart or denied mutation changed saved key")
	}
}
