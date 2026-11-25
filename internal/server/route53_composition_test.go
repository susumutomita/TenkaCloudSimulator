package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func route53ComposedCall(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Simulator-Token", token)
	}
	r.Header.Set("X-Simulator-Service", "route53")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestRoute53CompositionTokenPersistenceAndCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route53.db")
	h, closeStore, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("b", 64)
	access, e := NewAccess(h, token, false)
	if e != nil {
		t.Fatal(e)
	}
	body := `<CreateHostedZoneRequest><Name>composed.example</Name><CallerReference>composed</CallerReference></CreateHostedZoneRequest>`
	w := route53ComposedCall(access, "POST", "/2013-04-01/hostedzone", body, "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = route53ComposedCall(access, "POST", "/2013-04-01/hostedzone", body, token)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		Zone struct {
			ID string `xml:"Id"`
		} `xml:"HostedZone"`
	}
	if e = xml.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	if e = closeStore(); e != nil {
		t.Fatal(e)
	}
	h, closeStore, e = New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer closeStore()
	access, e = NewAccess(h, token, false)
	if e != nil {
		t.Fatal(e)
	}
	w = route53ComposedCall(access, "GET", "/_simulator/providers/aws/2013-04-01"+created.Zone.ID, "", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "composed.example.") {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/_simulator/v1/capabilities", nil)
	r.Header.Set("X-Simulator-Token", token)
	w = httptest.NewRecorder()
	access.ServeHTTP(w, r)
	var caps struct {
		Services map[string]struct {
			Operations []string `json:"operations"`
			Console    bool     `json:"console"`
		} `json:"services"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &caps); e != nil {
		t.Fatal(e)
	}
	if len(caps.Services["route53"].Operations) != 8 || !caps.Services["route53"].Console {
		t.Fatal(w.Body.String())
	}
	w = route53ComposedCall(access, "DELETE", "/2013-04-01"+created.Zone.ID, "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRoute53ConsoleCapabilityAdmission(t *testing.T) {
	h, closeStore, err := New(filepath.Join(t.TempDir(), "route53.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()

	for _, tc := range []struct {
		name                    string
		wrapped, actor, handoff bool
		want                    bool
	}{
		{name: "header cannot enable a bare composition"},
		{name: "trusted actor alone is not browser authentication", actor: true},
		{name: "wrapper marker alone does not establish an actor", wrapped: true},
		{name: "bound session is not a Route 53 console", wrapped: true, actor: true, handoff: true},
		{name: "authenticated access with native IAM", wrapped: true, actor: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/_simulator/v1/capabilities", nil)
			r.Header.Set("X-Simulator-Authentication", "required")
			ctx := r.Context()
			if tc.actor {
				ctx = provider.WithActor(ctx, provider.Actor{
					CredentialIdentity: "participant", ExerciseIdentity: "participant", CredentialRole: provider.CredentialParticipant,
				})
			}
			if tc.handoff {
				ctx = context.WithValue(ctx, consoleScopeKey{}, consoleScope{ParameterName: "/workshop", Region: "us-east-1"})
			}
			w := httptest.NewRecorder()
			if tc.wrapped {
				w.Header().Set("X-Simulator-Authentication", "required")
			}
			h.ServeHTTP(w, r.WithContext(ctx))
			var caps struct {
				Services map[string]struct {
					Console bool `json:"console"`
				} `json:"services"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &caps) != nil || caps.Services["route53"].Console != tc.want {
				t.Fatalf("unexpected Route 53 capability (status %d), console=%t", w.Code, caps.Services["route53"].Console)
			}
		})
	}
}
