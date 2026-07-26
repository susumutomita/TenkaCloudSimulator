# Agentic development contract

TenkaCloudSimulator participates in the TenkaCloud Symphony fleet as an independent repository. Symphony creates one isolated workspace per GitHub Issue and must operate only inside that workspace.

## Completion gate

The machine-readable completion contract is:

```bash
make agent-gate
```

The target delegates to the existing `before-commit` gate. That gate owns the architecture harness, harness tests, pre-release checks, duplication ratchet, prose and code linting, type checking, tests, coverage, and production build. New mandatory checks belong in `before-commit`, not in an agent-specific private command.

A green gate is required before a pull request is opened or updated. An agent must fix the implementation instead of weakening Biome, TypeScript, test, coverage, duplication, or architecture rules.

## Repository boundary

The Simulator implements generic cloud capabilities and reports the capabilities it supports.

- It must not know TenkaCloud problem IDs.
- It must not add branches for one problem or scenario.
- TenkaCloudChallenge declares the capabilities a problem requires.
- TenkaCloud compares required and implemented capabilities through the compatibility contract.
- The platform integrates the Simulator through the thin `ProblemRuntimeAdapter` boundary.

## Autonomous workflow

1. Read the Issue and extract acceptance criteria.
2. Record a reproduction or current-state signal before editing.
3. Inspect existing capability and runtime abstractions before adding code.
4. Keep the change inside the Issue scope.
5. Add behavior-level tests.
6. Run `make agent-gate` until it passes.
7. Review the complete diff for correctness, isolation, cleanup, race conditions, API compatibility, and accidental problem-specific behavior.
8. Put acceptance criteria, risk, validation evidence, and cross-repository impact in the pull request.

## High-risk changes

The following require human review and must not be auto-merged:

- capability names, meanings, negotiation, or compatibility semantics;
- public runtime APIs;
- world, deployment, cleanup, or resource-lifecycle behavior;
- authentication, authorization, credentials, or network boundaries;
- release, packaging, native dependencies, or deployment workflows;
- `GNUmakefile`, `Makefile`, agent guidance, harness rules, dependency manifests, or lockfiles.

## Prohibited actions

The autonomous workflow must not run deploy, destroy, release, production cloud, force-push, or secret-management commands. It must not access production credentials. A missing external credential is a blocker, not a reason to bypass a gate.
