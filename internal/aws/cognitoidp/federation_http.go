package cognitoidp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

const (
	nativeCreateIdentityProvider   = "CreateIdentityProvider"
	nativeDescribeIdentityProvider = "DescribeIdentityProvider"
	nativeCreateUserPoolClient     = "CreateUserPoolClient"
	nativeDescribeUserPoolClient   = "DescribeUserPoolClient"
)

// LocalFederationConfigOperations is the opt-in wire capability, not sign-in.
func LocalFederationConfigOperations() []string {
	return []string{nativeCreateIdentityProvider, nativeDescribeIdentityProvider, nativeCreateUserPoolClient, nativeDescribeUserPoolClient}
}

func localFederationOperation(op string) bool {
	switch op {
	case nativeCreateIdentityProvider, nativeDescribeIdentityProvider, nativeCreateUserPoolClient, nativeDescribeUserPoolClient:
		return true
	default:
		return false
	}
}

// Use the actual socket peer and the signed Host. Never recover either
// from proxy headers or accept browser-origin management on this opt-in path.
func directLocalFederationRequest(r *http.Request) bool {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !peer.Addr().IsLoopback() || peer.Port() == 0 {
		return false
	}
	var host netip.Addr
	if addrPort, err := netip.ParseAddrPort(r.Host); err == nil {
		if addrPort.Port() == 0 {
			return false
		}
		host = addrPort.Addr()
	} else {
		name, bracketed := r.Host, false
		if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
			name, bracketed = name[1:len(name)-1], true
		}
		host, err = netip.ParseAddr(name)
		if err != nil || host.Is6() != bracketed {
			return false
		}
	}
	if !host.IsLoopback() || host.Zone() != "" || peer.Addr().Zone() != "" {
		return false
	}
	for name := range r.Header {
		key := strings.ToLower(name)
		if key == "origin" || key == "cookie" || key == "forwarded" ||
			strings.HasPrefix(key, "x-forwarded-") || strings.HasPrefix(key, "sec-fetch-") {
			return false
		}
	}
	return true
}

