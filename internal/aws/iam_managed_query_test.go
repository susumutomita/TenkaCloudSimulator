package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type managedQueryProbe struct {
	t       *testing.T
	gateway *Gateway
}

func newManagedQueryProbe(t *testing.T) managedQueryProbe {
	t.Helper()
	db, s3 := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return managedQueryProbe{t, &Gateway{Store: db, S3: s3, Roles: roles, Identities: core.NewIdentities(db)}}
}
func (p managedQueryProbe) call(action, principal string, input url.Values, status int, contains string) string {
	p.t.Helper()
	if input == nil {
		input = url.Values{}
	}
	input.Set("Action", action)
	input.Set("Version", "2010-05-08")
	r := httptest.NewRequest("POST", "/", strings.NewReader(input.Encode()))
	r.Header.Set("X-Simulator-Service", "iam")
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	w := httptest.NewRecorder()
	p.gateway.ServeHTTP(w, r)
	if w.Code != status || !strings.Contains(w.Body.String(), contains) {
		p.t.Fatalf("%s: status %d response %s", action, w.Code, w.Body.String())
	}
	return w.Body.String()
}
func queryPolicy(t *testing.T, body string) IAMManagedPolicy {
	t.Helper()
	var out struct {
		Result struct{ Policy IAMManagedPolicy } `xml:"CreatePolicyResult"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Result.Policy
}
func queryMarker(t *testing.T, body, result string) string {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal("Missing marker result", result, err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == result {
			var page struct{ Marker string }
			if err = decoder.DecodeElement(&page, &start); err != nil || page.Marker == "" {
				t.Fatal("Missing marker", result, err)
			}
			return page.Marker
		}
	}
}

const managedQueryAllow = `{"Version":"2012-10-17", "Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::managed-query/*"}]}`
const managedQueryDeny = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::managed-query/*"}]}`

func (p managedQueryProbe) create(name string) IAMManagedPolicy {
	p.t.Helper()
	return queryPolicy(p.t, p.call("CreatePolicy", "", url.Values{"PolicyName": {name}, "PolicyDocument": {managedQueryAllow}, "Description": {"dummy metadata"}}, 200, "<DefaultVersionId>v1</DefaultVersionId>"))
}
func (p managedQueryProbe) role(name string) {
	p.t.Helper()
	p.call("CreateRole", "", url.Values{"RoleName": {name}, "AssumeRolePolicyDocument": {lambdaServiceTrust}}, 200, "CreateRoleResponse")
}
func (p managedQueryProbe) decision(role, want string) {
	p.t.Helper()
	got, err := p.gateway.Identities.DecisionContext(context.Background(), role, "s3:GetObject", "arn:aws:s3:::managed-query/key")
	if err != nil || got != want {
		p.t.Fatal("managed native authority", role, got, want, err)
	}
}

