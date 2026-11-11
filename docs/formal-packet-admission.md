# Packet admission guards and bounded byte-option scanner

Source scope: internal/workload/guestnet/packet.go, specifically header
admission guards and validTCPOptions. This is partial source correspondence,
not a complete parser refinement claim.

HeaderCheck evaluates actual numeric limits: Ethernet54..1514 bytes/typeIPv4,
version+IHL0x45, protocol6, nonzeroTTL, IP total40..frame-14, fragmentword0/DF,
checksum folded values65535, TCP headerwords*4 within20..total-20, low-nibble0
and nonzero ports. Six accepted witnesses and20 single-field invalid witnesses
exercise these conditions. The corpus is not a Cartesian product or proof of
all numeric inputs. Ethernet padding outside total is represented by accepted
frame60/1514 with total40.

Scan operates on actual finite byte sequences using cursor and seen-kind set.
It handles zero-only EOL tail; NOP; encoded length; duplicate nontrivial kinds;
MSS len4/nonzero16bit/SYN; scale len3/value<=14/SYN; SACK-permitted len2/SYN;
SACK len10..34,8-byte block alignment,ACK/nonSYN; timestamp len10; unknown reject.
Options are all zero/one/two chunk concatenations from the explicit valid and
malformed corpus, clipped at40bytes. SYN and ACK vary independently. Starts
have empty seen set/cursor1; all reachable terminal states are explored.
No seeded acceptance states or depth cutoff are used.

The independent oracle describes whole tokens and possible single partitions
of the complete byte string. It uses token length and numeric content, not the
scanner's cursor/seen/Policy. This is a finite language oracle for this corpus,
not a claim that arbitrary longer sequences contain at most two options.

## Independent oracle boundary cases

Options [2,4,3,0] with SYN set encode valid MSS768. The bytes can arise by
concatenating individually malformed chunks [2] and [4,3,0], so the independent
token grammar must accept represented nonzero MSS values rather than enumerate
only a few preselected valid tokens.

A removed minimum-frame guard alone is equivalent within the finite domain:
total>=40 and total<=frame-14 imply frame>=54. Similarly total>=40 follows from
TCP header>=20 and header<=total-20. These redundant single edits are not
sensitivity controls; the actual admission guards remain present. Malformed
option-length controls retain safe modeled indexing and must produce an
OptionSound counterexample, not an evaluator error.

## Native correspondence and exclusions

TestRejectTCPFrames maps length/version/protocol/TTL/fragment/header/port guards.
TestTCPOptions maps the concrete option policies; TestParseTCPFrame covers
valid field extraction; TestChecksumKnownVectors and TestTCPPseudoHeaderAndDF
cover byte checksums and pseudoheader/DF. These native tests complement the bounded model.

Checksum folded values are numeric inputs here, not a proof of checksumSum or
foldSum. A separate native arithmetic bound follows from max1514 frame bytes:
at most757 16-bit summands,757*65535=49,609,995<2^32; adding two folded sums is
at most131070. This arithmetic observation is not TLC implementation evidence.
Concrete slice indexing, endian decoding, checksum vectors/odd-byte handling,
packet field copy ownership and Go concurrency remain native obligations.
No ARP, frame stream, routing, TCP state, physical delivery or authorization
claim is made.
