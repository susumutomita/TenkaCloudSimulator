---------------------- MODULE SecretsDisclosureChannels ----------------------
EXTENDS Naturals, FiniteSets
CONSTANT GetValue, MetadataRead, ErrorResponse, PrivateLog,
         GetSink, MetadataSink, ErrorSink, LogSink
VARIABLES context, raw, emissions
vars == <<context,raw,emissions>>
Operations == {GetValue,MetadataRead,ErrorResponse,PrivateLog}
PrivateSinks == {MetadataSink,ErrorSink,LogSink}
Sinks == PrivateSinks \cup {GetSink}
Emission(sink,valueIsRaw) == [sink |-> sink,operation |-> context.operation,
                            allowed |-> context.allowed,raw |-> valueIsRaw]
Init == /\ context=[operation |-> MetadataRead,allowed |-> FALSE]
        /\ raw=FALSE /\ emissions={}
Start(operation,allowed) ==
    /\ context'=[operation |-> operation,allowed |-> allowed]
    /\ raw'=FALSE
    /\ UNCHANGED emissions
\* Only the authorized Get writer explicitly acquires plaintext. Metadata DTO,
\* formatter/JSON/slog and sanitized errors use the private/redacted channel.
AcquireRaw == /\ context.operation=GetValue /\ context.allowed
              /\ raw'=TRUE /\ UNCHANGED <<context,emissions>>
PrivateOutput(sink) ==
    /\ sink \in PrivateSinks /\ Cardinality(emissions)<2
    /\ emissions'=emissions \cup {Emission(sink,FALSE)}
    /\ UNCHANGED <<context,raw>>
GetOutput ==
    /\ context.operation=GetValue /\ context.allowed /\ raw
    /\ Cardinality(emissions)<2
    /\ emissions'=emissions \cup {Emission(GetSink,TRUE)}
    /\ UNCHANGED <<context,raw>>
Next == (\E operation \in Operations,allowed \in BOOLEAN : Start(operation,allowed))
        \/ AcquireRaw \/ GetOutput
        \/ (\E sink \in PrivateSinks : PrivateOutput(sink))
        \/ UNCHANGED vars
TypeOK == /\ context \in [operation:Operations,allowed:BOOLEAN]
          /\ raw \in BOOLEAN
          /\ emissions \subseteq [sink:Sinks,operation:Operations,allowed:BOOLEAN,raw:BOOLEAN]
          /\ Cardinality(emissions)<=2
RawRequiresAuthorizedGet == raw => context.operation=GetValue /\ context.allowed
MetadataLogErrorRedacted == \A e \in emissions : e.sink \in PrivateSinks => ~e.raw
DeniedNoRaw == \A e \in emissions : ~e.allowed => ~e.raw
RawEmissionRequiresGet == \A e \in emissions :
    e.raw => e.operation=GetValue /\ e.allowed /\ e.sink=GetSink
=============================================================================
