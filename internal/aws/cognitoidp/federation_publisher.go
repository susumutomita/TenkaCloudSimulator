// The federation publisher is a private opt-in adapter for an authenticated,
// trusted management caller. Nothing registers it in the simulator.
package cognitoidp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const (
	createFederationProvider   = "cognito-idp:" + nativeCreateIdentityProvider
	describeFederationProvider = "cognito-idp:" + nativeDescribeIdentityProvider
	deleteFederationProvider   = "cognito-idp:DeleteIdentityProvider"
	createFederationClient     = "cognito-idp:" + nativeCreateUserPoolClient
	describeFederationClient   = "cognito-idp:" + nativeDescribeUserPoolClient
	deleteFederationClient     = "cognito-idp:DeleteUserPoolClient"
)

var (
	errFederationDenied    = errors.New("local IAM does not allow this federation configuration action")
	errFederationNotFound  = errors.New("no matching local federation configuration")
	errFederationDuplicate = errors.New("federation configuration already exists")
	errFederationInUse     = errors.New("federation provider is referenced by application clients")
	errFederationLimit     = errors.New("local federation configuration capacity exceeded")
)

// Neither scope, actor nor authorize can come from a request body. A future
// adapter has to build this for its verified local SDK caller, exactly as in
// NewHTTPHandler. A nonnil Authorize is not proof of a verified signer.
type federationPublisher struct {
	store     *Store
	scope     Scope
	authorize Authorize
}

func newFederationPublisher(store *Store, scope Scope, actor Actor, authorize Authorize) (*federationPublisher, error) {
	if store == nil || store.db == nil || !scope.valid() || !actor.valid() || authorize == nil {
		return nil, errFederationDenied
	}
	return &federationPublisher{store, scope, authorize}, nil
}

