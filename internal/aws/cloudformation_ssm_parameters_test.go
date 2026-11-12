package aws

import (
	"context"
	"encoding/xml"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const ssmValueTemplate = `Parameters:
  Image:
    Type: AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>
    Default: /practice/image
    AllowedValues: [/practice/image]
  Message:
    Type: AWS::SSM::Parameter::Value<String>
    Default: /practice/message
    AllowedPattern: /practice/.*
Conditions:
  Selected: !Equals [!Ref Image, ami-0123456789abcdef0]
Resources:
  Copy:
    Type: AWS::SSM::Parameter
    Properties: {Name: /practice/copy, Type: String, Value: !Ref Message}
  ImageCopy:
    Type: AWS::SSM::Parameter
    Condition: Selected
    Properties: {Name: /practice/image-copy, Type: String, Value: !Ref Image}
Outputs:
  Snapshot: {Value: !Sub '${Image}:${Message}'}
`

func ssmValueGateway(t *testing.T) *Gateway {
	t.Helper()
	db, s3, stacks, parameters := s3StackState(t)
	return &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(db)}
}

func ssmValuePut(t *testing.T, g *Gateway, region, key, kind, value string) {
	t.Helper()
	if _, err := g.Parameters.Put(region, key, kind, value, nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestStackSSMParameterSnapshotLifecycleAndReplay(t *testing.T) {
	g, peer := ssmValueGateway(t), ssmValueGateway(t)
	peerBefore := stackConditionalDBFrame(t, peer.Store.Database())
	ssmValuePut(t, g, "ap-northeast-1", "/practice/image", "String", "ami-0123456789abcdef0")
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "雪 <&>\n")
	ssmValuePut(t, g, "eu-west-1", "/practice/message", "String", "other-region")
	params := url.Values{"ClientRequestToken": {"snapshot-token"}}
	w := conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "", params)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	records, err := g.Stacks.Records("ap-northeast-1", "boundary")
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	record := records[0]
	if record.Parameters["Image"] != "/practice/image" || record.ResolvedParameters["Image"] != "ami-0123456789abcdef0" || record.Outputs["Snapshot"] != "ami-0123456789abcdef0:雪 <&>\n" {
		t.Fatal("input, resolved value and Ref output were conflated", record)
	}
	for key, expected := range map[string]string{"/practice/copy": "雪 <&>\n", "/practice/image-copy": "ami-0123456789abcdef0"} {
		p, err := g.Parameters.Get("ap-northeast-1", key)
		if err != nil || p.Value != expected {
			t.Fatal(key, p, err)
		}
	}
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "new-value")
	if err = g.Parameters.Delete("ap-northeast-1", "/practice/image"); err != nil {
		t.Fatal(err)
	}
	before := stackConditionalDBFrame(t, g.Store.Database())
	w = conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "", params)
	if w.Code != 200 || !strings.Contains(w.Body.String(), record.ID) || before != stackConditionalDBFrame(t, g.Store.Database()) {
		t.Fatal("retry re-read source or mutated snapshot", w.Code, w.Body.String())
	}
	for _, name := range []string{"", record.ID, "bad/name", strings.Repeat("x", 129)} {
		w = conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "", url.Values{"ClientRequestToken": {"snapshot-token"}, "StackName": {name}})
		conditionBoundaryError(t, w, 400, "ValidationError", "")
		if before != stackConditionalDBFrame(t, g.Store.Database()) {
			t.Fatal("invalid-name replay changed state")
		}
	}
	w = conditionBoundarySend(g, "DescribeStacks", "", "", nil)
	var response struct {
		Result struct {
			Stacks []struct {
				Parameters []struct{ ParameterKey, ParameterValue, ResolvedValue string } `xml:"Parameters>member"`
			} `xml:"Stacks>member"`
		} `xml:"DescribeStacksResult"`
	}
	if err = xml.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || len(response.Result.Stacks) != 1 {
		t.Fatal(w.Code, err, w.Body.String())
	}
	ps := response.Result.Stacks[0].Parameters
	if len(ps) != 2 || ps[0].ParameterValue != "/practice/image" || ps[0].ResolvedValue != "ami-0123456789abcdef0" || ps[1].ResolvedValue != "雪 <&>\n" {
		t.Fatal("DescribeStacks did not preserve saved key/value or XML escaping", ps)
	}
	w = conditionBoundarySend(g, "CreateStack", ssmValueTemplate+"\n# different request\n", "", params)
	conditionBoundaryError(t, w, 400, "ValidationError", "")
	if err = g.Identities.Set("blocked", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"*"}, Resource: core.StringList{"*"}}, {Effect: "Deny", Action: core.StringList{"cloudformation:CreateStack"}, Resource: core.StringList{record.ID}}}}); err != nil {
		t.Fatal(err)
	}
	w = conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "blocked", params)
	conditionBoundaryError(t, w, 403, "AccessDenied", "")
	w = conditionBoundarySend(g, "DeleteStack", "", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p, err := g.Parameters.Get("ap-northeast-1", "/practice/message")
	if err != nil || p.Value != "new-value" || stackConditionalDBFrame(t, peer.Store.Database()) != peerBefore {
		t.Fatal("cleanup removed source or changed peer", p, err)
	}
}

