# Bounded guest command control

`internal/workload/qga` is an internal, provider-neutral QEMU Guest Agent (QGA)
protocol component. It is **not an EC2 or SSM runtime**, a VM launcher, a process
sandbox, or evidence that Hello World Battle runs. No provider, public API,
capability registry, default runtime, or dependency is changed by this component.
The single-binary runtime still requires no externally installed runtime.

## Authority and transport ownership

A future trusted lifecycle controller must supply an already-authorized,
exclusive, private `Transport` to `New`. The component never creates or discovers
a socket, network connection, file, host process or VM. It exposes no arbitrary
QGA method, file transfer, guest shutdown, network configuration or PTY/session
interface. `StartCommand` maps only to `guest-exec`; `CommandStatus` maps only to
`guest-exec-status`. Commands have a guest-local path, arguments and optional
binary stdin. The module does not insert a shell or set an environment.

The controller, not a guest or participant, chooses the transport and command
authority. AWS identity, IAM/SSM authorization, team/lease binding, lifecycle
isolation, command admission and guest privileges remain outside this module.
A private channel is not proof that a guest or its reported status is trustworthy.
Guest-controlled output and error descriptions must remain untrusted data.

`New` takes exclusive transport ownership even when initialization fails. The
transport must implement concurrency-safe `Close`/`SetDeadline`, prompt unblocking
on close, and deadlines that bound reads and writes. The client cannot enforce
those properties on an arbitrary implementation. No other reader/writer may
share the stream. Clients must not be copied; handles are in-memory capabilities,
not persistent or transferable identifiers. The caller must not mutate command
input while a call is using it.

## Protocol and bounded behavior

