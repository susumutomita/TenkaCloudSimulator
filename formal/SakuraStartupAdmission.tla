---------------------- MODULE SakuraStartupAdmission ----------------------
EXTENDS FiniteSets

VARIABLES enabled, present, valid, unsafe, optIn, allowRemote, address, phase, decision,
          listener, database, schema, registered, token
vars == <<enabled,present,valid,unsafe,optIn,allowRemote,address,phase,decision,
          listener,database,schema,registered,token>>
Fields == {"environment","project","zone","owner"}
Phases == {"parsed","listener","database","provider","token","done","refused"}
Decisions == {"unchecked","accepted","refused"}
Addresses == {"numeric-loopback","numeric-nonloopback","non-numeric"}
Inputs == <<enabled,present,valid,unsafe,optIn,allowRemote,address>>

(* A field absent from parsed CLI flags has the empty default, never a valid
   implicit realm. Explicit empty/invalid values are present but not valid.
   Inputs are immutable. Validity is abstract, not a Go regexp/UTF8 proof. *)
Init == /\ enabled \in BOOLEAN /\ unsafe \in BOOLEAN
        /\ optIn \in BOOLEAN /\ allowRemote \in BOOLEAN /\ address \in Addresses
        /\ present \in SUBSET Fields /\ valid \in SUBSET present
        /\ phase="parsed" /\ decision="unchecked"
        /\ listener=FALSE /\ database=FALSE /\ schema=FALSE
        /\ registered=FALSE /\ token=FALSE

(* Refusal is modeled for the Sakura guards, not the general listener policy.
   The expected predicate is independent of the implementation projections. *)
ExpectedSakura == IF enabled THEN ~unsafe /\ valid=Fields ELSE present={}
ExpectedAdmission == ExpectedSakura
                     /\ (optIn => enabled /\ ~unsafe /\ ~allowRemote
                                      /\ address="numeric-loopback")
Ready == IF enabled THEN ~unsafe /\ valid=Fields
         ELSE present={}
ConsoleReady == ~optIn \/ (enabled /\ ~unsafe /\ ~allowRemote
                            /\ address="numeric-loopback")
Check == /\ phase="parsed" /\ ConsoleReady
         /\ decision'=(IF Ready THEN "accepted" ELSE "refused")
         /\ phase'=(IF Ready THEN "listener" ELSE "refused")
         /\ UNCHANGED <<listener,database,schema,registered,token>>
         /\ UNCHANGED Inputs
RejectConsole == /\ phase="parsed" /\ ~ConsoleReady
                 /\ decision'="refused" /\ phase'="refused"
                 /\ UNCHANGED <<Inputs,listener,database,schema,registered,token>>
OpenListener == /\ phase="listener" /\ decision="accepted"
                /\ listener'=TRUE /\ phase'="database"
                /\ UNCHANGED <<Inputs,decision,
                                database,schema,registered,token>>
OpenDatabase == /\ phase="database" /\ decision="accepted"
                /\ database'=TRUE /\ phase'="provider"
                /\ UNCHANGED <<Inputs,decision,
                                listener,schema,registered,token>>
ConstructProvider == /\ phase="provider" /\ decision="accepted"
                     /\ schema'=enabled /\ registered'=enabled
                     /\ phase'="token"
                     /\ UNCHANGED <<Inputs,decision,
                                     listener,database,token>>
TokenSetup == /\ phase="token" /\ decision="accepted"
              /\ token' = ~unsafe /\ phase'="done"
              /\ UNCHANGED <<Inputs,decision,
                              listener,database,schema,registered>>
Next == Check \/ RejectConsole \/ OpenListener \/ OpenDatabase \/ ConstructProvider \/ TokenSetup
        \/ UNCHANGED vars

TypeOK == /\ enabled \in BOOLEAN /\ unsafe \in BOOLEAN
          /\ optIn \in BOOLEAN /\ allowRemote \in BOOLEAN /\ address \in Addresses
          /\ present \subseteq Fields /\ valid \subseteq present
          /\ phase \in Phases /\ decision \in Decisions
          /\ listener \in BOOLEAN /\ database \in BOOLEAN
          /\ schema \in BOOLEAN /\ registered \in BOOLEAN /\ token \in BOOLEAN
NoEffectsOnRefusal == decision="refused" =>
    ~listener /\ ~database /\ ~schema /\ ~registered /\ ~token
ValidationPrecedesEffects == listener \/ database \/ schema \/ registered \/ token
                            => decision="accepted"
AcceptedConfigExact == decision="accepted" => ExpectedAdmission
ValidAdmissionAvailable == decision#"unchecked" =>
                          ((decision="accepted") = ExpectedAdmission)
DisabledNoSakura == ~enabled => ~schema /\ ~registered
EnabledOnlyAuthenticated == schema \/ registered => enabled /\ ~unsafe
SchemaRequiresDatabase == schema => database
RegistrationRequiresSchema == registered => schema
TokenOnlyAuthenticated == token => ~unsafe
StartupComplete == phase="done" =>
    listener /\ database /\ (token = ~unsafe) /\ (schema=enabled)
    /\ (registered=enabled)
=============================================================================
