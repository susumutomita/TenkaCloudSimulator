# Bounded Finder/Fixer development

This is a development-only handoff mechanism, not part of the simulator runtime.
It separates evidence gathering, implementation and independent review. The
integration owner invokes the optional `issue-finder` and `issue-fixer` agent
instructions with the existing authorized agent tool. A standard-library Python
3 ledger enforces local claims, path conflicts, duplicate scenario IDs, a two-attempt
budget and exact-HEAD review evidence. The ledger does **not** launch an LLM, poll GitHub, create Issues/PRs or merge
anything. Its acceptance executor below runs a fixed reviewed command and checks
exact-HEAD test evidence. A `ready` entry is not remote CI or integration proof.

## Reference and adaptation

The [AWS Emulator AGENTS contract](https://github.com/aws/aws-emulator/blob/7753983c6e495ec3612695b1336bf4936fc7b7fd/AGENTS.md)
describes generated Smithy protocols, service-owned Rust operation/state modules,
service and cross-service tests, and protected generated/vendor files. The
[launch article](https://www.allthingsdistributed.com/2026/11/aws-emulator-and-the-engineers-job-when-agents-write-the-code.html)
describes tests checked against AWS, agent implementation and independent gates.
These are references for the separation of duties, not imported agent code.
The concrete `IssueFinderAgent`/`IssueFixerAgent` implementation and launch API were
not established by this inspection; do not claim this is that upstream harness.
Upstream's root is CC0; vendored components have separate notices. The initial ledger copied no upstream source. The new acceptance pilot below
preserves a pinned CC0 test and its license as test-only provenance.

Tenka's Go single-binary contract remains authoritative. Preserve provider-neutral
core, native protocol semantics, console/API shared state, bounded runtime and
explicit unsupported behavior. Recorded real-cloud results can be an oracle only
when provenance and conditions are known. Real-cloud execution requires separate
authorization, cost controls and cleanup; this workflow never initiates it.

## One local cycle

Store the ledger and evidence outside the repository, on a local filesystem.
All workers in one integration lane must use the **same** ledger; separate machines
or different ledgers do not coordinate claims. SQLite serializes claim changes;
there is no expiring lease that silently lets a second worker take over.
The integration owner also checks live external/local ownership as required by
[parallel-development.md](parallel-development.md).

A finding JSON has this shape (schema example only, not a verified defect;
replace the example operation and paths with the actual SQS pilot finding, or
register a corresponding acceptance suite first):

```json
{
  "provider": "aws", "operation": "DeleteMessage", "scenario": "deletion with zero visibility",
  "base": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "paths": ["internal/aws/sqs_store.go"],
  "expected": "Message absent after deletion", "actual": "Example only: message observed again",
  "reference": "Official API URL and relevant section",
  "oracle": "official-specification",
  "reproduction": "Reviewed local test name, input and evidence path, not a shell command",
  "inventory_checked_at": "2026-11-12T00:00:00Z", "related_work": [],
  "required_checks": ["make before-commit", "focused regression"],
  "acceptance": ["sqs-lifecycle"]
}
```

The owner validates this evidence before registering it. `related_work` includes
open/closed PRs and active local work; nonempty entries block a new claim until
manually reconciled. Inventory freshness is an owner responsibility, not a clock
assertion by this tool. Identity is provider/operation/scenario, independent of
base and prose. Repeated findings do not overwrite prior records. Stale unclaimed findings require explicit refresh. Terminal findings are never
automatically reopened.

```sh
python3 scripts/software_factory.py --db /tmp/factory.db add /tmp/finding.json
python3 scripts/software_factory.py --db /tmp/factory.db claim FINDING_ID --owner fixer-1 --base BASE_SHA
python3 scripts/software_factory.py --db /tmp/factory.db attempt FINDING_ID --owner fixer-1
# Invoke Fixer in its dedicated worktree, then record its committed candidate:
python3 scripts/software_factory.py --db /tmp/factory.db candidate FINDING_ID --owner fixer-1 --head HEAD_SHA
python3 scripts/software_factory.py --db /tmp/factory.db review FINDING_ID /tmp/review.json
python3 scripts/software_factory.py --db /tmp/factory.db list
```

The independent review JSON names `head`, a different `reviewer`, `changed_paths`,
a `findings` array and `checks`. Each check is `{ "status": "passed", "evidence":
"exact-head log location" }`; `failed` and `unrun` cannot produce `ready`.
Every finding's required check must appear. The owner defines all applicable gates
before claiming: always `make before-commit`, plus focused regression and any E2E
or formal checks required by AGENTS. Verify changed paths with the actual Git diff.
The tool checks declared paths/evidence only; it is not an adversarial sandbox or
proof of test execution. Security-sensitive or shared paths need owner review;
hidden paths are deliberately rejected by this first version.

After failure, the same owner may take the remaining attempt. After two failed
attempts, stop and escalate. On denial, stop immediately:

```sh
python3 scripts/software_factory.py --db /tmp/factory.db stop FINDING_ID --owner fixer-1 --reason 'permission denied; owner action required'
python3 scripts/test-software-factory.py
```

After independently verifying integration of the reviewed HEAD, its owner closes
that entry, releasing its path reservation. This records supplied evidence; it
never queries GitHub or proves a merge itself:

```sh
python3 scripts/software_factory.py --db /tmp/factory.db close FINDING_ID --owner fixer-1 --head HEAD_SHA --integration 'Verified PR URL, merge SHA and verification time'
```

For a stale **unclaimed** finding, inspect current main and related work again.
Read its fingerprint from `list`, prepare updated JSON with the same scenario,
and explicitly record who reconciled it and why:

```sh
python3 scripts/software_factory.py --db /tmp/factory.db refresh FINDING_ID /tmp/refreshed.json --actor integration-owner --reason 'Related PR inspected; independent scope confirmed on new base' --fingerprint CURRENT_FINGERPRINT
python3 scripts/software_factory.py --db /tmp/factory.db history FINDING_ID
```

Refresh rejects active, ready, closed or blocked entries and stale fingerprints.
It cannot clear a denial or reset a fixer's attempt budget. Close and refresh
store before/after snapshots, actor, reason and timestamp in the local audit table.
This is a cooperative local record, not tamper-resistant authorization.
`make test` (and therefore `make before-commit`/CI) runs the lightweight controller
tests before the existing Go gates.

No automatic expiry, reopening, publication, schedule or merge is installed.
Only the integration owner reconciles terminal entries and remote status. Before
any authorized publication/merge, check current remote HEAD, duplicate PRs,
required CI and unresolved reviews separately. Never turn an old ledger record
into a claim that a new HEAD passed.

## Executed acceptance gate (agent-neutral pilot)

The original ledger above checks cooperative declarations. New findings must also
name `"acceptance": ["sqs-lifecycle"]`, and an otherwise successful review must
supply `"acceptance_report": "/absolute/local/report.json"`. A missing, old-HEAD,
altered or skipped receipt cannot transition a job to ready. Failed reviews still
return the existing bounded retry state. Old persisted findings without an
acceptance suite cannot become ready: reconcile through the existing owner, never
replace a lost ledger with an empty one or automatically reset a denial.

The current registered pilot is **SQS lifecycle only**. Do not assign this suite
to an unrelated finding as its acceptance. Register a reviewed suite with named
required tests before starting another service. Additional full `make before-commit`,
E2E, formal, independent security/code review and remote CI remain required as
applicable. Passing this pilot alone is not a merge approval.

1. Test Writer fixes an oracle and observable assertions before Fixer changes.
2. Finder reproduces and checks inventory; integration owner claims existing ledger.
3. Fixer commits on the assigned branch; oracle edits need independent review.
4. In the clean candidate checkout run:

   ```sh
   python3 scripts/factory_acceptance.py --output /tmp/acceptance.json
   python3 scripts/factory_acceptance.py --verify --output /tmp/acceptance.json
   ```

5. Independent reviewer reruns the gate on that HEAD, examines the oracle/diff and
   records that report alongside all other required checks in the ledger review.

Commands are a reviewed in-code allowlist, not shell strings from issue text.
The executor disables Go module downloads and strips cloud credential/proxy
variables, enforces a timeout, captures JSON test events, and requires the exact
expected test and package completion. Empty output, skip, exit failure and timeout
fail closed. Missing Go/modules are blocked, not skipped. Logs bind to HEAD,
source checksums and test identities. `make test` runs the gate; existing CI calls
`make before-commit`, so a failure makes that CI job fail. **No branch protection
or GitHub required-check configuration has been changed**: this does not prevent
a privileged user bypassing CI. Likewise local receipts and reviewer names are
not authenticated attestations; they are reproducible evidence checked by CI,
not a hostile-agent security boundary. Use an isolated executor for untrusted code.

The Go SQS gateway/SQLite test adapts the pinned public CC0 AWS evaluation-suite
scenario in `test/factory/upstream/`. Original bytes, license, upstream commit,
Git blob IDs and SHA256 are preserved. The upstream README says its scenarios
were verified against real AWS; **Tenka did not execute AWS**. This is not the
upstream boto3 runner or an AWS-provided agent orchestrator. Zero visibility makes
deletion observable immediately rather than relying on a sleeping hidden message.
Our adaptation does not establish boto3/HTTP-listener/UI/VM equivalence. Repeated
DeleteMessage receipts intentionally differ from AWS in current Tenka and are
not covered or claimed fixed. Non-pilot services, Console, docs send buttons,
Client Libraries and real Battle phases remain separate acceptance requirements.

Claude Code already imports AGENTS.md through CLAUDE.md. Optional test-writer,
issue-finder and issue-fixer prompts offer the same division of work; neither
Claude-specific hooks nor new permissions are installed. Existing approved agent
dispatch remains external to this offline executor.

Pilot findings are mechanically limited to the five SQS lifecycle operations and
SQS handler/store/test files. An EC2/UI/shared-core finding cannot borrow a passing
SQS receipt. Ready also compares declared paths with the actual base-to-candidate
Git diff. Git environment overrides and ambient Go overlays/workspaces/config are
removed from evidence execution; `GO` selects one explicit executable, not shell
arguments. Already-active legacy ledger jobs need owner reconciliation; this
change does not silently migrate, reset or replace their ledger.
