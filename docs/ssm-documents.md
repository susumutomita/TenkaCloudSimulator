# Bounded SSM document management

This increment manages local document definitions only. It does not execute a
shell script, implement SendCommand, connect an SSM agent or establish Battle
readiness. `Status: Active` means a definition has been committed, not that a
command ran. The native endpoint exposes only definition management.

## Contract and authorization

The native adapter uses the existing regional root JSON 1.1 endpoint,
`X-Amz-Target: AmazonSSM.<operation>`, after host-token/session and existing SSM
method/path/dummy-credential/region validation. IAM uses one immutable policy
snapshot for all actions required by a request; explicit Deny wins. A console
handoff bound to one parameter cannot use these document APIs, even when its
underlying principal has ssm:* permission.

| Operation and primary specification | Fields in this subset | IAM resource |
| --- | --- | --- |
| [CreateDocument](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_CreateDocument.html) | Name, Content; optional DocumentType=Command, DocumentFormat=JSON | document ARN, ssm:CreateDocument |
| [GetDocument](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetDocument.html) | Name; optional DocumentVersion, DocumentFormat=JSON | document ARN, ssm:GetDocument |
| [DescribeDocument](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DescribeDocument.html) | Name; optional DocumentVersion | document ARN, ssm:DescribeDocument |
| [ListDocuments](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_ListDocuments.html) | optional MaxResults=1..50, NextToken | *, ssm:ListDocuments |
| [UpdateDocument](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_UpdateDocument.html) | Name, Content, DocumentVersion; optional DocumentFormat=JSON | document ARN, ssm:UpdateDocument **and ssm:GetDocument** |
| [DeleteDocument](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DeleteDocument.html) | Name only, removes all versions | document ARN, ssm:DeleteDocument |

The [official IAM operation/action table](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ssm.html)
was inspected on 2026-11-11. No accepted document carries a role or tags;
PassRole and AddTagsToResource cannot be triggered by this subset. Document ARNs
are `arn:aws:ssm:<region>:000000000000:document/<name>`. Cross-account/ARN document
names, policy Conditions, tags and sharing are unsupported, not implied grants.

Names use `[a-zA-Z0-9_.-]{3,128}`, rejecting case-insensitive aws/amazon/amzn
prefixes. Regions are the simulator's existing supported regions. The same name
in another region or another team's database is independent.

## Definitions, versions and local limits

Only JSON Command documents with schemaVersion `2.2` are accepted. Content is
1..65536 UTF-8 bytes and must contain 1..8 uniquely named mainSteps. Each step is
`aws:runShellScript` with literal `inputs.runCommand` strings (1..64 strings,
at most 8192 bytes each). Step names contain 1..128 ASCII letters, digits or
underscores; description is optional and at most 1024 bytes. Parameter
interpolation, parameters, preconditions, plugin options, nested documents,
attachments, Requires and unsupported schema fields are rejected. This is a
strict local definition subset, not AWS-wide document compatibility.

Definitions are never executed by this component. NUL and interpolation markers
are refused; literal command strings are retained as data, not sanitized into a
claim that they are safe to execute elsewhere.

Creation commits version 1. Update must name the current latest version or
`$LATEST`; it appends a new immutable version and rejects unchanged exact content.
Default remains version 1. Get/Describe accept an omitted selector or `$DEFAULT`,
`$LATEST`, or a canonical positive version number. Default-version updates,
version-specific deletion, VersionName and Force are outside the subset.

Local caps are 100 documents across the database, 100 versions per document and
8 MiB of retained content across all documents/versions. Requests are capped at
512 KiB to accommodate JSON escaping of a 64 KiB content string. Duplicate keys,
unknown or incorrectly cased fields, nulls, trailing JSON and nesting beyond 16
are rejected rather than normalized. These limits are not AWS quotas.

List returns default-version identifiers in name order with pages of at most 50.
Continuation tokens bind region, principal, page size and the current full
regional content/version snapshot. Mutation invalidates outstanding tokens with
InvalidNextToken rather than silently switching snapshots. Tokens are not
credentials; every page rechecks ListDocuments permission. This is a deliberately
stricter local pagination contract, not an AWS snapshot guarantee.

Save, quota checks, latest pointer and version insertion share one immediate
SQLite transaction. Delete and cascading version cleanup are one transaction.
Errors before commit do not acknowledge success; an HTTP disconnect after commit
cannot roll back a committed write. A repeated delete fails InvalidDocument.
The internal store itself grants no IAM authority. Host isolation continues to
require separate process/database/token authorities per team.

Not included: execution, inventory, sessions, YAML/TEXT, automation, associations,
shared/AWS-owned documents, tags, attachments, CloudFormation, console and runtime
permission grants. SDK compatibility and full repository acceptance require the
integrated endpoint's tests; direct adapter/store tests are not that evidence.

## Finite model boundary

`SSMDocumentState` models two addresses, two abstract content values, at most two
versions and one live document. Addresses abstract independently authorized
region/name pairs. It checks explicit denial, unchanged state on refusal,
address isolation, immutable history/default, latest-version preconditions and
full deletion. Version 2 is an exploration cutoff, not the runtime limit.
The model does not prove JSON/IAM parsing, SQL transactions, quotas in bytes,
pagination, concurrency, restart, disk durability or actual execution. These
need separate native tests. The registered mutation controls exercise these finite claims; they do not
establish implementation refinement.
