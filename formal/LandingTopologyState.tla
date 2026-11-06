----------------------- MODULE LandingTopologyState -----------------------
EXTENDS Naturals, FiniteSets
CONSTANT Absent
Providers == {"aws", "gcp", "azure"}
Nodes == {"aws-gateway", "aws-lambda", "aws-s3", "aws-ssm", "aws-iam",
          "gcp-storage", "gcp-object", "azure-container", "azure-blob"}
Owner(n) == IF n \in {"aws-gateway", "aws-lambda", "aws-s3", "aws-ssm", "aws-iam"}
            THEN "aws" ELSE IF n \in {"gcp-storage", "gcp-object"} THEN "gcp" ELSE "azure"
Default(p) == CASE p = "aws" -> "aws-lambda"
                [] p = "gcp" -> "gcp-storage"
                [] p = "azure" -> "azure-blob"
(* Independent literal expected pairs, not Default/Owner evaluation. *)
ProviderPairs == {<<"aws", "aws-lambda">>, <<"gcp", "gcp-storage">>, <<"azure", "azure-blob">>}
NodePairs == {<<"aws", "aws-gateway">>, <<"aws", "aws-lambda">>, <<"aws", "aws-s3">>,
 <<"aws", "aws-ssm">>, <<"aws", "aws-iam">>, <<"gcp", "gcp-storage">>,
 <<"gcp", "gcp-object">>, <<"azure", "azure-container">>, <<"azure", "azure-blob">>}
UI(p,n) == [provider |-> p, node |-> n, group |-> p, pressed |-> p,
             current |-> n, detail |-> n, available |-> TRUE]
FreshUI == UI("aws", "aws-lambda")
VARIABLES ui, decoration, capability, reducedMotion, selections, paints, last
vars == <<ui, decoration, capability, reducedMotion, selections, paints, last>>
Event(k,i) == [kind |-> k, input |-> i, beforeUI |-> ui, beforeDecoration |-> decoration]
Init == /\ ui = FreshUI
        /\ decoration = [phase |-> "new", shown |-> FALSE, pending |-> FALSE,
              provider |-> "aws", node |-> "aws-lambda", drawn |-> FALSE,
              drawnProvider |-> "aws", drawnNode |-> "aws-lambda"]
        /\ capability \in BOOLEAN /\ reducedMotion \in BOOLEAN
        /\ selections = 0 /\ paints = 0
        /\ last = [kind |-> "init", input |-> Absent, beforeUI |-> FreshUI,
                    beforeDecoration |-> decoration]
Select(k,i,p,n) ==
 /\ selections < 2
 (* Selection updates every stored HTML projection; Init uses the separate FreshUI. *)
 /\ ui' = UI(p,n)
 /\ decoration' = (IF decoration.phase = "alive" /\ (decoration.provider # p \/ decoration.node # n)
       THEN [decoration EXCEPT !.provider = p, !.node = n, !.pending = TRUE]
       ELSE decoration)
 /\ selections' = selections + 1
 /\ last' = Event(k,i)
 /\ UNCHANGED <<capability, reducedMotion, paints>>
ProviderSelect == \E p \in Providers : Select("provider",p,p,Default(p))
NodeSelect == \E n \in Nodes : Select("node",n,Owner(n),n)
InvalidSelect ==
 /\ selections < 2
 /\ ui' = ui
 /\ decoration' = decoration
 /\ selections' = selections + 1
 /\ last' = Event("invalid",Absent)
 /\ UNCHANGED <<capability, reducedMotion, paints>>
Mount ==
 /\ decoration.phase = "new"
 /\ decoration' = (IF capability
      THEN [decoration EXCEPT !.phase = "alive", !.shown = TRUE, !.pending = TRUE,
            !.provider = ui.provider, !.node = ui.node]
      ELSE [decoration EXCEPT !.phase = "dead", !.shown = FALSE, !.pending = FALSE])
 /\ last' = Event("mount",Absent)
 /\ UNCHANGED <<ui, capability, reducedMotion, selections, paints>>
Paint ==
 /\ decoration.phase = "alive" /\ paints < 2
 /\ decoration' = [decoration EXCEPT !.pending = FALSE, !.drawn = TRUE,
                    !.drawnProvider = decoration.provider, !.drawnNode = decoration.node]
 /\ paints' = paints + 1
 /\ last' = Event("paint",Absent)
 /\ UNCHANGED <<ui, capability, reducedMotion, selections>>
Retire(reason) ==
 /\ decoration.phase = "alive"
 /\ (reason = "error" \/ reason = "destroy" \/ (reason = "timeout" /\ decoration.pending))
 /\ decoration' = [decoration EXCEPT !.phase = "dead", !.shown = FALSE, !.pending = FALSE]
 /\ last' = Event(reason,Absent)
 /\ UNCHANGED <<ui, capability, reducedMotion, selections, paints>>
LatePaint ==
 /\ decoration.phase = "dead" /\ paints < 2
 /\ decoration' = decoration
 /\ paints' = paints + 1
 /\ last' = Event("latePaint",Absent)
 /\ UNCHANGED <<ui, capability, reducedMotion, selections>>
(* The finite horizon is complete only after retirement/unsupported mount,
   both bounded selections, and both bounded paint callbacks. Preserve last:
   completion must not erase the immediately preceding invariant oracle. *)
Done == /\ decoration.phase = "dead" /\ selections = 2 /\ paints = 2
Complete == /\ Done /\ UNCHANGED vars
Next == ProviderSelect \/ NodeSelect \/ InvalidSelect \/ Mount \/ Paint
        \/ (\E reason \in {"error","destroy","timeout"} : Retire(reason)) \/ LatePaint \/ Complete
Spec == Init /\ [][Next]_vars
TypeOK == /\ ui \in [provider:Providers,node:Nodes,group:Providers,pressed:Providers,
                       current:Nodes,detail:Nodes,available:BOOLEAN]
          /\ decoration \in [phase:{"new","alive","dead"},shown:BOOLEAN,pending:BOOLEAN,
                provider:Providers,node:Nodes,drawn:BOOLEAN,drawnProvider:Providers,drawnNode:Nodes]
          /\ capability \in BOOLEAN /\ reducedMotion \in BOOLEAN
          /\ selections \in 0..2 /\ paints \in 0..2
ProviderSelectsDefault == last.kind = "provider" =>
          <<ui.provider,ui.node>> \in ProviderPairs /\ ui.provider = last.input
NodeClickSelectsExact == last.kind = "node" => ui.node = last.input /\ <<ui.provider,ui.node>> \in NodePairs
InvalidInputPreservesSelection == last.kind = "invalid" => ui = last.beforeUI /\ decoration = last.beforeDecoration
DOMSelectionAgrees == /\ <<ui.provider,ui.node>> \in NodePairs
                     /\ ui.group = ui.provider /\ ui.pressed = ui.provider
                     /\ ui.current = ui.node /\ ui.detail = ui.node
CanonicalHTMLAvailable == ui.available
MountUsesLatestSelection == last.kind = "mount" /\ capability =>
                     decoration.provider = ui.provider /\ decoration.node = ui.node
UnsupportedCanvasHidden == ~capability => ~decoration.shown
RetiredCanvasHidden == decoration.phase = "dead" => ~decoration.shown /\ ~decoration.pending
PaintUsesLatestSelection == last.kind = "paint" =>
                     decoration.drawnProvider = ui.provider /\ decoration.drawnNode = ui.node
RetiredCallbackNoEffect == last.kind = "latePaint" => decoration = last.beforeDecoration
=============================================================================
