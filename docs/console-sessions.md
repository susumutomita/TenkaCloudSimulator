# Local browser sessions and sign out

The account menu provides **Sign out** for operator and participant browser sessions. Exercise identity selection remains an operator-only menu action; it is not an AWS account or STS session. The console is an independent local Simulator.

`POST /_simulator/login` still accepts the host-issued operator or participant credential. It now mints a separate random 256-bit browser credential in an HttpOnly, SameSite=Strict cookie (Secure on HTTPS). Only hashes are retained in memory. Ordinary browser lifetime is 15 minutes, capped at the participant parent's remaining lifetime. Every browser request also checks that the participant parent still exists, is unexpired and retains its identity. Re-sign-in rotates that browser's previous session. Restart loses browser sessions; the persisted operator token remains usable for a fresh sign-in. Pre-upgrade cookies containing SDK credentials require a fresh sign-in and are never silently adopted as browser sessions.

`POST /_simulator/logout` revokes only the supplied browser session and expires its cookie. It is idempotent: a missing, expired or already revoked browser cookie still produces 204. Old-cookie replay cannot authenticate afterward. It does not revoke the host's operator credential, participant SDK credentials or another browser's session. Requests already authorized before logout can finish; logout does not cancel running work. SDK header authentication keeps its existing semantics. A raw SDK credential placed in a browser cookie, or a browser credential placed in the SDK header, is rejected.

The login/logout routes retain the host/origin and cross-site guards. Origin must match the actual HTTP/HTTPS scheme and full host/port; opaque, malformed or cross-origin requests are rejected. Browser sessions are capped at 1000 active entries per Access instance, independently of the existing participant-credential quota. Expired entries are pruned. Team hosts still allocate one process, SQLite database and Access authority per team; browser identity selection does not provide tenant isolation.

A trusted TLS-terminating proxy must validate the browser origin at its public boundary and deliberately strip or rewrite it for its fixed loopback HTTP upstream; otherwise the public HTTPS Origin will be rejected against upstream HTTP. Access uses actual `r.TLS`, never untrusted `X-Forwarded-Proto`. `--allow-remote` alone does not configure TLS termination. A hosting gateway that injects an SDK credential on each request owns a separate browser authority: it must intercept/compose logout to revoke its own browser grant. Merely forwarding this logout while continuing credential injection would authenticate the next request again. This simulator endpoint does not revoke a host gateway grant.

The Access wrapper adds `X-Simulator-Authentication: required` on login and capabilities responses. The console reads this nonsecret mode metadata. With explicit `--unsafe-no-auth` (or an embedding without the Access wrapper), the menu displays **Sign out (authentication disabled)** as unavailable; it does not claim server revocation. A network failure or non-204 response shows an error and allows retry. Only confirmed server success navigates to login.

## Additive OpenAPI proposal for the integration owner

Merge this one path into the API reference alongside Issue305; do not replace its independent edits. Cookie names are per authority and credentials must never be included in examples.

```json
{
  "/_simulator/logout": {
    "post": {
      "tags": ["Local access"],
      "operationId": "signOutBrowserSession",
      "summary": "Sign out the current local browser session",
      "description": "Revoke only the browser session supplied by its same-origin HttpOnly cookie and expire that cookie. Idempotent without a valid browser session. Operator and participant SDK tokens and other browser sessions remain valid. Browser sessions last up to 15 minutes and never outlive their participant parent. Requires the Access wrapper; unsafe-no-auth does not provide browser-session revocation. Same-origin scheme/host/port and cross-site checks apply. No real AWS session is created or revoked.",
      "responses": {
        "204": {"description": "Browser session revoked or already absent; cookie expired"},
        "403": {"description": "Unrecognized host, cross-origin or cross-site request"},
        "405": {"description": "Use POST"}
      }
    }
  }
}
```

## Verification boundaries

`internal/server/access_browser_test.go` checks credential separation, replay, participant binding/expiry/revocation, instance isolation, quota/rotation/cleanup, concurrent logout and strict origin/cookie attributes. `test/e2e/console_logout.py` exercises the real account menu, failed sign-out and retry, protected401/re-login, two teams, independent sessions, operator/participant boundaries, PC/390px, keyboard, axe and no external requests. It uses dummy local data and does not print credentials.

The finite `formal/BrowserSessions.tla` model abstracts opaque/hash credentials into disjoint token sets, two clients and a bounded clock. Its invariants and mutants check selected-session revocation, SDK/unrelated-session preservation, parent validity and raw-cookie rejection. It does not prove randomness, cryptography, HTTP parsing, Go locking, UI behavior or the implementation; native/browser tests cover those separate seams. Integration runs the repository's required full-module and exact-HEAD CI/review gates before merge; the focused checks below do not replace those gates.


Focused verification on 2026-11-04: `go test -race ./internal/server -run 'TestAccess|TestBrowser' -count=1` passed (1.074s); the official Cloudscape bundle and standalone binary built. The finite baseline checked 7,348 distinct states, and all five mutations failed their exact intended invariants (logout retains selected session, ignores parent validity, accepts raw operator cookie, clears unrelated sessions, or revokes SDK authority). The actual standalone browser helper passed operator and participant sign-out, protected/replayed-cookie401, re-login, unrelated browser/SDK preservation, two-team separation, failed-network/server sign-out and retry, and truthful unsafe-mode controls. Keyboard navigation uses the pinned official component's ArrowDown and verifies focus on Sign out before Enter; End is not claimed as supported. Four operator/participant × 1440px/390px account-menu states passed axe and reflow. External requests and browser errors were zero; local dummy buckets were deleted and all test processes closed. Screenshots contain only dummy data and masked login input. Full repository regression/coverage and registered model gates remain integration responsibilities.


