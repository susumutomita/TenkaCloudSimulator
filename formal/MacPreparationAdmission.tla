---------------- MODULE MacPreparationAdmission ----------------
EXTENDS Naturals, FiniteSets
\* One attempt per independent mode. OS/parser/hash outcomes are inputs, not
\* proofs. No transition runs a process or certifies a Battle-ready runtime.
VARIABLE s
Init == \E mode \in {"plan", "image", "plist", "preflight", "builder"}:
 s = [mode |-> mode, phase |-> "new", accepted |-> FALSE,
      evidence |-> {}, retained |-> FALSE, closed |-> FALSE,
      absent |-> FALSE, originalChecked |-> FALSE, copyCreated |-> FALSE, passed |-> FALSE, launched |-> FALSE,
      installed |-> FALSE, finalized |-> FALSE, incomplete |-> FALSE]
Plan(platform, generation, paths) ==
 /\ s.mode="plan" /\ s.phase="new"
 /\ platform /\ generation /\ paths
 /\ s'=[s EXCEPT !.phase="done", !.accepted=TRUE,
   !.evidence={x \in {"platform","generation","paths"}:
     CASE x="platform" -> platform [] x="generation" -> generation [] OTHER -> paths}]
ImageOpen(pin, canonical, private, independent, regular, standalone, digest, stable) ==
 /\ s.mode="image" /\ s.phase="new"
 /\ pin /\ canonical /\ private /\ independent /\ regular
 /\ standalone /\ digest /\ stable
 /\ s'=[s EXCEPT !.phase="held", !.accepted=TRUE, !.retained=TRUE,
   !.evidence={x \in {"pin","canonical","private","independent","regular","standalone","digest","stable"}:
    CASE x="pin" -> pin [] x="canonical" -> canonical [] x="private" -> private
    [] x="independent" -> independent [] x="regular" -> regular
    [] x="standalone" -> standalone [] x="digest" -> digest [] OTHER -> stable}]
ImageClose == /\ s.mode="image" /\ s.phase="held"
 /\ s'=[s EXCEPT !.phase="done", !.retained=FALSE, !.closed=TRUE]
Plist(valid) == /\ s.mode="plist" /\ s.phase="new" /\ valid
 /\ s'=[s EXCEPT !.phase="done", !.accepted=TRUE,
               !.evidence=IF valid THEN {"valid"} ELSE {}]
OriginalAdmission(regular) ==
 /\ s.mode="preflight" /\ s.phase="new" /\ regular
 /\ s'=[s EXCEPT !.phase="admitted", !.originalChecked=regular]
CopyCheck(pin, copy, image) ==
 /\ s.mode="preflight" /\ s.phase="admitted" /\ s.originalChecked
 /\ pin /\ copy /\ image
 /\ s'=[s EXCEPT !.phase="checked", !.accepted=TRUE, !.copyCreated=TRUE,
   !.evidence={x \in {"original","copy","image"}:
    CASE x="original" -> pin [] x="copy" -> copy [] OTHER -> image}]
CopyClose(ok) == /\ s.mode="preflight" /\ s.phase="checked" /\ ok
 /\ s'=[s EXCEPT !.phase="closed", !.closed=TRUE]
CopyRemove(ok, absent) ==
 /\ s.mode="preflight" /\ s.phase="closed" /\ ok /\ absent
 /\ s'=[s EXCEPT !.phase="done", !.absent=absent, !.passed=TRUE]
Prepare(pins, fresh, copies) ==
 /\ s.mode="builder" /\ s.phase="new" /\ pins /\ fresh /\ copies
 /\ s'=[s EXCEPT !.phase="prepared", !.accepted=TRUE,
   !.evidence={x \in {"pins","fresh","copies"}:
    CASE x="pins" -> pins [] x="fresh" -> fresh [] OTHER -> copies}]
\* Emitted BUILD_SCRIPT obligations, not a claim the script has executed.
Install(overlay, binary, links) ==
 /\ s.mode="builder" /\ s.phase="prepared" /\ overlay /\ binary /\ links
 /\ s'=[s EXCEPT !.phase="installed", !.installed=TRUE,
   !.evidence=@ \cup {x \in {"overlay","binary","links"}:
    CASE x="overlay" -> overlay [] x="binary" -> binary [] OTHER -> links}]
Finalize(final, stopped, clean) ==
 /\ s.mode="builder" /\ s.phase="installed" /\ final /\ stopped /\ clean
 /\ s'=[s EXCEPT !.phase="done", !.finalized=TRUE,
   !.evidence=@ \cup {x \in {"final","stopped","clean"}:
    CASE x="final" -> final [] x="stopped" -> stopped [] OTHER -> clean}]
Failure == /\ s.phase \notin {"done","failed"}
 /\ s'=[s EXCEPT !.phase="failed", !.incomplete=TRUE, !.passed=FALSE]
Next == (\E a,b,c,d,e,f,g,h \in BOOLEAN: ImageOpen(a,b,c,d,e,f,g,h))
 \/ (\E a,b,c \in BOOLEAN: Plan(a,b,c) \/ CopyCheck(a,b,c) \/ Prepare(a,b,c)
        \/ Install(a,b,c) \/ Finalize(a,b,c))
 \/ (\E a,b \in BOOLEAN: CopyRemove(a,b))
 \/ (\E a \in BOOLEAN: Plist(a) \/ CopyClose(a) \/ OriginalAdmission(a))
 \/ ImageClose \/ Failure
TypeOK == /\ s.mode \in {"plan","image","plist","preflight","builder"}
 /\ s.phase \in {"new","admitted","held","checked","closed","prepared","installed","done","failed"}
 /\ s.accepted \in BOOLEAN /\ s.retained \in BOOLEAN /\ s.passed \in BOOLEAN
PlanEvidence == s.mode="plan" /\ s.accepted => s.evidence={"platform","generation","paths"}
ImageEvidence == s.mode="image" /\ s.accepted =>
 {"pin","canonical","private","independent","regular","standalone","digest","stable"} \subseteq s.evidence
ImageHandle == s.mode="image" /\ s.accepted => s.retained \/ s.closed
PlistEvidence == s.mode="plist" /\ s.accepted => "valid" \in s.evidence
OriginalBeforeCopy == s.copyCreated => s.originalChecked
CopyEvidence == s.mode="preflight" /\ s.accepted => {"original","copy","image"} \subseteq s.evidence
CopySuccess == s.passed => s.mode="preflight" /\ s.closed /\ s.absent /\ ~s.incomplete
PreparedEvidence == s.mode="builder" /\ s.accepted => {"pins","fresh","copies"} \subseteq s.evidence
InstalledEvidence == s.installed => {"overlay","binary","links"} \subseteq s.evidence
FinalizedEvidence == s.finalized => s.installed /\ {"final","stopped","clean"} \subseteq s.evidence
NoExecutionClaim == ~s.launched
=============================================================================
