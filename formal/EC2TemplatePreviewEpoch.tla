--------------------- MODULE EC2TemplatePreviewEpoch ---------------------
VARIABLES selected, pending, shown, moved
vars == <<selected, pending, shown, moved>>

Example == [visible |-> TRUE, handoff |-> FALSE, identity |-> "author",
            region |-> "east", draft |-> "example"]
Scopes == {Example, [Example EXCEPT !.visible = FALSE], [Example EXCEPT !.handoff = TRUE],
           [Example EXCEPT !.identity = "reviewer"], [Example EXCEPT !.region = "west"],
           [Example EXCEPT !.draft = "edited"], [Example EXCEPT !.draft = "oversized"]}
Ops == {"ValidateTemplate", "CreateStack"}
Native == {"valid", "invalid", "denied", "malformed"}
Useful(s) == s.draft # "oversized"
Ordinary(s) == s.visible /\ ~s.handoff
Fresh(s) == /\ s.visible = selected.visible
            /\ s.handoff = selected.handoff
            /\ s.identity = selected.identity
            /\ s.region = selected.region
            /\ s.draft = selected.draft

Init == /\ selected \in Scopes /\ pending = {} /\ shown = {} /\ moved = FALSE
Change(s) ==
  /\ ~moved /\ s # selected
  /\ selected' = s /\ moved' = TRUE
  /\ shown' = {} (* Discard previous preview before continuing. *)
  /\ UNCHANGED pending
Submit(op, url) ==
  /\ pending = {} /\ Ordinary(selected) /\ Useful(selected)
  /\ op = "ValidateTemplate" /\ ~url
  /\ pending' = {[scope |-> selected, action |-> op, carriesURL |-> url]} /\ shown' = {}
  /\ UNCHANGED <<selected, moved>>
Receive(raw, hasValue) ==
  /\ pending # {}
  /\ LET r == CHOOSE p \in pending: TRUE
     IN IF Fresh(r.scope)
        THEN shown' = {[scope |-> r.scope, source |-> raw, outcome |-> raw,
                        receivedValue |-> hasValue, displaysValue |-> FALSE]}
        ELSE UNCHANGED shown
  /\ pending' = {} /\ UNCHANGED <<selected, moved>>
Next ==
  \/ \E s \in Scopes: Change(s)
  \/ \E op \in Ops, url \in BOOLEAN: Submit(op,url)
  \/ \E raw \in Native, hasValue \in BOOLEAN: Receive(raw,hasValue)
  \/ UNCHANGED vars

TypeOK == /\ selected \in Scopes /\ moved \in BOOLEAN
          /\ pending \subseteq [scope: Scopes, action: Ops, carriesURL: BOOLEAN]
          /\ shown \subseteq [scope: Scopes, source: Native, outcome: Native,
                              receivedValue: BOOLEAN, displaysValue: BOOLEAN]
OrdinaryPreview == \A r \in pending: r.scope.visible /\ ~r.scope.handoff
BoundedDraft == \A r \in pending: r.scope.draft # "oversized"
FixedDeclarationRead == \A r \in pending: r.action = "ValidateTemplate" /\ ~r.carriesURL
OnlyCurrentIdentity == \A r \in shown: r.scope.identity = selected.identity
OnlyCurrentRegion == \A r \in shown: r.scope.region = selected.region
OnlyCurrentDraft == \A r \in shown: r.scope.draft = selected.draft
NoClosedOrHandoffResult == \A r \in shown: selected.visible /\ ~selected.handoff
EvidencePreserved == \A r \in shown: r.outcome = r.source
NoReturnedValues == \A r \in shown: ~r.displaysValue
========================================================================
