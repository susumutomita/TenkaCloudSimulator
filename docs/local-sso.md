# Try local Cognito and Entra sign-in

One Simulator binary can run a disposable local sign-in sample. It starts
a first-party Entra-compatible SAML identity provider, a first-party Cognito
User Pools hosted flow and a sample app. No AWS account, Microsoft tenant,
third-party identity provider, Docker or external database is used. This
describes a source revision that includes the command; check your binary's
[version and source](releases.md) if it does not recognize `local-sso`.

## Start

Build once from the repository root if you do not already have a matching
binary. With a matching binary, the second command is the only Simulator
startup step:

```sh
make build && ./dist/tenkacloud-simulator local-sso
```

Wait for `Local SSO sample ready`. The launcher prints an exact
`https://127.0.0.1:<allocated-port>/` sample URL, the path to the per-run
public CA certificate, and the path to the private one-time login code.
The code itself is not printed. Keep the launcher running. Do not change
the URL to `localhost`, use an HTTP URL, forward the ports, or reuse another
run's files.

## Use a disposable browser trust store

Use only the CA file from this process, on the **same computer** as the
launcher. Do not add it to your daily browser, system trust store or company
keychain. Do not click through a certificate warning or disable TLS
verification. A fresh browser profile alone is not sufficient if the
browser still uses shared system trust.

On Linux, for a Chromium build that uses the local NSS database, use a
disposable home and user-data directory. This example expects the system to
already have `chromium` and the NSS `certutil` utility. Replace the two
placeholder values with the **public CA path** and **sample URL** from the
current launcher. Do not put the login code in a shell command.

```sh
ca_file='/exact/path/from/launcher/local-ca.pem'
sample_url='https://127.0.0.1:PORT/'
browser_home=$(mktemp -d) || exit 1
trap 'rm -rf -- "$browser_home"' EXIT
mkdir -p "$browser_home/.pki/nssdb" || exit 1
db="sql:$browser_home/.pki/nssdb"
certutil -N --empty-password -d "$db" &&
certutil -A -d "$db" -t 'C,,' -n 'Disposable Simulator local SSO' -i "$ca_file" &&
HOME="$browser_home" XDG_CONFIG_HOME="$browser_home/config" \
  XDG_CACHE_HOME="$browser_home/cache" chromium \
  --disable-background-networking --disable-component-update \
  --user-data-dir="$browser_home/user-data" "$sample_url"
```

Run this in its own shell so its exit trap only removes this disposable
browser home. Close the browser and exit that shell when finished. This
trust setup and the complete sign-in were exercised with Chromium on Linux
amd64. macOS, Windows and other browser trust integrations are not qualified
by that test. If the chosen browser cannot keep trust isolated, use the
isolated Linux environment instead; do not install the CA globally.

## Sign in and stop

1. Open the printed URL in the disposable browser. Select **Sign in with
   local Entra**.
2. Read the private code file on this computer and paste its value into
   **One-time synthetic login code**. Select **Sign in synthetic identity**.
   Use no company credentials or personal data. Keep the code out of Git,
   screenshots, chat and logs.
3. **Local sign-in verified** means the sample app verified the full flow.
   It checked the callback and the ID token from local Cognito. It does not
   mean that an AWS, Microsoft, Simulator operator or durable app session
   was created. If a completion page remains, its **Continue to the
   application** link stays in the same enrolled sample flow.
4. Close the browser profile and stop the launcher with **Ctrl+C**.
   Normal shutdown and controlled startup failure close listeners and remove
   temporary SQLite state and per-run files. If cleanup fails, the launcher
   reports the failure without printing credentials or private paths. A forced
   termination or host crash may leave
   files in the private temporary directory; after establishing that this
   process has stopped, remove only that run's directory. The launcher
   never installs browser trust or cleans up a browser profile for you.

The launcher exits after ten minutes. Submit the code within two minutes
of the signed request to Entra. The sample app keeps the attempt for two
minutes and 30 seconds from selecting the sign-in link, to allow time for
the usual local redirects and token verification. Suspending the browser
or pausing between steps can still exceed the app's deadline; an expired
callback cannot sign you in.

Each run accepts its synthetic code once. If a sign-in expires before you
submit the code, return to the original sample URL. If you already submitted
the code and the result could not be verified, stop and restart `local-sso`
for fresh trust, state and a new code. Open a fresh disposable profile or
remove its prior CA before using a new run.

## Scope

- The command binds numeric loopback HTTPS. The signed, authenticated SDK
  bootstrap listener shuts down before it announces browser readiness.
  Cognito's browser endpoint, the Entra SAML endpoint and the sample app
  are three separate origins. Their credentials do not authorize the
  ordinary Console, Scalar, provider SDK or any production service.
- The sample is the authorization-code flow with PKCE S256 and a fixed
  callback, one local Entra registration, one synthetic user and
  `openid`. It checks a real signed SP-initiated SAML exchange, Cognito
  code redemption, and the sample app's state, nonce and token validation.
  It is not a drop-in AWS Cognito Hosted UI, a Keycloak interoperability
  check, or integration with an existing application.
- [The local Entra SAML component](azure-entra-local-saml.md) is independent
  of Cognito and can host the same narrow SAML contract for another
  explicitly enrolled local SP, including an Azure-oriented fixture. This
  is a component contract, not an Azure Console/ARM/Microsoft Graph or
  Entra OAuth/OIDC implementation. The separate
  [Entra OIDC verification fixture](azure-entra-verification-fixture.md)
  is still verification-only.
- Ordinary [Simulator startup](start.md), persistent practice state,
  operator access, and standalone Cognito configuration are unchanged by
  running this sample. It is not a Battle guest/runtime qualification.

## Repeat the Linux browser check

The [opt-in Chromium witness](../test/e2e/local_sso_strict_browser.cjs)
starts the actual binary in a disposable directory. It supplies its own
isolated NSS store, restricts the browser to local HTTPS, never modifies
host trust, and checks certificate refusal before importing the per-run CA.
It checks ordinary CLI authentication, a controlled failed start, the signed
browser path to the verified app callback, and process/file/port cleanup.
The test is separate from the normal user startup and does not install
dependencies. Run it on Linux with Node.js, `certutil`, and an available
Chromium and Playwright Core:

```sh
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" \
PLAYWRIGHT_MODULE='/absolute/path/to/playwright-core' \
CHROMIUM_BINARY='/absolute/path/to/chromium' \
  node test/e2e/local_sso_strict_browser.cjs
```

## Bounded contract

`LocalSSOSyntheticLifecycle` checks the declared transitions for one run
and two symbolic browser workflows, with up to twelve modeled steps.
It abstracts signed request trust, exact browser binding, origin, stable
subject and registration, OAuth code consumption, final app verification,
and normal shutdown as finite predicates. The concrete TLS trust store,
XML/JWS validation, PID/port/file cleanup, SQLite concurrency and runtime
refinement are independent obligations, not consequences of the TLC result.
