package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// This cross-project fixture is the exact generated Host template. Only its
// role-only stack is stored. The original Battle template is compiled and its
// real permission preflight evaluated, never provisioned or launched.
func TestActualHostBattleBootstrapContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/host-battle-bootstrap/template.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "25ca1739723a2dcab11477835fcdb07aee1be4516b55c9b2d46554ccfaab0bf7" {
		t.Fatal("Host fixture bytes changed without provenance review")
	}
	var provenance struct {
		TemplateSHA256 string            `json:"templateSHA256"`
		SourceFiles    map[string]string `json:"sourceFiles"`
	}
	metadata, err := os.ReadFile("testdata/host-battle-bootstrap/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(metadata, &provenance); err != nil || provenance.TemplateSHA256 != hex.EncodeToString(sum[:]) || len(provenance.SourceFiles) != 2 {
		t.Fatal("missing exact Host provenance", err)
	}
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stacks, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := compileStack("ap-northeast-1", "tc-simulator-bootstrap", string(raw), nil, "CAPABILITY_NAMED_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if len(bootstrap.Resources) != 2 {
		t.Fatal("fixture is not role-only")
	}
	for _, resource := range bootstrap.Resources {
		if resource.Type != "AWS::IAM::Role" {
			t.Fatal("fixture contains non-role resource")
		}
	}
	if err = stacks.Create(bootstrap); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, Identities: core.NewIdentities(db), Stacks: stacks}
	role, err := gateway.stsRole(context.Background(), bootstrap.Outputs["RoleArn"])
	if err != nil {
		t.Fatal(err)
	}
	policy, err := gateway.stsSessionPolicy(context.Background(), role)
	if err != nil {
		t.Fatal(err)
	}
	commandRole, err := gateway.stsRole(context.Background(), bootstrap.Outputs["CommandRoleArn"])
	if err != nil {
		t.Fatal(err)
	}
	commandPolicy, err := gateway.stsSessionPolicy(context.Background(), commandRole)
	if err != nil {
		t.Fatal(err)
	}
	if commandRole.Arn == role.Arn {
		t.Fatal("deploy and command roles were not separated")
	}
	// Actual signed CFN provider authorization, not a standalone evaluator only.
	authority := NewSTSAuthority("")
	credentials, e := authority.Issue(STSSessionRequest{Principal: role.RoleName, RoleARN: role.Arn, RoleID: role.RoleID, SessionName: "contract", Duration: 15 * time.Minute, Validate: func(ctx context.Context) error {
		current, e := gateway.stsRole(ctx, role.Arn)
		if e != nil {
			return e
		}
		if current.RoleID != role.RoleID {
			return fmt.Errorf("role generation changed")
		}
		return nil
	}, Snapshot: func(ctx context.Context) (core.Policy, error) { return gateway.stsSessionPolicy(ctx, role) }})
	if e != nil {
		t.Fatal(e)
	}
	roleOnly := `{"Resources":{"Viewer":{"Type":"AWS::IAM::Role","Properties":{"RoleName":"tc-hello-world-battle-contract-viewer","AssumeRolePolicyDocument":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"arn:aws:iam::999999999999:root"}}]},"Policies":[{"PolicyName":"ReadOnly","PolicyDocument":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"arn:aws:ssm:ap-northeast-1:000000000000:parameter/tc-hello-world-battle-contract"}]}}]}}}}`
	for _, action := range []string{"CreateStack", "DeleteStack"} {
		form := url.Values{"Action": {action}, "Version": {"2010-05-15"}, "StackName": {"tc-hello-world-battle-contract"}}
		if action == "CreateStack" {
			form.Set("TemplateBody", roleOnly)
			form.Set("Capabilities.member.1", "CAPABILITY_NAMED_IAM")
		}
		signed := bootstrapSignedCFN(credentials, form.Encode())
		request, identity, e := authority.Authenticate(signed)
		if e != nil {
			t.Fatal(e)
		}
		// The outer Access layer sets this from the authenticated identity, never
		// from a participant-selected header. Its separate tests cover that binding.
		request.Header.Set("X-Simulator-Principal", identity.Principal)
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, request)
		if w.Code != 200 {
			t.Fatalf("actual signed CFN %s rejected bootstrap role: HTTP%d %s", action, w.Code, w.Body.String())
		}
	}

	root := SDKIdentity{Account: "999999999999", ARN: "arn:aws:iam::999999999999:root", Principal: core.Administrator}
	allowed, err := stsTrustAllows(role.AssumeRolePolicyDocument, root, "private-installation-external-id-123456")
	if err != nil || !allowed {
		t.Fatal("actual Host operator cannot assume generated role", err)
	}
	for _, tc := range []struct {
		caller   SDKIdentity
		external string
	}{{root, "wrong-external-id"}, {SDKIdentity{Account: "000000000000", ARN: "arn:aws:sts::000000000000:assumed-role/viewer/alice"}, "private-installation-external-id-123456"}} {
		ok, e := stsTrustAllows(role.AssumeRolePolicyDocument, tc.caller, tc.external)
		if e == nil && ok {
			t.Fatal("wrong trust context accepted")
		}
	}
	allowed, err = stsTrustAllows(commandRole.AssumeRolePolicyDocument, root, "private-installation-external-id-123456")
	if err != nil || !allowed {
		t.Fatal("Host cannot assume command role", err)
	}
	if ok, _ := stsTrustAllows(commandRole.AssumeRolePolicyDocument, root, "wrong-external-id"); ok {
		t.Fatal("command role trusts wrong ExternalId")
	}
	if ok, _ := stsTrustAllows(commandRole.AssumeRolePolicyDocument, SDKIdentity{Account: "000000000000", ARN: "arn:aws:sts::000000000000:assumed-role/viewer/alice"}, "private-installation-external-id-123456"); ok {
		t.Fatal("command role trusts participant")
	}
	original, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if err != nil {
		t.Fatal(err)
	}
	battle, err := compileStackWithParameters("ap-northeast-1", "tc-hello-world-battle-team-job", string(original), map[string]string{"NamePrefix": "tc-hello-world-battle-team-job", "TenkaCloudAccountId": "999999999999", "ExternalId": "private-installation-external-id-123456"}, "CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
		out := map[string]string{}
		for key := range keys {
			out[key] = "ami-local-pinned"
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, deleting := range []bool{false, true} {
		count := 0
		err = stackResourcePermissions(battle, deleting, func(action, resource string) error {
			count++
			if core.EvaluateWithContext(policy, action, resource, nil) != "allowed" {
				return fmt.Errorf("actual Host bootstrap denies %s %s (delete=%v)", action, resource, deleting)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatal("empty permission preflight")
		}
	}
	for _, action := range []string{"cloudformation:CreateStack", "cloudformation:DescribeStacks", "cloudformation:DeleteStack"} {
		if core.EvaluateWithContext(policy, action, battle.ID, nil) != "allowed" {
			t.Fatalf("denied stack lifecycle %s %s", action, battle.ID)
		}
	}
	// Native command authorization always supplies the verified user identity,
	// even when the resource is a document or a resource-less recovery action.
	commandContext := core.PolicyContext{"aws:userid": commandRole.RoleID + ":contract"}
	document := "arn:aws:ssm:ap-northeast-1::document/AWS-RunShellScript"
	instance := networkARN(battle.Region, "instance", networkID(battle, "Ec2"))
	tags := core.PolicyContext{"aws:userid": commandRole.RoleID + ":contract", "ssm:resourceTag/TenkaCloud:Problem": "hello-world-battle", "ssm:resourceTag/TenkaCloud:NamePrefix": "tc-hello-world-battle-team-job"}
	if core.EvaluateWithContext(commandPolicy, "ssm:SendCommand", document, commandContext) != "allowed" || core.EvaluateWithContext(commandPolicy, "ssm:SendCommand", instance, tags) != "allowed" {
		t.Fatal("valid Battle command denied")
	}
	for _, bad := range []core.PolicyContext{nil, {"ssm:resourceTag/TenkaCloud:Problem": "other", "ssm:resourceTag/TenkaCloud:NamePrefix": "tc-hello-world-battle-team-job"}, {"ssm:resourceTag/TenkaCloud:Problem": "hello-world-battle", "ssm:resourceTag/TenkaCloud:NamePrefix": "unrelated"}} {
		if core.EvaluateWithContext(commandPolicy, "ssm:SendCommand", instance, bad) == "allowed" {
			t.Fatal("wrong or missing Battle tags accepted")
		}
	}
	for _, action := range []string{"ssm:GetCommandInvocation", "ssm:ListCommands"} {
		if core.EvaluateWithContext(commandPolicy, action, "*", commandContext) != "allowed" {
			t.Fatal("recovery action denied", action)
		}
	}
	for _, tc := range []struct{ action, resource string }{{"iam:PassRole", "arn:aws:iam::000000000000:role/unrelated"}, {"ssm:SendCommand", strings.Replace(instance, "ap-northeast-1", "us-west-2", 1)}, {"ssm:SendCommand", "arn:aws:ssm:ap-northeast-1::document/OtherDocument"}, {"cloudformation:DeleteStack", "arn:aws:cloudformation:ap-northeast-1:000000000000:stack/unrelated/one"}} {
		if core.EvaluateWithContext(policy, tc.action, tc.resource, tags) == "allowed" || core.EvaluateWithContext(commandPolicy, tc.action, tc.resource, tags) == "allowed" {
			t.Fatal("scope expansion", tc)
		}
	}
	for _, action := range []string{"ssm:SendCommand", "ssm:GetCommandInvocation", "ssm:ListCommands"} {
		for _, resource := range []string{document, instance, "*"} {
			if core.EvaluateWithContext(policy, action, resource, tags) == "allowed" {
				t.Fatal("deploy role has command permission", action)
			}
		}
	}
	for _, resource := range []string{battle.ID, "*"} {
		if core.EvaluateWithContext(commandPolicy, "cloudformation:CreateStack", resource, commandContext) == "allowed" {
			t.Fatal("command role has deploy permission")
		}
	}
	participantFound := false
	for _, r := range battle.Resources {
		if r.LogicalID == "ParticipantViewerRole" {
			participantFound = true
			participant, e := core.ParsePolicy([]byte(stringValue(r.Properties["SimulatorPolicy"])))
			if e != nil {
				t.Fatal(e)
			}
			for _, action := range []string{"ssm:SendCommand", "ssm:GetCommandInvocation", "ssm:ListCommands"} {
				for _, resource := range []string{instance, document, "*"} {
					if core.EvaluateWithContext(participant, action, resource, tags) == "allowed" {
						t.Fatalf("original participant acquired operator command %s", action)
					}
				}
			}
		}
	}
	if !participantFound {
		t.Fatal("original participant policy was not checked")
	}
	if err = stacks.Delete(bootstrap.Region, bootstrap.ID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"RoleArn", "CommandRoleArn"} {
		if _, err = gateway.stsRole(context.Background(), bootstrap.Outputs[key]); err == nil {
			t.Fatal("deleted bootstrap role remained assumable", key)
		}
	}
}

func bootstrapSignedCFN(c STSCredentials, body string) *http.Request {
	at := time.Now().UTC()
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Amz-Date", at.Format("20060102T150405Z"))
	r.Header.Set("X-Amz-Security-Token", c.SessionToken)
	headers := "content-type;host;x-amz-date;x-amz-security-token"
	canonical := "content-type:application/x-www-form-urlencoded\nhost:127.0.0.1:4566\nx-amz-date:" + r.Header.Get("X-Amz-Date") + "\nx-amz-security-token:" + c.SessionToken + "\n"
	digest := sha256.Sum256([]byte(body))
	request := "POST\n/\n\n" + canonical + "\n" + headers + "\n" + hex.EncodeToString(digest[:])
	hash := sha256.Sum256([]byte(request))
	date := at.Format("20060102")
	scope := date + "/ap-northeast-1/cloudformation/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + r.Header.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	key := sdkHMAC(sdkHMAC(sdkHMAC([]byte("AWS4"+c.SecretAccessKey), date), "ap-northeast-1"), "cloudformation")
	signature := sdkHMAC(sdkHMAC(key, "aws4_request"), toSign)
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", c.AccessKeyID, scope, headers, signature))
	return r
}
