# Bounded local Cognito user-pool metadata

Scope for [#253](https://github.com/susumutomita/TenkaCloudSimulator/issues/253):
the four default AWS Cognito control-plane operations below. The default
surface creates no identity, credential, session, signing key, app client,
or login endpoint. It contacts no external service.

The ordinary local simulator binary accepts only its already verified local
SigV4 credentials, signed for `cognito-idp` in one configured simulator region.
Use a numeric loopback endpoint and POST AWS JSON 1.1 to `/` with target
`AWSCognitoIdentityProviderService.<operation>`. A cookie, an unsigned local
header token, dummy signing header, or upstream issuer JWT is not a Cognito
management credential. The simulator selects its own account, and the
verified signing region scopes all storage. Request fields cannot select
another owner, account or region. The pinned botocore 1.41.6 service model
and its real requests use `application/x-amz-json-1.1`. Older API reference
samples showing JSON 1.0 do not describe this local protocol profile.

| Native action | Bounded input | Local policy resource |
| --- | --- | --- |
| [CreateUserPool](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_CreateUserPool.html) | `PoolName`, optional `DeletionProtection=INACTIVE` | `*` |
| [DescribeUserPool](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_DescribeUserPool.html) | `UserPoolId` | exact regional user-pool ARN |
| [ListUserPools](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_ListUserPools.html) | `MaxResults` 1–60, optional `NextToken` | `*` on every page |
| [DeleteUserPool](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_DeleteUserPool.html) | `UserPoolId`; empty HTTP 200 response on success | exact regional user-pool ARN |

The action prefix and resource type follow the
[AWS Cognito IAM reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_cognito-idp.html).
This limited slice stores the local pool ID, friendly name, and creation time
in the team's SQLite database. Create and describe return the name, ID,
ARN, dates, and inactive deletion protection. List returns summaries, not
authentication settings. Storage is capped at 100 pools per configured
account/region. Names are 1–128 ASCII wire characters (blank names are not
accepted). Requests are capped at 4 KiB.

List uses an opaque signed keyset continuation. It is tied to the current
server process, scope, authenticated and exercise identities, and page size.
Continuations are not a snapshot of concurrent changes. Restart preserves
pool rows, not continuations. Delete only removes the exact scoped row.

Other fields and operations are explicit errors, including active deletion
protection, app clients, users, groups, metadata/IdP configuration, OAuth/SAML,
token issuance, managed login, MFA, triggers and CloudFormation resources.
There is no browser, console, Scalar, or ProtoShip compatibility claim.
The explicit local management opt-in is documented separately in
[bounded federation configuration](cognito-federation-configuration.md);
it does not enable a login flow.
