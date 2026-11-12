//go:build linux

package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCanonicalMetadataIdentityMemoryOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	service := &ec2MetadataService{ctx: ctx, owner: leaseOwner(), ip: "10.99.1.4", tokens: map[string]time.Time{}}
	request := func(method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-aws-ec2-metadata-token", token)
		if method == "PUT" {
			r.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
		}
		w := httptest.NewRecorder()
		service.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/latest/meta-data/public-hostname", ""); w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
	issued := request("PUT", "/latest/api/token", "")
	token := issued.Body.String()
	if issued.Code != 200 || len(token) != 64 {
		t.Fatal(issued.Code, token)
	}
	w := request("GET", "/latest/meta-data/public-hostname", token)
	if w.Code != 200 || w.Body.String() != EC2InstancePublicDNSName(leaseOwner().Instance) || !strings.HasSuffix(w.Body.String(), ".simulator.invalid") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/latest/meta-data/iam/security-credentials/", token); w.Code != 404 {
		t.Fatal("fabricated credentials", w.Code)
	}
	other := &ec2MetadataService{ctx: ctx, owner: leaseOwner(), tokens: map[string]time.Time{}}
	r := httptest.NewRequest("GET", "/latest/meta-data/instance-id", nil)
	r.Header.Set("X-aws-ec2-metadata-token", token)
	w = httptest.NewRecorder()
	other.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cross-instance token accepted")
	}
	cancel()
	if w = request("GET", "/latest/meta-data/instance-id", token); w.Code != 503 {
		t.Fatal("revoked instance metadata", w.Code)
	}
}
