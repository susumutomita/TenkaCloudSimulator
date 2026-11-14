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
  services: [...]}` with exactly the signing services above, so a host can
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
