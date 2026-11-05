# CloudFormation REST API → Lambda example

`examples/cloudformation-rest-lambda.json` creates a local SSM parameter, one
inline-policy IAM role, a Python Lambda, Regional REST API/resource/method,
immutable deployment, stage and bounded Lambda permission. It is a runnable
learning example, not a completed Challenge problem or a real AWS deployment.
The Go binary embeds all runtime dependencies; boto3 is only a development client.

Start the simulator as described in README. Use an explicit local endpoint,
dummy TCSIM SDK credentials and the operator token hook in `test/e2e/check.py`.
Then create the stack with the example's TemplateBody and `CAPABILITY_IAM`:

```python
from pathlib import Path
import boto3
from botocore.config import Config
endpoint = 'http://127.0.0.1:4566'
token = Path('simulator-token').read_text().strip()
cloudformation = boto3.client('cloudformation', endpoint_url=endpoint,
    region_name='us-east-1', aws_access_key_id='TCSIMLOCAL',
    aws_secret_access_key='dummy', config=Config(retries={'max_attempts': 0}))
cloudformation.meta.events.register('before-sign.cloudformation',
    lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token))
created = cloudformation.create_stack(StackName='rest-practice',
    TemplateBody=Path('examples/cloudformation-rest-lambda.json').read_text(),
    Parameters=[{'ParameterKey': 'Value', 'ParameterValue': 'safe-dummy'}],
    Capabilities=['CAPABILITY_IAM'])
stack = cloudformation.describe_stacks(StackName=created['StackId'])['Stacks'][0]
outputs = {item['OutputKey']: item['OutputValue'] for item in stack['Outputs']}
# GET endpoint + outputs['LocalPath'], using the local host access token/session.
# The response is a non-executing attachment containing JSON value/path fields.
cloudformation.delete_stack(StackName=created['StackId'])
```

No automatic public AWS URL exists. `LocalPath` is the authenticated simulator
route. `/config` returns the saved-role SSM value; asking for `/private/dummy`
through `?name=` returns the function's403 because its policy only permits the
example's `/cfn-rest/public`. Two teams need separate process/SQLite/token
instances. IAM identity selection alone does not isolate participants.

## Supported resources and boundaries

The existing S3 Bucket, SSM Parameter, IAM Role and inline Python Lambda Function
remain supported. This slice adds exactly:

| Type | Supported properties/behavior |
| --- | --- |
| AWS::ApiGateway::RestApi | Name/Description/Tags, explicit EndpointConfiguration.Types:[REGIONAL]; Ref gives API ID, GetAtt RootResourceId |
| AWS::ApiGateway::Resource | RestApiId, ParentId, PathPart; same-stack API only, Ref gives resource ID |
| AWS::ApiGateway::Method | NONE, supported HTTP method, optional greedy-proxy binding, POST AWS_PROXY local Lambda integration; no API keys |
| AWS::ApiGateway::Deployment | RestApiId/Description; explicit DependsOn on methods; immutable snapshot |
| AWS::ApiGateway::Stage | RestApiId/DeploymentId/StageName/Variables; Ref gives stage name |
| AWS::Lambda::Permission | Local function name/ARN, lambda:InvokeFunction, apigateway.amazonaws.com, bounded same-region/account execute-api SourceArn |

Native API/CFN use the same transaction-aware primitives and SQLite rows. Create
is atomic; cleanup runs in reverse dependency order. API children must belong to
an API created in the same stack. Deployment ordering must be explicit where a
reference alone cannot express dependency on methods. A template that declares
unsupported fields fails rather than producing a partially represented resource.

Per-row fingerprints protect native drift: changed/new/unowned API rows block
DeleteStack; already removed unchanged owned rows do not strand cleanup. A
missing stack grant may be cleaned up without deleting another statement added
to the same function policy. An altered replacement grant is preserved and blocks
cleanup. Store/SQL read failures are not interpreted as missing resources.

The caller still needs cloudformation actions and underlying API Gateway
POST/PUT/DELETE, Lambda AddPermission/RemovePermission, Lambda Create/Delete,
iam:PassRole, SSM Put/Delete and IAM role/inline-policy actions. CAPABILITY_IAM
acknowledges role creation; it does not grant permissions. Native role trust
validation is unchanged. Existing CFN roles retain their documented legacy
metadata-only trust behavior; arbitrary managed-policy permissions are not added.
CFN-created IAM roles remain legacy aggregate identities: native IAM GetRole/
ListRoles and the native role picker do not return them. Migrating that IAM
visibility is still pending. API Gateway, Lambda and SSM resources do share their
actual native SQLite state and existing console views.

No UpdateStack/change sets, authorizers, API Gateway v2, WAF, custom resources,
guest HTTP networking, active browser application hosting or full problem grading
is enabled by this change. Native stage edits/method deletion remain unsupported.
The existing bounded local saved-role SSM/S3 SDK is the only guest SDK transport.

## Verification

