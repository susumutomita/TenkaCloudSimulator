----------------------- MODULE RESTDeploymentSnapshot -----------------------
EXTENDS FiniteSets
CONSTANTS Deployments, Values, None
VARIABLES exists, draft, snapshots, baseline, stage
vars == <<exists, draft, snapshots, baseline, stage>>
Empty == [d \in Deployments |-> None]
Init == /\ exists = FALSE /\ draft = None
        /\ snapshots = Empty /\ baseline = Empty /\ stage = None
CreateAPI == /\ ~exists /\ exists' = TRUE
             /\ UNCHANGED <<draft, snapshots, baseline, stage>>
EditDraft(v) == /\ exists /\ draft' = v
                /\ UNCHANGED <<exists, snapshots, baseline, stage>>
Deploy(d) == /\ exists /\ draft \in Values /\ snapshots[d] = None
             /\ snapshots' = [snapshots EXCEPT ![d] = draft]
             /\ baseline' = [baseline EXCEPT ![d] = draft]
             /\ UNCHANGED <<exists, draft, stage>>
BindStage(d) == /\ exists /\ snapshots[d] \in Values /\ stage' = d
                /\ UNCHANGED <<exists, draft, snapshots, baseline>>
DeleteStage == /\ stage' = None /\ UNCHANGED <<exists, draft, snapshots, baseline>>
DeleteAPI == /\ exists /\ exists' = FALSE /\ draft' = None
             /\ snapshots' = Empty /\ baseline' = Empty /\ stage' = None
Next == CreateAPI \/ (\E v \in Values \cup {None}: EditDraft(v))
        \/ (\E d \in Deployments: Deploy(d) \/ BindStage(d))
        \/ DeleteStage \/ DeleteAPI \/ UNCHANGED vars
TypeOK == /\ exists \in BOOLEAN /\ draft \in Values \cup {None}
          /\ snapshots \in [Deployments -> Values \cup {None}]
          /\ baseline \in [Deployments -> Values \cup {None}]
          /\ stage \in Deployments \cup {None}
SnapshotsFrozen == snapshots = baseline
NoStageOrphans == /\ (stage # None => exists /\ snapshots[stage] \in Values)
                  /\ (~exists => snapshots = Empty /\ draft = None)
=============================================================================