func serveLocalFederationConfig(w http.ResponseWriter, ctx context.Context, store *Store, scope Scope, actor Actor, authorize Authorize, op string, raw []byte) {
	fields := boundedObjectFields(raw, 8)
	id := clientString(fields["UserPoolId"])
	if fields == nil || !scope.validPoolID(id) {
		operationError(w, errInvalid)
		return
	}
	p, err := newFederationPublisher(store, scope, actor, authorize)
	if err != nil {
		RejectAuthentication(w)
		return
	}
	var body any
	switch op {
	case nativeCreateIdentityProvider:
		details := boundedObjectFields(fields["ProviderDetails"], 1)
		metadata := clientString(details["MetadataFile"])
		mapping, valid := wireFederationMappings(fields)
		if !only(fields, "UserPoolId", "ProviderName", "ProviderType", "ProviderDetails", "AttributeMapping") ||
			clientString(fields["ProviderType"]) != "SAML" || !only(details, "MetadataFile") || metadata == "" || !valid {
			operationError(w, errInvalid)
			return
		}
		var provider federationProvider
		provider, err = p.addProvider(ctx, id, federationProviderInput{clientString(fields["ProviderName"]), metadata, mapping})
		if err == nil {
			body = nativeProviderView(id, provider)
		}
	case nativeDescribeIdentityProvider:
		name := clientString(fields["ProviderName"])
		if !only(fields, "UserPoolId", "ProviderName") || !providerName(name) {
			operationError(w, errInvalid)
			return
		}
		var cfg federationConfig
		cfg, _, err = p.readConfiguration(ctx, id, federationSelection{provider: name})
		if err == nil {
			body = nativeProviderView(id, cfg.Providers[0])
		}
	case nativeCreateUserPoolClient:
		input, ok := wireFederationClient(fields)
		if !ok {
			operationError(w, errInvalid)
			return
		}
		var client federationClient
		client, err = p.addClient(ctx, id, input)
		if err == nil {
			body = nativeClientView(id, client)
		}
	case nativeDescribeUserPoolClient:
		client := clientString(fields["ClientId"])
		if !only(fields, "UserPoolId", "ClientId") || !validFederationClientID(client) {
			operationError(w, errInvalid)
			return
		}
		var cfg federationConfig
		cfg, _, err = p.readConfiguration(ctx, id, federationSelection{client: client})
		if err == nil {
			body = nativeClientView(id, cfg.Clients[0])
		}
	default:
		operationError(w, errInvalid)
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, errFederationDenied):
			writeError(w, http.StatusForbidden, "AccessDeniedException", "Local IAM policy does not allow this user pool operation.")
		case errors.Is(err, errFederationNotFound):
			writeError(w, http.StatusBadRequest, "ResourceNotFoundException", "Configuration not found in the local user pool.")
		case errors.Is(err, errFederationDuplicate) && op == nativeCreateIdentityProvider:
			writeError(w, http.StatusBadRequest, "DuplicateProviderException", "Identity provider is already configured in the local user pool.")
		case errors.Is(err, errFederationLimit):
			writeError(w, http.StatusBadRequest, "LimitExceededException", "Local federation configuration limit reached.")
		default:
			operationError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func wireFederationMappings(fields map[string]json.RawMessage) (map[string]string, bool) {
	raw, present := fields["AttributeMapping"]
	if !present {
		return nil, true
	}
	object := boundedObjectFields(raw, maxFederationMapping)
	if object == nil {
		return nil, false
	}
	mapping := make(map[string]string, len(object))
	for key, raw := range object {
		if value := clientString(raw); value != "" {
			mapping[key] = value
		} else {
			return nil, false
		}
	}
	return mapping, validFederationMapping(mapping)
}

func wireFederationClient(fields map[string]json.RawMessage) (federationClientInput, bool) {
	if !only(fields, "UserPoolId", "ClientName", "CallbackURLs", "SupportedIdentityProviders", "AllowedOAuthFlows",
		"AllowedOAuthScopes", "AllowedOAuthFlowsUserPoolClient", "GenerateSecret") ||
		string(fields["AllowedOAuthFlowsUserPoolClient"]) != "true" {
		return federationClientInput{}, false
	}
	if secret, present := fields["GenerateSecret"]; present && string(secret) != "false" {
		return federationClientInput{}, false
	}
	var flow []string
	input := federationClientInput{Name: clientString(fields["ClientName"])}
	for _, row := range []struct {
		field string
		dst   *[]string
	}{{"AllowedOAuthFlows", &flow}, {"CallbackURLs", &input.Callbacks},
		{"SupportedIdentityProviders", &input.Providers}, {"AllowedOAuthScopes", &input.Scopes}} {
		if json.Unmarshal(fields[row.field], row.dst) != nil {
			return federationClientInput{}, false
		}
	}
	return input, len(flow) == 1 && flow[0] == "code" && input.valid()
}

// Only the fields supported by this bounded local profile are projected. Do
// not echo the raw XML, stored certificate bytes or internal revision values.
func nativeProviderView(id string, provider federationProvider) any {
	view := map[string]any{"UserPoolId": id, "ProviderName": provider.Name, "ProviderType": "SAML",
		"ProviderDetails": map[string]string{"SSORedirectBindingURI": provider.RedirectURL}}
	if len(provider.Mapping) != 0 {
		view["AttributeMapping"] = provider.Mapping
	}
	return map[string]any{"IdentityProvider": view}
}

func nativeClientView(id string, client federationClient) any {
	return map[string]any{"UserPoolClient": map[string]any{
		"UserPoolId": id, "ClientName": client.Name, "ClientId": client.ID,
		"CallbackURLs": client.Callbacks, "SupportedIdentityProviders": client.Providers,
		"AllowedOAuthFlowsUserPoolClient": true, "AllowedOAuthFlows": []string{"code"},
		"AllowedOAuthScopes": client.Scopes,
	}}
}
