package server

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func lambdaZIP(t *testing.T, source string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("index.py")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(source))
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func lambdaCall(t *testing.T, s *httptest.Server, method, path string, input any, want int, extra map[string]string) map[string]any {
	t.Helper()
	body := ""
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = string(data)
	}
	headers := map[string]string{"X-Simulator-Service": "lambda", "Content-Type": "application/json"}
	for k, v := range extra {
		headers[k] = v
	}
	data := status(t, s, method, path, body, want, headers)
	var result map[string]any
	if len(data) > 0 && json.Unmarshal(data, &result) != nil {
		t.Fatalf("invalid Lambda JSON: %s", data)
	}
	return result
}
func lambdaInput(t *testing.T, name, source string) map[string]any {
	return map[string]any{"FunctionName": name, "Runtime": "python3.12", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/executor", "Timeout": 5, "Code": map[string]any{"ZipFile": lambdaZIP(t, source)}, "Environment": map[string]any{"Variables": map[string]string{"EXAMPLE_VALUE": "local state"}}}
}
func lambdaRole(t *testing.T, s *httptest.Server) {
	t.Helper()
	status(t, s, "PUT", "/_simulator/v1/iam/principals/executor/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"*"}]}`, 200, nil)
}

const functionRoot = "/2015-03-31/functions"

func TestLambdaNativeStateIAMAndActualExecution(t *testing.T) {
	s := instance(t)
	lambdaRole(t, s)
	source := "import os, sqlite3\ndef handler(event, context):\n    db=sqlite3.connect(':memory:')\n    return {'answer': db.execute('select ? * 6', (event['value'],)).fetchone()[0], 'env': os.environ['EXAMPLE_VALUE']}\n"
	input := lambdaInput(t, "greeting", source)
	created := lambdaCall(t, s, "POST", functionRoot, input, 201, nil)
	arn := created["FunctionArn"].(string)
	if created["Runtime"] != "python3.12" || created["State"] != "Active" || created["MemorySize"] != float64(128) {
		t.Fatal(created)
	}
	lambdaCall(t, s, "POST", functionRoot, input, 409, nil)
	config := lambdaCall(t, s, "GET", functionRoot+"/greeting/configuration", nil, 200, nil)
	if config["RevisionId"] != created["RevisionId"] {
		t.Fatal(config)
	}
	result := lambdaCall(t, s, "POST", functionRoot+"/greeting/invocations", map[string]any{"value": 7}, 200, nil)
	if result["answer"] != float64(42) || result["env"] != "local state" {
		t.Fatal(result)
	}
	for _, name := range []string{"alpha", "zulu"} {
		lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, name, "def handler(event, context): return {}"), 201, nil)
	}
	list := lambdaCall(t, s, "GET", functionRoot+"/?MaxItems=2", nil, 200, nil)
	if len(list["Functions"].([]any)) != 2 || list["NextMarker"] != "greeting" {
		t.Fatal(list)
	}
	next := lambdaCall(t, s, "GET", functionRoot+"/?Marker=greeting", nil, 200, nil)
	if len(next["Functions"].([]any)) != 1 {
		t.Fatal(next)
	}
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":%q}]}`, arn)
	status(t, s, "PUT", "/_simulator/v1/iam/principals/player/policy", policy, 200, nil)
	player := map[string]string{"X-Simulator-Principal": "player"}
	lambdaCall(t, s, "POST", functionRoot+"/greeting/invocations", map[string]int{"value": 8}, 200, player)
	lambdaCall(t, s, "POST", functionRoot+"/alpha/invocations", map[string]any{}, 403, player)
	lambdaCall(t, s, "GET", functionRoot+"/greeting/configuration", nil, 403, player)
	lambdaCall(t, s, "GET", functionRoot, nil, 403, player)
	lambdaCall(t, s, "DELETE", functionRoot+"/greeting", nil, 403, player)
	lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, "denied", source), 403, player)
	deny := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:*","Resource":"*"},{"Effect":"Deny","Action":"lambda:InvokeFunction","Resource":%q}]}`, arn)
	status(t, s, "PUT", "/_simulator/v1/iam/principals/denied/policy", deny, 200, nil)
	lambdaCall(t, s, "POST", functionRoot+"/greeting/invocations", map[string]int{"value": 1}, 403, map[string]string{"X-Simulator-Principal": "denied"})
	update := map[string]any{"ZipFile": lambdaZIP(t, "def handler(event, context): return {'changed': event['value']}"), "RevisionId": created["RevisionId"]}
	changed := lambdaCall(t, s, "PUT", functionRoot+"/greeting/code", update, 200, nil)
	if changed["RevisionId"] == created["RevisionId"] || changed["Role"] != created["Role"] {
		t.Fatal(changed)
	}
	lambdaCall(t, s, "PUT", functionRoot+"/greeting/code", update, 412, nil)
	lambdaCall(t, s, "PUT", functionRoot+"/greeting/code", update, 403, player)
	result = lambdaCall(t, s, "POST", functionRoot+"/greeting/invocations?Qualifier=$LATEST", map[string]int{"value": 9}, 200, player)
	if result["changed"] != float64(9) {
		t.Fatal(result)
	}
	tokyo := map[string]string{"X-Simulator-Region": "ap-northeast-1"}
	lambdaCall(t, s, "GET", functionRoot+"/greeting/configuration", nil, 404, tokyo)
	lambdaCall(t, s, "POST", functionRoot, input, 201, tokyo)
	lambdaCall(t, s, "POST", functionRoot+"/greeting/invocations", nil, 204, map[string]string{"X-Amz-Invocation-Type": "DryRun"})
	lambdaCall(t, s, "DELETE", functionRoot+"/greeting", nil, 204, nil)
	lambdaCall(t, s, "GET", functionRoot+"/greeting/configuration", nil, 404, nil)
	lambdaCall(t, s, "DELETE", functionRoot+"/greeting", nil, 404, nil)
	lambdaCall(t, s, "GET", functionRoot+"/greeting/configuration", nil, 200, tokyo)
	input = lambdaInput(t, "broken", "def handler(event, context): raise ValueError('real Python exception')")
	lambdaCall(t, s, "POST", functionRoot, input, 201, nil)
	headers := map[string]string{"X-Simulator-Service": "lambda"}
	response, data := call(t, s, "POST", functionRoot+"/broken/invocations", "{}", headers)
	if response.StatusCode != 200 || response.Header.Get("X-Amz-Function-Error") != "Unhandled" || !bytes.Contains(data, []byte("ValueError")) {
		t.Fatalf("%d %s %s", response.StatusCode, response.Header.Get("X-Amz-Function-Error"), data)
	}
}

