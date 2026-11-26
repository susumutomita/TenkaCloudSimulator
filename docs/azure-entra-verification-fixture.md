# Local Entra-shaped OIDC verification fixture

The simulator binary has one opt-in **test verifier fixture**. This is not
Microsoft Entra ID and cannot log a user in. The normal simulator gateway,
Azure ARM catalog and storage routes are unchanged.

```sh
./dist/tenkacloud-simulator entra-verification-fixture --listen 127.0.0.1:0
```

The command binds only numeric IPv4 loopback. Port zero picks an available
port and the process reports its exact issuer, metadata URL, JWKS URI and
expected audience in one JSON line. The synthetic issuer always has the form
`http://127.0.0.1:<port>/_simulator/azure/entra/v2.0`; the audience is
`urn:tenkacloud:simulator:test:entra`. Never add either to production trust.
Do not publish or reverse-proxy this listener.

- **GET `/_simulator/azure/entra/v2.0/verification-metadata.json`:** the fixed
  issuer, audience, signing algorithm and same-origin JWKS URI. This is
  verifier metadata, deliberately not the OIDC discovery document.
- **GET `/_simulator/azure/entra/discovery/v2.0/keys`:** one ephemeral public
  RSA JWK. Private material is never returned. Replies are not cacheable.
- To exercise an explicitly configured local verifier, start with
  `--test-id-token-nonce <your-local-verifier-nonce>` (16–96 URL-safe
  characters). Startup stdout then contains one synthetic, non-production
  `test_id_token` for the exact audience, fixed synthetic subject, nonce and
  current issuer. It expires after 60 seconds. Omitting the flag emits no
  token. Do not pass real PII, cookies, tokens or client credentials.
- Restarting generates a new in-memory key. Verifiers must read the new JWKS
  when restarting the fixture; the fresh key cannot verify the old fixture
  token. No files, tenant, discovery trust, registration or Azure state survive.

Only the exact loopback authority and the two bodyless GET routes work.
Requests with `Authorization`, `Cookie`, `Forwarded`, `X-Forwarded-For`,
`X-Forwarded-Host`, or `X-Forwarded-Proto` are refused; no header configures
issuer trust. Queries, other methods and paths are refused. There is no HTTP
issuance, token exchange, login, consent, browser
callback, standard discovery, SAML, Graph, Azure RBAC or real tenant.
Production OIDC discovery requires more endpoints and HTTPS. This deliberately
bounded loopback fixture uses OIDC-style signed claims and a JWK Set for
verification tests; it is not a replacement for those production flows.

```sh
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" python3 test/e2e/entra_verification_fixture.py
```

The optional native witness verifies the exact process, local routes, public
key signature, issuer/audience/nonce/expiry, refusal, fresh key on restart and
cleanup. It does not run Keycloak or Cognito or validate SAML or browser SSO.

References: [Microsoft identity platform OIDC](https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc),
[OIDC Discovery 1.0](https://openid.net/specs/openid-connect-discovery-1_0.html),
and [RFC 7517: JSON Web Key](https://www.rfc-editor.org/rfc/rfc7517).
