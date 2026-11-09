# Pinned AmazonSSMManagedInstanceCore policy

This increment supports exactly
`arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore` through native IAM
GetPolicy, GetPolicyVersion, ListPolicyVersions, AttachRolePolicy,
DetachRolePolicy and ListAttachedRolePolicies. It is not an AWS policy catalog.
ListPolicies continues to require Scope=Local and returns customer policies only;
AWS/All/default scope, user/group attachments and other AWS-managed ARNs remain
unsupported. The two legacy CFN metadata-only ARNs are unchanged.

## Source and immutable pin

The [official AWS policy reference](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AmazonSSMManagedInstanceCore.html)
was checked on 2026-11-09. It identifies default version **v2**, creation
2019-03-15 17:22 UTC and last edit 2019-05-23 16:54 UTC. The full JSON document
is retained in `internal/aws/iam_builtin.go`, with insignificant whitespace
removed. Its SHA-256 is `b49d8e0a366be4eb7ea97da2ef96ad84a9a563ef6efa926c40c581c1e074b993`.
Only v2 is available: no v1 content or history is fabricated. Version mutation,
default-version change and policy deletion return UnmodifiableEntity, including
selecting v2 as default again. The simulator PolicyId `ANPA0000000000000001`
is explicitly synthetic, not an asserted actual AWS identifier. The ARN,
document, dates, description and default version come from the reference.
No runtime calls to AWS or automatic policy refresh occur.

The pin has 25 actions across three Allow statements, each with Resource `*`.
Of these, only `ssm:GetParameter` and `ssm:GetParameters` have implemented
service APIs. The remaining 23 action strings do not implement or enable their
APIs. GetParametersByPath, PutParameter, SendCommand and StartSession are not
granted by this document. Missing APIs still return explicit unsupported errors.
Inline/customer-managed explicit Deny retains priority over builtin Allow.

## State, compatibility and limits

The builtin is seeded once into the existing provider-owned managed-policy
schema under `/aws/AmazonSSMManagedInstanceCore`. A slash is forbidden in a
customer policy name, so a customer policy named AmazonSSMManagedInstanceCore
remains a separate local-account ARN and can be attached alongside the builtin.
The seed and every builtin read validate the exact immutable metadata/document;
existing mismatches fail closed rather than being repaired or overwritten.
Foreign-key attachment references and role-generation checks remain shared.
Startup is transactional and idempotent; no user/customer policy is replaced.
Downgrade to older binaries is unsupported, as with other provider-schema changes.

The builtin does not consume the 1000 active/retired customer policy claims or
appear in local-policy pagination. It does count toward 20 attachments per role,
20000 total attachments, 100 aggregate statements and 32 KiB aggregate size.
Attached-policy pagination uses a distinct reserved cursor for the builtin, so
a same-display-name customer policy is not skipped. Existing customer list
markers and saved zero-managed CFN fingerprints retain their meanings.

Native roles, canonical IAM authorization, captured generation, aggregate state
and attachment views are shared with customer policies. Attach/detach checks
requested managed state and preserves inline state before acknowledgement.
Builtin storage does not grant roles permissions until attachment.

## CloudFormation scope

The existing native CFN role selection is unchanged: active profile composition
or a new zero-inline service-trust role. Such a role may request an empty managed
list or exactly this one ARN. Unknown/empty ARN strings, duplicates, other
policies and unsupported shapes are rejected. Legacy one-inline forms stay
legacy; this change does not promote metadata into grants.

Creation attaches and aggregates within the existing stack write transaction.
Cleanup detaches in that same transaction before role retirement. Requested
inline/managed children, generation, immutable pin, aggregate, ownership and
final event/history acknowledgements are checked. Drift, ignored writes or
trigger substitution roll back the stack operation. Callers need canonical
role AttachRolePolicy/DetachRolePolicy permissions as appropriate.

`internal/aws/testdata/hello-world-battle-iam-fragment.yaml` copies only the
original InstanceRole and InstanceProfile resources without changing them,
wrapped with a reduced NamePrefix parameter declaration. The fixture is a
**fragment**, not the unchanged Battle template. The original source was
`TenkaCloudChallenge/battles/hello-world-battle/template.yaml`; no source bytes
or capability availability flags were modified.
The inspected complete template SHA-256 was
`4de530625bd2a3c0b5a5881f511061709c94c9eb6884d8d6fd1037f3d9bc6bc5`;
the unchanged resource fragment SHA-256 was
`bd39fb7c3ec71a1167662ee1ab98e56713304ef4c47dccd8142019fa272723c9`.
A direct source-byte comparison passed; this is fixture provenance, not runtime
acceptance.

This is not SSM execution, instance credentials, an agent connection, EC2/VM
lifecycle, networking, SendCommand/StartSession, or Hello World Battle readiness.
The full original initialize → operate → grade → cleanup path remains blocked
by those absent capabilities and other unsupported IAM/template conditions.

## Verification scope

Native tests cover immutable policy reads, same-name customer isolation,
attachment authority, explicit Deny, revocation and transaction rollback.
`test/e2e/iam_ssm_builtin.py` exercises the same contracts through two local
CLI processes, including pagination, restart, original IAM resource-fragment
provenance and complete cleanup. The normal repository and CI gates remain
required for each integrated source revision.

These checks do not establish actual EC2, guest execution, Session Manager,
real AWS compatibility or the original Battle lifecycle.

### Source review follow-up: detached target preservation

Static review of candidate `185effd` found that the post-detach check inspected
only remaining attachments. An injected AFTER DELETE trigger could therefore
remove or corrupt the now-unattached builtin pin without being observed. This
is a fault-model gap, not a claim of an externally reachable SQL exploit.

The attachment helper now rereads and compares the target policy/version state
after its writes, excluding the expected attachment-count change. CFN also
validates every requested builtin pin at each fingerprint/acknowledgement,
including retired roles after final stack, deletion-event and history writes.
Four native-detach controls and sixteen CFN cleanup controls cover deleted
versions, corrupt documents/metadata and deleted pins. Each requires error,
whole-database rollback and successful cleanup after removing the injected
trigger. The controls must continue to pass in both normal and race tests.
SDK, browser and whole-repository acceptance are separate checks.
