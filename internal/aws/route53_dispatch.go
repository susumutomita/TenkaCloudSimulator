package aws

import (
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func r53SigningService(authorization string) string {
	_, scope, ok := strings.Cut(authorization, "Credential=")
	if !ok {
		return ""
	}
	parts := strings.Split(scope, "/")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}
func isRoute53(r *http.Request) bool {
	for _, value := range r.Header.Values("X-Simulator-Service") {
		if value == "route53" {
			return true
		}
	}
	for _, value := range r.Header.Values("Authorization") {
		if r53SigningService(value) == "route53" {
			return true
		}
	}
	// An explicitly selected S3 request may address a bucket/key that resembles
	// this REST prefix. The trusted Lambda S3 bridge already calls serveS3 directly.
	if r.Header.Get("X-Simulator-Service") == "s3" || r53SigningService(r.Header.Get("Authorization")) == "s3" {
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/2013-04-01/")
}
func (g *Gateway) serveRoute53(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Target", "X-Amz-Security-Token"} {
		if len(r.Header.Values(name)) > 1 {
			r53failure(w, r53invalid("Duplicate routing or authority headers are unsupported."))
			return
		}
	}
	if r.Header.Get("X-Amz-Target") != "" {
		r53failure(w, r53invalid("Route53 REST/XML does not accept a JSON target."))
		return
	}
	if region := r.Header.Get("X-Simulator-Region"); region != "" && region != "us-east-1" {
		r53failure(w, r53invalid("Route53 uses global state and us-east-1 signing scope."))
		return
	}
	if g.Route53 == nil {
		r53failure(w, r53err("NotImplemented", "Route53 state is not configured."))
		return
	}
	if g.Identities == nil {
		r53failure(w, r53err("AccessDenied", "Route53 identity authority is unavailable."))
		return
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
			r53failure(w, err)
			return
		}
		if !found {
			r53failure(w, r53err("AccessDenied", "Route53 identity is unavailable."))
			return
		}
	}
	g.Route53.ServeHTTP(w, r, func(action, resource string) error {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			return r53err("AccessDenied", "IAM evaluation: "+decision)
		}
		return nil
	})
}
