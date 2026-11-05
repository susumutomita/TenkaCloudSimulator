---------------------- MODULE SakuraStartupAdmission ----------------------
EXTENDS FiniteSets

VARIABLES enabled, present, valid, unsafe, phase, decision,
          listener, database, schema, registered, token
vars == <<enabled,present,valid,unsafe,phase,decision,
          listener,database,schema,registered,token>>
Fields == {"environment","project","zone","owner"}
Phases == {"parsed","listener","database","provider","token","done","refused"}
Decisions == {"unchecked","accepted","refused"}

(* A field absent from parsed CLI flags has the empty default, never a valid
   implicit realm. Explicit empty/invalid values are present but not valid.
   Inputs are immutable. Validity is abstract, not a Go regexp/UTF8 proof. *)
Init == /\ enabled \in BOOLEAN /\ unsafe \in BOOLEAN
        /\ present \in SUBSET Fields /\ valid \in SUBSET present
        /\ phase="parsed" /\ decision="unchecked"
        /\ listener=FALSE /\ database=FALSE /\ schema=FALSE
        /\ registered=FALSE /\ token=FALSE

(* ExpectedAdmission is the independent safety oracle. Ready is the concrete
   validation branch projection and the sole guard mutated by admission tests. *)
ExpectedAdmission == IF enabled THEN ~unsafe /\ valid=Fields
                     ELSE present={}
Ready == IF enabled THEN ~unsafe /\ valid=Fields
         ELSE present={}
Check == /\ phase="parsed"
         /\ decision'=(IF Ready THEN "accepted" ELSE "refused")
         /\ phase'=(IF Ready THEN "listener" ELSE "refused")
         /\ UNCHANGED <<listener,database,schema,registered,token>>
         /\ UNCHANGED <<enabled,present,valid,unsafe>>
OpenListener == /\ phase="listener" /\ decision="accepted"
                /\ listener'=TRUE /\ phase'="database"
                /\ UNCHANGED <<enabled,present,valid,unsafe,decision,
                                database,schema,registered,token>>
OpenDatabase == /\ phase="database" /\ decision="accepted"
                /\ database'=TRUE /\ phase'="provider"
                /\ UNCHANGED <<enabled,present,valid,unsafe,decision,
                                listener,schema,registered,token>>
ConstructProvider == /\ phase="provider" /\ decision="accepted"
                     /\ schema'=enabled /\ registered'=enabled
                     /\ phase'="token"
                     /\ UNCHANGED <<enabled,present,valid,unsafe,decision,
                                     listener,database,token>>
TokenSetup == /\ phase="token" /\ decision="accepted"
              /\ token' = ~unsafe /\ phase'="done"
              /\ UNCHANGED <<enabled,present,valid,unsafe,decision,
                              listener,database,schema,registered>>
Next == Check \/ OpenListener \/ OpenDatabase \/ ConstructProvider \/ TokenSetup
        \/ UNCHANGED vars

TypeOK == /\ enabled \in BOOLEAN /\ unsafe \in BOOLEAN
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
