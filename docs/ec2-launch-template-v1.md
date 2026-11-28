# Immutable native EC2 launch-template configuration

The local EC2 Query API supports `CreateLaunchTemplate`, `DescribeLaunchTemplates`,
`DescribeLaunchTemplateVersions` and `DeleteLaunchTemplate` (2016-11-15). It stores
real configuration in the process's SQLite database. It never launches an
instance, creates a scheduler endpoint or makes a PCS resource ready.

Create accepts a name, optional printable ASCII client token (128 bytes) and
version description (255 bytes), and `LaunchTemplateData.SecurityGroupIds` with
one to five distinct actual EC2-managed security groups in one native VPC.
The saved default/latest version is always 1. Version mutation, tags, UserData,
images, network interfaces, template instance profiles, DryRun true, filters and
pagination are explicitly unsupported. The local bounds are 20 active templates
and 1000 retained physical identities per Region; these are not AWS quotas.
Describe accepts exact IDs or names, or returns the complete bounded regional
list. Version reads accept 1, `$Default` or `$Latest` for one actual template.

The [EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonec2.html)
requires `*` for both Describe actions. Create/Delete use the actual
`arn:aws:ec2:REGION:000000000000:launch-template/ID`. Each request captures one
current identity policy before its provider transaction. Instance-scoped Console
sessions remain limited to their existing instance discovery.

A client token is locally scoped to the selected principal and Region. Reusing
it with the same normalized input returns the original physical receipt; changed
input returns `IdempotentParameterMismatch`. Deleted identities and receipts are
retained. A replay after deletion never resurrects that identity; a new create
with the same name and a new token receives a new ID. Native Describe is a
historical read of saved template data, even if a referenced SG is later removed.
Trusted dependency admission separately loads the active template and validates
its actual current native SG/VPC rows inside the consuming transaction. This
ordinary identity check is not a monotonic-generation or clock/ABA guarantee.

The [current PCS guide](https://docs.aws.amazon.com/pcs/latest/userguide/working-with_launch-templates.html)
permits direct instance settings without a custom template; pinned botocore
1.41.6 requires `customLaunchTemplate`. A basic SG-only template is one supported
prerequisite path. PCS [ignores template IamInstanceProfile](https://docs.aws.amazon.com/pcs/latest/userguide/working-with_launch-templates_overview.html).
The node group's own subnet and `iamInstanceProfileArn`, actual role and
`iam:PassRole` remain separate admission requirements. This increment does not
add that PCS admission, `UpdateCluster`, allocated capacity, or ACTIVE states.
Original #109 remains open.

`test/e2e/ec2_launch_templates.py` is an opt-in stock boto3 two-process lifecycle
fixture with owned restart and native cleanup. Local native tests and the finite
`EC2LaunchTemplateV1` model are separate evidence, not substitutes for executing
that client fixture or the final standard/formal gates. The six-transition model
covers saved identities, tokens, retirement, dependency lifetime and a populated
peer. It does not prove SQL/Go/XML/IAM parsing, SDK/browser behavior, timing or
provisioning. Its registry lists the exact bounds and negative controls.
