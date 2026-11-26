package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestEC2SecurityGroupsConsoleCapability(t *testing.T) {
	operator := provider.Actor{CredentialIdentity: "simulator-admin", ExerciseIdentity: "selected-reader", CredentialRole: provider.CredentialOperator}
	participant := provider.Actor{CredentialIdentity: "learner", ExerciseIdentity: "learner", CredentialRole: provider.CredentialParticipant}
	for _, c := range []struct {
		label  string
		auth   bool
		actor  *provider.Actor
		scoped bool
		want   bool
	}{
		{"plain embedding", false, nil, false, false},
		{"no authenticated actor", true, nil, false, false},
		{"actor without Access wrapper", false, &operator, false, false},
		{"ordinary operator", true, &operator, false, true},
		{"ordinary participant", true, &participant, false, true},
		{"instance handoff", true, &participant, true, false},
	} {
		t.Run(c.label, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/_simulator/v1/capabilities", nil)
			if c.actor != nil {
				r = r.WithContext(provider.WithActor(r.Context(), *c.actor))
			}
			if c.scoped {
				r = r.WithContext(context.WithValue(r.Context(), consoleScopeKey{}, consoleScope{Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}))
			}
			w := httptest.NewRecorder()
			if c.auth {
				w.Header().Set("X-Simulator-Authentication", "required")
			}
			ec2 := map[string]any{"console": true}
			capabilities := map[string]any{"services": map[string]any{"ec2": ec2}}
			addEC2NetworkReadbackCapabilities(w, r, capabilities)
			// Pin the exact helper contract independently: capability claims are
			// available to ordinary signed-in sessions, not to a scoped handoff.
			// The route capabilities do not grant IAM or advertise forwarding.
			want := map[string]any{"console": true}
			if c.want {
				want["securityGroupReadback"] = "DescribeSecurityGroups"
				want["routeTableReadback"] = "DescribeRouteTables"
				want["internetGatewayLifecycle"] = []string{"DescribeInternetGateways", "CreateInternetGateway", "DeleteInternetGateway", "AttachInternetGateway", "DetachInternetGateway"}
				want["internetGatewayPageLimit"] = 100
				want["subnetLifecycle"] = []string{"DescribeSubnets", "CreateSubnet", "DeleteSubnet"}
				want["routeTableLifecycle"] = []string{"DescribeRouteTables", "CreateRouteTable", "DeleteRouteTable"}
				want["routeTableAssociationLifecycle"] = []string{"DescribeRouteTables", "DescribeSubnets", "AssociateRouteTable", "DisassociateRouteTable"}
				want["defaultRouteLifecycle"] = []string{"DescribeRouteTables", "DescribeInternetGateways", "CreateRoute", "DeleteRoute"}
			}
			if !reflect.DeepEqual(ec2, want) {
				t.Fatalf("unexpected exact EC2 network capabilities: got %+v, want %+v", ec2, want)
			}
		})
	}
}

