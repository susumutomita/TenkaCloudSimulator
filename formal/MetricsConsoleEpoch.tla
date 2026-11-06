------------------------- MODULE MetricsConsoleEpoch -------------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANT None
VARIABLES scenario, pc, ui, requests, last
vars == <<scenario,pc,ui,requests,last>>
Channels == {"list","statistics"}
Ids == 1..3
Q(ns,dim,unit,window,valid) == [namespace |-> ns,metric |-> "DummyRequests",
 dimension |-> dim,unit |-> unit,window |-> window,valid |-> valid]
QCount == Q("A","selected","Count","first",TRUE)
QBytes == Q("A","selected","Bytes","first",TRUE)
QDimension == Q("A","other","Count","first",TRUE)
QNamespace == Q("B","selected","Count","first",TRUE)
QWindow == Q("A","selected","Count","second",TRUE)
QInvalid == Q("A","selected","Count","first",FALSE)
QueryFor(id) == CASE id="bytes" -> QBytes [] id="dimension" -> QDimension
 [] id="namespace" -> QNamespace [] id="window" -> QWindow
 [] id="invalid" -> QInvalid [] OTHER -> QCount
ScopeA == [principal |-> 1,region |-> 1,request |-> 1,lifetime |-> 1]
(* Distinct opaque fixture rows stand for genuine bounded native responses, not
   client-computed aggregations. Expected operators below are separate literals.
   request identity does NOT select a DB or alter data. Retired React instances
   have an independent delivery boundary; bypassing a component current() guard
   cannot make an unmounted instance publish into a different React instance. *)
WireList(scope,filter) == IF filter="a" THEN {"A/selected","A/other"}
                         ELSE IF filter="b" THEN {"B/selected"}
                         ELSE {"A/selected","A/other","B/selected"}
WireStats(scope,q) == IF scope.region=2 THEN {"Count:22,44,20,24,2"}
 ELSE IF q.namespace="B" THEN {"Count:777,777,777,777,1"}
 ELSE IF q.dimension="other" THEN {"Count:999,999,999,999,1"}
 ELSE IF q.unit="Bytes" THEN {"Bytes:12,24,11,13,2"}
 ELSE IF q.window="second" THEN {"Count:44,88,40,48,2"}
 ELSE {"Count:3,6,2,4,2"}
ExpectedList(filter) == CASE filter="a" -> {"A/selected","A/other"}
 [] filter="b" -> {"B/selected"} [] OTHER -> {"A/selected","A/other","B/selected"}
ExpectedStats(scope,q) == CASE scope.region=2 -> {"Count:22,44,20,24,2"}
 [] q=QNamespace -> {"Count:777,777,777,777,1"}
 [] q=QDimension -> {"Count:999,999,999,999,1"}
 [] q=QBytes -> {"Bytes:12,24,11,13,2"}
 [] q=QWindow -> {"Count:44,88,40,48,2"}
 [] q=QCount -> {"Count:3,6,2,4,2"} [] OTHER -> {}
EmptyRequest == [exists |-> FALSE,channel |-> "statistics",scope |-> ScopeA,
 revision |-> 0,query |-> QCount,filter |-> "a"]
InitialUI == [scope |-> ScopeA,query |-> QCount,selected |-> TRUE,
 filter |-> "all",revision |-> [list |-> 1,statistics |-> 2],
 rows |-> [list |-> {"A/selected","A/other","B/selected"},statistics |-> {}],loaded |-> None,
 busy |-> [c \in Channels |-> FALSE],error |-> "none",locale |-> "en"]
(* Initial selected/list state is an explicit reachable prelude: a completed
   ListMetrics and selecting its full identity. Epochs are normalized relative
   to that prelude. No HTTP/parent authentication follows from this assumption. *)
