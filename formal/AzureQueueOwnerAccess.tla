------------------------ MODULE AzureQueueOwnerAccess ------------------------
EXTENDS Naturals

\* A finite owner/discovery boundary, separate from AzureQueueLifecycle.
\* Tokens, required capability shape, and effective request identity are
\* abstracted as inputs. Only local operator selection advances the UI epoch.
\* Async replies may outlive a switch even though the UI aborts older requests.
\* Native admissibility is not a message state, HTTP status, or success proof.
VARIABLES mode, hostAccount, focus, pending, applied, latestReply, last
vars == <<mode, hostAccount, focus, pending, applied, latestReply, last>>

Owner == "owner"
Peer == "peer"
People == {Owner, Peer}
Handoffs == {"ssmHandoff", "ec2Handoff"}
Modes == {"operator", "ownerParticipant", "peerParticipant", "anonymous"} \cup Handoffs
Accounts == {"accounta", "accountb"}
Absent == "none"
AccountInputs == Accounts \cup {Absent}
ProjectionValues == AccountInputs \cup People
Epochs == 0..3
Focuses == [principal : People, epoch : Epochs]
Observation == [tag : Focuses, enabled : BOOLEAN, account : AccountInputs]
Methods == {"GET", "nonGET"}
Kinds == {"reference", "native"}

NoAttempt == [kind |-> Absent, method |-> "GET", hint |-> Absent,
              authorized |-> FALSE, defaultAccount |-> Absent, enumAccounts |-> {}]
FixedPrincipal == IF mode = "peerParticipant" THEN Peer ELSE Owner
Configured == hostAccount \in Accounts
Trusted == mode # "anonymous"
LocalScope == mode \notin Handoffs

DiscoverEnabled(tag) == Configured /\ Trusted /\ LocalScope /\ tag.principal = Owner
ObservationFor(tag) ==
    LET enabled == DiscoverEnabled(tag)
    IN [tag |-> tag, enabled |-> enabled, account |-> IF enabled THEN hostAccount ELSE Absent]

MatchingObservation ==
    applied.tag.epoch = focus.epoch /\ applied.tag.principal = focus.principal
ConsoleVisible == LocalScope /\ applied.enabled /\ MatchingObservation

Init ==
    /\ mode \in Modes
    /\ hostAccount \in AccountInputs
    /\ focus \in [principal : People, epoch : {0}]
    /\ (mode = "operator" \/ focus.principal = FixedPrincipal)
    /\ applied = ObservationFor(focus)
    /\ pending = {}
    /\ latestReply = 0
    /\ last = NoAttempt

Switch ==
    /\ mode = "operator" /\ focus.epoch < 3
    /\ \E next \in People \ {focus.principal} :
        /\ focus' = [principal |-> next, epoch |-> focus.epoch + 1]
        /\ pending' = pending \cup {focus'}
    /\ last' = NoAttempt
    /\ UNCHANGED <<mode, hostAccount, applied, latestReply>>

Reply ==
    \E incoming \in pending :
        /\ pending' = pending \ {incoming}
        /\ applied' = IF incoming.epoch < applied.tag.epoch
                      THEN applied ELSE ObservationFor(incoming)
        /\ latestReply' = IF incoming.epoch > latestReply THEN incoming.epoch ELSE latestReply
        /\ UNCHANGED <<mode, hostAccount, focus, last>>

ReferenceAdmission(method) ==
    method = "GET" /\ Configured /\ Trusted /\ focus.principal = Owner /\ LocalScope
\* The dynamic reference has no caller-selectable account. An abstract supplied
\* hint is irrelevant: both the default and singleton enum come from cfg.
ReferenceAccount(hint) == hostAccount
NativeAdmission(target) ==
    Configured /\ Trusted /\ focus.principal = Owner /\ LocalScope /\ target = hostAccount

Attempt(kind, method, hint) ==
    LET admitted == IF kind = "reference" THEN ReferenceAdmission(method) ELSE NativeAdmission(hint)
        projection == IF admitted /\ kind = "reference" THEN ReferenceAccount(hint) ELSE Absent
    IN /\ last' = [kind |-> kind, method |-> method, hint |-> hint, authorized |-> admitted,
                   defaultAccount |-> projection, enumAccounts |-> IF projection = Absent THEN {} ELSE {projection}]
       /\ UNCHANGED <<mode, hostAccount, focus, pending, applied, latestReply>>

Next ==
    \/ Switch \/ Reply
    \/ \E m \in Methods, a \in AccountInputs : Attempt("reference", m, a)
    \/ \E a \in Accounts : Attempt("native", "GET", a)
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ mode \in Modes /\ hostAccount \in AccountInputs /\ focus \in Focuses
    /\ pending \subseteq Focuses /\ applied \in Observation /\ latestReply \in Epochs
    /\ last \in [kind : Kinds \cup {Absent}, method : Methods, hint : AccountInputs,
                 authorized : BOOLEAN, defaultAccount : ProjectionValues,
                 enumAccounts : SUBSET ProjectionValues]

FixedParticipant == mode # "operator" => focus.epoch = 0 /\ focus.principal = FixedPrincipal
OnlyCurrentDiscovery ==
    ConsoleVisible => applied.tag.epoch = focus.epoch /\ applied.tag.principal = focus.principal
NewestObservationRetained == applied.tag.epoch = latestReply
DiscoveryAuthority ==
    applied.enabled => mode \in {"operator", "ownerParticipant"}
                       /\ applied.tag.principal = Owner
                       /\ hostAccount \in Accounts /\ applied.account = hostAccount
PresentationOwnerOnly ==
    ConsoleVisible => mode \in {"operator", "ownerParticipant"} /\ focus.principal = Owner
                      /\ hostAccount \in Accounts /\ applied.account = hostAccount
ReferenceGetAuthorized ==
    last.kind = "reference" =>
        (last.authorized <=> last.method = "GET" /\ mode \in {"operator", "ownerParticipant"}
                             /\ focus.principal = Owner /\ hostAccount \in Accounts)
ReferenceAccountIsConfigured ==
    IF last.kind = "reference" /\ last.authorized
    THEN last.defaultAccount = hostAccount /\ last.enumAccounts = {hostAccount}
    ELSE last.defaultAccount = Absent /\ last.enumAccounts = {}
NativePermissionBoundary ==
    last.kind = "native" =>
        (last.authorized <=> mode \in {"operator", "ownerParticipant"} /\ focus.principal = Owner
                             /\ hostAccount \in Accounts /\ last.hint = hostAccount)

\* Optional reachability controls: a named violation demonstrates that an
\* ordinary successful behavior is not blocked by a vacuous safety spec.
NotNativeWithoutCurrentDiscovery ==
    ~(focus.epoch = 2 /\ focus.principal = Owner /\ applied.tag.epoch = 0 /\ applied.tag.principal = Owner
      /\ ~ConsoleVisible /\ last.kind = "native" /\ last.authorized)
NotFreshOwnerAfterSwitch ==
    ~(focus.epoch = 2 /\ focus.principal = Owner /\ ConsoleVisible)
NotReferenceWithUnrelatedHint ==
    ~(last.kind = "reference" /\ last.authorized /\ last.hint \in Accounts /\ last.hint # hostAccount
      /\ last.defaultAccount = hostAccount /\ last.enumAccounts = {hostAccount})
=============================================================================
