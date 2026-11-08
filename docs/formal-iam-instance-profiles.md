# Instance profile lifecycle model

`IAMInstanceProfiles` is a finite admission/membership model for the seven native
profile APIs and the existing native role-delete dependency hook. It does not
model EC2 execution, role credentials, IMDS, Session Manager or a completed Battle.
Read/list wire format and pagination are checked by real-code/SDK tests rather
than declared covered by a stuttering transition.

## Scope and bounds

There are two profile names, two native role names, retained generations 0–2,
and at most two additional arbitrary lifecycle requests from four API-reachable
seeds. The seeds are: no profiles; two empty profiles; P attached to R with empty
Q; and a deleted/recreated P at generation 2 with empty Q. The last seed has the
explicit Create P, Delete P, Create P prelude. A seed is a specification witness,
not evidence of a real AWS API run. This is not exhaustive coverage of all
histories or longer request sequences.

A profile contains at most one generation-bound role edge. Add requires both
profile action authorization and PassRole; native mutations recheck the profile
and role incarnations captured before authorization. Populated profiles and
attached roles cannot be deleted. Duplicate Add and absent Remove refuse; failed
storage, denied, stale, missing and conflicting operations preserve the complete
durable frame. Recreated profiles have new identities. Peer state is unchanged.
Authorization, captured-generation validity and storage success are Boolean
inputs. They abstract the concrete IAM/SQLite/HTTP checks and do not prove their
implementation. The model has no liveness or fairness property. Exhaustion of
finite identity carriers stops allocation, rather than inventing an API limit.

## Properties and sensitivity controls

Eleven invariants cover type bounds, action authorization, PassRole, both
admitted-generation checks, failure frames, one-role membership, live matching
parents, fresh profile identity, peer isolation and exact removal. Twelve literal
source mutations must produce their designated invariant counterexample:

| Control | Required invariant |
| --- | --- |
| IgnoreActionAuthorization | AuthorizationRequired |
| IgnorePassRole | PassRoleRequired |
| IgnoreProfileGeneration | ProfileGenerationGuard |
| IgnoreRoleGeneration | RoleGenerationGuard |
| CommitStorageFailure | FailureFrame |
| AllowMultipleRoles | SingleRole |
| DeletePopulatedProfile | AttachmentAuthority |
| DeleteAttachedRole | AttachmentAuthority |
| BindWrongProfileGeneration | AttachmentAuthority |
| BindWrongRoleGeneration | AttachmentAuthority |
| ReuseProfileIdentity | FreshProfileIdentity |
| RemovePeerMembership | PeerFrame |

The initial working-tree run visited 1,191 distinct states and every control
produced exit 12 with its designated invariant. This focused specification run
is not immutable-HEAD merge evidence. The complete unchanged formal gate must
run after source integration and commit.

## Correspondence and exclusions

- `iam_instance_profiles.go`: profile and membership persistence, fresh provider
  IDs, one-role composite FKs, current parent checks, dependency refusal, checked
  writes and postcondition reads in one transaction.
- `iam_instance_profiles_query.go`: canonical resource authorization, additional
  PassRole authorization, captured identities and native API projection.
- `iam_roles.go`: schema initialization and role-delete membership dependency.
- `iam_query.go`: existing strict transport/field checks and seven-action dispatch.
- `internal/server/server.go`: capability declarations for this state lifecycle;
  metadata itself is tested by executed native reference examples, not by TLC.

Native tests cover real authorization-boundary replacement, corruption,
ignored/aborted/undone writes, quotas, lists, XML, restart and cleanup. The
separate `iam_instance_profiles.py` helper exercises actual boto3 and two distinct
processes/DBs/token authorities. These concrete tests are separate evidence from
finite TLC checks. The model does not establish Go refinement, SQL atomicity,
IAM parsing/ARN semantics, randomness, authentication, filesystem/hypervisor
containment, public-cloud equivalence, all concurrency schedules or browser UX.
