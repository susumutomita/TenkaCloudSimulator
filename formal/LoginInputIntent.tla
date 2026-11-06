-------------------------- MODULE LoginInputIntent --------------------------
EXTENDS Integers, Sequences, FiniteSets
\* Independent finite safety correspondence for committed event outcomes.
\* These opaque labels are not credentials and no authentication is modeled.
Values == {"empty", "A", "B"}
Scenarios == {"dom-insert", "dom-clear", "paste-priority", "disconnected",
 "blur-locale", "file-native", "file-edit", "file-paste", "file-current",
 "file-error", "file-stale-error", "paste-edit", "busy-request", "busy-file",
 "unmount-file", "file-locale-error", "native-focus-submit", "focused-key-clear-submit"}
Event(op,v) == [op |-> op, v |-> v]
Plan(k) == CASE
 k = "dom-insert" -> <<Event("native","A"),Event("submit","empty")>>
 [] k = "dom-clear" -> <<Event("edit","A"),Event("native","empty"),Event("submit","empty")>>
 [] k = "paste-priority" -> <<Event("paste","A"),Event("native","B"),Event("submit","empty")>>
 [] k = "disconnected" -> <<Event("edit","A"),Event("disconnect","empty"),Event("native","B"),Event("submit","empty")>>
 [] k = "blur-locale" -> <<Event("native","A"),Event("sync","empty"),Event("locale","empty"),Event("locale","empty")>>
 [] k = "file-native" -> <<Event("filebegin","A"),Event("native","B"),Event("sync","empty"),Event("fileok","empty")>>
 [] k = "file-edit" -> <<Event("filebegin","A"),Event("edit","B"),Event("fileok","empty")>>
 [] k = "file-paste" -> <<Event("filebegin","A"),Event("paste","B"),Event("flush","empty"),Event("fileok","empty")>>
 [] k = "file-current" -> <<Event("filebegin","A"),Event("fileok","empty"),Event("submit","empty")>>
 [] k = "file-error" -> <<Event("filebegin","A"),Event("fileerr","empty"),Event("submit","empty")>>
 [] k = "file-stale-error" -> <<Event("filebegin","A"),Event("edit","B"),Event("fileerr","empty")>>
 [] k = "paste-edit" -> <<Event("paste","A"),Event("edit","B"),Event("flush","empty"),Event("submit","empty")>>
 [] k = "busy-request" -> <<Event("edit","A"),Event("submit","empty"),Event("edit","B"),Event("submit","empty"),Event("finish","empty"),Event("submit","empty")>>
 [] k = "busy-file" -> <<Event("filebegin","A"),Event("native","B"),Event("submit","empty"),Event("fileok","empty")>>
 [] k = "unmount-file" -> <<Event("filebegin","A"),Event("unmount","empty"),Event("fileok","empty")>>
 [] k = "native-focus-submit" -> <<Event("native","A"),Event("focus","empty"),Event("submit","empty")>>
 [] k = "focused-key-clear-submit" -> <<Event("paste","A"),Event("flush","empty"),Event("native","empty"),Event("keydown","empty"),Event("submit","empty")>>
 [] k = "file-locale-error" -> <<Event("filebegin","A"),Event("locale","empty"),Event("fileerr","empty"),Event("locale","empty")>>

InitialState == [token |-> "empty", dom |-> "empty", connected |-> TRUE,
 alive |-> TRUE, rev |-> 0, pasteRev |-> 0, pasteVal |-> "empty",
 pasteConnected |-> TRUE, jobRev |-> 0, fileCurrent |-> 0, fileVal |-> "empty",
 fileState |-> "idle", error |-> FALSE, busy |-> FALSE, posts |-> <<>>, locale |-> "en"]
VARIABLES scenario, pc, state, observation
vars == <<scenario,pc,state,observation>>
Init == /\ scenario \in Scenarios /\ pc = 1 /\ state = InitialState
        /\ observation = [op |-> "init", before |-> InitialState]

\* Mutation targets operate on state used by the actual modeled transitions.
Invalidate(s) == [s EXCEPT !.rev = @ + 1,
 !.pasteRev = 0, \* NewIntentPasteCancel
 !.fileCurrent = 0, !.fileState = "idle"]
Adopt(s,v) == [Invalidate(s) EXCEPT !.token = v, !.dom = v, !.error = FALSE]
ActivePaste(s) == s.pasteRev # 0 /\ s.pasteRev = s.rev /\ s.pasteConnected
Selected(s) == IF ActivePaste(s) THEN s.pasteVal
               ELSE IF s.connected THEN s.dom ELSE s.token
