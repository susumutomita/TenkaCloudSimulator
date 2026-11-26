package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

func signPoolFixture(secret string, r *http.Request, body, service, region string) {
	now := time.Now().UTC()
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", stamp)
	names := []string{"host"}
	for k := range r.Header {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, n := range names {
		v := r.Header.Get(n)
		if n == "host" {
			v = r.Host
		}
		canonical.WriteString(n + ":" + v + "\n")
	}
	signed := strings.Join(names, ";")
	payload := sha256.Sum256([]byte(body))
	request := r.Method + "\n" + r.URL.Path + "\n\n" + canonical.String() + "\n" + signed + "\n" + hex.EncodeToString(payload[:])
	digest := sha256.Sum256([]byte(request))
	scope := strings.Join([]string{date, region, service, "aws4_request"}, "/")
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(value))
		return h.Sum(nil)
	}
	signing := mac(mac(mac(mac([]byte("AWS4"+secret), date), region), service), "aws4_request")
	signature := mac(signing, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hex.EncodeToString(digest[:]))
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=TCSIMOPERATOR/%s, SignedHeaders=%s, Signature=%x", scope, signed, signature))
}

// Use the actual server/router/Auth graph and independently signed fixture
// requests, not an injected provider actor or a bypass in the HTTP handler.
func TestCognitoUserPoolsThroughVerifiedNativeSDKBoundary(t *testing.T) {
	secret := strings.Repeat("c", 64)
	state := filepath.Join(t.TempDir(), "state.sqlite")
	server, cleanup, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	a, err := NewAccess(server, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	call := func(op, body, signer, region, principal string, expected int, code string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(body))
		req.Header.Set("Content-Type", cognitoidp.JSONMediaType)
		req.Header.Set("X-Amz-Target", cognitoidp.TargetPrefix+op)
		if principal != "" {
			req.Header.Set("X-Simulator-Principal", principal)
		}
		if signer != "" {
			signPoolFixture(secret, req, body, signer, region)
		} else {
			req.Header.Set("X-Simulator-Token", secret) // valid local access, not SDK signing
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != expected || w.Header().Get("X-Amzn-Errortype") != code {
			t.Fatalf("%s: wanted %d/%s, got %d/%s", op, expected, code, w.Code, w.Header().Get("X-Amzn-Errortype"))
		}
		return w
	}
	const svc, east, west = "cognito-idp", "us-east-1", "us-west-2"
	call("CreateUserPool", `{"PoolName":"not-native"}`, "", east, "", 403, "UnrecognizedClientException")
	call("CreateUserPool", `{"PoolName":"wrong-scope"}`, "ssm", east, "", 403, "UnrecognizedClientException")
	call("InitiateAuth", `{}`, svc, east, "", 400, "UnknownOperationException")
	created := call("CreateUserPool", `{"PoolName":"native pool","DeletionProtection":"INACTIVE"}`, svc, east, "", 200, "")
	var result struct {
		UserPool struct{ ID, Name, ARN string }
	}
	if json.Unmarshal(created.Body.Bytes(), &result) != nil || result.UserPool.Name != "native pool" ||
		!strings.HasPrefix(result.UserPool.ID, east+"_") || result.UserPool.ARN != "arn:aws:cognito-idp:us-east-1:000000000000:userpool/"+result.UserPool.ID {
		t.Fatal("unexpected scoped pool metadata")
	}
	input := fmt.Sprintf(`{"UserPoolId":%q}`, result.UserPool.ID)
	call("DescribeUserPool", input, svc, west, "", 400, "InvalidParameterException")
	call("DescribeUserPool", input, svc, east, "", 200, "")
	call("DescribeUserPool", input, svc, east, "ungranted-exercise", 403, "AccessDeniedException")
	call("DeleteUserPool", input, svc, east, "ungranted-exercise", 403, "AccessDeniedException")
	if w := call("DeleteUserPool", input, svc, east, "", 200, ""); w.Body.Len() != 0 {
		t.Fatal("DeleteUserPool response must be empty")
	}
	call("DescribeUserPool", input, svc, east, "", 400, "ResourceNotFoundException")
	if w := call("ListUserPools", `{"MaxResults":60}`, svc, east, "", 200, ""); !strings.Contains(w.Body.String(), `"UserPools":[]`) {
		t.Fatal("denied writes or deleted pool survived")
	}
}
