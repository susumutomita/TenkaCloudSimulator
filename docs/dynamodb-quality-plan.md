> Integration status (2026-11-05): See the
> [service backlog acceptance record](acceptance/backlog-services.md) for the
> integrated scope and required gates. Branch-specific status, receipts,
> model/control counts and E2E totals below are historical records of their
> named inputs. They do not establish the current integration's final CI
> or merge status.

> Historical native-source record. The main26d branch integration, separate executed receipts, unrun gates and scope gaps are tracked in [dynamodb-integration.md](dynamodb-integration.md).

# DynamoDB quality/generation follow-up — historical native verification

2026-11-04. External Muse PR309/head `ed877ecc56c0fd4eef7a3a5a3b0b23d3ca435cfe`
is not modified or published from this branch. Own history retains numeric
`12cdebde8570fa8ea22956bb527391ec208a1cd4`, semantic-size
`ab44a0234c648f50380e192e9571e74aba0de27f`, then NORMAL merge
`137af6c7d1c67b10cf86ad08229b5e1b9f80e8f5` of main
`55a111953eb3eb0dfd90aaa473ac3fe39cb10b66`. No rebase, force push or history deletion.

The merge resolved README, Gateway field/server initialization and root OpenAPI
conflicts additively. Current main's API Gateway/CFN/LambdaPermission, console,
formal workflow/strict coverage helpers are preserved. DDB target dispatch and
store setup remain. The old numeric behavior change remains a separate commit;
no stored DB rows or original schemas are migrated or deleted.

## Source-discovered missing property and candidate fix

Before this follow-up, the handler read a table and validated its schema before
Put/Get/DeleteItem entered its store operation. Store Put/Delete did not recheck
existence or TableID; item tables have no SQL FK. A delayed old request could
write an orphan after deletion, or use an old key schema after same-name recreation.
DeleteTable also captured metadata outside its transaction then deleted by name.
It is incorrect to claim an existing unrelated formal model proves these guards.

Candidate `8008b1e` makes item operations and stats accept the captured table
snapshot. `checkDynamoGeneration` rereads real table existence/TableID/schema in
the **same SQLite transaction** as condition evaluation and read/write. Changed
TableID returns ResourceNotFoundException; same-ID schema corruption fails
internally. DeleteTable's real path uses `deleteTableSnapshot` with the same
check before deleting child rows and metadata. Item key columns are checked
against actual item attributes before Put, and corrupt stored key data fails on
Get. Conditions remain the two supported exists/not_exists modes; unknown store
modes fail rather than turning into unconditional operations.

Startup checks for preexisting orphan rows and **refuses without deleting,
repairing, migrating or adopting them**. No FK/schema migration is added.
Operators must preserve/inspect the existing DB separately; no automatic repair
or destructive cleanup is authorized. Mixed-version processes or external SQL
writers sharing a live DB are outside this provider contract. Host must allocate
one owned DB/process/token authority per team.

Existing internal32-hex TableID representation is retained for compatibility; AWS documents UUID-shaped TableId. Full wire-format parity of that legacy field is not claimed or silently migrated by this generation fix.

Stats retain semantic item-size calculation and are read with the matching table
generation in one transaction. A delete response keeps the prior immediate-local
zero-count behavior without rereading a recreated table. Real AWS has async
DELETING and may accept item operations during deletion; this simulator's immediate
lifecycle is deliberately narrower. No capacity/billing/propagation parity claim.

## Coverage evidence and meaningful test inventory

Hosted run41797379476 on externaled877 reported statement93.43% PASS but
unfiltered source-line87.91% FAIL (5365/6103). This is an old exact source profile;
new main changes the denominator. Raw hosted coverage artifact was not available
locally after its materialization transport failed; it is not inspected here.
No invented per-file attribution of that87.91% failure is made.

The earlier **focused-only** profile at4bfa71d showed DDB handler92.09% statements
and store83.70% statements, while the selected aws package overall was12.7%.
Its zero-count ranges were mapped to the **old file**, not shifted current lines:

| Old focused gap / observed risk | Source-prepared meaningful verification | Status |
|---|---|---|
| Store creation/closed DB and malformed persisted schema | Real SQLite closed constructor and corrupted JSON/metadata, internal failure/no fake table | Native focused normal/race PASS |
| Conditional read/write isolation and no duplicate winner | Two goroutines release together with not_exists on one item, exactly1 success/1 condition failure, no lost item; exists/not_exists Delete behavior | Native focused normal/race PASS |
| Missing generation/existence, no FK, cached schema reuse | Capture metadata→delete→attempt stale operations; recreate same name with N key→old S snapshot Put/Get/Delete/stats/table-delete all reject and fresh item survives | Native focused normal/race PASS |
| Delayed SQL commit failure | Fixture-only deferred FK trigger fails after item write at Commit; transaction restores item/trigger rows | Native PASS; real INSERT/trigger succeeds before Commit fails |
| Existing orphan data | Real orphan fixture, constructor refusal and exact preserved old JSON | Native focused normal/race PASS |
| Key/schema corruption and unsupported store condition mode | Provided pk vs JSON key mismatch, wrong type/malformed document, unknown condition rejects without writes | Native focused normal/race PASS |
| Late insert/delete/table-delete trigger failure | Existing4bfa SQL rollback tests retained; new code must rerun them | Native rerun PASS after merge |
| True table100/item10000 quota | Existing limits unchanged; prepared100 native table creates and10000 small correctly typed item fixtures, new-item rejection but replacement success, no quota lowering | Native focused normal/race PASS |
| rand.Read failure / unreachable schema marshal branch / row-scan and cancellation details | Not claimed covered; avoid fake entropy/global mutation and branch-mirroring fixtures | Unverified |