func TestStackSSMParameterAdmissionAndAuthorizationFrames(t *testing.T) {
	g := ssmValueGateway(t)
	ssmValuePut(t, g, "ap-northeast-1", "/practice/image", "String", "ami-0123456789abcdef0")
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "value-not-matching-key-pattern")
	for _, body := range []string{
		strings.Replace(ssmValueTemplate, "Default: /practice/message", "Default: /missing", 1),
		strings.Replace(ssmValueTemplate, "Value<String>", "Value<List<String>>", 1),
		strings.Replace(ssmValueTemplate, "Value<String>", "Value<SecureString>", 1),
		strings.Replace(ssmValueTemplate, "Value<String>", "Name", 1),
		strings.Replace(ssmValueTemplate, "Default: /practice/message", "Default: /practice/message:1", 1),
		strings.Replace(ssmValueTemplate, "Default: /practice/message", "Default: 'arn:aws:ssm:eu-west-1:111111111111:parameter/x'", 1),
		strings.Replace(ssmValueTemplate, "Default: /practice/message", "Default: /practice/message\n    NoEcho: true", 1),
		strings.Replace(ssmValueTemplate, "Default: /practice/message", "Default: /practice/message\n    MinValue: 1", 1),
		strings.Replace(ssmValueTemplate, "AllowedValues: [/practice/image]", "AllowedValues: [ami-0123456789abcdef0]", 1),
		strings.Replace(ssmValueTemplate, "Default: /practice/image", "Default: /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64", 1),
		battleIntrinsicTemplate(t),
	} {
		before := stackConditionalDBFrame(t, g.Store.Database())
		w := conditionBoundarySend(g, "CreateStack", body, "", nil)
		conditionBoundaryError(t, w, 400, "ValidationError", "")
		if before != stackConditionalDBFrame(t, g.Store.Database()) {
			t.Fatal("invalid request changed native frame")
		}
	}
	for _, denied := range []string{"ssm:GetParameters", "cloudformation:CreateStack", "ssm:PutParameter"} {
		policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"*"}, Resource: core.StringList{"*"}}, {Effect: "Deny", Action: core.StringList{denied}, Resource: core.StringList{"*"}}}}
		if err := g.Identities.Set("restricted", policy); err != nil {
			t.Fatal(err)
		}
		before := stackConditionalDBFrame(t, g.Store.Database())
		w := conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "restricted", nil)
		conditionBoundaryError(t, w, 403, "AccessDenied", "")
		if strings.Contains(w.Body.String(), "value-not-matching") || before != stackConditionalDBFrame(t, g.Store.Database()) {
			t.Fatal("denial leaked value or changed native frame")
		}
	}
	for _, bad := range []struct{ kind, value string }{{"StringList", "a,b"}, {"SecureString", "dummy"}, {"String", ""}, {"String", strings.Repeat("x", 4097)}, {"String", string([]byte{0xff})}, {"String", "value\x00"}, {"String", "value\uffff"}} {
		ssmValuePut(t, g, "ap-northeast-1", "/practice/message", bad.kind, bad.value)
		before := stackConditionalDBFrame(t, g.Store.Database())
		w := conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "", nil)
		conditionBoundaryError(t, w, 400, "ValidationError", "")
		if before != stackConditionalDBFrame(t, g.Store.Database()) {
			t.Fatal("unsupported source changed native frame")
		}
	}
}