`test/e2e/cloudformation_rest.py`, registered in the normal E2E suite, runs real
binaries/SDKs in two team environments: CreateStack → native API/grant inspection
→ actual local HTTP → saved-role SSM access/denial → DeleteStack/native cleanup.
It checks cross-team token rejection, persistence across process restart and
preservation of unrelated SSM state. It does not return fabricated grading flags.
The Go adapter tests additionally cover rollback, foreign native children/policy
statements, missing owned state and immutable deployment behavior.

## Parameters and conditions

The local compiler accepts String and Number parameters with bounded defaults,
AllowedValues, String AllowedPattern/MinLength/MaxLength, and Number MinValue/
MaxValue. Every definition and default is checked, even when a supplied value
replaces that default. Unknown supplied parameters fail. The local String length
unit is bytes, not a claim of AWS Unicode-length parity. Number comparison uses
bounded exact decimal rational values (at most 128 text bytes, exponent −308…308);
it is not general AWS parameter-type or numeric compatibility.

Conditions support `Fn::Equals` over literals or parameter/pseudo-parameter Ref,
and `{Condition: name}` aliases. All condition definitions must be valid, including
unused aliases; unknown names and cycles fail. And, Or, Not, mappings, dynamic
references and general intrinsic languages remain unsupported. Local bounds are
100 parameter/condition definitions, depth 32 and 10,000 conditional-value nodes.
String scalars are capped at 51,200 bytes; the TemplateBody limit remains 51,200.

A resource or output may have a named Condition. `Fn::If` selects only the chosen
value; direct `AWS::NoValue` arms remove a map property or list member. Both arms
must have supported syntax and known Ref/GetAtt/Sub symbols. Only selected active
links may require active resources. Disabled declarations still undergo resource
kind/attribute/property validation and do not authorize unsupported types. This
is a pure compile step before native state mutation; it adds no UpdateStack,
custom resources, new resource types or host problem-specific behavior.

The exact original catalog fixture is retained at
`test/fixtures/crypto-battle-a749/template.yaml` with its Apache-2.0 license and
SHA-256 `fb2af9be3ba67619b80de0ad8f00dc102aaade740742aea5861c9039d8bd09e6`.
Its default-disabled ScoreSteal condition omits the optional SSM item/permissions
and conditioned outputs. A new enabled stack adds the SSM item and matching
inline permissions. `test/e2e/cloudformation_crypto.py` is the mandatory proposed
real-CLI/boto3 acceptance: unchanged fixture, two separate DB/token environments,
defaults and enabled mode, actual SSM permissions/refusals, invalid-input and late
name-conflict rollback, restart and cleanup. At `2d76646` the helper passed against a regular embedded CLI: 73 SDK logical
calls/sends/raw HTTP responses/parsed responses, plus nine auxiliary HTTP pairs,
across four CLI process incarnations. This dated result is not the new combined
61-case E2E after the normal main139eb merge, which remains pending. The full 429-case formal gate passed at `c298972`; this is not host grading acceptance.
It is not a game or grading acceptance. The standalone final DeleteStack ACK
correction from `7d56d9d` is now integrated as source: exactly one selected stack
row must be deleted and both selected stack and owned-claim records must be
absent before commit. A refusal rolls back the transaction. The new finite model
covers only these final records; it assumes physical resource handlers succeed
and does not prove their individual DELETE acknowledgement. Combined native,
E2E, formal and coverage gates still remain pending for this changed candidate.
The alias-depth cache correction additionally validates structural depth
independently of sorted-name/memoization order; native regressions cover both
naming directions, shared cached children and cycles. The separate
CFNConditionMemoDepth model is now registered with twelve properties and nine
controls. Its scoped `cca79267` run passed; the broad admission model is not its
proof, and neither model proves arbitrary Go graphs or unbounded-depth behavior.
The final-ACK CFG has an explicitly reviewed formatting-only adaptation to the
repository grammar: the same five invariants each use a named INVARIANT line;
its standalone TLA body, constants and finite bounds are unchanged.

`NoEcho` is accepted only as Boolean metadata. It does **not** guarantee secret
storage or redaction: dummy parameter values persist in SQLite and are visible
to the operator through DescribeStacks, including conditioned raw outputs.
Participant tokens are host-issued local role-bound credentials, not STS; native
CFN rejects their stack-output reads. CFN roles retain the legacy aggregate model,
so native IAM GetRole/ListRoles do not expose them. Trust and allowed
ManagedPolicyArns metadata do not grant AWS sign-in, federation, managed-policy
or external-account authority. Host coordination, private-output filtering and
actual crypto-battle scoring remain outside this simulator acceptance.

A separate candidate check now verifies every selected SSM parameter (region/name) and S3 bucket (global name) is absent at the very end of the same deletion transaction, after all resource, ownership and stack cleanup writes. It detects ignored physical DELETEs and late reinsertion, rolling back on failure. Existing missing-resource and nonempty-bucket behavior is preserved. This does not change the standalone native S3 DeleteBucket API or promise acknowledgement checks for other resource types. The 196 native/scoped model receipts are historical inputs; the current combined 429-case formal suite passed at `c298972`; 61 E2E/start and hosted CI remain pending.
