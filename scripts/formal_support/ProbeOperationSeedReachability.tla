-------------- MODULE ProbeOperationSeedReachability --------------
EXTENDS ProbeOperationLifecycle
VARIABLE chosen
ReachVars == <<vars, chosen>>
RawStart == <<"Start", 1, "raw", 1, 1, TRUE>>
ScopedStart == <<"Start", 1, "scoped", 1, 1, TRUE>>
SecondStart == <<"Start", 2, "raw", 2, 2, TRUE>>
OnePlan == <<RawStart>>
WorkerPlan == Append(OnePlan, <<"Admit",1>>)
RoutePlan == Append(WorkerPlan, <<"Route",1>>)
DispatchPlan == Append(RoutePlan, <<"Dispatch",1>>)
TimeoutPlan == Append(Append(DispatchPlan, <<"Tick">>), <<"Timeout",1>>)
EndPlan == Append(DispatchPlan, <<"End",1,"success">>)
PublishPlan == Append(EndPlan, <<"Publish",1>>)
SettlePlan == Append(PublishPlan, <<"Settle",1>>)
FailedEndPlan == Append(TimeoutPlan, <<"End",1,"failure">>)
FailedSettlePlan == Append(FailedEndPlan, <<"Settle",1>>)
WirePlan == Append(SettlePlan, <<"Lookup",1,"raw",0>>)
Plans == << <<>>, << <<"Bind",1>> >>, OnePlan, << <<"Bind",1>>, ScopedStart >>,
 WorkerPlan, RoutePlan, DispatchPlan, TimeoutPlan, EndPlan, PublishPlan, SettlePlan,
 Append(OnePlan, <<"Reject",1>>), FailedEndPlan, FailedSettlePlan,
 WirePlan, Append(FailedSettlePlan, <<"Lookup",1,"raw",0>>),
 Append(WirePlan, <<"Malformed",1>>), Append(WirePlan, <<"HostExpire",1>>),
 Append(SettlePlan, SecondStart), Append(WorkerPlan, SecondStart),
 << <<"Tick">>, RawStart >> >>
ReachInit == /\ chosen \in 1..Len(Plans) /\ Initialize(EmptyState) /\ depth = 0
ReachNext ==
 /\ depth < Len(Plans[chosen])
 /\ LET c == Plans[chosen][depth+1] IN
    CASE c[1] = "Bind" -> Bind(c[2])
      [] c[1] = "Start" -> Start(c[2], c[3], c[4], c[5], c[6])
      [] c[1] = "Admit" -> AdmitWorker(c[2])
      [] c[1] = "Route" -> CaptureRoute(c[2])
      [] c[1] = "Dispatch" -> Dispatch(c[2])
      [] c[1] = "Timeout" -> Timeout(c[2])
      [] c[1] = "End" -> WorkerEnd(c[2], c[3])
      [] c[1] = "Publish" -> Publish(c[2])
      [] c[1] = "Settle" -> Settle(c[2])
      [] c[1] = "Lookup" -> Lookup(c[2], c[3], c[4])
      [] c[1] = "Reject" -> RejectBeforeWorker(c[2])
      [] c[1] = "Malformed" -> MalformedRead(c[2])
      [] c[1] = "HostExpire" -> HostExpire(c[2])
      [] c[1] = "Tick" -> Tick
 /\ depth' = depth + 1 /\ UNCHANGED chosen
SeedTargetReached == depth = Len(Plans[chosen]) => State = SeedTargets[chosen]
PrefixHasNext == depth < Len(Plans[chosen]) => ENABLED ReachNext
===================================================================
