package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

type nativeSessionRouteBackend struct{ calls int }

func (b *nativeSessionRouteBackend) ResolveSessionTarget(context.Context, string, string) (aws.SSMSessionTarget, error) {
	b.calls++
	return aws.SSMSessionTarget{}, fmt.Errorf("injected unavailable guest")
}
func (*nativeSessionRouteBackend) OpenSessionChannel(context.Context, aws.SSMSessionTarget) (*guestsession.Driver, error) {
	return nil, fmt.Errorf("must not acquire a guest")
}

// This signs the real Access boundary, not an injected SDK context. The backend
// deliberately refuses acquisition: this is routing/authentication evidence only.
func signedNativeSession(c aws.STSCredentials, target, body string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Date", stamp)
	r.Header.Set("X-Amz-Security-Token", c.SessionToken)
	r.Header.Set("X-Amz-Target", target)
	names := "content-type;host;x-amz-date;x-amz-security-token;x-amz-target"
	canonical := "content-type:application/x-amz-json-1.1\nhost:" + r.Host + "\nx-amz-date:" + stamp + "\nx-amz-security-token:" + c.SessionToken + "\nx-amz-target:" + target + "\n"
	payload := sha256.Sum256([]byte(body))
	digest := sha256.Sum256([]byte("POST\n/\n\n" + canonical + "\n" + names + "\n" + hex.EncodeToString(payload[:])))
	scope := now.Format("20060102") + "/us-east-1/ssm/aws4_request"
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(value))
		return h.Sum(nil)
	}
	key := mac(mac(mac(mac([]byte("AWS4"+c.SecretAccessKey), now.Format("20060102")), "us-east-1"), "ssm"), "aws4_request")
	sig := mac(key, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hex.EncodeToString(digest[:]))
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", c.AccessKeyID, scope, names, sig))
	return r
}
func TestTerminalNativeGatewayUsesVerifiedParticipant(t *testing.T) {
	a, c, valid := terminalFixture(t)
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	backend := &nativeSessionRouteBackend{}
	sessions, err := aws.NewSSMSessionStore(db.Database(), backend, "ws://127.0.0.1:4566/_simulator/v1/terminal-channel")
	if err != nil {
		t.Fatal(err)
	}
	a.next = &aws.Gateway{Sessions: sessions}
	request := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	r := signedNativeSession(c, "AmazonSSM.StartSession", `{"Target":"i-own"}`)
	w := request(r)
	if backend.calls != 1 || w.Code == 200 {
		t.Fatalf("signed native dispatch did not reach refusing backend: calls=%d status=%d body=%s", backend.calls, w.Code, w.Body.String())
	}
	*valid = false
	w = request(signedNativeSession(c, "AmazonSSM.StartSession", `{"Target":"i-own"}`))
	if w.Code != 403 || backend.calls != 1 {
		t.Fatalf("revoked credential reached backend: %d calls=%d", w.Code, backend.calls)
	}
	*valid = true
	r = signedNativeSession(c, "AmazonSSM.StartSession", `{"Target":"i-own"}`)
	r.Header.Set("X-Amz-Target", "AmazonSSM.TerminateSession")
	w = request(r)
	if w.Code != 403 || backend.calls != 1 {
		t.Fatalf("tampered signed target accepted: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(`{"Target":"i-own"}`))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM.StartSession")
	r.Header.Set("X-Simulator-Token", strings.Repeat("a", 32))
	w = request(r)
	if w.Code != 403 || backend.calls != 1 {
		t.Fatalf("operator became participant: %d", w.Code)
	}
}
