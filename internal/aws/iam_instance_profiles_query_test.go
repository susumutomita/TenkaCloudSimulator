package aws

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestIAMInstanceProfilesQuerySevenAPIsAndPagination(t *testing.T) {
	p := newManagedQueryProbe(t)
	p.role("Role")
	for _, name := range []string{"Zebra", "alpha", "Beta"} {
		p.call("CreateInstanceProfile", "", url.Values{"InstanceProfileName": {name}}, 200, "<InstanceProfileId>AIPA")
		p.call("AddRoleToInstanceProfile", "", url.Values{"InstanceProfileName": {name}, "RoleName": {"role"}}, 200, "AddRoleToInstanceProfileResponse")
	}
	body := p.call("GetInstanceProfile", "", url.Values{"InstanceProfileName": {"ALPHA"}}, 200, "<InstanceProfileName>alpha</InstanceProfileName>")
	var out struct {
		Result struct{ InstanceProfile IAMInstanceProfile } `xml:"GetInstanceProfileResult"`
	}
	if e := xml.Unmarshal([]byte(body), &out); e != nil || len(out.Result.InstanceProfile.Roles) != 1 || out.Result.InstanceProfile.Roles[0].AssumeRolePolicyDocument != iamEncoded(lambdaServiceTrust) {
		t.Fatal(out, e)
	}
	for _, action := range []string{"ListInstanceProfiles", "ListInstanceProfilesForRole"} {
		fields := url.Values{"MaxItems": {"2"}}
		if action == "ListInstanceProfilesForRole" {
			fields.Set("RoleName", "ROLE")
		}
		first := p.call(action, "", fields, 200, "<IsTruncated>true</IsTruncated>")
		if strings.Contains(first, "<InstanceProfileName>Zebra</InstanceProfileName>") {
			t.Fatal(first)
		}
		marker := queryMarker(t, first, action+"Result")
		fields.Set("Marker", marker)
		last := p.call(action, "", fields, 200, "<IsTruncated>false</IsTruncated>")
		if !strings.Contains(last, "<InstanceProfileName>Zebra</InstanceProfileName>") {
			t.Fatal(last)
		}
		fields.Set("Marker", "bad")
		p.call(action, "", fields, 400, "InvalidInput")
	}
	p.call("DeleteRole", "", url.Values{"RoleName": {"Role"}}, 409, "DeleteConflict")
	p.call("DeleteInstanceProfile", "", url.Values{"InstanceProfileName": {"alpha"}}, 409, "DeleteConflict")
	p.call("AddRoleToInstanceProfile", "", url.Values{"InstanceProfileName": {"alpha"}, "RoleName": {"Role"}}, 409, "EntityAlreadyExists")
	for _, name := range []string{"alpha", "Beta", "Zebra"} {
		p.call("RemoveRoleFromInstanceProfile", "", url.Values{"InstanceProfileName": {name}, "RoleName": {"Role"}}, 200, "RemoveRoleFromInstanceProfileResponse")
		p.call("DeleteInstanceProfile", "", url.Values{"InstanceProfileName": {name}}, 200, "DeleteInstanceProfileResponse")
	}
	p.call("DeleteRole", "", url.Values{"RoleName": {"Role"}}, 200, "DeleteRoleResponse")
	p.call("ListInstanceProfiles", "", nil, 200, "<IsTruncated>false</IsTruncated>")
	p.call("GetInstanceProfile", "", url.Values{"InstanceProfileName": {"alpha"}}, 404, "NoSuchEntity")
}
func TestIAMInstanceProfilesQueryCanonicalAuthorizationAndUnsupported(t *testing.T) {
	p := newManagedQueryProbe(t)
	p.role("CaseRole")
	p.call("CreateInstanceProfile", "", url.Values{"InstanceProfileName": {"CaseProfile"}}, 200, "CreateInstanceProfileResponse")
	set := func(document string) {
		t.Helper()
		policy, e := core.ParsePolicy([]byte(document))
		if e != nil {
			t.Fatal(e)
		}
		if e = p.gateway.Identities.Set("caller", policy); e != nil {
			t.Fatal(e)
		}
	}
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"*"},{"Effect":"Deny","Action":"iam:PassRole","Resource":"arn:aws:iam::000000000000:role/CaseRole"}]}`)
	p.call("AddRoleToInstanceProfile", "caller", url.Values{"InstanceProfileName": {"caseprofile"}, "RoleName": {"caserole"}}, 403, "explicitDeny")
	got, e := p.gateway.Roles.GetInstanceProfile(context.Background(), "CaseProfile")
	if e != nil || len(got.Roles) != 0 {
		t.Fatal(got, e)
	}
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:AddRoleToInstanceProfile","Resource":"arn:aws:iam::000000000000:instance-profile/CaseProfile"},{"Effect":"Allow","Action":"iam:PassRole","Resource":"arn:aws:iam::000000000000:role/CaseRole"}]}`)
	p.call("AddRoleToInstanceProfile", "caller", url.Values{"InstanceProfileName": {"CASEPROFILE"}, "RoleName": {"CASEROLE"}}, 200, "AddRoleToInstanceProfileResponse")
	for action, fields := range map[string]url.Values{
		"GetInstanceProfile": {"InstanceProfileName": {"caseprofile"}}, "DeleteInstanceProfile": {"InstanceProfileName": {"caseprofile"}}, "CreateInstanceProfile": {"InstanceProfileName": {"new"}},
		"ListInstanceProfiles": {}, "ListInstanceProfilesForRole": {"RoleName": {"caserole"}}, "RemoveRoleFromInstanceProfile": {"InstanceProfileName": {"caseprofile"}, "RoleName": {"caserole"}},
	} {
		p.call(action, "caller", fields, 403, "AccessDenied")
	}
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"*"},{"Effect":"Deny","Action":"iam:*","Resource":"arn:aws:iam::000000000000:instance-profile/CaseProfile"}]}`)
	p.call("GetInstanceProfile", "caller", url.Values{"InstanceProfileName": {"caseprofile"}}, 403, "explicitDeny")
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListInstanceProfilesForRole","Resource":"arn:aws:iam::000000000000:role/CaseRole"}]}`)
	p.call("ListInstanceProfilesForRole", "caller", url.Values{"RoleName": {"caserole"}}, 200, "CaseProfile")
	for _, test := range []struct {
		action  string
		fields  url.Values
		status  int
		message string
	}{
		{"CreateInstanceProfile", url.Values{"InstanceProfileName": {"tagged"}, "Tags.member.1.Key": {"key"}}, 501, "Unsupported IAM field: Tags.member.1.Key"},
		{"CreateInstanceProfile", url.Values{"InstanceProfileName": {"path"}, "Path": {"/other/"}}, 501, "root instance profile path"},
		{"ListInstanceProfiles", url.Values{"PathPrefix": {"/other/"}}, 501, "root instance profile path"},
		{"CreateInstanceProfile", url.Values{"InstanceProfileName": {"a", "b"}}, 400, "Duplicate IAM fields"},
		{"GetInstanceProfile", url.Values{"InstanceProfileName": {"bad/name"}}, 400, "ValidationError"},
		{"AddRoleToInstanceProfile", url.Values{"InstanceProfileName": {"CaseProfile"}, "RoleName": {core.Administrator}}, 400, "ValidationError"},
		{"AddRoleToInstanceProfile", url.Values{"InstanceProfileName": {"CaseProfile"}, "RoleName": {"missing"}}, 404, "NoSuchEntity"},
		{"ListInstanceProfiles", url.Values{"MaxItems": {"1001"}}, 400, "ValidationError"},
		{"ListInstanceProfiles", url.Values{"MaxItems": {""}}, 400, "ValidationError"},
	} {
		p.call(test.action, "", test.fields, test.status, test.message)
	}
}

