# NIC/session bookkeeping correspondence

Scope is deliberately narrow: `internal/workload/guestnet/tcp_sessions.go`.
The model is a finite logical bookkeeping abstraction, not physical authority,
TCP sequence/state correctness, or Go refinement.

## Concrete correspondence

- `Open`: immutable `records[allocated]`, `allocated++`, `live++`, port
  `ProbeSourcePort + allocated`; tuple tombstones survive cancellation/drain.
- `RouteIncoming`: charge frames/bytes before parsing/classification, reject
  unknown or malformed tuple/direction, discard known retired tuples, enqueue
  only to exact active session, close registry on queue overflow.
- `ReadFrame`: one read per session; inflight debt remains across revocation;
  buffered frames are not returned after observed revocation.
- `Transmit`: charge before busy check, exact route validation, one registry-wide
  callback, inflight retained until callback returns, late result rejected.
- `closeLocked`: close admission and purge queue without claiming callback
  completion. `finishLocked` requires watcher completion and zero inflight.
- Registry `finishLocked`: wait for zero live sessions and cancellation watcher.
- Rewrapping/discovery is stuttering over the existing registry, not permission
  to construct a replacement registry with fresh counters.

`ok` abstracts the conjunction of valid parsed tuple and trusted direction.
This does NOT prove ParseTCPFrame, checksums, ARP, byte slice ownership,
classifier correctness, gVisor, or actual policy/physical delivery authority.
`effectPossible` records that a delivery callback was admitted. It is never
cleared by cancellation or logical drain. No invariant claims that revocation
recalls or prevents effects of an already-admitted callback.

## Bounds and seeds

Two lifetime tuple slots, two active sessions, one pending callback, one queued
frame per session, three cumulative frames, four abstract byte units, and
three-step suffixes from eight explicit reachable seeds. These stand for much
larger implementation limits; no arithmetic scaling proof is claimed. Seeds:
empty; one open; two open; one queued incoming frame; one admitted callback;
revoked/watched pending callback; drained first tuple; and registry-closing
pending read. Each seed is obtained by a stated Open/Receive/Send/Revoke/Watch/
Drain prefix. This is bounded transition exploration, not unbounded safety or
liveness proof. Timers/context errors are abstracted by revocation actions.

## Excluded state and remaining obligations

- `guestnet/frame.go`, `packet.go`, `arp.go`, `tcp_routes.go`: parser/encoder and
  classifier correctness remains native-test and separate model work.
- `guesttcp/engine.go`, listener and gVisor: TCP state, deadlines, close/reset
  semantics and module behavior are not modeled here.
- `transport_persistent.go`: queue/selected-write ownership has similar concepts
  but different locks, pump joins and shared services; source correspondence is
  diagnostic only, not automatic registry binding to this model.
- Physical owner, atomic NIC revocation/publication fencing, actual VM/QGA/SSM
  and operating-system/process containment remain unqualified/held.
- `go.mod`/`go.sum`: keep existing WAF obligation; new TCP dependency semantics
  and provenance are separate from this bookkeeping model.

## Reachable seed prefixes

All operations below use session 1 unless specified. Receive/Send use a
matching tuple and one byte unit. The two-service case allocates session 2.

| Seed | Prefix from Empty |
| --- | --- |
| Empty | empty prefix |
| One | Open(web) |
| Two | Open(web), Open(health) |
| Queued | Open(web), Receive |
| Writing | Open(web), Send |
| ClosingWrite | Open(web), Send, RevokeSession, WatchSession |
| Retired | Open(web), RevokeSession, WatchSession, DrainSession |
| ClosingRead | Open(web), ReadStart, RevokeNIC, WatchNIC, WatchSession |

These prefixes establish model reachability, not concrete Go execution. The
registered model explores three-transition suffixes from these eight states;
it is not an exhaustive exploration from initial allocation, a full TCP model,
or a proof of the implementation.

## Regression boundary

The registration regression pins all 16 properties, 20 semantic controls,
the single concrete source scope, and declared compact bounds. Model controls
must fail their intended invariant after a real transition, not from an
inconsistent initial state or syntax error. Changing model bounds or seed
definitions requires corresponding review of the reachability prefixes.

The normal formal suite also requires prefix replay through
scripts/formal_seed_replay.py. The support harness/config live in
scripts/formal_support, outside the registry-only formal directory. The runner
copies the exact allowlisted registered dependency with the harness, checks
their pinned hashes, and checks SeedTargetReached and PrefixHasNext from Empty.
It also requires a semantic negative control with a deliberately wrong terminal
target to violate SeedTargetReached after a transition. Missing support files,
changed dependency bytes, timeout, or replay failure prevent suite success.

Replay uses the same verified TLC jar as the ordinary models and immutable HEAD
bytes in merge mode. It supplements rather than replaces the registered
baseline, semantic controls, and final immutable-input check. This is still
bounded model reachability, not concrete Go refinement or physical acceptance.
