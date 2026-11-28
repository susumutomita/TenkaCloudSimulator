--------------------- MODULE StoreConnectionLifetime ---------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Stores, Values, MaxSteps
VARIABLES live, connected, configured, backing, accepted, staged, event, step
vars == <<live, connected, configured, backing, accepted, staged, event, step>>
Empty == "empty"
Data == Values \cup {Empty}

(* One logical row name is shared by both stores; the store identity owns its
   backing namespace. Values stand for committed core/provider data and schema.
   Initialization succeeded. SQLite transactions, unique names and Close success
   are assumptions, not a driver/SQL/refinement or crash-recovery proof. *)
Init == /\ live = Stores /\ connected = Stores /\ configured = Stores
        /\ backing = [s \in Stores |-> Empty]
        /\ accepted = [s \in Stores |-> Empty]
        /\ staged = [s \in Stores |-> Empty]
        /\ event = [kind |-> "Init", owner |-> CHOOSE s \in Stores: TRUE,
                     before |-> [s \in Stores |-> Empty]]
        /\ step = 0
Mark(kind, s) == /\ event' = [kind |-> kind, owner |-> s, before |-> backing]
                 /\ step' = step + 1
Begin(s, value) == /\ s \in live \cap connected /\ staged[s] = Empty
                   /\ staged' = [staged EXCEPT ![s] = value]
                   /\ UNCHANGED <<live, connected, configured, backing, accepted>>
                   /\ Mark("Begin", s)
Commit(s) == /\ s \in live \cap connected /\ staged[s] \in Values
             /\ backing' = [backing EXCEPT ![s] = staged[s]] (* CommitOwnBacking *)
             /\ accepted' = [accepted EXCEPT ![s] = staged[s]]
             /\ staged' = [staged EXCEPT ![s] = Empty]
             /\ UNCHANGED <<live, connected, configured>> /\ Mark("Commit", s)
Cancel(s) == /\ s \in live \cap connected /\ staged[s] \in Values
             /\ staged' = [staged EXCEPT ![s] = Empty] (* DiscardCanceledStage *)
             /\ connected' = connected \ {s} /\ configured' = configured \ {s}
             /\ backing' = backing (* CancelKeepsCommitted *)
             /\ UNCHANGED <<live, accepted>> /\ Mark("Cancel", s)
Disconnect(s) == /\ s \in live \cap connected /\ staged[s] = Empty
                 /\ connected' = connected \ {s} /\ configured' = configured \ {s}
                 /\ backing' = backing (* ConnectionDoesNotOwnBacking *)
                 /\ UNCHANGED <<live, accepted, staged>> /\ Mark("Disconnect", s)
Reconnect(s) == /\ s \in live /\ s \notin connected (* LiveStoreRequired *)
                /\ connected' = connected \cup {s}
                /\ configured' = configured \cup {s} (* ReapplyConnectionPragmas *)
                /\ UNCHANGED <<live, backing, accepted, staged>> /\ Mark("Reconnect", s)
Dispose(s) == /\ s \in live /\ staged[s] = Empty
              /\ live' = live \ {s} /\ connected' = connected \ {s}
              /\ configured' = configured \ {s}
              /\ backing' = [backing EXCEPT ![s] = Empty] (* ReleaseOwnedBacking *)
              /\ UNCHANGED <<accepted, staged>> /\ Mark("Dispose", s)
Next == /\ step < MaxSteps
        /\ \E s \in Stores: (\E value \in Values: Begin(s, value))
              \/ Commit(s) \/ Cancel(s) \/ Disconnect(s) \/ Reconnect(s) \/ Dispose(s)
TypeOK == /\ live \subseteq Stores /\ connected \subseteq Stores
          /\ configured \subseteq Stores /\ backing \in [Stores -> Data]
          /\ accepted \in [Stores -> Data] /\ staged \in [Stores -> Data]
          /\ step \in 0..MaxSteps /\ event.owner \in Stores
          /\ event.before \in [Stores -> Data]
          /\ event.kind \in {"Init", "Begin", "Commit", "Cancel", "Disconnect", "Reconnect", "Dispose"}
PeerBackingUnchanged == \A s \in Stores \ {event.owner}: backing[s] = event.before[s]
ConnectionTurnoverPreserves == event.kind \in {"Disconnect", "Reconnect"} => backing = event.before
CancelDiscardsStaged == event.kind = "Cancel" => staged[event.owner] = Empty /\ backing = event.before
DisposedBackingReleased == \A s \in Stores \ live: backing[s] = Empty
ClosedStoresUnavailable == connected \subseteq live
ConnectionPragmasApplied == configured = connected
LiveCommittedData == \A s \in live: backing[s] = accepted[s]
Spec == Init /\ [][Next]_vars
=============================================================================
