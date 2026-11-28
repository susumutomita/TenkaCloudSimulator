# Native SDK credentials and STS

This candidate adds a bounded, authenticated AWS SDK path for unchanged
CloudFormation/SSM/Lambda exercises. It is not a real AWS identity service. Configure
the client endpoint to the running simulator's numeric-loopback URL, use local
access key `TCSIMOPERATOR`, and use the private simulator token-file value as
the secret key. Read the file privately in the integration process; never log,
commit, send to participants, or use this credential against a real AWS URL.
Existing console sessions and dummy SDK clients with `X-Simulator-Token` keep
their separate authentication behavior.

The native operator identity belongs to synthetic account `999999999999`.
Resources and assumed roles belong to synthetic account `000000000000`. This
distinction permits a host to exercise its existing cross-account checks without
creating, using, or charging an actual AWS account. It does not turn one
simulator process into a multi-tenant account service: separate teams still need
separate process, SQLite, endpoint, credential authority and host authorization.

## Supported native surface

- Header-signed SigV4 POST requests at `/` for STS, IAM, CloudFormation and SSM.
- Header-signed SigV4 Lambda REST requests below `/2015-03-31/functions`:
  ListFunctions, CreateFunction, GetFunctionConfiguration, Invoke,
  UpdateFunctionCode and DeleteFunction. Function names must be plain names
  (no ARN, no percent-escaping) and the request has no query string, so
  `Qualifier`, pagination and resource-policy paths are unavailable here.
  Bodies are limited to 384 KiB for a base64 ZIP. An assumed-role session is
  authorized by its role snapshot. The function's own SDK calls are always
  evaluated as its saved execution role: the caller's session policy, identity
  and console or terminal boundary are never carried into them. A browser
  session bound to one SSM parameter cannot reach Lambda.
- The authenticated capability document advertises `nativeSdk: {version: 1,
  services: [...]}` with the enabled signing services, so a host can
  check for `lambda` before offering a problem that needs it. An embedding
  without the Access wrapper advertises none.
- `GetCallerIdentity` and `AssumeRole`, with ordinary official SDK request and
  credential response shapes.
- Native IAM roles and existing CloudFormation-owned role definitions. A legacy
  identity-policy name by itself is not an assumable role.
- Explicit account-root/account-ID or IAM-role principals, Allow/Deny,
  `sts:AssumeRole`, and exact `StringEquals` `sts:ExternalId` trust conditions.
  Caller identity permission and role trust must both allow assumption.
- Role duration limits, a 900-second minimum, and a 3,600-second chaining limit.
  Expired or invalid credentials fail closed, including expiry while receiving a
  request. Restart invalidates all temporary credentials.

Unsigned payloads, presigned/query authentication, other signing services,
session policies/managed session policies, session tags, SourceIdentity, MFA,
web identity and federation are not implemented. Unsupported fields fail instead
of being ignored. AWS console federation and EC2/SSM shell sessions are not
enabled by this work.

The server verifies the signature, body, timestamp, host, dispatch selectors and
session token before accepting a request. A role's generation and current policy
are read from one SQLite snapshot. Authorization uses that immutable policy
snapshot, so an old credential cannot inherit a deleted/recreated role's new
permissions. A request already authenticated against a generation may complete
under that snapshot; later requests see deletion or policy changes. Stored
credentials and signing secrets are process-local and never included in reports.

## STS Query contract

