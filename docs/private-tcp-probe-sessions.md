# Private TCP probe sessions

`guestnet.TCPProbeSessions` separates short logical TCP exchanges from the lifetime
of one private NIC incarnation. It owns only memory, timers and logical admission.
It does not open, attach, close or qualify a physical NIC, byte stream or VM.

## Fixed identity and finite resources

The trusted caller fixes the two IPv4 addresses, Ethernet addresses and up to eight
service ports. Incoming frames cannot teach the registry a destination. Each
successful `Open` takes a source port from the fixed range 40000-41023 (1024
tuples). A tuple is reused only after its session has drained and a further
`TupleQuarantine` of four minutes (TCP 2*MSL) has passed, so a delayed segment of
the old connection cannot reach its successor. If no tuple is free, `Open` returns
`ErrProbeCapacity`; this is temporary. The sustained rate is about 1024 probes per
four minutes per NIC, which is enough to keep probing for a whole battle.
The caller must retain the same registry for the entire NIC incarnation.
Creating another registry while old packets or work can survive does not safely
skip the quarantine.

At most eight registries and eight live sessions per registry are admitted.
Unfinished local work retains its capacity. Each session has a copied-frame queue
of 16 entries and permits one pending reader. A registry permits one synchronous
transmit callback; concurrent transmit attempts return `ErrProbeBusy` rather than
queueing. Each tuple incarnation has its own budget of 8192 frames and 2 MiB for
incoming traffic and admitted outgoing attempts, including its retired traffic and
busy attempts; exceeding it closes the whole registry. Frames for an unknown tuple
also close the registry. A reused tuple starts a fresh budget.

The registry captures an absolute expiry, no later than 24 hours or its parent's
deadline. Each probe is limited to ten seconds, its parent's original deadline and
the registry's expiry. Timers and admission checks both enforce those original
limits. Passing a context with delayed cancellation notification does not extend
its advertised deadline. These are upper bounds, not a promise that any physical
runtime supports a day-long incarnation.

## Isolation and retirement

`RouteIncoming` validates the complete TCP frame and the exact reverse tuple,
including addresses, ports and MAC addresses. It copies valid active traffic into
that session's queue. Traffic for an exact retired tuple is discarded and charged
to the cumulative budget. Malformed, unknown, forged or overflowing traffic revokes
the registry. A TCP engine still owns sequence numbers, handshake validation and
TCP state; tuple routing is not a TCP implementation.

Session `Revoke` cancels its admission and discards queued input. Other sessions
can continue unless a shared transmission has an uncertain outcome. Registry
`Revoke` cancels all sessions without waiting for a callback. `Wait` observes local
work completion with a bounded caller deadline, at most five seconds. A timed-out
wait does not release capacity. Successful registry `Wait` returns only after its
memory admission slot has been released. Neither method proves physical cleanup.

`Transmit` validates the session's outbound tuple, copies the frame and invokes
the supplied callback at most once. The callback must finish its work before
returning. Failure, panic, cancellation or expiry during delivery revokes shared
logical admission because a partial write may have occurred. A blocked callback
retains capacity until it actually returns, even after revocation. Late success is
rejected and does not authorize retry. A caller must finish admitted writes before
normal per-probe cancellation if it wants sibling probes to remain usable.

## TCP engine composition and remaining boundary

`guesttcp.Config.SourcePort` accepts a trusted caller-selected port. Zero preserves
the existing default, 40000. Engine binding, frame checks and routed transport
classification use the captured effective port. The caller remains responsible
for uniqueness across engines; the session registry supplies that property only
within its own incarnation.

The memory composition test runs two concurrent HTTP services twice against one
independent gVisor peer. It demonstrates tuple allocation, routing and retirement
without replacing the peer between probes. It does not exercise an OS socket, VM,
physical stream, provider policy or scorer endpoint.

A production owner still needs a bounded persistent physical reader/writer,
ARP/IMDS multiplexing, and atomic delivery fencing against owner and policy
revocation. It must join admitted writes before claiming physical drain and retain
whole-domain cleanup debt across crashes. Context checks cannot recall an entered
write or eliminate a check-to-write race. This registry is not a `FrameCommitter`,
and the existing per-observation `Transport` must not be wrapped with a no-op
`Close` to pretend that it owns a persistent NIC. No runtime capability is enabled
by constructing these objects.
