--------------------------- MODULE EC2NativeSubnetTagOnCreate -----------------
EXTENDS Naturals, FiniteSets

CONSTANT TagFixtures
VARIABLES phase, intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
  committed, stored, attempts, sameView, observedReceipt, observedDescribe

vars == <<phase, intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
  committed, stored, attempts, sameView, observedReceipt, observedDescribe>>
DraftReady == phase = "draft"
NativeTarget == native
TaggedOperationAdvertised == intent = {} \/ advertised
TagPolicy == intent = {} \/ (wildcardTag /\ exactTag)
DurableTags(tags) == tags
ReceiptMatches(tags) == tags = intent
DescribeMatches(tags) == tags = intent

Init ==
  /\ phase = "draft" /\ intent \in SUBSET TagFixtures
  /\ admissible \in BOOLEAN /\ native \in BOOLEAN /\ advertised \in BOOLEAN
  /\ createScope \in BOOLEAN /\ wildcardTag \in BOOLEAN /\ exactTag \in BOOLEAN
  /\ committed = FALSE /\ stored = {} /\ attempts = 0 /\ sameView = TRUE
  /\ observedReceipt = {} /\ observedDescribe = {}

Send ==
  /\ DraftReady /\ admissible /\ sameView /\ TaggedOperationAdvertised
  /\ attempts < 2
  /\ phase' = "pending" /\ attempts' = attempts + 1
  /\ UNCHANGED <<intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
       committed, stored, sameView, observedReceipt, observedDescribe>>

Decide ==
  /\ phase = "pending"
  /\ LET permit == NativeTarget /\ createScope /\ TagPolicy
     IN /\ phase' = IF permit THEN "committed" ELSE "denied"
        /\ committed' = permit
        /\ stored' = IF permit THEN DurableTags(intent) ELSE {}
  /\ UNCHANGED <<intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
       attempts, sameView, observedReceipt, observedDescribe>>

Receipt(tags) ==
  /\ phase = "committed"
  /\ observedReceipt' = tags
  /\ phase' = IF sameView /\ ReceiptMatches(tags) THEN "readback" ELSE "unconfirmed"
  /\ UNCHANGED <<intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
       committed, stored, attempts, sameView, observedDescribe>>

ReadBack(tags) ==
  /\ phase = "readback"
  /\ observedDescribe' = tags
  /\ phase' = IF sameView /\ DescribeMatches(tags) THEN "shown" ELSE "unconfirmed"
  /\ UNCHANGED <<intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
       committed, stored, attempts, sameView, observedReceipt>>

Retire ==
  /\ sameView
  /\ phase \in {"pending", "committed", "readback"}
  /\ sameView' = FALSE
  /\ UNCHANGED <<phase, intent, admissible, native, advertised, createScope, wildcardTag, exactTag,
       committed, stored, attempts, observedReceipt, observedDescribe>>

Next ==
  \/ Send
  \/ Decide
  \/ \E tags \in SUBSET TagFixtures : Receipt(tags) \/ ReadBack(tags)
  \/ Retire

TypeOK ==
  /\ phase \in {"draft","pending","denied","committed","readback","shown","unconfirmed"}
  /\ intent \in SUBSET TagFixtures /\ admissible \in BOOLEAN /\ native \in BOOLEAN
  /\ advertised \in BOOLEAN /\ createScope \in BOOLEAN /\ wildcardTag \in BOOLEAN /\ exactTag \in BOOLEAN
  /\ committed \in BOOLEAN /\ stored \in SUBSET TagFixtures /\ attempts \in 0..2 /\ sameView \in BOOLEAN
  /\ observedReceipt \in SUBSET TagFixtures /\ observedDescribe \in SUBSET TagFixtures
TagPermissionBeforeCommit == (committed /\ intent # {}) => (wildcardTag /\ exactTag)
NativeAndAuthorizedParent == committed => (native /\ createScope)
AtomicTagSet == IF committed THEN stored = intent ELSE stored = {}
ExactCurrentReadback == phase = "shown" => (committed /\ sameView /\ observedReceipt = intent /\ observedDescribe = intent)
OneCreateAttempt == attempts <= 1
AdvertisedCapabilityOnly == (attempts # 0 /\ intent # {}) => advertised
==============================================================================
