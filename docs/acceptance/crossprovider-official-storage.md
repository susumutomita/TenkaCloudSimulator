# Official storage SDK acceptance

The executable acceptance uses real official Python clients against two
authenticated local simulator processes. Development packages and their resolved
HTTP/transport dependencies are pinned in
[`scripts/requirements-test.txt`](../../scripts/requirements-test.txt). They are
not Go single-binary runtime dependencies. No Google/Azure account, cloud
credential discovery, real cloud request, or third-party emulator is used.

## Run and fail-closed behavior

```sh
python3 -m venv /tmp/tcsim-tests
/tmp/tcsim-tests/bin/pip install -r scripts/requirements-test.txt
make build
/tmp/tcsim-tests/bin/python test/e2e/test_crossprovider_official_storage.py
/tmp/tcsim-tests/bin/python test/e2e/crossprovider_official_storage.py \
  --binary "$PWD/dist/tenkacloud-simulator" --provider both
```

`--provider gcp` or `azure` selects one SDK independently. Missing selected
packages produces `FAIL/NOT-RUN`, names the missing packages and exits 2 before
creating a process or making a request. API, state, serialization, assertion,
startup and cleanup failures exit 1 with `FAIL/NOT-ACCEPTED`. Safe failure reports
include the phase, exception class, harness source location, and bounded SDK
frame names. Denial failures include only numeric HTTP statuses and the SDK
exception class. Exception text, request headers, CLI logs and tokens are never
printed. Optimized Python (`-O`) is refused so it cannot disable assertions.
There are no skips, fake clients, raw-HTTP substitutions for SDK operations, or
passes on missing infrastructure.

Six focused harness checks cover absent-SDK failure, refusal of optimized
execution, finite list bounds, rejection of unrelated errors as HTTP denial,
safe failure diagnostics, and real Azure path preparation. They are harness
checks, not substitutes for the actual two-process runtime acceptance.

## Pinned clients and local authentication

| Client/transport | Dependency pin | Configuration |
|---|---|---|
| Google Cloud Storage | `google-cloud-storage==3.17.1` | Explicit project, endpoint, anonymous credentials and token header |
| Google authentication | `google-auth==2.62.1` | Official `AuthorizedSession`; OAuth refresh disabled |
| Azure Blob Storage | `azure-storage-blob==12.31.1` | Explicit account URL, anonymous credential, API version `2023-11-03` and token hook |
| Azure core | `azure-core==1.41.0` | Normal SDK HTTP/error handling, retries disabled |
| HTTP transport | `requests==2.34.2` | Real Requests preparation/transfer; no URL or serializer rewriting |

GCP endpoint is `/_simulator/providers/gcp`, with project `tenkacloud-local`.
Azure account URL is `/_simulator/providers/azure/tenkacloudlocal`. These are the
existing provider prefixes. The token uses public SDK header/hook configuration.
Raw HTTP is used only for CLI readiness and the participant-token control API;
all object operations use the official SDKs.

Google's default `AuthorizedSession` tries to refresh credentials after 401.
`AnonymousCredentials.refresh` raises `InvalidOperation`, hiding the HTTP denial.
Local simulator tokens are not Google OAuth credentials, so this fixture uses
the official session with `refresh_status_codes=()` and
`max_refresh_attempts=0`, injected through the storage Client constructor's
`_http` argument. Upstream documents that argument but considers it private;
this is a pinned, tested local configuration, not a claim about default-client
behavior or future versions. The original HTTP/error handling remains intact.
The server must return GCP's nested `error` object for a denied GCP request,
allowing the official parser to expose the real 401. Parsing failures are never
accepted as substitute authentication failures.

GCP metadata/list requests use `projection='noAcl'`. Small
`upload_from_string` uses real multipart transfer, MD5 and generation conditions.
Fresh `bucket.blob(name)` references select the live object; a loaded Blob can
add a generation selector. Only the current generation is supported. Older
history, resumable uploads, general ACL projections, and other SDK defaults are
outside this acceptance.

Azure uploads use `ContentSettings` with body-derived MD5. Downloads retain
`download_blob().readall()` and its real range requests. No Range header is
removed and no download is replaced with raw GET. Staged blocks, large uploads,
SAS/SharedKey/Entra authentication and newer default API versions remain outside
this bounded slice.

## Exact names and explicit Azure exclusions

GCP requires all twelve original names without collisions:

`a/b`, `a%2Fb`, `a//b`, `a/./b`, `a/../b`, `a+b`, `a b`, `日本語/雪`,
`a%252Fb`, `/leading`, `tail/`, and `a/acl/entity`.

The official Azure client exposes slash-separated names to Requests. Real
`PreparedRequest` processing normalizes two names before HTTP transfer:

| Requested Azure SDK key | Prepared key | Disposition |
|---|---|---|
| `a/./b` | `a/b` | Explicitly measured exclusion from positive SDK exact-name lifecycle |
| `a/../b` | `b` | Explicitly measured exclusion from positive SDK exact-name lifecycle |