Scenarios == {"ListCurrentSuccess", "ListCurrentError", "ListScopeSuccess_principal", "ListScopeSuccess_region", "ListScopeSuccess_request", "ListScopeSuccess_lifetime", "ListScopeError", "ListScopeFinally", "StatsCurrentSuccess", "StatsCurrentError", "StatsOlderSuccess", "StatsOlderError", "StatsOlderFinally", "StatsScopeSuccess_principal", "StatsScopeSuccess_region", "StatsScopeSuccess_request", "StatsScopeSuccess_lifetime", "StatsScopeError", "StatsScopeFinally", "QueryEditRetiresPending", "BrowseRetiresPendingStats", "StatsRepeatClears", "BrowseClearsLoadedStats", "EditClears_bytes", "Capture_bytes", "EditClears_dimension", "Capture_dimension", "EditClears_namespace", "Capture_namespace", "EditClears_window", "Capture_window", "EditClears_invalid", "Capture_invalid", "LocalePreservesLoaded", "InvalidRead", "ScopeClearsLoaded", "BrowseInitialClears"}
Plan(s) ==
  CASE s="ListCurrentSuccess" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="ListCurrentError" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "error"]>>
    [] s="ListScopeSuccess_principal" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "principal", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 3, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="ListScopeSuccess_region" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 3, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="ListScopeSuccess_request" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "request", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 3, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="ListScopeSuccess_lifetime" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "lifetime", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 3, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="ListScopeError" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 3, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "error"]>>
    [] s="ListScopeFinally" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "a", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 3, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsCurrentSuccess" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsCurrentError" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "error"]>>
    [] s="StatsOlderSuccess" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 2, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsOlderError" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 2, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "error"]>>
    [] s="StatsOlderFinally" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 2, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 2, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsScopeSuccess_principal" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "principal", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "error"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsScopeSuccess_region" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsScopeSuccess_request" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "request", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "error"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsScopeSuccess_lifetime" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "lifetime", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsScopeError" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "error"]>>
    [] s="StatsScopeFinally" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 3, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="QueryEditRetiresPending" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="BrowseRetiresPendingStats" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 2, arg |-> "b", outcome |-> "success"],
           [kind |-> "complete", channel |-> "list", id |-> 2, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="StatsRepeatClears" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 2, arg |-> "count", outcome |-> "success"]>>
    [] s="BrowseClearsLoadedStats" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "begin", channel |-> "list", id |-> 2, arg |-> "a", outcome |-> "success"]>>
    [] s="EditClears_bytes" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "bytes", outcome |-> "success"]>>
    [] s="Capture_bytes" -> <<[kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "bytes", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="EditClears_dimension" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "dimension", outcome |-> "success"]>>
    [] s="Capture_dimension" -> <<[kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "dimension", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="EditClears_namespace" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "namespace", outcome |-> "success"]>>
    [] s="Capture_namespace" -> <<[kind |-> "select", channel |-> "statistics", id |-> 1, arg |-> "namespace", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="EditClears_window" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "window", outcome |-> "success"]>>
    [] s="Capture_window" -> <<[kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "window", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="EditClears_invalid" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "invalid", outcome |-> "success"]>>
    [] s="Capture_invalid" -> <<[kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "invalid", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="LocalePreservesLoaded" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "locale", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="InvalidRead" -> <<[kind |-> "edit", channel |-> "statistics", id |-> 1, arg |-> "invalid", outcome |-> "success"],
           [kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"]>>
    [] s="ScopeClearsLoaded" -> <<[kind |-> "begin", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "complete", channel |-> "statistics", id |-> 1, arg |-> "count", outcome |-> "success"],
           [kind |-> "scope", channel |-> "statistics", id |-> 1, arg |-> "region", outcome |-> "success"]>>
    [] s="BrowseInitialClears" -> <<[kind |-> "begin", channel |-> "list", id |-> 1, arg |-> "b", outcome |-> "success"]>>

Init == /\ scenario \in Scenarios /\ pc=1 /\ ui=InitialUI
 /\ requests=[i \in Ids |-> EmptyRequest]
 /\ last=[event |-> "none",before |-> InitialUI,after |-> InitialUI,
          request |-> EmptyRequest,channel |-> "statistics",outcome |-> "none",
          intentQuery |-> QCount,intentFilter |-> "a"]
Effect(after,nextRequests,event,r,ch,outcome,intentQuery,intentFilter) ==
 /\ ui'=after /\ requests'=nextRequests /\ pc'=pc+1
 /\ last'=[event |-> event,before |-> ui,after |-> after,request |-> r,
    channel |-> ch,outcome |-> outcome,intentQuery |-> intentQuery,intentFilter |-> intentFilter]
 /\ UNCHANGED scenario
Begin(e) ==
 /\ ~requests[e.id].exists /\ ~ui.busy[e.channel]
 /\ LET ch == e.channel
        query == ui.query
        capturedQuery == query (* CaptureQuery *)
        capturedFilter == (IF ch="list" THEN e.arg ELSE "all") (* CaptureListFilter *)
        revision == ui.revision[ch]+1
        r == [exists |-> TRUE,channel |-> ch,scope |-> ui.scope,
              revision |-> revision,query |-> capturedQuery,filter |-> capturedFilter]
        valid == ch="list" \/ query.valid (* FrontendValidation *)
        listRows == IF ch="list" THEN {} ELSE ui.rows.list (* ListStartClear *)
        statRows == IF ch="list" THEN {} ELSE {} (* StatisticsStartClear *)
        loaded == None (* BeginSnapshotClear *)
        after == [ui EXCEPT !.revision=(IF ch="list"
          THEN [list |-> revision,statistics |-> ui.revision.statistics+1]
          ELSE [list |-> ui.revision.list,statistics |-> revision]),
          !.rows.list=listRows,!.rows.statistics=statRows,!.loaded=loaded,
          !.selected=(IF ch="list" THEN FALSE ELSE @),
          !.filter=(IF ch="list" THEN e.arg ELSE @),
          !.busy.list=(IF ch="list" THEN TRUE ELSE @),
          !.busy.statistics=(IF ch="list" THEN FALSE ELSE valid),
          !.error=(IF valid THEN "none" ELSE "failed")]
     IN /\ (ch="list" \/ (ui.selected /\ ~ui.busy.list))
        /\ Effect(after,(IF valid THEN [requests EXCEPT ![e.id]=r] ELSE requests),
            (IF valid THEN "begin" ELSE "invalid"),r,ch,"none",query,e.arg)

Complete(e) ==
 /\ requests[e.id].exists
 /\ LET r == requests[e.id]
        ch == r.channel
        scopeCurrent == r.scope=ui.scope
        revisionCurrent == r.revision=ui.revision[ch]
        listSuccessScope == scopeCurrent (* ListSuccessScope *)
        listSuccessRevision == revisionCurrent (* ListSuccessRevision *)
        statsSuccessScope == scopeCurrent (* StatsSuccessScope *)
        statsSuccessRevision == revisionCurrent (* StatsSuccessRevision *)
        listErrorScope == scopeCurrent (* ListErrorScope *)
        listErrorRevision == revisionCurrent (* ListErrorRevision *)
        statsErrorScope == scopeCurrent (* StatsErrorScope *)
        statsErrorRevision == revisionCurrent (* StatsErrorRevision *)
        listFinallyScope == scopeCurrent (* ListFinallyScope *)
        listFinallyRevision == revisionCurrent (* ListFinallyRevision *)
        statsFinallyScope == scopeCurrent (* StatsFinallyScope *)
        statsFinallyRevision == revisionCurrent (* StatsFinallyRevision *)
        instanceDelivery == r.scope.lifetime=ui.scope.lifetime
        publish == instanceDelivery /\ e.outcome="success" /\
          (IF ch="list" THEN listSuccessScope /\ listSuccessRevision
           ELSE statsSuccessScope /\ statsSuccessRevision)
        errorCurrent == instanceDelivery /\ (IF ch="list" THEN listErrorScope /\ listErrorRevision
                          ELSE statsErrorScope /\ statsErrorRevision)
        finallyCurrent == instanceDelivery /\ (IF ch="list" THEN listFinallyScope /\ listFinallyRevision
                            ELSE statsFinallyScope /\ statsFinallyRevision)
        errorValue == (IF ch="list" THEN "failed" ELSE "failed") (* VisibleReadError *)
        settle == (IF ch="list" THEN FALSE ELSE FALSE) (* CurrentFinally *)
        payload == IF ch="list" THEN WireList(r.scope,r.filter) ELSE WireStats(r.scope,r.query)
        after == [ui EXCEPT !.rows[ch]=(IF publish THEN payload ELSE @),
           !.loaded=(IF publish /\ ch="statistics" THEN r.query ELSE @),
           !.error=(IF e.outcome="error" /\ errorCurrent THEN errorValue ELSE @),
           !.busy[ch]=(IF finallyCurrent THEN settle ELSE @)]
     IN Effect(after,[requests EXCEPT ![e.id].exists=FALSE],"complete",r,ch,e.outcome,ui.query,ui.filter)

Edit(e) ==
 /\ ui.selected
 /\ LET newQuery == QueryFor(e.arg)
        after == [ui EXCEPT !.query=newQuery,
          !.revision.statistics=@+1, (* QueryEditRetire *)
          !.rows.statistics={},!.loaded=None,!.busy.statistics=FALSE] (* QueryEditClear *)
    IN Effect(after,requests,"edit",EmptyRequest,"statistics","none",newQuery,ui.filter)
SelectMetric(e) ==
 /\ ui.rows.list#{}
 /\ LET after == [ui EXCEPT !.query=QueryFor(e.arg),!.selected=TRUE,
        !.revision.statistics=@+1,!.rows.statistics={},!.loaded=None,!.busy.statistics=FALSE]
     IN Effect(after,requests,"select",EmptyRequest,"statistics","none",QueryFor(e.arg),ui.filter)
ChangeScope(e) ==
 LET newScope == CASE e.arg="principal" -> [ui.scope EXCEPT !.principal=2]
      [] e.arg="region" -> [ui.scope EXCEPT !.region=2]
      [] e.arg="request" -> [ui.scope EXCEPT !.request=2]
      [] OTHER -> [ui.scope EXCEPT !.lifetime=2]
     scopeRows == [list |-> {},statistics |-> {}] (* ScopeClear *)
     after == [ui EXCEPT !.scope=newScope,!.revision=[list |-> 1,statistics |-> 1],
       !.selected=FALSE,!.rows=scopeRows,!.loaded=None,
       !.busy=[list |-> TRUE,statistics |-> FALSE],!.error="none",!.filter="all"]
     automaticList == [exists |-> TRUE,channel |-> "list",scope |-> newScope,
       revision |-> 1,query |-> ui.query,filter |-> "all"]
 IN /\ ~requests[2].exists
    /\ Effect(after,[requests EXCEPT ![2]=automaticList],"scope",EmptyRequest,"statistics","none",ui.query,"all")
ChangeLocale ==
 LET after == [ui EXCEPT !.locale=(IF @="en" THEN "ja" ELSE "en")] (* LocaleOnly *)
 IN Effect(after,requests,"locale",EmptyRequest,"statistics","none",ui.query,ui.filter)
Next == IF pc<=Len(Plan(scenario)) THEN
 LET e == Plan(scenario)[pc]
 IN CASE e.kind="begin" -> Begin(e) [] e.kind="complete" -> Complete(e)
   [] e.kind="edit" -> Edit(e) [] e.kind="select" -> SelectMetric(e)
   [] e.kind="scope" -> ChangeScope(e) [] OTHER -> ChangeLocale
 ELSE UNCHANGED vars
Spec == Init /\ [][Next]_vars

QueryType(q) == /\ q.namespace \in {"A","B"} /\ q.metric="DummyRequests"
 /\ q.dimension \in {"selected","other"} /\ q.unit \in {"Count","Bytes"}
 /\ q.window \in {"first","second"} /\ q.valid \in BOOLEAN
ScopeType(s) == /\ s.principal \in 1..2 /\ s.region \in 1..2
 /\ s.request \in 1..2 /\ s.lifetime \in 1..2
TypeOK == /\ scenario \in Scenarios /\ pc \in 1..(Len(Plan(scenario))+1)
 /\ ScopeType(ui.scope) /\ QueryType(ui.query) /\ ui.selected \in BOOLEAN
 /\ ui.filter \in {"a","b","all"} /\ (IF ui.loaded=None THEN TRUE ELSE QueryType(ui.loaded))
 /\ \A c \in Channels : ui.revision[c] \in 0..6 /\ ui.busy[c] \in BOOLEAN
 /\ ui.rows.list \subseteq {"A/selected","A/other","B/selected"}
 /\ ui.rows.statistics \subseteq {"Count:3,6,2,4,2","Bytes:12,24,11,13,2",
    "Count:777,777,777,777,1","Count:999,999,999,999,1","Count:44,88,40,48,2",
    "Count:22,44,20,24,2","Count:101,202,100,102,2"}
 /\ ui.error \in {"none","failed"} /\ ui.locale \in {"en","ja"}
 /\ \A i \in Ids : requests[i].exists \in BOOLEAN /\ requests[i].channel \in Channels
      /\ ScopeType(requests[i].scope) /\ QueryType(requests[i].query)
      /\ requests[i].revision \in 0..6 /\ requests[i].filter \in {"a","b","all","count"}
ExpectedCurrent == last.request.scope=last.before.scope
                /\ last.request.revision=last.before.revision[last.channel]
IsComplete == last.event="complete"
ListSuccessGuarded == IF IsComplete /\ last.channel="list" /\ last.outcome="success" /\ ~ExpectedCurrent
 THEN last.after.rows.list=last.before.rows.list ELSE TRUE
StatisticsSuccessGuarded == IF IsComplete /\ last.channel="statistics" /\ last.outcome="success" /\ ~ExpectedCurrent
 THEN last.after.rows.statistics=last.before.rows.statistics /\ last.after.loaded=last.before.loaded ELSE TRUE
ListCurrentResult == IF IsComplete /\ last.channel="list" /\ last.outcome="success" /\ ExpectedCurrent
 THEN last.after.rows.list=ExpectedList(last.before.filter) ELSE TRUE
StatisticsCurrentResult == IF IsComplete /\ last.channel="statistics" /\ last.outcome="success" /\ ExpectedCurrent
 THEN last.after.rows.statistics=ExpectedStats(last.before.scope,last.before.query) ELSE TRUE
StatisticsSnapshotBound == IF IsComplete /\ last.channel="statistics" /\ last.outcome="success" /\ ExpectedCurrent
 THEN last.after.loaded=last.before.query /\ last.before.selected ELSE TRUE
StaleErrorGuarded == IF IsComplete /\ last.outcome="error" /\ ~ExpectedCurrent
 THEN last.after.error=last.before.error ELSE TRUE
CurrentErrorVisible == IF IsComplete /\ last.outcome="error" /\ ExpectedCurrent
 THEN last.after.error="failed" ELSE TRUE
StaleFinallyGuarded == IF IsComplete /\ ~ExpectedCurrent
 THEN last.after.busy=last.before.busy ELSE TRUE
CurrentFinallySettles == IF IsComplete /\ ExpectedCurrent
 THEN last.after.busy[last.channel]=FALSE ELSE TRUE
QueryEditClearsStatistics == IF last.event \in {"edit","select"}
 THEN last.after.rows.statistics={} /\ last.after.loaded=None /\ ~last.after.busy.statistics ELSE TRUE
BeginSnapshotClears == IF last.event \in {"begin","invalid"} THEN last.after.loaded=None ELSE TRUE
StatisticsBeginClears == IF last.event="begin" /\ last.channel="statistics"
 THEN last.after.rows.statistics={} /\ last.after.loaded=None /\ last.after.busy.statistics /\ last.after.error="none" ELSE TRUE
BrowseClearsViews == IF last.event="begin" /\ last.channel="list"
 THEN last.after.rows.list={} /\ last.after.rows.statistics={} /\ last.after.loaded=None
      /\ ~last.after.selected /\ ~last.after.busy.statistics /\ last.after.busy.list ELSE TRUE
ScopeResetClears == IF last.event="scope" THEN
 last.after.rows.list={} /\ last.after.rows.statistics={} /\ last.after.loaded=None
 /\ ~last.after.selected /\ last.after.error="none" ELSE TRUE
LocaleFrame == IF last.event="locale" THEN
 last.after=[last.before EXCEPT !.locale=last.after.locale] ELSE TRUE
InvalidInputNoWire == IF last.event="invalid" THEN
 last.after.rows.statistics={} /\ last.after.loaded=None /\ ~last.after.busy.statistics
 /\ last.after.error="failed" /\ ~requests[Plan(scenario)[pc-1].id].exists
 ELSE IF last.event="begin" /\ last.channel="statistics" THEN last.intentQuery.valid ELSE TRUE
CaptureMatchesIntent == IF last.event="begin" THEN
 (IF last.channel="list" THEN last.request.filter=last.intentFilter
  ELSE last.request.query=last.intentQuery) ELSE TRUE
=============================================================================
