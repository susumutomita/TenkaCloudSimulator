# Bounded CloudFormation SSM parameter snapshot

`CFNParameterSnapshot` models admission, capture and persistence of a selected
local SSM parameter set and one subsequent client-token replay. It is a finite
state-machine check, not a proof of the Go implementation, SQL isolation, IAM
matching or AWS compatibility.

## Scope and finite bounds

Two selected local keys have independently chosen cells: either of two String
values, missing, or wrong type. The two accepted template carriers,
`AWS::SSM::Parameter::Value<String>` and
`AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>`, share the same abstract valid
admission class. Other classes witness a rejected carrier, failed key constraint,
and failed downstream compilation. Four permission subsets include the important
case where the first key is allowed but the second is denied. Replay is matching,
mismatched, or denied by existing-stack authorization.

One arbitrary replacement of the whole source map may occur after capture,
either before publication or after creation. Replacement can update or delete
either selected source. This explicitly permits source data to become stale
before the creation commit. No latest-at-commit requirement is imposed. Capture
is a single abstract transition; implementation transaction isolation remains a
separate obligation. The state space contains one stack, one resource and one
abstract event-publication marker, not the actual number of lifecycle events.

Keys and resolved values use distinct symbols. The key constraint witness is
checked before reading, and valid resolved String values must not be rejected
merely because they are not keys. Concrete lengths, UTF-8, regex constraints,
namespace validation and JSON/YAML/XML parsing are outside this abstraction.

## Source correspondence

- `internal/aws/cloudformation_conditions.go`, `stackParameterValues`: accepts
  the two carriers and validates original supplied/default keys and constraints.
  `Validate` abstracts this rejection boundary. NoEcho, unsupported type and
  invalid concrete-key cases require native tests, not separate model parsers.
- `internal/aws/cloudformation_ssm_parameters.go`, `stackSSMRequests` and
  `resolveSSMParameters`: build selected logical-name/key mappings, check every
  local key and `ssm:GetParameters` ARN before opening the read transaction, then
  select each region/name from `aws_ssm_parameters` in that single transaction.
  `Authorize` and `Read` correspond to this ordering. `missing` and `wrongType`
  also stand for rejected empty, oversized or malformed source values.
- `internal/aws/cloudformation.go`, `compileStackWithParameters`: preserves
  original `Parameters`, stores the resolver's `ResolvedParameters`, and replaces
  reference-map entries before evaluating conditions and compiling resources.
  The `plan` witness represents remaining compilation/admission failure. The
  model's reference and equality-condition observations must use captured values.
- `StackStore.Create` and the CreateStack permission path in that file publish
  stack/resource/event state only after successful compilation and authorization.
  `Publish` abstracts the complete transactional result. Failure before publication
  leaves all three modeled categories absent. The model does not replace the
  existing ownership, event-history or cleanup models and does not prove rollback
  for arbitrary SQLite failure injection.
- `stackSSMParametersXML` serializes saved original keys and resolved values for
  DescribeStacks. `KeysPreserved` and `SnapshotFrozen` check the persisted data
  underlying this view, including after source changes. XML escaping, order,
  wire shape and restart decoding remain native/SDK test obligations.
- The CreateStack `ClientRequestToken` fast path in `cloudformation.go` compares
  the saved token/request hash, authorizes the existing stack and returns its
  existing ID without compilation or Parameter Store lookup. `Replay` abstracts
  one retry of that saved token. Matching retries need not retain source-read
  permission; existing-stack authorization still applies. Mismatched requests
  and denied existing-stack access return rejection without changing saved data.
  Hash construction/collision resistance, stack-name reuse, token parsing,
  simultaneous requests and other token values are not modeled.

## Independent obligations and negative controls

Sixteen named invariants check type bounds, validation and all-key authorization
before reading, coherent capture, no failed/early writes, exact admission,
non-rejection of valid keys because of resolved values, original-key preservation,
immutable saved values, resolved references/conditions, complete publication,
and replay identity/no-read/no-mutation. Expected admission properties use
literal input classes rather than reusing operational validation predicates.
The independent `captured` observation records the source at the read boundary;
comparison against operational `snapshot` detects an intentionally torn capture.

The registry requires fifteen named controls for this new model:
`SkipInputValidation`, `AuthorizeOnlyFirstKey`, `AcceptNonStringSource`,
`SkipPlanValidation`, `ValidateResolvedValueAsKey`, `TearSnapshot`,
`StoreValuesAsKeys`, `RefreshAtCommit`, `RefsUseKeys`, `ConditionsUseKeys`,
`OmitEvents`, `ReplayReadsAgain`, `AcceptMismatchedToken` and
`SkipReplayAuthorization` and `LeakWritesOnReadFailure`. Each must violate its declared invariant. A syntax
error, timeout, wrong invariant or arbitrary failing exit is not success.
These are abstract mutation sensitivity checks, not generated Go mutations.

## Compatibility and evidence boundaries

No lookup leaves the simulator. AMI existence, images/VM execution, list and
SecureString values, versioned names, cross-account references, dynamic
references, secret handling, full AWS CloudFormation behavior and the original
Battle workload lifecycle are excluded. No unbounded or liveness theorem is
claimed. Generic transaction and IAM outcomes are abstractions, not established
SQL/Go refinement.

Exact-source model results must come from the validation receipt. Native tests
must separately establish key constraints, per-key authorization, missing/type
rejections, atomic/no-write failure behavior, actual Ref and condition behavior,
saved DescribeStacks values after update/deletion/restart and matching/mismatched
replay. Registration and a scoped TLC run do not replace the complete formal,
quality, SDK/E2E and exact-HEAD review gates required before merge.

## Local validation receipt, 2026-11-08

The scoped working-tree run used the registry-pinned TLC 1.7.4 jar, one worker
and a 512 MiB heap. The baseline completed with 2,808 generated states, 2,616
distinct states, an empty queue and depth seven. All fifteen controls produced
their declared invariant counterexamples. The added write-leak control initially
had an expression-precedence diagnostic; its corrected run passed, and the
failed diagnostic is not counted as a counterexample. Existing registry entries
were preserved and the complete registry passed structural validation.

The corresponding native test targets are
`TestStackSSMParameterSnapshotLifecycleAndReplay`,
`TestStackSSMParameterAdmissionAndAuthorizationFrames` and
`TestStackSSMParameterResolverBoundaries` in
`internal/aws/cloudformation_ssm_parameters_test.go`. Their existence is not a
claim that they or the full merge gates passed; implementation execution is
reported separately. This scoped working-tree receipt is not immutable-HEAD
merge evidence and must not substitute for the final complete formal gate.
