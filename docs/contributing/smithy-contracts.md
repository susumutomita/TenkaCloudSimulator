# Pinned Smithy contracts (initial SQS slice)

Smithy describes API shapes; it does not implement SQLite state, lifecycle,
authorization, networking, guest execution or full AWS compatibility. This
initial integration is a test-only oracle for five existing SQS AWS JSON 1.0
operations: CreateQueue, SendMessage, ReceiveMessage, DeleteMessage, DeleteQueue.
It adds no runtime dependencies and does not replace the existing Go handlers.

## Source and license

The unmodified `internal/aws/testdata/smithy/sqs.json` comes from the official
[AWS SDK for Go v2 model](https://github.com/aws/aws-sdk-go-v2/blob/73f3c63d93a0c418cb525482adf29014685d10b4/codegen/sdk-codegen/aws-models/sqs.json).
The commit is pinned in `source.json`, with its Git blob ID and SHA-256. The
model uses Smithy 2.0 and SQS service version 2012-11-05. That API version is not
a claim that the source is from 2012. The upstream revision is the 2026-11-11
SDK release.

Upstream Apache-2.0 `LICENSE.txt` and `NOTICE.txt` are preserved beside the
model. The generated `sqs-contracts.json` is a **modified derivative** of that
model: it includes only input/output transitive shape closure for the selected
operations, omits documentation traits, and synthesizes Smithy's built-in Unit
shape. Its attribution and Apache-2.0 license remain those of the model; the
repository's MIT license does not replace them. No AWS Emulator implementation
code or generated service runtime is copied. The full model is testdata and is
not embedded into the executable.

## Offline verification

```sh
python3 scripts/smithy-contracts.py --check
python3 scripts/test-smithy-contracts.py
go test ./internal/aws -run '^TestSQSSmithy' -count=1
```

The Go test drives the actual gateway with an in-memory SQLite fixture. It
checks request and response shapes through a queue/message lifecycle, checks
that deletion takes effect, and derives missing-required-field rejection cases
from the pinned model. Separate negative oracle tests detect malformed
responses. Each consumed shape and operation is independently compared to the
upstream model during the Go test. The extraction check and Python tests also
run in the normal `make test` gate. Full repository gates still apply; these focused tests do not
replace `make before-commit` or `make e2e`.

The oracle validates structure member names, required members, JSON scalar
kinds, lists, maps, 32-bit integer range and base64 blobs. String enum values
remain open. It does not validate documentation-only constraints, IAM, error
shape/status compatibility, Query/XML, all enum features, all SQS operations,
or all possible valid inputs. Existing service-specific tests remain required.
It is deliberately strict about unknown members in tested payloads; this is a
regression check, not a general SDK deserializer's unknown-member policy.

To update: fetch the model and license/notice from a reviewed immutable upstream
commit, verify source blob hashes, update source.json, regenerate with
`python3 scripts/smithy-contracts.py`, review the diff and rerun tests. The
script never downloads or updates upstream automatically. Expanding to a new
service requires selecting its protocol and supported behaviors explicitly.