Send a form-encoded body of at most 32 KiB with one value per field,
`Action` and `Version=2011-06-15`. Sign for `sts` in `us-east-1`,
`us-west-2`, `ap-northeast-1` or `eu-west-1` at the same local endpoint.
Roles and sessions are shared across those signing Regions within one process.
Successful responses include `ResponseMetadata.RequestId`. AWS publishes its
own regional and global endpoints. [AWS STS endpoints and quotas](https://docs.aws.amazon.com/general/latest/gr/sts.html)

| Operation | Local request | HTTP 200 XML result |
| --- | --- | --- |
| [GetCallerIdentity](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html) | Only the two common fields. AWS requires no IAM permission for this call. The simulator authenticates the signing credentials and performs no additional IAM action check. | `Account`, `Arn` and `UserId` identify the signer. The operator has the root ARN of `999999999999` and user ID `simulator-admin`. An assumed role has account `000000000000`, an STS `assumed-role/<role>/<session>` ARN and user ID `<role-id>:<session>`. |
| [AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html) | Requires `RoleArn` and `RoleSessionName`. Accepts `DurationSeconds` and `ExternalId`. All other fields are rejected, including session policies, tags, MFA, `SourceIdentity`, `ProvidedContexts` and `MinimumSessionTokenSize`. | `Credentials` contains `AccessKeyId`, `SecretAccessKey`, `SessionToken` and `Expiration`. `AssumedRoleUser` contains the session's `Arn` and `AssumedRoleId`. |

The target ARN is `arn:aws:iam::000000000000:role/<name>`. The role name
syntax is 1–64 ASCII letters, digits, underscores or hyphens. `RoleSessionName`
accepts 2–64 ASCII letters, digits or `_+=,.@-`. `ExternalId` accepts
2–1,224 of the same characters plus `:/`. `DurationSeconds` defaults to
3,600 and accepts integers from 900 through 43,200. The role's
`MaxSessionDuration` caps the request (default 3,600, configurable up to
43,200). Role chaining further caps it at 3,600.

AWS permits a same-account IAM user explicitly named in a role's trust policy
to assume it without a separate identity-policy grant. The local caller must
pass a decision for `sts:AssumeRole` on the target ARN and match its trust
policy. The built-in operator passes the caller decision. A chained session
uses its current role policy and matches trust as its IAM role. A matching
trust `Deny` wins. Native IAM or CloudFormation supplies the stored target
role. Sessions are held by one process, up to 1,000 unexpired sessions across
its signing Regions. With a file-backed database, restarting preserves role
definitions and discards sessions. AWS credentials expire at the returned time.
An authorized caller can request fresh credentials. [AWS temporary credentials](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_temp.html)

| Local failure | HTTP status and STS code |
| --- | --- |
| Invalid or expired local SDK credentials | 403 `InvalidClientTokenId` |
| Malformed form, wrong version, duplicate or unsupported field, invalid session name, external ID or duration, or invalid stored trust document | 400 `ValidationError` |
| Missing or unavailable role ARN, caller or trust denial (including an unmet external ID condition), or session issuance failure such as the 1,000-session limit | 403 `AccessDenied` |
| Another STS action in an authenticated Query request, including `GetSessionToken` or `AssumeRoleWithSAML` | 501 `NotImplemented` |

AWS also documents errors for features outside this surface, such as
`PackedPolicyTooLarge` for session policies and tags. The local mappings follow
the [Query handler](../internal/aws/sts.go), [role and trust checks](../internal/aws/sts_roles.go)
and [SDK authentication wrapper](../internal/server/access_sdk.go).

## Verification and limits

`test/e2e/sts_sdk.py` uses the official boto3 client without custom auth hooks.
It creates a least-privilege deployment role, verifies wrong ExternalId rejection,
assumes the role, deploys the unchanged pinned original hello-world template,
issues the original participant role, reads its actual SSM value, rejects answer
output access and mutation, deletes the stack, and verifies revoked access and
resource cleanup, plus actual committed deployment/deletion event history. The fixture source/hash/license are in
`internal/aws/testdata/sts-original/README.md`.

Focused Go tests cover trust/denial, signature mutation, credential isolation,
expiry, generation replacement, native/CloudFormation snapshots and malformed
requests. The finite STS model is described in
[the model candidate](sts-formal-candidate.md) and is registered with eight
required sensitivity controls. It assumes a coherent generation/policy snapshot
and abstracts signature, trust and tenant decisions; it does not prove
cryptography, SQL/Go refinement or actual cross-process isolation.
[The separate event/history model](formal-cfn-event-history.md) covers bounded
transactional event publication, retention and generation-scoped read rules.
Final exact-source applicability and complete gate results remain separate from
registration and historical scoped execution.

These checks are API/runtime evidence. They do not establish a functioning
CloudFormation creation GUI, full participant Portal scoring, deployment-log
display, or Hello World Battle. Those are separate acceptance stages and must
be exercised through their actual host and participant surfaces.