// Runs a deterministic replacement when the handler asks for its IAM policy,
// after canonical profile/role admission but before its generation-bound store
// operation. No timing assumption or production test hook is needed.
type profileAdmissionContext struct {
	context.Context
	once    sync.Once
	replace func()
}

func (c *profileAdmissionContext) Value(key any) any {
	if _, ok := key.(sdkPolicyContextKey); ok {
		c.once.Do(c.replace)
	}
	return c.Context.Value(key)
}
func TestIAMInstanceProfilesQueryAdmissionGenerationFences(t *testing.T) {
	for _, test := range []struct {
		action          string
		roleReplacement bool
	}{
		{"GetInstanceProfile", false},
		{"DeleteInstanceProfile", false},
		{"AddRoleToInstanceProfile", false},
		{"AddRoleToInstanceProfile", true},
		{"RemoveRoleFromInstanceProfile", false},
		{"RemoveRoleFromInstanceProfile", true},
		{"ListInstanceProfilesForRole", true},
	} {
		name := test.action + "/profile"
		if test.roleReplacement {
			name = test.action + "/role"
		}
		t.Run(name, func(t *testing.T) {
			action := test.action
			p := newManagedQueryProbe(t)
			s := p.gateway.Roles
			ctx := context.Background()
			profile := profileCreate(t, s, "Profile")
			role := profileRole(t, s, "Role")
			if action == "RemoveRoleFromInstanceProfile" {
				if e := s.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
					t.Fatal(e)
				}
			}
			fields := url.Values{"Action": {action}, "Version": {"2010-05-08"}}
			if action != "ListInstanceProfilesForRole" {
				fields.Set("InstanceProfileName", "Profile")
			}
			if action == "AddRoleToInstanceProfile" || action == "RemoveRoleFromInstanceProfile" || action == "ListInstanceProfilesForRole" {
				fields.Set("RoleName", "Role")
			}
			replace := func() {
				if action == "RemoveRoleFromInstanceProfile" {
					if e := s.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, false); e != nil {
						t.Fatal(e)
					}
				}
				if test.roleReplacement {
					if e := s.Delete(ctx, role.RoleName, role.RoleID); e != nil {
						t.Fatal(e)
					}
					fresh := profileRole(t, s, "ROLE")
					if action == "RemoveRoleFromInstanceProfile" {
						if e := s.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, fresh.RoleName, fresh.RoleID, true); e != nil {
							t.Fatal(e)
						}
					}
					return
				}
				if e := s.DeleteInstanceProfile(ctx, profile.InstanceProfileName, profile.InstanceProfileID); e != nil {
					t.Fatal(e)
				}
				profileCreate(t, s, "PROFILE")
			}
			r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode())).WithContext(&profileAdmissionContext{Context: ctx, replace: replace})
			r.Header.Set("X-Simulator-Service", "iam")
			w := httptest.NewRecorder()
			p.gateway.ServeHTTP(w, r)
			if w.Code != 404 || !strings.Contains(w.Body.String(), "NoSuchEntity") {
				t.Fatal(w.Code, w.Body.String())
			}
			current, e := s.GetInstanceProfile(ctx, "profile")
			wantRoles := 0
			if action == "RemoveRoleFromInstanceProfile" && test.roleReplacement {
				wantRoles = 1
			}
			if e != nil || len(current.Roles) != wantRoles {
				t.Fatal("replacement was mutated", current, e)
			}
			if wantRoles == 1 && current.Roles[0].RoleID == role.RoleID {
				t.Fatal("old role membership returned after replacement", current)
			}
		})
	}
}

