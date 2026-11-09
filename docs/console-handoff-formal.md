# Participant console handoff: finite correspondence

`formal/ConsoleHandoff.tla` is a separate bounded model, not an extension of the
claims made for `BrowserSessions` or `STSSessionLifecycle`. The registry retains
all 58 existing models and adds this 59th model. No implementation refinement or
GUI/SDK execution follows from TLC success.

## Correspondence

- `Issue` represents `issueConsoleHandoff` and `ConsoleBinding`: a participant
  STS reference, never the operator credential, produces one opaque code. Its
  deadline is capped by the parent expiry.
- `Redeem` represents `redeemConsoleHandoff`: lookup and consumption occur
  together, including failed attempts; expiry and the current parent generation
  are checked before a separate browser credential is created. A second attempt
  exercises replay. The browser deadline is capped by the parent expiry.
- `Use` represents `consoleBrowserRequest`, the `ConsoleBinding` callback and
  `sdkDecision`, plus the parsed SSM destination check in `ssm.go`. Each operation
  observes parent presence, generation and expiry,
  the current policy verdict, exact parameter and region, and Get/Put rather
  than Delete. Metadata/assets are outside the modeled data operation.
- `Logout` represents `revokeBrowser` in `access.go`. It removes only the current
  browser, leaving an unrelated live session and SDK authority unchanged.
- `Environment` advances time, removes the parent, changes the role generation,
  or changes the policy. Policy restoration is also explored.

`stsSessionPolicy` in `sts_roles.go` obtains role generation and current policy
from one SQL statement in each native/CloudFormation success branch. Although
`ConsoleBinding` first invokes a separate validation callback, the subsequent
snapshot rechecks generation; the separate calls do not by themselves establish
a stale-generation vulnerability. Coherence of actual SQL and enforcement by
handlers remain implementation obligations, not consequences of this model.

The registry binds these runtime files and the focused handoff/STS credential
regressions. It also binds `server.go` as the capability-discovery seam: the
additive `consoleScope`/`consoleHandoff` metadata is not a modeled authorization
decision. No React/login/navigation behavior is modeled. Exact parameter identity
means
the stored name, including its leading slash: `/own/value` and `own/value` are
different simulator keys despite sharing a normalized IAM ARN. The parsed-name
and region boundary must reject this alias; ARN policy matching alone cannot
implement the model's `ExactDestination` property. Registered source hashes
establish the reviewed correspondence inputs, not that TLC executes or verifies
those Go files.

## Bounds and independent checks

There is one code, two redemption attempts, one bound browser and one unrelated
live browser, two generations, two parameters/regions, three actions, Boolean
policy authority and times 0 through 3. Parent expiry is 2 and handoff TTL is 1.
Browser TTL is chosen from 1 or 3, so both an earlier browser expiry and an
expiry clipped by its parent are exercised. These are scaled orderings, not
production TTL constants or exhaustive timing schedules.

Historical request witnesses copy the prestate and requested destination. The
acceptance invariants read those values directly without reusing admission
helpers. Structural invariants independently inspect code consumption, successful
redemption count, stored deadlines, logout state, unrelated sessions and SDK
credential sets. Twenty required mutants remove or widen each admission class,
including separate generation checks, introduce replay or retained logout
cookies, extend deadlines, revoke a peer/SDK credential, or grant browser SDK
authority. The gate requires each named counterexample, not merely a failing run.

The model assumes unique unguessable credentials and request-bound fresh
observation of authority. It does not model hashing/randomness, quotas, parsing,
header ambiguity, CSRF/origin/cookie flags, URL canonicalization, network failures,
SQL statement implementation, arbitrary session populations, concurrent in-flight
logout, or liveness. The peer is live, so this frame does not prohibit pruning
unrelated expired sessions. A fresh handoff after logout is not modeled: logout
revokes the browser, not the original STS credential. Native tests and real GUI
acceptance must independently establish the corresponding runtime behavior.