func TestStackSSMParameterResolverBoundaries(t *testing.T) {
	g := ssmValueGateway(t)
	ssmValuePut(t, g, "ap-northeast-1", "/practice/image", "String", "local-image-label")
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "old")
	allow := func(string, string) error { return nil }
	resolver := func(keys map[string]string) (map[string]string, error) {
		return g.Stacks.resolveSSMParameters(context.Background(), "ap-northeast-1", keys, allow)
	}
	// The unchanged original parameter declaration is admitted independently;
	// no default public value or EC2 resource is fabricated.
	original := object(intrinsicYAML(t, battleIntrinsicTemplate(t)))
	defs := object(original["Parameters"])
	for name, raw := range defs {
		if stackSSMValueType(stringValue(object(raw)["Type"])) {
			values, err := stackParameterValues(map[string]any{name: raw}, map[string]string{name: "/practice/image"})
			if err != nil || values[name] != "/practice/image" {
				t.Fatal("original SSM parameter declaration rejected", err)
			}
		}
	}
	if _, err := compileStack("ap-northeast-1", "test", ssmValueTemplate, nil, ""); err == nil {
		t.Fatal("pure compiler invented SSM state")
	}
	if _, err := compileStackWithParameters("ap-northeast-1", "test", ssmValueTemplate, nil, "", func(string, map[string]string) (map[string]string, error) { return map[string]string{}, nil }); err == nil {
		t.Fatal("incomplete resolver accepted")
	}
	record, err := compileStackWithParameters("ap-northeast-1", "test", ssmValueTemplate, nil, "", func(_ string, keys map[string]string) (map[string]string, error) { return resolver(keys) })
	if err != nil {
		t.Fatal(err)
	}
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "new")
	if err = g.Stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	p, _ := g.Parameters.Get("ap-northeast-1", "/practice/copy")
	if p.Value != "old" || len(record.Resources) != 1 {
		t.Fatal("creation re-read SSM or condition used input key", p, record.Resources)
	}
	// Every key is authorized before any row is read, including an absent first
	// row. An access denial must win rather than reveal the absent row.
	calls := []string{}
	denial := errors.New("denied")
	_, err = g.Stacks.resolveSSMParameters(context.Background(), "ap-northeast-1", map[string]string{"A": "/absent", "Z": "/practice/message"}, func(action, resource string) error {
		calls = append(calls, action+" "+resource)
		if strings.HasSuffix(resource, "/practice/message") {
			return denial
		}
		return nil
	})
	if err != denial || len(calls) != 2 || !strings.HasPrefix(calls[0], "ssm:GetParameters arn:aws:ssm:ap-northeast-1:") {
		t.Fatal(calls, err)
	}
	for _, key := range []string{"", "a:1", "a//b", "a b", strings.Repeat("a", 1012), strings.Repeat("/a", 16)} {
		if _, err = resolver(map[string]string{"X": key}); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	for _, key := range []string{"/aws/service/image", "name", "/a/b", strings.Repeat("a", 1011)} {
		if !stackSSMKey(key) {
			t.Fatal("valid key rejected", key)
		}
	}
	if _, err = g.Stacks.resolveSSMParameters(context.Background(), "eu-west-1", map[string]string{"X": "/practice/message"}, allow); err == nil {
		t.Fatal("cross-region source read")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.Stacks.resolveSSMParameters(ctx, "ap-northeast-1", map[string]string{"X": "/practice/message"}, allow); err == nil {
		t.Fatal("canceled source read")
	}
	if _, err = g.Store.Database().Exec("DROP TABLE aws_ssm_parameters"); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver(map[string]string{"X": "/practice/message"}); err == nil {
		t.Fatal("damaged source read")
	}
	if !reflect.DeepEqual(stackSSMRequests(map[string]any{"A": map[string]any{"Type": "String"}}, map[string]string{"A": "plain"}), map[string]string{}) {
		t.Fatal("ordinary String was resolved")
	}
}

func TestStackSSMParameterExactARNPreflightAndRollback(t *testing.T) {
	g := ssmValueGateway(t)
	ssmValuePut(t, g, "ap-northeast-1", "/practice/image", "String", "ami-0123456789abcdef0")
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "snapshot")
	pattern := "arn:aws:cloudformation:ap-northeast-1:000000000000:stack/boundary/" + strings.Repeat("?", 32)
	allow := core.Policy{Version: "2012-10-17", Statement: core.Statements{
		{Effect: "Allow", Action: core.StringList{"cloudformation:CreateStack"}, Resource: core.StringList{pattern}},
		{Effect: "Allow", Action: core.StringList{"ssm:GetParameters", "ssm:PutParameter"}, Resource: core.StringList{"*"}},
	}}
	if err := g.Identities.Set("exact", allow); err != nil {
		t.Fatal(err)
	}
	w := conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "exact", nil)
	if w.Code != 200 {
		t.Fatal("actual generated ARN policy failed", w.Code, w.Body.String())
	}
	if err := g.Stacks.Delete("ap-northeast-1", "boundary"); err != nil {
		t.Fatal(err)
	}
	deny := core.Policy{Version: "2012-10-17", Statement: core.Statements{
		{Effect: "Allow", Action: core.StringList{"*"}, Resource: core.StringList{"*"}},
		{Effect: "Deny", Action: core.StringList{"cloudformation:CreateStack"}, Resource: core.StringList{pattern}},
	}}
	if err := g.Identities.Set("exact", deny); err != nil {
		t.Fatal(err)
	}
	if err := g.Parameters.Delete("ap-northeast-1", "/practice/image"); err != nil {
		t.Fatal(err)
	}
	before := stackConditionalDBFrame(t, g.Store.Database())
	w = conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "exact", nil)
	conditionBoundaryError(t, w, 403, "AccessDenied", "")
	if before != stackConditionalDBFrame(t, g.Store.Database()) {
		t.Fatal("exact ARN denial changed state")
	}
	ssmValuePut(t, g, "ap-northeast-1", "/practice/image", "String", "ami-0123456789abcdef0")
	if _, err := g.Store.Database().Exec(`CREATE TRIGGER reject_ssm_snapshot_event BEFORE INSERT ON aws_cfn_events WHEN NEW.logical_id='ImageCopy' AND NEW.status='CREATE_COMPLETE' BEGIN SELECT RAISE(ABORT,'injected event refusal'); END`); err != nil {
		t.Fatal(err)
	}
	before = stackConditionalDBFrame(t, g.Store.Database())
	w = conditionBoundarySend(g, "CreateStack", ssmValueTemplate, "", nil)
	if w.Code == 200 || before != stackConditionalDBFrame(t, g.Store.Database()) {
		t.Fatal("failed create leaked partial snapshot/resources/events", w.Code)
	}
}