Sync(s) == IF s.connected /\ s.dom # s.token THEN Adopt(s,s.dom) ELSE s
\* Explicit environment assumption, not the established cause of the CI timeout:
\* focus may trigger a controlled render that resets DOM to the adopted token.
\* Capture must observe/adopt the connected native value before that reset.
Focus(s) == LET adopted == Sync(s) IN [adopted EXCEPT !.dom = adopted.token]
\* Assumed controlled reset after a focused keydown capture, not established
\* browser/extension causality. Adopt native clear BEFORE this reset.
KeyDown(s) == LET adopted == Sync(s) IN [adopted EXCEPT !.dom = adopted.token]
Flush(s) == IF ActivePaste(s) THEN Adopt(s,Selected(s))
           ELSE IF s.connected /\ s.dom # s.token THEN Adopt(s,Selected(s))
           ELSE [s EXCEPT !.pasteRev = 0]
Capture(s,v) == [Invalidate(s) EXCEPT !.pasteRev = s.rev + 1,
 !.pasteVal = v, !.pasteConnected = s.connected]
BeginFile(s,v) == [Invalidate(s) EXCEPT !.token = "empty", !.dom = "empty",
 !.jobRev = s.rev + 1, !.fileCurrent = s.rev + 1, !.fileVal = v,
 !.fileState = "loading", !.error = FALSE]
FileEligible(s) == s.alive /\ s.jobRev # 0 /\ s.jobRev = s.rev /\ s.fileCurrent = s.jobRev
CompleteFile(s,success) ==
 IF FileEligible(s)
 THEN IF success
      THEN [Adopt(s,s.fileVal) EXCEPT !.jobRev = 0, !.fileState = "loaded"]
      ELSE [s EXCEPT !.jobRev = 0, !.fileCurrent = 0, !.fileState = "idle", !.error = TRUE]
 ELSE [s EXCEPT !.jobRev = 0]
Ready(s) == ~s.busy /\ s.fileCurrent = 0
Submit(s) == IF Ready(s)
 THEN LET f == Flush(s) value == Selected(s)
      IN IF value = "empty" THEN [f EXCEPT !.error = TRUE]
         ELSE [f EXCEPT !.busy = TRUE, !.posts = Append(@,value), !.error = FALSE]
 ELSE s
\* Unfocused native writes can be lost on a locale rerender. DOM is deliberately
\* reset from controlled state here; the locale property does NOT promise otherwise.
Locale(s) == [s EXCEPT !.locale = IF @ = "en" THEN "ja" ELSE "en", !.dom = s.token]
Unmount(s) == [s EXCEPT !.rev = @ + 1, !.pasteRev = 0, !.fileCurrent = 0,
 !.alive = FALSE, !.connected = FALSE]
NextState(s,e) == CASE e.op = "edit" -> Adopt(s,e.v)
 [] e.op = "native" -> [s EXCEPT !.dom = e.v]
 [] e.op = "paste" -> Capture(s,e.v)
 [] e.op = "sync" -> Sync(s)
 [] e.op = "focus" -> Focus(s)
 [] e.op = "keydown" -> KeyDown(s)
 [] e.op = "flush" -> Flush(s)
 [] e.op = "filebegin" -> BeginFile(s,e.v)
 [] e.op = "fileok" -> CompleteFile(s,TRUE)
 [] e.op = "fileerr" -> CompleteFile(s,FALSE)
 [] e.op = "submit" -> Submit(s)
 [] e.op = "finish" -> [s EXCEPT !.busy = FALSE]
 [] e.op = "locale" -> Locale(s)
 [] e.op = "disconnect" -> [s EXCEPT !.connected = FALSE]
 [] e.op = "unmount" -> Unmount(s)
Step == /\ pc <= Len(Plan(scenario))
        /\ LET e == Plan(scenario)[pc]
           IN /\ state' = NextState(state,e)
              /\ observation' = [op |-> e.op, before |-> state]
        /\ pc' = pc + 1 /\ UNCHANGED scenario
\* No arbitrary stutter hides unfinished scenarios; only completed plans stutter.
Next == Step \/ (pc > Len(Plan(scenario)) /\ UNCHANGED vars)

\* Independent before-state oracle. It never calls the mutated selection,
\* invalidation, eligibility, readiness or publication functions above.
ExpectedSelected(s) == IF s.pasteRev # 0 /\ s.pasteRev = s.rev /\ s.pasteConnected
 THEN s.pasteVal ELSE IF s.connected THEN s.dom ELSE s.token
