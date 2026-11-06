------------------------- MODULE CapabilityProjection -------------------------
EXTENDS Naturals, FiniteSets, Sequences

(* Original finite descriptive capability contract. Backend/qualification verdicts
   are trusted inputs, not a proof of source integration, HTTP JSON, SDK parity or
   data authorization. The host installs its handler only after startup succeeds.
   The source-wired Metrics candidate has backendReady=TRUE and console
   qualification TRUE; SDK/reference remain FALSE. These inputs are explicit
   declarations, not a proof that build/browser/runtime acceptance occurred. *)
Operations == {"PutMetricData", "ListMetrics", "GetMetricStatistics"}
PeerSeeds == {<<"Logs metrics disabled", "KMS12">>,
              <<"Logs metrics disabled", "Secrets8">>}
QualificationInputs == [console : BOOLEAN, reference : BOOLEAN, sdk : BOOLEAN]
EmptyDescriptor == [present |-> FALSE, available |-> FALSE, operations |-> {},
                    protocol |-> "", version |-> "", provider |-> "",
                    selector |-> "", console |-> FALSE, reference |-> FALSE,
                    sdk |-> FALSE]

VARIABLES backendReady, qualified, initialPeer, peer, done, descriptor,
          consumerCase, consumerDone, visible, searchResult
vars == <<backendReady, qualified, initialPeer, peer, done, descriptor,
          consumerCase, consumerDone, visible, searchResult>>

(* Closed primitive tags stand for strict JavaScript field/type comparisons.
   No mixed absent/record/string comparisons or JSON/React execution is modeled.
   The 15 original mount negative cases are all retained. The additional fourth
   duplicate slot distinguishes the actual length guard from distinct/membership. *)
ConsumerCases == {"produced", "valid", "valid-reordered", "root-null", "root-empty",
 "missing-available", "missing-console", "missing-apiVersion", "missing-protocol",
 "missing-serviceSelector", "missing-operations", "console-false", "available-false",
 "truthy-console", "wrong-version", "JSON", "wire-alias", "duplicate-op",
 "missing-op", "extra-op", "truthy-available", "nonarray-ops", "duplicate-extra-slot"}
LiteralBadCases == {"root-null", "root-empty", "missing-available", "missing-console",
 "missing-apiVersion", "missing-protocol", "missing-serviceSelector", "missing-operations",
 "console-false", "available-false", "truthy-console", "wrong-version", "JSON",
 "wire-alias", "duplicate-op", "missing-op", "extra-op", "truthy-available",
 "nonarray-ops", "duplicate-extra-slot"}
WireOperations == <<"PutMetricData", "ListMetrics", "GetMetricStatistics">>
OpsToSeq(ops) ==
 (IF "PutMetricData" \in ops THEN <<"PutMetricData">> ELSE <<>>) \o
 (IF "ListMetrics" \in ops THEN <<"ListMetrics">> ELSE <<>>) \o
 (IF "GetMetricStatistics" \in ops THEN <<"GetMetricStatistics">> ELSE <<>>) \o
 (IF "GetMetricData" \in ops THEN <<"GetMetricData">> ELSE <<>>)
ConsumerInput(c) ==
 LET absent == c \in {"root-null", "root-empty"}
 IN [available |-> IF c = "produced" THEN
                     IF descriptor.available THEN "bool-true" ELSE "bool-false"
                   ELSE IF absent \/ c = "missing-available" THEN "absent"
                   ELSE IF c = "available-false" THEN "bool-false"
                   ELSE IF c = "truthy-available" THEN "string-true" ELSE "bool-true",
     console |-> IF c = "produced" THEN
                   IF descriptor.console THEN "bool-true" ELSE "bool-false"
                 ELSE IF absent \/ c = "missing-console" THEN "absent"
                 ELSE IF c = "console-false" THEN "bool-false"
                 ELSE IF c = "truthy-console" THEN "string-true" ELSE "bool-true",
     version |-> IF c = "produced" THEN descriptor.version
                 ELSE IF absent \/ c = "missing-apiVersion" THEN "absent"
                 ELSE IF c = "wrong-version" THEN "2010-05-15" ELSE "2010-08-01",
     protocol |-> IF c = "produced" THEN descriptor.protocol
                  ELSE IF absent \/ c = "missing-protocol" THEN "absent"
                  ELSE IF c = "JSON" THEN "JSON" ELSE "Query",
     selector |-> IF c = "produced" THEN descriptor.selector
                  ELSE IF absent \/ c = "missing-serviceSelector" THEN "absent"
                  ELSE IF c = "wire-alias" THEN "cloudwatch" ELSE "monitoring",
     operationsKind |-> IF absent \/ c = "missing-operations" THEN "absent"
                        ELSE IF c = "nonarray-ops" THEN "object" ELSE "array",
     operations |-> IF c = "produced" THEN OpsToSeq(descriptor.operations)
                    ELSE IF absent \/ c = "missing-operations" THEN <<>>
                    ELSE IF c = "valid-reordered" THEN
                      <<"GetMetricStatistics", "PutMetricData", "ListMetrics">>
                    ELSE IF c = "duplicate-op" THEN <<"PutMetricData", "ListMetrics", "ListMetrics">>
                    ELSE IF c = "missing-op" THEN <<"ListMetrics", "GetMetricStatistics">>
                    ELSE IF c = "extra-op" THEN WireOperations \o <<"GetMetricData">>
                    ELSE IF c = "duplicate-extra-slot" THEN WireOperations \o <<"PutMetricData">>
                    ELSE WireOperations]