type publicImageRuntime struct{ alias string }

func (publicImageRuntime) PrepareStackInstance(context.Context, StackRecord, StackResource) (StackInstanceAttempt, error) {
	return nil, errors.New("no instance in this template")
}
func (r publicImageRuntime) PublicImageParameter(name string) (string, bool) {
	if strings.HasPrefix(name, "/aws/service/ami-amazon-linux-latest/") {
		return r.alias, true
	}
	return "", false
}

func TestStackSSMPublicAMIAliasResolvesThroughConfiguredRuntime(t *testing.T) {
	g := ssmValueGateway(t)
	if err := g.Stacks.ConfigureInstanceRuntime(publicImageRuntime{alias: "ami-0a1b2c3d4e5f60718"}); err != nil {
		t.Fatal(err)
	}
	ssmValuePut(t, g, "ap-northeast-1", "/practice/message", "String", "hello")
	public := "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-x86_64"
	body := strings.Replace(strings.Replace(ssmValueTemplate, "Default: /practice/image", "Default: "+public, 1), "AllowedValues: [/practice/image]", "AllowedValues: ["+public+"]", 1)
	w := conditionBoundarySend(g, "CreateStack", body, "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	records, err := g.Stacks.Records("ap-northeast-1", "boundary")
	if err != nil || len(records) != 1 || records[0].Parameters["Image"] != public || records[0].ResolvedParameters["Image"] != "ami-0a1b2c3d4e5f60718" {
		t.Fatal("public alias did not resolve to the pinned runtime image", records, err)
	}
	if w = conditionBoundarySend(g, "DeleteStack", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Non-AMI public names are never synthesized.
	g2 := ssmValueGateway(t)
	if err := g2.Stacks.ConfigureInstanceRuntime(publicImageRuntime{alias: "ami-0a1b2c3d4e5f60718"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g2.Stacks.resolveSSMParameters(context.Background(), "ap-northeast-1", map[string]string{"Image": "/aws/service/other/thing"}, func(string, string) error { return nil }); err == nil {
		t.Fatal("non-AMI public parameter was synthesized")
	}
}
