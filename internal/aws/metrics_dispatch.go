package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/metrics"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// Metadata selects a protocol, never an authenticated principal. Recognize even
// conflicting monitoring intent so it cannot fall through to CFN/S3 success.
func isMetricsRequest(r *http.Request) bool {
	for _, raw := range r.Header.Values("X-Simulator-Service") {
		for _, value := range strings.Split(raw, ",") {
			if strings.EqualFold(strings.TrimSpace(value), "monitoring") {
				return true
			}
		}
	}
	for _, raw := range r.Header.Values("Authorization") {
		_, credential, ok := strings.Cut(raw, "Credential=")
		parts := strings.SplitN(credential, "/", 5)
		if ok && len(parts) >= 4 && strings.EqualFold(parts[3], "monitoring") {
			return true
		}
	}
	return false
}
func metricsMetadata(r *http.Request) (string, error) {
	invalid := func() error {
		return &metrics.Error{Code: "InvalidParameterValue", Message: "Metric Region or service metadata is invalid.", Status: 400}
	}
	one := func(name string) (string, bool) {
		values := r.Header.Values(name)
		if len(values) == 0 {
			return "", true
		}
		if len(values) != 1 {
			return "", false
		}
		return values[0], true
	}
	service, ok := one("X-Simulator-Service")
	if !ok || service != "" && service != "monitoring" {
		return "", invalid()
	}
	region, ok := one("X-Simulator-Region")
	if !ok {
		return "", invalid()
	}
	auth, ok := one("Authorization")
	if !ok {
		return "", invalid()
	}
	if len(r.Header.Values("X-Amz-Security-Token")) != 0 {
		return "", &metrics.Error{Code: "InvalidParameterCombination", Message: "Session credentials are unsupported.", Status: 400}
	}
	if auth != "" {
		_, credential, found := strings.Cut(auth, "Credential=")
		parts := strings.SplitN(credential, "/", 5)
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !found || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || len(parts[1]) != 8 || !strings.HasPrefix(parts[4], "aws4_request,") {
			return "", &metrics.Error{Code: "AccessDenied", Message: "Use unsigned requests or dummy TCSIM credentials for monitoring.", Status: 403}
		}
		for _, c := range parts[1] {
			if c < '0' || c > '9' {
				return "", invalid()
			}
		}
		if parts[3] != "monitoring" || parts[2] == "" || region != "" && region != parts[2] {
			return "", invalid()
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		return "", invalid()
	}
	return region, nil
}

type metricsAdapterScopeKey struct{}
type metricsGateway struct{ native *metrics.Handler }

// NewMetricsGateway binds the leaf to the SAME database as the identity-policy
// projection. Hosts must supply Actor through actual local authentication. The
// callback reads policy with the caller's SQL Tx, never a nested DB connection.
func NewMetricsGateway(store *metrics.Store, database *sql.DB) (http.Handler, error) {
	if store == nil || database == nil || !store.UsesDatabase(database) {
		return nil, fmt.Errorf("metrics and identity authority require the same database")
	}
	native, err := metrics.NewQueryHandler(store, func(r *http.Request) (metrics.Scope, error) {
		scope, ok := r.Context().Value(metricsAdapterScopeKey{}).(metrics.Scope)
		if !ok {
			return metrics.Scope{}, metrics.ErrUnauthenticated
		}
		return scope, nil
	})
	if err != nil {
		return nil, err
	}
	return &metricsGateway{native: native}, nil
}
func (g *metricsGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := provider.ActorFromContext(r.Context())
	if !ok {
		metrics.WriteQueryError(w, &metrics.Error{Code: "NotAuthorized", Message: "Authenticated local actor context is required.", Status: 401})
		return
	}
	region, err := metricsMetadata(r)
	if err != nil {
		metrics.WriteQueryError(w, err)
		return
	}
	principal := actor.ExerciseIdentity
	scope := metrics.Scope{Region: region, Authorize: func(ctx context.Context, tx *sql.Tx, action, resource string) (bool, error) {
		if tx == nil || resource != "*" || !slices.Contains([]string{"cloudwatch:PutMetricData", "cloudwatch:ListMetrics", "cloudwatch:GetMetricStatistics"}, action) {
			return false, fmt.Errorf("invalid metrics authorization contract")
		}
		decision := "allowed"
		// Administrator is an explicit selected exercise identity supplied by the
		// trusted actor, matching core's existing policy semantics. CredentialRole
		// is never used to bypass a reader's policy or supply a missing identity.
		if principal != core.Administrator {
			var document []byte
			err := tx.QueryRowContext(ctx, "SELECT document FROM identity_policies WHERE name=?", principal).Scan(&document)
			if errors.Is(err, sql.ErrNoRows) {
				decision = "implicitDeny"
			} else if err != nil {
				return false, err
			} else {
				if len(document) > 32<<10 {
					return false, fmt.Errorf("invalid persisted metric identity policy")
				}
				policy, err := core.ParsePolicy(document)
				if err != nil {
					return false, err
				}
				decision = core.Evaluate(policy, action, resource)
			}
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision == "allowed", nil
	}}
	g.native.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), metricsAdapterScopeKey{}, scope)))
}
func (g *Gateway) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if g.CloudWatchMetrics == nil {
		metrics.WriteQueryError(w, &metrics.Error{Code: "UnknownOperationException", Message: "Metric state is not configured.", Status: 501})
		return
	}
	g.CloudWatchMetrics.ServeHTTP(w, r)
}
