---------------------- MODULE CodeCommitConsoleIntent -----------------------
EXTENDS Naturals, FiniteSets

\* A single deliberate command from a mounted repository detail. Values are
\* observations of the native API, not a model of a Git object database.
Repos == {"original", "other"}
Views == {"empty", "incomplete", "tip", "history"}
Kinds == {"put", "fork"}
Replies == {"verified", "conflict", "lost-applied", "lost-unapplied", "malformed",
            "wrong-file", "wrong-commit", "moved-branch", "changed-repository"}
Reads == {"exact-file", "missing-file", "named-branch", "all-pages-absent",
          "first-page-absent", "other-branch", "other-path", "unavailable",
          "changed-before-read", "changed-after-read"}
No == "none"
Tip == "attached"
Old == "historical"
Inspected(v) == CASE v = "tip" -> Tip [] v = "history" -> Old [] OTHER -> No

VARIABLES repo, view, phase, command, checked, sends, reply, draft, notice,
          tries, ticket, reading, wasCurrent
vars == <<repo, view, phase, command, checked, sends, reply, draft, notice,
          tries, ticket, reading, wasCurrent>>

MayChoose(k,v) ==
 IF k = "put" THEN v \in {"empty","tip"} ELSE Inspected(v) # No
MakeCommand(k,v,r) ==
 [kind |-> k, scope |-> r, branch |-> IF k = "put" THEN "main" ELSE "fork",
  path |-> IF k = "put" THEN "file" ELSE No,
  parent |-> IF k = "put" /\ v = "tip" THEN Tip ELSE No,
  from |-> IF k = "fork" THEN Inspected(v) ELSE No]
NoCommand == [kind |-> No, scope |-> No, branch |-> No, path |-> No,
              parent |-> No, from |-> No]

Init == /\ repo \in Repos /\ view \in Views /\ phase = "choose"
        /\ command = NoCommand /\ checked = No /\ sends = 0 /\ reply = No
        /\ draft = FALSE /\ notice = No /\ tries = 0 /\ ticket = No
        /\ reading = No /\ wasCurrent = FALSE

Choose(k) ==
 /\ phase = "choose" /\ MayChoose(k,view)
 /\ command' = MakeCommand(k,view,repo) /\ phase' = "prepared"
 /\ draft' = (k = "put")
 /\ UNCHANGED <<repo, view, checked, sends, reply, notice, tries, ticket, reading, wasCurrent>>

\* The real client checks GetRepository and the current AbortSignal before
\* sending. Replacement after that check is outside this API guarantee.
CanSend(actual) == actual = repo
Preflight(actual) ==
 /\ phase = "prepared" /\ checked' = actual
 /\ IF CanSend(actual)
       THEN /\ phase' = "sent" /\ sends' = sends + 1 /\ draft' = draft
       ELSE /\ phase' = "stopped" /\ sends' = sends /\ draft' = FALSE
 /\ UNCHANGED <<repo, view, command, reply, notice, tries, ticket, reading, wasCurrent>>

