-------------------------- MODULE SecretsRevealEpoch --------------------------
EXTENDS Naturals, FiniteSets
CONSTANT Resources
VARIABLES alive, selected, epoch, pending, visible, emissions
vars == <<alive, selected, epoch, pending, visible, emissions>>
Request(r,e) == [resource |-> r, ticket |-> e]
Init == /\ alive=TRUE /\ selected \in Resources /\ epoch=0
        /\ pending={} /\ visible={} /\ emissions={}

\* Each explicit retrieve invalidates earlier replies and clears the prior value.
Retrieve == /\ alive /\ epoch<2
            /\ epoch'=epoch+1
            /\ pending'=pending \cup {Request(selected,epoch+1)}
            /\ visible'={}
            /\ UNCHANGED <<alive, selected, emissions>>
Navigate(r) == /\ alive /\ epoch<2
               /\ selected'=r /\ epoch'=epoch+1 /\ visible'={}
               /\ UNCHANGED <<alive, pending, emissions>>
Hide == /\ alive /\ epoch<2
        /\ epoch'=epoch+1 /\ visible'={}
        /\ UNCHANGED <<alive, selected, pending, emissions>>
Unmount == /\ alive /\ epoch<3
           /\ alive'=FALSE /\ epoch'=epoch+1 /\ visible'={}
           /\ UNCHANGED <<selected, pending, emissions>>

\* A success response may be delayed or contain a mismatched ARN. The UI
\* accepts it only for the current explicit request and matching returned ARN.
Reply(req,returned) ==
    /\ req \in pending
    /\ alive /\ req.ticket=epoch /\ returned=req.resource
    /\ pending'=pending \ {req}
    /\ visible'={Request(returned,req.ticket)}
    /\ emissions'=emissions \cup {
        [requested |-> req.resource, returned |-> returned,
         selectedAtEmission |-> selected, requestEpoch |-> req.ticket,
         epochAtEmission |-> epoch, aliveAtEmission |-> alive]}
    /\ UNCHANGED <<alive, selected, epoch>>
Reject(req) == /\ req \in pending
               /\ pending'=pending \ {req}
               /\ UNCHANGED <<alive, selected, epoch, visible, emissions>>
Next == Retrieve \/ Hide \/ Unmount
        \/ (\E r \in Resources : Navigate(r))
        \/ (\E req \in pending, returned \in Resources : Reply(req,returned))
        \/ (\E req \in pending : Reject(req))
        \/ UNCHANGED vars
TypeOK ==
    /\ alive \in BOOLEAN /\ selected \in Resources /\ epoch \in 0..3
    /\ pending \subseteq [resource:Resources,ticket:1..2]
    /\ visible \subseteq [resource:Resources,ticket:1..2]
    /\ emissions \subseteq [requested:Resources,returned:Resources,
        selectedAtEmission:Resources,requestEpoch:1..2,
        epochAtEmission:0..3,aliveAtEmission:BOOLEAN]
NoUnmountedDisplay == ~alive => visible={}
VisibleContextMatches == \A r \in visible : r.resource=selected /\ r.ticket=epoch
FreshEmissionHistory == \A r \in emissions :
    r.aliveAtEmission /\ r.requestEpoch=r.epochAtEmission
ReturnedARNMatches == \A r \in emissions :
    r.requested=r.returned /\ r.returned=r.selectedAtEmission
=============================================================================
