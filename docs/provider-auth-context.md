# Trusted provider actor context

This additive local composition seam separates **credential identity**, **effective
exercise identity**, and **credential role**. It does not implement Azure/GCP
services, cloud sign-in or new provider IAM. This seam introduces no Kumo runtime dependency
or copied Kumo code; the design follows the reviewed modular/provider-local
composition plan rather than its global service singletons.

`internal/provider/actor.go` defines a value-only `Actor` with
`CredentialIdentity`, `ExerciseIdentity`, and typed `CredentialRole`:
`CredentialOperator` or `CredentialParticipant`. `WithActor(ctx, actor)` is a
trusted Go composition operation with a private typed context key.
`ActorFromContext(ctx) (Actor, bool)` returns a copy and rejects absent/invalid
actors. No default administrator, request-header lookup, token, token hash,
provider policy, or environment claim exists in this DTO. Identity strings must
be nonempty valid UTF-8 of at most 1024 bytes, without surrounding whitespace or
control characters. Unknown roles and participant identity substitution are
invalid. Invalid mappings shadow earlier context values rather than retaining
an earlier actor's authority.

`server.NewAccess` attaches this actor only after existing credential validation,
role-header sanitation and participant identity-selection guards. An operator may
choose an exercise identity: credential identity/role remain operator, while
`ExerciseIdentity` is the selected value (or the authenticated identity when no
selection was supplied). A participant's two identities remain identical.
The capabilities-path selection exception remains in place, with its existing
principal-header replacement. Rejected credentials/selections never reach the
protected provider handler. Public login/bootstrap assets do not acquire actors.

The current operator credential happens to bind `simulator-admin`; this literal
belongs to Access's existing simulator contract, not to the generic DTO. Native
AWS keeps its existing wire headers and policy evaluation unchanged. This seam
does not reinterpret an operator credential as permission to bypass the selected
exercise identity's data policies. It does not change existing unsafe-no-auth
AWS behavior. `server.New` remains trusted unauthenticated embedding: hosts must
wrap it in `NewAccess` for local authentication. New provider adapters must deny
requests with no valid actor, including unsafe/unwrapped composition.

## Azure/GCP adapter contract

The trusted root adapter maps central `ExerciseIdentity` to the leaf's local
subject field and supplies a **fixed configured trusted environment**. It must
not take environment, subject, operator role or account-owner authority from HTTP
headers/query/body. The leaf authorizer still validates resource/action/account
ownership and its provider-specific policy. `CredentialRole` can distinguish
explicit host-only control operations; it is not automatic provider data-plane
permission. Providers must never silently map a missing actor to an administrator.
A caller label does not provide team isolation: independent process/SQLite/token
authorities per team remain the host requirement. Leaf data authorizers own their
transaction and policy snapshot rules; this DTO opens no DB transaction.

The prototype was preserved from base `cb529272ce14be9cab645e284b1733da37b21cda`
and normally merged onto main `b61d0299a2139661f01ef29ae5826e4c070cc7bf`.
Current browser login mints a separate opaque browser credential, stored only as a
hash with parent binding. SDK credentials are rejected as raw cookies and browser
credentials are rejected in the SDK header. Actor attachment follows either
successful existing authentication channel, then the same selection guards.
The new tests also replay a revoked cookie, preserve the other browser's Actor
and SDK Actor, and reject both cross-channel credential replays. This adds no new
cookie/session/token mechanism and does not replace BrowserSessions obligations.

## Verification and limits

New tests exercise private-key isolation, absent/invalid mappings, copy semantics,
unknown roles, participant substitution, actual HTTP authentication and sanitized
headers, operator exercise selection, cookie/SDK precedence, cross-instance
rejection, public bootstrap without actor, and native SQLite-backed S3/IAM read,
denied-write preservation and cleanup. They are tests of this composition seam,
not evidence of unimplemented GCP/Azure SDK compatibility or full cloud IAM.

The finite `ProviderActor` model is registered with required invariants and six
required negative controls alongside every existing model. It models authentication outcome,
credential role/selection, guarded attachment and captured per-emission identity.
It does not model cryptography, credential issuance, cookies/TTL/revocation,
HTTP parsing, Go context internals, Unicode validation, provider policy, SQLite,
OS isolation or a refinement proof. Exact applicability and actual TLC/mutation/Go/E2E receipts must be checked on
the final candidate; a source registration alone is not execution evidence.


The storage integration maps this validated exercise identity to fixed synthetic
project/account owners. `--cloud-storage-owner` is trusted host configuration;
it grants no AWS identity-policy permissions. See [storage composition](multicloud-storage.md).
