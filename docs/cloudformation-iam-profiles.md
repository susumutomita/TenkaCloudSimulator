# CloudFormation native IAM role/profile composition

`AWS::IAM::InstanceProfile` is a bounded native IAM prerequisite, not EC2 or
Session Manager execution. CreateStack and DeleteStack share the actual IAM
role/profile tables, validation, quotas and transaction-aware mutation helpers.
GetRole, ListRoles and GetInstanceProfile see these newly created native objects.

## Supported forms

Instance profiles accept `InstanceProfileName`, `Path: /` and required `Roles`
with zero or one native role name. Ref returns the profile name; GetAtt Arn
returns its local-account root-path ARN. An omitted name is stack-unique and
at most 128 characters. Explicit role/profile names need
`CAPABILITY_NAMED_IAM`; otherwise `CAPABILITY_IAM` suffices.

A role takes the new native path when either:

- An active, compiled instance profile in the same template names it, including
  a resolved Ref/Sub/If or an exact literal physical name. References provide
  normal dependency ordering; literal names require DependsOn where needed.
- It supplies AssumeRolePolicyDocument and omits Policies or supplies `[]`.
  Zero-inline roles were not accepted by the prior legacy adapter.

The native role subset accepts explicit Service-principal `sts:AssumeRole`
trust, root Path, RoleName, Description, MaxSessionDuration (3600–43200), and
zero to ten distinct named inline policies using the existing bounded policy
grammar. Empty roles have no aggregate policy row and grant nothing. Native CFN
roles reject AWS-principal/mixed trust, every trust Condition, nonempty
ManagedPolicyArns except the [pinned SSM core policy](iam-ssm-managed-policy.md), tags, unsupported paths and unsupported policy Conditions.

An external role must already be native. Its canonical name and generation are
captured in the same write transaction as permission preflight and creation.
A profile does not own or delete that external role. In-template role names must
match exactly; a case variant cannot turn an existing legacy identity into native
IAM. Native lookups of external role names remain case-insensitive, with the
canonical ARN used for authorization.

## Compatibility is explicit

Previously supported one-inline-policy roles not selected by an active profile
keep the legacy adapter. Their trust, tags and the two historically accepted
managed-policy names remain metadata, not native IAM trust or policy grants.
This preserves the unchanged supported Hello World challenge and legacy Lambda
fixtures. A disabled profile does not promote those roles. Legacy roles remain
absent from native IAM GetRole/ListRoles and cannot be added to a native profile.
Existing persisted stacks without a native marker retain their prior cleanup
path and are never silently migrated or adopted.

The original **Hello World Battle is still unsupported**. Its unchanged
InstanceRole can now attach the [pinned AmazonSSMManagedInstanceCore v2](iam-ssm-managed-policy.md)
policy in native composition. This only grants already-implemented parameter
reads. EC2 resources, guest execution, instance metadata, networking, agent
channels, credentials, session/command transport and the original initialize →
operate → grade → cleanup lifecycle are separate work. The new IAM fixture is
a clearly labeled resource fragment, not an altered full Battle acceptance.

## Atomic authority and cleanup

The caller needs the stack action plus Create/DeleteRole, Put/DeleteRolePolicy
when inline policies are present, Attach/DetachRolePolicy when the builtin is
requested, Create/DeleteInstanceProfile and
Add/RemoveRoleFromInstanceProfile when membership is present. Add additionally
requires PassRole on the canonical role ARN. Capabilities grant no permissions.
These checks precede writes and run under the same transaction as native
reference admission; no nested store transaction is opened.

The saved stack document records exact role/profile generations and fingerprints.
Native name claims, aggregates, inline policies, membership, stack ownership and
events commit together or roll back together. Failed attempts do not mutate the
caller's compiled generation snapshot. Both IAM names remain reserved by their
stack after native deletion until stack cleanup succeeds. After cleanup the
native tombstone remains, and recreation receives a fresh generation.

Changed trust, inline policies, membership or generations refuse cleanup. Added
managed-policy attachments or external profile dependencies are preserved and
block deletion. Already retired objects with the same generation and metadata
can be cleaned up if they have no remaining dependencies. Cleanup removes owned
membership before retiring its profile and role, and does not delete an external
role. Final acknowledgements recheck retired generations, absent children,
stack/claim removal and saved metadata after all event/history writes. Ignored,
undone or late resurrecting writes fail atomically.

## Evidence and limits

The Go tests cover Query/native shared state, caller and PassRole denial,
case/legacy separation, active/disabled conditions and intrinsics, persistence,
retired-name reuse, native and aggregate quotas, external dependencies, drift,
ignored/undone/late writes and whole-database peer-frame rollback. The immutable
supported Hello World fixture is checked at SHA-256
`ad8b4fcf3f1d42475c5c2e8617d3194536810cc044a6ea166a2699cac719c8bb`.

`test/e2e/cfn_iam_profiles.py` exercises two authenticated local CLIs through
boto3, with denied PassRole, shared native views, unchanged legacy Hello World,
restart, external dependencies, generation replacement and cleanup. Its actual
run receipt is separate from source presence. Full repository quality/E2E/formal
checks and any model-to-source applicability belong to the integration candidate;
focused tests do not constitute those passes or real AWS compatibility.

Public contracts: [InstanceProfile](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-iam-instanceprofile.html),
[Role](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-iam-role.html),
[AddRoleToInstanceProfile](https://docs.aws.amazon.com/IAM/latest/APIReference/API_AddRoleToInstanceProfile.html).
