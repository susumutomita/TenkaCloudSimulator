# DynamoDB finite-model correspondence

Current private composition: main0a4 plus original PR309 and selective owned fixes.
Main18 models/94 controls are preserved; DynamoDB adds two models/30 controls.
Candidate20 baselines/124 controls are SOURCE ONLY until canonical TLC runs.
Source declarations and exact hashes are not semantic acceptance or Go proof.

| Model/property boundary | Concrete correspondence | Explicit limitation |
|---|---|---|
| DynamoDBLifecycle: NoOrphans, GenerationChecked, NoStaleItems | Same-Tx table existence/TableID/schema snapshot guards; child/table delete together; non-destructive constructor orphan refusal is native-tested | Two preselected DB/table authorities, one key and two generations; not schema/SQL/imported-data proof |
| ConditionalAtomicity, FailedUnchanged | Conditional predicate and write share a transaction; deferred rollback and commit failures propagate | Does not execute SQL or prove filesystem durability; cached-generation/two-writer/deferred-commit tests are separate |
| NoUnauthorizedMutation | Gateway DecisionContext preflight before trusted Store calls | Store itself is NOT an IAM boundary. No policy/ARN/token parser or host authentication proof |
| NoForeignMutation | Already-selected Store DB plus captured region/table predicates | Not row-level multi-tenancy; actual two-CLI/token evidence is a separate required E2E |
| DynamoDBAdmission: ValidKeysOnly, CanonicalKeys, SemanticBytes | Selected normalized numeric alias/type-tag and semantic-size fixture contracts, paired with literal Go tests | No full decimal/UTF8/base64/recursive grammar or private AWS number encoding proof |
| TableBudget, ItemBudget, ItemCountBudget, StatisticsExact | Same-DB global table cap, per-table item/semantic byte admission and immediate derived statistics | Two region/table slots, max1 active table, max1 item/table, max4 abstract bytes; not exhaustive real100/10000/409600 exploration or billing |
| CreateCommitted, CommittedBytes, AcknowledgedRemoval, RefusalFrame | Exact transaction-local RowsAffected and caller rollback before acknowledging native writes | Actual six SQL IGNORE regressions are separate proof for those faults; not arbitrary SQL/driver/trigger refinement |
| InitializedBeforeMutation, StartupAdmission, StartupRefusedFrame | Server returns error before handler installation on constructor refusal; Gateway dispatch calls initialized Store/preflight handler | startupValid abstracts constructor result, not arbitrary SQL discovery. Shared source bindings cover only added DDB call seams, not all routes or actor authority |

Lifecycle source/CFG and its eight controls remain byte-identical to the earlier
unrun e97 proposal. Admission has eight normalized key tokens, six size fixtures,
and22 properties/22 controls covering wrong aliases/types, validation/allow/deny,
wire-length/nested sizing, table/item quotas, false acknowledgements, statistics,
peer frames and restart. Scopes here are two region/table namespaces in ONE
selected Store/DB authority. Lifecycle's two labels instead abstract separately
selected DB/table authorities. Neither is a two-authenticated-process proof or a
joint refinement/composition proof of the two models.

All124 literal control IDs, required model/property sets and omission fixtures
are protected. Counterexamples must be the specified invariant with exit12 and
an actual trace; parse/type/oracle/deadlock/runtime errors never count as success.
TLC and new required SDK cases remain UNRUN at source preparation.

The optional Python native-size fixture has one exact test-support path, called
only from dynamodb_size_test.go and outside product embed globs. Nearby.py/.go
negative fixtures keep runtime paths unclassified; no broad Go exemption exists.
All runtime Go deltas have selected DynamoDB model bindings with explicit bounds.
The main24 E2E methods and all61/34 API IDs/paths are preserved, with two added
mandatory real-binary SDK/served-template cases (26 total). No DynamoDB console,
CFN, function SDK or additional Challenge grading is claimed.

See [integration and verification order](dynamodb-integration.md),
[historical and current native evidence](dynamodb-quality-plan.md), and
[native API/compatibility](dynamodb.md). Previous failed/blocked source and runtime
receipts remain outside the repository; they are not relabelled as current passes.