The implementation follows the command subset in the
[QEMU 11.0.0 QGA schema](https://github.com/qemu/qemu/blob/v11.0.0/qga/qapi-schema.json).
That schema is a protocol reference, not an included runtime or real-guest test.

- Initialization sends a `0xFF` parser-reset byte and `guest-sync-delimited` with
  a fresh cryptographically random nonnegative int64 echo. It accepts only a
  delimited reply matching both the echo and request ID, discarding bounded stale
  bytes/replies first. A new sentinel resets a partial frame.
- Every normal response must match the one outstanding request ID. Malformed
  JSON, duplicate/unknown fields, missing/null fields, stale/future IDs, conflicting
  return/error envelopes and invalid result types are rejected. There is no
  automatic retry. The client permanently closes after transport or protocol
  failure, rather than reusing or resynchronizing a timed-out stream.
- Calls are serialized. The per-call hard timeout includes waiting for the RPC
  slot and is the earlier of the caller's deadline and the configured duration.
  The default is 2 seconds; the maximum is 5 seconds. A queued cancellation sends
  nothing and does not disrupt the active call. Active cancellation closes the
  channel and completes its callback before releasing the RPC slot.
- There are at most 32 active, unreaped handles per client. Callers cannot pass a
  raw PID. Handles are bound to their original client; all copies become invalid
  when exit is observed or the agent explicitly rejects a status request. A new
  command can reuse a reaped PID only with a new handle. Duplicate active PIDs
  fail closed. Closing the client invalidates all subsequent operations.
- Output is exposed only as a complete exited result with exactly one exit code
  or signal. Both preserve the full signed int64 QAPI value, including Windows
  high-bit exit/exception codes stored in signed `gint` by
  [QEMU 11.0.0](https://github.com/qemu/qemu/blob/v11.0.0/qga/commands.c).
  For nontruncated output, invalid/noncanonical base64 and combined decoded
  output overflow fail closed. Either truncation flag returns `ErrOutputTruncated`, no partial
  result, and a retired handle. A well-formed truncated result does not itself
  poison the channel. Pending status cannot contain output or exit metadata.

Fixed limits, including unsuccessful attempts:

| Quantity | Limit |
| --- | --- |
| Guest path | 4 KiB; nonempty valid UTF-8, no NUL |
| Argument count | 64 |
| Combined path and argument bytes | 64 KiB; valid UTF-8, no NUL |
| Binary stdin | 256 KiB |
| Encoded request, including framing | 512 KiB |
| Response frame, excluding newline | 1 MiB |
| Entire initialization scan, including stale data/sentinels | 2 MiB |
| Combined decoded stdout and stderr | 256 KiB |
| Active command handles | 32 |

The serialized request limit is also checked after JSON escaping/base64 encoding.
A request rejected before transmission leaves the channel usable. All reads and
writes, including synchronization and malformed/partial streams, have deadlines.
These bounds limit this client's resources, not the guest agent's own output
buffering or guest process resource use. There is no hidden unbounded RPC queue;
callers waiting for the serial slot are bounded by their own call timeout.

## Uncertain starts and process lifetime

An explicit, correlated QGA `error` is a `RemoteError`. A failed start after any
request bytes were transmitted without a trustworthy outcome is instead a
`StartUncertainError`: **the command may have started and may still be running**.
The underlying timeout, protocol or IO error is available through `errors.Is` /
`errors.As`. Partial writes are treated conservatively as uncertain.

An RPC timeout, context cancellation, closed channel, unsuccessful status check,
or `Client.Close` does **not** cancel a guest process. The API intentionally has
no `Run` method that would imply such a guarantee. Reconnecting and retrying an
uncertain command could execute it twice. Lost status responses may also have
reaped QGA's record, so they cannot be retried safely through a raw PID.

Before this component can be used as a runtime, the lifecycle owner must enforce
actual command/VM time and resource budgets and stop/reclaim the VM or implement
real, bounded guest-process cancellation on uncertain outcomes. Close alone is
not containment. Pending commands can occupy all 32 handles until reaped or the
client is discarded; admission is not a process-count limit in the guest.

## Command observation driver

`internal/workload/guestcommand` is the next internal adapter above QGA. It binds
one supplied QGA client to noninteractive command observation. It does not parse
AWS documents, select an instance, authorize a caller, create a transport or
launch a guest. The trusted provider supplies the guest command and an explicit
observation timeout. The original Battle's RunShellScript stop/revert commands
can eventually use this seam, but interactive StartSession remains a separate,
unimplemented PTY/data-channel requirement.

The lifecycle owner transfers **exclusive** use of the QGA client to one driver,
bound to one immutable guest/lease generation. Sharing it with another driver,
reading/writing its transport elsewhere or swapping its target is outside the
contract. One active command is admitted; concurrent starts/polls are rejected
rather than queued. The driver never exposes a PID. Finished handles retain only
their own immutable observation and cannot address a reused guest PID.

`Start` reports the QGA outcome, not a fabricated running instance:

- `Accepted` means QGA accepted a command handle. It does not prove the process
  is currently alive or that its side effects succeeded.
- `Rejected` means QGA did not accept the command, including explicit remote
  errors and requests proven unsent. These do not assert a guest-cleanup
  obligation. A broken channel may still retire the driver.
- `Exited` contains the observed signed exit/signal value and complete bounded
  output. Exit is not necessarily success; the provider must interpret it.
- `OutputUnavailable` records QGA-observed exit with truncation, without exposing
  partial output or inventing an exit code. It is not a successful invocation.
- `Uncertain` has sticky `CleanupRequired`, with a reason. It never means that a
  process was stopped, killed or successfully cancelled.

The observation deadline begins before dispatch and is capped at one hour. Its
in-memory timer closes the control channel even if nobody polls. While Start is
still in flight, timeout/Close does not guess whether bytes were sent: QGA's
actual start-error classification determines rejection versus uncertainty.
Accepted execution, uncertain dispatch, ambiguous status, deadline expiry or a
cancellation request can require real guest cleanup. Quarantine permanently
refuses new commands. There is no reset, automatic retry, or false cleanup-ack API.

Polling is explicit, with at least 100 ms between RPC starts, no more than 1,024
status attempts per command and QGA's existing per-RPC hard timeout. A caller
must choose a cadence that fits the observation budget; the adapter does not run
a hidden polling loop. Rate-limit rejection sends nothing and preserves the
active command. Exhausting the attempt budget quarantines the driver. A delayed
timer or cancellation for a completed job cannot affect its successor.

`RequestCancellation` intentionally returns a cleanup obligation for an active
command. It only closes the channel. The lifecycle owner must stop/reclaim the
bound VM/lease or implement verified process-group cancellation, including child
processes, and persist that reconciliation separately. The timeout is **not a
guest-process runtime limit**. A command can fork children that outlive its QGA
record; this adapter supplies no CPU, memory, disk or process containment.

Before dispatch, the native provider must durably record intent and target
generation. It must persist accepted/uncertain/terminal observations, reconcile
in-flight work after restart, enforce IAM/SSM document/target rules, and never
retry an uncertain command blindly. Driver handles/timers are not durable or
restart recovery tokens. The 600-second Battle revert also needs independent
persistent scheduling. None of these provider/lifecycle integrations is supplied
by this adapter, and no simulator capability is enabled by its presence.

## Validation and remaining gates

Tests use only `net.Pipe` and injected in-memory transport faults. They cover
initial sync/reset/stale-data bounds, strict schemas and correlation, byte-limit
boundaries, remote versus uncertain outcomes, PID/handle lifetime, output
truncation/base64 validation, queue cancellation, blocked IO and concurrent close.
The driver tests additionally verify exact command forwarding, observed outcomes,
request-not-sent versus uncertain dispatch, deadline/cancellation obligations,
exclusive admission, bounded polls, stale-timer fences and PID-free handle reuse.
They do not boot a guest, create an OS socket, start a host process or use a
network transport.

In-memory protocol checks are not real QGA interoperability, EC2/SSM behavior
or Battle acceptance.

Before integration/merge, independent exact-candidate review and the repository's
aggregate gates remain required. Before any runtime capability can be enabled,
the following must additionally be demonstrated with an authorized actual guest:

1. Transport and VM lifecycle integration, authority isolation and bounded
   cleanup after crashes, timeouts, uncertain starts and controller restart.
2. Actual guest-agent synchronization, command execution, status/output behavior,
   process resource limits and real cancellation/termination guarantees.
3. Native EC2/SSM state and authorization, unchanged original Battle initialization,
   networking, operate/grade/recover/cleanup, and multi-team isolation.

No current runtime support or successful Battle lifecycle is claimed here.
