//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The installed producer is the actual trusted Battle constructor and real
// CloudFormation receiver. The positive capability is not a physical boot
// witness: the existing inert bundle fixture must not be executed.
func TestEC2InstanceStackLaunchDiscoveryFollowsConcreteRuntime(t *testing.T) {
	config, _ := inertBattleConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	config.Lifetime = ctx
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "Battle runtime"}[configured], func(t *testing.T) {
			h, closeAll, err := New(":memory:")
			if configured {
				if err == nil {
					if err = closeAll(); err != nil {
						t.Fatal(err)
					}
				}
				h, closeAll, err = NewWithBattleRuntime(":memory:", DefaultProviderConfig(), "http://127.0.0.1:4566", battleClock(), config)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeAll(); err != nil {
					t.Error(err)
				}
			}()
			token := strings.Repeat("o", 64)
			access, err := NewAccess(h, token, false)
			if err != nil {
				t.Fatal(err)
			}
			cookie := browserSignIn(t, access, token, nil)
			participant := browserSignIn(t, access, participantCredential(t, access, token), nil)
			discovery := func(principal string, admin, want bool) {
				t.Helper()
				req := httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/capabilities", nil)
				req.RemoteAddr = "127.0.0.1:7111"
				if admin {
					req.AddCookie(cookie)
				} else {
					req.AddCookie(participant)
				}
				req.Header.Set("X-Simulator-Principal", principal)
				got := httptest.NewRecorder()
				access.ServeHTTP(got, req)
				var doc struct {
					Services map[string]struct {
						Launch string `json:"instanceStackLaunch"`
					} `json:"services"`
				}
				if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &doc) != nil {
					t.Fatalf("capabilities status %d %s", got.Code, got.Body.String())
				}
				if x := doc.Services["ec2"].Launch; (want && x != "CloudFormation/CreateStack") || (!want && x != "") {
					t.Fatalf("capability = %q, expected availability %v", x, want)
				}
			}
			discovery("", true, configured)
			discovery("simulator-admin", true, configured)
			discovery("reader", true, false)
			discovery("", false, false)

			// This syntactically valid template deliberately selects an
			// unconfigured alias, so the installed Go runtime refuses before
			// VM work or stack commit. With no runtime, the same request hits
			// the existing explicit NotImplemented failure instead.
			template := `{"Resources":{
				"Vpc":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.91.0.0/16"}},
				"Subnet":{"Type":"AWS::EC2::Subnet","Properties":{"VpcId":{"Ref":"Vpc"},"CidrBlock":"10.91.1.0/24"}},
				"Group":{"Type":"AWS::EC2::SecurityGroup","Properties":{"VpcId":{"Ref":"Vpc"},"GroupDescription":"No ingress"}},
				"Role":{"Type":"AWS::IAM::Role","Properties":{"AssumeRolePolicyDocument":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}}},
				"Profile":{"Type":"AWS::IAM::InstanceProfile","Properties":{"Roles":[{"Ref":"Role"}]}},
				"Instance":{"Type":"AWS::EC2::Instance","Properties":{"ImageId":"ami-00000000","InstanceType":"t3.micro","SubnetId":{"Ref":"Subnet"},"SecurityGroupIds":[{"Ref":"Group"}],"IamInstanceProfile":{"Ref":"Profile"},"UserData":"I2Nsb3VkLWNvbmZpZwp7fQo="}}}}`
			headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "cloudformation", "X-Simulator-Region": "ap-northeast-1"}
			create := url.Values{"Action": {"CreateStack"}, "Version": {"2010-05-15"}, "StackName": {"ui-stack"}, "TemplateBody": {template}, "Capabilities.member.1": {"CAPABILITY_IAM"}}.Encode()
			got := browserRequest(access, "POST", "/", create, "", participant, headers)
			if got.Code != 403 || !strings.Contains(got.Body.String(), "<Code>AccessDenied</Code>") {
				t.Fatalf("participant create status %d %s", got.Code, got.Body.String())
			}
			got = browserRequest(access, "POST", "/", create, "", cookie, headers)
			code, xml := 501, "<Code>NotImplemented</Code>"
			if configured {
				code, xml = 500, "<Code>InternalFailure</Code>"
			}
			if got.Code != code || !strings.Contains(got.Body.String(), xml) {
				t.Fatalf("operator create status %d %s", got.Code, got.Body.String())
			}
			got = browserRequest(access, "POST", "/", "Action=DescribeStacks&Version=2010-05-15", "", cookie, headers)
			if got.Code != 200 || !strings.Contains(got.Body.String(), "<Stacks></Stacks>") {
				t.Fatalf("refusal left stack state: %d %s", got.Code, got.Body.String())
			}
			if configured {
				cancel()
				discovery("", true, false)
			}
		})
	}
}
