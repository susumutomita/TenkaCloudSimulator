# Lambda invocation log-tail disclosure model

`LambdaLogTailDisclosure` adds a finite response-admission and view-lifetime
model. Existing models and controls are preserved. Registration and detector
success do not establish a completed TLC run or implementation correctness.

## Correspondence and limits

`Respond` abstracts `internal/aws/lambda.go`: a response includes a log tail only
for an explicit synchronous Tail request authorized by `lambda:InvokeFunction`.
None/omitted requests and denied invocation do not expose it; DryRun+Tail and
Event remain unsupported. `capturedSize` uses three classes: empty, within the
4096-byte limit, and overflow. `TailCap` abstracts the bounded tail supplied by
`internal/workload/python/runtime.go`. It does not verify actual bytes, suffix
selection, SDK/result-frame filtering, base64, UTF-8, or the legacy Logs prefix.

`Display` abstracts the alive/current invocation-revision check in `lambda.jsx`.
`ChangeEpoch` represents a new request, event/checkbox edit, tab change, or
configuration-response application that clears the prior invocation.
`ChangeContext` abstracts target selection: it clears the result and invalidates
the request epoch. There is no invented separate context-equality guard in the
implementation; context freshness follows invalidation and the revision check.
`Unmount` represents removing the view, including the existing parent
region/principal/navigation remount. Visibility becomes false because the view
is removed, not because a React setState call after unmount is required.
The parent main-view remount contract is assumed, not newly verified here.
`lambda-invoke.mjs` requests/decodes logs only when the user opts in.

The shared `internal/server/web/app.js` scope is limited to the generated Lambda
invocation and log-reveal view corresponding to these source guards. It does not
cover other bundled services; their existing scopes remain unchanged.
`internal/server/server.go` is included only for the Tail capability metadata
that describes this response contract. That metadata is not a native
authorization proof. These explicit paths do not grant automatic coverage to
unrelated behavior elsewhere in either shared file. Final checks must verify
the generated bundle matches the reviewed source.

The finite universe has one request, two contexts, epochs 0 and 1, three
invocation kinds, Boolean opt-in/permission/alive, and three size classes. A
context may change only while epoch is 0. Async arrival may occur before or
after invalidation or unmount. Repeated requests, arbitrary context sequences,
network cancellation completion, liveness, React scheduling, host authentication,
IAM evaluation, WASI containment, SQL, persistence and cloud delivery are outside
scope. This is not a Go/JS refinement proof or AWS compatibility proof. Native,
real-runtime, helper and browser tests remain independent obligations.

## Properties and sensitivity controls

The seven properties check typing, opt-in, synchronous-only response, Invoke
permission, the abstract size bound, current mounted display, and presence of an
exposed response before display. Eleven controls remove opt-in, sync or Invoke
permission; remove the cap; accept an old epoch or unmounted reply; skip context
invalidation; retain display on epoch/context/unmount changes; or display without
an exposed response. Each control must violate its named semantic invariant,
not merely fail parsing, typing or execution. The registry, required gate policy
and detector tests protect model presence, every property/control, exact source
scope and mutation-anchor matching. Exact integrated TLC receipts remain a
separate gate, including checks against the final UI source.
