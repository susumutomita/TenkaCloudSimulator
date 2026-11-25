# SimpleDB first acceptance registration

## Current candidate and limits

The isolated SimpleDB candidate at `6aaac45c3fc33489b2024d755f8e9f1ccc0fd638`
contains the independently reviewed native implementation from `82b7ffc`.
It implements these five local Query APIs:

- `CreateDomain`: idempotent creation.
- `ListDomains`: bounded pagination.
- `DeleteDomain`: delete the domain and its items.
- `PutAttributes`: add or replace attribute values atomically.
- `GetAttributes`: read attributes, optionally filtered by name.

The composed server uses account `000000000000` and region `us-east-1`.
Another region is rejected, not selected from an arbitrary request header.
Requests use POST at the root, form encoding, Version `2009-04-15`, and explicit
`X-Simulator-Service: sdb`. The existing `NewAccess` local authentication wrapper
is required. The Gateway uses its trusted actor and the existing IAM decision
path; dummy AWS signing fields do not authenticate a caller. The caller-owned
SQLite database stores the same state across supported operations and restart.

This is not full SimpleDB compatibility. Select, conditional writes,
DomainMetadata and DeleteAttributes are unsupported. No console, external AWS
connection, additional credential mechanism or multiregion service claim is made.
Pagination tokens are local to one Store instance; continuation across restart
is not supported.

At that exact candidate, the six frozen native tests and twelve validation
subtests passed with the real server handler, authentication wrapper and SQLite,
using HTTP recorders without listeners. The independent leaf contract passed all
seven cases; focused Gateway and adapter checks also passed. These results were
independently reviewed. **Actual AWS SDK wire acceptance has not been run.**
A source change needs fresh exact-HEAD evidence.

## Isolated registry and default

The registry contains the original SQS suite and the four SimpleDB suites below.
There are no Azure Queue or GCP DNS registrations in this isolated branch.
The original SQS descriptor is unchanged.

`DEFAULT_SUITES` is only `sqs-lifecycle`. SimpleDB registration is not automatic
activation: all four SimpleDB surfaces must be selected explicitly. An unselected
surface is neither passed nor skipped evidence.

- `simpledb-first-oracle`: immutable TestWriter fixture/hash consistency only.
- `simpledb-first-native`: six named native tests and twelve required validation
  subtests; five native APIs, real handler and SQLite, not SDK/UI.
- `simpledb-first-sdk-wire`: pinned SDK, freshly built same-clean-HEAD CLI, four
  required wire cases. Missing dependencies, product failure, preflight-only,
  timeout and cleanup failure cannot pass.
- `simpledb-first-sdk-harness-controls`: three pure detector controls, not
  actual SDK execution.

In a separately approved local-only SDK test environment, the explicit selection
is:

```sh
python3 scripts/factory_acceptance.py \
  --suite simpledb-first-oracle \
  --suite simpledb-first-native \
  --suite simpledb-first-sdk-wire \
  --suite simpledb-first-sdk-harness-controls \
  --output /tmp/simpledb-acceptance.json
```

This command executes the SDK/CLI surface, including a local listener; it is not
a read-only preflight. Do not run it where local listeners or its required
environment configuration are disallowed.

## Immutable inputs and authority boundaries

The original TestWriter snapshot is
`7253c5b5dd9b0ba4574fbfe8bb2209228f99ba6d`, based on main
`a53131cd61030c8d238823478ec96917b19e95ef`. Its nine files remain byte-for-byte
unchanged. `test/factory/simpledb-registration.json` pins these files and the
fixed SDK entrypoint. Suite descriptors, exact commands and leaf paths are part
of the source binding. Receipts bind clean HEAD, inputs, toolchain, process exit,
required cases and log hashes.

A finding's registered scope requires all four suites and one of the five
operations above. Its implementation allowlist remains explicitly named leaf
Go files. Gateway/server/auth, adapter transport and fixture edits are not granted
authority by that leaf registration. The separately reviewed integration does
not silently broaden this allowlist or reset prior claim/attempt history.

## SDK and remaining release evidence

The SDK entrypoint checks the repository pins `boto3==1.42.0` and
`botocore==1.41.6` before a build. It installs nothing. All transitive dependencies
and the chosen Python version still need verification in the approved environment.
The fixture removes ambient AWS/BOTO and proxy settings, uses dummy credentials
and an authenticated numeric-loopback endpoint. Use it only where those initial
settings and local execution are already permitted, not to bypass restrictions.

The fixed entrypoint builds the exact clean candidate with the configured Go
tool. Receipts retain the Python invocation path, including a venv path.
Preflight, synthetic events and pure detector controls cannot substitute for
real SDK CRUD, restart, pagination, validation and verified cleanup.

Before merge, obtain the missing exact-HEAD SDK evidence, the normal
[working-contract gates](../../AGENTS.md), independent final review and
[formal applicability/model evidence](../formal-validation.md).
The current prep branch retains main's applicability file unchanged; it does
**not** classify or formally validate the SimpleDB runtime changes.

Run `python3 scripts/test-factory-acceptance.py` for the SQS/SimpleDB registration
detectors. Their synthetic events are controls, not product acceptance receipts.
No ledger action, PR publication, merge or release follows automatically.

## Historical registration stage

The initial registration-only snapshot had no SimpleDB implementation; its
native tests stopped at CreateDomain HTTP501 from the CloudFormation fallback.
That was an observed historical failure, not the current native result.
Its registration scripts also carried Azure/GCP descriptors. The isolated prep
removed those foreign registrations and adjusted only the registry-union detector.
The original functional oracles were not changed.
