# Native builtin pin transaction correspondence

## Scope and integration contract

This bounded specification is not a Go refinement proof. It covers independent
target preservation after native detach and final CFN pin validation even for
retired roles. Concrete regression checks and exact source applicability remain
separate requirements.

The existing IAMManagedPolicies model remains unchanged for customer-managed
policy/version semantics; CFNNativeIAMTransaction remains unchanged for its
no-builtin subdomain. GuestCommandLifecycle and every prior registry entry,
invariant, sensitivity control and source requirement remain required. The new
model does not declare those existing models obsolete or widen their claims.

The registered scope includes `internal/aws/iam_builtin.go` and the native
attachment/CFN helpers listed below. Verify the complete registry and exact
source bindings for the integrated revision; individual model results are not
an exemption from the repository's other acceptance gates.

## Finite domain

One native role and one builtin attachment, one independent customer-policy
frame with the same display name, role/policy generations 1 and 2, only version
v2 in the valid builtin pin, and one transaction per behavior. The pin uses a
reserved builtin namespace; the same-name customer policy is not its storage
key. Seven operations are NativeRead, NativeAttach, NativeDetach, CFNCreate,
CFNDelete, MutateBuiltin and MutateCustomer. The latter two represent readonly
mutation refusal and customer-namespace independence, not a reimplementation of
customer version algorithms. Existing IAMManagedPolicies covers those bounded
algorithms separately.

The requested builtin attachment and inline explicit-deny flag are Boolean.
NativeAttach begins unattached; NativeDetach begins attached. CFN create/delete
include both requested-empty and requested-builtin cases. Duplicate native
attach/absent detach no-ops, startup seeding and pagination are outside this
model. Pin fields are grouped into existence, retained version set, default,
exact serialized document, exact metadata, namespace and generation. `Pin25`
and `Exact` denote exact source-pinned document bytes and metadata equality,
not permissive parsing or semantic document equivalence. Dates, description,
next version counter and synthetic policy ID are grouped in these equality
atoms rather than enumerated as strings.

There are 25 configured normal/single-fault alternatives. Faults cover prior pin
corruption; policy/version deletion; wrong/extra version; document, metadata,
namespace or generation corruption; injected builtin/customer attachment;
stale role or edge generation; changed inline state; missing/excess aggregate
permissions; ignored explicit deny; and post-event pin corruption or role/stack
resurrection. Only one configured fault applies per behavior, not arbitrary
compound faults or concurrent transactions.

## Actual permissions, not service implementation

`PinnedActions` enumerates the actual 25 action strings from the pinned
AmazonSSMManagedInstanceCore v2 document. All three source statements are Allow
with Resource `*`. The additional `ssm:SendCommand` atom is deliberately outside
the pin and is used only as an excess-authority fault. The probe action is
`ssm:GetParameter`; inline Deny overrides an attached builtin Allow. Stored
aggregate Allow/Deny sets and the probe decision are separately represented
from the expected request-derived sets. The decision field is an abstract
observable authorization result, not a new SQLite column.

This represents grants, not implementations. Of these pinned actions, only GetParameter/GetParameters
have implemented SSM service APIs in the inspected source. The other action
strings do not establish SSM agent, channels, session, EC2, credentials, VM,
network, SendCommand/StartSession or full Hello World Battle capability.
The isolated source-domain test compares the model's 25 names with both a fixed
expected set and the runtime's three Allow/Resource-* statements; this is not a
parser or authorization refinement proof.

## Requested versus observed state

`Before` is committed initial state; `Wanted` is derived exclusively from the
operation and immutable request. `work` is uncommitted state. `Corrupt` permits
immediate write faults, `Capture` records even corrupted state in `observed`,
and `Event` permits late CFN event/history faults. A successful response may
publish `work` only after independent final checks. Neither Wanted nor the
pinned expected document is reconstructed from observed state.

