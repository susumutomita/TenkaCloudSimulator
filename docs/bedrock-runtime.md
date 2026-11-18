# Bedrock Runtime: opt-in local inference

The simulator can answer Amazon Bedrock Runtime **InvokeModel** and
**Converse** requests with a model that runs on your own machine. It is
**off by default**. The operator must choose the backend and map every model ID
explicitly. There is no real-AWS, cloud-model or synthetic fallback: a
disabled, unmapped, missing or unreachable backend returns an error, never a
made-up answer.

This page covers the bounded contract of [Issue #453](https://github.com/susumutomita/TenkaCloudSimulator/issues/453)
phases P0–P2: Ollama and an optional Apple Foundation Models helper. Streaming,
tool use and Claude Code compatibility (P3) are **not implemented**; see
[What is not supported](#what-is-not-supported).

## Start it with Ollama

1. Install [Ollama](https://ollama.com/) yourself and pull a model with
   `ollama pull <model>`. The simulator never downloads, deletes or lists
   models.
2. Start the simulator with an access token (the default) and one mapping per
   Bedrock model ID that clients may call:

```sh
./dist/tenkacloud-simulator --state ./practice.db --token-file ./simulator-token \
  --bedrock-runtime-backend ollama \
  --bedrock-model anthropic.claude-3-haiku-20240307-v1:0=llama3.2:1b
```

| Flag | Meaning |
| --- | --- |
| `--bedrock-runtime-backend` | `ollama` or `apple`. Absent means disabled. `apple` requires an explicitly configured native helper (see below). |
| `--bedrock-apple-helper` | Absolute path to the trusted optional helper, used only with `apple`. No PATH search, environment discovery or automatic build. |
| `--bedrock-ollama-url` | `http://<numeric loopback IP>:<port>`, default `http://127.0.0.1:11434`. Host names, HTTPS, credentials, paths, queries and non-loopback addresses are refused. |
| `--bedrock-model` | Repeatable `MODEL_ID=LOCAL_MODEL`, 1–32 mappings. `MODEL_ID` uses the Bedrock foundation-model ID form, including geography-prefixed IDs such as `us.anthropic.…`. `LOCAL_MODEL` is `system` for Apple or an Ollama tag; `:cloud` and `-cloud` tags are refused because Ollama runs them remotely. |
| `--bedrock-runtime-timeout` | Per-request generation deadline, 1s–5m, default 1m. |
| `--bedrock-runtime-concurrency` | Simultaneous generations, 1–8, default 2. Extra requests fail at once with `ThrottlingException`; they are not queued. |

Any Bedrock flag without `--bedrock-runtime-backend` is a startup error, and
the backend cannot be combined with `--unsafe-no-auth`. Every flag except
`--bedrock-model` may be given only once; a repeat is refused, not overridden.
Startup does not contact Ollama, so a stopped Ollama is reported on the first
request.

## Call it

Use the simulator endpoint, Region `us-east-1`, `us-west-2`, `ap-northeast-1`
or `eu-west-1`, a dummy `TCSIM…` access key, and the operator or participant
token in the `X-Simulator-Token` header. Use synthetic prompts only. The native
SDK credentials `TCSIMOPERATOR` and assumed-role `TCSIMASIA…` keys are accepted
only for STS, IAM, CloudFormation and SSM signing, so they are refused for
`bedrock`. Bedrock API keys (`Authorization: Bearer`) are refused too. There is
no console page.

```python
import boto3, json
from botocore.config import Config

token = open("simulator-token").read().strip()  # never print or commit it
client = boto3.client("bedrock-runtime", endpoint_url="http://127.0.0.1:4566",
                      region_name="us-east-1", aws_access_key_id="TCSIMEXAMPLE",
                      aws_secret_access_key="simulator-only",
                      config=Config(retries={"max_attempts": 0}))
client.meta.events.register("before-sign.bedrock-runtime",
                            lambda request, **_: request.headers.add_header("X-Simulator-Token", token))

reply = client.converse(modelId="anthropic.claude-3-haiku-20240307-v1:0",
                        messages=[{"role": "user", "content": [{"text": "Say hello."}]}],
                        inferenceConfig={"maxTokens": 64})
print(reply["output"]["message"]["content"][0]["text"])

body = {"anthropic_version": "bedrock-2023-05-31", "max_tokens": 64,
        "messages": [{"role": "user", "content": "Say hello."}]}
reply = client.invoke_model(modelId="anthropic.claude-3-haiku-20240307-v1:0", body=json.dumps(body))
print(json.loads(reply["body"].read())["content"][0]["text"])
```

`/_simulator/v1/capabilities` lists `services["bedrock-runtime"]`: whether it is
enabled, the backend kind, the mapped Bedrock model IDs and the limits. Every
authenticated caller, participants included, can read it, so mapped model IDs
are not secret. It never shows the backend address or backend model names.

## Contract

**Routing.** Requests signed for `bedrock` (or selected with
`X-Simulator-Service: bedrock-runtime`) that address `/model/…`,
`/async-invoke…` or `/guardrail/…` reach Bedrock Runtime. Other `bedrock`
paths, such as `/guardrails`, stay with the separate
[Bedrock control-plane catalog](aws-catalog-services.md), whose stored state is
unchanged. Unknown methods or paths return `UnknownOperationException` (404).

**Model IDs.** The URI `modelId` is either a mapped ID or
`arn:aws:bedrock:<request Region>::foundation-model/<mapped ID>`. Other ARN
kinds (inference profiles, custom, provisioned or imported models, prompts)
are refused with `ValidationException`. A well-formed ID without a mapping
returns `ResourceNotFoundException`.

**Authorization.** Both operations evaluate `bedrock:InvokeModel` on
`arn:aws:bedrock:<Region>::foundation-model/<model ID>` for the request's
exercise identity, before the request body is read, before the mapping is
consulted and before any backend call. A geography-prefixed ID is authorized as this foundation-model ARN; real
inference-profile ARNs and their extra checks are not modeled. As elsewhere,
this identity check is exercise context, not tenant isolation: give each team
its own process, database and token.

**InvokeModel** accepts only the Anthropic Claude Messages body:
`anthropic_version` (must be `bedrock-2023-05-31`), `max_tokens` (1–8192),
`messages`, optional `system`, `temperature` (0–1), `top_p` (0–1) and `top_k`
(1–500). Content is a string or `{"type":"text","text":…}` blocks. `Content-Type`
is absent or `application/json`; `Accept` is absent, `application/json` or
`*/*`. The response has the Messages shape (`id`, `type`, `role`, `model`,
`content`, `stop_reason`, `stop_sequence`, `usage`) and the
`X-Amzn-Bedrock-Input-Token-Count`, `-Output-Token-Count` and
`-Invocation-Latency` headers. Other model-family bodies are refused.

**Converse** requires `Content-Type: application/json` (the SDKs send it) and
accepts `messages`, optional `system` text blocks and
`inferenceConfig` with `maxTokens` (1–8192, default 1024), `temperature` and
`topP`. Each content block sets exactly one member, `text`. Roles must start and
end with `user` and alternate. The response contains `output.message`,
`stopReason`, `usage` and `metrics.latencyMs`.

**Strictness.** Unknown, unsupported, repeated or `null` members, blank text,
other content types, any `X-Amzn-Bedrock-*` option header (trace, guardrails,
latency, service tier), query parameters, session tokens, invalid UTF-8,
trailing JSON, incomplete bodies and bodies over 1 MiB return
`ValidationException`; nothing is silently dropped. Multiple text blocks in one message are joined with a
newline. A final `assistant` message (prefill) and `stop_sequences` /
`stopSequences` are refused, because the local backend cannot report
`stop_sequence` truthfully.

**Results.** `stop_reason`/`stopReason` is `end_turn` or `max_tokens` from
Ollama's `done_reason`. Token counts are Ollama's own tokenizer counts (0 when
Ollama omits them), not Anthropic or AWS counts. An empty reply has an empty
`content` list. A reply containing reasoning (`thinking`), tool calls, images
or a remote-execution marker is refused with `ModelErrorException`, not trimmed.
The adapter sends no `think` option. Depending on the Ollama version, a
reasoning model may instead put its reasoning inside the reply text, which is
returned as is, so prefer a non-reasoning model. Ollama may also drop earlier
turns to fit its context window; the adapter cannot detect that.

| Situation | Error (HTTP) |
| --- | --- |
| Bedrock Runtime not enabled | `UnknownOperationException` (501) |
| Streaming, CountTokens, ApplyGuardrail, async invocation | `UnknownOperationException` (501) |
| IAM denial | `AccessDeniedException` (403) |
| Unmapped model ID, or mapped model not installed in Ollama | `ResourceNotFoundException` (404) |
| Ollama unreachable, or the simulator is shutting down | `ServiceUnavailableException` (503) |
| All slots busy, or Ollama reports it is busy | `ThrottlingException` (429) |
| Operator or caller deadline expired | `ModelTimeoutException` (408) |
| Ollama error or unusable reply | `ModelErrorException` (424) with `resourceName`, and `originalStatusCode` when Ollama returned a status |

Ollama error bodies are never echoed, because they may quote the prompt.

## Transport and safety boundary

- The adapter dials only the configured numeric loopback `host:port`, over
  plain HTTP, with no environment proxy, no redirects, no name resolution and
  no TLS endpoint. It calls only `POST /api/chat` with `"stream": false`.
- Request bodies are limited to 1 MiB, backend requests to 2 MiB and backend
  replies (headers 16 KiB, body 1 MiB). Prompt text is sent without HTML
  escaping, so any valid body under 1 MiB fits the backend bound; if the bound
  were exceeded, the request would fail with `ValidationException` before
  reaching Ollama. The deadline, client disconnects and
  shutdown cancel the backend request. A request under way extends the
  listener's 30-second read/write deadlines to its own deadline plus 10 seconds.
- Prompts and replies are not written to SQLite or logs. A Go test checks the
  database files after a synthetic call. The simulator cannot prove where an
  Ollama daemon runs a model; refusing cloud tags and remote markers is a guard,
  not a guarantee. Check your Ollama configuration yourself.
- Local generation uses your CPU/GPU and memory. Only the operator decides
  which models can be called.

## Apple Foundation Models

`apple` uses only `SystemLanguageModel.default`, the on-device model. It does
not use Private Cloud Compute. It requires an **Apple silicon Mac, macOS 27+
and an already available Apple Intelligence model**. The simulator never enables
Apple Intelligence, changes security/OS settings or downloads models. Availability
is checked at startup and before every generation; device ineligibility,
Apple Intelligence disabled, model not ready and unsupported OS fail explicitly.

The native helper is a separate, optional executable. The normal `make build`
still produces the same standalone `CGO_ENABLED=0` Go binary, including on Linux
and Windows, and never requires Swift. Build the helper yourself on an Apple
silicon Mac with Xcode's **macOS 27 SDK / Swift 6.4+**:

```sh
make build-apple-helper
./dist/tenkacloud-apple-helper --check
./dist/tenkacloud-simulator --state ./practice.db --token-file ./simulator-token \
  --bedrock-runtime-backend apple \
  --bedrock-apple-helper "$PWD/dist/tenkacloud-apple-helper" \
  --bedrock-model anthropic.claude-3-haiku-20240307-v1:0=system
```

Only operator mapping to `system` is accepted. Missing helper, unsupported host,
failed availability check or lost model availability never chooses Ollama, a
cloud model or a fabricated answer. The shared authentication, IAM, request size,
concurrency, deadline and shutdown admission rules remain in force.

**Native boundary.** The trusted helper has the operator's native process
privileges; this is not a guest-code sandbox. Use an executable you built and
control. Go invokes the absolute path directly with `--check` or `--generate`,
without a shell or inherited environment. A versioned JSON message travels over
bounded stdin/stdout pipes, never prompt arguments, files or sockets. Stderr and
unrecognized error details are discarded. Startup has a 10-second probe deadline;
each generation uses a fresh process and in-memory session. Cancellation kills
and reaps the helper. This bounds the helper process, not Apple's internal service
scheduler. The native implementation makes no network requests, launches no
children, serializes no transcript and adds no prompt/response logs. The OS and
Apple framework remain outside the simulator's persistence guarantees.

**Bounded differences from Ollama.** System instructions and previous user and
assistant turns become typed transcript entries, preserving their roles. Apple
supports temperature and either topP (greater than 0, at most 1) or topK; zero
topP or combining topP and topK is refused
with `ModelErrorException` (424). Its context window and guardrails remain in
force: overlong context, refusals, unsupported language/options or native errors
fail without truncating the supplied history or substituting text.

The macOS 27 minimum is deliberate: its public
[token usage API](https://developer.apple.com/documentation/foundationmodels/languagemodelsession/usage-swift.struct)
provides actual input/output counts; earlier APIs cannot satisfy this adapter's
truthful usage/termination contract. The framework exposes no public stop reason.
A successful reply below the requested cap returns `end_turn` and those native
counts. A reply **at the token cap is refused** with `ModelErrorException`, even
if a natural end might coincide with the cap; the adapter does not guess
`max_tokens`. Non-text transcript entries and generated reasoning are refused.
These restrictions intentionally narrow the shared wire contract. Swift's SDK
availability declarations and build boundary are described in
[native/apple/README.md](../native/apple/README.md).

Offline acceptance through the built binary and boto3 uses a synthetic helper:

```sh
make e2e-apple
```

On a supported Mac with the already available model, explicitly request real
on-device acceptance (InvokeModel, Converse with history, sampling, token-cap
refusal and no prompt/reply in SQLite):

```sh
make e2e-apple APPLE_HELPER="$PWD/dist/tenkacloud-apple-helper"
```

These tests send only synthetic content, omit generated text from their report,
and perform no download or settings change. An unavailable real model fails the
check; it is not replaced with the test double. Neither check verifies Claude Code.

## What is not supported

- InvokeModelWithResponseStream, ConverseStream and the bidirectional stream
  (AWS event-stream framing), CountTokens, ApplyGuardrail and async invocation.
- Images, documents, video, tool use and tool results, reasoning, citations,
  cache points, guardrails and prompt management.
- Model families other than the Anthropic Messages InvokeModel body.
- Inference-profile ARNs, cross-Region routing, provisioned throughput and
  model catalog/listing APIs.
- **Claude Code.** Claude Code needs streaming and tool use, so it does not
  work yet. Do not describe this as Claude Code support.
- A console page or API-reference page for Bedrock Runtime.

## Verification

The default gates use synthetic prompts and test doubles:

- `internal/inference`: URL and model-name validation, actual HTTP calls to a
  fake Ollama server (request body, options, every failure mapping, no proxy,
  no redirect, size limits, cancellation) and the concurrency/deadline gate.
- `internal/aws`: actual HTTP requests to the gateway for routing ahead of the
  control-plane catalog, disabled and unsupported operations, both request
  shapes, every refusal, IAM allow/deny before the mapping, failure mapping,
  throttling, timeout, shutdown cancellation, extended listener deadlines and
  no prompt in the SQLite files.
- `cmd/simulator`: flag validation, refusal before any startup file is
  created, and an actual CLI run with token authentication.
- `test/e2e/bedrock_runtime.py`: the built binary with the official boto3
  client against a fake Ollama process, including participant IAM denial and
  restart with the same database.

- `formal/BedrockRuntimeAdmission.tla`: a bounded model of admission order,
  shutdown, slots and truthful results, with fourteen sensitivity controls
  ([model contract](formal-bedrock-runtime.md)).

The default gates do not use a real model or verify Claude Code. The optional
`e2e-apple` command above distinguishes a process double from actual Apple
hardware/model verification. Results with a real Ollama model are not part of
these checks.
