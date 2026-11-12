# Canonical guest Run Command backend

The opt-in native factory composes the existing durable SSM command store with
its canonical VM runtime, not an in-memory command double. Current authenticated
IAM checks remain the native adapter's responsibility. The exact target lease
supplies the deployment; an operator role's owning stack, a comment or job ID
must not be substituted for target provenance.

Dispatch validates the already committed provider command and binds its complete
immutable target intent to a second durable token reservation before any worker
or guest-exec RPC. Reservation readback and exact duplicate/conflict checks are
fail-closed. Lost commit acknowledgments and restart records without retained
workers are Unknown and never replayed. Quota is 1024 retained target operations.

The backend claims the ready receipt's actual QGA client once into one command
Driver. It never redials, launches another VM or executes a host shell. The guest
command is explicitly `/bin/bash -c` with the ordered requested script lines.
Known completed commands allow later sequential commands on the same client;
concurrent busy admission is rejected before a new guest effect. No ordinary
request cancellation resets the original owner or delivery deadline.

The owner submission fence encloses actual guest-exec, not only a metadata check.
Resource seal denies later admission, closes QGA and the physical stream before
joining the submission gate. A command admitted before seal may have reached the
guest. Its lost response remains Unknown/containment debt; close or cancellation
is not proof that it did not execute or stopped. Full VM/process cleanup remains
the existing canonical lifetime owner's separate responsibility.

Only the background worker performs bounded QGA status polling and persists its
observations. Observe reads canonical durable observations only; it never polls,
reaps a guest process or resumes work. Known exit and bounded valid output produce
terminal results. Truncated output cannot produce Success. Unknown exit, control
loss and unverified cleanup never become terminal TimedOut merely from a clock.
Terminal records are immutable. Shutdown seals new reservations and joins both
pre-reservation callers and retained workers before database disposal.

## Native API scope

The configured backend exposes three bounded SSM JSON 1.1 operations:
`SendCommand` for `AWS-RunShellScript`, `GetCommandInvocation`, and
`ListCommands`. A Send request targets instances from one retained deployment.
List requires exactly one `InvokedAfter` filter and supports `MaxResults` 1–50.
Other optional fields and filters are rejected rather than silently ignored.

Send checks the standard document and every target instance against one current
role-policy snapshot. Instance tags come from the owned CloudFormation record.
This does not promise global IAM locking throughout later guest execution.
List and Get require their IAM action on `*` and additionally restrict visibility
to the same role incarnation and region. This is narrower than AWS's account-wide
command listing. Renewed temporary credentials for that role can recover its
commands; a recreated role cannot adopt them. Comments and session names grant
no authority.

Pagination tokens bind an immutable request-summary snapshot to the role,
region, filter and page size. Summaries use numeric epoch-second timestamps and
do not claim execution success. After an uncertain Send outcome, discover the
original command with List and observe it with Get; do not resubmit it.

References: [SendCommand](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_SendCommand.html),
[GetCommandInvocation](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetCommandInvocation.html),
[ListCommands](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_ListCommands.html).

Source and net.Pipe tests do not qualify actual QGA, VM execution, shell behavior,
SSM service compatibility or Battle recovery. Existing physical execution holds
remain in force; no relay/session channel is used as an alternate QGA route.

An Unknown observation while the same live actor is still publishing its retained
worker means pending and does not assert absence of effects. Lost/orphaned local
continuations instead carry CleanupRequired. The runtime singleton is the sole
process actor for its database; a second independent coordinator cannot adopt a
live reservation or use another actor's observation as cross-process liveness.
