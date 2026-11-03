# Real TenkaCloud local integration

hello-world.ts imports the existing **real** TenkaCloud catalog acquisition, HostingService, HostStore and CloudFormationEngine. It starts two actual Go executables, distinct SQLite files and token authorities, then uses AWS SDK HTTP calls. Scoring is the platform's submission/ledger implementation, not a substitute presence check.

It is a development integration runner, not a production host adapter or native STS. The explicit synthetic STS routing seam selects a process by simulated team account, strips its routing placeholder before HTTP, and never contacts AWS. Native federation/CLI credential routes are unavailable. Only hello-world is selectable; the battle is filtered out. Original problem metadata/templates are not modified.

Reviewed versions for this run:

- TenkaCloud: f99b3d9a00076a2a5e97f1d4d9b0b20a7e02245c
- TenkaCloudChallenge: a749b142416770abee3f173673702c55df3856aa
- Simulator base: 639af7a9e7384dcc0d6fdf42d05024f03ab75349 plus local working tree

With the platform's locked development dependencies available and Bun 1.4.4:

```sh
make build
TENKACLOUD_ROOT=/path/to/TenkaCloud CHALLENGE_ROOT=/path/to/TenkaCloudChallenge bun test/platform/hello-world.ts
```

The runner uses a disposable temp directory, the real hash/sealed catalog snapshot installer, host bootstrap and authenticated HTTP APIs. It creates/deploys/schedules an event; obtains the SSM flag with a participant-bound identity; checks private-output redaction and 403, cross-process participant/operator tokens 401, unique flags, other-team answer wrong with penalty, eight concurrent correct answers with exactly one award, final scores 100 and 95, and DeleteStack/DELETED. It destroys temporary state and stops children even on failure. Output never includes credentials or flags.

No AWS config, real credentials, catalog scripts, Docker, native federation or public exposure are required. Existing problem instructions still describe real AWS; a future production host adapter must present explicit local console/endpoint instructions and authenticated team routing before this is offered as an ordinary event.

## Participant-driven practice

```sh
SIMULATOR_PRACTICE=1 TENKACLOUD_ROOT=/path/to/TenkaCloud CHALLENGE_ROOT=/path/to/TenkaCloudChallenge bun test/platform/hello-world.ts
```

This imports/deploys/schedules the real hello-world automatically, then stays alive
for participant operations. Open the printed loopback portal. The two printed
**file paths**, not tokens, identify private 0600 JSON files with each team's
teamKey, local consoleUrl and participant consoleToken. Give each team only its
own file. The simple development portal explains the local workflow and submits
to the real authenticated platform API; it is not an AWS-console imitation.
Open the Simulator's actual Cloudscape Systems Manager UI, choose ap-northeast-1,
read the value, then submit in the practice portal. No expected answer is printed.
Ctrl-C tears down stacks, closes listeners/children and removes temporary files.
This is a disposable ten-minute local rehearsal, without production team proxy,
resume, token renewal or AWS federation. Do not publish these listeners.

CI and clean local setup can use the minimal development-only locked tools:

```sh
npm ci --prefix tools/platform --ignore-scripts --no-audit --no-fund
node tools/platform/setup.mjs /path/to/fresh/TenkaCloud
export BUN="$PWD/tools/platform/node_modules/@oven/bun-linux-x64/bin/bun"
export TENKACLOUD_ROOT=/path/to/fresh/TenkaCloud CHALLENGE_ROOT=/path/to/TenkaCloudChallenge
"$BUN" test/platform/hello-world.ts
/tmp/tcsim-tests/bin/python test/platform/browser.py
```

The checked-in tooling Bun binary package targets Linux x64 CI. Other development
hosts can use their own Bun 1.4.4 with the same platform dependencies. setup.mjs
creates resolution symlinks only if absent or already correct; it never replaces
an existing node_modules or platform source. Browser coverage drives both teams'
real Cloudscape login, Services/region navigation, SSM Overview value read and
practice portal submission, checks 100/95, no external requests, mobile width and
stack cleanup. Flags/tokens are never printed.
