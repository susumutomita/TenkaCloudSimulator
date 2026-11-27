# Bounded local Cognito federation configuration

This [#253](https://github.com/susumutomita/TenkaCloudSimulator/issues/253)
slice stores and inspects local test configuration. It does **not** enable
federation sign-in. Normal launches still have only the four
[pool metadata operations](cognito-user-pools.md).

Launch the ordinary simulator with `--cognito-local-federation-config` to
enable this configuration slice. The flag requires authenticated numeric
loopback and rejects `--allow-remote`, `--unsafe-no-auth`, and hostname
listeners. Send the existing signed local Cognito AWS JSON 1.1 POST to `/`,
not a separate web route. The peer and the signed Host must be numeric
loopback; browser-origin, cookie, and forwarded requests are not accepted.
The existing whole signed request limit is **4 KiB**, including the inline
fixture XML and JSON escaping. Do not use real AWS credentials or IdP metadata;
the intended input is a compact self-contained local test document.

To exercise an HTTPS-only test topology, provide both `--cognito-local-tls-cert`
and `--cognito-local-tls-key` along with the config opt-in. The certificate must
cover the canonical numeric-loopback listener. Use a new development key and
trust its CA only in the isolated SDK or test browser. This changes the
local listener transport, not SigV4, authorization or the set of operations;
it is rejected with Battle, owned PostgreSQL, remote or unsafe modes. Without
both TLS flags the existing default HTTP configuration is unchanged.

| Opt-in operation | Supported wire input and output |
| --- | --- |
| `CreateIdentityProvider` | `UserPoolId`, `ProviderName` (`Keycloak` or `Entra`), `ProviderType: "SAML"`, `ProviderDetails: {"MetadataFile":"<xml>"}`, optional `AttributeMapping`. |
| `DescribeIdentityProvider` | `UserPoolId` and `ProviderName`. Both provider operations return `IdentityProvider` with provider name/type, pool ID, supported optional mappings, and the one selected `SSORedirectBindingURI`. The source XML, certificate and private revision do not appear in a response. |
| `CreateUserPoolClient` | `UserPoolId`, `ClientName`, `CallbackURLs`, `SupportedIdentityProviders`, `AllowedOAuthFlows: ["code"]`, `AllowedOAuthFlowsUserPoolClient: true`, and `AllowedOAuthScopes` including `openid`. `GenerateSecret` may be omitted or exactly `false`. |
| `DescribeUserPoolClient` | `UserPoolId` and the `ClientId` from creation. Both client operations return just the supported `UserPoolClient` metadata, not a secret or token. |

These four operations are advertised only when the option is enabled.
Individual client/provider update, list and delete operations remain
unsupported; existing `DeleteUserPool` removes the owned configuration.
Nondefault fields are not silently ignored. The allowlisted protocol shape
does not imply compatibility with broader Cognito tooling or provider configs.

The publisher is constructed explicitly for the provider layer's trusted scope
and local caller. Before opening storage it requires the caller's current
local policy decision for the exact action and user-pool ARN. Returning
the full raw typed configuration to an internal reader requires both client
and provider describe decisions before storage is opened. Each wire Describe
requires only its matching action, returns only the selected projection, and
never exposes the raw graph or revision.
These action/resource choices follow the
[AWS IAM reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_cognito-idp.html).
The publisher does not verify a credential; its caller must already have
done so. It does not cache policy decisions or make the decision and SQL
commit one cross-layer transaction.

| Internal operation | Bounded effect |
| --- | --- |
| Add provider | [CreateIdentityProvider](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_CreateIdentityProvider.html) policy. Exact `Keycloak` or `Entra` label; one operator-supplied inline SAML metadata entity and IdP role with one current explicitly pinned public X.509 signing certificate. |
| Add public client | [CreateUserPoolClient](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_CreateUserPoolClient.html) policy. Server-generated ID, 1–5 exact loopback callbacks, 1–2 existing providers from the same live pool, `openid` and optional `email` or `profile` only. The only representable profile is public authorization code with `S256` ([RFC 7636](https://www.rfc-editor.org/rfc/rfc7636#section-4.2)). |
| Inspect binding | Both `DescribeUserPoolClient` and `DescribeIdentityProvider` policies. Match client ID, its registered provider, exact callback string, declared `S256`, requested scopes and current pin in the same pool/revision. A supplied prior revision must still be current. This is a configuration read, not authentication. |
| Remove client/provider | The corresponding `DeleteUserPoolClient` or `DeleteIdentityProvider` policy. Provider removal fails while a client references it; it never deletes clients under the provider's policy. Pool deletion removes all configuration without a separate cleanup call. |

The local subset accepts no metadata URLs, no remote key discovery or
callbacks, no confidential client secret or alternate grant, and no authority
mapping. Inline XML is limited to 32 KiB and structurally bounded; only the
entity identifier, selected HTTP-Redirect destination, pin, validity, and
permitted claim mapping are persisted. An HTTP-POST declaration is accepted
only at the same broker and does not become an alternative destination. A pin
is an operator's trust input, not proof that the metadata document was signed.
RSA pins require an odd 2048–4096-bit modulus and odd exponent from 3 through
2³¹−1, matching the pinned Go verifier's accepted public key shape and the
local size limit.
A callback must be a canonical numeric-loopback HTTP or HTTPS URL with an
explicit port and path and cannot preselect OAuth response parameters.
Registered callback strings are compared without normalization. An optional
POST metadata entry must share the exact Redirect scheme, host and port;
it is still never selected as a fallback.

The versioned projection fits in the canonical pool's 64 KiB metadata slot,
capped at two providers and ten clients per pool. A read or failed edit does
not initialize a generation. Accepted edits serialize on the same SQLite
transaction as the scoped lookup, with exact readback and a fresh committed
revision; no separate pool registry can be recreated. Bounded maps, slices,
and revision expectations are copied before a policy callback so the checked
and used inputs are the same. The implementation does not fetch configuration
or send anything to a broker.

Later work will need service-provider metadata, fully validated signed SAML
and request correlation,
real PKCE proof, replay/expiry, and scoped issuance checks before login is
exposed. In particular, a future multi-step operation must check its original
config revision and pool lifetime again in the transaction that commits it.
No live Entra/Keycloak, SDK compatibility beyond the existing metadata
profile, or ProtoShip sign-in is implied.

Use the separate [experimental HTTPS hosted-login profile](cognito-local-hosted-login.md)
only when explicitly testing signed local browser federation. The config flag
by itself still exposes no browser flow.
