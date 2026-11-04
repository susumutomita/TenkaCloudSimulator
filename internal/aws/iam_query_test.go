package aws

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestNativeIAMQueryLifecyclePaginationAndAuthorization(t *testing.T) {
	db, s3 := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	identities := core.NewIdentities(db)
	g := &Gateway{Store: db, S3: s3, Roles: roles, Identities: identities}
	call := func(action, principal string, fields url.Values, status int, contains string) string {
		t.Helper()
		if fields == nil {
			fields = url.Values{}
		}
		fields.Set("Action", action)
		fields.Set("Version", "2010-05-08")
		r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode()))
		r.Header.Set("X-Simulator-Service", "iam")
		if principal != "" {
			r.Header.Set("X-Simulator-Principal", principal)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != status || !strings.Contains(w.Body.String(), contains) {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	for _, name := range []string{"Zebra", "alpha", "Beta"} {
		call("CreateRole", "", url.Values{"RoleName": {name}, "AssumeRolePolicyDocument": {lambdaServiceTrust}}, 200, "<RoleId>AROA")
	}
	first := call("ListRoles", "", url.Values{"MaxItems": {"2"}}, 200, "<IsTruncated>true</IsTruncated>")
	var page struct {
		Result struct {
			Marker string
			Roles  []IAMRole `xml:"Roles>member"`
		} `xml:"ListRolesResult"`
	}
	if err = xml.Unmarshal([]byte(first), &page); err != nil || len(page.Result.Roles) != 2 || page.Result.Roles[0].RoleName != "alpha" || page.Result.Roles[1].RoleName != "Beta" {
		t.Fatal(page, err)
	}
	last := call("ListRoles", "", url.Values{"Marker": {page.Result.Marker}, "MaxItems": {"2"}}, 200, "<IsTruncated>false</IsTruncated>")
	if !strings.Contains(last, "<RoleName>Zebra</RoleName>") || strings.Contains(last, "<RoleName>alpha</RoleName>") {
		t.Fatal(last)
	}
	call("GetRole", "", url.Values{"RoleName": {"ALPHA"}}, 200, "<RoleName>alpha</RoleName>")
	for _, name := range []string{"z", "a", "m"} {
		call("PutRolePolicy", "", url.Values{"RoleName": {"alpha"}, "PolicyName": {name}, "PolicyDocument": {nativeReadPolicy}}, 200, "PutRolePolicyResponse")
	}
	first = call("ListRolePolicies", "", url.Values{"RoleName": {"alpha"}, "MaxItems": {"2"}}, 200, "<IsTruncated>true</IsTruncated>")
	var policies struct {
		Result struct {
			Marker string
			Names  []string `xml:"PolicyNames>member"`
		} `xml:"ListRolePoliciesResult"`
	}
	if err = xml.Unmarshal([]byte(first), &policies); err != nil || strings.Join(policies.Result.Names, ",") != "a,m" {
		t.Fatal(policies, err)
	}
	call("ListRolePolicies", "", url.Values{"RoleName": {"alpha"}, "Marker": {policies.Result.Marker}}, 200, "<member>z</member>")
	encoded := call("GetRolePolicy", "", url.Values{"RoleName": {"alpha"}, "PolicyName": {"a"}}, 200, iamEncoded(nativeReadPolicy))
	if strings.Contains(encoded, "+%22") {
		t.Fatal("policy encoding used form spaces", encoded)
	}
	call("DeleteRole", "", url.Values{"RoleName": {"alpha"}}, 409, "DeleteConflict")
	for _, name := range []string{"a", "m", "z"} {
		call("DeleteRolePolicy", "", url.Values{"RoleName": {"alpha"}, "PolicyName": {name}}, 200, "DeleteRolePolicyResponse")
	}
	call("UpdateAssumeRolePolicy", "", url.Values{"RoleName": {"alpha"}, "PolicyDocument": {lambdaServiceTrust}}, 200, "UpdateAssumeRolePolicyResponse")
	call("DeleteRole", "", url.Values{"RoleName": {"alpha"}}, 200, "DeleteRoleResponse")
	call("GetRole", "", url.Values{"RoleName": {"alpha"}}, 404, "NoSuchEntity")
	call("GetRole", "nobody", url.Values{"RoleName": {"Beta"}}, 403, "AccessDenied")
	call("GetRole", "nobody", url.Values{"RoleName": {"absent"}}, 403, "AccessDenied")
	p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:GetRole","Resource":"arn:aws:iam::000000000000:role/Beta"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("reader", p); err != nil {
		t.Fatal(err)
	}
	call("GetRole", "reader", url.Values{"RoleName": {"beta"}}, 200, "<RoleName>Beta</RoleName>")
	call("DeleteRole", "reader", url.Values{"RoleName": {"Beta"}}, 403, "AccessDenied")
	call("ListRoles", "reader", nil, 403, "AccessDenied")
}