ExpectedReady(s) == ~s.busy /\ s.fileCurrent = 0
ExpectedFile(s) == s.alive /\ s.jobRev # 0 /\ s.jobRev = s.rev /\ s.fileCurrent = s.jobRev
Before == observation.before
Op == observation.op
Flushing == Op = "flush" \/ (Op = "submit" /\ ExpectedReady(Before))
NeedsAdoption == (Op \in {"sync","focus","keydown"} /\ Before.connected /\ Before.dom # Before.token)
 \/ (Flushing /\ ((Before.pasteRev # 0 /\ Before.pasteRev = Before.rev /\ Before.pasteConnected)
                  \/ (Before.connected /\ Before.dom # Before.token)))
ExpectedToken == IF Op = "edit" THEN Plan(scenario)[pc-1].v
 ELSE IF Op \in {"sync","focus","keydown"} /\ Before.connected THEN Before.dom
 ELSE IF Flushing THEN ExpectedSelected(Before)
 ELSE IF Op = "fileok" /\ ExpectedFile(Before) THEN Before.fileVal
 ELSE Before.token
StateShape(s) == /\ s.token \in Values /\ s.dom \in Values
 /\ s.connected \in BOOLEAN /\ s.alive \in BOOLEAN /\ s.rev \in 0..3
 /\ s.pasteRev \in 0..3 /\ s.pasteVal \in Values /\ s.pasteConnected \in BOOLEAN
 /\ s.jobRev \in 0..3 /\ s.fileCurrent \in 0..3 /\ s.fileVal \in Values
 /\ s.fileState \in {"idle","loading","loaded"} /\ s.error \in BOOLEAN
 /\ s.busy \in BOOLEAN /\ s.posts \in Seq(Values) /\ Len(s.posts) <= 2
 /\ s.locale \in {"en","ja"}
TypeOK == /\ scenario \in Scenarios /\ pc \in 1..7
          /\ StateShape(state) /\ StateShape(Before)
SubmissionSelectionExact ==
 (Op = "submit" /\ ExpectedReady(Before)) =>
 IF ExpectedSelected(Before) = "empty" THEN state.posts = Before.posts
 ELSE state.posts = Append(Before.posts,ExpectedSelected(Before))
AdoptedValueExact ==
 /\ ((Op \in {"edit","sync","focus","keydown","flush","submit","fileok","fileerr"}) =>
      state.token = ExpectedToken)
 /\ ((Op = "fileok" /\ ExpectedFile(Before)) =>
      /\ state.fileState = "loaded" /\ state.jobRev = 0)
 /\ ((Op = "fileerr" /\ ExpectedFile(Before)) =>
      /\ state.error /\ state.fileCurrent = 0 /\ state.fileState = "idle" /\ state.jobRev = 0)
NoEmptyPOST == /\ (\A i \in 1..Len(state.posts): state.posts[i] # "empty")
 /\ ((Op = "submit" /\ ExpectedReady(Before) /\ ExpectedSelected(Before) = "empty")
      => state.posts = Before.posts)
NoPOSTWhileBlocked == (Op = "submit" /\ ~ExpectedReady(Before)) => state.posts = Before.posts
IntentInvalidatesAsync ==
 /\ ((Op = "edit" \/ NeedsAdoption) =>
      /\ state.rev = Before.rev + 1 /\ state.pasteRev = 0 /\ state.fileCurrent = 0)
 /\ (Op = "paste" =>
      /\ state.rev = Before.rev + 1 /\ state.pasteRev = state.rev /\ state.fileCurrent = 0)
 /\ (Op = "filebegin" =>
      /\ state.rev = Before.rev + 1 /\ state.pasteRev = 0 /\ state.fileCurrent = state.rev)
Visible(s) == <<s.token,s.dom,s.rev,s.pasteRev,s.pasteVal,s.pasteConnected,
 s.fileCurrent,s.fileVal,s.fileState,s.error,s.busy,s.posts,s.locale,s.alive,s.connected>>
StaleFileCompletionFrame ==
 (Op \in {"fileok","fileerr"} /\ ~ExpectedFile(Before)) => Visible(state) = Visible(Before)
LocaleIntentFrame == Op = "locale" =>
 <<state.token,state.rev,state.pasteRev,state.fileCurrent,state.fileState,state.error,state.busy,state.posts>> =
 <<Before.token,Before.rev,Before.pasteRev,Before.fileCurrent,Before.fileState,Before.error,Before.busy,Before.posts>>
=============================================================================
