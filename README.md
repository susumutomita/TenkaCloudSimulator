# TenkaCloud Simulator

An independent cloud API simulator built as one Go executable. The console, SDK clients, and interactive API reference operate on the same SQLite state. No Docker, Node, cloud account, external database, or real infrastructure is needed at runtime.

The first working increment implements **AWS S3 basics and a limited identity-policy evaluator**. AWS is the first provider of a multi-cloud product, not the product boundary. Google Cloud and Azure are not implemented yet.

```sh
make build
./dist/tenkacloud-simulator
```

Requires Go 1.26 to build (CI pins the current maintained patch 1.26.10). Open <http://127.0.0.1:4566/_simulator/console/> for the console or <http://127.0.0.1:4566/_simulator/docs/> for the embedded Scalar API reference. The API reference supports actual requests to this simulator and makes no runtime CDN or hosted proxy requests.

```sh
# Persistent state; omission means a fresh in-memory SQLite database.
./dist/tenkacloud-simulator --state ./practice.db
# Each CI job should use its own process and database.
./dist/tenkacloud-simulator --listen 127.0.0.1:4567 --state ./job.db
```

SIGINT/SIGTERM gracefully shuts down the HTTP server and closes SQLite. Binding outside a numeric loopback address requires explicit `--allow-remote`; expose it only within a trusted isolated environment. Principals are exercise context, not authentication. This is not a tenant isolation boundary.

## Basic API and SDK use

S3 uses its native REST/XML wire format with path-style bucket addressing. Both the root endpoint and `/_simulator/providers/aws/` address the same provider. There is no REST wrapper masquerading as AWS SDK compatibility.

```sh
export AWS_ACCESS_KEY_ID=TCSIMEXAMPLE
export AWS_SECRET_ACCESS_KEY=simulator-only
export AWS_DEFAULT_REGION=us-east-1
aws --endpoint-url http://127.0.0.1:4566 s3api create-bucket --bucket practice-bucket
aws --endpoint-url http://127.0.0.1:4566 s3api list-buckets
curl -X PUT --data-binary 'hello' http://127.0.0.1:4566/practice-bucket/hello.txt
curl http://127.0.0.1:4566/practice-bucket/hello.txt
```

Unsigned requests work. SDK-generated signatures using dummy `TCSIM…` access keys are accepted without signature verification. Real AWS-style access keys, session tokens, and presigned requests are rejected. Never supply real credentials. For boto3 use `Config(s3={'addressing_style':'path'}, request_checksum_calculation='when_required', response_checksum_validation='when_required')` with an explicit endpoint and dummy credentials. Buffered PutObject is supported; aws-chunked streaming and managed multipart transfers are not.

| Supported operation | Behavior |
| --- | --- |
| ListBuckets / CreateBucket / HeadBucket / GetBucketLocation | Global bucket names in the single simulated account; four declared regions |
| DeleteBucket | Fails with `BucketNotEmpty` while child objects exist |
| ListObjectsV2 | Lexical order, prefix, delimiter, max-keys, start-after, opaque continuation, URL encoding |
| PutObject | Atomic replacement, ETag, If-Match / If-None-Match, selected checksum validation |
| GetObject / HeadObject | Exact bytes, content type, size, ETag, single byte range, ETag conditions |
| DeleteObject | Idempotent deletion of keys in an existing bucket |

Limits: 8 MiB per object, 64 MiB total object bytes, 1,000 buckets, 10,000 objects. These bound payload state, not whole-process resident memory or peak concurrent upload buffers. Regions: `us-east-1`, `us-west-2`, `ap-northeast-1`, `eu-west-1`.

## IAM simulation

The default `simulator-admin` is an unrestricted local setup identity. Send `x-simulator-principal: reader` to evaluate the stored identity policy for `reader`. Unknown principals implicitly deny. The console's identity button uses this same header.

```sh
curl -X PUT http://127.0.0.1:4566/_simulator/v1/iam/principals/reader/policy \
  -H 'Content-Type: application/json' --data '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets","s3:ListBucket","s3:GetObject"],"Resource":"*"},{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::practice-bucket/private/*"}]}'
curl -H 'x-simulator-principal: reader' http://127.0.0.1:4566/practice-bucket/hello.txt
```

