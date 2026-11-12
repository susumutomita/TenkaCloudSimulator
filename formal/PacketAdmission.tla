---------------- MODULE PacketAdmission ----------------
EXTENDS Naturals, Sequences, FiniteSets
Zeros(n) == [i \in 1..n |-> 0]
MSS1 == <<2,4,0,1>>
MSS256 == <<2,4,1,0>>
WS0 == <<3,3,0>>
WS14 == <<3,3,14>>
SACKPerm == <<4,2>>
SACK(n) == <<5,n>> \o Zeros(n-2)
TS == <<8,10>> \o Zeros(8)
ValidTokens == {<<0>>,<<0,0>>,<<1>>,MSS1,MSS256,WS0,WS14,SACKPerm,SACK(10),SACK(18),SACK(34),TS}
BadTokens == {<<0,1>>,<<2>>,<<2,1>>,<<2,4,0,0>>,<<2,5,0,1,0>>,
              <<3,3,15>>,<<3,2>>,<<3,4,0,0>>,<<8,10>>,<<4,3,0>>,<<5,2>>,<<5,11>> \o Zeros(9),
              <<5,42>> \o Zeros(8),<<8,9>> \o Zeros(7),<<9,2>>}
Chunks == ValidTokens \cup BadTokens \cup {<<>>}
Options == {s \in {a \o b: a,b \in Chunks}: Len(s) <= 40}
AllZero(s) == \A i \in DOMAIN s: s[i] = 0
\* Independent byte-language grammar: whole tokens and one split, not scanner state.
Token(t,syn,ack) ==
 IF t = <<1>> THEN TRUE
 ELSE IF Len(t) < 2 THEN FALSE
 ELSE
   \/ (Len(t) = 4 /\ t[1] = 2 /\ t[2] = 4 /\ syn /\ (t[3] # 0 \/ t[4] # 0))
   \/ (Len(t) = 3 /\ t[1] = 3 /\ t[2] = 3 /\ syn /\ t[3] \in 0..14)
   \/ (t = <<4,2>> /\ syn)
   \/ (Len(t) \in {10,18,26,34} /\ t[1] = 5 /\ t[2] = Len(t) /\ ~syn /\ ack)
   \/ (Len(t) = 10 /\ t[1] = 8 /\ t[2] = 10)
LanguageMember(s,syn,ack) ==
 AllZero(s) \/ Token(s,syn,ack) \/
 (\E cut \in 1..(Len(s)-1):
   LET a == SubSeq(s,1,cut)
       b == SubSeq(s,cut+1,Len(s))
   IN Token(a,syn,ack) /\
      (AllZero(b) \/ (Token(b,syn,ack) /\ (a[1] = 1 \/ b[1] = 1 \/ a[1] # b[1]))))

Header == [frame |-> 54, eth |-> 2048, vihl |-> 69, protocol |-> 6, ttl |-> 1,
           total |-> 40, fragment |-> 0, ipsum |-> 65535, words |-> 5,
           reserved |-> 0, sp |-> 1, dp |-> 1, tcpsum |-> 65535]
GoodHeaders == {Header, [Header EXCEPT !.frame = 60], [Header EXCEPT !.frame = 1514],
 [Header EXCEPT !.fragment = 16384], [Header EXCEPT !.ttl = 64],
 [Header EXCEPT !.frame = 60, !.total = 46, !.words = 6]}
BadHeaders == {[Header EXCEPT !.frame = 53], [Header EXCEPT !.frame = 1515],
 [Header EXCEPT !.eth = 2054], [Header EXCEPT !.vihl = 70], [Header EXCEPT !.vihl = 101],
 [Header EXCEPT !.protocol = 17], [Header EXCEPT !.ttl = 0],
 [Header EXCEPT !.total = 39], [Header EXCEPT !.total = 41],
 [Header EXCEPT !.fragment = 8192], [Header EXCEPT !.fragment = 32768],
 [Header EXCEPT !.fragment = 1], [Header EXCEPT !.ipsum = 65534],
 [Header EXCEPT !.words = 4], [Header EXCEPT !.words = 6],
 [Header EXCEPT !.reserved = 1], [Header EXCEPT !.reserved = 8],
 [Header EXCEPT !.sp = 0], [Header EXCEPT !.dp = 0],
 [Header EXCEPT !.tcpsum = 65534]}
HeaderPass(h) ==
 /\ h.frame >= 54 /\ h.frame <= 1514 /\ h.eth = 2048
 /\ h.vihl = 69 /\ h.protocol = 6 /\ h.ttl # 0
 /\ h.total >= 40 /\ h.total <= h.frame-14
 /\ h.fragment \in {0,16384} /\ h.ipsum = 65535
 /\ h.words*4 >= 20 /\ h.words*4 <= h.total-20
 /\ h.reserved = 0 /\ h.sp # 0 /\ h.dp # 0 /\ h.tcpsum = 65535
VARIABLES mode, header, options, syn, ack, pos, seen, done, accepted
vars == <<mode,header,options,syn,ack,pos,seen,done,accepted>>
Init ==
 /\ mode \in {"header","options"}
 /\ IF mode = "header" THEN
       /\ header \in GoodHeaders \cup BadHeaders /\ options = <<>>
       /\ syn = FALSE /\ ack = FALSE
    ELSE
       /\ header = Header /\ options \in Options /\ syn \in BOOLEAN /\ ack \in BOOLEAN
 /\ pos = 1 /\ seen = {} /\ done = FALSE /\ accepted = FALSE
Policy(k,n) ==
 CASE k = 2 -> n = 4 /\ syn /\ options[pos+2]*256+options[pos+3] # 0
   [] k = 3 -> n = 3 /\ syn /\ options[pos+2] <= 14
   [] k = 4 -> n = 2 /\ syn
   [] k = 5 -> ~syn /\ ack /\ n >= 10 /\ n <= 34 /\ (n-2)%8 = 0
   [] k = 8 -> n = 10
   [] OTHER -> FALSE
HeaderCheck == /\ mode = "header" /\ ~done /\ done' = TRUE
 /\ accepted' = HeaderPass(header)
 /\ UNCHANGED <<mode,header,options,syn,ack,pos,seen>>
Finish == /\ mode = "options" /\ ~done /\ pos > Len(options)
 /\ done' = TRUE /\ accepted' = TRUE
 /\ UNCHANGED <<mode,header,options,syn,ack,pos,seen>>
Scan ==
 /\ mode = "options" /\ ~done /\ pos <= Len(options)
 /\ LET k == options[pos]
        remaining == Len(options)-pos+1
        n == IF remaining >= 2 THEN options[pos+1] ELSE 0
        good == remaining >= 2 /\ n >= 2 /\ n <= remaining /\ k \notin seen
                /\ Policy(k,n)
    IN IF k = 0 THEN
         /\ done' = TRUE /\ accepted' = AllZero(SubSeq(options,pos+1,Len(options)))
         /\ UNCHANGED <<pos,seen>>
       ELSE IF k = 1 THEN
         /\ pos' = pos+1 /\ UNCHANGED <<seen,done,accepted>>
       ELSE IF good THEN
         /\ pos' = pos+n /\ seen' = seen \cup {k} /\ UNCHANGED <<done,accepted>>
       ELSE
         /\ done' = TRUE /\ accepted' = FALSE /\ UNCHANGED <<pos,seen>>
 /\ UNCHANGED <<mode,header,options,syn,ack>>
Next == HeaderCheck \/ Finish \/ Scan
HeaderSound == mode = "header" /\ done => accepted = (header \in GoodHeaders)
OptionSound == mode = "options" /\ done => (accepted => LanguageMember(options,syn,ack))
OptionComplete == mode = "options" /\ done => ((LanguageMember(options,syn,ack)) => accepted)
Progress == ~done => ENABLED Next
BoundedCursor == pos >= 1 /\ pos <= Len(options)+1
=============================================================================
