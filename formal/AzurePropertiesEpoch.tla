------------------------ MODULE AzurePropertiesEpoch ------------------------
EXTENDS Naturals, FiniteSets, TLC
CONSTANTS Contexts, Epochs, Resources
VARIABLES current, alive, epoch, selected, pane, status, properties,
          pending, used, aborted, locale, last, localeBefore
vars == <<current, alive, epoch, selected, pane, status, properties,
          pending, used, aborted, locale, last, localeBefore>>
None == "none"
Empty == [present |-> FALSE, context |-> None, epoch |-> None, resource |-> None]
Payload(r) == [present |-> TRUE, context |-> r.context,
               epoch |-> r.epoch, resource |-> r.resource]
IdleLast == [published |-> FALSE, eligible |-> TRUE, successful |-> TRUE]
Frame == <<current, alive, epoch, selected, pane, status, properties, pending, used, aborted>>
Request(c,e,k) == [context |-> c, epoch |-> e, resource |-> k]
Eligible(r) == r.context \in alive /\ epoch[r.context] = r.epoch /\ r \notin aborted
FinalizeEligible(r) == r.context \in alive /\ epoch[r.context] = r.epoch

Init == /\ current = "A" /\ alive = {"A"}
        /\ epoch = [c \in Contexts |-> None]
        /\ selected = [c \in Contexts |-> None]
        /\ pane = [c \in Contexts |-> FALSE]
        /\ status = [c \in Contexts |-> "Idle"]
        /\ properties = [c \in Contexts |-> Empty]
        /\ pending = {} /\ used = {} /\ aborted = {}
        /\ locale = "en" /\ last = IdleLast /\ localeBefore = <<>>

Open(e,k) ==
  LET r == Request(current,e,k) IN
  /\ current \in alive /\ status[current] # "Loading"
  /\ <<current,e>> \notin used
  /\ epoch' = [epoch EXCEPT ![current] = e]
  /\ selected' = [selected EXCEPT ![current] = k]
  /\ pane' = [pane EXCEPT ![current] = TRUE]
  /\ status' = [status EXCEPT ![current] = "Loading"]
  /\ properties' = [properties EXCEPT ![current] = Empty] (* ClearOnOpen *)
  /\ pending' = pending \cup {r} /\ used' = used \cup {<<current,e>>}
  /\ last' = IdleLast /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,alive,aborted,locale>>

Close ==
  /\ pane[current]
  /\ pane' = [pane EXCEPT ![current] = FALSE]
  /\ epoch' = [epoch EXCEPT ![current] = None]
  /\ status' = [status EXCEPT ![current] = "Idle"]
  /\ properties' = [properties EXCEPT ![current] = Empty] (* ClearOnClose *)
  /\ aborted' = aborted \cup {r \in pending : r.context = current}
  /\ last' = IdleLast /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,alive,selected,pending,used,locale>>

Unmount ==
  /\ current \in alive
  /\ alive' = alive \ {current}
  /\ pane' = [pane EXCEPT ![current] = FALSE]
  /\ epoch' = [epoch EXCEPT ![current] = None]
  /\ status' = [status EXCEPT ![current] = "Idle"]
  /\ properties' = [properties EXCEPT ![current] = Empty]
  /\ aborted' = aborted \cup {r \in pending : r.context = current}
  /\ last' = IdleLast /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,selected,pending,used,locale>>

(* Context B is a fresh component/lifecycle object, never a reactivation of A. *)
MountNext ==
  /\ current = "A"
  /\ current' = "B" /\ alive' = {"B"}
  /\ epoch' = [c \in Contexts |-> None]
  /\ selected' = [c \in Contexts |-> None]
  /\ pane' = [c \in Contexts |-> FALSE]
  /\ status' = [c \in Contexts |-> "Idle"]
  /\ properties' = [c \in Contexts |-> Empty]
  /\ aborted' = aborted \cup {r \in pending : r.context = "A"}
  /\ last' = IdleLast /\ localeBefore' = <<>>
  /\ UNCHANGED <<pending,used,locale>>

(* Transport can finish after abort. Neither delivery nor finally owns a new view. *)
Complete(r,outcome) ==
  LET eligible == Eligible(r)
      publish == eligible /\ outcome = "Success"
      finalize == FinalizeEligible(r)
  IN /\ r \in pending
     /\ pending' = pending \ {r}
     /\ properties' = IF publish THEN [properties EXCEPT ![r.context] = Payload(r)] ELSE properties
     /\ status' = IF finalize THEN [status EXCEPT ![r.context] = IF publish THEN "Success" ELSE "Failure"] ELSE status
     /\ last' = [published |-> publish, eligible |-> Eligible(r), successful |-> outcome = "Success"]
     /\ localeBefore' = <<>>
     /\ UNCHANGED <<current,alive,epoch,selected,pane,used,aborted,locale>>

ChangeLocale ==
  /\ locale' = IF locale = "en" THEN "ja" ELSE "en"
  /\ localeBefore' = Frame /\ last' = IdleLast
  /\ UNCHANGED <<current,alive,epoch,selected,pane,status,properties,pending,used,aborted>>

Next == \/ \E e \in Epochs, k \in Resources : Open(e,k)
        \/ Close \/ Unmount \/ MountNext
        \/ \E r \in pending, outcome \in {"Success","Forbidden","Missing"} : Complete(r,outcome)
        \/ ChangeLocale

TypeOK == /\ current \in Contexts /\ alive \subseteq Contexts
          /\ epoch \in [Contexts -> Epochs \cup {None}]
          /\ selected \in [Contexts -> Resources \cup {None}]
          /\ pane \in [Contexts -> BOOLEAN]
          /\ status \in [Contexts -> {"Idle","Loading","Success","Failure"}]
          /\ properties \in [Contexts -> {Empty} \cup
               {[present |-> TRUE,context |-> c,epoch |-> e,resource |-> k] :
                 c \in Contexts,e \in Epochs,k \in Resources}]
          /\ pending \subseteq [context : Contexts,epoch : Epochs,resource : Resources]
          /\ used \subseteq Contexts \X Epochs
          /\ aborted \subseteq [context : Contexts,epoch : Epochs,resource : Resources]
          /\ locale \in {"en","ja"}
SuccessfulOnly == ~last.published \/ last.successful
AcceptedOnlyCurrent == ~last.published \/ last.eligible
ClearedWhenClosed == \A c \in Contexts : ~pane[c] => ~properties[c].present
LoadingClearsPrevious == \A c \in Contexts : status[c] = "Loading" => ~properties[c].present
FailureNoDisclosure == \A c \in Contexts : status[c] = "Failure" => ~properties[c].present
CurrentProperties == \A c \in Contexts : properties[c].present =>
  /\ c \in alive /\ pane[c] /\ status[c] = "Success"
  /\ properties[c].context = c /\ properties[c].epoch = epoch[c]
  /\ properties[c].resource = selected[c]
CurrentRequestLoading == \A r \in pending : FinalizeEligible(r) => status[r.context] = "Loading"
LocaleStable == localeBefore = <<>> \/ localeBefore = Frame
Spec == Init /\ [][Next]_vars
=============================================================================
