# Azure Queue: local acceptance candidate

This branch implements a bounded local Queue data plane, not an Azure account,
Azure RBAC, cloud deployment or completed release. API version: `2023-11-03`.

## Local contract

The endpoint is `/_simulator/providers/azure-queue/{account}/{queue}`. It is
separate from the existing Azure Blob route. Eight operations are supported:
CreateQueue, DeleteQueue, PutMessage, GetMessages, PeekMessages, UpdateMessage,
DeleteMessage and ClearMessages. Queue state is persisted in provider-owned
SQLite tables. Visibility reservations and opaque pop receipts govern update
and deletion; Peek does not reserve messages. Deletion is observed before the
separate clear operation in the acceptance oracle.

The host binds account, owner and environment. Existing local access tokens
provide authentication; the configured exercise identity supplies owner access.
Persisted scope changes are rejected. Separate teams require separate process,
database and token authorities. These are not Entra, SharedKey, SAS or Azure IAM.

Bounds include 64 KiB message content and 512 KiB requests. TTL defaults to seven
days; supported positive TTLs are bounded to seven days, with -1 for no expiry.
Other Azure features, including account provisioning, queue listing and metadata,
are unsupported rather than silently simulated. The implementation remains the
source for the full validation and quota limits; this is not full Azure parity.

The local Azure Console can open and operate a queue by its exact name, not
list the account. The Console and the reference at
`/_simulator/docs/azure-queue.html` use the same native local data plane.
The dynamic specification and Console discovery are limited to the configured
owner. Native authorization is enforced independently of that visibility.

## Launch and verification prerequisites

Use a separately approved built simulator binary. Local startup supports
`--listen 127.0.0.1:0 --state /private/test/state.db --token-file /private/test/token`.
The CLI prints its allocated endpoint and creates a private local token. Do not
reuse production state, credentials or tokens.

The separate [CLI runner](../../test/e2e/azure_queue_cli.py) requires both an
explicit `--binary` path and independently recorded `--binary-sha256`. It uses
numeric-loopback listeners only. It checks Queue capabilities and the unchanged
eight-operation SDK oracle, then checks separate same-name queues, peer-token
refusal, state/receipt persistence across one restart and cleanup before success. It rejects configured
proxy settings rather than silently altering them; redirects are not followed.
The runner does not install dependencies, build the binary or discover credentials.

Official SDK prerequisite: `azure-storage-queue==12.18.1`, Python >=3.10.
Use a reviewed compatible Python version and the dedicated
[wheel hash pin](../../scripts/requirements-azure-queue-test.txt). Review the
[existing dependency pins](../../scripts/requirements-test.txt) separately, run
`pip check` and record their actual versions. The single-wheel hash does not
verify the entire environment. The runner checks the Queue SDK version, not every
existing dependency pin. No installation or real SDK result is implied here.

Offline boundary checks require only stdlib:

```sh
python3 -m unittest discover -s test/e2e -p test_azure_queue_cli.py -v
```

These tests use doubles and open no listener. They are not SDK acceptance.
Normal CI and independent review apply to the exact candidate. The separately
isolated browser and official SDK checks are not blanket merge gates; run the
applicable check before claiming its coverage. The optional browser runner keeps
Chromium sandboxing enabled and requires a loopback-only runner. The Console
and Scalar fixture or native-handler tests do not establish Terraform support.

## Evidence lineage, not an aggregate completion claim

- `92028d29ca4eb803f1d4a19a4cf177a8e1ad5b3b`: original leaf SQL mutation
  corrections were reviewed, but the original finding exhausted two attempts and
  remains blocked after a separate Peek zero-field defect was discovered. Its
  full-gate termination was not established. Do not reopen or mark it complete.
- `10911908e0623deeb57601f370547721a215458c`: separate narrow Peek XML correction
  independently reviewed, with scoped wire/lifecycle evidence. Not full gate proof.
- `c4a716f60144965ca6dfbc1bbd9d331de7a4b404`: local routing and both Sakura suites
  passed at this exact HEAD, with separate leaf/wire and Queue race results;
  independent technical review passed. Not actual official SDK acceptance.
- `5967a3fd7f6205d10d16b4b35442ce0f719fb0c2`: test-only CLI runner independently
  reviewed; its initial stdlib log was not bound to clean exact HEAD.
- `166332fdd47b0262d9d756ebfa7ca976f36f028b`: dedicated SDK hash pin; clean exact
  receipt records 13 stdlib boundary tests passing and dependency-file lint.
  No SDK installation, listener, real CLI run or full CI was performed.

Later documentation commits do not inherit these receipts as exact-HEAD passes.
The registered `AzureQueueLifecycle` model covers selected finite row transitions
and peer frames with explicit bounds. It is not a proof of the Go/SQLite
implementation or real Azure behavior. Formal applicability still requires exact
artifact binding and review. No SDK/full CI/PR-ready/release claim is made.