The other ten names retain all exact-key/byte assertions. Before runtime tests,
the harness constructs real Azure Blob clients and prepares their requests using
real Requests without opening a connection. It asserts these exact two known
normalizations and that all other names are unchanged. Unexpected normalization
or an altered exclusion fails; it cannot silently shrink the accepted set.
Success JSON always includes `excluded_sdk_key_cases` with the original key,
prepared key, provider and concrete cause, plus `exact_key_cases_per_team`.

This corrects the scope of a previously unexecuted source candidate. It does not
change native provider path behavior, the full GCP oracle, or existing native
exact-path regressions. The server cannot reconstruct names collapsed before
it receives them. No transport patch manufactures parity, and full Azure
exact-key compatibility is not claimed.

## Required runtime assertions

Two authenticated numeric-loopback CLI processes use separate temporary SQLite
files and persistent operator-token files, with identical resource names and
distinct team contents. Their `PATH` contains no runtime executables. Health wait
is 25 seconds, shutdown 8 seconds, per-call SDK timeouts 3 seconds and the whole
script deadline 300 seconds. Retries are disabled and list results capped at 100.

Connections and name resolution are restricted to the two allocated numeric
loopback ports. Temporary proxy-environment removal applies only inside this
local harness; nonlocal requests remain forbidden. This is a harness boundary,
not an OS sandbox claim. Port-allocation races and cleanup errors fail acceptance.

Required assertions include:

- Identical names retain distinct team bytes through process restart with the
  same persistent operator tokens.
- Complete exact-name sets, pagination, per-object bytes and MD5 survive SDK
  transport and native persistence.
- GCP create-only/matching/stale-generation and Azure matching/stale-ETag
  operations preserve state on failure and advance versions on success.
- Missing and other-team tokens produce SDK-visible 401. A participant fixed
  to `storage-reader` gets 403 on a write, leaving the full set/bytes unchanged.
- GCP US/EU location metadata is correct. This is location metadata, not physical
  regional isolation; Azure's data-plane fixture exposes no region assertion.
- SDK deletion and not-found checks remove all objects and buckets/containers;
  processes stop and temporary DB/token files are removed.

`storage-reader` does not imply a functioning read role. Current provider
factories deny nonowners, including reads. This acceptance proves denied
nonowner writes, not Google IAM, Azure RBAC or an AWS-policy grant.

## Recorded execution and limits

On 2026-11-05, Azure's ten-key lifecycle passed against clean main
`f5dba056da9bf69df047b391a45d06a3aff3dc66`, built with Go 1.26.10 and the normal
Makefile flags (`CGO_ENABLED=0`, intact VCS stamp, `vcs.modified=false`).

After adding the local Google session configuration and the integration owner's
native GCP 401 envelope fix, **both providers passed** against the root integration
working binary. GCP exercised all twelve keys and Azure all ten disclosed keys
in each of two processes, including restart/CAS/checksum/range/pagination,
missing/cross-team/nonowner rejection, and cleanup. All six focused harness
checks and dependency consistency (`pip check`) also passed.

That combined result is a focused working-tree execution, not immutable full-HEAD
CI evidence. The integration owner must rerun the mandatory acceptance and full
Go/race/coverage, browser/accessibility, formal and hosted CI gates after composing
and committing all changes. The PR records the final tested commit and merge.
No authenticated cloud visual comparison, real-cloud comparison, Go refinement
proof, all-version SDK support, or full provider conformance is claimed.

## Primary references

- [Google Client configuration](https://docs.cloud.google.com/python/docs/reference/storage/latest/google.cloud.storage.client.Client)
- [Google official AuthorizedSession](https://google-auth.readthedocs.io/en/latest/reference/google.auth.transport.requests.html#google.auth.transport.requests.AuthorizedSession)
- [Google Blob upload/download/conditions](https://docs.cloud.google.com/python/docs/reference/storage/latest/google.cloud.storage.blob.Blob)
- [Google pinned distribution](https://pypi.org/project/google-cloud-storage/3.17.1/)
- [Azure BlobServiceClient](https://learn.microsoft.com/en-us/python/api/azure-storage-blob/azure.storage.blob.blobserviceclient?view=azure-python)
- [Azure BlobClient conditions/download](https://learn.microsoft.com/en-us/python/api/azure-storage-blob/azure.storage.blob.blobclient?view=azure-python)
- [Azure pinned distribution](https://pypi.org/project/azure-storage-blob/12.31.1/)
- [Requests pinned distribution](https://pypi.org/project/requests/2.34.2/)

Native contracts: [Cloud Storage](../gcp-storage.md), [Blob Storage](../azure-blob.md),
[trusted Actor](../provider-auth-context.md) and
[native/browser acceptance](crossprovider-console.md).
