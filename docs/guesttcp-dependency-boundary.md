# Optional guest TCP dependency boundary

The module files select executable dependencies; they are not metadata-only. `go.mod` and `go.sum` retain their existing `WAFRequestEvaluation` obligation and additionally participate in `EngineLifecycle` and `ListenerLifecycle` as dependency selection for these two adapters. Those models check bounded wrapper lifecycle transitions under explicit external-library assumptions, not gVisor implementation refinement, protocol compliance, dependency security, physical cleanup or authenticated authority. No model properties, controls or finite bounds change with this binding.

## Reviewed pins

All four version and content checksums are pinned together. Replacement or exclusion directives require dependency-boundary review; the guard conservatively rejects any such directive. A changed pin must not be accepted merely by rewriting a hash: recheck the source assumptions, resolved module graph, provenance, notices, native tests and vulnerability status. Source checksum consistency alone is not a current security assessment.

```
gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8 h1:Zy8IV/+FMLxy6j6p87vk/vQGKcdnbprwjTxc8UiUtsA=
gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8/go.mod h1:QkHjoMIBaYtpVufgwv3keYAbln78mBoCuShZrPrer1Q=
github.com/google/btree v1.1.2 h1:xf4v41cLI2Z6FxbKm+8Bu+m8ifhj15JuZ9sa0jZCMUU=
github.com/google/btree v1.1.2/go.mod h1:qOPhT0dTNdNzV6Z/lhRX0YXUafgPLFUh+gZMl761Gm4=
golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa h1:FRnLl4eNAQl8hwxVVC17teOw8kdjVDVAiFMtgUdTSRQ=
golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa/go.mod h1:zk2irFbV9DP96SEBUUAy67IdHUaZuSnrz1n472HUCLE=
golang.org/x/time v0.12.0 h1:ScB/8o8olJvc+CQPWrK3fPZNfh7qgwCrY0zJmoEQLSE=
golang.org/x/time v0.12.0/go.mod h1:CDIdPxbZBQxdj6cxyCIdrNogrJKMJ7pr37NYpMcMDSg=
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

Pinned gVisor source (commit `573d5e7127a83e0b81df83b86a949d838a02b49a`):

- `go.mod`: `35db32c9c0ea0c140c323b2107608658f3102557db40802ff4d2ddcf2efd8ee5`
- `go.sum`: `8380296164d8bfc0ac6cd968670f1013790aa047957d7b01d2288d2c068b26f8`
- `pkg/tcpip/stack/stack.go`: `ee6dea53a06e245b32a46c0e45cd896692fc618b8106b4a2af3f005ac8dc0897`
- `pkg/tcpip/link/channel/channel.go`: `8c1ae6b1da82c3e69cefaad66eeb4c60cb11a2162cdce50bec7e7c4d29dd2b20`
- `pkg/tcpip/adapters/gonet/gonet.go`: `7c6e776de42b349b185aa5ef896c28fe36e374b327e9d094ed4931f6cf14f3f9`
- `pkg/tcpip/transport/tcp/sack_scoreboard.go`: `8b1a5e9dd4eceaa3b0c7151f9f05c1a79279459433b7d0f1f8dd69c7031e7aba`
- `pkg/tcpip/transport/tcp/dispatcher.go`: `1eeb7db9c386c16b58fb49cdd57fe94bdd1602bdb83edf899f90db37031b6ca8`
- `pkg/buffer/chunk.go`: `3ae0d4ca7facc4a7322df650b60788c392ffe6cc499c52a4f098778dfacda0fe`
- `pkg/bits/bits_template.go`: `7a6f1391c2ed049b30913d4edfdf7c4f977e01585bcd599a4447f467ee20f3a7`

The anchors identify the source behind the reviewed boundary assumptions, not an upstream authenticity attestation. The provenance record and Go module sums remain separate checks. Current advisory results require a dated scan and database context; model success, a patched Go toolchain and notice consistency do not establish absence of vulnerable dependencies.
