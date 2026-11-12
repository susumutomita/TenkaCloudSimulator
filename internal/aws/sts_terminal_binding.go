package aws

import (
	"context"
	"fmt"
	"net/http"
)

type terminalBoundaryKey struct{}
type terminalBoundary struct{ region, target string }

// TerminalBinding is a separately scoped participant handoff. It does not
// broaden ConsoleBinding, and its request context must only enter session APIs.
func (a *STSAuthority) TerminalBinding(r *http.Request, region, target string) (func(*http.Request) (*http.Request, SDKIdentity, error), error) {
	caller, err := bindSSMSessionCaller(r)
	if err != nil || caller.authority != a || region == "" || target == "" {
		return nil, fmt.Errorf("verified participant target binding required")
	}
	if _, exists := r.Context().Value(terminalBoundaryKey{}).(terminalBoundary); exists {
		return nil, fmt.Errorf("terminal bindings cannot mint handoffs")
	}
	if err := caller.authorize(r.Context(), "ssm:StartSession", networkARN(region, "instance", target), ""); err != nil {
		return nil, err
	}
	return func(request *http.Request) (*http.Request, SDKIdentity, error) {
		policy, err := caller.snapshot(request.Context())
		if err != nil {
			return nil, SDKIdentity{}, err
		}
		ctx := context.WithValue(request.Context(), sdkIdentityContextKey{}, caller.identity)
		ctx = context.WithValue(ctx, sdkPolicyContextKey{}, policy)
		ctx = context.WithValue(ctx, sdkSessionKeyContextKey{}, caller.key)
		ctx = context.WithValue(ctx, stsAuthorityContextKey{}, a)
		ctx = context.WithValue(ctx, terminalBoundaryKey{}, terminalBoundary{region, target})
		return request.Clone(ctx), caller.identity, nil
	}, nil
}
func checkTerminalBoundary(r *http.Request, region, target string) error {
	if bound, ok := r.Context().Value(terminalBoundaryKey{}).(terminalBoundary); ok && (bound.region != region || bound.target != target) {
		return fmt.Errorf("terminal target boundary mismatch")
	}
	return nil
}