\* lost-applied and lost-unapplied are ghost alternatives for the same missing
\* client response. Neither supplies acknowledgement evidence or a commit ID.
\* "verified" includes the native acknowledgement, exact saved branch tip and
\* commit, exact file/tree/parent for PutFile, and final physical identity.
Verified(r) == r = "verified"
Respond(r) ==
 /\ phase = "sent" /\ (r # "wrong-file" \/ command.kind = "put")
 /\ reply' = r
 /\ IF Verified(r)
       THEN /\ phase' = "terminal" /\ notice' = "saved" /\ draft' = FALSE
       ELSE IF r = "conflict"
       THEN /\ phase' = "terminal" /\ notice' = "rejected" /\ draft' = (command.kind = "put")
       ELSE /\ phase' = "unknown" /\ notice' = "uncertain" /\ draft' = FALSE
 /\ sends' = sends
 /\ UNCHANGED <<repo, view, command, checked, tries, ticket, reading, wasCurrent>>

\* The user must explicitly request a current-state read of the original
\* branch/path. A new invocation can follow one unsuccessful recovery.
BeginRead ==
 /\ phase = "unknown" /\ tries < 2
 /\ phase' = "checking" /\ tries' = tries + 1 /\ ticket' = "active"
 /\ UNCHANGED <<repo, view, command, checked, sends, reply, draft, notice, reading, wasCurrent>>
CancelRead ==
 /\ phase = "checking" /\ ticket = "active" /\ ticket' = "retired"
 /\ UNCHANGED <<repo, view, phase, command, checked, sends, reply, draft, notice, tries, reading, wasCurrent>>

CurrentEvidence(k,o) ==
 \/ o = "all-pages-absent"
 \/ k = "put" /\ o \in {"exact-file","missing-file"}
 \/ k = "fork" /\ o = "named-branch"
UseRead(k,o) == ticket = "active" /\ CurrentEvidence(k,o)
FinishRead(o) ==
 /\ phase = "checking"
 /\ reading' = o /\ wasCurrent' = (ticket = "active") /\ ticket' = No
 /\ IF UseRead(command.kind,o)
       THEN /\ phase' = "terminal" /\ notice' = "current"
       ELSE /\ phase' = "unknown" /\ notice' = "uncertain"
 /\ UNCHANGED <<repo, view, command, checked, sends, reply, draft, tries>>

Next == \/ \E k \in Kinds : Choose(k)
        \/ \E id \in Repos \cup {"aborted"} : Preflight(id)
        \/ \E r \in Replies : Respond(r)
        \/ BeginRead \/ CancelRead \/ \E o \in Reads : FinishRead(o)

TypeOK ==
 /\ repo \in Repos /\ view \in Views
 /\ phase \in {"choose","prepared","sent","stopped","unknown","checking","terminal"}
 /\ command.kind \in Kinds \cup {No} /\ command.scope \in Repos \cup {No}
 /\ command.branch \in {"main","fork",No} /\ command.path \in {"file",No}
 /\ command.parent \in {Tip,Old,No} /\ command.from \in {Tip,Old,No}
 /\ checked \in Repos \cup {"aborted",No} /\ sends \in 0..2 /\ reply \in Replies \cup {No}
 /\ draft \in BOOLEAN /\ notice \in {No,"saved","rejected","uncertain","current"}
 /\ tries \in 0..2 /\ ticket \in {"active","retired",No} /\ reading \in Reads \cup {No}
 /\ wasCurrent \in BOOLEAN

ChoiceAndParent ==
 /\ command.kind = "put" => /\ view \in {"empty","tip"} /\ command.branch = "main"
                             /\ command.path = "file" /\ command.from = No
                             /\ command.parent = IF view = "empty" THEN No ELSE Tip
 /\ command.kind = "fork" => /\ view \in {"tip","history"} /\ command.branch = "fork"
                              /\ command.path = No /\ command.parent = No
                              /\ command.from = IF view = "history" THEN Old ELSE Tip
PhysicalAdmission == sends > 0 => command.scope = repo /\ checked = repo
NoReplay == sends <= 1
Acknowledged == notice = "saved" => reply = "verified"
SavedWhenVerified == reply = "verified" => phase = "terminal" /\ notice = "saved" /\ ~draft
ConflictKeepsEdit == reply = "conflict" => notice = "rejected" /\ draft = (command.kind = "put")
UnknownLock ==
 (reply \in Replies \ {"verified","conflict"} /\ notice # "current")
 => phase \in {"unknown","checking"} /\ notice = "uncertain" /\ ~draft
RecoveryEvidence ==
 notice = "current" =>
   /\ tries > 0 /\ wasCurrent
   /\ (reading = "all-pages-absent" \/
       (command.kind = "put" /\ reading \in {"exact-file","missing-file"}) \/
       (command.kind = "fork" /\ reading = "named-branch"))
RecoveryCompletes ==
 (wasCurrent /\ (reading = "all-pages-absent" \/
                 (command.kind = "put" /\ reading \in {"exact-file","missing-file"}) \/
                 (command.kind = "fork" /\ reading = "named-branch")))
 => notice = "current" /\ phase = "terminal"
CurrentIsObservation == notice = "current" => reply \in Replies \ {"verified","conflict"}
                              /\ phase = "terminal" /\ ~draft

=============================================================================
