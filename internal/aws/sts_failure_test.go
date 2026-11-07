package aws

import (
	"context"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSTSQueryMalformedBoundariesNeverIssueCredentials(t *testing.T) {
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(context.Background(), "failure-target", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Roles: roles, Identities: core.NewIdentities(db)}
	a := NewSTSAuthority("test-only-private")
	caller := SDKIdentity{Principal: core.Administrator, Account: "000000000000", ARN: "arn:aws:iam::000000000000:root"}
	base := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {role.Arn}, "RoleSessionName": {"session"}, "ExternalId": {"team-external"}}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		ctx := context.WithValue(r.Context(), stsAuthorityContextKey{}, a)
		ctx = context.WithValue(ctx, sdkIdentityContextKey{}, caller)
		w := httptest.NewRecorder()
		g.serveSTS(w, r.WithContext(ctx))
		return w
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"method", "GET", "/", base.Encode(), 400}, {"path", "POST", "/not-root", base.Encode(), 400}, {"query", "POST", "/?Action=AssumeRole", base.Encode(), 400}, {"oversize", "POST", "/", strings.Repeat("x", (32<<10)+1), 400}, {"malformed-percent", "POST", "/", "Action=%zz", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(tc.method, tc.path, tc.body)
			if w.Code != tc.status || len(a.sessions) != 0 || strings.Contains(w.Body.String(), "AccessKeyId") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		key, value string
		status     int
	}{{"RoleSessionName", "x", 400}, {"RoleSessionName", "bad space", 400}, {"ExternalId", "x", 400}, {"ExternalId", strings.Repeat("a", 1225), 400}, {"RoleArn", "arn:aws:iam::000000000000:role/missing", 403}} {
		q, _ := url.ParseQuery(base.Encode())
		q.Set(tc.key, tc.value)
		w := call("POST", "/", q.Encode())
		if w.Code != tc.status || len(a.sessions) != 0 {
			t.Fatal(tc.key, w.Code, w.Body.String())
		}
	}
	identities := g.Identities
	g.Identities = nil
	if w := call("POST", "/", base.Encode()); w.Code != 403 {
		t.Fatal("nil identity store accepted")
	}
	g.Identities = identities
	if _, err = db.Database().Exec(`UPDATE aws_iam_roles SET trust='{}' WHERE name=?`, role.RoleName); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/", base.Encode()); w.Code != 400 || len(a.sessions) != 0 {
		t.Fatal("invalid trust accepted", w.Code)
	}
	if err = roles.UpdateTrust(context.Background(), role.RoleName, stsRootTrust); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 1000; n++ {
		a.sessions[fmt.Sprint(n)] = sdkSession{credentials: STSCredentials{Expiration: time.Now().Add(time.Hour)}}
	}
	if w := call("POST", "/", base.Encode()); w.Code != 403 || len(a.sessions) != 1000 {
		t.Fatal("session quota silently exceeded", w.Code, len(a.sessions))
	}
	a.sessions = map[string]sdkSession{}
	if w := call("POST", "/", base.Encode()); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, session := range a.sessions {
		p, err := session.snapshot(context.Background())
		if err != nil || core.Evaluate(p, "ssm:GetParameter", "*") == "allowed" {
			t.Fatal("empty role snapshot must deny", err)
		}
	}
}

func TestSTSTrustAndJSONFailureShapes(t *testing.T) {
	for _, document := range []string{
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000","Service":"lambda.amazonaws.com"}}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000"},"Condition":{"StringEquals":{"wrong":"id"}}}}`,
		`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"000000000000"},"Condition":{"StringEquals":{"sts:ExternalId":"x"}}}}`,
	} {
		if _, err := iamTrust(document); err == nil {
			t.Fatal("unsupported trust accepted", document)
		}
	}
	for _, document := range []string{`{`, `[`, `{"a":`, `{"a":1,"a":2}`, `{"a":[1,}`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34), `{} {}`} {
		if stsUniqueJSON([]byte(document)) {
			t.Fatal("ambiguous or invalid JSON accepted", document)
		}
	}
	for _, document := range []string{`{"a":[1,null,true,"text"]}`, `[]`, `{}`} {
		if !stsUniqueJSON([]byte(document)) {
			t.Fatal("valid JSON rejected", document)
		}
	}
}

func TestSTSSnapshotFailsClosedOnMalformedState(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Roles: roles}
	for _, role := range []IAMRole{{}, {RoleName: "target", Arn: "wrong", RoleID: "id"}, {RoleName: "target", Arn: "arn:aws:iam::000000000000:role/target", RoleID: "id"}} {
		if _, err = g.stsSessionPolicy(ctx, role); err == nil {
			t.Fatal("invalid/absent role snapshot accepted")
		}
	}
	if _, err = g.stsRole(ctx, "arn:aws:iam::111111111111:role/target"); err == nil {
		t.Fatal("foreign role resolved")
	}
	if _, err = g.stsRole(ctx, "arn:aws:iam::000000000000:role/target"); err == nil {
		t.Fatal("missing stores resolved role")
	}
	if _, err = db.Database().Exec(`DROP TABLE aws_iam_roles`); err != nil {
		t.Fatal(err)
	}
	role := IAMRole{RoleName: "target", Arn: "arn:aws:iam::000000000000:role/target", RoleID: "id"}
	if _, err = g.stsSessionPolicy(ctx, role); err == nil {
		t.Fatal("DB failure accepted")
	}
	if _, err = g.stsRole(ctx, role.Arn); err == nil {
		t.Fatal("DB failure resolved role")
	}
}

func TestSTSSignatureMalformedScopeAndSignedHeaders(t *testing.T) {
	now := time.Now().UTC()
	a := NewSTSAuthority("synthetic-private-token")
	replacements := map[string][2]string{
		"algorithm": {"AWS4-HMAC-SHA256", "OTHER"}, "duplicate-field": {"Signature=", "Credential=duplicate, Signature="}, "empty-field": {"SignedHeaders=content-type;host;x-amz-date", "SignedHeaders="}, "unknown-field": {"Signature=", "Unknown=x, Signature="}, "scope-length": {"/us-east-1/sts/aws4_request", "/sts/aws4_request"}, "unsupported-region": {"/us-east-1/", "/unknown-region/"}, "unsupported-service": {"/sts/", "/s3/"}, "invalid-signature-hex": {"Signature=", "Signature=zz"}, "unsorted-headers": {"content-type;host;x-amz-date", "host;content-type;x-amz-date"}, "single-header": {"content-type;host;x-amz-date", "host"}, "duplicate-header": {"content-type;host;x-amz-date", "content-type;host;host;x-amz-date"}, "missing-header": {"content-type;host;x-amz-date", "content-type;host;x-amz-date;x-absent"}, "invalid-header-name": {"content-type;host;x-amz-date", "content-type;host;x-amz-date;x_bad"}, "missing-date-header": {"content-type;host;x-amz-date", "content-type;host"},
	}
	for name, replace := range replacements {
		t.Run(name, func(t *testing.T) {
			r := signedSTSRequest("TCSIMOPERATOR", a.operator, "", now)
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), replace[0], replace[1], 1))
			if _, _, err := a.Authenticate(r); err == nil {
				t.Fatal("malformed signature accepted")
			}
		})
	}
	r := signedSTSRequest("TCSIMOPERATOR", a.operator, "", now)
	r.Method = "GET"
	if _, _, err := a.Authenticate(r); err == nil {
		t.Fatal("unsupported method accepted")
	}
}
