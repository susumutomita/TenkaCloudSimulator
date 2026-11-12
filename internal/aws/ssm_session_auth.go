package aws

import (
	"context"
	"fmt"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// A caller binding is minted only from the verified native SigV4 context. It
// retains no secret token and never derives authority from principal headers.
// A browser data-channel use must repeat this binding's role/policy validation.
type ssmSessionCaller struct {
	authority *STSAuthority
	key       string
	identity  SDKIdentity
}

func bindSSMSessionCaller(r *http.Request) (ssmSessionCaller, error) {
	if _, scoped := r.Context().Value(consoleParameterBoundaryKey{}).(consoleParameterBoundary); scoped {
		return ssmSessionCaller{}, fmt.Errorf("parameter handoff cannot open sessions")
	}
	authority, ok := STSAuthorityFromContext(r.Context())
	if !ok {
		return ssmSessionCaller{}, fmt.Errorf("verified native session required")
	}
	key, ok := r.Context().Value(sdkSessionKeyContextKey{}).(string)
	if !ok || key == "" || key == "TCSIMOPERATOR" {
		return ssmSessionCaller{}, fmt.Errorf("participant native session required")
	}
	identity, ok := SDKIdentityFromContext(r.Context())
	if !ok || identity.UserID == "" {
		return ssmSessionCaller{}, fmt.Errorf("verified native identity required")
	}
	caller := ssmSessionCaller{authority: authority, key: key, identity: identity}
	if _, err := caller.snapshot(r.Context()); err != nil {
		return ssmSessionCaller{}, err
	}
	return caller, nil
}
func (c ssmSessionCaller) snapshot(ctx context.Context) (core.Policy, error) {
	if c.authority == nil || c.key == "" || c.identity.UserID == "" {
		return core.Policy{}, fmt.Errorf("missing session binding")
	}
	c.authority.mu.Lock()
	session, exists := c.authority.sessions[c.key]
	valid := exists && c.authority.now().Before(session.credentials.Expiration)
	c.authority.mu.Unlock()
	if !valid || session.identity != c.identity || session.validate == nil || session.snapshot == nil {
		return core.Policy{}, fmt.Errorf("native session expired or replaced")
	}
	if err := session.validate(ctx); err != nil {
		return core.Policy{}, err
	}
	policy, err := session.snapshot(ctx)
	if err != nil {
		return core.Policy{}, err
	}
	c.authority.mu.Lock()
	again, exists := c.authority.sessions[c.key]
	valid = exists && again.identity == c.identity && c.authority.now().Before(again.credentials.Expiration)
	c.authority.mu.Unlock()
	if !valid {
		return core.Policy{}, fmt.Errorf("native session changed during authorization")
	}
	return policy, nil
}

type ssmSessionPermission struct{ action, resource string }

func (c ssmSessionCaller) authorize(ctx context.Context, action, resource, sessionOwnerTag string) error {
	return c.authorizeAll(ctx, []ssmSessionPermission{{action, resource}}, sessionOwnerTag)
}
func (c ssmSessionCaller) authorizeAll(ctx context.Context, permissions []ssmSessionPermission, sessionOwnerTag string) error {
	policy, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	values := core.PolicyContext{"aws:userid": c.identity.UserID}
	// Only a server-created retained session record supplies this tag. EC2
	// DescribeInstances does not support ResourceTag; no such key is invented.
	if sessionOwnerTag != "" {
		values["ssm:resourceTag/aws:ssmmessages:session-id"] = sessionOwnerTag
	}
	for _, permission := range permissions {
		if core.EvaluateWithContext(policy, permission.action, permission.resource, values) != "allowed" {
			return ssmFailure{"AccessDeniedException", "The participant policy does not allow this session action."}
		}
	}
	return nil
}
func (c ssmSessionCaller) owns(owner ssmSessionCaller) bool {
	// IAM's ${aws:userid}* intentionally retains wildcard semantics. Exact native
	// issuer binding prevents peer prefixes or another role generation taking over.
	return c.authority != nil && c.authority == owner.authority && c.key == owner.key && c.identity == owner.identity
}
