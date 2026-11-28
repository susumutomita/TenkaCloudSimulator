------------------------- MODULE CloudFrontLocalCache -------------------------
EXTENDS Naturals, Integers
CONSTANT MaxSteps
VARIABLES origin, bound, config, enabled, bytes, valid, cached, cacheActor,
          cacheOrigin, cacheConfig, expired, peer, steps, last
Actors == {"a", "b"}
State == [origin |-> origin, bound |-> bound, config |-> config, enabled |-> enabled,
          bytes |-> bytes, valid |-> valid, cached |-> cached, cacheActor |-> cacheActor,
          cacheOrigin |-> cacheOrigin, cacheConfig |-> cacheConfig,
          expired |-> expired, peer |-> peer]
Event(kind, actor, dg, og, ok, output, hit) ==
    [kind |-> kind, actor |-> actor, distributionGrant |-> dg, objectGrant |-> og,
     ok |-> ok, output |-> output, hit |-> hit, before |-> State]
Init == /\ origin = 1 /\ bound = 1 /\ config = 0 /\ enabled = TRUE /\ bytes = 0
        /\ valid = FALSE /\ cached = 0 /\ cacheActor = "a" /\ cacheOrigin = 1
        /\ cacheConfig = 0 /\ expired = FALSE /\ peer = 99 /\ steps = 0
        /\ last = Event("init", "a", FALSE, FALSE, TRUE, -1, FALSE)

Deliver(actor, dg, og, ok) ==
    /\ steps < MaxSteps
    /\ LET allowed == dg /\ og /\ enabled /\ origin # 0 /\ origin = bound (* BothLiveGrants *)
           hit == valid /\ ~expired /\ cacheActor = actor /\ cacheOrigin = origin /\ cacheConfig = config (* ExactCacheIdentity *)
       IN /\ IF allowed /\ ok
              THEN /\ valid' = TRUE /\ cached' = IF hit THEN cached ELSE bytes
                   /\ cacheActor' = actor /\ cacheOrigin' = origin /\ cacheConfig' = config /\ expired' = FALSE
              ELSE UNCHANGED <<valid, cached, cacheActor, cacheOrigin, cacheConfig, expired>>
          /\ last' = Event("deliver", actor, dg, og, ok,
                           IF allowed /\ ok THEN IF hit THEN cached ELSE bytes ELSE -1,
                           allowed /\ ok /\ hit)
    /\ steps' = steps + 1
    /\ UNCHANGED <<origin, bound, config, enabled, bytes, peer>>

ChangeBytes == /\ steps < MaxSteps /\ bytes' = 1 - bytes
               /\ last' = Event("bytes", "a", TRUE, TRUE, TRUE, -1, FALSE)
               /\ steps' = steps + 1
               /\ UNCHANGED <<origin,bound,config,enabled,valid,cached,cacheActor,cacheOrigin,cacheConfig,expired,peer>>
Expire == /\ steps < MaxSteps /\ valid /\ expired' = TRUE
          /\ last' = Event("expire", "a", TRUE, TRUE, TRUE, -1, FALSE)
          /\ steps' = steps + 1
          /\ UNCHANGED <<origin,bound,config,enabled,bytes,valid,cached,cacheActor,cacheOrigin,cacheConfig,peer>>
OriginGone == /\ steps < MaxSteps /\ origin = 1 /\ origin' = 0
              /\ last' = Event("originGone", "a", TRUE, TRUE, TRUE, -1, FALSE)
              /\ steps' = steps + 1
              /\ UNCHANGED <<bound,config,enabled,bytes,valid,cached,cacheActor,cacheOrigin,cacheConfig,expired,peer>>
RecreateOrigin == /\ steps < MaxSteps /\ origin = 0 /\ origin' = 2 /\ bytes' = 1
                  /\ last' = Event("originRecreate", "a", TRUE, TRUE, TRUE, -1, FALSE)
                  /\ steps' = steps + 1
                  /\ UNCHANGED <<bound,config,enabled,valid,cached,cacheActor,cacheOrigin,cacheConfig,expired,peer>>
Update(expected, on, ok) ==
    /\ steps < MaxSteps /\ config < 2 /\ origin # 0
    /\ IF expected = config /\ ok (* ExactETag *)
          THEN /\ config' = config + 1 /\ bound' = origin /\ enabled' = on
               /\ valid' = FALSE (* UpdateEvicts *)
          ELSE UNCHANGED <<config,bound,enabled,valid>>
    /\ last' = Event(IF expected = config THEN "update" ELSE "staleUpdate", "a", TRUE, TRUE, ok, -1, FALSE)
    /\ steps' = steps + 1
    /\ UNCHANGED <<origin,bytes,cached,cacheActor,cacheOrigin,cacheConfig,expired,peer>>
Invalidate(ok) ==
    /\ steps < MaxSteps
    /\ valid' = IF ok THEN FALSE ELSE valid (* InvalidationAtomic *)
    /\ last' = Event("invalidate", "a", TRUE, TRUE, ok, -1, FALSE)
    /\ steps' = steps + 1
    /\ UNCHANGED <<origin,bound,config,enabled,bytes,cached,cacheActor,cacheOrigin,cacheConfig,expired,peer>>
Restart == /\ steps < MaxSteps /\ last' = Event("restart", "a", TRUE, TRUE, TRUE, -1, FALSE)
           /\ steps' = steps + 1
           /\ UNCHANGED <<origin,bound,config,enabled,bytes,valid,cached,cacheActor,cacheOrigin,cacheConfig,expired,peer>>
Next == \/ \E actor \in Actors, dg, og, ok \in BOOLEAN : Deliver(actor,dg,og,ok)
        \/ ChangeBytes \/ Expire \/ OriginGone \/ RecreateOrigin
        \/ \E expected \in 0..2, on, ok \in BOOLEAN : Update(expected,on,ok)
        \/ \E ok \in BOOLEAN : Invalidate(ok)
        \/ Restart

TypeOK == /\ origin \in 0..2 /\ bound \in 1..2 /\ config \in 0..2 /\ enabled \in BOOLEAN
          /\ bytes \in 0..1 /\ cached \in 0..1 /\ valid \in BOOLEAN /\ expired \in BOOLEAN
          /\ cacheActor \in Actors /\ cacheOrigin \in 1..2 /\ cacheConfig \in 0..2
          /\ steps \in 0..MaxSteps /\ peer = 99
FreshJointAuthority == (last.kind = "deliver" /\ last.output # -1) =>
    /\ last.distributionGrant /\ last.objectGrant /\ last.before.enabled
    /\ last.before.origin # 0 /\ last.before.origin = last.before.bound
ExactHitIdentity == last.hit =>
    /\ last.before.valid /\ ~last.before.expired /\ last.actor = last.before.cacheActor
    /\ last.before.origin = last.before.cacheOrigin /\ last.before.config = last.before.cacheConfig
MissReadsOrigin == (last.kind = "deliver" /\ last.output # -1 /\ ~last.hit) => last.output = last.before.bytes
FailureRollsBack == ~last.ok => State = last.before
RefusalUnchanged == (last.kind = "deliver" /\ last.output = -1) => State = last.before
StaleETagUnchanged == last.kind = "staleUpdate" => State = last.before
UpdateClearsCache == (last.kind = "update" /\ last.ok) => ~valid
InvalidationClearsCache == (last.kind = "invalidate" /\ last.ok) => ~valid
RestartPreserves == last.kind = "restart" => State = last.before
PeerUnchanged == peer = 99
=============================================================================
