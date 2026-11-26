# TenkaCloud Simulator

English | [日本語](README.ja.md)

An independent learning simulator for cloud APIs and console workflows, without a cloud account.
AWS is the main focus, with limited Google Cloud, Azure and optional Sakura support.
Supported console actions and APIs share the same SQLite state.

One Go binary includes the consoles, API reference, SQLite and Python Lambda runtime.
**Running a built binary needs no Go, Node.js, host Python, Docker, external database
or real cloud connection. Use dummy credentials and practice data only.**

An explicitly configured Battle VM runtime is separate from these default API
simulation requirements: it needs Linux or macOS (amd64 or arm64; WSL2 on Windows), a trusted pinned QEMU/firmware/image
bundle and private operator configuration. Host clock qualification is also
required; enabling the flag does not establish it. See [opt-in runtime setup](docs/start.md#explicit-battle-runtime-linux-and-macos).
Battle capability discovery resolves only after the retained instance is ready and its exact CloudFormation stack is complete.


## Start and sign in

From the repository root, with **Go 1.26.3 or newer, GNU make and a POSIX shell** installed:

```sh
make start
```

This builds the current source and starts at `127.0.0.1:4566`, reusing
`./practice.db` and `./simulator-token`. A failed build never starts an old binary.

1. Open <http://127.0.0.1:4566/_simulator/console/>.
2. Choose **Choose token file**, select `simulator-token`, then **Open sandbox**.
   The browser reads the file locally and sends the token only when you sign in.
   You can also paste the token into the masked field.
3. Press **Ctrl+C** to stop. Restart with `make start` to keep your saved resources.

The operator token controls this environment. Keep it out of Git, logs and screen
recordings, and never give it to participants. New token files use mode `0600`.
Interactive startup can display the token; redirected output and CI suppress it.
**Account → Sign out** ends only that browser session.

More: [startup options and existing binaries](docs/start.md),
[token-file help](docs/login-token-file.md), [sessions](docs/console-sessions.md).
This README describes this source tree; main and released binaries may differ,
including Lambda Tail support. Check the [binary's source/version](docs/releases.md).

## Try it: run a Python Lambda and read its output

Prefer the browser? In the local console, open **Lambda → Create function**, choose an existing execution role, edit the Python sample in CodeMirror, and create it. Use **Code → Deploy code**, then **Test** to run it without making a ZIP yourself. Deployed handler source can be reopened after reload, including existing Python ZIP functions. Deploy preserves the other ZIP files. Reading source requires `lambda:GetFunction`; editing also requires `lambda:UpdateFunctionCode`. Unsaved drafts remain in memory; download them before leaving. See [console limits](docs/lambda.md).

Use the operator session above. In the **AWS** console:

1. Open **IAM → Roles → Create role**. Choose **AWS service → Lambda**, leave
   permissions empty, and create a role named `hello-role`.
2. Open **Lambda → Create function**. Name it `hello-local` and choose
   `hello-role` under **Permissions → Existing role**. Choose **Create function**.
3. Save this as `lambda_function.py` and ZIP that file at the archive root
   using your usual ZIP tool. No host Python is needed to run the function.

```python
def lambda_handler(event, context):
    print("Hello from local Lambda", flush=True)
    return {"message": "hello", "event": event}
```

4. In the function's **Code** tab, upload the ZIP and choose **Deploy**.
5. Open **Test**, enter `{"name":"world"}`, select **Include log tail for this
   test**, then choose **Test**. The response contains your event; the log shows
   `Hello from local Lambda`.

Tail is **off by default**. For the API, explicitly send `X-Amz-Log-Type: Tail`
with synchronous Invoke; `X-Amz-Log-Result` contains base64-encoded output,
up to the last **4 KiB**. Logs can contain sensitive values. There is no
CloudWatch delivery or persistent history; stdout then stderr is not chronological
interleaving. Tail uses the existing `lambda:InvokeFunction` permission.

Python 3.12 only, 128 MiB guest memory, 1–30 second timeout, and no arbitrary
network access. The empty role is enough for this example; S3/SSM calls need
explicit role permissions. See [Lambda APIs, ZIP limits and SDK bridge](docs/lambda.md).

## Find a service guide

Each guide lists the supported operations and limits. An API does not imply a
console, CloudFormation adapter or Challenge integration.

- **Storage and data:** [S3](docs/s3.md) ([multipart](docs/s3-multipart.md)),
  [DynamoDB](docs/dynamodb.md), [Parameter Store](docs/ssm-parameters.md).
- **Systems Manager documents:** [definition and version history](docs/ssm-documents.md).
  List versions through the native read-only API or compare saved definitions in the console.
  This does not execute the documents.
- **Functions and messaging:** [Lambda](docs/lambda.md), [API Gateway](docs/apigateway.md),
  [SQS](docs/sqs.md), [SNS](docs/sns.md) (local HTTP/HTTPS subscription Console and reference; no external delivery),
  [CloudWatch Logs](docs/cloudwatch-logs.md).
- **Identity and secrets:** [IAM](docs/iam.md), [native SDK / STS](docs/sts-native.md),
  [Secrets Manager](docs/secrets-manager.md), [KMS](docs/kms.md), [WAFv2](docs/wafv2.md).
- **Networking:** [EC2/VPC discovery and VPC create/delete](docs/ec2-read-discovery.md),
  [route tables: CloudFormation readback, native tables and explicit subnet associations](docs/ec2-read-discovery.md#route-table-readback),
  [IPv4 subnet lifecycle for existing EC2-managed VPCs](docs/ec2-native-subnet.md),
  [security groups: CloudFormation readback and detached Console workflow](docs/ec2-read-discovery.md#security-group-readback),
  [detached network ACL API and Console workflow](docs/ec2-detached-network-acls.md),
  [VPC and one subnet in the Console](docs/ec2-read-discovery.md#console-vpc-and-one-subnet),
  [Elastic Load Balancing](docs/elb.md) (Lambda targets),
  [CloudFront VPC origins](docs/cloudfront-vpc-origin.md), [Route 53 public zones and one-PTR Console](docs/route53.md#console-browse-public-zones-and-create-one-ptr).
- **Scaling:** [Auto Scaling plans for local DynamoDB tables](docs/autoscaling-plans.md).
- **Infrastructure:** [CloudFormation](docs/cloudformation-rest.md),
  [read-only EC2 template preview in the Console](docs/ec2-read-discovery.md#instance-launch-preview-is-not-a-launch),
  [SSM value parameters](docs/cloudformation-ssm-parameters.md),
  [native IAM profiles](docs/cloudformation-iam-profiles.md),
  [pinned SSM policy](docs/iam-ssm-managed-policy.md).
- **Local AI inference (opt-in):** [Bedrock Runtime InvokeModel/Converse through your own
  Ollama or an optional macOS Foundation Models helper](docs/bedrock-runtime.md). Off by default; no cloud fallback, streaming or tool use.
- **Other clouds:** [Google Cloud Storage / Azure Blob Storage](docs/multicloud-storage.md),
  [Azure Blob conditional and CRC64 read limits](docs/azure-blob.md#single-range-download-subset),
  [App Engine (Python 3.12 WSGI versions, traffic splits, firewall)](docs/gcp-appengine.md),
  [Cloud DNS (bounded zone and record changes; Console SRV editor)](docs/acceptance/gcp-cloud-dns.md),
  [Google Cloud console](docs/gcp-console.md), [Azure console](docs/azure-console.md),
  [Azure Queue Storage (bounded named-queue operations)](docs/acceptance/azure-queue.md),
  [local Entra OIDC verification fixture (not login or SAML)](docs/azure-entra-verification-fixture.md),
  [opt-in Sakura Disk metadata](docs/sakura.md).
- **Cognito User Pools:** [Local metadata only](docs/design/cognito-user-pools.md). Verified local SigV4
  supports Create, Describe, List, and DeleteUserPool. No user sign-in, federation, or token issuance.
- **SimpleDB:** Native `PutAttributes` and `DeleteAttributes` accept one SDK-style `Expected` condition. It
  can compare a single value or require that an attribute be absent. The check and
  change share one transaction. Delete individual values, entire attributes or the item;
  repeat unconditional deletion safely. [Native `Select`](docs/simpledb-select.md) reads matching items or their names
  with a single predicate and paged results. Batch operations are not supported. A write that exceeds the [256-pair item limit](docs/simpledb-item-limits.md)
  is rejected atomically. In the [SimpleDB console](docs/simpledb-console.md),
  find item names by an optional exact attribute value, read an item, then conditionally replace or delete a single attribute.
- **Configuration-only catalogs:** [AWS](docs/aws-catalog.md),
  [Google Cloud](docs/gcp-catalog.md), [Azure Resource Manager](docs/azure-catalog.md).
  `--rds-private-postgres-topology` optionally validates private standalone
  PostgreSQL configuration against native EC2 on authenticated loopback. It
  starts no database and returns no endpoint.
  These do not run workloads, provide data planes or provision real cloud resources.
  AppFlow stores S3-to-S3 flow definitions only. An `OnDemand` definition
  rejects schedule settings; a `Scheduled` definition requires a nonblank
  schedule expression. AppFlow does not start jobs or move objects.
  AWS Batch validates job-queue time-limit actions against the queue family
  and allows time limits from 600 to 86,400 seconds; it does not execute them.
  Redshift can read tags on one existing stored resource ARN; it does not
  enumerate account-wide tags or execute SQL.

After signing in, open <http://127.0.0.1:4566/_simulator/docs/> for the API reference.
Examples are static; **Test Request → Send** makes a live local request.
See [API navigation](docs/api-reference.md) and [SDK connection examples](test/platform/README.md).
Use an explicit local endpoint and the guide's local authentication, never real cloud credentials.

## Before using it with others

- This is a learning simulator, not a production cloud replacement. Cloud APIs,
  IAM and console behavior are limited subsets. Unlisted services and operations
  are unsupported; roadmap entries are not working features.
- Secrets Manager values and KMS key material are stored in local SQLite.
  Do not use real secrets or treat this as production secret storage.
- Give each team its own **process, SQLite database and operator token**.
  Hosts authenticate users, authorize proxy routes and issue identity-bound
  participant tokens. Selecting an IAM identity or binding to localhost is not tenant isolation.
- Google Cloud Storage and Azure Blob use local ownership, not Google IAM or Azure RBAC.
- `--allow-remote` adds no TLS or tenant isolation. Use it only behind a trusted
  authenticated proxy or on an isolated network. The same OS user can access local files.
- Challenge support is separate from service support. Check the
  [challenge compatibility table](docs/catalog.md) before enabling a problem.

Read the [security boundaries](SECURITY.md) before hosting participants.

## Development

- [Working contract](AGENTS.md), [architecture](docs/design/0001-go-standalone.md),
  [parallel development](docs/contributing/parallel-development.md), [status](docs/development-status.md).
- [CI and required checks](docs/ci.md), [formal validation](formal/README.md),
  [current CI runs](https://github.com/susumutomita/TenkaCloudSimulator/actions/workflows/ci.yml).
  Results apply only to their tested revision; bounded models are not implementation proofs.
- Rebuild guides: [console](tools/console/README.md), [Scalar](tools/scalar/README.md),
  [Python/WASI](tools/python/README.md). Their development tools are not runtime dependencies.

## License and thanks to Kumo

This project is [MIT licensed](LICENSE). Bundled components' original notices are
available at `/_simulator/licenses/` and must be preserved on redistribution.

Thank you to [Kumo](https://github.com/sivchari/kumo)'s maintainers and contributors.
Its MIT-licensed work informed service registration, routing and SDK endpoints;
Kumo is not a runtime backend. Earlier adapted portions are recorded for
[IAM](third_party/kumo-iam.json), [API Gateway](third_party/kumo-apigateway.json),
[CloudWatch Logs](third_party/kumo-cloudwatch-logs.json) and [KMS](third_party/kumo-kms.json).
The identified runtime portions were independently reimplemented against public
AWS contracts. We retain the [original MIT notice](third_party/KUMO-LICENSE.txt)
and [reviewed replacement scope](third_party/independent-native-source.json).
These records are not a clean-room or legal certification.