func TestLambdaValidationAndServiceDispatch(t *testing.T) {
	s := instance(t)
	lambdaRole(t, s)
	source := "def handler(event, context): return {}"
	for _, change := range []func(map[string]any){func(m map[string]any) { m["FunctionName"] = "bad/name" }, func(m map[string]any) { m["Runtime"] = "nodejs24.x" }, func(m map[string]any) { m["Role"] = "arn:aws:iam::123456789012:role/executor" }, func(m map[string]any) { m["Role"] = "arn:aws:iam::000000000000:role/missing" }, func(m map[string]any) { m["MemorySize"] = 256 }, func(m map[string]any) { m["Timeout"] = 0 }, func(m map[string]any) { m["Timeout"] = 31 }, func(m map[string]any) { m["PackageType"] = "Image" }, func(m map[string]any) { m["Code"] = map[string]string{"S3Bucket": "not-fetched"} }, func(m map[string]any) { m["Code"] = map[string]string{"ZipFile": "not-base64"} }, func(m map[string]any) { m["Description"] = strings.Repeat("x", 257) }, func(m map[string]any) {
		m["Environment"] = map[string]any{"Variables": map[string]string{"BAD=KEY": "x"}}
	}, func(m map[string]any) {
		m["Environment"] = map[string]any{"Variables": map[string]string{"TOO_LONG": strings.Repeat("x", 4097)}}
	}, func(m map[string]any) { m["VpcConfig"] = map[string]any{} }, func(m map[string]any) { m["Description"] = nil }} {
		input := lambdaInput(t, "invalid", source)
		change(input)
		lambdaCall(t, s, "POST", functionRoot, input, 400, nil)
	}
	lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, "sample", source), 201, nil)
	defaults := lambdaInput(t, "defaults", source)
	delete(defaults, "Timeout")
	delete(defaults, "Environment")
	defaultConfig := lambdaCall(t, s, "POST", functionRoot, defaults, 201, nil)
	if defaultConfig["Timeout"] != float64(3) {
		t.Fatal(defaultConfig)
	}
	lambdaCall(t, s, "GET", functionRoot+"?MaxItems=10000", nil, 200, nil)
	for _, input := range []any{map[string]any{"FunctionName": "bad-zip", "Runtime": "python3.12", "Role": "arn:aws:iam::000000000000:role/executor", "Handler": "index.handler", "Code": map[string]any{"ZipFile": []byte("not ZIP")}}, nil} {
		lambdaCall(t, s, "POST", functionRoot, input, 400, nil)
	}
	badTags := lambdaInput(t, "bad-tags", source)
	badTags["Tags"] = map[string]string{"": "invalid"}
	lambdaCall(t, s, "POST", functionRoot, badTags, 400, nil)
	tags := map[string]string{}
	for i := 0; i < 51; i++ {
		tags[fmt.Sprint(i)] = "value"
	}
	badTags["Tags"] = tags
	lambdaCall(t, s, "POST", functionRoot, badTags, 400, nil)
	lambdaCall(t, s, "POST", functionRoot+"/unknown/invocations", map[string]any{}, 404, nil)
	lambdaCall(t, s, "POST", functionRoot+"/sample/invocations", nil, 200, nil)
	lambdaCall(t, s, "GET", functionRoot, nil, 400, map[string]string{"X-Simulator-Service": "s3", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/lambda/aws4_request"})
	for _, entry := range []struct {
		method, path string
		code         int
		headers      map[string]string
	}{{"GET", functionRoot + "?MaxItems=0", 400, nil}, {"GET", functionRoot + "?MaxItems=10001", 400, nil}, {"GET", functionRoot + "?MaxItems=x", 400, nil}, {"GET", functionRoot + "?Marker=bad/name", 400, nil}, {"GET", functionRoot + "?MaxItems=1&MaxItems=2", 400, nil}, {"GET", functionRoot + "?unknown=x", 400, nil}, {"GET", functionRoot + "?Qualifier=$LATEST", 501, nil}, {"GET", functionRoot + "/sample/configuration?Qualifier=1", 501, nil}, {"DELETE", functionRoot + "/sample?Qualifier=$LATEST", 501, nil}, {"GET", functionRoot + "/sample/code", 501, nil}, {"POST", functionRoot + "/sample/invocations", 501, map[string]string{"X-Amz-Invocation-Type": "Event"}}, {"POST", functionRoot + "/sample/invocations", 501, map[string]string{"X-Amz-Log-Type": "Unknown"}}, {"POST", functionRoot + "/sample/invocations", 501, map[string]string{"X-Amz-Log-Type": "Tail", "X-Amz-Invocation-Type": "DryRun"}}, {"POST", functionRoot + "/sample/invocations", 501, map[string]string{"X-Amz-Client-Context": "e30="}}, {"GET", functionRoot, 403, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/date/us-east-1/lambda/aws4_request"}}, {"GET", functionRoot, 501, map[string]string{"X-Amz-Security-Token": "dummy"}}, {"GET", functionRoot, 400, map[string]string{"X-Simulator-Region": "unsupported"}}, {"GET", functionRoot, 400, map[string]string{"X-Simulator-Region": "ap-northeast-1", "Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/lambda/aws4_request"}}, {"GET", functionRoot, 400, map[string]string{"X-Amz-Target": "AmazonSSM.DescribeParameters"}}, {"GET", "/unsupported", 501, nil}, {"GET", functionRoot + "/bad!name/configuration", 400, nil}, {"GET", functionRoot + "/sample/code/extra", 400, nil}, {"GET", functionRoot + "/sample/configuration?Marker=x", 400, nil}} {
		lambdaCall(t, s, entry.method, entry.path, nil, entry.code, entry.headers)
	}
	headers := map[string]string{"X-Simulator-Service": "lambda"}
	status(t, s, "POST", functionRoot+"/sample/invocations", "invalid", 400, headers)
	status(t, s, "POST", functionRoot+"/sample/invocations", strings.Repeat("x", (256<<10)+1), 400, headers)
	status(t, s, "POST", functionRoot, strings.Repeat("x", (384<<10)+1), 400, headers)
	lambdaCall(t, s, "PUT", functionRoot+"/sample/code", map[string]any{"ZipFile": lambdaZIP(t, source), "Runtime": ""}, 400, nil)
	lambdaCall(t, s, "PUT", functionRoot+"/missing/code", map[string]any{"ZipFile": lambdaZIP(t, source)}, 404, nil)
	status(t, s, "PUT", "/2015-03-31", "", 200, nil)
	status(t, s, "PUT", "/2015-03-31/functions/sample/invocations", "literal S3 bytes", 200, nil)
	body := status(t, s, "GET", "/2015-03-31/functions/sample/invocations", "", 200, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/s3/aws4_request"})
	if string(body) != "literal S3 bytes" {
		t.Fatalf("service collision: %s", body)
	}
	lambdaCall(t, s, "GET", functionRoot, nil, 200, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/date/us-east-1/lambda/aws4_request"})
	status(t, s, "PUT", "/_simulator/v1/iam/principals/creator/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"*"}]}`, 200, nil)
	lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, "cannot-pass-role", source), 403, map[string]string{"X-Simulator-Principal": "creator"})
}

func TestLambdaPersistenceAndFunctionQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	h, closeState, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	lambdaRole(t, s)
	source := "def handler(event, context): return {}"
	lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, "persisted", source), 201, nil)
	s.Close()
	if err = closeState(); err != nil {
		t.Fatal(err)
	}
	h, closeState, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	s = httptest.NewServer(h)
	defer s.Close()
	lambdaCall(t, s, "GET", functionRoot+"/persisted/configuration", nil, 200, nil)
	for i := 0; i < 99; i++ {
		lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, fmt.Sprintf("quota-%03d", i), source), 201, nil)
	}
	lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, "over-quota", source), 429, nil)
	lambdaCall(t, s, "PUT", functionRoot+"/persisted/code", map[string]any{"ZipFile": lambdaZIP(t, source)}, 200, nil)
}

