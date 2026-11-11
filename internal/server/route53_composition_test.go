package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
	if len(caps.Services["route53"].Operations) != 8 || caps.Services["route53"].Console {
		t.Fatal(w.Body.String())
	}
	w = route53ComposedCall(access, "DELETE", "/2013-04-01"+created.Zone.ID, "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
