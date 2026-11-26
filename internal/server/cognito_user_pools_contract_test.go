package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

func TestCognitoUserPoolsCapabilitiesAndExactSDKAdmission(t *testing.T) {
	const addr = "http://127.0.0.1"
	secret := strings.Repeat("c", 64) // Process-only test fixture.
	s, done, err := New(filepath.Join(t.TempDir(), "cognito.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = done() })
	protected, err := NewAccess(s, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, addr+"/_simulator/v1/capabilities", nil)
	r.Header.Set("X-Simulator-Token", secret)
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	var manifest struct {
		Services map[string]struct {
			Operations                []string
			Protocol                  string
			NativeSDKAuthentication   bool
			MetadataOnly              bool
			Console, Scalar, SignIn   *bool
			PoolLimitPerAccountRegion int
		}
	}
	wantActions := []string{"CreateUserPool", "DescribeUserPool", "ListUserPools", "DeleteUserPool"}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &manifest) != nil {
		t.Fatal("could not read real simulator capabilities")
	}
	capability := manifest.Services["cognito-idp"]
	if !slices.Equal(capability.Operations, wantActions) || !capability.NativeSDKAuthentication || !capability.MetadataOnly ||
		capability.PoolLimitPerAccountRegion != cognitoidp.PoolLimit || !strings.Contains(capability.Protocol, "JSON 1.1") ||
		capability.Console == nil || *capability.Console || capability.Scalar == nil || *capability.Scalar ||
		capability.SignIn == nil || *capability.SignIn {
		t.Fatal("published capability must describe only the bounded four-action SDK surface")
	}
	for _, tc := range []struct {
		name, body, mediaType string
		signMediaType         bool
	}{
		{"legacy wire version", `{"PoolName":"incorrect media"}`, "application/x-amz-json-1.0", true},
		{"omitted signed content type", `{"PoolName":"unsigned media"}`, cognitoidp.JSONMediaType, false},
		{"oversized signed request", `{"PoolName":"` + strings.Repeat("x", cognitoidp.RequestLimit) + `"}`, cognitoidp.JSONMediaType, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, addr+"/", strings.NewReader(tc.body))
			r.Header.Set("X-Amz-Target", cognitoidp.TargetPrefix+"CreateUserPool")
			if tc.signMediaType {
				r.Header.Set("Content-Type", tc.mediaType)
			}
			signPoolFixture(secret, r, tc.body, "cognito-idp", "us-east-1")
			r.Header.Set("Content-Type", tc.mediaType)
			w := httptest.NewRecorder()
			protected.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || w.Header().Get("X-Amzn-Errortype") != "UnrecognizedClientException" ||
				w.Header().Get("X-Simulator-IAM-Decision") != "" {
				t.Fatal("inadmissible request reached local policy or provider state")
			}
		})
	}
	const readback = `{"MaxResults":60}`
	r = httptest.NewRequest(http.MethodPost, addr+"/", strings.NewReader(readback))
	r.Header.Set("Content-Type", cognitoidp.JSONMediaType)
	r.Header.Set("X-Amz-Target", cognitoidp.TargetPrefix+"ListUserPools")
	signPoolFixture(secret, r, readback, "cognito-idp", "us-east-1")
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"UserPools":[]`) {
		t.Fatal("admission failures left durable pool state")
	}
}
