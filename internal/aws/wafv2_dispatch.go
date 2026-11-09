package aws

import (
	"errors"
	"github.com/ncruces/go-sqlite3"
	"net/http"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func isWAFv2(r *http.Request) bool {
	if r.Header.Get("X-Simulator-Service") == "wafv2" || strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AWSWAF_20190729.") {
		return true
	}
	_, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	parts := strings.Split(scope, "/")
	return ok && len(parts) >= 5 && parts[3] == "wafv2"
}
func wafScopeRegion(r *http.Request) (string, error) {
	region := r.Header.Get("X-Simulator-Region")
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != "wafv2" {
		return "", wafInvalid("Service selection and WAF target disagree.")
	}
	if !strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AWSWAF_20190729.") {
		return "", wafInvalid("Use the WAFv2 JSON target.")
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", wafInvalid("AWS session tokens are unsupported.")
	}
	for _, field := range []string{"X-Amz-Credential", "X-Amz-Algorithm", "X-Amz-Security-Token", "X-Amz-Signature"} {
		if r.URL.Query().Has(field) {
			return "", wafInvalid("Presigned requests are unsupported.")
		}
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, scope, ok := strings.Cut(auth, "Credential=")
		credential, _, _ := strings.Cut(scope, ",")
		parts := strings.Split(credential, "/")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[3] != "wafv2" || parts[4] != "aws4_request" {
			return "", ssmFailure{"AccessDeniedException", "Use unsigned requests or dummy TCSIM WAFv2 credentials."}
		}
		if region != "" && region != parts[2] {
			return "", wafInvalid("Region header and credential scope disagree.")
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		return "", wafInvalid("Unsupported simulator region.")
	}
	return region, nil
}
func (g *Gateway) serveWAFv2(w http.ResponseWriter, r *http.Request) {
	region, e := wafScopeRegion(r)
	if e != nil {
		wafHTTPError(w, e)
		return
	}
	if g.WAF == nil {
		wafHTTPError(w, ssmFailure{"UnknownOperationException", "WAFv2 state is not configured."})
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	var policy core.Policy
	if principal != core.Administrator {
		if g.Identities == nil {
			wafHTTPError(w, ssmFailure{"AccessDeniedException", "No identity authority configured."})
			return
		}
		policy, _, e = g.Identities.Policy(principal)
		if e != nil {
			wafHTTPError(w, ssmFailure{"AccessDeniedException", "The selected identity cannot perform WAF operations."})
			return
		}
	}
	g.WAF.ServeHTTP(w, r, region, func(action, resource string) bool {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision == "allowed"
	})
}

// SQLite RESTRICT may report either FOREIGNKEY or its internal constraint
// trigger. Preserve unrelated storage failures as internal errors.
func wafDeletionError(err error) error {
	if errors.Is(err, sqlite3.CONSTRAINT_FOREIGNKEY) || (errors.Is(err, sqlite3.CONSTRAINT_TRIGGER) && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")) {
		return apiFailure("ConflictException", "Remove WAF associations before deleting the API or stage.")
	}
	return err
}
