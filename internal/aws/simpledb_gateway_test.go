package aws

import (
	"context"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/simpledb"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestSimpleDBGatewayAuthority(t *testing.T) {
	db, e := core.New(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
	})
	s, e := simpledb.NewStore(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	ids := core.NewIdentities(db)
	if e := ids.Set("reader", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"sdb:GetAttributes"}, Resource: core.StringList{"*"}}}}); e != nil {
		t.Fatal(e)
	}
	g := &Gateway{SimpleDB: s, SimpleDBScope: simpledb.Scope{Account: "000000000000", Region: "us-east-1"}, Identities: ids}
	call := func(action, who string, withActor bool, region string) *httptest.ResponseRecorder {
		p := url.Values{"Action": {action}, "Version": {"2009-04-15"}, "DomainName": {"domain"}}
		if action == "PutAttributes" {
			p.Set("ItemName", "item")
			p.Set("Attribute.1.Name", "key")
			p.Set("Attribute.1.Value", "bad")
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(p.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Simulator-Service", "sdb")
		r.Header.Set("X-Simulator-Region", region)
		r.Header.Set("X-Simulator-Principal", core.Administrator)
		r.Header.Set("X-Simulator-Account", "foreign")
		if withActor {
			r = r.WithContext(provider.WithActor(r.Context(), provider.Actor{CredentialIdentity: who, ExerciseIdentity: who, CredentialRole: provider.CredentialOperator}))
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w
	}
	if w := call("CreateDomain", core.Administrator, false, "us-east-1"); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("CreateDomain", core.Administrator, true, "us-west-2"); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("CreateDomain", core.Administrator, true, "us-east-1"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, action := range []string{"CreateDomain", "PutAttributes", "DeleteDomain"} {
		if w := call(action, "reader", true, "us-east-1"); w.Code != 403 {
			t.Fatal(action, w.Code, w.Body)
		}
	}
	ds, _, e := s.ListDomains(context.Background(), "000000000000", "us-east-1", 100, "")
	if e != nil || len(ds) != 1 {
		t.Fatal(ds, e)
	}
	attrs, e := s.Get(context.Background(), "000000000000", "us-east-1", "domain", "item", nil)
	if e != nil || len(attrs) != 0 {
		t.Fatal(attrs, e)
	}
	// A verified SDK policy snapshot must override administrator fallback.
	deny := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Deny", Action: core.StringList{"sdb:*"}, Resource: core.StringList{"*"}}}}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader("Action=DeleteDomain&Version=2009-04-15&DomainName=domain"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Simulator-Service", "sdb")
	ctx := provider.WithActor(r.Context(), provider.Actor{CredentialIdentity: core.Administrator, ExerciseIdentity: core.Administrator, CredentialRole: provider.CredentialOperator})
	r = r.WithContext(context.WithValue(ctx, sdkPolicyContextKey{}, deny))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("SDK snapshot not applied", w.Code, w.Body)
	}
	ds, _, e = s.ListDomains(context.Background(), "000000000000", "us-east-1", 100, "")
	if e != nil || len(ds) != 1 {
		t.Fatal("denied delete changed state", ds, e)
	}
	foreign, _, e := s.ListDomains(context.Background(), "foreign", "us-east-1", 100, "")
	if e != nil || len(foreign) != 0 {
		t.Fatal(foreign, e)
	}
}
func TestSimpleDBGatewayDispatchBoundary(t *testing.T) {
	g := &Gateway{}
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader("Action=CreateDomain&Version=2009-04-15"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 501 || !strings.Contains(w.Body.String(), "cloudformation.amazonaws.com") {
		t.Fatal(w.Code, w.Body)
	}
	// Existing catalog precedence remains in front of the new service hook.
	called := false
	g.CatalogOwns = func(*http.Request) bool { return true }
	g.Catalog = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(202) })
	r.Header.Set("X-Simulator-Service", "sdb")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if !called || w.Code != 202 {
		t.Fatal("catalog precedence changed")
	}
}
