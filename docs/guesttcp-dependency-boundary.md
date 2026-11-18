# Optional guest TCP dependency boundary

The module files select executable dependencies; they are not metadata-only. `go.mod` and `go.sum` retain their existing `WAFRequestEvaluation` obligation and additionally participate in `EngineLifecycle` and `ListenerLifecycle` as dependency selection for these two adapters. Those models check bounded wrapper lifecycle transitions under explicit external-library assumptions, not gVisor implementation refinement, protocol compliance, dependency security, physical cleanup or authenticated authority. No model properties, controls or finite bounds change with this binding.

## Reviewed pins

All four version and content checksums are pinned together. Replacement or exclusion directives require dependency-boundary review; the guard conservatively rejects any such directive. A changed pin must not be accepted merely by rewriting a hash: recheck the source assumptions, resolved module graph, provenance, notices, native tests and vulnerability status. Source checksum consistency alone is not a current security assessment.

```
gvisor.dev/gvisor v0.0.0-20260909235104-1db01ca840f4 h1:IyKvejmukmnokiyneuLVIMiFYlnRJEy2cceShDCA/3Y=
gvisor.dev/gvisor v0.0.0-20260909235104-1db01ca840f4/go.mod h1:8aLQqUBHDH8fY5y60lzmwDpMMbQCcT3EBfoSwhfaGCY=
github.com/google/btree v1.1.2 h1:xf4v41cLI2Z6FxbKm+8Bu+m8ifhj15JuZ9sa0jZCMUU=
github.com/google/btree v1.1.2/go.mod h1:qOPhT0dTNdNzV6Z/lhRX0YXUafgPLFUh+gZMl761Gm4=
golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc h1:TS73t7x3KarrNd5qAipmspBDS1rkMcgVG/fS1aRb4Rc=
golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc/go.mod h1:A+z0yzpGtvnG90cToK5n2tu8UJVP2XUATh+r+sfOOOc=
golang.org/x/time v0.15.0 h1:bbrp8t3bGUeFOx08pvsMYRTCVSMk89u4tKbNOZbp88U=
golang.org/x/time v0.15.0/go.mod h1:Y4YMaQmXwGQZoFaVFk4YpCt4FLQMYKZe9oeV/f4MSno=
```

## Adapter correspondence and assumptions

- `engine.go` constructs an IPv4/TCP gVisor stack and owns constructor reservation, single-use Dial, endpoint registration before completion, deadline mutation/Close joins, stack destruction, worker and capacity release. `EngineLifecycle` treats external calls and their return as events. It does not prove scheduler fairness, connection validity at caller return, external cleanup success, or all caller-context interleavings.
- `listener.go` constructs the passive stack and owns bounded attempt/connection accounting, Accept registration before completion, expiry, owned connection joins and stack destruction. `ListenerLifecycle` preserves retained attempts and return debts. Stack TCP uniqueness/peer-IP semantics are assumptions, not consequences of wrapper port checks.
- gVisor `stack.Destroy` calls `Close` and `Wait`. The pinned implementation warns that concurrently created/modified endpoints can escape these operations. The adapters' Dial/Accept completion joins before Destroy are therefore required, not optional implementation details. The model assumes destruction returns according to its external contract; it cannot independently prove every goroutine stopped.
- `gonet` stores timer fields for deadlines. Zero deadline resets timers; `TCPConn.Close` closes its endpoint without itself clearing all timer fields. Adapter timer-reset calls and their return are distinct from connection Close. Ignored reset/Close errors establish return/join observations, not successful physical cleanup.
- Channel endpoint Close closes/drains the outbound queue. Inbound delivery uses the currently attached dispatcher; channel Close alone is not an inbound fencing proof. Detachment and adapter admission synchronization remain separate requirements.
- `github.com/google/btree` is used by the TCP SACK scoreboard; `golang.org/x/time/rate` by TCP/stack rate handling; buffer/chunk reaches `pkg/bits`, whose template uses `golang.org/x/exp/constraints`. Their algorithmic, concurrency and parsing behavior is not modeled by the wrapper lifecycle models.
- Module selection must be checked with the actual root graph. In particular, root `golang.org/x/sys v0.44.0` exceeds gVisor's upstream requirement; the upstream graph is not a substitute for the application's selected graph.

