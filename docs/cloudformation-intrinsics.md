# Bounded CloudFormation value intrinsics

The local CloudFormation compiler supports `Fn::Select`, `Fn::GetAZs` and
`Fn::Base64` in resource properties and outputs, with YAML shorthand. Scalar
`Fn::Sub` supports literal escapes: `${!aws:userid}` becomes `${aws:userid}`.
An escaped placeholder is never resolved, treated as a dependency or expanded
again. A substituted parameter value is also not recursively expanded. This
adds no `Fn::Sub` variable-map form, new resource type or executable UserData.

## Local availability contract

`Fn::GetAZs` returns exactly one **synthetic local zone** for each supported AWS
provider region: the region string followed by `a`. The source is the existing
provider `Regions` list, currently `us-east-1`, `us-west-2`, `ap-northeast-1` and
`eu-west-1`. An empty region uses the current stack's `AWS::Region`; a region
string or `Ref` is accepted. Unknown or unresolved regions fail.

This is deterministic local simulated availability, not a claim about real AWS
Availability Zones, account access, default subnets or EC2 service readiness.
It performs no AWS API request, discovers no cloud topology and creates no zone,
network or instance. The isolated provider-owned source can later be replaced by
an actual local simulated network inventory. Real AWS `GetAZs` has different
account, subnet, IAM and ordering behavior.

## Operand and size limits

- `Fn::Select` takes exactly two arguments: a zero-based index and a nonempty
  list. Index text is a canonical nonnegative decimal integer, directly or via
  `Ref`. YAML/JSON integer syntax and decimal integer strings are accepted;
  signs, leading zeroes, fractional/exponent notation, float coercion and
  indexes at or above 10,000 are rejected. The actual index must be in range.
- Lists cannot contain null entries, including entries not selected. A literal
  list or the supported list carriers `GetAZs`, `Ref`, `GetAtt`, `If` is allowed;
  `Ref`/`GetAtt` must actually produce a list (current resource attributes and
  String/Number parameter refs do not). All list expressions and symbols are
  checked; this is not lazy evaluation of otherwise invalid list members.
- `Fn::Base64` requires a string or a supported string-producing intrinsic. It
  uses standard padded Base64 on the exact UTF-8 bytes, including newlines.
  Decoding does not execute the result. Resolved strings are capped at 51,200
  bytes, so Base64 accepts at most 38,400 input bytes.
- Existing depth 32 / 10,000-node preparation bounds remain. Resolution now has
  its own depth/node checks and rejects oversized expansion rather than clipping.
  TemplateBody remains limited to 51,200 bytes. This is a per-value/per-walk
  bound, not a total process-memory or AWS compatibility guarantee.
- Scalar `Sub` variables use letters, digits, underscore, period and colon.
  Empty, nested, newline-containing or unclosed placeholders fail. Escaped
  literal payloads must be nonempty and contain no braces or newlines. Unicode
  and newlines outside placeholders, and Unicode escaped literals, are kept.
  Launch-template-specific backslash processing is not implemented.
- Use quoted empty strings for shorthand empty arguments. A missing operand or
  consecutive short tags such as `!Base64 !Sub` are rejected; use the full name
  for at least one nested function.

Both `If` branch shapes and their symbols are still validated before selecting
an arm. Existing conditions, permissions, event history, ownership checks and
transactional create/delete behavior remain unchanged. Parsing a literal IAM
policy variable does not implement IAM condition/variable evaluation or grant
new permissions.

## Original Battle fixture and acceptance boundary

The unchanged [Hello World Battle fixture](../test/fixtures/hello-world-battle-c1aaaa9/README.md)
now passes YAML intrinsic parsing. Tests resolve its actual subnet
`!Select [0, !GetAZs ""]`, Base64/Sub UserData bytes and escaped session ARN.
The full original CreateStack still fails explicitly on unsupported resource
types, before any native state or successful deployment history is written.

A later [local SSM value parameter slice](cloudformation-ssm-parameters.md) admits
the AMI parameter carrier without providing public image data.
EC2/VPC resources, IAM InstanceProfile, the full
IAM policy language, real guest boot, Session Manager and guest HTTP services
remain outside this change. This is an intrinsic prerequisite, **not a working
Battle deployment or gameplay/grading acceptance**. Supported native SSM test
stacks separately exercise forward dependencies, permission denial, state/event
rollback, physical-delete acknowledgement, cleanup, name reuse and peer state
preservation. These are in-process native API tests, not a real-CLI/browser or
Battle guest test.

## Public specification references

Checked against AWS CloudFormation Template Reference:

- [Fn::Select](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-select.html)
- [Fn::GetAZs](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-getavailabilityzones.html)
- [Fn::Base64](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-base64.html)
- [Fn::Sub](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-sub.html)

The restrictions above deliberately describe a bounded subset; they are not a
claim that every accepted or rejected spelling matches the live AWS service.
