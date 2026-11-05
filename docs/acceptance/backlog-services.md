# Service backlog integration — 2026-11-05

This integration brings the independently developed SQS console (PR410),
CloudWatch Logs (PR407), Secrets Manager (PR408), and DynamoDB (PR309) into the
provider-specific console and language controls merged in PR412. Their original
branches are preserved. Historical passing runs on each isolated PR do not
certify the composed source; use the resulting PR's exact-head Actions checks.

The integrated source revisions are PR410 `39fca989`, PR407 `dfbf97ed`,
PR408 `ffdf1e29`, and PR309 `734da3bf`. PR408 and PR410 received additional
locale, navigation-alias, login and acceptance-test work while this integration
was being validated. Those newer deltas remain separate; incorporating the
reviewed service revisions does not close or claim to supersede those two PRs.

## Delivered scope

| Area | Implemented behavior | Remaining scope |
| --- | --- | --- |
| SQS | Native Standard-queue console: create/list/open/inspect/delete queues, send/receive/delete messages and change visibility; all nine served API examples execute against the same SQLite state | FIFO, batch, long polling, DLQ, SNS, CloudFormation and Lambda bridge |
| CloudWatch Logs | Seven native group/stream/event APIs, pagination and shared-state console | Metrics, alarms, retention APIs, subscriptions, Insights, automatic Lambda delivery and CloudFormation |
| Secrets Manager | Seven native metadata/value/version APIs, current/previous staging, explicit value reveal/hide and immediate full-ARN deletion | Real secret storage, KMS encryption, binary values, rotation, replicas, arbitrary staging labels and recovery windows; practice values remain plaintext in SQLite |
| DynamoDB | Seven native table/item APIs, S/N/B keys, optional sort key, recursive values, bounded item sizes and partition-key existence conditions | Console, Query/Scan/UpdateItem, secondary indexes, Streams, transactions, CloudFormation and Lambda bridge |
| Provider SDKs | Pinned official Google/Azure Python SDKs use two authenticated local CLI processes with independent persistent state | Real cloud identity/provisioning and universal SDK/default-option compatibility; see the [exact storage SDK subset](crossprovider-official-storage.md) |

SQS, Logs and Secrets Manager appear only inside the AWS service catalog and
only when their capability is present. DynamoDB is API-only and is not offered
as a console route. Japanese/English switching changes presentation, preserves
user input and service data, and keeps native operation identifiers canonical.

## Corrections made during integration

- KMS's Japanese Encrypt/Decrypt buttons previously translated the protocol
  operation name. Requests now always use `TrentService.Encrypt` and
  `TrentService.Decrypt`; an actual Japanese browser/SDK round trip guards this.
- DynamoDB rejects ambiguous duplicate JSON, unsupported nested fields and
  invalid schema/key shapes before mutation. A duplicate HASH/RANGE attribute
  cannot leave an unreadable table or reserve its name after a failed request.
  Refused item writes preserve existing bytes and statistics. Number/Binary Sets
  reject duplicate values across alternate encodings; partition/sort keys enforce
  2048/1024-byte limits after UTF-8/Base64 decoding.
- The Secrets console closes a completed mutation form before refreshing
  metadata. A failed follow-up read preserves the successful-write notice and
  offers a metadata-only retry. Create/update/delete browser regressions hold
  and fail the read after a real committed write, then verify exact SDK state
  and that retry never repeats the mutation. Deleted details and cached list
  rows are removed as soon as deletion succeeds.
- The common GCP-prefixed authentication denial now returns the native nested
  error object expected by Google's error parser. Authentication decisions,
  browser-session rules and legacy AWS/control error bodies are unchanged.
- The API reference retains one definition of each shared-root header and all
  61 existing operation IDs / 34 paths. SQS and DynamoDB JSON 1.0 templates
  coexist; KMS, SSM, Logs and Secrets JSON 1.1 examples are preserved. Each
  service's examples execute through its actual native API. A maintained JSON
  Schema validator checks all 16 JSON 1.0 examples and actual native responses,
  with eight negative/sensitivity controls for branch omission and invalid shapes.
- Formal inventory is the union of all established obligations: 26 models,
  149 named sensitivity controls and exact-file applicability. No prior model,
  invariant, control or 90% coverage floor is removed.

## Acceptance

`make before-commit` remains the native formatting/vet/module/asset/race/build
gate. CI additionally generates Cobertura and enforces both statement and
unfiltered source-line coverage at 90%. `make e2e` includes every prior test plus
the four service candidates, Japanese operation regressions and official
cross-provider SDK acceptance. The SDK boundary helper itself has fail-closed
tests for dependencies, bounded iteration, wrong status and unexpected success.
Browser checks use actual authenticated HTTP, native SDK state, desktop/mobile
reflow, axe accessibility and local-only request guards.

`Formal validation` checks the committed applicability record and executes all
175 baseline/control cases against the pinned TLC jar. These finite models do
not prove Go/SQLite/HTTP/UI refinement, full AWS compatibility or real-cloud
equivalence. Protection settings and reviewer requirements are not modified.

Broad service catalog issues remain open wherever their declared scope exceeds
this subset. Existing `make start` was already delivered by PR308; it is not
claimed as a new implementation. WAF50 and other held candidates are outside
this integration. No release, deployment, real cloud mutation or automated
problem enablement is performed.
