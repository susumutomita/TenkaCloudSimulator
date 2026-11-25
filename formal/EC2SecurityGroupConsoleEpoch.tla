-------------------- MODULE EC2SecurityGroupConsoleEpoch --------------------
CONSTANT Vpcs
VARIABLES selected, pending, shown
vars == <<selected,pending,shown>>
Epochs == {0,1}
Gates == [trusted: BOOLEAN, offered: BOOLEAN, handoff: BOOLEAN, managed: BOOLEAN]
Scopes == [target: Vpcs, epoch: Epochs, gates: Gates]
Actions == {"DescribeSecurityGroups", "unsupported"}
Observations == {"rows", "empty", "denied", "error"}
Eligible(g) == g.trusted /\ g.offered /\ ~g.handoff /\ g.managed

Init ==
  /\ selected \in { [target |-> v, epoch |-> 0, gates |-> g] : v \in Vpcs, g \in Gates }
  /\ pending = {}
  /\ shown = {}

Select(v,g) ==
  /\ selected.epoch = 0
  /\ selected' = [target |-> v, epoch |-> 1, gates |-> g]
  /\ shown' = {} (* ClearScope *)
  /\ UNCHANGED pending

Begin(action) ==
  /\ pending = {}
  /\ Eligible(selected.gates)
  /\ action = "DescribeSecurityGroups"
  /\ pending' = {[scope |-> selected, action |-> action]}
  /\ shown' = {}
  /\ UNCHANGED selected

Reply(outcome,vpc) ==
  /\ pending # {}
  /\ LET request == CHOOSE r \in pending: TRUE IN
     IF request.scope.epoch = selected.epoch
     THEN shown' = {[scope |-> request.scope, observed |-> outcome, vpc |-> vpc,
                     status |-> IF outcome = "rows" /\ vpc # request.scope.target
                                THEN "empty" ELSE outcome]}
     ELSE UNCHANGED shown
  /\ pending' = {}
  /\ UNCHANGED selected

Next ==
  \/ \E v \in Vpcs, g \in Gates: Select(v,g)
  \/ \E a \in Actions: Begin(a)
  \/ \E o \in Observations, v \in Vpcs: Reply(o,v)
  \/ UNCHANGED vars

TypeOK ==
  /\ selected \in Scopes
  /\ pending \subseteq [scope: Scopes, action: Actions]
  /\ shown \subseteq [scope: Scopes, observed: Observations, vpc: Vpcs, status: Observations]
EligibleRequest == \A r \in pending:
  /\ r.scope.gates.trusted /\ r.scope.gates.offered
  /\ ~r.scope.gates.handoff /\ r.scope.gates.managed
ReadOnlyOperation == \A r \in pending: r.action = "DescribeSecurityGroups"
OnlyCurrentScope == \A r \in shown: r.scope = selected
OnlySelectedVPC == \A r \in shown: r.status = "rows" => r.vpc = selected.target
FailuresNeverEmpty == \A r \in shown: r.observed \in {"error","denied"} => r.status = r.observed
=============================================================================
