# Local browser sessions and sign out

The account menu provides **Sign out** for operator and participant browser sessions. Exercise identity selection remains an operator-only menu action; it is not an AWS account or STS session. The console is an independent local Simulator.

`POST /_simulator/login` still accepts the host-issued operator or participant credential. It now mints a separate random 256-bit browser credential in an HttpOnly, SameSite=Strict cookie (Secure on HTTPS). Only hashes are retained in memory. Operator browser sessions start with a 15-minute idle deadline and an immutable 60-minute absolute deadline from sign-in. Explicit cookie renewal can extend only the idle deadline. Participant browsers retain the original fixed 15-minute parent cap and cannot renew. Ordinary browser requests never renew either session. Every browser request also checks that the participant parent still exists, is unexpired and retains its identity. Re-sign-in rotates that browser's previous session. Restart loses browser sessions; the persisted operator token remains usable for a fresh sign-in. Pre-upgrade cookies containing SDK credentials require a fresh sign-in and are never silently adopted as browser sessions.

`POST /_simulator/logout` revokes only the supplied browser session and expires its cookie. It is idempotent: a missing, expired or already revoked browser cookie still produces 204. Old-cookie replay cannot authenticate afterward. It does not revoke the host's operator credential, participant SDK credentials or another browser's session. Requests already authorized before logout can finish; logout does not cancel running work. SDK header authentication keeps its existing semantics. A raw SDK credential placed in a browser cookie, or a browser credential placed in the SDK header, is rejected.

The login/logout routes retain the host/origin and cross-site guards. Origin must match the actual HTTP/HTTPS scheme and full host/port; opaque, malformed or cross-origin requests are rejected. Browser sessions are capped at 1000 active entries per Access instance, independently of the existing participant-credential quota. Expired entries are pruned. Team hosts still allocate one process, SQLite database and Access authority per team; browser identity selection does not provide tenant isolation.

A trusted TLS-terminating proxy must validate the browser origin at its public boundary and deliberately strip or rewrite it for its fixed loopback HTTP upstream; otherwise the public HTTPS Origin will be rejected against upstream HTTP. Access uses actual `r.TLS`, never untrusted `X-Forwarded-Proto`. `--allow-remote` alone does not configure TLS termination. A hosting gateway that injects an SDK credential on each request owns a separate browser authority: it must intercept/compose logout to revoke its own browser grant. Merely forwarding this logout while continuing credential injection would authenticate the next request again. This simulator endpoint does not revoke a host gateway grant.

The Access wrapper adds `X-Simulator-Authentication: required` on login and capabilities responses. The console reads this nonsecret mode metadata. With explicit `--unsafe-no-auth` (or an embedding without the Access wrapper), the menu displays **Sign out (authentication disabled)** as unavailable; it does not claim server revocation. A network failure or non-204 response shows an error and allows retry. Only confirmed server success navigates to login.

## Bounded operator renewal (new source, integration pending)

The default operator hard lifetime changes from fixed 15 minutes to at most 60 minutes (fourfold). The 15-minute idle deadline advances only on successful `POST /_simulator/session/renew`; no ordinary API request, polling loop or timer advances it. This increases the time a stolen live operator browser session could remain useful, bounded by the original 60-minute deadline. The values are project policy, not OWASP-mandated numbers. [OWASP session expiration guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html#automatic-session-expiration) explains why idle timeout alone is insufficient and absolute expiration must be enforced by the server.

Renewal requires the existing HttpOnly browser cookie, an exact same-origin Origin, `X-Simulator-Session-Action: renew`, and same-origin Fetch Metadata if provided. SDK-header authentication cannot renew. It preserves cookie flags and the same cookie value; this is deadline renewal, **not session-ID rotation**. Admission, live authority checks and capped deadline mutation are atomic under the server mutex. Response JSON contains only `expiresAt` and `hardExpiresAt`, never credentials. Missing/expired/revoked/restarted sessions receive401; a live participant receives403 `participant_fixed_lifetime`; invalid renewal authority/CSRF inputs receive403; wrong method receives405. Parent invalidation is checked before participant refusal. A response racing logout can carry a stale Set-Cookie, but its deleted server hash must remain unusable.

The shared first-party activity module records only trusted pointer/key/input events while visible and focused. A ten-second eligibility window is consumed once at an actual API-request boundary. The common console request path may consume a recent genuine gesture even when the next request is a refresh; passive polling without a new eligible gesture never renews. Documentation Send retains its explicit-action entry point. Concurrent explicit actions can await the same renewal. There is no background heartbeat, storage, API callback or automatic replay. Login, logout, renewal and credential-control requests must be excluded from the common hook. `isTrusted` is a UX classifier, not a security boundary or proof of human presence; server idle/absolute/parent checks remain authoritative. An error is presented in English/Japanese without echoing server response bodies or credentials.

**Current limits:** the console and docs hooks are integrated in source, but generated assets and product runtime validation remain unfinished. Reduced prompt frequency is not yet established by end-to-end execution. A protected native401 or renewal401 now latches a one-way in-memory recovery lock in source instead of automatically navigating away. Later requests and supported pending response/body reads are rejected. The mounted page remains available to inspect remaining input, with English/Japanese guidance for separate-tab sign-in or explicit discard/fresh start. Signing in elsewhere never unlocks or rebinds this page, and no request is replayed. This is not automatic draft restoration: already acknowledged actions may clear child state, already delivered decoded values cannot be retracted, and open-modal notice/focus plus child-form behavior remain unverified in browsers. Automatic safe resumption still requires trustworthy process/state/parent-context identifiers absent from the current contract. The previous implementation used a fixed 15-minute browser lifetime. This new policy does not establish why a particular session ended; expiry, process restart, revocation and a different simulator address are distinct conditions.

Source tests add fake-clock renewal boundary/concurrency/revocation cases, but Go/race, server, real browser, clipboard/file, locale/reflow, generated assets and full CI have **not run for these changes**. Ten pure Node controls for the shared classifier passed locally; their mocked events do not prove real browser behavior. The separate unregistered finite renewal model is source-only and has not been parsed or checked. Existing dated checks below concern their earlier immutable source and do not establish renewal correctness.

## Additive OpenAPI proposal for the integration owner

Merge this one path into the API reference alongside Issue305; do not replace its independent edits. Cookie names are per authority and credentials must never be included in examples.

```json
{
  "/_simulator/logout": {
    "post": {
      "tags": ["Local access"],
      "operationId": "signOutBrowserSession",
      "summary": "Sign out the current local browser session",
      "description": "Revoke only the browser session supplied by its same-origin HttpOnly cookie and expire that cookie. Idempotent without a valid browser session. Operator and participant SDK tokens and other browser sessions remain valid. Operator browser sessions have a 15-minute idle deadline and a 60-minute absolute cap; participant browsers never outlive their original parent deadline. Requires the Access wrapper; unsafe-no-auth does not provide browser-session revocation. Same-origin scheme/host/port and cross-site checks apply. No real AWS session is created or revoked.",
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
