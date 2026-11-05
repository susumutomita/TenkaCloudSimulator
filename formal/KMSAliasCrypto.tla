-------------------------- MODULE KMSAliasCrypto --------------------------
EXTENDS Naturals
CONSTANTS KeyA, KeyB, Alias, None
Keys == {KeyA,KeyB}
Selectors == {Alias,KeyA,KeyB,None}
Ops == {"Encrypt","Decrypt"}
AuthModes == {"policy","missing","error"}
Outcomes == {"ok","cryptoFail","writeFail","commitFail"}
Phases == {"choose","prepared","resolve","authorize","check","crypto","commit","done"}
Active == {"resolve","authorize","check","crypto","commit"}
Statuses == {"pending","invalid","denied","authorizationError","incorrectKey","nativeError","writeError","commitError","success"}
Other(k) == IF k = KeyA THEN KeyB ELSE KeyA
VARIABLES phase, aliasTarget, counts, enabled, op, selector, blobKey, permit,
          aliasPermit, authMode, outcome, retargeted, snapshotAlias, snapshotCounts,
          resolved, authorized, temporary, stagedCounts, published, status
vars == <<phase,aliasTarget,counts,enabled,op,selector,blobKey,permit,
          aliasPermit,authMode,outcome,retargeted,snapshotAlias,snapshotCounts,
          resolved,authorized,temporary,stagedCounts,published,status>>
EmptyCounts == [k \in Keys |-> 0]
Init ==
 /\ phase = "choose" /\ aliasTarget = KeyA /\ counts = EmptyCounts
 /\ enabled = Keys /\ op = "Encrypt" /\ selector = Alias /\ blobKey = KeyA
 /\ permit = [k \in Keys |-> FALSE] /\ aliasPermit = FALSE
 /\ authMode = "policy" /\ outcome = "ok" /\ retargeted = FALSE
 /\ snapshotAlias = None /\ snapshotCounts = EmptyCounts
 /\ resolved = None /\ authorized = None /\ temporary = None
 /\ stagedCounts = EmptyCounts /\ published = None /\ status = "pending"
Choose(a,b,e,operation,selection,permissions,aliasPermission,authorization,storage) ==
 /\ phase = "choose" /\ phase' = "prepared"
 /\ aliasTarget' = a /\ blobKey' = b /\ enabled' = e
 /\ op' = operation /\ selector' = selection /\ permit' = permissions
 /\ aliasPermit' = aliasPermission /\ authMode' = authorization /\ outcome' = storage
 \* Existing ciphertext was minted by a prior accepted encryption under b.
 /\ counts' = [k \in Keys |-> IF k = b THEN 1 ELSE 0]
 /\ UNCHANGED <<retargeted,snapshotAlias,snapshotCounts,resolved,authorized,
                temporary,stagedCounts,published,status>>
Retarget ==
 /\ phase = "prepared" /\ ~retargeted
 /\ aliasTarget' = Other(aliasTarget) /\ retargeted' = TRUE
 /\ UNCHANGED <<phase,counts,enabled,op,selector,blobKey,permit,aliasPermit,
                authMode,outcome,snapshotAlias,snapshotCounts,resolved,authorized,
                temporary,stagedCounts,published,status>>
Begin ==
 /\ phase = "prepared" /\ phase' = "resolve"
 /\ snapshotAlias' = aliasTarget /\ snapshotCounts' = counts
 /\ UNCHANGED <<aliasTarget,counts,enabled,op,selector,blobKey,permit,aliasPermit,
                authMode,outcome,retargeted,resolved,authorized,
                temporary,stagedCounts,published,status>>
Resolve ==
 LET key == IF selector = Alias THEN snapshotAlias
            ELSE IF selector = None THEN IF op = "Decrypt" THEN blobKey ELSE None
            ELSE selector
 IN /\ phase = "resolve" /\ resolved' = key
    /\ phase' = IF key = None THEN "done" ELSE "authorize"
    /\ status' = IF key = None THEN "invalid" ELSE "pending"
    /\ UNCHANGED <<aliasTarget,counts,enabled,op,selector,blobKey,permit,aliasPermit,
                   authMode,outcome,retargeted,snapshotAlias,snapshotCounts,authorized,
                   temporary,stagedCounts,published>>
PolicyAllows(resource) == IF resource = Alias THEN aliasPermit ELSE permit[resource]
Authorize ==
 LET resource == resolved
     allowed == authMode = "policy" /\ PolicyAllows(resource)
 IN /\ phase = "authorize" /\ authorized' = resource
    /\ phase' = IF allowed THEN "check" ELSE "done"
    /\ status' = IF authMode = "error" THEN "authorizationError"
                  ELSE IF allowed THEN "pending" ELSE "denied"
    /\ UNCHANGED <<aliasTarget,counts,enabled,op,selector,blobKey,permit,aliasPermit,
                   authMode,outcome,retargeted,snapshotAlias,snapshotCounts,resolved,
                   temporary,stagedCounts,published>>