`FinalPin` requires the exact builtin pin and preservation of the original
validated target whenever the operation references it. It is independent of
whether any attachment remains or whether the role is still live. Thus native
detach and CFN retirement must not accidentally stop validating the now-orphaned
pin. `RequestedEdges` checks exact requested attachment/absence and generation;
`ExactAggregate` checks grants, inline Deny and the resulting decision;
`RoleFrame` preserves role generation/inline ownership; `FinalRecords` checks
live/retired role and stack/claim state after events. Pin attachment count is
not an immutable pin field, matching the runtime's deliberate exclusion of its
expected count change.

An initially corrupt builtin must be refused, not repaired. Independent customer
mutation is allowed to preserve an unrelated already-corrupt builtin unchanged;
it does not read, repair, overwrite or claim to validate that pin. A customer
policy mutation only changes the customer frame, never the builtin namespace.
`CustomerFrame` is a write-footprint obligation, not a fictional whole-database
customer scan. Its mutation control corrupts that frame at commit. Arbitrary
SQL triggers writing unrelated peer objects are not claimed to be detected.

## Concrete correspondence obligations

- `iam_builtin.go`: exact ARN/key separation, readonly mutation classification,
  fixed document and metadata. Startup seeding/idempotence is outside the model.
- `iam_managed.go`: `iamManagedRead` checks exact pin including only v2;
  mutation methods reject builtin modification. `iamManagedAttachmentTx`
  validates role/policy generations, requested attachment set and inline frame,
  aggregates in the same transaction and rereads the detached target independently
  of remaining attachments. Customer policies retain their existing semantics.
- `iam_managed_query.go`: exact builtin ARN routing and distinct namespace are
  admission inputs, not a proof of wire parsing or pagination.
- `iam_roles.go`: `iamSyncPolicy` materializes the actual aggregate, including
  removal of builtin grants after detach and preservation of explicit inline
  Deny. The existing native aggregate/frame tests remain necessary.
- `iam_cloudformation.go`: `compileStackNativeIAM` accepts an empty list or the
  one exact builtin ARN. Existing native-role selection is a precondition, not
  broadened here. `stackIAMRequestedManaged` compares request versus attachment
  state before capture and checks each requested pin again for retired roles;
  `stackIAMFingerprint` is not an authority to adopt injected state.
- `cloudformation.go`: existing shared transaction and final post-event/history
  IAM ACK before commit. Tombstoned native roles are represented as not live,
  not as an assumption that their retained rows are physically deleted.
- `s3_cloudformation.go`: `stackResourcePermissions` supplies canonical native
  AttachRolePolicy/DetachRolePolicy authorization; this model treats the already
  canonical action/role authorization decision as an explicit input.

SQL isolation, exact parser behavior, IAM ARN matching, role trust evaluation,
quota accounting, pagination, provider dispatch and crash recovery are assumptions
or outside the finite domain. There is no source-code refinement theorem.

## Sensitivity and verification recipe

Twenty-one controls must independently produce their specified invariant violations:
admission bypasses; skipped/edge-only/live-role-only pin checks; trusting the
observed pin; checking before late events only; forgetting native detached target
or retired CFN requested pin; requested edge/aggregate/role/final-record omissions;
trusting observed edges or both observed edges and aggregate; customer-frame mutation; refused partial commit;
pending visibility; and refusal of a valid normal detach. The normal-request
invariant checks successful terminal outcomes for valid normal requests; it is
not a liveness theorem or fairness claim.

The gate tests remove each model/property/control, change scope, replace
controls with no-op/stale edits, substitute unrelated counterexamples and compare
the action domain with the concrete pin. Run the complete policy regression
suite and the registered baseline plus all controls through `make formal` with
the repository-pinned TLC tool. A parse failure or unrelated counterexample is
not an acceptable substitute for the required invariant violation.

Native unit/race, SDK, browser and full original-problem acceptance remain
separate checks. No model result establishes a runtime implementation proof.
