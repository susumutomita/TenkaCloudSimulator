package aws

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Unknown JSON targets must not move an EC2-marked Query request into the
// generic CloudFormation form route. EC2 remains read-only and rejects it.
func TestEC2ReadRoutingRejectsUnknownTarget(t *testing.T) {
	for _, header := range []string{"X-Simulator-Service", "Authorization"} {
		t.Run(header, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader("Action=CreateStack&Version=2010-05-15&StackName=unexpected"))
			r.Header.Set("X-Amz-Target", "Unknown.Operation")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if header == "Authorization" {
				r.Header.Set(header, "AWS4-HMAC-SHA256 Credential=TCSIMLOCAL/20261113/us-east-1/ec2/aws4_request")
			} else {
				r.Header.Set(header, "ec2")
			}
			w := httptest.NewRecorder()
			// No stores are provided: this malformed request must be refused before
			// any backing service state can be consulted or changed.
			(&Gateway{}).ServeHTTP(w, r)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "<Code>InvalidRequest</Code>") {
				t.Fatalf("expected EC2 native refusal, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