CheckCiphertext ==
 LET mismatch == op = "Decrypt" /\ resolved # blobKey
 IN /\ phase = "check"
    /\ phase' = IF mismatch THEN "done" ELSE "crypto"
    /\ status' = IF mismatch THEN "incorrectKey" ELSE "pending"
    /\ UNCHANGED <<aliasTarget,counts,enabled,op,selector,blobKey,permit,aliasPermit,
                   authMode,outcome,retargeted,snapshotAlias,snapshotCounts,resolved,
                   authorized,temporary,stagedCounts,published>>
Crypto ==
 LET dataKey == IF op = "Decrypt" THEN blobKey ELSE resolved
     ready == dataKey \in enabled /\ (op # "Encrypt" \/ snapshotCounts[dataKey] < 2)
     nativeFailure == ~ready \/ outcome = "cryptoFail"
     writeFailure == op = "Encrypt" /\ outcome = "writeFail"
     candidate == IF op = "Encrypt" /\ outcome # "writeFail"
                  THEN [snapshotCounts EXCEPT ![resolved] = @ + 1] ELSE snapshotCounts
     result == [key |-> IF op = "Decrypt" THEN blobKey ELSE resolved,
                kind |-> IF op = "Decrypt" THEN "plaintext" ELSE "ciphertext"]
 IN /\ phase = "crypto"
    /\ phase' = IF nativeFailure \/ writeFailure THEN "done" ELSE "commit"
    /\ status' = IF nativeFailure THEN "nativeError" ELSE IF writeFailure THEN "writeError" ELSE "pending"
    /\ stagedCounts' = IF nativeFailure \/ writeFailure THEN snapshotCounts ELSE candidate
    /\ temporary' = IF nativeFailure \/ writeFailure THEN None ELSE result
    /\ UNCHANGED <<aliasTarget,counts,enabled,op,selector,blobKey,permit,aliasPermit,
                   authMode,outcome,retargeted,snapshotAlias,snapshotCounts,resolved,
                   authorized,published>>
Commit ==
 LET success == outcome # "commitFail"
 IN /\ phase = "commit" /\ phase' = "done"
    /\ counts' = IF success THEN stagedCounts ELSE snapshotCounts
    /\ published' = IF success THEN temporary ELSE None
    /\ status' = IF success THEN "success" ELSE "commitError"
    /\ UNCHANGED <<aliasTarget,enabled,op,selector,blobKey,permit,aliasPermit,
                   authMode,outcome,retargeted,snapshotAlias,snapshotCounts,resolved,
                   authorized,temporary,stagedCounts>>
Next == (\E a,b \in Keys, e \in SUBSET Keys, operation \in Ops, selection \in Selectors,
            permissions \in [Keys -> BOOLEAN], aliasPermission \in BOOLEAN,
            authorization \in AuthModes, storage \in Outcomes :
            Choose(a,b,e,operation,selection,permissions,aliasPermission,authorization,storage)) \/
        Retarget \/ Begin \/ Resolve \/ Authorize \/ CheckCiphertext \/ Crypto \/ Commit
TypeOK ==
 /\ phase \in Phases /\ aliasTarget \in Keys /\ counts \in [Keys -> 0..2]
 /\ enabled \subseteq Keys /\ op \in Ops /\ selector \in Selectors /\ blobKey \in Keys
 /\ permit \in [Keys -> BOOLEAN] /\ aliasPermit \in BOOLEAN
 /\ authMode \in AuthModes /\ outcome \in Outcomes /\ retargeted \in BOOLEAN
 /\ snapshotAlias \in Keys \cup {None} /\ snapshotCounts \in [Keys -> 0..2]
 /\ resolved \in Keys \cup {None} /\ authorized \in Keys \cup {None,Alias}
 /\ stagedCounts \in [Keys -> 0..2] /\ status \in Statuses
TransactionSnapshotStable == phase \in Active => aliasTarget = snapshotAlias /\ counts = snapshotCounts
AliasResolutionCurrent == (selector = Alias /\ resolved # None) => resolved = snapshotAlias
OmittedDecryptUsesBlob == (op = "Decrypt" /\ selector = None /\ resolved # None) => resolved = blobKey
ConcreteKeyAuthorization == authorized # None => authorized = resolved
AuthorizationBeforeMismatch == (phase = "done" /\ status = "incorrectKey") =>
 authorized = resolved /\ authMode = "policy" /\ permit[resolved]
NoPublicationBeforeCommit == phase # "done" => published = None
FailureCounterFrame == (phase = "done" /\ status # "success") => counts = snapshotCounts
FailureNoPublication == (phase = "done" /\ status # "success") => published = None
WriteFailureRefused ==
 (phase = "done" /\ op = "Encrypt" /\ outcome = "writeFail") => status # "success"
PublishedKeyBound == (phase = "done" /\ status = "success") =>
 /\ published # None /\ published.key = resolved
 /\ (op = "Decrypt" => resolved = blobKey)
 /\ authMode = "policy" /\ permit[resolved] /\ resolved \in enabled
EncryptCounterExact == (phase = "done" /\ status = "success" /\ op = "Encrypt") =>
 counts = [snapshotCounts EXCEPT ![resolved] = @ + 1]
DecryptCounterFrame == (phase = "done" /\ status = "success" /\ op = "Decrypt") => counts = snapshotCounts
Spec == Init /\ [][Next]_vars
=============================================================================
