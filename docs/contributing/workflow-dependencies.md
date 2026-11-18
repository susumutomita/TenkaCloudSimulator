# Maintain workflow dependencies

The official GitHub Actions dependency lockfile at
`.github/workflows/actions.lock` records action commits and source repository
identities for each onboarded workflow, including transitive action dependencies.
The generator converts existing action SHA references to their release tags
while retaining the same commits in the lockfile. Generate the lockfile with the
official CLI; never edit it by hand.

## Update the lockfile

From the repository root, run:

```sh
gh extension install github/gh-actions-lock --pin v0.4.1
gh actions-lock
gh actions-lock --verify
git diff -- .github/workflows
```

If the extension is already installed, check `gh actions-lock --version` and use
the version pinned in `verify-actions-lock.yml`. Review the entire generated diff.
Commit workflow edits and the generated lockfile together. Run these commands
after adding, removing, or changing workflows or action dependencies, including
dependencies inside composite actions. The default command onboards every workflow.

The dedicated verification workflow runs on every pull request, main push, and
merge-group event, and supports manual execution. It uses read-only repository
permissions and `gh actions-lock --verify`; it never commits fixes. The check
fails for missing workflow coverage, stale entries, or invalid upstream pins.
For a fast offline coverage check, run `gh actions-lock --verify-local`.
The offline check does not verify upstream reachability or repository identity.

Dependabot checks GitHub Actions dependencies weekly and updates matching lockfile
entries for workflows that are already onboarded. It does not onboard new workflows.
Existing `CODEOWNERS` coverage includes the lockfile and workflows.

## Review dependency changes

A changed `commit` changes the action code that runs. Review upstream changes
before accepting it. Investigate any changed `owner_id` or `repo_id`.
For a moved branch or partial version, the default command retains its locked
commit. After reviewing upstream history, use `gh actions-lock --relock` to advance
it. Do not automate `--accept-moved`; rewritten history requires a separate review.

Calls to reusable workflows (`jobs.<job_id>.uses`) are not action dependencies.
Keep external reusable workflow calls pinned to a full commit SHA separately.
Their actions are covered by the source repository's own lockfile. Same-repository
calls in `release.yml` continue to use the caller's commit.

## Check rollout prerequisites and limits

GitHub's [October 28, 2026 GA announcement](https://github.blog/changelog/2026-10-28-github-actions-workflow-lockfiles-are-generally-available/)
includes Free, Team, GitHub Enterprise Cloud, and Enterprise Cloud with data
residency. The CLI supports github.com and *.ghe.com; GitHub Enterprise Server
is unsupported. If an organization or enterprise restricts allowed actions,
confirm that the locked commit SHAs are allowed: tag-only allowlist entries do
not match onboarded pins.
This change does not modify repository, organization, or enterprise settings,
rulesets, required checks, or security policies. An administrator can evaluate
the separate Require lockfile policy after confirming coverage and compatibility.

The lockfile fixes action code resolution. It does not replace npm dependency
audits, Go dependency checks, runner Git security updates, or runner-image
maintenance. It does not pin downloads performed by scripts, CLI extension release
assets, container images, workflow inputs, or secrets. Continue the existing
security and application checks independently. The verification CLI is installed
at the explicit official release version shown above.

See GitHub's [lockfile generation guide](https://docs.github.com/en/actions/how-tos/secure-your-work/lock-workflow-dependencies),
[lockfile concepts](https://docs.github.com/en/actions/concepts/security/workflow-dependency-lockfiles),
and [enforcement guide](https://docs.github.com/en/actions/how-tos/administer/enforce-workflow-lockfiles).
