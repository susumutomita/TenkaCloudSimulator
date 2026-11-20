package aws

import (
	"context"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/simpledb"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// serveSimpleDB consumes only the local authentication context installed by
// NewAccess. Dummy signing fields and request identity/account headers confer
// no authority. The host configures a single account/region for this first slice.
func (g *Gateway) serveSimpleDB(w http.ResponseWriter, r *http.Request) {
	actor, ok := provider.ActorFromContext(r.Context())
	if !ok || g.Identities == nil {
		fail(w, r, "AccessDenied", "Authenticated local identity required.")
		return
	}
	if g.SimpleDBScope.Account == "" || g.SimpleDBScope.Region == "" {
		fail(w, r, "AccessDenied", "SimpleDB scope is not configured.")
		return
	}
	supported := false
	for _, region := range Regions {
		if region == g.SimpleDBScope.Region {
			supported = true
		}
	}
	if !supported || (r.Header.Get("X-Simulator-Region") != "" && r.Header.Get("X-Simulator-Region") != g.SimpleDBScope.Region) {
		fail(w, r, "InvalidParameterValue", "Unsupported configured region.")
		return
	}
	if len(r.Header.Values("X-Simulator-Service")) != 1 || len(r.Header.Values("X-Simulator-Region")) > 1 {
		fail(w, r, "InvalidParameterValue", "Ambiguous service or region.")
		return
	}
	simpledb.NewHTTPHandler(g.SimpleDB, g.SimpleDBScope, func(ctx context.Context, action, resource string) bool {
		decision, err := g.sdkDecision(r.WithContext(ctx), actor.ExerciseIdentity, action, resource)
		if err != nil {
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision == "allowed"
	}).ServeHTTP(w, r)
}
