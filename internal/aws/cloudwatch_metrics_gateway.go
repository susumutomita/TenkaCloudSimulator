package aws

import (
	"net/http"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cloudwatchmetrics"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func isCloudWatchMetrics(r *http.Request) bool {
	if slices.Contains(r.Header.Values("X-Simulator-Service"), "monitoring") {
		return true
	}
	for _, a := range r.Header.Values("Authorization") {
		if elbSigningService(a) == "monitoring" {
			return true
		}
	}
	return false
}

func (g *Gateway) serveCloudWatchMetrics(w http.ResponseWriter, r *http.Request) {
	actor, ok := provider.ActorFromContext(r.Context())
	if !ok || g.CloudWatchMetrics == nil || g.Identities == nil {
		cloudwatchmetrics.Reject(w, true)
		return
	}
	for _, h := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Amz-Target", "X-Amz-Security-Token"} {
		if len(r.Header.Values(h)) > 1 {
			cloudwatchmetrics.Reject(w, false)
			return
		}
	}
	// A signed request supplies routing metadata only; the host-issued actor
	// above remains authoritative for IAM. Match the complete credential scope.
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, scope, found := strings.Cut(auth, "Credential=")
		credential, _, _ := strings.Cut(scope, ",")
		parts := strings.Split(credential, "/")
		if !found || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || len(parts) != 5 ||
			len(parts[1]) != 8 || parts[4] != "aws4_request" {
			cloudwatchmetrics.Reject(w, false)
			return
		}
	}
	region, err := apiScopeRegion(r, "monitoring", "")
	if err != nil {
		cloudwatchmetrics.Reject(w, false)
		return
	}
	g.CloudWatchMetrics.ServeQuery(w, r, region, func(action, resource string) (bool, error) {
		decision, err := g.sdkDecision(r, actor.ExerciseIdentity, action, resource)
		if err != nil {
			return false, err
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision == "allowed", nil
	})
}
