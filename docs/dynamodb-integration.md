# PR309 current-main integration — SOURCE ONLY

2026-11-04. This private branch starts from the actual public PR309 head
`ed877ecc56c0fd4eef7a3a5a3b0b23d3ca435cfe`; a normal two-parent merge incorporates
main `5c97dc5916fa39fd43d80ae79532d5892fcb30ea` as `fc15261`. The next normal merge incorporates KMS main `0a4ed897f492011bcef0a5d8765f45c200809db9`; its18 models/94 controls and24 E2E cases are retained. External Muse work,
the original public branch and previous local `e97f69f` candidate are preserved.
No force push, public update, workflow rerun or repository-setting change occurred.

## Preserved contracts and selective source reuse

Main GCP/Azure handlers, Actor/access context, raw path preservation, fixed provider
scope menu, assets/dependencies/notices, all24 existing mandatory E2E methods and
all18 existing model entries/94 literal controls remain intact. Four conflicts
were resolved additively: README, Gateway field, server initialization/capability
metadata and two OpenAPI descriptions. OpenAPI paths and all61 operation IDs are retained, along with all main schema and example objects. Seven new DynamoDB JSON1.0 templates and two mandatory actual-process/served-template E2E sources are prepared; their current execution is UNRUN.

Owned earlier fixes are separate commits: validation tests `cedc2f3`, decimal
compatibility `edd8743`, semantic400KiB/statistics `67d45c6`, generation preparation
`58185f3` and native follow-up `c67c34c`. All13 DynamoDB native/SDK source files
match the earlier `generation-native-inputs.sha256` receipt byte-for-byte.
Historical67 selected normal/race entries and real350000-byte binary/70000-literal
string SDK proof are evidence for that old input, not a test run against this
new composition. The c1ac108 row-effect repair subsequently passed selected native normal/race/vet and the explicit SDK-size test. Those results precede the KMS-main merge; this new composition needs fresh validation.

The numeric change deliberately rejects fractions, hexadecimal, excessive
precision/range and huge exponents previously accepted by big.Rat. It preserves
valid decimal canonical keys and saved JSON without migration or deletion. Size
uses UTF8 names/strings and decoded binary, including documented nested overhead;
number sizing is an approximation rather than an AWS private encoding guarantee.
Same-Tx table/schema identity, conditional mutation, late SQL rollback and
non-destructive orphan refusal are retained. Immediate local table lifecycle and
32-hex TableID differ from AWS asynchronous state and documented UUID format.

## Bounded formal correspondence — SOURCE, not yet executed

DynamoDBLifecycle is unchanged: two preselected DB/table scopes, one key, two
generations, eight properties and eight exact controls. It models generation,
same-Tx conditions, abstract preflight authorization and failure frames.
DynamoDBAdmission adds one preselected DB authority, two region/table slots,
one active table, one item/table, four abstract semantic bytes, eight normalized
key tokens and six size fixtures. Its22 properties/22 controls cover constructor
readiness, typed canonical aliases, normalized validation and semantic-byte
admission, table/item quotas, replacement/statistics, acknowledged SQL effects,
removal and refusal/peer frames. Main18/94 remains byte-identical in the registry;
candidate inventory is20 baselines/124 controls (144 total cases, UNRUN).

This is bounded normalized admission, not a full decimal grammar, arbitrary
UTF8/base64/JSON, exact400KiB private AWS encoding, SQL/driver, Go refinement,
IAM parsing, credentials or host authentication proof. Independent literal Go
key/size tests and genuine SQLite ignored-write fault tests execute concrete code.
The helper models' input fixtures deliberately do not claim an exhaustive parser
or backend proof. Table/item limits are small finite analogues of documented
local100/10000/409600 limits, not the actual runtime quota values.

Admission scopes are two region/table namespaces inside ONE already-selected
DB; Lifecycle owners abstract separately selected DB/table authorities. Neither
is two authenticated CLI processes; the mandatory real-process acceptance is
separate and has not run. Constructor startupValid is the normalized success/
orphan-refusal result, not a proof of arbitrary SQL discovery or imported data.
Shared constructor/Gateway/dispatch files bind only to initialized-store and
preflight-denial call contracts. Existing routes and actor authority retain their
main models and native tests. No unrelated S3 model is used to justify a DDB
number/size helper. No runtime Go path receives a nonformal exemption.

The optional Python SDK fixture is test-only: its exact path is referenced only
by dynamodb_size_test.go, outside product embed patterns. A narrow exact-path
test-support classification and negative nearby.py/.go fixtures are prepared;
there is no broad testdata or Go exemption. Mandatory SDK tests do not skip.
The exact applicability record is regenerated against the new main after the
source merge; declaration and hash matching are not semantic verification.

## Verification queue

1. Review the decimal compatibility restriction, semantic size approximation,
   table-generation/rollback source and model correspondence. No repair/delete
   of existing user DBs is allowed.
2. Reserved focused native normal/race/vet and the real semantic-size SDK helper;
   verify deterministic stale snapshot, conditional writers, late commit and
   non-destructive orphan refusal on this exact source.
3. Implement/register missing helper/initialization contracts, exact-HEAD formal
   baseline and specifically classified mutant runs, guard omission fixtures and
   exact applicability. Parsing/runtime errors never count as counterexamples.
4. One current whole-module profile for both original90% floors. Public old
   source87.91% failure remains a real historical blocker, not a current profile.
   Do not merge profiles, lower floors, add exclusions or infer global coverage
   from selected tests.
5. Preserve all24 main E2E cases; retain two new mandatory real DynamoDB cases: two authenticated
   CLI/processes, distinct DB/token, same-name keys, cross-token rejection,
   participant IAM refusal, restart and cleanup. Existing two-database native
   fixtures are not two authenticated processes. The optional SDK size test is
   not a mandatory whole E2E replacement. The seven served templates are the second mandatory case; both cases remain UNRUN until an actual current binary is supplied.
6. Full before-commit/build/cross/E2E/start/audit/rights and hosted canonical
   security checks, independent review, fresh exact-HEAD CI. Known denied feed
   and old ed877 check-run reads are stopped; no retry or alternate route.

No DynamoDB console, CloudFormation, function SDK bridge, Query/Scan/indexes or
additional Challenge problem is enabled. Only the recorded hello-world grading
slice remains supported; no new full catalog lifecycle is claimed.

Official primary references re-read 2026-11-04:
[constraints](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html),
[item sizing](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html),
[TableDescription](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TableDescription.html).
