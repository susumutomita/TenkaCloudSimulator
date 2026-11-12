package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func ssmCall(t *testing.T, s *httptest.Server, op string, input any, code int, extra map[string]string) map[string]any {
	t.Helper()
	h := map[string]string{"X-Amz-Target": "AmazonSSM." + op, "Content-Type": "application/x-amz-json-1.1"}
	for k, v := range extra {
		h[k] = v
	}
	body, _ := json.Marshal(input)
	data := status(t, s, "POST", "/", string(body), code, h)
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		t.Fatal(string(data))
	}
	return out
}
func TestSSMLifecycleAndIAM(t *testing.T) {
	s := instance(t)
	put := map[string]any{"Name": "/team/hello", "Type": "String", "Value": "dummy-value", "Description": "Preserve this metadata"}
	if got := ssmCall(t, s, "PutParameter", put, 200, nil); got["Version"] != float64(1) {
		t.Fatal(got)
	}
	ssmCall(t, s, "PutParameter", put, 400, nil)
	get := map[string]any{"Name": "/team/hello"}
	got := ssmCall(t, s, "GetParameter", get, 200, nil)["Parameter"].(map[string]any)
	if got["Value"] != "dummy-value" || got["Version"] != float64(1) {
		t.Fatal(got)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:GetParameter","ssm:GetParameters","ssm:GetParametersByPath"],"Resource":"arn:aws:ssm:*:*:parameter/team/*"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/reader/policy", policy, 200, nil)
	reader := map[string]string{"X-Simulator-Principal": "reader"}
	ssmCall(t, s, "GetParameter", get, 200, reader)
	ssmCall(t, s, "PutParameter", put, 403, reader)
	ssmCall(t, s, "DescribeParameters", map[string]any{}, 403, reader)
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/other/hello"}, 403, reader)
	list := ssmCall(t, s, "DescribeParameters", map[string]any{}, 200, nil)["Parameters"].([]any)
	if _, ok := list[0].(map[string]any)["Value"]; ok {
		t.Fatal("metadata leaked value")
	}
	ssmCall(t, s, "PutParameter", map[string]any{"Name": "/team/hello", "Value": "updated", "Overwrite": true}, 200, nil)
	got = ssmCall(t, s, "GetParameter", get, 200, reader)["Parameter"].(map[string]any)
	if got["Value"] != "updated" || got["Version"] != float64(2) {
		t.Fatal(got)
	}
	metadata := ssmCall(t, s, "DescribeParameters", map[string]any{}, 200, nil)["Parameters"].([]any)[0].(map[string]any)
	if metadata["Description"] != "Preserve this metadata" {
		t.Fatal("overwrite erased omitted description", metadata)
	}
	tokyo := map[string]string{"X-Simulator-Region": "ap-northeast-1"}
	ssmCall(t, s, "GetParameter", get, 400, tokyo)
	ssmCall(t, s, "PutParameter", put, 200, tokyo)
	ssmCall(t, s, "DeleteParameter", get, 200, nil)
	ssmCall(t, s, "GetParameter", get, 400, nil)
	ssmCall(t, s, "DeleteParameter", get, 400, nil)
	ssmCall(t, s, "GetParameter", get, 200, tokyo)
}
func TestSSMPaginationAndValidation(t *testing.T) {
	s := instance(t)
	for _, n := range []string{"/team/a", "/team/b", "/team/nested/c", "/other/a"} {
		ssmCall(t, s, "PutParameter", map[string]any{"Name": n, "Value": "value", "Type": "StringList"}, 200, nil)
	}
	result := ssmCall(t, s, "GetParametersByPath", map[string]any{"Path": "/team", "MaxResults": 1}, 200, nil)
	token := result["NextToken"]
	second := ssmCall(t, s, "GetParametersByPath", map[string]any{"Path": "/team", "MaxResults": 1, "NextToken": token}, 200, nil)
	if second["NextToken"] != nil {
		t.Fatal(second)
	}
	ssmCall(t, s, "GetParametersByPath", map[string]any{"Path": "/other", "NextToken": token}, 400, nil)
	all := ssmCall(t, s, "GetParametersByPath", map[string]any{"Path": "/team", "Recursive": true}, 200, nil)
	if len(all["Parameters"].([]any)) != 3 {
		t.Fatal(all)
	}
	page := ssmCall(t, s, "DescribeParameters", map[string]any{"MaxResults": 1}, 200, nil)
	ssmCall(t, s, "DescribeParameters", map[string]any{"NextToken": page["NextToken"]}, 200, nil)
	got := ssmCall(t, s, "GetParameters", map[string]any{"Names": []string{"/team/a", "/team/missing"}}, 200, nil)
	if len(got["InvalidParameters"].([]any)) != 1 {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		op   string
		body map[string]any
	}{
		{"PutParameter", map[string]any{"Name": "/new", "Value": "x", "Type": "SecureString"}},
		{"PutParameter", map[string]any{"Name": "/new", "Value": "x", "Type": "String", "Tier": "Advanced"}},
		{"PutParameter", map[string]any{"Name": "/new", "Value": "x", "Type": "String", "Tags": []any{}}},
		{"PutParameter", map[string]any{"Name": "/new", "Value": "", "Type": "String"}},
		{"PutParameter", map[string]any{"Name": "/new", "Value": "a,,b", "Type": "StringList"}},
		{"GetParameter", map[string]any{"Name": nil}}, {"GetParameter", map[string]any{"Name": "bad name"}}, {"GetParameter", map[string]any{"Name": 123}},
		{"GetParameters", map[string]any{"Names": []string{}}}, {"GetParameters", map[string]any{"Names": []string{"bad name"}}},
		{"GetParametersByPath", map[string]any{"Path": "relative"}}, {"GetParametersByPath", map[string]any{"Path": "/team", "MaxResults": 11}},
		{"DescribeParameters", map[string]any{"MaxResults": -1}}, {"DescribeParameters", map[string]any{"NextToken": "invalid"}},
	} {
		ssmCall(t, s, tc.op, tc.body, 400, nil)
	}
	// The host access token authorizes ordinary simulator APIs, not an assumed
	// role's guest execution. Native session/command routes require verified STS
	// participant credentials before parsing payloads or checking backend setup.
	for _, action := range []string{"StartSession", "SendCommand"} {
		ssmCall(t, s, action, map[string]any{}, 403, nil)
		ssmCall(t, s, action, map[string]any{}, 403, map[string]string{"X-Simulator-Principal": "simulator-admin"})
	}
	ssmCall(t, s, "GetParameter", nil, 400, nil)
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/team/a"}, 403, map[string]string{"X-Simulator-Principal": "unknown"})
	for _, headers := range []map[string]string{{"X-Amz-Security-Token": "dummy"}, {"X-Simulator-Region": "invalid"}, {"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/date/us-east-1/ssm/aws4_request", "X-Simulator-Region": "ap-northeast-1"}} {
		ssmCall(t, s, "GetParameter", map[string]any{"Name": "/team/a"}, 400, headers)
	}
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/team/a"}, 403, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/date/us-east-1/ssm/aws4_request"})
	status(t, s, "GET", "/", "", 400, map[string]string{"X-Amz-Target": "AmazonSSM.GetParameter"})
	status(t, s, "POST", "/?unsupported=1", "{}", 400, map[string]string{"X-Amz-Target": "AmazonSSM.DescribeParameters"})
	status(t, s, "POST", "/_simulator/providers/aws/", "{}", 200, map[string]string{"X-Amz-Target": "AmazonSSM.DescribeParameters"})
	status(t, s, "POST", "/", strings.Repeat("x", (48<<10)+1), 400, map[string]string{"X-Amz-Target": "AmazonSSM.DescribeParameters"})
}
