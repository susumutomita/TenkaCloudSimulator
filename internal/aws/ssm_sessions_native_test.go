package aws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionNativeStrictObject(t *testing.T) {
	allowed := map[string]bool{"Target": true, "DocumentName": true}
	for _, body := range []string{`null`, `[]`, `{"Target":null}`, `{"Target":42}`, `{"Target":"a","Target":"b"}`, `{"Target":"a","\u0054arget":"b"}`, `{"target":"a"}`, `{"Target":"a","Parameters":{}}`, `{"Target":"a"} {}`, `{"Target":"a"} garbage`} {
		if _, err := decodeSessionObject(strings.NewReader(body), allowed); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	got, err := decodeSessionObject(strings.NewReader(`{"Target":"i-own","DocumentName":"SSM-SessionManagerRunShell"}`), allowed)
	if err != nil || got["Target"] != "i-own" {
		t.Fatalf("valid input: %v %v", got, err)
	}
}
func TestSessionNativeHandoffCannotCrossBoundary(t *testing.T) {
	r, a, _, _ := sessionAuthFixture(t)
	binding, err := a.TerminalBinding(r, "us-east-1", "i-own")
	if err != nil {
		t.Fatal(err)
	}
	terminal, _, err := binding(r)
	if err != nil {
		t.Fatal(err)
	}
	parameter := r.WithContext(context.WithValue(r.Context(), consoleParameterBoundaryKey{}, consoleParameterBoundary{}))
	unsigned := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Target":"i-own"}`))
	var absent *SSMSessionStore
	for _, request := range []*http.Request{terminal, parameter, unsigned} {
		w := httptest.NewRecorder()
		absent.ServeNative(w, request)
		if w.Code != http.StatusForbidden {
			t.Fatalf("handoff accepted: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	absent.ServeNative(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("missing backend: %d", w.Code)
	}
}
func TestSessionNativeEnvelopeRejectsBeforeBackend(t *testing.T) {
	// The fixture installs the independently tested verifier's private context;
	// this test is adapter admission, not a claim to verify these dummy signatures.
	makeRequest := func(body string) *http.Request {
		r, _, _, _ := sessionAuthFixture(t)
		r.Body = io.NopCloser(strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM.StartSession")
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIM-test-session/20261111/us-east-1/ssm/aws4_request, SignedHeaders=host, Signature="+strings.Repeat("0", 64))
		return r
	}
	s := &SSMSessionStore{}
	for _, mutate := range []func(*http.Request){func(r *http.Request) { r.Method = "GET" }, func(r *http.Request) { r.Header.Add("X-Amz-Target", "AmazonSSM.TerminateSession") }, func(r *http.Request) { r.Header.Add("Content-Type", "application/x-amz-json-1.1") }, func(r *http.Request) {
		r.Header.Set("Authorization", strings.ReplaceAll(r.Header.Get("Authorization"), "/ssm/", "/sts/"))
	}, func(r *http.Request) { r.URL.Path = "/other" }, func(r *http.Request) { r.URL.RawQuery = "token=x" }, func(r *http.Request) { r.Header.Set("X-Simulator-Region", "us-west-2") }, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, func(r *http.Request) { r.Header.Set("X-Amz-Target", "AmazonSSM.SendCommand") }} {
		r := makeRequest(`{"Target":"i-own"}`)
		mutate(r)
		w := httptest.NewRecorder()
		s.ServeNative(w, r)
		want := http.StatusBadRequest
		if strings.Contains(r.Header.Get("Authorization"), "/sts/") {
			want = http.StatusForbidden
		}
		if w.Code != want {
			t.Fatalf("invalid envelope: %d %s", w.Code, w.Body.String())
		}
	}
	for _, target := range []string{"i-peer", "i-own"} {
		// i-peer is denied at target authorization; i-own lacks document permission.
		// Neither may touch the intentionally absent backend.
		r := makeRequest(`{"Target":"` + target + `"}`)
		w := httptest.NewRecorder()
		s.ServeNative(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("policy denial not403: %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"Target":"i-own","Target":"i-peer"}`, `{"Target":null}`, `{"Target":"` + strings.Repeat("x", 17<<10) + `"}`} {
		r := makeRequest(body)
		w := httptest.NewRecorder()
		s.ServeNative(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid body: %d", w.Code)
		}
	}
}
