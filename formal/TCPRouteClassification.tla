---------------- MODULE TCPRouteClassification ----------------
EXTENDS Naturals, Integers, Sequences, FiniteSets
Base == [lip |-> 1, pip |-> 2, lm |-> 1, pm |-> 2, lp |-> 1, pp |-> 1, any |-> FALSE]
Pool == {Base, [Base EXCEPT !.pp = 2],
         [Base EXCEPT !.pp = 0, !.any = TRUE], [Base EXCEPT !.lp = 2],
         [Base EXCEPT !.lp = 0], [Base EXCEPT !.pm = 1]}
Configs == {<<>>} \cup {<<a>>: a \in Pool} \cup {<<a,b>>: a,b \in Pool}
Packets == [sip:0..2, dip:0..2, sm:0..2, dm:0..2, sp:0..2, dp:0..2]
Blank == [sip |-> 0, dip |-> 0, sm |-> 0, dm |-> 0, sp |-> 0, dp |-> 0]
Valid(r) == r.lip \in {1,2} /\ r.pip \in {1,2} /\ r.lip # r.pip
 /\ r.lm \in {1,2} /\ r.pm \in {1,2} /\ r.lm # r.pm /\ r.lp # 0
 /\ ((r.any /\ r.pp = 0) \/ (~r.any /\ r.pp # 0))
SameBase(a,b) == <<a.lip,a.pip,a.lm,a.pm,a.lp>> = <<b.lip,b.pip,b.lm,b.pm,b.lp>>
Overlap(a,b) == SameBase(a,b) /\ (a.any \/ b.any \/ a.pp = b.pp)
ValidConfig(c) == Len(c) > 0 /\ Len(c) <= 2 /\ (\A i \in DOMAIN c: Valid(c[i]))
 /\ \A i,j \in DOMAIN c: i < j => ~Overlap(c[i],c[j])
Normalize(p,dir) == IF dir = "to"
 THEN [lip |-> p.sip, pip |-> p.dip, lm |-> p.sm, pm |-> p.dm, lp |-> p.sp, pp |-> p.dp]
 ELSE [lip |-> p.dip, pip |-> p.sip, lm |-> p.dm, pm |-> p.sm, lp |-> p.dp, pp |-> p.sp]
Matches(r,n) == <<r.lip,r.pip,r.lm,r.pm,r.lp>> = <<n.lip,n.pip,n.lm,n.pm,n.lp>>
 /\ (r.any \/ r.pp = n.pp)
\* Independent directional oracle; no Normalize or Matches reused.
Expected(r,p,dir) ==
 IF dir = "to" THEN
   p.sip = r.lip /\ p.dip = r.pip /\ p.sm = r.lm /\ p.dm = r.pm
   /\ p.sp = r.lp /\ (r.any \/ p.dp = r.pp)
 ELSE
   p.dip = r.lip /\ p.sip = r.pip /\ p.dm = r.lm /\ p.sm = r.pm
   /\ p.dp = r.lp /\ (r.any \/ p.sp = r.pp)
VARIABLES raw, routes, ready, phase, packet, direction, syntax, result
vars == <<raw,routes,ready,phase,packet,direction,syntax,result>>
Init == /\ raw \in Configs /\ routes = <<>> /\ ready = FALSE /\ phase = "new"
 /\ packet = Blank /\ direction = "invalid" /\ syntax = FALSE /\ result = -1
Construct ==
 /\ phase = "new"
 /\ ready' = ValidConfig(raw)
 /\ routes' = IF ValidConfig(raw) THEN raw ELSE <<>>
 /\ phase' = "configured"
 /\ UNCHANGED <<raw,packet,direction,syntax,result>>
Classify(p,dir,parsed) ==
 /\ phase = "configured"
 /\ LET permitted == ready /\ dir \in {"from","to"} /\ parsed /\ p.sp # 0 /\ p.dp # 0
        candidates == {i \in DOMAIN routes: Matches(routes[i], Normalize(p,dir))}
    IN result' = IF permitted /\ candidates # {} THEN (CHOOSE i \in candidates: \A j \in candidates: i <= j)-1 ELSE -1
 /\ packet' = p /\ direction' = dir /\ syntax' = parsed /\ phase' = "done"
 /\ UNCHANGED <<raw,routes,ready>>
Next == Construct \/ (phase = "configured" /\ (\E p \in Packets, dir \in {"from","to","invalid"}, parsed \in BOOLEAN: Classify(p,dir,parsed)))
TypeOK == /\ raw \in Configs /\ routes \in Configs /\ ready \in BOOLEAN
 /\ phase \in {"new","configured","done"} /\ packet \in Packets
 /\ direction \in {"from","to","invalid"} /\ syntax \in BOOLEAN /\ result \in {-1,0,1}
ConstructorGuard == phase # "new" => ready = ValidConfig(raw)
CopiedConfiguration == ready => routes = raw
NoAmbiguousRoutes == ready => \A i,j \in DOMAIN routes: i # j => ~Overlap(routes[i],routes[j])
FailClosed == result # -1 => ready /\ direction \in {"from","to"} /\ syntax
NonzeroPorts == result # -1 => packet.sp # 0 /\ packet.dp # 0
ExactDirectionalTuple == result # -1 => result+1 \in DOMAIN routes /\ Expected(routes[result+1],packet,direction)
SoleIndex == result # -1 => \A i \in DOMAIN routes: Expected(routes[i],packet,direction) => i = result+1
CompleteSelection == phase = "done" /\ ready /\ direction \in {"from","to"} /\ syntax /\ packet.sp # 0 /\ packet.dp # 0 =>
 ((result # -1) <=> (\E i \in DOMAIN routes: Expected(routes[i],packet,direction)))
=============================================================================