func TestIAMManagedQueryTwelveOperationsAndDocumentProjection(t *testing.T) {
	p := newManagedQueryProbe(t)
	a := p.create("Alpha")
	b := p.create("Beta")
	if !a.IsAttachable || a.PolicyID == "" {
		t.Fatal("customer policy metadata missing")
	}
	p.call("GetPolicy", "", url.Values{"PolicyArn": {a.Arn}}, 200, "<Description>dummy metadata</Description>")
	body := p.call("GetPolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "VersionId": {"v1"}}, 200, iamEncoded(managedQueryAllow))
	if strings.Contains(body, "+%22") {
		t.Fatal("RFC3986 document used form-space encoding")
	}
	body = p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "MaxItems": {"1"}}, 200, "<IsTruncated>true</IsTruncated>")
	if strings.Contains(body, "<Description>") || strings.Contains(body, "<Document>") {
		t.Fatal("list leaked document/description")
	}
	marker := queryMarker(t, body, "ListPoliciesResult")
	p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "Marker": {marker}}, 200, "<PolicyName>Beta</PolicyName>")
	p.role("First")
	p.role("Second")
	p.decision("First", "implicitDeny")
	for _, role := range []string{"First", "Second"} {
		p.call("AttachRolePolicy", "", url.Values{"RoleName": {role}, "PolicyArn": {a.Arn}}, 200, "AttachRolePolicyResponse")
		p.decision(role, "allowed")
	}
	p.call("AttachRolePolicy", "", url.Values{"RoleName": {"First"}, "PolicyArn": {a.Arn}}, 200, "AttachRolePolicyResponse")
	p.call("AttachRolePolicy", "", url.Values{"RoleName": {"First"}, "PolicyArn": {b.Arn}}, 200, "AttachRolePolicyResponse")
	body = p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"First"}, "MaxItems": {"1"}}, 200, "<IsTruncated>true</IsTruncated>")
	edgeMarker := queryMarker(t, body, "ListAttachedRolePoliciesResult")
	p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"First"}, "Marker": {edgeMarker}}, 200, "<PolicyName>Beta</PolicyName>")
	p.call("DetachRolePolicy", "", url.Values{"RoleName": {"First"}, "PolicyArn": {b.Arn}}, 200, "DetachRolePolicyResponse")
	body = p.call("CreatePolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "PolicyDocument": {managedQueryDeny}}, 200, "<VersionId>v2</VersionId>")
	if strings.Contains(body, "<Document>") {
		t.Fatal("create version exposed document")
	}
	p.decision("First", "allowed")
	body = p.call("ListPolicyVersions", "", url.Values{"PolicyArn": {a.Arn}, "MaxItems": {"1"}}, 200, "<IsTruncated>true</IsTruncated>")
	if strings.Contains(body, "<Document>") {
		t.Fatal("version list exposed document")
	}
	versionMarker := queryMarker(t, body, "ListPolicyVersionsResult")
	p.call("ListPolicyVersions", "", url.Values{"PolicyArn": {a.Arn}, "Marker": {versionMarker}}, 200, "<VersionId>v2</VersionId>")
	p.call("SetDefaultPolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "VersionId": {"v2"}}, 200, "SetDefaultPolicyVersionResponse")
	p.decision("First", "explicitDeny")
	p.decision("Second", "explicitDeny")
	p.call("DeletePolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "VersionId": {"v2"}}, 409, "DeleteConflict")
	p.call("SetDefaultPolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "VersionId": {"v1"}}, 200, "SetDefaultPolicyVersionResponse")
	p.call("DeletePolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "VersionId": {"v2"}}, 200, "DeletePolicyVersionResponse")
	p.call("DeletePolicy", "", url.Values{"PolicyArn": {a.Arn}}, 409, "DeleteConflict")
	p.call("DeleteRole", "", url.Values{"RoleName": {"First"}}, 409, "DeleteConflict")
	for _, role := range []string{"First", "Second"} {
		p.call("DetachRolePolicy", "", url.Values{"RoleName": {role}, "PolicyArn": {a.Arn}}, 200, "DetachRolePolicyResponse")
		p.decision(role, "implicitDeny")
		p.call("DeleteRole", "", url.Values{"RoleName": {role}}, 200, "DeleteRoleResponse")
	}
	p.call("DeletePolicy", "", url.Values{"PolicyArn": {a.Arn}}, 200, "DeletePolicyResponse")
	p.call("DeletePolicy", "", url.Values{"PolicyArn": {b.Arn}}, 200, "DeletePolicyResponse")
}

func TestIAMManagedQueryValidationAuthorizationAndMarkerContexts(t *testing.T) {
	p := newManagedQueryProbe(t)
	a := p.create("Alpha")
	p.role("Reader")
	for _, input := range []url.Values{nil, {"Scope": {"All"}}, {"Scope": {"AWS"}}, {"Scope": {"Local"}, "OnlyAttached": {"false"}}, {"Scope": {"Local"}, "PolicyUsageFilter": {"PermissionsPolicy"}}, {"Scope": {"Local"}, "PathPrefix": {"/nested/"}}} {
		p.call("ListPolicies", "", input, 501, "NotImplemented")
	}
	p.call("CreatePolicy", "", url.Values{"PolicyName": {"Nested"}, "PolicyDocument": {managedQueryAllow}, "Path": {"/nested/"}}, 501, "NotImplemented")
	for _, max := range []string{"", "0", "1001", "x"} {
		p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "MaxItems": {max}}, 400, "InvalidInput")
	}
	for _, flag := range []string{"", "1", "TRUE"} {
		p.call("CreatePolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "PolicyDocument": {managedQueryAllow}, "SetAsDefault": {flag}}, 400, "InvalidInput")
	}
	p.call("CreatePolicy", "", url.Values{"PolicyName": {"Extra"}, "PolicyDocument": {managedQueryAllow}, "Tags.member.1.Key": {"unsupported"}}, 501, "NotImplemented")
	p.call("GetPolicy", "nobody", url.Values{"PolicyArn": {a.Arn}}, 403, "AccessDenied")
	allow, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:AttachRolePolicy","Resource":"arn:aws:iam::000000000000:role/Reader"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.gateway.Identities.Set("attacher", allow); err != nil {
		t.Fatal(err)
	}
	p.call("AttachRolePolicy", "attacher", url.Values{"RoleName": {"Reader"}, "PolicyArn": {a.Arn}}, 200, "AttachRolePolicyResponse")
	p.call("GetPolicy", "attacher", url.Values{"PolicyArn": {a.Arn}}, 403, "AccessDenied") // attach requires no invented GetPolicy grant
	context := iamManagedQueryMarker{Action: "ListPolicies", Scope: "Local", Prefix: "/", After: "alpha"}
	raw := iamManagedQueryEncodeMarker(context)
	p.call("ListPolicyVersions", "", url.Values{"PolicyArn": {a.Arn}, "Marker": {raw}}, 400, "InvalidInput")
	for _, bad := range []string{"!", strings.Repeat("x", 2049), base64.RawURLEncoding.EncodeToString([]byte(`{}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"action":"ListPolicies","scope":"Local","prefix":"/","parentArn":"","parentId":"","after":"Alpha"}`))} {
		p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "Marker": {bad}}, 400, "InvalidInput")
	}
	for _, auth := range []struct{ authorization, session string }{{"AWS4-HMAC-SHA256 Credential=FOREIGNDUMMY/20260101/us-east-1/iam/aws4_request", ""}, {"", "dummy-session"}} {
		r := httptest.NewRequest("POST", "/", strings.NewReader("Action=ListPolicies&Version=2010-05-08&Scope=Local"))
		r.Header.Set("X-Simulator-Service", "iam")
		r.Header.Set("Authorization", auth.authorization)
		r.Header.Set("X-Amz-Security-Token", auth.session)
		w := httptest.NewRecorder()
		p.gateway.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("managed branch bypassed shared credential checks", w.Code)
		}
	}
	for _, body := range []string{"Action=GetPolicy&Version=wrong", "Action=ListPolicies&Version=2010-05-08&Scope=Local&Scope=Local", "Action=CreatePolicy&Version=2010-05-08&PolicyName=Bad&PolicyDocument=%ZZ"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("X-Simulator-Service", "iam")
		w := httptest.NewRecorder()
		p.gateway.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("managed branch bypassed shared strict Query validation", w.Code)
		}
	}
}

