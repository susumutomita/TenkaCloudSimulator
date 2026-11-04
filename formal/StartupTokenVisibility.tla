------------------------- MODULE StartupTokenVisibility -------------------------
EXTENDS Sequences, Naturals
CONSTANTS Tokens, None, MaxEmissions
ASSUME /\ Tokens # {} /\ None \notin Tokens /\ MaxEmissions \in Nat
VARIABLES fileState, fileToken, baseline, private, context, emissions
vars == <<fileState, fileToken, baseline, private, context, emissions>>
Contexts == [auth: BOOLEAN, show: BOOLEAN, tty: BOOLEAN, ci: BOOLEAN]
EmissionType == [token: Tokens, auth: BOOLEAN, show: BOOLEAN,
                 tty: BOOLEAN, ci: BOOLEAN, private: BOOLEAN]
Init == /\ fileState \in {"absent", "valid", "invalid"}
        /\ fileToken \in (IF fileState = "absent" THEN {None} ELSE Tokens)
        /\ baseline = fileToken /\ private = (fileState = "valid")
        /\ context \in Contexts /\ emissions = <<>>
Chosen(t) == IF fileState = "absent" THEN t ELSE fileToken
PrintAllowed == context.show /\ context.tty /\ ~context.ci /\ context.auth
NewPrivate == IF fileState = "absent" THEN TRUE ELSE private
Start(t) == /\ context.auth /\ fileState # "invalid"
            /\ (~PrintAllowed \/ Len(emissions) < MaxEmissions)
            /\ fileState' = "valid" /\ fileToken' = Chosen(t)
            /\ baseline' = IF baseline = None THEN Chosen(t) ELSE baseline
            /\ private' = NewPrivate
            /\ emissions' = IF PrintAllowed THEN Append(emissions,
                 [token |-> Chosen(t), auth |-> context.auth, show |-> context.show,
                  tty |-> context.tty, ci |-> context.ci, private |-> NewPrivate])
                 ELSE emissions
            /\ UNCHANGED context
ChangeContext(c) == /\ context' = c
                    /\ UNCHANGED <<fileState, fileToken, baseline, private, emissions>>
Next == (\E t \in Tokens: Start(t)) \/ (\E c \in Contexts: ChangeContext(c))
        \/ UNCHANGED vars
TypeOK == /\ fileState \in {"absent", "valid", "invalid"}
          /\ fileToken \in Tokens \cup {None} /\ baseline \in Tokens \cup {None}
          /\ private \in BOOLEAN /\ context \in Contexts
          /\ emissions \in Seq(EmissionType) /\ Len(emissions) <= MaxEmissions
FileIdentityFrozen == baseline # None => fileToken = baseline
CreatedFilePrivate == fileState = "valid" => private
HistoryVisibility == \A i \in 1..Len(emissions):
                     /\ emissions[i].auth /\ emissions[i].show /\ emissions[i].tty
                     /\ ~emissions[i].ci /\ emissions[i].private
                     /\ emissions[i].token = baseline
InvalidFileNeverEmits == fileState = "invalid" => emissions = <<>>
=============================================================================
