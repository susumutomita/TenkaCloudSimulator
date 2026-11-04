package aws

import (
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http"
	"strings"
)

func isKMS(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("X-Amz-Target"), "TrentService.") || r.Header.Get("X-Simulator-Service") == "kms"
}
func kmsRequestRegion(r *http.Request) (string, error) {
	region := r.Header.Get("X-Simulator-Region")
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != "kms" {
		return "", kmsInvalid()
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", kmsInvalid()
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, scope, ok := strings.Cut(auth, "Credential=")
		parts := strings.SplitN(scope, "/", 5)
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[3] != "kms" || !strings.HasPrefix(parts[4], "aws4_request,") {
			return "", kmsFailure("AccessDeniedException", "Use unsigned requests or dummy TCSIM credentials for KMS.")
		}
		if region != "" && region != parts[2] {
			return "", kmsInvalid()
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !kmsRegion(region) {
		return "", kmsInvalid()
	}
	return region, nil
}
func (g *Gateway) serveKMS(w http.ResponseWriter, r *http.Request) {
	if g.KMS == nil {
		kmsWriteError(w, kmsFailure("UnknownOperationException", "KMS state is not configured."))
		return
	}
	region, err := kmsRequestRegion(r)
	if err != nil {
		kmsWriteError(w, err)
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	var policy core.Policy
	if principal != core.Administrator {
		if g.Identities == nil {
			kmsWriteError(w, kmsFailure("AccessDeniedException", "KMS identity authority is unavailable."))
			return
		}
		policy, _, err = g.Identities.Policy(principal)
		if err != nil {
			kmsWriteError(w, kmsFailure("AccessDeniedException", "KMS identity is unavailable."))
			return
		}
	}
	g.KMS.ServeHTTP(w, r, region, func(action, resource string) bool {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision == "allowed"
	})
}
