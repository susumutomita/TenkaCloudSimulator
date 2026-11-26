package aws

import (
	"context"
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const cognitoUserPoolsSigningName = "cognito-idp"

func isCognitoUserPools(r *http.Request) bool {
	for _, target := range r.Header.Values("X-Amz-Target") {
		if strings.HasPrefix(target, cognitoidp.TargetPrefix) {
			return true
		}
	}
	return r.Header.Get("X-Simulator-Service") == cognitoUserPoolsSigningName
}

func (g *Gateway) serveCognitoUserPools(w http.ResponseWriter, r *http.Request) {
	actor, authenticated := provider.ActorFromContext(r.Context())
	sdk, signed := SDKIdentityFromContext(r.Context())
	// Neither an unsigned local token/browser session, nor a Cognito/Entra
	// assertion or insecure dummy signing string admits this native SDK path.
	sig, err := parseSDKSignature(r)
	if !authenticated || !signed || err != nil || sig.service != cognitoUserPoolsSigningName ||
		actor.CredentialIdentity != sdk.Principal || g.Identities == nil {
		cognitoidp.RejectAuthentication(w)
		return
	}
	cognitoidp.NewHTTPHandler(g.CognitoUserPools,
		cognitoidp.Scope{Account: g.CognitoResourceAccount, Region: sig.region},
		cognitoidp.Actor{Credential: actor.CredentialIdentity, Exercise: actor.ExerciseIdentity},
		func(ctx context.Context, action, resource string) bool {
			decision, err := g.sdkDecision(r.WithContext(ctx), actor.ExerciseIdentity, action, resource)
			if err != nil {
				return false
			}
			w.Header().Set("X-Simulator-IAM-Decision", decision)
			return decision == "allowed"
		}).ServeHTTP(w, r)
}
