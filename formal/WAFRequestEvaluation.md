# Bounded WAF request evaluation model

This independent finite safety model follows the implementation inspected in
`/workspace/parallel-waf/internal/aws/wafv2_match.go` (`Evaluate`) and
`wafv2_store.go` (`AssociateWebACLTx`, `DeleteWebACLTx`, and association foreign
keys). It is **not an implementation refinement proof** or a claim of AWS-wide
WAF compatibility. The shared `scripts/check-formal.py` gate is unchanged.

The actual evaluator sorts unique rule priorities ascending, skips nonmatches,
records matching Count rules and continues, returns immediately on a matching
Allow or Block rule, then uses the ACL default action if none terminates. With
no associated ACL it allows and returns no terminating-rule metadata. Association
creation requires an existing ACL and stage; ACL deletion checks associations,
and the stage foreign key uses ON DELETE RESTRICT.

## State and bounds

The config fixes three rules, one ACL and one stage. Initialization exhaustively
chooses all six unique priority permutations, all27 action assignments, all8
match subsets, and both default actions:2592 configurations. Rules retain their
configuration through the run. The lifecycle can create/delete the ACL or stage,
associate/disassociate, and begin/evaluate/reset requests. A request captures
whether an ACL was associated when evaluation begins. Later disassociation or
deletion cannot rewrite that request's snapshot. Rule matching itself is arbitrary
Boolean input; the model does not implement SQLi, transformations or body parsing.

`pc` walks increasing priority. `counted` is an ordered sequence of rule IDs;
`terminating=0` means no ACL/no terminal rule and `RuleCount+1` means default.
An independent priority-based oracle computes the first matching non-Count rule
and all preceding matching Counts. Mutations change evaluator actions, not this
oracle.

The checked invariants are:

- `TypeOK`: finite domains, unique priorities and bounded request state.
- `NoAssociationOrphans`: an association always has an existing ACL and stage.
- `CountContinues`: an active request remains pending, has exactly the processed
  Count prefix, and has not skipped an earlier terminating rule.
- `FirstTerminatingRuleWins`: a completed associated request contains exactly the
  expected Counts and first terminating rule/action, or the default action.
- `UnassociatedAllows`: an unassociated completed request allows with no Counts
  or terminating-rule metadata.

There is no fairness/liveness assertion. Multiple concurrent requests, ACL edits
or revisions during a request, stage identity reuse, multi-ACL association
replacement, multiple teams/regions, IAM, SQLite transaction failures, and SQLi
engine correctness are outside these bounds. Native tests remain necessary for
those implementation boundaries. A model pass does not demonstrate the full
original problem's provisioning, grading or cleanup.

## Reproduction and sensitivity controls

Use the existing checksum-pinned TLC jar:

```sh
TLA_JAR=/path/to/checksum-verified/tla2tools.jar make formal
```

The runner validates `formal/tla2tools.sha256`, puts Java temporary/model/state
files below the configured report directory, runs one TLC worker with a 60s
per-variant deadline, preserves complete logs, and removes temporary
state files after completion. It performs no AWS/network/publication operations.

Verified with TLC2.19 rev5a47802: baseline277728 states generated,
70080 distinct, queue empty, depth12, all five invariants hold. Mutation controls:

| Deliberate fault | Expected detector | Result |
| --- | --- | --- |
| Count ends evaluation | FirstTerminatingRuleWins | Detected |
| Rules evaluated in reverse priority order | CountContinues or FirstTerminatingRuleWins | CountContinues detected |
| Default accepted before processing final rule | FirstTerminatingRuleWins | Detected |
| Delete associated ACL | NoAssociationOrphans | Detected |
| Delete associated stage | NoAssociationOrphans | Detected |

A mutation must fail by the intended invariant; parsing errors or an empty action
are not accepted as successful sensitivity controls. The runner checks mutation
anchor uniqueness and removes `phase` from the Count mutant's UNCHANGED tuple so
its bad action is reachable. The model and five controls are registered in `formal/models.json` and run by
the shared fail-closed checker alongside all current-main models. The state
counts above are historical branch evidence; the integrated source requires a
fresh complete formal run.
