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
