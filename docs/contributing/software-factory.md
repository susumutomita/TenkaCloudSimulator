# Bounded Finder/Fixer development

This is a development-only handoff mechanism, not part of the simulator runtime.
It separates evidence gathering, implementation and independent review. The
integration owner invokes the optional `issue-finder` and `issue-fixer` agent
instructions with the existing authorized agent tool. A standard-library Python
3 ledger enforces local claims, path conflicts, duplicate scenario IDs, a two-attempt
budget and exact-HEAD review evidence. It does **not** launch an LLM, run commands,
verify log authenticity, poll GitHub, create Issues/PRs or merge anything. A `ready`
entry means evidence was recorded, not that CI or remote integration occurred.

## Reference and adaptation

The [AWS Emulator AGENTS contract](https://github.com/aws/aws-emulator/blob/7753983c6e495ec3612695b1336bf4936fc7b7fd/AGENTS.md)
describes generated Smithy protocols, service-owned Rust operation/state modules,
service and cross-service tests, and protected generated/vendor files. The
[launch article](https://www.allthingsdistributed.com/2026/11/aws-emulator-and-the-engineers-job-when-agents-write-the-code.html)
describes tests checked against AWS, agent implementation and independent gates.
These are references for the separation of duties, not imported agent code.
The concrete `IssueFinderAgent`/`IssueFixerAgent` implementation and launch API were
not established by this inspection; do not claim this is that upstream harness.
Upstream's root is CC0; vendored components have separate notices. No upstream
source is copied here.

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

A finding JSON has this shape (example only, not a verified defect):

```json
{
  "provider": "aws", "operation": "ExampleOperation", "scenario": "missing resource",
  "base": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "paths": ["internal/example"],
  "expected": "Documented missing-resource error", "actual": "Observed response",
  "reference": "Official API URL and relevant section",
  "oracle": "official-specification",
  "reproduction": "Reviewed local test name, input and evidence path, not a shell command",
  "inventory_checked_at": "2026-11-12T00:00:00Z", "related_work": [],
  "required_checks": ["make before-commit", "focused regression"]
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
