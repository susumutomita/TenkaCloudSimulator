---------------------- MODULE RDSOwnedGenerationFence ----------------------
VARIABLE s

TypeOK == s \in [auth: BOOLEAN, private: BOOLEAN, current: BOOLEAN, clean: BOOLEAN,
                  phase: {"empty","metadata","pending","proof","held","retiring","clean","debt"},
                  intent: {"host","valid-modify","invalid-modify"}, retiredByModify: BOOLEAN,
                  row: BOOLEAN, fence: BOOLEAN, launched: BOOLEAN, drained: BOOLEAN,
                  changed: BOOLEAN, endpoint: BOOLEAN]
Init == /\ TypeOK /\ s.phase="empty"
        /\ ~s.row /\ ~s.fence /\ ~s.launched /\ ~s.drained /\ ~s.changed /\ ~s.endpoint /\ ~s.retiredByModify
Reserve == s.auth /\ s.private
LaunchOK == s.fence
StillCurrent == s.current
MayChange == ~s.fence
MayRetire == s.intent # "invalid-modify"
KeepFence == ~s.clean

Create ==
  /\ s.phase="empty"
  /\ s'=[s EXCEPT !.row=s.auth,
          !.phase=IF Reserve THEN "pending" ELSE "metadata", !.fence=Reserve]
Begin ==
  /\ s.phase="pending" /\ LaunchOK
  /\ s'=[s EXCEPT !.phase="proof", !.launched=TRUE]
Check ==
  /\ s.phase="proof"
  /\ s'=[s EXCEPT !.phase=IF StillCurrent THEN "held" ELSE "retiring", !.endpoint=FALSE]
Change ==
  /\ s.row /\ ~s.changed /\ MayChange
  /\ s'=[s EXCEPT !.changed=TRUE]
Retire ==
  /\ s.phase="held" /\ MayRetire
  /\ s'=[s EXCEPT !.phase="retiring", !.retiredByModify=s.intent # "host"]
Finish ==
  /\ s.phase="retiring"
  /\ s'=[s EXCEPT !.phase=IF KeepFence THEN "debt" ELSE "clean",
          !.drained=s.clean, !.fence=KeepFence]
LoseOwner ==
  /\ s.fence /\ s.phase="held"
  /\ s'=[s EXCEPT !.phase="debt"]
Next == Create \/ Begin \/ Check \/ Change \/ Retire \/ Finish \/ LoseOwner
Spec == Init /\ [][Next]_s

LaunchNeedsCommittedAuthority == s.launched => (s.auth /\ s.private /\ s.row /\ (s.phase="proof" => s.fence))
AttachNeedsCurrent == s.phase="held" => (s.current /\ s.fence /\ s.launched)
FencedGenerationCannotChange == s.fence => ~s.changed
UncertainCleanupRemainsFenced == s.phase="debt" => s.fence
NoPublishedEndpoint == ~s.endpoint
InvalidModifyKeepsOwner == s.retiredByModify => s.intent="valid-modify"
=============================================================================
