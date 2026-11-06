package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessGCPUnauthorizedPreservesNativeErrorAndBoundary(t *testing.T) {
	operator := strings.Repeat("a", 64)
	forwarded := 0
	access, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusNoContent)
	}), operator, false)
	if err != nil {
		t.Fatal(err)
	}
	const message = "A valid simulator access token is required"
	for _, path := range []string{
		"/_simulator/providers/gcp/storage/v1/b?project=tenkacloud-local",
		"/_simulator/providers/gcp/upload/storage/v1/b/practice/o?uploadType=multipart",
		"/_simulator/providers/gcp/download/storage/v1/b/practice/o/key?alt=media",
	} {
		for _, token := range []string{"", "invalid", strings.Repeat("b", 64)} {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566"+path, nil)
			r.Header.Set("X-Simulator-Token", token)
			response := httptest.NewRecorder()
			access.ServeHTTP(response, r)
			var body struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
					Status  string `json:"status"`
				} `json:"error"`
			}
			if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/json" || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error.Code != 401 || body.Error.Message != message || body.Error.Status != "UNAUTHENTICATED" {
				t.Fatalf("GCP denial lost native wire contract: %s %s", path, response.Body.String())
			}
		}
	}
	if forwarded != 0 {
		t.Fatal("unauthenticated provider requests reached the underlying handler")
	}
	// A lookalike prefix or an AWS/control endpoint keeps the public legacy body.
	for _, path := range []string{"/", "/_simulator/v1/capabilities", "/_simulator/providers/gcp-lookalike/storage/v1/b", "/_simulator/providers/azure/tenkacloudlocal"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566"+path, nil)
		r.Header.Set("X-Simulator-Service", "gcp")
		response := httptest.NewRecorder()
		access.ServeHTTP(response, r)
		var body map[string]string
		if response.Code != http.StatusUnauthorized || json.Unmarshal(response.Body.Bytes(), &body) != nil || body["error"] != message {
			t.Fatalf("legacy denial changed: %s %s", path, response.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566/_simulator/providers/gcp/storage/v1/b", nil)
	r.Header.Set("X-Simulator-Token", operator)
	response := httptest.NewRecorder()
	access.ServeHTTP(response, r)
	if response.Code != http.StatusNoContent || forwarded != 1 {
		t.Fatal("valid authentication changed")
	}
}
