# Contained Linux execution: acceptance boundary

Status: implementation investigation, 2026-11-08. This document does not declare
EC2, Session Manager, or Hello World Battle available. The default standalone Go
runtime contract remains unchanged. No GitHub Actions workflow substitutes for
the actual workload execution requested on the development cloud computer.

## Source workload

The unmodified `battles/hello-world-battle` fixture was inspected at
TenkaCloudChallenge `c1aaaa967031ad2bcfa78feda0ae00f1ea87b67e`:

- `template.yaml` SHA-256:
  `4de530625bd2a3c0b5a5881f511061709c94c9eb6884d8d6fd1037f3d9bc6bc5`.
- `metadata.json` SHA-256:
  `4244b9659883f51a7c4da7211d71e9b24ae8690606275938e9da05ad3f9466dc`.
- `simulation.json` SHA-256:
  `debece92ae1d5bc80a5f362b1871d9b5e1a5b5a17407ff9c200fd95c680ac885`.

The instance runs Amazon Linux 2023. Its original UserData calls `dnf update`,
installs nginx and Python, writes a frontend and `/healthz` handler, starts actual
systemd services on ports 80 and 8080, and queries IMDSv2 for the host banner.
Interpreting those commands as canned simulator responses is not acceptance.

VPC, subnet, IGW attachment, route table/route/association, SG, native IAM role
and instance profile are prerequisites. The template has no explicit NACL, but
the user requested NACL behavior: default association and bounded ingress/egress
rules cannot be omitted solely because the fixture relies on defaults.

The participant template also uses resource-tag and session-owner IAM conditions
and policy-variable expansion. Existing generic IAM wildcard matching is not a
substitute. Unsupported conditions must continue to fail closed until their
specific native action context is implemented and negatively tested.

## Separate proof from supported runtime

QEMU TCG is a possible development proof without KVM or Docker. A kernel boot,
PID 1 systemd, a real guest response, and the unchanged complete UserData are
separate milestones. An official image, its checksum/signature, exact emulator
source, guest logs, commands and cleanup evidence must be retained.

Installing QEMU in a development directory does not meet the shipped standalone
contract. No external executable or image is silently added to default startup.
A future explicit VM capability must either provide a properly reviewed,
self-contained distribution or document its opt-in runtime dependencies and
unavailability when they are absent. That packaging decision remains open.

## Required execution boundaries

- A provider-neutral machine runtime owns isolated guest execution only. AWS owns
  image IDs, instance state, network/IAM policy and native API projections. The
  runtime must not contain problem IDs, points, canned nginx responses or graders.
- Guest commands execute inside the guest. No participant-selected host command,
  environment, executable path, host filesystem mount, socket proxy or cloud
  credentials are passed through. The host control channel is not a participant
  authority source.
- Each team keeps its existing separate process, DB and token authority. Guest
  disks, control channels, metadata and listener assignments must also be distinct.
- Default guest egress is denied. The original dnf calls need a verified offline
  repository/image preparation path, not unrestricted runtime Internet access.
  Missing packages are failures, not rewritten successful shell commands.
- Endpoint traffic must pass the current instance state, route/IGW, SG and NACL
  evaluation before reaching the actual guest listener. Detached or denied paths
  cannot remain reachable through an alternate exposed forwarding port.
- IMDS is instance-bound synthetic local metadata. Its token expiry, instance ID,
  addresses and any local role credentials must never expose host credentials or
  another team's state. Permission or role-generation changes revoke authority.
- Start/stop/terminate have persisted desired and observed states. Do not label a
  booting or failed machine running. Never hold a SQLite transaction while waiting
  for a guest that needs the same state. Restart reconciles pending/failed cleanup.
- SSM control requests, actual data-channel authentication and command execution
  require independent acceptance. A returned SessionId or metadata row alone is
  not a shell. Ownership, expiry/revocation, current role permissions, disconnect,
  timeout, cancellation and bounded output must be tested.

## Original Battle acceptance

Only after the generic runtime/native APIs are implemented may the host enable
this problem. Verify all of these using the unchanged fixture and real guest:

1. Create the complete stack, execute original UserData, and return actual
   nginx `/` and Python `/healthz` HTTP 200 responses.
2. Keep both default URL outputs empty. Before registration, award no points.
3. Register both endpoints through the existing participant flow. Observe actual
   one-minute probes and +100 only when both return their expected status.
4. Execute the operator's selected-team `AWS-RunShellScript` disruption. nginx
   stops in that guest, the peer team remains healthy, and the next probe is -100.
5. Recover early through an authenticated participant Session Manager shell and
   actual `systemctl start nginx`. Separately verify the 600-second automatic
   revert, including process restart while it is pending.
6. Test route, SG, NACL, IAM and cross-team denial without partial mutation or
   continued hidden reachability. Stop/restart the guest and simulator.
7. Delete the stack and verify guest processes, listeners, disks, profile/role
   membership, IP leases and scheduled work are removed or explicitly retryable.

Full local quality/E2E/formal gates and current-commit review remain required for
publication. Bounded formal models establish neither concrete VM containment nor
AWS-equivalence. Failed, skipped and unexecuted stages must remain visible.
