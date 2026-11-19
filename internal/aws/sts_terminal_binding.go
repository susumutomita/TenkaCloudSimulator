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

type consoleInstanceBoundaryKey struct{}
type consoleInstanceBoundary struct{ region, instance string }

// ConsoleInstanceBinding authorizes a browser console session for exactly one
// instance. It applies the terminal handoff checks (participant STS only, never
// the operator, ssm:StartSession on the instance, original session) and adds a
// console boundary, so the session can describe that instance and open its
// terminal but cannot reach any other EC2 resource or generic AWS API.
func (a *STSAuthority) ConsoleInstanceBinding(r *http.Request, region, instance string) (func(*http.Request) (*http.Request, SDKIdentity, error), error) {
	if _, exists := r.Context().Value(consoleInstanceBoundaryKey{}).(consoleInstanceBoundary); exists {
		return nil, fmt.Errorf("console bindings cannot mint handoffs")
	}
	terminal, err := a.TerminalBinding(r, region, instance)
	if err != nil {
		return nil, err
	}
	return func(request *http.Request) (*http.Request, SDKIdentity, error) {
		bound, identity, err := terminal(request)
		if err != nil {
			return nil, SDKIdentity{}, err
		}
		ctx := context.WithValue(bound.Context(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region, instance})
		return bound.Clone(ctx), identity, nil
	}, nil
}
