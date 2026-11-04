> Current main5c97 integration is SOURCE ONLY; see [current integration gaps](dynamodb-integration.md). The following record describes prior local e97 preparation and historical ee836 native evidence. No current-branch execution is implied.

# DynamoDB registration preparation — not a gate pass

2026-11-04. Current main2d8a41a0cb576bd42561a7162d35129f44979c08 was
normally merged as cc550feaa0d7a21e4bf8ee9e5eee2bad6a6da2a5. Only formal-policy
helpers/docs/fixtures changed from main55a; the complete recorded DDB native Go
and SDK input hashes still match ee83646's67 selected normal/race results.
Those are native results only, not a fresh full profile or a TLC result.

## Meaning of the model's owner and authorization

Each `Owners` label denotes one preselected scope tuple:
**(owned simulator process/database, fixed region, fixed table name)**. Two labels
are not two tenants authenticated inside one SQL database. All provider SQL
queries use the Store's trusted database receiver and snapshot Region/Name.
The host still must allocate distinct process/DB/token authorities and authorize
every team route. Neither the model nor the current native two-database evidence
proves actual two-process cross-token/cookie rejection.

Store methods are trusted internal APIs. They receive a captured table snapshot;
they do not parse credentials or evaluate IAM. Gateway's `serveDynamoDB` computes
`dynamodb:<Operation>` and table ARN/`*`, evaluates Identities.DecisionContext and
rejects Deny before calling the Store. The model's Allow/Deny is that **preflight
composition assumption**, not a fabricated IAM check inside PutItem/DeleteItem.
Exact policy parsing, ARN binding, transport and principal identity need native
and actual authenticated-process tests. The model's foreign-mutation property
preserves the preselected scope; it does not verify HTTP/team routing.

## Concrete property correspondence

| Property | Candidate mechanism | Evidence / scope limit |
|---|---|---|
| NoOrphans | constructor non-destructive orphan refusal; same-Tx table existence in checkDynamoGeneration; child/table deletion together | ee83646 native missing-table and rollback/orphan tests PASS; model is an abstraction of SQLite, not a SQL/FK/refinement proof |
| GenerationChecked / NoStaleItems | helper rereads TableID plus schema under item/table Tx; old validated snapshot cannot enter replacement generation | Native cached snapshot→Delete/Recreate with changed S→N key type rejects Put/Get/Delete/stats/table-delete, fresh item retained. Internal32-hex TableID retained; not a new UUID/row-generation column |
| ConditionalAtomicity | exists/not_exists predicate read and write in same immediate transaction | Native two-writer one-winner and conditional deletion PASS; model ghosts reqSawExists solely to test a cached-check mutant |
| NoUnauthorizedMutation | Gateway authorization preflight then trusted Store | Abstract Allow/Deny only; Store itself has no IAM boundary |
| FailedUnchanged | deferred rollback on validation/SQL failure, Commit failure propagation | Genuine deferred INSERT/trigger succeeds inside Tx then Commit fails; native Store restores rows. Model does not prove filesystem durability |
| NoForeignMutation | trusted Store DB receiver + captured Region/Name carried to SQL | Process/row-routing assumptions, not row-level multi-tenancy or real host authentication |

The registered source scope is intentionally only the state/protocol boundary:
`internal/aws/dynamodb.go`, `dynamodb_store.go`, `dynamodb_generation.go`.
The model and cfg now use canonical `formal/DynamoDBLifecycle.tla/.cfg` paths.
Eight exact single-anchor mutants are declared in formal/models.json. Source
schema/config-invariant/anchor checks were read and mechanically accepted, but
**TLA parsing, baseline, all mutant outcomes and shared required formal check
have not run**. No timeout/error/deadlock may become a successful negative.

## Applicability remains incomplete

Do not bind numeric normalization or semantic byte arithmetic to NoOrphans merely
because their files belong to the same service. `dynamodb_number.go` and
`dynamodb_size.go` are new pure runtime helpers. The lifecycle model does not
model decimal lexical equivalence/precision/range, UTF8/base64 sizes, nested
container overhead, numeric approximation or full400KiB arithmetic. These require
an explicit reviewed applicability argument and suitable property/model work;
existing native boundary tests are useful separate evidence, not a false finite
model claim. A TypeOK-only binding is not sufficient under the current policy.

Shared Gateway/server initialization additions likewise need honest composition
scope review, keeping main's REST/S3/CFN properties intact. No applicability entry
was fabricated to exempt runtime helpers or declare all changed blobs modeled.
The main applicability record is not a valid final candidate record yet. Exact
immutable-head record generation/check is pending after scopes/model source are
reviewed. Full formal CI must remain blocked until this is resolved.

## Remaining verification order

1. Review/freeze registered lifecycle model and8 mutations; reserved Java1 TLC
   baseline + each expected invariant/exit12/trace, with pinned JAR/input hashes.
2. Resolve pure helper and initialization/dispatch applicability honestly; create
   exact current-base record and run policy/negative controls. Do not reuse an
   unrelated S3/SSM model as a number/size proof.
3. Exclusive whole-Go/race/WASI profile on current exact candidate and both90%
   floors, meaningful current coverage gaps, govulncheck/security and relevant
   SDK/E2E. Old public87.91 source coverage is not current profile attribution.
4. Actual two authenticated processes, same-name data, stale generation, restart,
   caller denial and cleanup; console/API docs where applicable. Original problem
   support remains unavailable without full real host scoring acceptance.

During SQS→S3 heavy reservations this is source-only: no Go/Java/browser or network
installation. External Muse PR309 branch, public merges, blocked Actions-lock
candidates, release743 and host work remain untouched. All public merges are held.
