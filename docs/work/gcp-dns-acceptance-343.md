# Cloud DNS #343: work provenance and evidence boundaries

The current DNS-only branch descends from main
`a53131cd61030c8d238823478ec96917b19e95ef`. Its prepared runtime/test revision is
`4e4185534fad64f58badf94ab0ff1e11e0832d02`; the later documentation update does
not assert new runtime acceptance. See the [scope and usage guide](../acceptance/gcp-cloud-dns.md)
and [factory workflow](../contributing/software-factory.md).

## Preserved provenance

- The initial unreviewed runtime worktree was frozen and was not copied/adopted.
- Independent oracle: `d46c86fba6475dcec116a858b136dcc964cb2c95`, strengthened by
  `aaaecd2764540ac1dd306f110b89362fb60536c6` before the implementation claim.
- Leaf implementation: `4f26845fd68dd458df6dae82ae8361d9936e6e39`. Independent review
  requested correction of AFTER-trigger final-state drift. Attempt 2,
  `5def7d7b025ad702c50af5755dfeeebc78e1a33a`, checks the full RRset collection and
  exact history sequence/raw documents before commit. Eight added AFTER-trigger
  controls reproduced erroneous 200 responses before the fix, then passed
  refusal, old-state restoration and normal retry after removing the fixture.
- Frozen socket oracle: `ff7524eef4485cf3dd35ed5b5defbd600a18707c`. Execution
  stopped on listener permission, not a claimed routing failure/success.
- Recorder oracle: `c639d09d5ea4c8f6c8fbbe31bdf8532f3cbf1124`. Real production
  handler flow returned 404 for DNS creation before routing was connected.
- Routing: `2923e23be102d85245864c2fc77d7df88a8c9167`, a 15-line single-file
  change in `internal/server/cloud_storage.go`; recorder CRUD/boundaries and
  existing GCP route controls passed. Both frozen oracles remained unchanged.
- DNS-only preparation: `4e4185534fad64f58badf94ab0ff1e11e0832d02` on fresh
  main. All 17 approved paths were moved; 15 blobs were identical to `2923e23`.
  Only `scripts/factory_acceptance.py` and `scripts/test-factory-acceptance.py`
  differed, removing Azure registration/tests and preserving DNS plus SQS.

The old oracle-development ancestry temporarily contained the Azure-only
registration `154e7a74e1d5b544784a45a42c18d42af5b6aefa` (from
`69b57784a59e33d507eaae4b94d86c28ba56a7f3`). It is not an ancestor/dependency of
this DNS-only preparation. No Azure runtime or oracle is included. Existing
working branches were preserved; no Azure branch was edited to resolve routing.

The original leaf finding/claim was `9d7f02096f4b7b4b288d`, owner
`gcp-dns-worker`, with two attempts. Its approved claim base was `aaaecd2`.
This historical record is not a new claim, reset, ready declaration or current
ledger-status assertion. Later test/routing/preparation work had separately
assigned scopes. No ledger was changed by DNS-only preparation or this update.

## Exact local evidence

| Source | Existing command/result | Local evidence filename |
| --- | --- | --- |
| `5def7d7` | Leaf package tests and vet exit 0; 93.7% leaf statements; registered DNS acceptance plus verify passed | `gcp-dns-attempt2-5def7d7-tests.jsonl`, `gcp-dns-attempt2-5def7d7-acceptance.json` |
| `5def7d7` | Scoped DNS package race passed | Recorded command `go test -race -count=1 ./internal/gcp/dns`; not evidence for later routing race |
| socket oracle source | Listener blocked before HTTP: `listen tcp6 [::1]:0: bind: operation not permitted` | `gcp-dns-composition-red.jsonl` |
| `c639d09` | Recorder creation 404/notFound instead of 200; auth and existing-route controls passed | `gcp-dns-handler-c639d09-tests.jsonl` |
| `2923e23` | Recorder 4 tests, leaf tests and focused vet exit 0 | `gcp-dns-route-2923e23-evidence.json` |
| `4e41855` | `go test -json -count=1 ./internal/gcp/dns` exit 0 | `gcp-dns-only-4e41855-leaf.jsonl` |
| `4e41855` | `go test -json -count=1 -run '^TestGCPDNSHandler' ./internal/server` exit 0 | `gcp-dns-only-4e41855-handler.jsonl` |
| `4e41855` | Read-only registry/scope validation passed; 17-path scope, 15 identical blobs, clean HEAD | `gcp-dns-only-4e41855-evidence.json` |

These filenames identify local `/tmp` artifacts, not uploaded hosted CI results
or a permanent evidence archive. Scope validation does not execute all suites.
The detailed factory receipt is leaf HTTP/SQLite only; the executor's generic
printed gateway label is not evidence of routing, host auth or SDK execution.
The socket error was not bypassed or retried in a different environment.

## Remaining normal PR conditions

Full `make before-commit`, `make e2e`, whole-module race/coverage, hosted CI,
DNS finite model/mutation/registry/applicability and applicable aggregate checks
remain pending. Official SDK, real-binary socket/E2E and Terraform compatibility
are unverified. A manual/hosted workflow existing in source is not proof of
permission to trigger it, nor a substitute for a missing exact-HEAD run.
Broad implementation/merge authorization does not waive repository gates.
No PR, push, merge, tag, release or deployment has been performed by this work.
