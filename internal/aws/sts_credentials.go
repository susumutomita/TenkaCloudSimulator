package aws

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// SDKIdentity is established only by verified local SigV4 credentials. These
// credentials have no authority in real AWS or in a different simulator process.
type SDKIdentity struct {
	Principal, ARN, Account, UserID string
	Expires                         time.Time
}

type STSSessionRequest struct {
	Principal, RoleARN, RoleID, SessionName string
	Duration                                time.Duration
	Validate                                func(context.Context) error
	Snapshot                                func(context.Context) (core.Policy, error)
}

type STSCredentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expiration                                 time.Time
}

type sdkSession struct {
	credentials STSCredentials
	identity    SDKIdentity
	validate    func(context.Context) error
	snapshot    func(context.Context) (core.Policy, error)
}

// STSAuthority owns ephemeral credentials. It never persists or logs secrets.
// Restart invalidates sessions; the operator's existing private token file can
// be configured as AWS_SECRET_ACCESS_KEY with AWS_ACCESS_KEY_ID=TCSIMOPERATOR.
type STSAuthority struct {
	mu       sync.Mutex
	operator string
	sessions map[string]sdkSession
	now      func() time.Time
}

func NewSTSAuthority(operatorToken string) *STSAuthority {
	return &STSAuthority{operator: operatorToken, sessions: map[string]sdkSession{}, now: time.Now}
}

type stsAuthorityContextKey struct{}
type sdkIdentityContextKey struct{}
type sdkPolicyContextKey struct{}
type sdkSessionKeyContextKey struct{}
type consoleParameterBoundaryKey struct{}

func SDKPolicyFromContext(ctx context.Context) (core.Policy, bool) {
	p, ok := ctx.Value(sdkPolicyContextKey{}).(core.Policy)
	return p, ok
}

// sdkDecision consumes the policy from the same snapshot as role-generation validation.
func (g *Gateway) sdkDecision(r *http.Request, principal, action, resource string) (string, error) {
	if resourceLimit, ok := r.Context().Value(consoleParameterBoundaryKey{}).(string); ok {
		if resource != resourceLimit || (action != "ssm:GetParameter" && action != "ssm:PutParameter") {
			return "implicitDeny", nil
		}
	}
	if p, ok := SDKPolicyFromContext(r.Context()); ok {
		return core.Evaluate(p, action, resource), nil
	}
	return g.Identities.DecisionContext(r.Context(), principal, action, resource)
}

func STSAuthorityFromContext(ctx context.Context) (*STSAuthority, bool) {
	a, ok := ctx.Value(stsAuthorityContextKey{}).(*STSAuthority)
	return a, ok && a != nil
}

func SDKIdentityFromContext(ctx context.Context) (SDKIdentity, bool) {
	i, ok := ctx.Value(sdkIdentityContextKey{}).(SDKIdentity)
	return i, ok && i.Principal != ""
}

