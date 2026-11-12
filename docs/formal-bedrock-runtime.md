# Bedrock Runtime admission model

`formal/BedrockRuntimeAdmission.tla` is a finite abstraction of how the opt-in
[Bedrock Runtime](bedrock-runtime.md) admits a request to the one
operator-selected local backend and reports its result. It is bounded model
checking, not a proof that the Go code refines the model.

## What is modeled

Two requests each arrive with Boolean attributes: supported operation, IAM
allow, operator-mapped model and valid input. The backend is enabled or not for
the whole behavior, there is one concurrency slot, the runtime may be closed
once, and a running request either finishes with one of six backend outcomes
(`ok`, `unavailable`, `missing`, `timeout`, `busy`, `error`) or is canceled by
shutdown. Admission checks run in the implementation's order: operation,
enabled backend, IAM, mapping, input, shutdown, slot.

| Invariant | Meaning |
| --- | --- |
| `BackendOnlyAfterAdmission` | Unsupported operations, a disabled backend, unmapped models and invalid input never reach the backend. |
| `AuthorizationFirst` | A request reaches the backend, or learns about the mapping, input, shutdown or slots, only after IAM allowed it. |
| `NoCallAfterClose` | A closed runtime admits nothing new to the backend. |
| `ConcurrencyBound` | Running requests never exceed the slot limit, and every ending releases its slot. |
| `SuccessOnlyFromBackend` | Success means the backend returned it; nothing is synthesized. |
| `NoFallback` | Only the configured backend answers. |
| `TruthfulFailure` | Backend results are reported as themselves; refusals and shutdown cancellation never claim a backend result. |
| `NoPersistence` | No prompt or reply is stored. |

Fourteen sensitivity controls each break one check and must produce a
counterexample for exactly the invariant they target: omitting authorization
or moving the mapping before it, omitting the enabled/mapping/input/operation
checks, admitting after close, ignoring or leaking the slot, a synthetic
disabled success, falling back to another backend, misreporting a missing model
or a shutdown, and persisting the prompt.

## Source correspondence

| Model step | Source |
| --- | --- |
| `Refusal` order | `internal/aws/bedrock_runtime.go` `bedrockRuntime` (route, disabled, IAM, mapping, parse) and `internal/inference/inference.go` `Bounded.Generate` (ended context, slot) |
| `closed`, `Cancel` | `BedrockRuntime.Close`/`generate` and `Bounded.Generate` |
| `Finish` result mapping | `bedrockInferenceError` and the `inference` error kinds |
| One configured backend | `cmd/simulator/bedrock_flags.go`, `NewBedrockRuntime`, `Gateway.BedrockRuntime` |
| No persistence | no SQLite or log write in the runtime path |

## Limits

The IAM decision, strict JSON validation, Ollama transport pins, deadline
arithmetic, listener deadlines and token counts are assumed, not modeled. The
model's shutdown and cancellation results are distinct kinds, while the wire
uses `ServiceUnavailableException` for both with distinct messages. Streaming,
tool use, the Apple backend, Claude Code and liveness are outside the model.
The Go, CLI and boto3 tests listed in [Bedrock Runtime](bedrock-runtime.md)
check the implementation boundaries independently.
