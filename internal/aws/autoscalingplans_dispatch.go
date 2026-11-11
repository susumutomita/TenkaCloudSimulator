package aws

import (
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func isAutoScalingPlans(r *http.Request) bool {
	for _, value := range r.Header.Values("X-Amz-Target") {
		if strings.HasPrefix(value, aspTargetPrefix) {
			return true
		}
	}
	for _, value := range r.Header.Values("X-Simulator-Service") {
		if value == aspSigningName {
			return true
		}
	}
	for _, value := range r.Header.Values("Authorization") {
		if r53SigningService(value) == aspSigningName {
			return true
		}
	}
	return false
}

// serveAutoScalingPlans captures one immutable caller policy before dispatch
// and uses it for every authorization callback of this request.
func (g *Gateway) serveAutoScalingPlans(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Target", "X-Amz-Security-Token"} {
		if len(r.Header.Values(name)) > 1 {
			aspWriteError(w, aspInvalid("Duplicate routing or authority headers are unsupported."))
			return
		}
	}
	if g.AutoScalingPlans == nil {
		aspWriteError(w, aspFailure{"UnknownOperationException", "Auto Scaling Plans state is not configured."})
		return
	}
	if g.Identities == nil {
		aspWriteError(w, aspFailure{"AccessDeniedException", "Auto Scaling Plans identity authority is unavailable."})
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if _, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential="); ok {
		parts := strings.Split(scope, "/")
		if len(parts) >= 3 {
			if region != "" && region != parts[2] {
				aspWriteError(w, aspInvalid("Region header and credential scope disagree."))
				return
			}
			region = parts[2]
		}
	}
	if region == "" {
		region = "us-east-1"
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	var policy core.Policy
	if principal != core.Administrator {
		var found bool
		var err error
		policy, found, err = g.Identities.PolicyContext(r.Context(), principal)
		if err != nil {
			aspWriteError(w, err)
			return
		}
		if !found {
			aspWriteError(w, aspFailure{"AccessDeniedException", "Auto Scaling Plans identity is unavailable."})
			return
		}
	}
	g.AutoScalingPlans.ServeHTTP(w, r, region, func(action, resource string) error {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			return aspFailure{"AccessDeniedException", "IAM evaluation for " + action + ": " + decision}
		}
		return nil
	})
}