Earlier semantic-size proof atab44: raw350000-byte B and70000 literal `<` S
roundtrip through actual boto3 HTTP, correct semantic stats, oversized replacement
rejection without mutation; UTF8/boundary/nested/sets/restart/rollback tests passed
focused normal/race. Those receipts are **historical**, not proof that this merge
or new generation code passes. SDK helper remains and must rerun unchanged.

## Bounded model and protocol correspondence

Prepared source: `formal/dynamodb/DynamoDBLifecycle.tla/.cfg`, with honest mapping
and limits in its README. Two owner labels, one table name/owner, one key, two
unique generations; delayed request interleaves with another complete writer or
Delete/Recreate. Candidate properties target NoOrphans, generation check,
conditional-at-commit, IAM decision guard, foreign mutation and unchanged failures.
Ghost item generations represent the schema that validated the request; no new
physical item-generation column is claimed.

No TLC run, model registry/applicability registration, parser acceptance or
counterexample success is claimed. Root must review/freeze source, register an
explicit actual source scope, run baseline and intended guard mutants with real
invariant/exit12/trace controls. Existing models are not substitutes. The model
abstracts SQL atomicity and authorization; it proves no Go refinement, JSON/key/
number/size/quota parser, real IAM matching, token/OS/team isolation or liveness.

Official API is JSON1.0 with `X-Amz-Target: DynamoDB_20120810.<Operation>`;
2012-08-10 denotes that version date, not an invented Query Action interface.
Primary references read2026-11-04:
[PutItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_PutItem.html),
[DeleteItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DeleteItem.html),
[DeleteTable](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DeleteTable.html),
[TableDescription](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TableDescription.html).

The initial phase was source-only. The subsequent authorized native phase ran
gofmt/compiler and the full selected `^TestDynamoDB` aws+server tests, normal and
race, with the actual existing boto3 semantic-size HTTP helper. Full module+WASI
profile and both90% floors, browser/E2E, security, two authenticated processes,
finite models/applicability and exact-HEAD hosted CI remain pending. No test was
excluded to pass and no floor/coverage source-helper semantics were weakened.


## Executed native generation follow-up (2026-11-04)

GOMAXPROCS=1 / Go `-p 1`, exact selection `^TestDynamoDB`, both internal/aws and
internal/server, count1. Final normal PASS: aws2.067s, server1.032s. Final race
PASS: aws14.098s, server9.711s.67 RUN entries include subtests. Logs:
`/workspace/ddb-309-evidence/generation-normal.log` and `generation-race.log`;
source SHA receipt: `generation-native-inputs.sha256` in that directory.

The deferred-FK fixture was strengthened after the first successful selection:
it explicitly INSERTs/observes the pending trigger child within the transaction,
then proves Commit fails. The real store PutItem subsequently fails and restores
all item/trigger rows. This is not an arbitrary early SQL failure counted as
commit-path coverage. The strengthened complete selection was rerun normal/race.

Actual generation evidence is deterministic native-provider snapshot→delete or
same-name/different-key-schema recreation, followed by stale Put/Get/DeleteItem,
stats and table-delete calls rejecting without touching the fresh item. It is
not a claimed timed two-process HTTP race. Server lifecycle/reopen tests exercise
production server composition, but CLI token/cross-team process auth is separate.

The actual boto3 loopback helper proves rawB350000 and literal`<`70000 string
roundtrip and semantic stats, oversized replacement rejection/preserved state.
Its subprocess explicitly uses `/dev/null` AWS config/credentials files and
metadata lookup disabled, with existing interpreter/dependencies and dummy
credentials. No AWS endpoint, new install, guest workload, browser, TLC or whole
Go suite was run. All owned Go/SDK processes and loopback httptest servers ended.

No production semantic fix was needed after the first runtime invocation; current
production changes were formatted and passed. Only the late-commit fixture and
SDK test environment were strengthened, then rechecked. A new main merge changes
the candidate and requires appropriate fresh exact-source validation; do not
carry these results over automatically.


## Historical main5c97 ignored-write repair (native checks executed)

An immutable9b7f365 probe with only a new regression test reproduced five false
HTTP200 acknowledgements under genuine SQLite RAISE(IGNORE): new/replacement
PutItem, DeleteItem, and both DeleteTable child/parent refusal. The latter two
changed owned rows. Ignored CreateTable instead surfaced400 ResourceNotFound
while keeping rows; it was not a false200. Previous evidence is preserved.

The current candidate checks exact RowsAffected in the same transaction for
CreateTable, PutItem, DeleteItem and table+child deletion. A genuine missing-item
DeleteItem accepts zero affected rows. Internal Store PutItem also revalidates
semantic size rather than relying only on the HTTP caller. All six regression
cases now return bounded500 with exact table/item row frames unchanged;
missing-item deletion still returns200. No SQL details or row payloads are logged.

Current selected ^TestDynamoDB aws/server normal PASS1.574s/1.160s; race
PASS17.846s/12.969s; vet passed. The optional SDK test was skipped in that
selection, then explicitly executed with the already-installed boto3 interpreter:
PASS1.503s, actual loopbackB350000/S70000 roundtrip, oversize refusal and preserved
state. The standalone two-CLI mandatory E2E is separate and still UNRUN.
Whole-profile90%, full E2E, current formal models and applicability remain pending.
