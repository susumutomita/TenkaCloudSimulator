package aws

import (
	"errors"
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Detect the native Secrets protocol before the other root protocols. Local
// access-token authentication remains the outer server middleware boundary.
func isSecretRequest(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "secretsmanager.") || r.Header.Get("X-Simulator-Service") == "secretsmanager" {
		return true
	}
	_, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	parts := strings.Split(scope, "/")
	return ok && len(parts) >= 4 && parts[3] == "secretsmanager"
}

func serveSecretGateway(w http.ResponseWriter, r *http.Request, store *SecretStore, identities *core.Identities) {
	if !secretSingleSelectors(r) {
		secretHTTPError(w, secretInvalid())
		return
	}
	if store == nil || identities == nil {
		secretHTTPError(w, errors.New("secret storage or identity authority is not configured"))
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if _, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential="); ok {
		parts := strings.Split(scope, "/")
		if len(parts) >= 3 {
			if region != "" && region != parts[2] {
				secretHTTPError(w, secretInvalid())
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
	// Resolve and validate the policy before any store transaction. The closure
	// only evaluates this snapshot and writes a response header; it never reads DB.
	var policy core.Policy
	if principal != core.Administrator {
		var err error
		policy, _, err = identities.PolicyContext(r.Context(), principal)
		if err != nil {
			secretHTTPError(w, err)
			return
		}
	}
	store.ServeHTTP(w, r, region, func(action, arn string) error {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, arn)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			return secretFailure{"AccessDeniedException", "IAM evaluation: " + decision}
		}
		return nil
	})
}
