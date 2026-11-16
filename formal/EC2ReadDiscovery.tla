------------------------- MODULE EC2ReadDiscovery -------------------------
EXTENDS FiniteSets
CONSTANT Regions, Owners
VARIABLE result
vars == <<result>>
Init == result = {}
Read(requestRegion, recordRegion, claimedOwner, storedOwner, allow, deny) ==
 /\ allow /\ ~deny
 /\ requestRegion = recordRegion
 /\ claimedOwner = storedOwner
 /\ result' = {[requestRegion |-> requestRegion, recordRegion |-> recordRegion,
                claimedOwner |-> claimedOwner, storedOwner |-> storedOwner,
                allow |-> allow, deny |-> deny]}
Next == (\E rr, sr \in Regions, co, so \in Owners, a, d \in BOOLEAN:
          Read(rr, sr, co, so, a, d)) \/ UNCHANGED vars
TypeOK == result \subseteq [requestRegion: Regions, recordRegion: Regions,
                            claimedOwner: Owners, storedOwner: Owners,
                            allow: BOOLEAN, deny: BOOLEAN]
AuthorizedRead == \A x \in result: x.allow /\ ~x.deny
RegionBound == \A x \in result: x.requestRegion = x.recordRegion
OwnedRead == \A x \in result: x.claimedOwner = x.storedOwner
=============================================================================