func (p *federationPublisher) admit(ctx context.Context, action, id string) error {
	if p == nil || p.store == nil || p.authorize == nil {
		return errFederationDenied
	}
	if !p.scope.validPoolID(id) {
		return errInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Match the AWS actions and exact, locally configured pool ARN. The caller
	// performs live policy evaluation before each operation, not as part of the
	// SQLite write transaction, and a configuration revision is never a grant.
	if !p.authorize(ctx, action, p.scope.arn(id)) {
		return errFederationDenied
	}
	return nil
}

func (p *federationPublisher) change(ctx context.Context, action, id string, edit func(*federationConfig) error) error {
	if err := p.admit(ctx, action, id); err != nil {
		return err
	}
	store, err := p.store.openFederationStorage(ctx)
	if err != nil {
		return err
	}
	owner := federationOwner{p.scope, id}
	_, err = store.transition(ctx, p.scope, id, func(current federationSnapshot, _ bool) ([]byte, bool, error) {
		cfg, err := readFederationConfig(current.metadata, owner)
		if err != nil {
			return nil, false, err
		}
		if err = edit(&cfg); err != nil {
			return nil, false, err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return nil, false, err
		}
		if len(raw) > maxFederationMetadata {
			return nil, false, errFederationLimit
		}
		return raw, true, nil
	})
	return err
}

// configuration is the sole raw reader and requires both Describe decisions.
// A native Describe authorizes and returns just its one matching projection.
func (p *federationPublisher) configuration(ctx context.Context, id string) (federationConfig, federationRevision, error) {
	return p.readConfiguration(ctx, id, federationSelection{})
}

type federationSelection struct{ provider, client string }

func (p *federationPublisher) readConfiguration(ctx context.Context, id string, selection federationSelection) (federationConfig, federationRevision, error) {
	actions := []string{describeFederationClient, describeFederationProvider}
	switch {
	case selection.provider != "":
		if selection.client != "" || !providerName(selection.provider) {
			return federationConfig{}, federationRevision{}, errInvalid
		}
		actions = []string{describeFederationProvider}
	case selection.client != "":
		if !validFederationClientID(selection.client) {
			return federationConfig{}, federationRevision{}, errInvalid
		}
		actions = []string{describeFederationClient}
	}
	for _, action := range actions {
		if err := p.admit(ctx, action, id); err != nil {
			return federationConfig{}, federationRevision{}, err
		}
	}
	store, err := p.store.openFederationStorage(ctx)
	if err != nil {
		return federationConfig{}, federationRevision{}, err
	}
	snapshot, err := store.transition(ctx, p.scope, id, nil)
	if err != nil {
		return federationConfig{}, federationRevision{}, err
	}
	cfg, err := readFederationConfig(snapshot.metadata, federationOwner{p.scope, id})
	if err != nil {
		return federationConfig{}, federationRevision{}, err
	}
	projection := federationConfig{Version: cfg.Version, Owner: cfg.Owner}
	switch {
	case selection.provider != "":
		i := cfg.providerIndex(selection.provider)
		if i < 0 {
			return federationConfig{}, federationRevision{}, errFederationNotFound
		}
		projection.Providers = []federationProvider{cfg.Providers[i]}
		return projection, federationRevision{}, nil
	case selection.client != "":
		i := cfg.clientIndex(selection.client)
		if i < 0 {
			return federationConfig{}, federationRevision{}, errFederationNotFound
		}
		projection.Clients = []federationClient{cfg.Clients[i]}
		return projection, federationRevision{}, nil
	default:
		return cfg, snapshot.revision, nil
	}
}

func (c federationConfig) providerIndex(name string) int {
	return slices.IndexFunc(c.Providers, func(provider federationProvider) bool { return provider.Name == name })
}

func (c federationConfig) clientIndex(id string) int {
	return slices.IndexFunc(c.Clients, func(client federationClient) bool { return client.ID == id })
}

func (p *federationPublisher) addProvider(ctx context.Context, id string, input federationProviderInput) (federationProvider, error) {
	provider, err := compileFederationProvider(input, time.Now())
	if err != nil {
		return federationProvider{}, err
	}
	if err := p.change(ctx, createFederationProvider, id, func(cfg *federationConfig) error {
		if cfg.providerIndex(provider.Name) >= 0 {
			return errFederationDuplicate
		}
		if len(cfg.Providers) >= maxFederationProviders {
			return errFederationLimit
		}
		if !provider.currentPin(time.Now()) {
			return errInvalid
		}
		var registration [federationRegistrationBytes]byte
		rand.Read(registration[:])
		provider.RegistrationID = hex.EncodeToString(registration[:])
		if slices.ContainsFunc(cfg.Providers, func(v federationProvider) bool { return v.RegistrationID == provider.RegistrationID }) {
			return errFederationDuplicate
		}
		cfg.Providers = append(cfg.Providers, provider)
		return nil
	}); err != nil {
		return federationProvider{}, err
	}
	return provider, nil
}

func (p *federationPublisher) removeProvider(ctx context.Context, id, name string) error {
	if !providerName(name) {
		return errInvalid
	}
	return p.change(ctx, deleteFederationProvider, id, func(cfg *federationConfig) error {
		at := cfg.providerIndex(name)
		if at < 0 {
			return errFederationNotFound
		}
		// Do not silently delete dependent clients under a different IAM action
		// or leave references that could bind to a provider with a new pin.
		for _, client := range cfg.Clients {
			if slices.Contains(client.Providers, name) {
				return errFederationInUse
			}
		}
		cfg.Providers = slices.Delete(cfg.Providers, at, at+1)
		return nil
	})
}

func (p *federationPublisher) addClient(ctx context.Context, id string, input federationClientInput) (federationClient, error) {
	if len(input.Callbacks) > maxFederationCallbacks || len(input.Providers) > maxFederationProviders || len(input.Scopes) > maxFederationScopes {
		return federationClient{}, errInvalid
	}
	input.Callbacks = slices.Clone(input.Callbacks)
	input.Providers = slices.Clone(input.Providers)
	input.Scopes = slices.Clone(input.Scopes)
	if !input.valid() {
		return federationClient{}, errInvalid
	}
	var client federationClient
	err := p.change(ctx, createFederationClient, id, func(cfg *federationConfig) error {
		if len(cfg.Clients) >= maxFederationClients {
			return errFederationLimit
		}
		now := time.Now()
		for _, name := range input.Providers {
			i := cfg.providerIndex(name)
			if i < 0 || !cfg.Providers[i].currentPin(now) {
				return errFederationNotFound
			}
		}
		var suffix [12]byte
		rand.Read(suffix[:])
		newID := hex.EncodeToString(suffix[:])
		if cfg.clientIndex(newID) >= 0 {
			return errFederationDuplicate
		}
		client = federationClient{newID, input}
		cfg.Clients = append(cfg.Clients, client)
		return nil
	})
	if err != nil {
		return federationClient{}, err
	}
	return client, nil
}

func (p *federationPublisher) removeClient(ctx context.Context, id, clientID string) error {
	if !validFederationClientID(clientID) {
		return errInvalid
	}
	return p.change(ctx, deleteFederationClient, id, func(cfg *federationConfig) error {
		i := cfg.clientIndex(clientID)
		if i < 0 {
			return errFederationNotFound
		}
		cfg.Clients = slices.Delete(cfg.Clients, i, i+1)
		return nil
	})
}

type federationBindingRequest struct {
	ClientID   string
	Provider   string
	Callback   string
	PKCEMethod string // Declaration only; this stage never receives a challenge/verifier.
	Scopes     []string
}

type federationBinding struct {
	Owner    federationOwner
	Revision federationRevision
	ClientID string
	Provider federationProvider
	Callback string
	Scopes   []string
}

// inspectBinding proves only that an exact tuple exists in a current config
// revision. It is NOT authentication. Future sign-in must validate real SAML,
// PKCE proof, request/response state and expiry, then atomically recheck the
// original revision and apply any flow transition.
func (p *federationPublisher) inspectBinding(ctx context.Context, id string, request federationBindingRequest, expected *federationRevision) (federationBinding, error) {
	if len(request.Scopes) > maxFederationScopes {
		return federationBinding{}, errInvalid
	}
	request.Scopes = slices.Clone(request.Scopes)
	var expectedRevision federationRevision
	checkRevision := expected != nil
	if checkRevision {
		expectedRevision = *expected
	}
	if !validFederationClientID(request.ClientID) || !providerName(request.Provider) ||
		request.PKCEMethod != federationPKCE || !validFederationScopes(request.Scopes) {
		return federationBinding{}, errInvalid
	}
	if _, err := localFederationURL(request.Callback, true); err != nil {
		return federationBinding{}, err
	}
	cfg, revision, err := p.configuration(ctx, id)
	if err != nil {
		return federationBinding{}, err
	}
	if checkRevision && expectedRevision != revision {
		return federationBinding{}, errFederationStale
	}
	clientIndex, providerIndex := cfg.clientIndex(request.ClientID), cfg.providerIndex(request.Provider)
	if clientIndex < 0 || providerIndex < 0 {
		return federationBinding{}, errFederationNotFound
	}
	client, provider := cfg.Clients[clientIndex], cfg.Providers[providerIndex]
	if !slices.Contains(client.Providers, provider.Name) || !slices.Contains(client.Callbacks, request.Callback) ||
		!provider.offers(request.Scopes) || !provider.currentPin(time.Now()) {
		return federationBinding{}, errFederationNotFound
	}
	for _, scope := range request.Scopes {
		if !slices.Contains(client.Scopes, scope) {
			return federationBinding{}, errFederationNotFound
		}
	}
	return federationBinding{cfg.Owner, revision, client.ID, provider, request.Callback, request.Scopes}, nil
}