// The console's exact Query requests use a normal signed-in cookie, the
// real Access handler and native SQLite state. No browser driver or socket.
func TestEC2SecurityGroupConsoleNativeReadback(t *testing.T) {
	h, closeDB, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeDB(); err != nil {
			t.Error(err)
		}
	})
	operator := strings.Repeat("s", 64)
	access, err := NewAccess(h, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	operatorCookie := browserSignIn(t, access, operator, nil)
	query := func(cookie *http.Cookie, principal, service string, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		return browserRequest(access, "POST", "/", values.Encode(), "", cookie, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": service,
			"X-Simulator-Region": "us-east-1", "X-Simulator-Principal": principal,
		})
	}
	const template = `{"Resources":{
		"VPC":{"Type":"AWS::EC2::VPC","Properties":{"CidrBlock":"10.42.0.0/16"}},
		"RouteTable":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"VPC"}}},
		"LocalGroup":{"Type":"AWS::EC2::SecurityGroup","Properties":{
			"VpcId":{"Ref":"VPC"},"GroupDescription":"Local saved ingress",
			"Tags":[{"Key":"Course","Value":""}],
			"SecurityGroupIngress":[{"IpProtocol":"tcp","FromPort":8080,"ToPort":8080,"CidrIp":"192.0.2.129/25"}]
		}}
	}}`
	const ok = http.StatusOK
	created := query(operatorCookie, "simulator-admin", "cloudformation", url.Values{
		"Action": {"CreateStack"}, "Version": {"2010-05-15"}, "StackName": {"sg-console-fixture"}, "TemplateBody": {template},
	})
	if created.Code != ok {
		t.Fatalf("create saved fixture stack: %d %s", created.Code, created.Body.String())
	}
	defer func() {
		deleted := query(operatorCookie, "simulator-admin", "cloudformation",
			url.Values{"Action": {"DeleteStack"}, "Version": {"2010-05-15"}, "StackName": {"sg-console-fixture"}})
		if deleted.Code != ok {
			t.Errorf("delete saved fixture stack: %d %s", deleted.Code, deleted.Body.String())
		}
	}()

	writePolicy := func(name, raw string) {
		t.Helper()
		w := browserRequest(access, "PUT", "/_simulator/v1/iam/principals/"+name+"/policy", raw, "", operatorCookie,
			map[string]string{"Content-Type": "application/json"})
		if w.Code != ok {
			t.Fatalf("set fixture identity policy: %d %s", w.Code, w.Body.String())
		}
	}
	writePolicy("group-reader", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ec2:DescribeVpcs","ec2:DescribeSecurityGroups"],"Resource":"*"}]}`)
	writePolicy("reader", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ec2:DescribeVpcs","ec2:DescribeRouteTables"],"Resource":"*"}]}`)
	learnerCookie := browserSignIn(t, access, participantCredential(t, access, operator), nil)

	for _, tc := range []struct {
		name      string
		principal string
		cookie    *http.Cookie
		wantGroup int
		wantRoute int
	}{
		{"operator with selected exercise identity", "group-reader", operatorCookie, ok, http.StatusForbidden},
		{"ordinary learner without group IAM", "reader", learnerCookie, http.StatusForbidden, ok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capabilities := browserRequest(access, "GET", "/_simulator/v1/capabilities", "", "", tc.cookie,
				map[string]string{"X-Simulator-Principal": tc.principal})
			var c struct {
				Services struct {
					EC2 struct {
						Readback string `json:"securityGroupReadback"`
						Routes   string `json:"routeTableReadback"`
					} `json:"ec2"`
				} `json:"services"`
			}
			if capabilities.Code != ok || capabilities.Header().Get("X-Simulator-Authentication") != "required" ||
				json.Unmarshal(capabilities.Body.Bytes(), &c) != nil || c.Services.EC2.Readback != "DescribeSecurityGroups" || c.Services.EC2.Routes != "DescribeRouteTables" {
				t.Fatal("signed-in VPC pane was not advertised")
			}
			vpcs := query(tc.cookie, tc.principal, "ec2", url.Values{"Action": {"DescribeVpcs"}, "Version": {"2016-11-15"}})
			var view struct {
				VPCs []struct {
					ID        string `xml:"vpcId"`
					ManagedBy string `xml:"simulatorManagedBy"`
				} `xml:"vpcSet>item"`
			}
			if vpcs.Code != ok || xml.Unmarshal(vpcs.Body.Bytes(), &view) != nil || len(view.VPCs) != 1 ||
				view.VPCs[0].ID == "" || view.VPCs[0].ManagedBy != "cloudformation" {
				t.Fatalf("missing canonically selectable VPC: %d %s", vpcs.Code, vpcs.Body.String())
			}
			routes := query(tc.cookie, tc.principal, "ec2", url.Values{"Action": {"DescribeRouteTables"}, "Version": {"2016-11-15"}})
			if routes.Code != tc.wantRoute {
				t.Fatalf("native route-table read status %d, want %d", routes.Code, tc.wantRoute)
			}
			if tc.wantRoute == http.StatusForbidden {
				if !strings.Contains(routes.Body.String(), "<Code>UnauthorizedOperation</Code>") || strings.Contains(routes.Body.String(), "<routeTableSet>") {
					t.Fatal("unauthorized route reader received the wrong result")
				}
			} else {
				var routeView struct {
					XMLName xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeRouteTablesResponse"`
					Tables  []struct {
						ID  string `xml:"routeTableId"`
						VPC string `xml:"vpcId"`
					} `xml:"routeTableSet>item"`
				}
				if xml.Unmarshal(routes.Body.Bytes(), &routeView) != nil || len(routeView.Tables) != 1 ||
					!strings.HasPrefix(routeView.Tables[0].ID, "rtb-") || routeView.Tables[0].VPC != view.VPCs[0].ID {
					t.Fatal("native route table does not belong to the selected VPC")
				}
			}
			groups := query(tc.cookie, tc.principal, "ec2", url.Values{"Action": {"DescribeSecurityGroups"}, "Version": {"2016-11-15"}})
			if groups.Code != tc.wantGroup {
				t.Fatalf("native group read status %d, want %d: %s", groups.Code, tc.wantGroup, groups.Body.String())
			}
			if tc.wantGroup == http.StatusForbidden {
				if !strings.Contains(groups.Body.String(), "<Code>UnauthorizedOperation</Code>") || strings.Contains(groups.Body.String(), "Local saved ingress") {
					t.Fatal("IAM refusal was not preserved")
				}
				return
			}
			var native struct {
				XMLName xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeSecurityGroupsResponse"`
				ID      string   `xml:"requestId"`
				Groups  []struct {
					GroupID     string `xml:"groupId"`
					VPC         string `xml:"vpcId"`
					Description string `xml:"groupDescription"`
					CIDR        string `xml:"ipPermissions>item>ipRanges>item>cidrIp"`
					TagKey      string `xml:"tagSet>item>key"`
				} `xml:"securityGroupInfo>item"`
			}
			if xml.Unmarshal(groups.Body.Bytes(), &native) != nil || native.ID == "" || len(native.Groups) != 1 {
				t.Fatalf("invalid native group read: %s", groups.Body.String())
			}
			g := native.Groups[0]
			if g.GroupID == "" || g.VPC != view.VPCs[0].ID || g.Description != "Local saved ingress" ||
				g.CIDR != "192.0.2.128/25" || g.TagKey != "Course" {
				t.Fatalf("native group read disagrees with exact saved VPC/rule/tag: %+v", g)
			}
		})
	}
}