func TestIAMInstanceProfilesQueryMarkersRequireCanonicalBase64(t *testing.T) {
	p := newManagedQueryProbe(t)
	profileCreate(t, p.gateway.Roles, "a")
	profileCreate(t, p.gateway.Roles, "b")
	marker := queryMarker(t, p.call("ListInstanceProfiles", "", url.Values{"MaxItems": {"1"}}, 200, "<Marker>"), "ListInstanceProfilesResult")
	p.call("ListInstanceProfiles", "", url.Values{"Marker": {marker}}, 200, "<InstanceProfileName>b</InstanceProfileName>")
	for _, raw := range []string{marker + "\n", marker[:4] + "\r\n" + marker[4:]} {
		p.call("ListInstanceProfiles", "", url.Values{"Marker": {raw}}, 400, "InvalidInput")
	}
	// Use a canonical JSON payload whose final base64 symbol has unused bits.
	want := iamInstanceProfileMarker{Action: "ListInstanceProfiles"}
	value := want
	value.After = "a"
	for len(iamInstanceProfileEncodeMarker(value))%4 == 0 {
		value.After += "a"
	}
	canonical := iamInstanceProfileEncodeMarker(value)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	index := strings.IndexByte(alphabet, canonical[len(canonical)-1])
	alias := canonical[:len(canonical)-1] + string(alphabet[index+1])
	before, e := base64.RawURLEncoding.DecodeString(canonical)
	after, aliasErr := base64.RawURLEncoding.DecodeString(alias)
	if e != nil || aliasErr != nil || string(before) != string(after) {
		t.Fatal("test alias does not preserve the decoded marker", e, aliasErr)
	}
	if _, e = iamInstanceProfileDecodeMarker(canonical, want); e != nil {
		t.Fatal(e)
	}
	if _, e = iamInstanceProfileDecodeMarker(alias, want); iamCode(e) != "InvalidInput" {
		t.Fatal("accepted noncanonical padding bits", e)
	}
	p.call("ListInstanceProfiles", "", url.Values{"Marker": {alias}}, 400, "InvalidInput")
}
func TestIAMInstanceProfilesQueryMarkersBindActionAndRoleGeneration(t *testing.T) {
	p := newManagedQueryProbe(t)
	s := p.gateway.Roles
	ctx := context.Background()
	role := profileRole(t, s, "Role")
	var profiles []IAMInstanceProfile
	for _, name := range []string{"a", "b"} {
		profile := profileCreate(t, s, name)
		profiles = append(profiles, profile)
		if e := s.ChangeInstanceProfileRole(ctx, name, profile.InstanceProfileID, role.RoleName, role.RoleID, true); e != nil {
			t.Fatal(e)
		}
	}
	marker := queryMarker(t, p.call("ListInstanceProfilesForRole", "", url.Values{"RoleName": {"Role"}, "MaxItems": {"1"}}, 200, "<Marker>"), "ListInstanceProfilesForRoleResult")
	p.call("ListInstanceProfiles", "", url.Values{"Marker": {marker}}, 400, "InvalidInput")
	for _, profile := range profiles {
		if e := s.ChangeInstanceProfileRole(ctx, profile.InstanceProfileName, profile.InstanceProfileID, role.RoleName, role.RoleID, false); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Delete(ctx, role.RoleName, role.RoleID); e != nil {
		t.Fatal(e)
	}
	profileRole(t, s, "Role")
	p.call("ListInstanceProfilesForRole", "", url.Values{"RoleName": {"Role"}, "Marker": {marker}}, 400, "InvalidInput")
	for _, raw := range []string{strings.Repeat("a", 1025), base64.RawURLEncoding.EncodeToString([]byte(`{}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"action":"ListInstanceProfiles","roleId":"","roleName":"","after":"A"}`))} {
		p.call("ListInstanceProfiles", "", url.Values{"Marker": {raw}}, 400, "InvalidInput")
	}
}
