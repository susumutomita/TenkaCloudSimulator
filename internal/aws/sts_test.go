package aws

import (
	"context"
	"encoding/xml"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSTSQueryBoundariesAndGeneration(t *testing.T) {
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(context.Background(), "sts-target", stsRootTrust, "", 7200)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Roles: roles, Identities: core.NewIdentities(db)}
	a := NewSTSAuthority("local-test-only")
	caller := SDKIdentity{Principal: core.Administrator, ARN: "arn:aws:iam::000000000000:root", Account: "000000000000", UserID: "local-test"}
	call := func(q url.Values, c SDKIdentity, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(q.Encode()))
		if auth {
			ctx := context.WithValue(r.Context(), stsAuthorityContextKey{}, a)
			ctx = context.WithValue(ctx, sdkIdentityContextKey{}, c)
			r = r.WithContext(ctx)
		}
		w := httptest.NewRecorder()
		g.serveSTS(w, r)
		return w
	}
	base := func() url.Values {
		return url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {role.Arn}, "RoleSessionName": {"session"}, "ExternalId": {"team-external"}}
	}
	for _, tc := range []struct {
		name   string
		edit   func(url.Values)
		caller SDKIdentity
		auth   bool
		status int
	}{
		{"success", func(q url.Values) {}, caller, true, 200},
		{"missing authentication", func(q url.Values) {}, caller, false, 403},
		{"external wrong", func(q url.Values) { q.Set("ExternalId", "wrong") }, caller, true, 403},
		{"external missing", func(q url.Values) { q.Del("ExternalId") }, caller, true, 403},
		{"caller policy denied", func(q url.Values) {}, SDKIdentity{Principal: "nobody", ARN: "arn:aws:iam::000000000000:user/nobody", Account: "000000000000"}, true, 403},
		{"too long", func(q url.Values) { q.Set("DurationSeconds", "7201") }, caller, true, 400},
		{"too short", func(q url.Values) { q.Set("DurationSeconds", "899") }, caller, true, 400},
		{"fraction", func(q url.Values) { q.Set("DurationSeconds", "900.5") }, caller, true, 400},
		{"session policy", func(q url.Values) { q.Set("Policy", "{}") }, caller, true, 400},
		{"managed session policy", func(q url.Values) { q.Set("PolicyArns.member.1.arn", "arn:aws:iam::aws:policy/AdministratorAccess") }, caller, true, 400},
		{"tag", func(q url.Values) { q.Set("Tags.member.1.Key", "key") }, caller, true, 400},
		{"duplicate", func(q url.Values) { q.Add("RoleArn", role.Arn) }, caller, true, 400},
		{"wrong version", func(q url.Values) { q.Set("Version", "wrong") }, caller, true, 400},
		{"unknown operation", func(q url.Values) { q.Set("Action", "GetFederationToken") }, caller, true, 501},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base()
			tc.edit(q)
			w := call(q, tc.caller, tc.auth)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Code != 200 && strings.Contains(w.Body.String(), "SecretAccessKey") {
				t.Fatal("credentials on denial")
			}
		})
	}
	chain := caller
	chain.Expires = time.Now().Add(time.Hour)
	q := base()
	q.Set("DurationSeconds", "3601")
	if w := call(q, chain, true); w.Code != 400 {
		t.Fatal("chain duration accepted", w.Code)
	}
	identity := call(url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}, caller, true)
	if identity.Code != 200 || !strings.Contains(identity.Body.String(), caller.ARN) {
		t.Fatal(identity.Body.String())
	}
	w := call(base(), caller, true)
	var result struct {
		Result struct {
			Credentials struct {
				AccessKeyID string `xml:"AccessKeyId"`
			}
		} `xml:"AssumeRoleResult"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	session, exists := a.sessions[result.Result.Credentials.AccessKeyID]
	if !exists {
		t.Fatal("no issued session")
	}
	if err := session.validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = roles.Delete(context.Background(), role.RoleName); err != nil {
		t.Fatal(err)
	}
	if err = session.validate(context.Background()); err == nil {
		t.Fatal("deleted generation validated")
	}
	if _, err = roles.Create(context.Background(), role.RoleName, stsRootTrust, "", 7200); err != nil {
		t.Fatal(err)
	}
	if err = session.validate(context.Background()); err == nil {
		t.Fatal("recreated generation validated")
	}
	if _, err = db.Database().Exec(`DROP TABLE aws_iam_roles`); err != nil {
		t.Fatal(err)
	}
	if err = session.validate(context.Background()); err == nil {
		t.Fatal("database failure validated")
	}
}