Native correspondence includes engine construction/configuration, single-use Dial, owned connection cleanup, pending Dial and deadline/Close races; listener native tests cover tuple churn, aggregate bounds, expiry, Accept/Close races and retained cleanup. These tests complement rather than prove the external assumptions. No synthetic ValidDependency state is used to waive runtime obligations.

## Source anchors (SHA-256)

Adapter files:

- `internal/workload/guesttcp/engine.go`: `a65ecd959d6f1fa8ac0e6b813209f03678dc69735127577f8072ee917c3b3e31`
- `internal/workload/guesttcp/listener.go`: `6af08dbb6225309e2599e5d5fef415dfa5cee56393636c5e98e6095a9cf16e96`

Pinned gVisor source (commit `1db01ca840f442dd546a1824ce8de1c01c26b017`):

- `go.mod`: `2003903d9f23d5bcc09493b6e3178f3224fbdad74ba12e3adf3699637285120c`
- `go.sum`: `5d3c42a2456135454d8db74aeed5f20f2ef3f6f7cc5a5d652c07a6c871fb3ba4`
- `pkg/tcpip/stack/stack.go`: `78aa55366d9120de380c98193b40ab35f7bbcdf77791ad68e037d04416953f3b`
- `pkg/tcpip/link/channel/channel.go`: `8c1ae6b1da82c3e69cefaad66eeb4c60cb11a2162cdce50bec7e7c4d29dd2b20`
- `pkg/tcpip/adapters/gonet/gonet.go`: `700235f948cc7e363057e61ebf7d5186ea9fbfcc08d5abdea3b03f16879e809d`
- `pkg/tcpip/transport/tcp/sack_scoreboard.go`: `1a69fe4333ffd09c51af4ffe9322bd1d79cad975930e06be1d2fcd3a73a94fef`
- `pkg/tcpip/transport/tcp/dispatcher.go`: `d28c508a0a33e18da4235849cb14862f1241aec7364608510a1564535a55560a`
- `pkg/buffer/chunk.go`: `3ae0d4ca7facc4a7322df650b60788c392ffe6cc499c52a4f098778dfacda0fe`
- `pkg/bits/bits_template.go`: `7a6f1391c2ed049b30913d4edfdf7c4f977e01585bcd599a4447f467ee20f3a7`

The anchors identify the source behind the reviewed boundary assumptions, not an upstream authenticity attestation. The provenance record and Go module sums remain separate checks. Current advisory results require a dated scan and database context; model success, a patched Go toolchain and notice consistency do not establish absence of vulnerable dependencies.

## ARM64 race CAS repair

The selected generated Go export contains the [upstream CAS operand repair](https://github.com/google/gvisor/commit/1b2946c4fa59c1a64643bce962140b5686062601). The older ARM64 race assembly loaded both the expected and replacement operands into R1 while storing R2. A matching 1-to-2 CAS consequently failed, and a memory-only Sleeper consumed a full CPU while waiting. The selected export loads the replacement into R2. A direct native CAS regression checks matching, mismatching, and replacement-equals-current cases; the existing publication, route, owner and drain assertions remain intact.

Qualification uses official Go 1.26.11 on Darwin arm64. The dependency requires Go 1.26.3, raising this module's minimum from 1.26.0; x/exp and x/time advance only to the versions required by the selected graph. Other direct dependencies and application implementations are unchanged. The September export was selected after inspecting actual CAS source in generated snapshots; a raw source commit has an incompatible netstack package layout, and the latest November export requires Go 1.27.3. Neither was adopted.

Reviewed source differences preserve Stack.Destroy's Close/Wait sequence, the endpoint-creation caveat, TCPConn.Close and deadline reset behavior, channel ownership and dispatcher worker selection. Dispatcher changes add checklocks annotations; buffer/chunk, bits_template and channel are unchanged. Stack adds unrelated filtering/migration configuration that these adapters do not enable; gonet initializes the same timer fields through a value-returning constructor. These observations bound the adapter review, not a whole-gVisor security or protocol audit. Existing native tests and independently dated vulnerability checks remain required.