The account-menu helper is a mandatory `Conformance` subprocess in `make e2e`. Prepare the pinned development-only tool first and pass the same canonical variable as `audit.py`:

```sh
/tmp/tcsim-tests/bin/python scripts/get-audit-tools.py /tmp/simulator-audit
AXE_SCRIPT=/tmp/simulator-audit/axe.min.js make e2e PYTHON=/tmp/tcsim-tests/bin/python
```

Missing `AXE_SCRIPT` or an unreadable tool fails the helper; there is no optional accessibility skip. CI obtains the existing pinned axe tool before E2E and continues to run the separate `make audit` checks afterward.

## Registered finite-model integration

The current registry includes BrowserSessions, SSMParameterState and the original
five protected models. BrowserSessions retains its eight CFG invariants and five
specified sensitivity mutants. The checker protects the combined required IDs
and invariants: removing registered models and their files, or reducing required
properties in both CFG and registry, must fail. Runtime behavior and generated
console assets are unchanged by this source integration.

Correspondence is intentionally limited: login/session digest state, parent
validity at use and selected revocation map to access.go; main.jsx and the named
native/browser helpers are caller/test seams. TLC does not execute them or prove
Origin parsing, cookie flags, HTTP error handling, Go concurrency, token entropy,
PC/mobile layout or cross-team isolation. Those are independent existing tests.
Clock/TTL/token counts are exploration bounds, not the actual15-minute TTL limit.

The combined eight baselines and eighteen specified sensitivity controls require
fresh immutable-HEAD TLC execution. Source-only integration and Python policy/
applicability checks are separate from native Go, actual browser, hosted CI and
review evidence; no fresh runtime execution is implied by restacking.

## Participant STS console handoff (version 1)

A host can open the participant's existing local STS session in the actual
Cloudscape Parameter Store console without exposing its operator token. Sign
`POST /_simulator/v1/console-handoffs` using the participant's local SigV4 STS
credentials, service `sts`, and `Content-Type: application/json`. The exact body
is `{"ParameterName":"/practice/own","Region":"us-east-1"}`. Both fields are
required; unknown fields fail. The response is `{"Code":"…","ExpiresAt":"…"}`.
The code is a 256-bit random, single-use reference, valid for at most 60 seconds
and never beyond the original STS expiry. Issuance is capped at 1000 outstanding
codes per process. No operator, legacy participant token, or unsigned request
can mint this handoff. Signed non-root routes other than this exact endpoint
remain unsupported.

Navigate to `/_simulator/login#handoff=CODE`. The login script erases the fragment
before rendering or making requests, then redeems it through a same-origin form
POST. The server consumes the code atomically and returns its bound destination;
no arbitrary redirect target is accepted. Failed redemption requires a fresh
code. A top-level cross-site navigation may load only public login HTML; it does
not redeem the fragment or authorize any API. Cross-origin POSTs, subframes and
other cross-site requests remain rejected. Hosts should use an isolated new tab
with `noopener,noreferrer` and must not put codes in query strings, logs or
persistent storage.

The resulting HttpOnly, SameSite=Strict cookie lasts at most 15 minutes and
never outlives the original STS session. Every authenticated request revalidates
that session's existence and expiry, role generation and current effective
policy snapshot. Role deletion/recreation, permission changes, process restart,
expiry and browser logout retain their original effects. A browser does not
receive native STS issuing authority or become a legacy access-token session.
Concurrent work already authorized may finish after revocation, as for the
existing browser path.

This first handoff surface permits only `GetParameter` and `PutParameter` on
the exact bound region/account/parameter ARN, intersected with the current role
policy. It does not grant writes to a read-only role. Enumeration, deletion,
other parameters/regions, all other cloud providers and identity/control APIs
are refused server-side. Hiding UI controls is not the security boundary. A
host must still authorize the user and choose the correct parameter; this
restriction does not change the SDK credentials' own permissions or replace the
one-process/database/token-authority-per-team hosting contract.

Capabilities advertise `consoleHandoff: {version: 1, services:
["ssm-parameter"]}`. Bound browser discovery also includes its own
`consoleScope` (parameterName and region), allowing a one-parameter list and
detail/refresh through GetParameter without DescribeParameters. Older binaries
must show an explicit upgrade requirement in the host rather than falling back
to operator login. Embeddings without Access do not provide handoff authority.

Verification: the new Go tests exercise actual SSM authorization, exact-resource
and region limits, fresh policy changes, role generation/expiry, replay, quota,
operator rejection, route refusal and cross-site navigation boundaries.
`test/e2e/console_handoff.py`, registered in `make e2e`, exercises actual native
STS issuance, cross-site top-level login, fragment removal, list/detail/reload,
read-only refusal and authorized writes, replay and role deletion, plus desktop
and mobile accessibility. Host answer submission is separate integration
acceptance. The existing BrowserSessions finite model describes the legacy
session subset, not the new STS binding; no new formal refinement claim is made.

Local candidate validation, 2026-11-08: `make before-commit` passed with all
packages under the race detector, 95.60% whole-module statement coverage, vet,
asset/license checks and standalone build. Focused handoff tests also passed.
`make e2e` and the new GUI helper stopped before page launch because this
executor rejects Chromium's process-singleton socket, including with approved
escalated execution. GUI, accessibility and host answer submission are therefore
not claimed as locally verified; the registered tests require hosted execution.
