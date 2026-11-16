-------------------- MODULE CFNParameterRuleAdmission --------------------
EXTENDS Naturals

Modes == {"validate", "create"}
Locations == {"west", "east"}
Defects == {"none", "unknownRef", "badArity"}
Inputs == [mode : Modes, copies : {1, 2}, primary : Locations,
           secondary : Locations \cup {""}, secondAssertion : BOOLEAN,
           defect : Defects]

VARIABLES input, phase, published
vars == <<input, phase, published>>

Init ==
    /\ input \in Inputs
    /\ phase = "received"
    /\ published = FALSE

\* A defect represents an undeclared Ref or a malformed operand in the
\* second arm of And. Both arms must be well-formed, even for one copy.
CheckShape ==
    /\ phase = "received"
    /\ phase' = IF input.defect = "none" THEN "checked" ELSE "invalid"
    /\ UNCHANGED <<input, published>>

CheckValues ==
    LET active == input.copies = 2
        complete == ~(input.secondary = "")
        distinct == ~(input.primary = input.secondary)
        firstAssertion == complete /\ distinct
        passes == firstAssertion /\ input.secondAssertion
    IN  /\ phase = "checked"
        /\ phase' = IF input.mode = "validate" THEN "validated"
                    ELSE IF ~active \/ passes THEN "admitted" ELSE "refused"
        /\ UNCHANGED <<input, published>>

Commit ==
    /\ phase = "admitted"
    /\ phase' = "created"
    /\ published' = TRUE
    /\ UNCHANGED input

Terminal == phase \in {"invalid", "refused", "validated", "created"}
Next == CheckShape \/ CheckValues \/ Commit \/ (Terminal /\ UNCHANGED vars)

ExpectedValues == input.copies = 1 \/
                (input.secondary \in Locations \ {input.primary} /\ input.secondAssertion)

EveryOperandChecked == (phase # "received") => ((phase = "invalid") = (input.defect # "none"))
ValidationIgnoresValues == (Terminal /\ input.mode = "validate" /\ input.defect = "none")
                            => phase = "validated"
AllActiveAssertionsRequired == (phase \in {"refused", "admitted", "created"})
                               => ((phase # "refused") = ExpectedValues)
PublicationFollowsAdmission == published = (phase = "created")
ValidatedCreatesNothing == (input.mode = "validate" \/ input.defect # "none") => ~published
TypeOK == /\ input \in Inputs
          /\ phase \in {"received", "checked", "invalid", "admitted", "refused", "validated", "created"}
          /\ published \in BOOLEAN
=============================================================================
