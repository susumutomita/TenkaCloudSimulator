# Development status

Current assignments, decisions, blockers and verification results belong in the
relevant Issue or PR, not in a second tracked roster.

- [Issues](https://github.com/susumutomita/TenkaCloudSimulator/issues) track scope,
  dependencies and acceptance criteria. Check open and closed work before claiming
  a task.
- [Pull requests](https://github.com/susumutomita/TenkaCloudSimulator/pulls) identify
  proposed changes, reviews and merge state.
- [CI](https://github.com/susumutomita/TenkaCloudSimulator/actions) records checks
  for specific commits. A focused pass or an older successful run does not
  validate a newer candidate; verify post-merge main separately.
- [Service documentation](../README.md#find-a-service-guide) defines supported
  operations and limits. An open Issue, registered service or successful API call
  is not evidence of complete service or original-problem support.

Use the [parallel development rules](contributing/parallel-development.md) for
ownership, handoffs and integration. Preserve existing branches, uncommitted work,
security boundaries and third-party provenance. Publishing a status update,
merging or releasing still requires the relevant authorization; this page grants
none and does not lift a stopped action.

Durable contracts remain in the [service integration design](design/0005-parallel-service-contracts.md),
[contained Linux design](design/0011-contained-linux-execution.md),
[release guide](releases.md) and [formal validation guide](formal-validation.md).
Original problem bytes, real execution, two-team isolation, grading, recovery and
cleanup remain acceptance requirements where applicable. Metadata or standalone
guest checks do not enable the full Battle or WP lifecycle.

Historical source-specific snapshots remain available in Git history. They are
not current assignments, authorization or verification for a new commit.