func randomCredential(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *STSAuthority) Issue(input STSSessionRequest) (STSCredentials, error) {
	if input.Principal == "" || input.Principal == core.Administrator || input.RoleID == "" || input.RoleARN == "" || input.SessionName == "" || input.Validate == nil || input.Snapshot == nil || input.Duration < 15*time.Minute || input.Duration > 12*time.Hour {
		return STSCredentials{}, fmt.Errorf("invalid local role session")
	}
	if err := input.Validate(context.Background()); err != nil {
		return STSCredentials{}, err
	}
	key, err := randomCredential(16)
	if err != nil {
		return STSCredentials{}, err
	}
	secret, err := randomCredential(32)
	if err != nil {
		return STSCredentials{}, err
	}
	token, err := randomCredential(32)
	if err != nil {
		return STSCredentials{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for key, session := range a.sessions {
		if !now.Before(session.credentials.Expiration) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 1000 {
		return STSCredentials{}, fmt.Errorf("local STS session limit reached")
	}
	credentials := STSCredentials{"TCSIMASIA" + key, secret, token, now.Add(input.Duration)}
	identity := SDKIdentity{Principal: input.Principal, ARN: "arn:aws:sts::000000000000:assumed-role/" + input.Principal + "/" + input.SessionName, Account: "000000000000", UserID: input.RoleID + ":" + input.SessionName, Expires: credentials.Expiration}
	a.sessions[credentials.AccessKeyID] = sdkSession{credentials, identity, input.Validate, input.Snapshot}
	return credentials, nil
}

// Authenticate verifies a header-signed native request, then binds its immutable
// credential identity. Query/presigned authentication is intentionally refused.
// The returned cloned request omits the now-verified session token so legacy
// service adapters cannot misinterpret it as an unverified real AWS credential.
func (a *STSAuthority) Authenticate(r *http.Request) (*http.Request, SDKIdentity, error) {
	signature, err := parseSDKSignature(r)
	if err != nil {
		return nil, SDKIdentity{}, err
	}
	a.mu.Lock()
	now := a.now()
	secret := a.operator
	// A synthetic operator account is deliberately distinct from the fixed local
	// resource account, matching the host's cross-account deployment boundary.
	identity := SDKIdentity{Principal: core.Administrator, ARN: "arn:aws:iam::999999999999:root", Account: "999999999999", UserID: core.Administrator}
	var validate func(context.Context) error
	var snapshot func(context.Context) (core.Policy, error)
	if signature.key != "TCSIMOPERATOR" {
		session, ok := a.sessions[signature.key]
		if !ok || !now.Before(session.credentials.Expiration) {
			delete(a.sessions, signature.key)
			a.mu.Unlock()
			return nil, SDKIdentity{}, fmt.Errorf("invalid or expired local AWS credentials")
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Amz-Security-Token")), []byte(session.credentials.SessionToken)) != 1 {
			a.mu.Unlock()
			return nil, SDKIdentity{}, fmt.Errorf("invalid local session token")
		}
		secret, identity, validate = session.credentials.SecretAccessKey, session.identity, session.validate
		snapshot = session.snapshot
	} else if r.Header.Get("X-Amz-Security-Token") != "" {
		a.mu.Unlock()
		return nil, SDKIdentity{}, fmt.Errorf("operator credentials cannot carry a session token")
	}
	a.mu.Unlock()
	if err := verifySDKSignature(r, signature, secret, now); err != nil {
		return nil, SDKIdentity{}, err
	}
	if validate != nil {
		if err := validate(r.Context()); err != nil {
			return nil, SDKIdentity{}, fmt.Errorf("local role session is no longer valid")
		}
	}
	ctx := context.WithValue(r.Context(), stsAuthorityContextKey{}, a)
	ctx = context.WithValue(ctx, sdkSessionKeyContextKey{}, signature.key)
	ctx = context.WithValue(ctx, sdkIdentityContextKey{}, identity)
	if snapshot != nil {
		policy, err := snapshot(r.Context())
		if err != nil {
			return nil, SDKIdentity{}, fmt.Errorf("local role session is no longer valid")
		}
		ctx = context.WithValue(ctx, sdkPolicyContextKey{}, policy)
	}
	// Receiving the signed body and reading state can span the credential deadline.
	finished := a.now()
	stamp, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if stamp.Before(finished.Add(-5*time.Minute)) || stamp.After(finished.Add(5*time.Minute)) {
		return nil, SDKIdentity{}, fmt.Errorf("local signature expired during authentication")
	}
	if !identity.Expires.IsZero() && !finished.Before(identity.Expires) {
		return nil, SDKIdentity{}, fmt.Errorf("local session expired")
	}
	clone := r.Clone(ctx)
	clone.Header.Del("X-Amz-Security-Token")
	return clone, identity, nil
}

// ConsoleBinding retains only an internal session reference, never an operator
// credential. Every use repeats expiry, role generation and policy validation.
func (a *STSAuthority) ConsoleBinding(r *http.Request, region, parameter string) (func(*http.Request) (*http.Request, SDKIdentity, error), error) {
	key, ok := r.Context().Value(sdkSessionKeyContextKey{}).(string)
	authority, _ := STSAuthorityFromContext(r.Context())
	if !ok || key == "TCSIMOPERATOR" || authority != a {
		return nil, fmt.Errorf("participant STS session required")
	}
	return func(request *http.Request) (*http.Request, SDKIdentity, error) {
		a.mu.Lock()
		session, exists := a.sessions[key]
		valid := exists && a.now().Before(session.credentials.Expiration)
		a.mu.Unlock()
		if !valid {
			return nil, SDKIdentity{}, fmt.Errorf("session expired")
		}
		if err := session.validate(request.Context()); err != nil {
			return nil, SDKIdentity{}, err
		}
		policy, err := session.snapshot(request.Context())
		if err != nil {
			return nil, SDKIdentity{}, err
		}
		if !a.now().Before(session.credentials.Expiration) {
			return nil, SDKIdentity{}, fmt.Errorf("session expired")
		}
		ctx := context.WithValue(request.Context(), sdkIdentityContextKey{}, session.identity)
		ctx = context.WithValue(ctx, sdkPolicyContextKey{}, policy)
		ctx = context.WithValue(ctx, consoleParameterBoundaryKey{}, "arn:aws:ssm:"+region+":000000000000:parameter/"+strings.TrimPrefix(parameter, "/"))
		// No STS authority or native signing key is granted to a browser.
		return request.Clone(ctx), session.identity, nil
	}, nil
}
