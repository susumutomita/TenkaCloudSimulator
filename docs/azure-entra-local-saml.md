# First-party local Entra-shaped SAML identity provider

This component authenticates **one generated synthetic user** for **one
explicitly registered local service provider (SP)**. It is independent of
Cognito, Azure Resource Manager and the
[separate verification fixture](azure-entra-verification-fixture.md). The
verification fixture does not log a person in; this one does not implement
OIDC discovery, token exchange or Microsoft Graph. Neither emulates Entra ID
as a real production directory.

## Launcher boundary

An explicitly selected **local development** launcher creates the two numeric
loopback HTTPS listeners and temporary TLS material before provisioning. It
passes the actual Entra origin and the SP's exact entity ID, HTTPS ACS URL
and public RSA request-signing certificate into `entraidp.New`. Do not add the
local CA or the generated IdP issuer to host or production trust. Use an
isolated browser context with explicit test CA trust.

The resulting handler and its `Public()` descriptor can be reused wherever a
local Azure exercise needs a SAML IdP. The descriptor contains only the
generated tenant and object IDs, synthetic email (via `Public().Email()`),
issuer, metadata and sign-in URLs, and public signing certificate. The email
is not in the served federation metadata. The launcher installs the narrowly scoped IdP
trust into the chosen SP from that descriptor. Each side keeps its own
signing key. The SP does not receive an account secret, a simulator operator
credential, or authority to issue SAML.

At provisioning, the trusted launcher also receives a random one-time
sign-in code. Hand it to the local tester through a private per-run file
(e.g. mode 0600 in a newly created private directory), not in a URL, process
argument, console log or SP registration. There is no default credential,
HTTP user registration, host SSO, company account or reusable login session.
Destroy the file and close the fixture at launcher shutdown. A restart
creates a new identity and address, one-use code and keys; local trust
must be rebuilt.
This package does not start a listener or change the ordinary simulator
runtime on import.

To try the integrated one-command sample (including the Cognito SP and
sample application), see [local SSO startup](local-sso.md).

## Wire contract

- All requests must arrive directly via HTTPS on the exact configured
  `127.0.0.1` origin. The ACS must be a different, explicit numeric loopback
  HTTPS origin and fixed path. The entity ID may also be a bounded opaque
  URN; it is never resolved. Host aliases, proxies and remote callbacks
  are excluded.
- The tenant-specific federation metadata path describes one signing key,
  persistent NameID, and one Redirect sign-in binding. The SP enrolls
  this explicit pin, not a public cloud issuer or remote metadata service.
- The tenant's `/saml2` path begins with a fresh RSA-SHA256 authenticated
  HTTP-Redirect AuthnRequest bound to the registered issuer, exact
  destination and ACS, and HTTP-POST response binding. RelayState is an
  opaque, bounded value and is echoed unchanged. The request is accepted
  once while live; it is not a proof of the user.
- The operator's one-time code must be supplied on the IdP's same-origin
  form along with the bound browser cookie and pending flow. The pending
  set is at most 12 entries, expires within two minutes, and supports up to
  six code attempts for the whole run. The fixture lives at most twelve
  minutes. Code consumption is atomic even if requests race. Browser forms
  send only the referrer origin, never the URL or query; the receiver can
  still require the exact non-opaque Origin on the form POST.
- On success, the browser posts a signed SAML Response to the registered
  ACS. The signed assertion carries exact audience, recipient and
  `InResponseTo`, a persistent NameID, and the generated `objectidentifier`
  and `tenantid` GUIDs. Every successful assertion to this one enrolled SP
  also includes the signed synthetic email attribute,
  `http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress`. The
  non-deliverable `.invalid` address is bound to the generated tenant and
  object IDs at provisioning; no browser field can choose it. There is no
  `email_verified`, credential, group, or role assertion.
  Assertion validity is capped at one minute and cannot exceed fixture
  lifetime. The SP must independently verify the pin, binding, time and
  replay; closing the fixture does not revoke already issued assertions.

## Using the synthetic email in a local Cognito client

Register the explicit SAML mapping
`{"email":"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"}`.
Enable and request `openid email` for that app client to get the mapped
address in its ID token. The access token does not contain email. The
`local-sso` sample still requests only `openid` and does not return an
email to its relying party. Email is not the stable subject, does not
assert control of a mailbox, and must not be used as a real-world identity
or granted access to any real resource. Do not configure an
`email_verified` mapping to this fixture: it has no such assertion.
See [Cognito SAML attribute mapping](https://docs.aws.amazon.com/cognito/latest/developerguide/cognito-user-pools-managing-saml-idp.html)
and [OpenID Connect standard claims](https://openid.net/specs/openid-connect-core-1_0.html#StandardClaims).

This deliberately narrow developer profile excludes unsigned or
IdP-initiated login, passive login, alternative bindings, bearer codes
issued over HTTP, assertion encryption, MFA, tenant/user management,
federation metadata discovery and public-cloud trust. A browser on an
independent SAML SP can test the actual protocol without a Microsoft tenant
or an installation of any third-party Entra emulator. Browser and application
witnesses do not establish Azure OIDC/OAuth or all Microsoft Entra parity.

References: [Microsoft SAML sign-in](https://learn.microsoft.com/en-us/entra/identity-platform/single-sign-on-saml-protocol),
[Microsoft SAML claims](https://learn.microsoft.com/en-us/entra/identity-platform/reference-saml-tokens),
[WHATWG Fetch: request Origin header](https://fetch.spec.whatwg.org/#append-a-request-origin-header),
[OASIS SAML 2.0 Core](https://docs.oasis-open.org/security/saml/v2.0/saml-core-2.0-os.pdf), and
[OASIS SAML 2.0 Bindings §3.4/§3.5](https://docs.oasis-open.org/security/saml/v2.0/saml-bindings-2.0-os.pdf).
The bounds above are implementation restrictions, not claims that Microsoft
applies the same bounds.
