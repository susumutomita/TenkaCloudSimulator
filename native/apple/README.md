# Optional Foundation Models helper

This executable is built separately with `make build-apple-helper`. The ordinary
Go simulator build does not import, embed or compile Swift, use cgo, or discover
a helper automatically. Runtime requires Apple silicon, macOS 27+ and an already
available Apple Intelligence model. Building requires Xcode's macOS 27 SDK and
Swift 6.4+. The executable's deployment target is 26.0 so its `--check` can report
`unsupportedOS` on 26.x; generation and actual token usage are guarded by the
27.0 SDK availability boundary. Older OSes fail explicitly at process startup.

The framework itself first shipped in macOS 26. The narrower 27+ runtime floor
here follows the local SDK's `LanguageModelSession.Response.usage` declaration,
which is `@available(macOS 27.0, *)`. See Apple's
[usage documentation](https://developer.apple.com/documentation/foundationmodels/languagemodelsession/usage-swift.struct)
and [availability guidance](https://developer.apple.com/documentation/foundationmodels/generating-content-and-performing-tasks-with-foundation-models).
This helper only constructs `SystemLanguageModel.default`. No Private Cloud
Compute model, custom adapter, model download, OS change, tools, feedback/logging
API or outbound network transport is used.

## Protocol v1

One invocation, one response, then exit. `--check` reads no input and returns
`{"version":1}` only when the required OS and on-device model are available.
`--generate` reads at most 2 MiB JSON from stdin until EOF:

```json
{"version":1,"model":"system","system":"Reply briefly.","messages":[{"role":"user","text":"Say hello."}],"maxTokens":64}
```

Optional `temperature`, `topP` and `topK` map to Foundation Models generation
options. At most one of topP/topK is accepted. Conversation turns alternate
user/assistant, beginning and ending with user. Previous turns become typed
transcript entries; the last user turn is the current prompt. Each process
creates a fresh session, keeping its transcript in memory only.

A successful generation response is at most 1 MiB:

```json
{"version":1,"result":{"text":"…","stop":"end","inputTokens":65,"outputTokens":9}}
```

Counts are the native API's usage, not AWS/Anthropic counts. The example is a
schema illustration, not an expected model answer. Responses at the token cap
are refused (`tokenLimit`), since the framework supplies no public stop reason.
Only completed responses below the cap can carry `end`. Generated reasoning,
tool calls and any non-text transcript entries fail rather than being dropped.

Failure responses use `{"version":1,"error":"CODE"}`. Fixed codes include
`deviceNotEligible`, `appleIntelligenceNotEnabled`, `modelNotReady`,
`unsupportedOS`, `unsupported`, `tokenLimit`, `contextLimit`, `busy`, `timeout`,
`generationFailed` and `outputLimit`. The Go adapter whitelists error codes and
never echoes arbitrary output, stderr, framework/decoder errors or prompt text.
Errors do not select another engine. Exit failures and malformed output fail too.

Go starts the operator's absolute executable path directly, with an empty
environment, stdin/stdout pipes and discarded stderr. There is no shell, PATH
lookup or socket listener. It is trusted code running with operator privileges,
not a containment boundary for untrusted executables. The shared admission gate
bounds concurrency and deadline; cancellation kills/reaps the helper. Stdout
exceeding the bound kills the helper immediately. The Swift source starts no
children. The framework/OS service lifecycle is outside this process boundary.

`make e2e-apple` tests a synthetic process double via the built binary and boto3.
`make e2e-apple APPLE_HELPER="$PWD/dist/tenkacloud-apple-helper"` explicitly tests
the actual on-device model, with synthetic input and no output text in reports.
It fails if unavailable and never substitutes the double. These are non-streaming
text APIs and do not establish Claude Code compatibility (Issue #453 P3).