func TestLambdaLogTailHTTPOptInAndIAM(t *testing.T) {
	s := instance(t)
	lambdaRole(t, s)
	lambdaCall(t, s, "POST", functionRoot, lambdaInput(t, "tail", "def handler(event, context):\n    print('visible guest log')\n    return {'value':'payload-only'}\n"), 201, nil)
	status(t, s, "PUT", "/_simulator/v1/iam/principals/tail-reader/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:tail"}]}`, 200, nil)
	operator := strings.Repeat("a", 64)
	access, err := NewAccess(s.Config.Handler, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	authenticated := httptest.NewServer(access)
	defer authenticated.Close()
	tokens := map[string]string{}
	for _, principal := range []string{"tail-reader", "executor"} {
		response, data := call(t, authenticated, "POST", "/_simulator/v1/access", fmt.Sprintf(`{"Principal":%q}`, principal), map[string]string{"X-Simulator-Token": operator})
		var issued struct{ AccessToken string }
		if response.StatusCode != 200 || json.Unmarshal(data, &issued) != nil || issued.AccessToken == "" {
			t.Fatalf("participant issuance: %d %s", response.StatusCode, data)
		}
		tokens[principal] = issued.AccessToken
	}
	for _, tc := range []struct {
		logs, principal string
		status          int
		tail            bool
	}{
		{"", "tail-reader", 200, false}, {"None", "tail-reader", 200, false}, {"Tail", "tail-reader", 200, true}, {"Tail", "executor", 403, false},
	} {
		response, data := call(t, authenticated, "POST", functionRoot+"/tail/invocations", "{}", map[string]string{"X-Simulator-Service": "lambda", "X-Simulator-Token": tokens[tc.principal], "X-Amz-Log-Type": tc.logs})
		if response.StatusCode != tc.status {
			t.Fatalf("status=%d body=%s", response.StatusCode, data)
		}
		values := response.Header.Values("X-Amz-Log-Result")
		if (len(values) > 0) != tc.tail {
			t.Fatal("incorrect log disclosure", tc)
		}
		if tc.tail {
			decoded, err := base64.StdEncoding.DecodeString(values[0])
			if err != nil || !bytes.Contains(decoded, []byte("visible guest log")) || bytes.Contains(decoded, []byte("payload-only")) || bytes.Contains(decoded, []byte("TCSIM_")) {
				t.Fatalf("unexpected tail=%q err=%v", decoded, err)
			}
		}
	}
	for _, headers := range []map[string]string{
		{"X-Simulator-Service": "lambda", "X-Amz-Log-Type": "Tail"},
		{"X-Simulator-Service": "lambda", "X-Amz-Log-Type": "Tail", "X-Simulator-Token": tokens["tail-reader"], "X-Simulator-Principal": "simulator-admin"},
	} {
		response, _ := call(t, authenticated, "POST", functionRoot+"/tail/invocations", "{}", headers)
		if (response.StatusCode != 401 && response.StatusCode != 403) || len(response.Header.Values("X-Amz-Log-Result")) != 0 {
			t.Fatal("invalid access exposed invocation/logs", response.StatusCode)
		}
	}

}

func TestLambdaLogTailCapabilitiesPreserveInstanceProfiles(t *testing.T) {
	s := instance(t)
	var capabilities struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil), &capabilities); err != nil {
		t.Fatal(err)
	}
	iam := capabilities.Services["iam"]
	for key, want := range map[string]any{
		"instanceProfiles": true, "instanceProfilePath": "/",
		"instanceProfileClaimsLimit": float64(1000), "rolesPerInstanceProfile": float64(1),
		// Native CFN profile composition is implemented; credentials and console remain unsupported.
		"instanceProfileCredentials": false, "instanceProfileCloudFormation": true, "instanceProfileConsole": false,
	} {
		if got, present := iam[key]; !present || got != want {
			t.Errorf("IAM capability %s = %v (present=%v), want %v", key, got, present, want)
		}
	}
	operations, ok := iam["operations"].([]any)
	if !ok {
		t.Fatal("IAM operations are missing")
	}
	for _, want := range []string{"CreateInstanceProfile", "GetInstanceProfile", "ListInstanceProfiles", "DeleteInstanceProfile", "AddRoleToInstanceProfile", "RemoveRoleFromInstanceProfile", "ListInstanceProfilesForRole"} {
		found := false
		for _, operation := range operations {
			found = found || operation == want
		}
		if !found {
			t.Errorf("IAM operation %s is missing", want)
		}
	}
	tail, ok := capabilities.Services["lambda"]["invokeLogTail"].(map[string]any)
	if !ok {
		t.Fatal("Lambda log-tail capability is missing")
	}
	for key, want := range map[string]any{
		"request": "explicit synchronous LogType=Tail only", "maxBytes": float64(4096),
		"encoding": "base64 of raw bytes; UTF-8 may be split", "ordering": "stdout then stderr, not interleaved",
		"internalFrames": false, "cloudWatchDelivery": false, "persistence": false,
	} {
		if got, present := tail[key]; !present || got != want {
			t.Errorf("Lambda tail capability %s = %v (present=%v), want %v", key, got, present, want)
		}
	}
}
