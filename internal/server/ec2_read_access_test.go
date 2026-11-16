package server

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Exercise the real composition and Access boundary without opening a socket.
func TestEC2ReadAccessBoundary(t *testing.T) {
	h, closeDB, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeDB(); err != nil {
			t.Error(err)
		}
	})
	operator := strings.Repeat("a", 64)
	access, err := NewAccess(h, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		return w
	}
	issue := request("POST", "/_simulator/v1/access", `{"Principal":"ec2-reader"}`, operator, nil)
	var participant struct{ AccessToken string }
	if issue.Code != 200 || json.Unmarshal(issue.Body.Bytes(), &participant) != nil || participant.AccessToken == "" {
		t.Fatal("participant issuance failed", issue.Code)
	}
	for _, tc := range []struct {
		name, token, principal, session string
		status                          int
	}{
		{"operator", operator, "", "", 200},
		{"unauthenticated", "", "", "", 401},
		{"foreign authority", strings.Repeat("b", 64), "", "", 401},
		{"participant admin forgery", participant.AccessToken, "simulator-admin", "", 403},
		{"unknown selected identity", operator, "missing-ec2-identity", "", 403},
		{"participant missing policy", participant.AccessToken, "", "", 403},
		{"session token unsupported", operator, "", "synthetic-session-token", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request("POST", "/", "Action=DescribeVpcs&Version=2016-11-15", tc.token, map[string]string{
				"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2", "X-Simulator-Region": "us-east-1", "X-Simulator-Principal": tc.principal, "X-Amz-Security-Token": tc.session,
			})
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 200 && !strings.Contains(w.Body.String(), "DescribeVpcsResponse") {
				t.Fatal("missing native response", w.Body.String())
			}
			if tc.session != "" && !strings.Contains(w.Body.String(), "UnsupportedOperation") {
				t.Fatal("missing explicit session refusal", w.Body.String())
			}
		})
	}
	// A native temporary credential attempt must not fall back to operator access.
	w := request("POST", "/", "Action=DescribeVpcs&Version=2016-11-15", "", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "X-Simulator-Service": "ec2",
		"Authorization":        "AWS4-HMAC-SHA256 Credential=TCSIMASIAINVALID/20261113/us-east-1/ec2/aws4_request, SignedHeaders=host, Signature=invalid",
		"X-Amz-Security-Token": "synthetic-session-token",
	})
	if w.Code != 403 {
		t.Fatalf("native invalid session fell through: %d", w.Code)
	}
	w = request("GET", "/_simulator/v1/capabilities", "", operator, nil)
	var caps struct {
		Services map[string]struct {
			Console            bool     `json:"console"`
			ReadOnly           bool     `json:"readOnly"`
			SessionCredentials bool     `json:"sessionCredentials"`
			Operations         []string `json:"operations"`
		} `json:"services"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil {
		t.Fatal("capabilities unavailable", w.Code)
	}
	ec2, exists := caps.Services["ec2"]
	if !exists || !ec2.Console || !ec2.ReadOnly || ec2.SessionCredentials || !reflect.DeepEqual(ec2.Operations, []string{"DescribeInstances", "DescribeVpcs"}) {
		t.Fatalf("unexpected EC2 capabilities: %+v", ec2)
	}
}
