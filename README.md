# TenkaCloud Simulator

English | [日本語](README.ja.md)

An independent learning simulator for cloud APIs and console workflows, without a cloud account.
AWS is the main focus, with limited Google Cloud, Azure and optional Sakura support.
Supported console actions and APIs share the same SQLite state.

One Go binary includes the consoles, API reference, SQLite and Python Lambda runtime.
**Running a built binary needs no Go, Node.js, host Python, Docker, external database
or real cloud connection. Use dummy credentials and practice data only.**

## Start and sign in

From the repository root, with **Go 1.26, GNU make and a POSIX shell** installed:

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
- **Functions and messaging:** [Lambda](docs/lambda.md), [API Gateway](docs/apigateway.md),
  [SQS](docs/sqs.md), [SNS](docs/sns.md), [CloudWatch Logs](docs/cloudwatch-logs.md).
- **Identity and secrets:** [IAM](docs/iam.md), [native SDK / STS](docs/sts-native.md),
  [Secrets Manager](docs/secrets-manager.md), [KMS](docs/kms.md), [WAFv2](docs/wafv2.md).
- **Networking:** [Elastic Load Balancing](docs/elb.md) (Lambda targets),
  [Route 53](docs/route53.md).
- **Infrastructure:** [CloudFormation](docs/cloudformation-rest.md),
  [SSM value parameters](docs/cloudformation-ssm-parameters.md),
  [native IAM profiles](docs/cloudformation-iam-profiles.md),
  [pinned SSM policy](docs/iam-ssm-managed-policy.md).
- **Other clouds:** [Google Cloud Storage / Azure Blob Storage](docs/multicloud-storage.md),
  [Google Cloud console](docs/gcp-console.md), [Azure console](docs/azure-console.md),
  [opt-in Sakura Disk metadata](docs/sakura.md).
- **Configuration-only catalogs:** [AWS](docs/aws-catalog.md),
  [Google Cloud](docs/gcp-catalog.md), [Azure Resource Manager](docs/azure-catalog.md).
  These do not run workloads, provide data planes or provision real cloud resources.

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
