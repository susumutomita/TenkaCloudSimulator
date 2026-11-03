package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const simpleTemplate = `{"Parameters":{"Value":{"Type":"String","Default":"safe-dummy"}},"Resources":{"P":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/owned/hello","Type":"String","Value":{"Ref":"Value"}}},"R":{"Type":"AWS::IAM::Role","Properties":{"Policies":[{"PolicyName":"read","PolicyDocument":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"arn:aws:ssm:*:*:parameter/owned/*"}]}}]}}},"Outputs":{"Name":{"Value":{"Ref":"P"}},"Value":{"Value":{"Fn::GetAtt":["P","Value"]}},"Role":{"Value":{"Fn::GetAtt":"R.Arn"}}}}`

func cfnCall(t *testing.T, s *httptest.Server, action string, fields url.Values, code int, extra map[string]string) []byte {
	t.Helper()
	if fields == nil {
		fields = url.Values{}
	}
	fields.Set("Action", action)
	fields.Set("Version", "2010-05-15")
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range extra {
		h[k] = v
	}
	return status(t, s, "POST", "/", fields.Encode(), code, h)
}
func TestCloudFormationAtomicLifecycleAndOwnership(t *testing.T) {
	s := instance(t)
	create := url.Values{"StackName": {"owned"}, "TemplateBody": {simpleTemplate}, "Capabilities.member.1": {"CAPABILITY_NAMED_IAM"}, "ClientRequestToken": {"same-request"}, "Tags.member.1.Key": {"tenkacloud:job"}, "Tags.member.1.Value": {"local-job"}}
	cfnCall(t, s, "CreateStack", create, 200, nil)
	original := string(cfnCall(t, s, "DescribeStacks", url.Values{"StackName": {"owned"}}, 200, nil))
	if !strings.Contains(original, "safe-dummy") || !strings.Contains(original, "local-job") {
		t.Fatal(original)
	}
	cfnCall(t, s, "CreateStack", create, 200, nil)
	create.Set("Parameters.member.1.ParameterKey", "Value")
	create.Set("Parameters.member.1.ParameterValue", "changed")
	cfnCall(t, s, "CreateStack", create, 400, nil)
	delete(create, "Parameters.member.1.ParameterKey")
	delete(create, "Parameters.member.1.ParameterValue")
	delete(create, "ClientRequestToken")
	cfnCall(t, s, "CreateStack", create, 400, nil)
	reader := map[string]string{"X-Simulator-Principal": "owned-R"}
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/owned/hello"}, 200, reader)
	cfnCall(t, s, "DescribeStacks", nil, 403, reader)
	// A manually deleted resource remains reserved until its owner is deleted.
	ssmCall(t, s, "DeleteParameter", map[string]any{"Name": "/owned/hello"}, 200, nil)
	create.Set("StackName", "neighbor")
	cfnCall(t, s, "CreateStack", create, 400, nil)
	cfnCall(t, s, "DescribeStacks", url.Values{"StackName": {"neighbor"}}, 400, nil)
	cfnCall(t, s, "DeleteStack", url.Values{"StackName": {"owned"}}, 200, nil)
	cfnCall(t, s, "CreateStack", create, 200, nil)
	cfnCall(t, s, "DeleteStack", url.Values{"StackName": {"neighbor"}}, 200, nil)
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/owned/hello"}, 400, nil)
	cfnCall(t, s, "DescribeStacks", nil, 200, nil)
	cfnCall(t, s, "DeleteStack", nil, 400, nil)
	cfnCall(t, s, "DeleteStack", url.Values{"StackName": {"missing"}}, 400, nil)
}
func TestCloudFormationWireValidation(t *testing.T) {
	s := instance(t)
	for _, fields := range []url.Values{
		{"TemplateURL": {"https://example.invalid/never-fetch"}},
		{"Parameters.member.101.ParameterKey": {"x"}},
		{"Parameters.member.1.ParameterValue": {"x"}},
		{"Capabilities.member.2": {"CAPABILITY_IAM"}},
		{"Tags.member.1.Key": {"x"}},
		{"Tags.member.0.Key": {"x"}},
		{"Tags.member.01.Key": {"x"}},
		{"StackName": {"x", "y"}},
		{"Tags.member.1.Key": {"x"}, "Tags.member.1.Value": {"one"}, "Tags.member.2.Key": {"x"}, "Tags.member.2.Value": {"two"}},
	} {
		cfnCall(t, s, "CreateStack", fields, 400, nil)
	}
	cfnCall(t, s, "UpdateStack", nil, 501, nil)
	cfnCall(t, s, "DescribeStacks", url.Values{"TemplateBody": {simpleTemplate}}, 400, nil)
	for _, headers := range []map[string]string{{"Authorization": "real key"}, {"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAREAL/20260101/us-east-1/cloudformation/aws4_request"}} {
		cfnCall(t, s, "DescribeStacks", nil, 403, headers)
	}
	for _, headers := range []map[string]string{{"X-Simulator-Region": "invalid"}, {"X-Amz-Security-Token": "session"}, {"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIM/20260101/us-east-1/cloudformation/aws4_request", "X-Simulator-Region": "ap-northeast-1"}} {
		cfnCall(t, s, "DescribeStacks", nil, 400, headers)
	}
	cfnCall(t, s, "DescribeStacks", nil, 200, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIM/20260101/ap-northeast-1/cloudformation/aws4_request"})
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for _, path := range []string{"/bucket", "/?x=1"} {
		status(t, s, "POST", path, "Action=DescribeStacks&Version=2010-05-15", 400, h)
	}
	for _, body := range []string{"%invalid", "Action=DescribeStacks&Version=wrong", strings.Repeat("a", (1<<20)+1)} {
		status(t, s, "POST", "/", body, 400, h)
	}
}