func TestNativeIAMQueryRejectsUnsupportedAndMalformedInputs(t *testing.T) {
	db, s3 := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: db, S3: s3, Roles: roles, Identities: core.NewIdentities(db)}
	valid := "Action=ListRoles&Version=2010-05-08"
	for _, tc := range []struct {
		name, method, path, body, auth, session string
		status                                  int
		code                                    string
	}{
		{"method", "GET", "/", valid, "", "", 400, "ValidationError"},
		{"path", "POST", "/roles", valid, "", "", 400, "ValidationError"},
		{"query", "POST", "/?Action=ListRoles", valid, "", "", 400, "ValidationError"},
		{"credentials", "POST", "/", valid, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20261104/us-east-1/iam/aws4_request", "", 403, "AccessDenied"},
		{"session", "POST", "/", valid, "", "session", 403, "AccessDenied"},
		{"bad-form", "POST", "/", "%zz", "", "", 400, "ValidationError"},
		{"version", "POST", "/", "Action=ListRoles&Version=wrong", "", "", 400, "ValidationError"},
		{"unknown", "POST", "/", "Action=CreateUser&Version=2010-05-08", "", "", 501, "NotImplemented"},
		{"duplicate", "POST", "/", valid + "&Action=ListRoles", "", "", 400, "ValidationError"},
		{"unknown-field", "POST", "/", valid + "&Tags.member.1.Key=x", "", "", 501, "NotImplemented"},
		{"invalid-name", "POST", "/", "Action=GetRole&Version=2010-05-08&RoleName=bad%2Fpath", "", "", 400, "ValidationError"},
		{"max-invalid", "POST", "/", valid + "&MaxItems=no", "", "", 400, "ValidationError"},
		{"max-zero", "POST", "/", valid + "&MaxItems=0", "", "", 400, "ValidationError"},
		{"max-large", "POST", "/", valid + "&MaxItems=1001", "", "", 400, "ValidationError"},
		{"marker-encoding", "POST", "/", valid + "&Marker=%25", "", "", 400, "ValidationError"},
		{"marker-value", "POST", "/", valid + "&Marker=" + base64.RawURLEncoding.EncodeToString([]byte("bad/path")), "", "", 400, "ValidationError"},
		{"path-prefix", "POST", "/", valid + "&PathPrefix=%2Funsupported%2F", "", "", 501, "NotImplemented"},
		{"body-limit", "POST", "/", strings.Repeat("x", (128<<10)+1), "", "", 413, "RequestEntityTooLarge"},
		{"role-path", "POST", "/", "Action=CreateRole&Version=2010-05-08&RoleName=test&Path=%2Fnested%2F", "", "", 501, "NotImplemented"},
		{"duration", "POST", "/", "Action=CreateRole&Version=2010-05-08&RoleName=test&MaxSessionDuration=no", "", "", 400, "ValidationError"},
		{"trust", "POST", "/", "Action=CreateRole&Version=2010-05-08&RoleName=test&AssumeRolePolicyDocument=%7B%7D", "", "", 400, "MalformedPolicyDocument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("X-Simulator-Service", "iam")
			r.Header.Set("Authorization", tc.auth)
			r.Header.Set("X-Amz-Security-Token", tc.session)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), "<Code>"+tc.code+"</Code>") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(valid))
	w := httptest.NewRecorder()
	(&Gateway{}).serveIAM(w, r)
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = httptest.NewRequest("POST", "/", strings.NewReader(valid)).WithContext(ctx)
	w = httptest.NewRecorder()
	g.serveIAM(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), "context canceled") {
		t.Fatal(w.Code, w.Body.String())
	}
	if isIAM(httptest.NewRequest(http.MethodGet, "/iam/object", nil)) {
		t.Fatal("S3 bucket path was reserved by IAM routing")
	}
}
