# CFN/native IAM transaction correspondence

This finite requested-state specification is not a proof that Go implements
it. Native role creation and cleanup must reject injected policy/aggregate state
and verify final absence after deletion. Concrete fault regressions and exact
source applicability remain separate requirements; passing TLC does not replace
them.

## Boundary and finite domain

One target role/profile pair, one unchanged peer frame, generations 1 and 2,
zero or one requested Read policy and one injected Allow policy. Four selection
modes distinguish active in-template profile composition, explicit-trust
zero-inline native roles, unchanged legacy one-inline metadata roles, and
condition-filtered declarations. A separate `legacy` frame bit is never migrated
into native ownership. Each behavior executes one Create, Delete, or native
ClearPolicy transaction, with fifteen single faults or the normal case (sixteen
total configured alternatives). Native ClearPolicy represents deleting the
last inline policy and is restricted to the native Profile mode with an existing
Read policy; deleting an absent policy is not admitted. It is not an added CFN
UpdateStack capability.

No EC2, SSM agent, Battle, guest execution, AWS-managed policies, native
condition/tag support or new legacy behavior is claimed. No arbitrary policy
parser, intrinsic evaluation, trust evaluation, ARN matching, name case-folding,
SQL semantics, crash recovery, unbounded history, concurrent transactions or
multi-fault coverage is proved. `Read`/`InjectedAllow` are authority-set atoms,
not actual IAM documents. Exact native names and documents are represented by
identity/equality predicates, not parsed in TLA+. The peer is an abstract frame,
not a claim that arbitrary database triggers cannot alter unrelated tables.

## Abstraction relation and obligations

- `mode`, `Native`, `Supported`: `compileStackIAMRoles` and
  `compileStackNativeIAM` in `internal/aws/iam_cloudformation.go`; active resources
  are selected by `cloudformation.go`. The limited profile `Arn` reference
  linkage belongs to `cloudformation_conditions_link.go`. Filtered/legacy roles
  cannot be admitted into this native transaction. Existing legacy Hello World
  remains on its own runtime path, which this model does not simulate.
- `ActionAllowed` and `PassAllowed`: `stackResourcePermissions` in
  `s3_cloudformation.go` after canonical role admission. The booleans stand for
  authorization on canonical physical ARNs; CAPABILITY_IAM is not authority.
  PassRole is required on profile attachment/create, not removal/delete.
- `CanonicalOwner`, `RoleGeneration`, `ProfileGeneration`:
  `admitStackIAM`, `iamStackNameAvailable`, saved `stackNativeIAM` generation IDs,
  `checkStackIAM`, `iamProfileRole`, and generation guards in
  `iam_instance_profiles.go`. A conflicting retained claim, stale incarnation,
  case alias or legacy/native collision must not be adopted. Profile-generation
  admission applies only to Profile mode, never to a nonexistent profile. The model does not
  enumerate strings; these are explicit admission predicates.
- `NoExternalDependency`: reverse `deleteStackIAM` cleanup must refuse deletion
  of externally attached owned roles. External dependency discovery itself is
  an input abstraction, not a proved SQL query. `Write` abstracts reverse
  membership/profile/role cleanup as transaction-local changes.
- `Before`, `Wanted`, `work`, `data`: saved pre-state, request-derived post-state,
  uncommitted SQLite state, and committed visible state respectively. `Wanted`
  never depends on `fingerprint`. `Corrupt` represents trigger/ignored-write
  effects on inline policies, aggregate authority, membership, metadata,
  generations, owner or unexpected managed attachment. `Capture` intentionally records even a bad
  observed state, so the observed fingerprint alone cannot justify success.
- `RequestedPolicies`: `iamCreateRoleTx` must return an empty role with no
  inline/managed/membership/aggregate children; `iamSyncPolicy` must verify final
  aggregate absence when empty. `iamPutRolePolicyTx`, `iamDeleteRolePolicyTx`
  and `iamInlineMutationFrame` preserve the requested child set. CFN
  `createStackIAM` compares requested policy count/names/documents and role
  metadata before saving fingerprints. An unexpected managed attachment is represented as one Boolean plus injected
  authority. General native managed-policy mutation semantics are not modeled.
- `RequestedMembership`, `RequestedMetadata`, `CapturedIdentity`: native
  profile writes preserve exact profile and referenced role incarnations and
  metadata, with zero/one membership. Observed fingerprint equality is only
  useful after requested state has independently been validated.
- `Event`, `FinalPhysicalACK`: `cloudformation.go` checks final IAM physical state
  and stack/ownership absence after event/history writes, before commit.
  A resurrection after the earlier fingerprint must be rejected. The record
  Booleans abstract live/tombstoned state and absence of claims; they do not
  require physical deletion of retained native tombstones.
- `Commit` and `Rollback`: one shared transaction, final validated state visible
  only on success; refusal restores `Before`, pending work stays invisible,
  peer and legacy frame remain unchanged. Peer preservation is an explicit
  write-footprint assumption, not a fictional whole-database peer scan in Go.
  The peer mutation control checks this assumption is visible to the invariant.
  No SQL isolation/refinement theorem
  follows from these abstract transitions.

## Sensitivity and integration

The registry requires every invariant, exact six-file scope and all 17 controls.
Controls independently bypass native/action/PassRole/owner/generation/dependency/
trust admission; omit requested policy/member/metadata/identity/final-state
checks; corrupt the peer on commit; trust the observed fingerprint; publish a pending write; or commit a
refused partial mutation. Each must produce its named invariant counterexample,
not merely a parse error or some unrelated invariant. Existing models, controls,
scopes and thresholds remain required. The negative gate tests remove each
property/control, substitute no-op/stale edits, alter scope and supply incorrect
counterexample outcomes.

The source relation is intentionally narrow and manually reviewed. The table is
an obligation map, not automatically checked refinement. Bind final evidence to
the composed source HEAD only after independent runtime review and integration;
this model does not replace native/CFN fault regression tests or original problem
lifecycle verification.

## Verification boundary

Run the registered baseline and all 17 controls with the repository's pinned
TLC tool through `make formal`. Each control must produce its named invariant
counterexample. The registry and applicability checks preserve all existing
models, controls and exact source bindings.

A bounded model result is not a runtime refinement proof. Native/CFN fault
regressions, `make before-commit`, SDK/browser E2E and the complete original
problem lifecycle remain separate acceptance requirements.