Supported: identity policy `Version`, `Statement`, `Sid`, `Effect`, `Action`, `Resource`; scalar or array forms; `*`/`?` patterns; case-insensitive action and case-sensitive resource matching; explicit Deny overrides Allow; no matching Allow means implicit Deny. Unsupported policy fields, including Condition, Principal, NotAction, and NotResource, are rejected rather than ignored. Policy management is a **simulator JSON control API**, not the AWS IAM Query protocol.

Not implemented: resource policies, ACLs, conditions, roles/sessions, boundaries, SCPs, cross-account policy evaluation, or AWS account authentication. This limited model must not be described as full IAM compatibility.

## Verification

```sh
make before-commit              # format, vet, asset identity, module integrity, race tests, static build
python3 -m venv /tmp/tcsim-tests
/tmp/tcsim-tests/bin/pip install -r scripts/requirements-test.txt
/tmp/tcsim-tests/bin/playwright install chromium
make e2e PYTHON=/tmp/tcsim-tests/bin/python
make cross-check                # compile Linux amd64/arm64, Darwin amd64/arm64, Windows amd64
curl -fL https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar -o /tmp/tla2tools.jar
TLA_JAR=/tmp/tla2tools.jar make formal
```

The browser/SDK tests start and stop the actual binary from an empty directory with no executables on PATH, create resources through the console and boto3, check state in both directions, exercise IAM/deletion errors, restart a persistent database, check process isolation, and execute Scalar's request client. All browser requests outside the simulator origin are blocked and fail the test if attempted. Java, Python, the SDK, and Chromium are development-only tools.

TLC checks a finite TLA+ model (two buckets, two keys, abstract Allow/Deny states) and verifies that removing the nonempty-delete guard produces a counterexample. This is bounded model checking of the specified state machine, **not a proof of the Go implementation or all IAM rules**. See [formal/README.md](formal/README.md).

[CI](docs/ci.md) runs these same gates on Linux, enforces both 90% coverage floors, and publishes a workflow summary plus downloadable Go/XML/HTML coverage evidence. Foreign-platform binaries are compile-checked, not runtime-tested. Evidence and screenshots are in [reports/](reports/).

## Architecture and compatibility

[The design note](docs/design/0001-go-standalone.md) describes provider registration, SQLite constraints, and the transition from the old TypeScript runtime. This is a deliberate replacement: the old worlds/deployments/snapshots/CloudFormation/multi-provider APIs are not retained. Existing TenkaCloud/Challenge integrations need a separate adapter and capability compatibility checks. Simulator contains no problem IDs or graders.

Not implemented in this increment: EC2, VPC, compute execution, SSH, Terraform/CloudFormation, cross-cloud resource links, versioning, multipart upload, lifecycle, bucket policies, arbitrary metadata, fault injection, or real data-plane networking. The stored object bytes are real local simulated data; no cloud resources or infrastructure are created.

## Licensing and attribution

Repository license remains MIT. The console uses original HTML/CSS and no copied cloud logos, screenshots, fonts, icons, or console assets. AWS service names identify simulated APIs; this project is independent and not affiliated with AWS. Scalar 1.76.4 is MIT, rebuilt from integrity-locked npm modules with the optional color picker excluded; its bundle hash and dependency inventory are pinned under [third_party/](third_party/). Runtime Go dependency notices and bundled frontend notices are embedded at `/_simulator/licenses/`.

The earlier missing-notice concern is resolved by **excluding** `@replit/codemirror-css-color-picker` from the shipped API reference. The local rebuild omits CSS color swatches only; it preserves request editing/execution. It retains original notices for all 194 included package/style entries. See [rebuild instructions and exclusion evidence](tools/scalar/README.md) and [removal record](third_party/color-picker-removal.json). No copyright wording was invented. This removes that specific distribution blocker; it is not a legal certification. GitHub CI runs the canonical Go vulnerability check before merge. No release or deployment is part of this change.

Additional console verification: `python3 scripts/get-audit-tools.py /tmp/tcsim-audit`, then `AXE_SCRIPT=/tmp/tcsim-audit/axe.min.js make audit PYTHON=/tmp/tcsim-tests/bin/python`. This development-only tool retains its original licenses outside the application. The seven-state [console audit](reports/audit/README.md) records keyboard focus, validation, responsive reflow, screenshots, contrast fixes, and remaining UX limits. See the [vulnerability-data review](reports/vulnerability-review.md) for the official-source snapshot fallback, dependency update, and the remaining canonical-service limitation.
