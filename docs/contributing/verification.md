# Common verification receipts

This optional development adapter invokes existing Go/Python gates and records
command-level evidence. It adds no simulator runtime dependency and does not
replace the factory acceptance ledger, registered acceptance suites, independent
review, remote CI, formal checks, or existing merge policy.

```sh
make verify          # existing make before-commit, with a receipt
make verify_release  # same gate + make e2e + explicitly blocked VM acceptance
```

Python 3.10+ on Linux/macOS and the existing Go development toolchain are required.
`fast` is the ordinary lane, not a claim that the full native gate is fast. It does
not select fewer tests or hide failure. Release is an optional evidence profile,
not a new branch-protection requirement. There is no release/deploy command here.
Model changes still require the repository's existing formal verification.

The feature map in `verification.json` identifies source paths, native commands
and required environments. Paths are discovery hints, not automatic test filters.
The VM check deliberately stays blocked until a reviewed, exact-HEAD real-VM
acceptance command and its prerequisites are wired. Native API E2E is not proof
that Battle, two-team hosting or recovery works on a VM. No VM was provisioned by
this adapter, and it does not install QEMU or fetch private runtime bundles.

## Shared runner and update policy

The runner is vendored byte-for-byte from `susumutomita/typescript-template` at
commit `b434822e6bd2e80480dc8af610ad3f2a234a35e0` (initial Draft PR #127), with its
MIT notice. `scripts/verification/upstream.json` records source path and SHA-256.
`run.py` checks that digest before executing the runner. No network fetch occurs,
so an upstream branch update cannot silently change this repository's checks.
Updating the runner requires a reviewed source commit, exact file and pin update,
license review, and adapter tests in one PR. Do not edit the vendored file locally
without reviewing the corresponding upstream change. This is reproducibility,
not protection from a malicious author who can change both source and pin.

## Interpreting receipts

Each run writes a new directory outside the checkout and prints its location.
`receipt.json` records selected commands, exit codes, logs and hashes, runner/map
hashes, and repository HEAD/dirty state before and after the run. Log content may
contain application data; inspect and redact it before sharing. Nothing is uploaded.

`passed` means the selected command returned zero. Missing tools are `blocked`,
nonzero exits/timeouts are `failed`, and interrupted remaining checks are `not-run`.
Overall success also requires an unchanged clean checkout and unchanged runner/map.
A dirty run still retains command results but cannot attest a clean commit. Commit
the candidate and rerun. Evidence is not cryptographically trusted proof that a
command tested its stated behavior. Review the assertions and toolchain provenance.
Existing factory reports remain the stronger test-level acceptance evidence.

The wrapper's own tests run in `make test-scripts`, without requiring VM fixtures.
