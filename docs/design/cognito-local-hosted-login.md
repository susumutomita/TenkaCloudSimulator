# Experimental local HTTPS Cognito federation (#253)

This test-only profile does not enable hosted login in the default build or
use AWS, live Entra, DNS discovery or remote key/metadata fetch. It is distinct
from AWS hosted UI feature parity and does not qualify any existing application.
The [AWS authorize](https://docs.aws.amazon.com/cognito/latest/developerguide/authorization-endpoint.html)
and [token](https://docs.aws.amazon.com/cognito/latest/developerguide/token-endpoint.html)
endpoints supply the wire names; the explicit simulator bounds are narrower.

Build explicitly with the separate, pinned local experiment manifest:

```sh
CGO_ENABLED=0 go build -mod=readonly -modfile=cognito-hosted.mod -tags=cognito_hosted -o dist/cognito-local ./cmd/simulator
```

Run `make check-cognito-hosted` for focused provider, HTTP and CLI tests,
including issuer-pinned signed ACS and code redemption with offline synthetic
Entra and Keycloak assertions. It also compiles the tagged static binary and
verifies its separate module graph.
`make before-commit` and the normal pull request CI require this check. It does
not enable hosted login in the default binary or change release artifacts.

Start using the existing `--cognito-local-federation-config` and both
`--cognito-local-tls-cert` and `--cognito-local-tls-key`, plus the explicit
`--cognito-local-hosted-login` flag. Use a private scratch state/token and
an isolated browser trust store. This requires direct numeric loopback
and a current TLS certificate covering that exact IP. It cannot run with remote,
no-auth, Battle, or owned PostgreSQL configuration. The client, simulator and
IdP must be HTTPS on the same numeric IP; their loopback ports may differ.

1. Using the ordinary **signed** local SDK API, create a pool, register a
   self-contained local signing pin and redirect endpoint under the exact
   `Entra` or `Keycloak` name, and register a public code-flow client, the
   exact HTTPS callback and selected scopes. Neither label can fall back
   to the other. See [the management scope](cognito-federation-configuration.md).
   Providers saved before registration IDs were introduced remain manageable,
   but cannot do hosted login. Use a new scratch pool for this profile, or
   explicitly remove the dependent clients and re-register the provider and
   clients. No prior registration or identity is silently taken over.
2. For an existing pool, `GET /_simulator/cognito/<pool>/saml2/metadata`
   supplies the SP entity and POST ACS URL. Configure the isolated IdP for
   this exact entity and URL. The browser begins at the pool's
   `GET /oauth2/authorize` with an explicit client/provider, exact
   callback, state, scopes, and S256 challenge. A supplied `nonce` must be
   non-empty and appears unchanged in the ID token; if omitted, the server
   generates a fresh value for this request, following [Cognito's federated
   ID token behavior](https://docs.aws.amazon.com/cognito/latest/developerguide/amazon-cognito-user-pools-using-the-id-token.html).
3. The only trusted callback is the same pool's `POST /saml2/idpresponse`
   from the registered IdP Origin, correlated to both the request and its
   Secure/HttpOnly browser cookie. The pinned SAML parser validates the
   authenticated node, issuer, recipient, audience, lifetime and request.
   Unsigned messages are rejected; verified assertion-only SAML conforms
   to the Entra-style envelope without needing to trust its unsigned fields.
   AuthnRequest and metadata specify persistent NameID. Set Keycloak to honor
   that request or to emit [persistent NameID](https://www.keycloak.org/docs/latest/server_admin/#_client-saml-configuration)
   when overriding the request. Its opaque persistent NameID must be 1–256
   printable ASCII characters without spaces. Entra must supply exactly one
   plain signed [object identifier](https://learn.microsoft.com/en-us/entra/identity-platform/reference-saml-tokens),
   `http://schemas.microsoft.com/identity/claims/objectidentifier`,
   a nonzero lowercase directory object GUID. Entra Emulator v0.9.0 can put a
   mutable UPN in NameID even with the persistent format, so Entra identity
   never falls back to NameID, email or display name. On either provider, the
   subject's qualifiers, when present, must match the pinned issuer and exact
   pool SP entity. Transient, absent or other NameID formats, alternate SP
   identifiers and encrypted SAML fields are unsupported.
4. The client validates OAuth state itself and redeems the one-minute
   single-use code on `POST /oauth2/token` with its exact redirect, client
   and verifier. The local JWT issuer is the HTTPS pool URL. Separate
   ephemeral RS256 ID and access keys are visible at
   `GET /.well-known/jwks.json` under that pool. Verify issuer, intended
   audience/client, token use and expiry, and compare the ID token nonce
   to the original value when the client supplied one. Rejected exchanges
   return a generic HTTP 400 rather than an AWS OAuth error document.
   `sub` is derived from the supported signed identifier and the account,
   region, pool and chosen trust registration (including its exact issuer,
   pin and redirect endpoint). It stays the same across repeat sign-ins,
   unrelated client edits and process restarts. Separate registrations never
   share subjects, even with identical issuer/key and subject bytes. Deleting
   and re-creating a registration or changing its trusted authority gives
   different subjects; there is no implicit link or pin-rotation migration.

## Run the public local Entra witness

This repository implements the Cognito service provider and public OAuth client
exchange only. It does not contain an Entra SAML identity provider. The unrelated
[Entra verification fixture](../azure-entra-verification-fixture.md) is not an
IdP and cannot replace one in the following test.

The compatible independent IdP is [Entra Emulator v0.9.0](https://github.com/calvinchengx/entra-emulator/releases/tag/v0.9.0)
at clean upstream commit `cdbef2c0f3d28c00f19e5f98072406f7382c6d0f` (Apache-2.0,
upstream LICENSE and NOTICE). Supply its public binary yourself, or build it from
that verified upstream source with `go build -trimpath ./cmd/entra-emulator`.
This Simulator profile does not ship it, install it, or start it on service boot.
The upstream emulator is a **local developer tool**, not Microsoft Entra ID.
Its admin API is unauthenticated; launch only on 127.0.0.1 in disposable state.

With the tagged Simulator just built above, the separately supplied IdP, an
installed Chromium executable, `certutil` (NSS), and the development Python
dependencies from [requirements-test.txt](../../scripts/requirements-test.txt):

```sh
python3 test/e2e/cognito_hosted_public_entra.py \
  --simulator dist/cognito-local \
  --entra "$PUBLIC_ENTRA_EMULATOR_BINARY" \
  --chromium "$CHROMIUM_BINARY" \
  --expected-simulator-revision "$(git rev-parse HEAD)" \
  --expected-simulator-sha256 "$(shasum -a 256 dist/cognito-local | cut -d ' ' -f1)"
```

Use the exact clean Simulator commit you built. The witness requires its full
revision and SHA-256, refuses mismatches and Python `-O` before starting any
fixture processes, and records the checked pairing with the successful result.
It does not obtain or install either binary for you.

The witness launches the external process using its documented `HOST`, `PORT`,
`ORIGIN_MODE=compat`, `PUBLIC_ORIGIN`, TLS paths, and `ENTRA_DATA_DIR` settings.
It disables seeded accounts, generates one dummy account, and registers only
the exact local SP entity and POST ACS with the IdP. It gives a fresh
loopback CA to the isolated Chromium trust store; it does not ignore TLS errors
or modify host trust. The browser drives IdP password sign-in, a genuinely
signed SAML POST, the Cognito authorization result, and a separate protected
test page. The application checks its own state, S256, token signature,
issuer, audience, use, expiry and nonce. It checks wrong verifier/replay
refusal and stable identity across an upstream UPN rename. No existing
directory, real AWS, real Entra, private application or token is reused.

The public browser witness exercises Entra only. Keycloak registration,
persistent NameID and signed response handling are exercised with the tagged
fixtures; this is not a live Keycloak compatibility result. The witness is
a targeted opt-in test, not an every-change end-to-end CI job.

The [bounded model](../../formal/CognitoHostedLoginLifecycle.tla) separately
covers selected trust registration, expiry/epoch races, verified return and
single-use grants in synthetic states. It does not prove real XML/TLS,
Go/SQLite refinement or physical provider interoperability.

A runtime slot on the original account/region/pool SQLite row carries bounded
pending state, grants and replay digests. Configuration revision, exact row
lifetime and compare-and-set readback are checked in the issuance transaction.
Native deletion removes the browser slot with the pool. Process restart
invalidates pending operations and keys; this is not a refresh or rotation
contract. Provider configuration stays durable. There is no extra pool
store, simulator API authority from JWTs, refresh token, session reuse, host
SSO, hosted chooser/login form, discovery, logout or real-cloud compatibility.

Validate production builds and the separate pinned Battle inputs independently.
The ordinary Go manifest, artifact pins and admission rules are not updated
by the local experiment build. Do not reuse a database written by this test
profile with an older binary that does not recognize registration IDs.
