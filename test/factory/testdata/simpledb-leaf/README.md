# Independent SimpleDB leaf TestWriter contract v1

Input commit: `6ed042dbda1df865078e1b0d7192752cc110f789`; immutable existing native/SDK TestWriter assets remain at `7253c5b5dd9b0ba4574fbfe8bb2209228f99ba6d`. This directory is test support, not production implementation, a registered factory suite, a claim, or service acceptance. Nothing under internal/, server, Gateway, dispatch or shared registry is changed.

## Frozen leaf API and ownership

`contract.Store` specifies structural Go methods using standard context/string/maps plus database/sql. Production package is `internal/aws/simpledb`; its constructor must be `NewStore(*sql.DB) (*Store,error)` and its concrete Store must satisfy the structural interface. It must not import this test fixture package. `binding/subject_test.go` performs that independent assignment without rewriting outputs or errors. A nil subject is BINDING_UNESTABLISHED. Caller owns the database lifetime. Constructor initializes only provider-owned schema; reopening an existing database must retain state. No server, host token, IAM, cloud credentials, SDK, external storage or network is part of leaf acceptance.

Account/region are trusted caller-supplied scope identifiers, not a new authentication/tenant-isolation mechanism. Root remains the only owner of choosing authenticated scope/principal and authorizing the action. Leaf scope tests assert state separation only. Actual team isolation remains separate process/DB/token authority.

All five methods are synchronous local data operations. Create is idempotent; Delete is idempotent and cascade removes the old values; Put merges unique value sets and Replace replaces only named sets; Get returns copies, optional name filtering and empty attributes for a missing item in an existing domain. Error values expose `Code() string`: InvalidParameterValue, NoSuchDomain, InvalidNextToken, Cancelled. These are internal error contracts; Cancelled is not an invented AWS HTTP error. Root maps documented API errors and cancellation separately.

List limit **0 is the internal omitted/default sentinel (100)**. It does not legalize explicit HTTP MaxNumberOfDomains=0: root must still reject that request, as existing immutable native/SDK tests require. Valid explicit limits are1..100. Tokens remain opaque to assertions; stable-state pages must progress, terminate, and neither duplicate nor omit names. No ordering or token encoding is prescribed. Concurrent pagination, quotas, binary/XML-restricted strings, DeleteAttributes, DomainMetadata, Select and conditional writes require later contracts.

Whole Put validation precedes mutation, rejected create/list/Put and cancelled writes preserve observable state, and wrong-scope operations cannot disclose or create state. Restart closes the real database and creates a new Store over a reopened connection; domain delete/recreate must not resurrect old values. Logical absence is checked; this fixture does not prove physical media erasure or all orphan-row/storage-quota invariants.

## Independent oracle, models and actual binding

`contract/vectors.json` supplies immutable scope, names, values and seven required cases. `contract/contract.go` is a reusable test-only oracle. It opens actual existing ncruces SQLite and checks its own DB availability before constructing the subject. Errors during DB setup/close are HARNESS_ERROR; a subject initialization/behavior failure is a failed product contract. It uses no future package import.

The positive control model and intentionally broken mutants exist only in `contract/control_test.go`. They persist an opaque oracle document in a test-only SQLite table; they are not a SimpleDB implementation. The control set demonstrates detection of Create/Put/Delete no-op, partial Put commit, ignored scope, cross-scope list disclosure and cross-scope deletion. Model PASS means the detector works; it is never product compatibility, restart or service acceptance evidence.

Production is exercised only by `binding/subject_test.go`. That test imports the future actual package and runs the same oracle. It cannot compile today; this is deliberately isolated under testdata and never compiled by the control command or ordinary ./.... The runner does not relabel that compile failure as product RED.

`runner.py` requires distinct external output paths and checks every input hash and clean Git state first. If production store.go is absent, it returns **UNIMPLEMENTED_RED/1, zero cases executed**, with a presence-only qualification, without trying to compile the missing import. Once production exists, it runs only the fixed binding package offline: missing constructor/signature/package is **BINDING_UNESTABLISHED/2**; driver/SQLite/zero-work/skip errors are **HARNESS_ERROR/2**; only a named, result-report-backed failing production case is **PRODUCT_CONTRACT_RED/1**. PASS requires exactly the parent, all seven named cases and package completion. A parent test start without a full oracle result report never establishes product RED. Typed-nil subjects are rejected as unestablished binding. Inputs and clean HEAD are rechecked after execution. Compiler descendants are killed in the runner's owned process group on timeout; no fallback to the model is possible.

## Commands and registration handoff

- Controls only: `go test -p=1 -json -count=1 -timeout=60s ./test/factory/testdata/simpledb-leaf/contract`.
- Runner detectors only: `python3 -B test/factory/testdata/simpledb-leaf/test_runner.py`.
- Actual binding: `python3 -B test/factory/testdata/simpledb-leaf/runner.py --go /absolute/Go1.26.11/bin/go --report /absolute/local/report.json --log /absolute/local/log.jsonl`.

Commands use only existing Go modules/SQLite and standard Python. No boto3 bypass, installation or SDK pin change. Existing composed native HTTP501 RED and SDK dependency HARNESS_ERROR remain separate, unchanged surfaces. See registration-proposal.json: it is a proposal only, not edits to factory/ledger. Root must register and review the immutable files/commands/path authority and update formal applicability before any claim/integration promotion. Existing all-four-surface service requirement remains unchanged; a passing leaf contract cannot mark the whole service ready.

Root adapter negative fixtures are in root-adapter-negative.json only. They are not executed coverage. They reserve missing/denied token, scope authority, non-SDB routing and malformed/version rejection checks for root.
