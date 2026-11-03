package server

import (
	"encoding/json"
	"encoding/xml"
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
	reader := map[string]string{"X-Simulator-Principal": strings.TrimPrefix(stackOutput(t, []byte(original), "Role"), "arn:aws:iam::000000000000:role/")}
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

func TestParticipantCannotReadPrivateOutputsWithWildcardIAM(t *testing.T) {
	h, closeState, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	operator := strings.Repeat("o", 64)
	access, err := NewAccess(h, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(access)
	defer s.Close()
	admin := map[string]string{"X-Simulator-Token": operator}
	status(t, s, "PUT", "/_simulator/v1/iam/principals/broad/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`, 200, admin)
	cfnCall(t, s, "CreateStack", url.Values{"StackName": {"private"}, "TemplateBody": {simpleTemplate}, "Capabilities.member.1": {"CAPABILITY_IAM"}}, 200, admin)
	data := status(t, s, "POST", "/_simulator/v1/access", `{"Principal":"broad"}`, 200, admin)
	var issued struct{ AccessToken string }
	if json.Unmarshal(data, &issued) != nil {
		t.Fatal("invalid issuance")
	}
	participant := map[string]string{"X-Simulator-Token": issued.AccessToken, "X-Simulator-Access-Role": "operator"}
	for _, fields := range []url.Values{nil, {"StackName": {"private"}}} {
		body := cfnCall(t, s, "DescribeStacks", fields, 403, participant)
		if strings.Contains(string(body), "safe-dummy") {
			t.Fatal("private output leaked")
		}
	}
	cfnCall(t, s, "DescribeStacks", url.Values{"StackName": {"private"}}, 200, admin)
	// Wildcard exercise permission remains usable for the intended data plane.
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/owned/hello"}, 200, participant)
}

func TestStackIAMUsesActualResourceARN(t *testing.T) {
	s := instance(t)
	template := func(name string) string { return strings.ReplaceAll(simpleTemplate, "/owned/hello", "/"+name+"/hello") }
	create := func(name string, headers map[string]string) string {
		t.Helper()
		body := cfnCall(t, s, "CreateStack", url.Values{"StackName": {name}, "TemplateBody": {template(name)}, "Capabilities.member.1": {"CAPABILITY_IAM"}, "ClientRequestToken": {"retry"}}, 200, headers)
		var response struct {
			Result struct {
				ID string `xml:"StackId"`
			} `xml:"CreateStackResult"`
		}
		if err := xml.Unmarshal(body, &response); err != nil || response.Result.ID == "" {
			t.Fatal(string(body), err)
		}
		return response.Result.ID
	}
	protected := create("protected", nil)
	allowed := create("allowed", nil)
	identity := map[string]string{"X-Simulator-Principal": "stack-operator"}
	policy := func(statements ...map[string]any) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
		status(t, s, "PUT", "/_simulator/v1/iam/principals/stack-operator/policy", string(data), 200, nil)
	}
	statement := func(effect, action, resource string) map[string]any {
		return map[string]any{"Effect": effect, "Action": action, "Resource": resource}
	}
	policy(statement("Allow", "cloudformation:*", "*"), statement("Deny", "cloudformation:DeleteStack", protected))
	for _, name := range []string{"protected", protected} {
		cfnCall(t, s, "DeleteStack", url.Values{"StackName": {name}}, 403, identity)
	}
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/protected/hello"}, 200, nil)
	policy(statement("Allow", "cloudformation:DescribeStacks", protected))
	for _, name := range []string{"protected", protected} {
		cfnCall(t, s, "DescribeStacks", url.Values{"StackName": {name}}, 200, identity)
	}
	cfnCall(t, s, "DescribeStacks", nil, 403, identity)
	cfnCall(t, s, "DescribeStacks", url.Values{"StackName": {"allowed"}}, 403, identity)
	policy(statement("Allow", "cloudformation:*", "*"), statement("Deny", "cloudformation:DeleteStack", protected))
	cfnCall(t, s, "DeleteStack", url.Values{"StackName": {allowed}}, 200, identity)
	policy(statement("Allow", "cloudformation:DeleteStack", protected))
	cfnCall(t, s, "DeleteStack", url.Values{"StackName": {"protected"}}, 200, identity)
	policy(statement("Allow", "cloudformation:CreateStack", "arn:aws:cloudformation:us-east-1:000000000000:stack/permitted/*"))
	permitted := create("permitted", identity)
	cfnCall(t, s, "CreateStack", url.Values{"StackName": {"forbidden"}, "TemplateBody": {template("forbidden")}, "Capabilities.member.1": {"CAPABILITY_IAM"}}, 403, identity)
	ssmCall(t, s, "GetParameter", map[string]any{"Name": "/forbidden/hello"}, 400, nil)
	// Idempotent create authorizes the persisted target, not a new random ID.
	policy(statement("Allow", "cloudformation:CreateStack", permitted))
	if got := create("permitted", identity); got != permitted {
		t.Fatal("retry changed ID")
	}
	policy(statement("Allow", "cloudformation:CreateStack", "*"), statement("Deny", "cloudformation:CreateStack", permitted))
	cfnCall(t, s, "CreateStack", url.Values{"StackName": {"permitted"}, "TemplateBody": {template("permitted")}, "Capabilities.member.1": {"CAPABILITY_IAM"}, "ClientRequestToken": {"retry"}}, 403, identity)
	policy(statement("Allow", "cloudformation:DescribeStacks", "*"), statement("Deny", "cloudformation:DescribeStacks", permitted))
	body := cfnCall(t, s, "DescribeStacks", nil, 403, identity)
	if strings.Contains(string(body), "safe-dummy") {
		t.Fatal("list leaked denied outputs")
	}
	cfnCall(t, s, "DescribeStacks", nil, 403, map[string]string{"X-Simulator-Principal": "unknown"})
}

func stackOutput(t *testing.T, body []byte, key string) string {
	t.Helper()
	var response struct {
		Outputs []struct {
			Key   string `xml:"OutputKey"`
			Value string `xml:"OutputValue"`
		} `xml:"DescribeStacksResult>Stacks>member>Outputs>member"`
	}
	if err := xml.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	for _, output := range response.Outputs {
		if output.Key == key {
			return output.Value
		}
	}
	t.Fatalf("Missing output %s", key)
	return ""
}
