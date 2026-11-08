# CloudFormation local SSM parameter values

CloudFormation accepts these two additional template parameter types:

- `AWS::SSM::Parameter::Value<String>`
- `AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>`

Both read an existing **local Standard String** parameter in the stack's region.
The second is a string carrier used by the original Battle template, not evidence
that an AMI exists or an EC2 instance can run. Nothing contacts AWS or supplies
public Amazon Linux image aliases. The original full Battle remains unsupported.

## Example

First put a dummy String parameter named `/practice/image` through the existing
local SSM API. Then create a stack with this template:

```yaml
Parameters:
  Image:
    Type: AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>
    Default: /practice/image
Resources:
  SavedLabel:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /practice/saved-image-label
      Type: String
      Value: !Ref Image
Outputs:
  ImageLabel:
    Value: !Ref Image
```

Ref, Sub and conditions use the fetched value. AllowedValues, AllowedPattern and
length constraints apply to the original **key**, including its template default,
not to the resolved value. Unsupported types, unknown input names and invalid
defaults are rejected even when an override supplies another key.

The caller needs `cloudformation:CreateStack` on the actual generated stack ARN,
`ssm:GetParameters` on every source parameter ARN, and the existing permissions
for all resources being created. All source permissions are checked before any
source row is read. A region/name-scoped SQLite transaction captures the selected
values together and closes before native creation. Later source updates do not
refresh that snapshot, including an update between compilation and creation.

DescribeStacks returns `ParameterKey`, the original key in `ParameterValue`, and
the saved `ResolvedValue` for this typed subset. Ordinary String/Number parameter
output behavior is unchanged. Stored values survive process restart and source
updates/deletion. As with existing stack outputs, any caller allowed to describe
the stack can see these resolved values. Use only dummy practice data;
`NoEcho: true` is rejected for these types rather than promising secrecy.

A matching ClientRequestToken retry returns the existing stack ID after current
stack authorization without re-reading sources. An altered request with the same
token is rejected. CreateStack still requires a valid stack name, not an ARN or
an omitted name. Fresh creation after source deletion fails. DeleteStack cleans
up owned resources and retains the independently supplied source parameters.

## Bounds and exclusions

The existing 100-parameter, 51,200-byte template and 4,096-byte SSM value limits
remain. Source values must be nonempty valid UTF-8 String data representable in
XML 1.0; NUL/control and U+FFFE/U+FFFF data is rejected rather than replaced in
DescribeStacks. Keys must be local
unversioned names; ARN, version and label selectors are rejected. Read syntax
permits `/aws/...`, but no AWS public values are seeded and normal PutParameter
continues to reject reserved names. Missing public defaults fail explicitly.

SSM Name/list/SecureString parameter types, dynamic references, UpdateStack,
UsePreviousValue, cross-account reads, AMI discovery/validation, InstanceProfile,
EC2, Linux/nginx/systemd execution and Session Manager are not added. This does
not enable an unverified Challenge/Battle or provide a grading result.

## Validation and sources

`test/e2e/cloudformation_ssm_parameters.py`, registered in the mandatory E2E suite,
uses actual boto3 clients and two separate authenticated CLI processes. It checks
key/value wire fields, regional and team separation, immutable replay, restart,
cleanup and missing-source refusal. Native tests additionally exercise per-key
IAM denial, exact generated-ARN patterns, malformed inputs and event-write
rollback. The [finite model](formal-cfn-parameter-snapshot.md) has independent
snapshot/admission/replay controls and does not prove the Go or SQLite code.
Full current-HEAD CI and formal checks remain the merge acceptance criteria.

Primary contracts checked 2026-11-08:
[CloudFormation-supplied parameter types](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cloudformation-supplied-parameter-types.html),
[Parameter API fields](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_Parameter.html),
and [SSM GetParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameters.html).