func TestIAMManagedQueryCursorParentGenerationAndCanonicalJSON(t *testing.T) {
	p := newManagedQueryProbe(t)
	a := p.create("Generation")
	p.create("Second")
	p.role("Paged")
	p.call("CreatePolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "PolicyDocument": {managedQueryAllow}}, 200, "v2")
	versionMarker := queryMarker(t, p.call("ListPolicyVersions", "", url.Values{"PolicyArn": {a.Arn}, "MaxItems": {"1"}}, 200, "<Marker>"), "ListPolicyVersionsResult")
	p.call("DeletePolicyVersion", "", url.Values{"PolicyArn": {a.Arn}, "VersionId": {"v2"}}, 200, "DeletePolicyVersionResponse")
	p.call("DeletePolicy", "", url.Values{"PolicyArn": {a.Arn}}, 200, "DeletePolicyResponse")
	recreated := p.create("Generation")
	if recreated.PolicyID == a.PolicyID {
		t.Fatal("policy generation reused")
	}
	p.call("ListPolicyVersions", "", url.Values{"PolicyArn": {a.Arn}, "Marker": {versionMarker}}, 400, "InvalidInput")
	for _, arn := range []string{a.Arn, iamManagedQueryPolicyPrefix + "Second"} {
		p.call("AttachRolePolicy", "", url.Values{"RoleName": {"Paged"}, "PolicyArn": {arn}}, 200, "AttachRolePolicyResponse")
	}
	edgeMarker := queryMarker(t, p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"Paged"}, "MaxItems": {"1"}}, 200, "<Marker>"), "ListAttachedRolePoliciesResult")
	for _, arn := range []string{a.Arn, iamManagedQueryPolicyPrefix + "Second"} {
		p.call("DetachRolePolicy", "", url.Values{"RoleName": {"Paged"}, "PolicyArn": {arn}}, 200, "DetachRolePolicyResponse")
	}
	p.call("DeleteRole", "", url.Values{"RoleName": {"Paged"}}, 200, "DeleteRoleResponse")
	p.role("Paged")
	p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"Paged"}, "Marker": {edgeMarker}}, 400, "InvalidInput")
	good := iamManagedQueryMarker{Action: "ListPolicies", Scope: "Local", Prefix: "/", After: "generation"}
	data, _ := json.Marshal(good)
	for _, bad := range [][]byte{append([]byte(" "), data...), []byte(strings.Replace(string(data), `"scope":"Local"`, `"scope":"Local","scope":"Local"`, 1)), []byte(strings.Replace(string(data), `"scope"`, `"Scope"`, 1))} {
		if _, err := iamManagedQueryDecodeMarker(base64.RawURLEncoding.EncodeToString(bad), iamManagedQueryMarker{Action: "ListPolicies", Scope: "Local", Prefix: "/"}); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}
