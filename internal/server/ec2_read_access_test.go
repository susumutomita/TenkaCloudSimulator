package server

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Exercise the real composition and Access boundary without opening a socket.
func TestEC2ReadAccessBoundary(t *testing.T) {
	h, closeDB, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeDB(); err != nil {
			t.Error(err)
		}
	})
	operator := strings.Repeat("a", 64)
	access, err := NewAccess(h, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		return w
	}
	issue := request("POST", "/_simulator/v1/access", `{"Principal":"ec2-reader"}`, operator, nil)
	var participant struct{ AccessToken string }
	if issue.Code != 200 || json.Unmarshal(issue.Body.Bytes(), &participant) != nil || participant.AccessToken == "" {
		t.Fatal("participant issuance failed", issue.Code)
	}
	for _, tc := range []struct {
		name, token, principal, session string
		status                          int
	}{
		{"operator", operator, "", "", 200},
		{"unauthenticated", "", "", "", 401},
		{"foreign authority", strings.Repeat("b", 64), "", "", 401},
		{"participant admin forgery", participant.AccessToken, "simulator-admin", "", 403},
		{"unknown selected identity", operator, "missing-ec2-identity", "", 403},
		{"participant missing policy", participant.AccessToken, "", "", 403},
		{"session token unsupported", operator, "", "synthetic-session-token", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request("POST", "/", "Action=DescribeVpcs&Version=2016-11-15", tc.token, map[string]string{
				"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2", "X-Simulator-Region": "us-east-1", "X-Simulator-Principal": tc.principal, "X-Amz-Security-Token": tc.session,
			})
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 200 && !strings.Contains(w.Body.String(), "DescribeVpcsResponse") {
				t.Fatal("missing native response", w.Body.String())
			}
			if tc.session != "" && !strings.Contains(w.Body.String(), "UnsupportedOperation") {
				t.Fatal("missing explicit session refusal", w.Body.String())
			}
		})
	}
	// Mutations pass the same token boundary; participants never gain admin.
	create := func(token, principal string) *httptest.ResponseRecorder {
		return request("POST", "/", "Action=CreateVpc&Version=2016-11-15&CidrBlock=10.9.0.0%2F16", token, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2", "X-Simulator-Region": "us-east-1", "X-Simulator-Principal": principal,
		})
	}
	for _, tc := range []struct {
		name, token, principal string
		status                 int
	}{
		{"unauthenticated create", "", "", 401},
		{"participant admin forgery create", participant.AccessToken, "simulator-admin", 403},
		{"participant missing policy create", participant.AccessToken, "", 403},
	} {
		if w := create(tc.token, tc.principal); w.Code != tc.status {
			t.Fatalf("%s: got %d want %d: %s", tc.name, w.Code, tc.status, w.Body.String())
		}
	}
	if w := request("POST", "/", "Action=DescribeVpcs&Version=2016-11-15", operator, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2"}); w.Code != 200 || strings.Contains(w.Body.String(), "<vpcId>") {
		t.Fatalf("refused creates changed state: %d %s", w.Code, w.Body.String())
	}
	if w := create(operator, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "<simulatorManagedBy>ec2</simulatorManagedBy>") {
		t.Fatalf("operator CreateVpc failed: %d %s", w.Code, w.Body.String())
	}
	// A native temporary credential attempt must not fall back to operator access.
	w := request("POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2",
		"Authorization":        "AWS4-HMAC-SHA256 Credential=TCSIMASIAINVALID/20261113/us-east-1/ec2/aws4_request, SignedHeaders=host, Signature=invalid",
		"X-Amz-Security-Token": "synthetic-session-token",
	})
	if w.Code != 403 {
		t.Fatalf("native invalid session fell through: %d", w.Code)
	}
	w = request("GET", "/_simulator/v1/capabilities", "", operator, nil)
	var caps struct {
		Services map[string]struct {
			Console                        bool                `json:"console"`
			ReadOnly                       bool                `json:"readOnly"`
			SessionCredentials             bool                `json:"sessionCredentials"`
			Operations                     []string            `json:"operations"`
			Pagination                     bool                `json:"pagination"`
			Filters                        bool                `json:"filters"`
			ResourceLimit                  int                 `json:"resourceLimit"`
			DescribeSelectors              map[string][]string `json:"describeSelectors"`
			DetachedConfigurationOnly      []string            `json:"detachedConfigurationOnly"`
			InternetGatewayLifecycle       []string            `json:"internetGatewayLifecycle"`
			InternetGatewayPageLimit       int                 `json:"internetGatewayPageLimit"`
			RouteTableLifecycle            []string            `json:"routeTableLifecycle"`
			RouteTableAssociationLifecycle []string            `json:"routeTableAssociationLifecycle"`
		} `json:"services"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil {
		t.Fatal("capabilities unavailable", w.Code)
	}
	ec2, exists := caps.Services["ec2"]
	if !exists || !ec2.Console || ec2.ReadOnly || ec2.SessionCredentials {
		t.Fatalf("unexpected EC2 access capabilities: %+v", ec2)
	}
	// The expected public subset is independent of the provider's registration.
	// Do not accept a missing operation or advertise any unsupported mutation.
	wantOperations := []string{
		"DescribeInstances", "DescribeVpcs", "DescribeSubnets", "DescribeSecurityGroups", "DescribeRouteTables",
		"CreateVpc", "DeleteVpc", "CreateSubnet", "DeleteSubnet", "CreateSecurityGroup", "DeleteSecurityGroup",
		"CreateNetworkAcl", "DescribeNetworkAcls", "DeleteNetworkAcl", "CreateRouteTable", "DeleteRouteTable",
		"AssociateRouteTable", "DisassociateRouteTable",
		"CreateInternetGateway", "DescribeInternetGateways", "DeleteInternetGateway", "AttachInternetGateway", "DetachInternetGateway",
	}
	if !reflect.DeepEqual(ec2.Operations, wantOperations) {
		t.Fatalf("unexpected EC2 operations: got %v, want %v", ec2.Operations, wantOperations)
	}
	wantSelectors := map[string][]string{
		"DescribeSecurityGroups":   {"GroupId.N", "vpc-id"},
		"DescribeNetworkAcls":      {"NetworkAclId.N", "vpc-id"},
		"DescribeInternetGateways": {"InternetGatewayId.N", "MaxResults", "NextToken"},
	}
	if ec2.Pagination || !ec2.Filters || ec2.ResourceLimit != 1000 || !reflect.DeepEqual(ec2.DescribeSelectors, wantSelectors) ||
		!reflect.DeepEqual(ec2.DetachedConfigurationOnly, []string{"native security groups", "native network ACLs"}) ||
		ec2.InternetGatewayPageLimit != 100 ||
		!reflect.DeepEqual(ec2.InternetGatewayLifecycle, []string{"DescribeInternetGateways", "CreateInternetGateway", "DeleteInternetGateway", "AttachInternetGateway", "DetachInternetGateway"}) ||
		!reflect.DeepEqual(ec2.RouteTableLifecycle, []string{"DescribeRouteTables", "CreateRouteTable", "DeleteRouteTable"}) ||
		!reflect.DeepEqual(ec2.RouteTableAssociationLifecycle, []string{"DescribeRouteTables", "DescribeSubnets", "AssociateRouteTable", "DisassociateRouteTable"}) {
		t.Fatalf("unexpected EC2 capability limits: %+v", ec2)
	}
}
