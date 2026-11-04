--------------------------- MODULE KMSKeyState ---------------------------
EXTENDS Naturals
CONSTANTS KeyA, KeyB, TeamA, TeamB, RegionA, RegionB, ContextA, ContextB, SlotA, SlotB, None
Keys == {KeyA, KeyB}
Teams == {TeamA, TeamB}
Regions == {RegionA, RegionB}
Contexts == {ContextA, ContextB}
Slots == {SlotA, SlotB}
Owner(k) == IF k = KeyA THEN TeamA ELSE TeamB
KeyRegion(k) == IF k = KeyA THEN RegionA ELSE RegionB
VARIABLES present, enabled, count, blobs, last
vars == <<present, enabled, count, blobs, last>>
Event(kind, k, t, r, permit, stateOK, contextOK, keyOK) ==
 [kind |-> kind, permit |-> permit, ownerOK |-> t = Owner(k),
  regionOK |-> r = KeyRegion(k), stateOK |-> stateOK,
  contextOK |-> contextOK, keyOK |-> keyOK]
Authority(k,t,r,permit) == permit /\ t = Owner(k) /\ r = KeyRegion(k)
Init ==
 /\ present = {}
 /\ enabled = {}
 /\ count = [k \in Keys |-> 0]
 /\ blobs = [s \in Slots |-> None]
 /\ last = Event("none",KeyA,TeamA,RegionA,TRUE,TRUE,TRUE,TRUE)
Create(k,t,r,permit) ==
 /\ k \notin present
 /\ Authority(k,t,r,permit)
 /\ present' = present \cup {k}
 /\ enabled' = enabled \cup {k}
 /\ last' = Event("mutation",k,t,r,permit,TRUE,TRUE,TRUE)
 /\ UNCHANGED <<count, blobs>>
SetEnabled(k,t,r,permit,on) ==
 /\ k \in present
 /\ Authority(k,t,r,permit)
 /\ enabled' = IF on THEN enabled \cup {k} ELSE enabled \ {k}
 /\ last' = Event("mutation",k,t,r,permit,TRUE,TRUE,TRUE)
 /\ UNCHANGED <<present,count,blobs>>
Encrypt(k,t,r,permit,c,s) ==
 /\ k \in present
 /\ Authority(k,t,r,permit)
 /\ k \in enabled
 /\ count[k] < 2
 /\ count' = [count EXCEPT ![k] = @ + 1]
 /\ blobs' = [blobs EXCEPT ![s] = [key |-> k, region |-> r, context |-> c]]
 /\ last' = Event("crypto",k,t,r,permit,k \in enabled,TRUE,TRUE)
 /\ UNCHANGED <<present,enabled>>
Decrypt(k,t,r,permit,c,s) ==
 /\ k \in present
 /\ Authority(k,t,r,permit)
 /\ k \in enabled
 /\ blobs[s] # None
 /\ blobs[s].key = k
 /\ blobs[s].region = r
 /\ blobs[s].context = c
 /\ last' = Event("crypto",k,t,r,permit,k \in enabled,
                   blobs[s].context = c,blobs[s].key = k /\ blobs[s].region = r)
 /\ UNCHANGED <<present,enabled,count,blobs>>
Next == \E k \in Keys,t \in Teams,r \in Regions,p \in BOOLEAN:
 Create(k,t,r,p) \/ (\E on \in BOOLEAN:SetEnabled(k,t,r,p,on)) \/
 (\E c \in Contexts,s \in Slots:Encrypt(k,t,r,p,c,s) \/ Decrypt(k,t,r,p,c,s))
TypeOK ==
 /\ present \subseteq Keys
 /\ enabled \subseteq present
 /\ count \in [Keys -> 0..2]
 /\ blobs \in [Slots -> ({None} \cup [key:Keys,region:Regions,context:Contexts])]
 /\ last.kind \in {"none","mutation","crypto"}
AuthorizedEvents == last.kind = "none" \/ (last.permit /\ last.ownerOK /\ last.regionOK)
EnabledCrypto == last.kind # "crypto" \/ last.stateOK
BoundContextAndKey == last.kind # "crypto" \/ (last.contextOK /\ last.keyOK)
=============================================================================
