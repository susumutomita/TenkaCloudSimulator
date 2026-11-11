---------------- MODULE ARPTrustedReply ----------------
EXTENDS Naturals, FiniteSets
Config == [guestIP |-> 1, gatewayIP |-> 2, guestMAC |-> 1, gatewayMAC |-> 2]
BadConfigs == {[Config EXCEPT !.guestIP = 0],[Config EXCEPT !.gatewayIP = 3],
 [Config EXCEPT !.gatewayIP = 1],[Config EXCEPT !.guestMAC = 0],
 [Config EXCEPT !.gatewayMAC = 4],[Config EXCEPT !.gatewayMAC = 1]}
Configs == {Config} \cup BadConfigs
Request == [length |-> 42, eth |-> 2054, padding |-> 0, dst |-> 3, src |-> 1,
 htype |-> 1, ptype |-> 2048, hlen |-> 6, plen |-> 4, op |-> 1,
 smac |-> 1, sip |-> 1, tmac |-> 0, tip |-> 2]
GoodRequests == {[Request EXCEPT !.length = n, !.dst = d, !.tmac = t]:
 n \in {42,60}, d \in {2,3}, t \in {0,2}}
BadRequests == {[Request EXCEPT !.length = 41],[Request EXCEPT !.length = 61],
 [Request EXCEPT !.eth = 2048],[Request EXCEPT !.padding = 1],
 [Request EXCEPT !.dst = 1],[Request EXCEPT !.src = 2],
 [Request EXCEPT !.htype = 2],[Request EXCEPT !.ptype = 34525],
 [Request EXCEPT !.hlen = 5],[Request EXCEPT !.plen = 16],
 [Request EXCEPT !.op = 2],[Request EXCEPT !.smac = 2],
 [Request EXCEPT !.sip = 0],[Request EXCEPT !.tip = 1],
 [Request EXCEPT !.tmac = 1]}
Valid(c) == c.guestIP \in {1,2} /\ c.gatewayIP \in {1,2} /\ c.guestIP # c.gatewayIP
 /\ c.guestMAC \in {1,2} /\ c.gatewayMAC \in {1,2} /\ c.guestMAC # c.gatewayMAC
Admitted(q,c) == q.length >= 42 /\ q.length <= 60 /\ q.eth = 2054 /\ q.padding = 0
 /\ q.dst \in {3,c.gatewayMAC} /\ q.src = c.guestMAC
 /\ q.htype = 1 /\ q.ptype = 2048 /\ q.hlen = 6 /\ q.plen = 4 /\ q.op = 1
 /\ q.smac = c.guestMAC /\ q.sip = c.guestIP /\ q.tip = c.gatewayIP
 /\ q.tmac \in {0,c.gatewayMAC}
Encode(c) == [length |-> 60, eth |-> 2054, padding |-> 0,
 dst |-> c.guestMAC, src |-> c.gatewayMAC, htype |-> 1, ptype |-> 2048,
 hlen |-> 6, plen |-> 4, op |-> 2, smac |-> c.gatewayMAC, sip |-> c.gatewayIP,
 tmac |-> c.guestMAC, tip |-> c.guestIP]
VARIABLES original, caller, binding, request, ready, phase, sent, reply
vars == <<original,caller,binding,request,ready,phase,sent,reply>>
Init == /\ original \in Configs /\ caller = original /\ binding = Config
 /\ request \in GoodRequests \cup BadRequests /\ ready = FALSE
 /\ phase = "new" /\ sent = FALSE /\ reply = Request
Construct == /\ phase = "new" /\ binding' = caller /\ ready' = Valid(caller)
 /\ phase' = "bound" /\ UNCHANGED <<original,caller,request,sent,reply>>
CallerMutation == /\ phase = "bound" /\ caller' \in Configs /\ phase' = "call"
 /\ UNCHANGED <<original,binding,request,ready,sent,reply>>
Reply == /\ phase = "call"
 /\ sent' = (ready /\ Admitted(request,binding))
 /\ reply' = (IF ready /\ Admitted(request,binding) THEN Encode(binding) ELSE reply)
 /\ phase' = "done" /\ UNCHANGED <<original,caller,binding,request,ready>>
Next == Construct \/ CallerMutation \/ Reply
ConstructorExact == phase # "new" => ready = (original = Config)
ImmutableBinding == phase # "new" => binding = original
AdmissionSound == sent => original = Config /\ request \in GoodRequests
AdmissionComplete == phase = "done" /\ original = Config /\ request \in GoodRequests => sent
TrustedReply == sent =>
 /\ reply.dst = original.guestMAC /\ reply.src = original.gatewayMAC
 /\ reply.smac = original.gatewayMAC /\ reply.sip = original.gatewayIP
 /\ reply.tmac = original.guestMAC /\ reply.tip = original.guestIP
ReplyFormat == sent => reply.length = 60 /\ reply.padding = 0 /\ reply.eth = 2054
 /\ reply.htype = 1 /\ reply.ptype = 2048 /\ reply.hlen = 6 /\ reply.plen = 4 /\ reply.op = 2
=============================================================================
