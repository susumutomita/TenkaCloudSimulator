package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestLambdaCapabilityUsesSavedRoleAndNativeState(t *testing.T) {
	db, s := s3TestState(t)
	parameters, err := NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	identities := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:GetParameter","ssm:GetParameters"],"Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/own/*"},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject"],"Resource":["arn:aws:s3:::versions-test/*","arn:aws:s3:::2015-03-31/*"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("executor", policy); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"/own/value": "safe-value", "/private/value": "private-dummy"} {
		if _, err = parameters.Put("us-east-1", name, "String", value, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Create("2015-03-31", "us-east-1"); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, S3: s, Parameters: parameters, Identities: identities}
	invoke := gateway.lambdaExchange(LambdaFunction{Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor"}, "us-east-1")
	call := func(service, op string, args map[string]any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"service": service, "operation": op, "parameters": args})
		data, err := invoke(context.Background(), body)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if json.Unmarshal(data, &result) != nil {
			t.Fatal(string(data))
		}
		return result
	}
	got := call("ssm", "GetParameter", map[string]any{"Name": "/own/value"})
	if !strings.Contains(string(mustBridgeJSON(t, got)), "safe-value") {
		t.Fatal(got)
	}
	denied := call("ssm", "GetParameter", map[string]any{"Name": "/private/value"})
	if denied["error"].(map[string]any)["status"] != float64(403) || strings.Contains(string(mustBridgeJSON(t, denied)), "private-dummy") {
		t.Fatal(denied)
	}
	for _, key := range []string{"incident.json", "nested/../literal.txt", "functions/other/configuration"} {
		bucket := "versions-test"
		if strings.HasPrefix(key, "functions/") {
			bucket = "2015-03-31"
		}
		put := call("s3", "PutObject", map[string]any{"Bucket": bucket, "Key": key, "Body": base64.StdEncoding.EncodeToString([]byte("from-guest")), "ContentType": "text/plain"})
		if put["error"] != nil {
			t.Fatal(put)
		}
		native, err := s.Get(bucket, key, "")
		if err != nil || string(native.Data) != "from-guest" {
			t.Fatal(native, err)
		}
		get := call("s3", "GetObject", map[string]any{"Bucket": bucket, "Key": key})
		if get["error"] != nil || get["result"].(map[string]any)["Body"] != base64.StdEncoding.EncodeToString([]byte("from-guest")) {
			t.Fatal(get)
		}
		if deleted := call("s3", "DeleteObject", map[string]any{"Bucket": bucket, "Key": key}); deleted["error"] != nil {
			t.Fatal(deleted)
		}
		if _, err = s.Get(bucket, key, ""); core.Code(err) != "NoSuchKey" {
			t.Fatal(err)
		}
	}
	for _, args := range []map[string]any{{"Bucket": "other-bucket", "Key": "private"}, {"Bucket": "versions-test", "Key": "x", "principal": "simulator-admin"}} {
		if got = call("s3", "GetObject", args); got["error"] == nil {
			t.Fatal(got)
		}
	}
	multiple := call("ssm", "GetParameters", map[string]any{"Names": []string{"/own/value"}})
	if multiple["error"] != nil {
		t.Fatal(multiple)
	}
	names := []string{"/own/escaped-a", "/own/escaped-b", "/own/escaped-c"}
	for _, name := range names {
		if _, err := parameters.Put("us-east-1", name, "String", strings.Repeat("\x01", 4096), nil, false); err != nil {
			t.Fatal(err)
		}
	}
	largeSSM := call("ssm", "GetParameters", map[string]any{"Names": names})
	failure := largeSSM["error"].(map[string]any)
	if failure["code"] != "EntityTooLarge" || !strings.Contains(failure["message"].(string), "48 KiB") || largeSSM["result"] != nil {
		t.Fatal("oversized encoded SSM values did not produce the documented transport error", largeSSM)
	}
	deniedMultiple := call("ssm", "GetParameters", map[string]any{"Names": []string{"/own/value", "/private/value"}})
	if deniedMultiple["error"] == nil || strings.Contains(string(mustBridgeJSON(t, deniedMultiple)), "safe-value") {
		t.Fatal(deniedMultiple)
	}
	for _, size := range []int{40 << 10, 64 << 10} {
		if _, err = s.Put("versions-test", "large.bin", "application/octet-stream", []byte(strings.Repeat("x", size)), "", ""); err != nil {
			t.Fatal(err)
		}
		result := call("s3", "GetObject", map[string]any{"Bucket": "versions-test", "Key": "large.bin"})
		if result["error"].(map[string]any)["code"] != "EntityTooLarge" {
			t.Fatal(result)
		}
	}
	oversized := call("s3", "PutObject", map[string]any{"Bucket": "versions-test", "Key": "new.bin", "Body": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", (32<<10)+1)))})
	if oversized["error"] == nil {
		t.Fatal("oversized SDK write succeeded")
	}
	if _, err = s.Get("versions-test", "new.bin", ""); core.Code(err) != "NoSuchKey" {
		t.Fatal("rejected write left data", err)
	}
	for _, versioned := range []bool{false, true} {
		if versioned {
			s3Enable(t, s, "Enabled")
		}
		if _, err = s.Put("versions-test", "empty-version.txt", "text/plain", []byte("must remain"), "", ""); err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"GetObject", "DeleteObject"} {
			rejected := call("s3", op, map[string]any{"Bucket": "versions-test", "Key": "empty-version.txt", "VersionId": ""})
			if rejected["error"] == nil {
				t.Fatal("empty explicit selector accepted", op, rejected)
			}
			stored, err := s.Get("versions-test", "empty-version.txt", "")
			if err != nil || string(stored.Data) != "must remain" {
				t.Fatal("invalid selector mutated current object", err)
			}
		}
	}
	s3Enable(t, s, "Enabled")
	putVersion := call("s3", "PutObject", map[string]any{"Bucket": "versions-test", "Key": "version.txt", "Body": base64.StdEncoding.EncodeToString([]byte("retained"))})
	version := putVersion["result"].(map[string]any)["VersionId"]
	marker := call("s3", "DeleteObject", map[string]any{"Bucket": "versions-test", "Key": "version.txt"})
	if marker["result"].(map[string]any)["DeleteMarker"] != true {
		t.Fatal(marker)
	}
	deniedVersion := call("s3", "DeleteObject", map[string]any{"Bucket": "versions-test", "Key": "version.txt", "VersionId": version})
	if deniedVersion["error"].(map[string]any)["status"] != float64(403) {
		t.Fatal("DeleteObject granted DeleteObjectVersion", deniedVersion)
	}
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Allow", Action: []string{"s3:GetObjectVersion"}, Resource: []string{"arn:aws:s3:::versions-test/*"}})
	if err = identities.Set("executor", policy); err != nil {
		t.Fatal(err)
	}
	readVersion := call("s3", "GetObject", map[string]any{"Bucket": "versions-test", "Key": "version.txt", "VersionId": version})
	if readVersion["result"].(map[string]any)["Body"] != base64.StdEncoding.EncodeToString([]byte("retained")) {
		t.Fatal(readVersion)
	}
	unavailable := (&Gateway{}).lambdaExchange(LambdaFunction{Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor"}, "us-east-1")
	unavailableResponse, err := unavailable(context.Background(), json.RawMessage(`{"service":"ssm","operation":"GetParameter","parameters":{"Name":"/own/value"}}`))
	if err != nil || !strings.Contains(string(unavailableResponse), "ServiceException") {
		t.Fatal(string(unavailableResponse), err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = invoke(canceled, json.RawMessage(`{}`)); err != context.Canceled {
		t.Fatal(err)
	}

	for _, body := range []string{`null`, `[]`, `{`, `{"service":"ssm","operation":"GetParameter","parameters":null}`, `{"service":"ssm","operation":"GetParameter","parameters":{},"principal":"simulator-admin"}`, `{"service":"ssm","operation":"GetParameter","region":"eu-west-1","parameters":{}}`, `{"service":"iam","operation":"CreateRole","parameters":{}}`, `{"service":"s3","operation":"PutObject","parameters":{"Bucket":"versions-test","Key":"x","Body":"invalid-base64"}}`} {
		reply, err := invoke(context.Background(), json.RawMessage(body))
		if err != nil || !strings.Contains(string(reply), `"error"`) {
			t.Fatal(body, string(reply), err)
		}
	}
	for _, function := range []LambdaFunction{{Region: "us-east-1"}, {Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/simulator-admin"}, {Region: "eu-west-1", Role: "arn:aws:iam::000000000000:role/executor"}} {
		reply, err := gateway.lambdaExchange(function, "us-east-1")(context.Background(), json.RawMessage(`{}`))
		if err != nil || !strings.Contains(string(reply), "AccessDeniedException") {
			t.Fatal(string(reply), err)
		}
	}
}
func mustBridgeJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