ConsumerGate(x) ==
 /\ x.available = "bool-true"
 /\ x.console = "bool-true"
 /\ x.version = "2010-08-01"
 /\ x.protocol = "Query"
 /\ x.selector = "monitoring"
 /\ x.operationsKind = "array"
 /\ Len(x.operations) = 3
 /\ Cardinality({x.operations[i] : i \in 1..Len(x.operations)}) = 3
 /\ \A operation \in {"PutMetricData", "ListMetrics", "GetMetricStatistics"} :
      \E i \in 1..Len(x.operations) : x.operations[i] = operation

(* Independent literal case oracle: it does not call ConsumerInput, ConsumerGate,
   OpsToSeq, operation length/set/membership, or producer descriptor fields. *)
ExpectedVisible(c) ==
 c \in {"valid", "valid-reordered"} \/
 (c = "produced" /\ backendReady /\ qualified.console)


Init == /\ backendReady \in BOOLEAN
        /\ qualified \in QualificationInputs
        /\ initialPeer \in PeerSeeds
        /\ peer = initialPeer
        /\ done = FALSE
        /\ descriptor = EmptyDescriptor
        /\ consumerCase \in ConsumerCases
        /\ consumerDone = FALSE /\ visible = FALSE /\ searchResult = <<>>

Project ==
 /\ ~done
 /\ done' = TRUE
 /\ descriptor' =
      [present |-> backendReady,
       available |-> backendReady,
       operations |-> IF backendReady THEN Operations ELSE {},
       protocol |-> IF backendReady THEN "Query" ELSE "",
       version |-> IF backendReady THEN "2010-08-01" ELSE "",
       provider |-> IF backendReady THEN "aws" ELSE "",
       selector |-> IF backendReady THEN "monitoring" ELSE "",
       console |-> backendReady /\ qualified.console,
       reference |-> backendReady /\ qualified.reference,
       sdk |-> backendReady /\ qualified.sdk]
 /\ peer' = peer
 /\ UNCHANGED <<backendReady, qualified, initialPeer>>
 /\ UNCHANGED <<consumerCase, consumerDone, visible, searchResult>>

Consume ==
 /\ done /\ ~consumerDone
 /\ consumerDone' = TRUE
 /\ visible' = ConsumerGate(ConsumerInput(consumerCase))
 /\ searchResult' = (IF ConsumerGate(ConsumerInput(consumerCase))
                       THEN <<"cloudwatch-metrics">> ELSE <<>>)
 /\ UNCHANGED <<backendReady, qualified, initialPeer, peer, done, descriptor, consumerCase>>

Next == Project \/ Consume
TypeOK ==
 /\ backendReady \in BOOLEAN /\ qualified \in QualificationInputs
 /\ initialPeer \in PeerSeeds
 /\ peer \in (PeerSeeds \cup {<<"Logs metrics enabled", "KMS12">>})
 /\ done \in BOOLEAN
 /\ descriptor.present \in BOOLEAN /\ descriptor.available \in BOOLEAN
 /\ descriptor.operations \subseteq (Operations \cup {"GetMetricData"})
 /\ descriptor.protocol \in {"", "Query", "JSON"}
 /\ descriptor.version \in {"", "2010-08-01", "future"}
 /\ descriptor.provider \in {"", "aws", "gcp"}
 /\ descriptor.selector \in {"", "monitoring", "logs"}
 /\ descriptor.console \in BOOLEAN /\ descriptor.reference \in BOOLEAN
 /\ descriptor.sdk \in BOOLEAN
HostReadyBeforePublication == (done /\ ~backendReady) => ~descriptor.present
AvailableOnlyWhenReady == descriptor.available => backendReady
ExactSupportedOperations == (done /\ descriptor.present) =>
 descriptor.operations = {"PutMetricData", "ListMetrics", "GetMetricStatistics"}
ExactProtocol == (done /\ descriptor.present) => descriptor.protocol = "Query"
ExactVersion == (done /\ descriptor.present) => descriptor.version = "2010-08-01"
ExactProvider == (done /\ descriptor.present) => descriptor.provider = "aws"
ExactSelector == (done /\ descriptor.present) => descriptor.selector = "monitoring"
ConsoleRequiresQualification == descriptor.console => (backendReady /\ qualified.console)
ReferenceRequiresQualification == descriptor.reference => (backendReady /\ qualified.reference)
SDKRequiresQualification == descriptor.sdk => (backendReady /\ qualified.sdk)
PeerPreserved == peer = initialPeer

ConsumerTypeOK == /\ consumerCase \in ConsumerCases /\ consumerDone \in BOOLEAN
                  /\ visible \in BOOLEAN
                  /\ searchResult \in {<<>>, <<"cloudwatch-metrics">>}
CatalogVisibilityExact == consumerDone => visible = ExpectedVisible(consumerCase)
MalformedDescriptorHidden ==
 (consumerDone /\ consumerCase \in LiteralBadCases) => ~visible
QualifiedProducedVisibility ==
 (consumerDone /\ consumerCase = "produced") =>
 visible = (backendReady /\ qualified.console)
KnownValidDescriptorVisible ==
 (consumerDone /\ consumerCase \in {"valid", "valid-reordered"}) => visible
SearchExact == consumerDone =>
 searchResult = IF ExpectedVisible(consumerCase) THEN <<"cloudwatch-metrics">> ELSE <<>>

Spec == Init /\ [][Next]_vars
=============================================================================
