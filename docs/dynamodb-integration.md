# PR309 current-main integration — SOURCE ONLY

2026-11-04. This private branch starts from the actual public PR309 head
`ed877ecc56c0fd4eef7a3a5a3b0b23d3ca435cfe`; a normal two-parent merge incorporates
main `5c97dc5916fa39fd43d80ae79532d5892fcb30ea` as `fc15261`. External Muse work,
the original public branch and previous local `e97f69f` candidate are preserved.
No force push, public update, workflow rerun or repository-setting change occurred.

## Preserved contracts and selective source reuse

Main GCP/Azure handlers, Actor/access context, raw path preservation, fixed provider
scope menu, assets/dependencies/notices, all22 existing mandatory E2E methods and
all16 existing model entries/82 literal controls remain intact. Four conflicts
were resolved additively: README, Gateway field, server initialization/capability
metadata and two OpenAPI descriptions. OpenAPI paths, all61 operation IDs, schemas
and examples are unchanged; seven validated DynamoDB reference templates have not
been added by this preparation.

Owned earlier fixes are separate commits: validation tests `cedc2f3`, decimal
compatibility `edd8743`, semantic400KiB/statistics `67d45c6`, generation preparation
`58185f3` and native follow-up `c67c34c`. All13 DynamoDB native/SDK source files
match the earlier `generation-native-inputs.sha256` receipt byte-for-byte.
Historical67 selected normal/race entries and real350000-byte binary/70000-literal
string SDK proof are evidence for that old input, not a test run against this
new composition. Go compilation has NOT run on this branch.

The numeric change deliberately rejects fractions, hexadecimal, excessive
precision/range and huge exponents previously accepted by big.Rat. It preserves
valid decimal canonical keys and saved JSON without migration or deletion. Size
uses UTF8 names/strings and decoded binary, including documented nested overhead;
number sizing is an approximation rather than an AWS private encoding guarantee.
Same-Tx table/schema identity, conditional mutation, late SQL rollback and
non-destructive orphan refusal are retained. Immediate local table lifecycle and
32-hex TableID differ from AWS asynchronous state and documented UUID format.

## Formal preparation and remaining honest gaps

The registry adds only DynamoDBLifecycle: two abstract owner/database scopes,
one key, two table generations and one delayed request. Eight properties and
eight required mutant IDs protect generation/NoOrphans, same-Tx conditions,
preflight-Allow/Deny composition, failed-state frames and other-scope frames.
All main16/82 entries remain; candidate inventory is17 baselines/90 controls.
The old exact TLA/CFG bytes and eight mutations are retained; a new omission
fixture source removes the model, each property/CFG invariant and each control.
**All new parser/unit/TLC checks are UNRUN.** No source presence is a formal pass.

DynamoDBLifecycle does not prove decimal normalization, UTF8/base64 arithmetic,
400KiB boundaries, HTTP parsing, actual IAM/token authentication, startup SQL,
wire serialization or Go/SQL refinement. Do not bind pure number/size helpers to
NoOrphans. An attribute-budget model with meaningful oversize/failed-update/frame
mutants and a bounded decimal-key/precision/range model need source review and
actual sensitivity checks. Factory/dispatch changes also need a narrow, honest
composition argument; the lifecycle's trusted DB/scope assumptions cannot be
presented as proved host routing or constructor behavior.

The current formal/applicability.json is inherited main history, NOT a final
record for this PR. Regenerate the exact diff/blob record only after these scope
gaps are resolved; the candidate must remain non-ready until then. Main's exact
Formal validation workflow is included unchanged; no required-check setting was
altered. Its existence does not make an unclassified runtime diff acceptable.

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
5. Preserve all22 main E2E cases; add mandatory real DynamoDB two authenticated
   CLI/processes, distinct DB/token, same-name keys, cross-token rejection,
   participant IAM refusal, restart and cleanup. Existing two-database native
   fixtures are not two authenticated processes. The optional SDK size test is
   not a mandatory whole E2E replacement. Served examples remain separate work.
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
