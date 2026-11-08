# Native IAM instance profiles

This bounded local Query/XML lifecycle stores instance profiles in the same
SQLite database as native IAM roles. It does not run EC2 instances, issue
credentials, expose instance metadata, or implement SSM Session Manager. An EC2
service trust document alone does not enable execution. CloudFormation instance
profiles, tags, non-root paths and EC2 profile associations remain unsupported.

## API contract

Use the existing IAM root POST endpoint and Version `2010-05-08`. Supported:

- `CreateInstanceProfile`, `GetInstanceProfile`, `DeleteInstanceProfile` with
  `InstanceProfileName`.
- `ListInstanceProfiles`, optionally `PathPrefix`, `Marker`, `MaxItems`.
- `AddRoleToInstanceProfile`, `RemoveRoleFromInstanceProfile` with
  `InstanceProfileName` and `RoleName`.
- `ListInstanceProfilesForRole` with `RoleName`, optionally `Marker`, `MaxItems`.

Create accepts optional `Path=/`; List accepts optional `PathPrefix=/`.
Unsupported fields, including all tags, return `NotImplemented` naming the exact
field. Duplicate fields fail validation. Profile names use 1–128 ASCII IAM name
characters `[A-Za-z0-9_+=,.@-]`; role names retain the existing native subset.
Names are case-insensitive for lookup but their saved spelling is canonical in
ARNs and responses. The account is the local fixed `000000000000` account.

Named profile operations require their `iam:<Action>` on the canonical
`arn:aws:iam::000000000000:instance-profile/<name>`. Create authorizes the exact
requested spelling that would be saved. `ListInstanceProfiles` requires `*`;
`ListInstanceProfilesForRole` requires the canonical native role ARN. Add also
requires `iam:PassRole` on that canonical role ARN. No role trust condition is
interpreted as granting caller permissions.

One role can belong to multiple profiles, but a profile holds at most one role.
Duplicate Add of the same role returns `EntityAlreadyExists`; a different role
in an occupied profile returns `LimitExceeded`. Remove of an absent/mismatched
role returns `NoSuchEntity`. Missing resources and stale admitted identities
return `NoSuchEntity`. DeleteProfile and DeleteRole return `DeleteConflict` while
membership exists. Remove membership before deleting either object. These are
immediate local changes, not an emulation of AWS propagation timing.

Lists sort names case-insensitively. `MaxItems` defaults to 100 and accepts
1–1000. `IsTruncated` and `Marker` describe the next page. Markers are bounded
canonical cursors bound to the operation and, for role-filtered lists, the saved
role name and generation. They are not secrets, authentication tokens, signed
capabilities or stable snapshots across updates. Noncanonical JSON or base64 spellings (including embedded newlines and nonzero
unused padding bits), and cross-operation/generation markers return `InvalidInput`.
A caller can construct a canonical cursor for a different position; the cursor
does not grant access or promise snapshot consistency.

## Persistence and boundaries

`aws_iam_instance_profiles` retains names after deletion, with a fresh ID on each
recreation. At most 1000 active plus retired distinct profile names are retained.
`aws_iam_instance_profile_roles` has one row per profile and composite foreign
keys to both saved generations. A native role must exist; a legacy identity or
CFN legacy role is not adopted. Get rechecks its admitted profile generation;
Add/Remove recheck both admitted generations; Delete and role-filtered List
recheck their respective generation. Reads of profile plus membership, writes,
required affected-row checks, and postcondition checks occur in one transaction.
Ignored, aborted or invalidated writes fail rather than report success. Malformed
persisted profile identity/date, role identity/trust and stale membership fail
closed. Retired records are not garbage-collected by this API.

The only integration hooks are IAM initialization of the two tables, Query
operation dispatch, and the native role-delete membership check. No provider-neutral
core, legacy identity authority, execution capability or public catalog
availability is changed by this module. The integration advertises the seven
implemented operations in capabilities and the local API reference. Downgrading binaries against the
same database remains unsupported.

## Primary contracts

The independent implementation uses public AWS references:
[Create](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateInstanceProfile.html),
[Get](https://docs.aws.amazon.com/IAM/latest/APIReference/API_GetInstanceProfile.html),
[List](https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListInstanceProfiles.html),
[Delete](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeleteInstanceProfile.html),
[AddRole and PassRole](https://docs.aws.amazon.com/IAM/latest/APIReference/API_AddRoleToInstanceProfile.html),
[RemoveRole](https://docs.aws.amazon.com/IAM/latest/APIReference/API_RemoveRoleFromInstanceProfile.html),
[ListForRole](https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListInstanceProfilesForRole.html),
[one-role membership](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_use_switch-role-ec2_instance-profiles.html),
and [delete dependencies](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_manage_delete.html).

The focused tests cover seven wire operations, canonical ARN/PassRole denial,
generation replacement at the actual authorization boundary, quota, case aliases,
wrong/expired pagination context, persistence, dependency cleanup, composite FKs,
and trigger-injected ignored/aborted/undone mutations, including retired-name
recreation. Upgrade tests reconstruct the pre-profile native-role schema and
check additive initialization without executing an older binary. They do not establish real
AWS compatibility, full SDK coverage, EC2 execution or Session Manager behavior.
Whole-repository gates, SDK process tests, API-reference integration and
finite-model checking are separate integration-owner acceptance checks. An
instance-profile console is not implemented.
