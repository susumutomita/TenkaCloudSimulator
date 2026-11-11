# Standard SSM parameters

The native JSON 1.1 service uses `X-Amz-Target: AmazonSSM.<operation>` at the
regional root endpoint. The existing console edits individual parameters. The
SDK/API also supports `DeleteParameters` for bounded batch cleanup; there is no
batch-delete console control.

## Batch deletion

[DeleteParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DeleteParameters.html)
accepts `Names`, an array of 1–10 names. This simulator retains its existing
Standard Parameter Store subset: String/StringList values, supported simulator
regions, parameter names of at most 1011 characters and 15 slashes, no reserved
`aws`/`ssm` prefix (case-insensitive), and no ARN, label or version selectors.
Names cannot contain spaces. A leading slash is part of the stored name.

- Validate the complete raw list before deduplication or mutation. Invalid names,
  nulls, duplicate JSON fields, unexpected fields and incorrectly cased fields
  fail with `ValidationException`.
- Authorize **`ssm:DeleteParameters`** against each distinct parameter ARN,
  including names that do not exist. `ssm:DeleteParameter` alone does not grant
  batch deletion. Explicit Deny wins; any denied target refuses the whole batch.
- Delete all present targets in one local SQLite transaction. Missing names are
  reported in `InvalidParameters`; deleted names are in `DeletedParameters`.
  Both arrays are always present. Each distinct name is reported once, preserving
  first-occurrence order within each array.
- A database, cancellation or commit failure before commit rolls back the batch
  and returns no success result. Commit is the success boundary; cancellation or
  loss of the HTTP connection after commit cannot undo a committed deletion.
- Other names, regions and separate team databases retain their state. A failed
  request does not confer access to names the caller was not allowed to inspect.

Atomicity, duplicate handling and response ordering are explicit simulator
bounds, not guarantees of identical AWS implementation behavior. AWS documents a
30-second wait before recreating deleted names; this simulator does **not** impose
that delay. A repeated batch reports already-deleted names as missing.

Use disposable parameter names when executing the API reference's destructive
example. Local host-token authentication is still required; dummy SDK signing
keys do not grant host access. Browser sessions restricted to a single parameter
cannot use the batch API. Service credentials, SecureString, parameter history,
labels and broad SSM command/session support are outside this increment.
