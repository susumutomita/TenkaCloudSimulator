package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func elbComposedCall(h http.Handler, token string, fields url.Values) *httptest.ResponseRecorder {
	fields.Set("Version", "2015-12-01")
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(fields.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261111/us-east-1/elasticloadbalancing/aws4_request, SignedHeaders=host, Signature=dummy")
	if token != "" {
		r.Header.Set("X-Simulator-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestELBCompositionTokenPersistenceDataPlaneAndCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elb.db")
	h, closeStore, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("c", 64)
	access, e := NewAccess(h, token, false)
	if e != nil {
		t.Fatal(e)
	}
	create := url.Values{"Action": {"CreateLoadBalancer"}, "Name": {"composed"}, "Subnets.member.1": {"subnet-11111111"}, "Subnets.member.2": {"subnet-22222222"}}
	if w := elbComposedCall(access, "", create); w.Code != 401 {
		t.Fatal("unauthenticated ELB request was served", w.Code)
	}
	w := elbComposedCall(access, token, create)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ARN string `xml:"CreateLoadBalancerResult>LoadBalancers>member>LoadBalancerArn"`
	}
	if e = xml.Unmarshal(w.Body.Bytes(), &created); e != nil || created.ARN == "" {
		t.Fatal(e, w.Body.String())
	}
	listener := url.Values{"Action": {"CreateListener"}, "LoadBalancerArn": {created.ARN}, "Protocol": {"HTTP"}, "Port": {"8080"}, "DefaultActions.member.1.Type": {"fixed-response"}, "DefaultActions.member.1.FixedResponseConfig.StatusCode": {"202"}, "DefaultActions.member.1.FixedResponseConfig.MessageBody": {"accepted"}}
	if w = elbComposedCall(access, token, listener); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	started := time.Now()
	if e = closeStore(); e != nil {
		t.Fatal(e)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("closing the composed server waited too long for the health loop")
	}
	h, closeStore, e = New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer closeStore()
	if access, e = NewAccess(h, token, false); e != nil {
		t.Fatal(e)
	}
	for _, target := range []string{"/_elb/us-east-1/composed/8080/x", "/_simulator/providers/aws/_elb/us-east-1/composed/8080/x"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1"+target, nil)
		w = httptest.NewRecorder()
		access.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("unauthenticated data-plane request was served", target, w.Code)
		}
		r.Header.Set("X-Simulator-Token", token)
		w = httptest.NewRecorder()
		access.ServeHTTP(w, r)
		if w.Code != 202 || w.Body.String() != "accepted" || w.Header().Get("Content-Disposition") == "" {
			t.Fatal(target, w.Code, w.Header(), w.Body.String())
		}
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/_simulator/v1/capabilities", nil)
	r.Header.Set("X-Simulator-Token", token)
	w = httptest.NewRecorder()
	access.ServeHTTP(w, r)
	var caps struct {
		Services map[string]struct {
			Operations  []string `json:"operations"`
			TargetTypes []string `json:"targetTypes"`
			Console     bool     `json:"console"`
		} `json:"services"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &caps); e != nil {
		t.Fatal(e)
	}
	elb := caps.Services["elasticloadbalancing"]
	if len(elb.Operations) != 13 || len(elb.TargetTypes) != 1 || elb.TargetTypes[0] != "lambda" || elb.Console {
		t.Fatal(w.Body.String())
	}
	if w = elbComposedCall(access, token, url.Values{"Action": {"DeleteLoadBalancer"}, "LoadBalancerArn": {created.ARN}}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
