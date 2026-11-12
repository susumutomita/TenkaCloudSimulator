# Frame codec lifecycle

Scope is only `internal/workload/guestnet/frame.go`. The model starts from one
empty decoder and one empty encoder, and explores all histories within two calls
each. Header/body/write completion are separate transitions. No seed selection,
symmetry reduction or depth truncation is used; new calls stop after the explicit
bound, while already-started calls may finish. Deadlock checking is disabled for
terminal bounded states. This is not an unbounded or implementation proof.

- DStart corresponds to serialized entry and sticky failed/nil-reader rejection.
- DHeader abstracts io.ReadFull(header), big-endian length decoding and range test:
  full/valid are supplied Booleans. Invalid/incomplete headers poison permanently
  and cannot proceed to a body read.
- DBody abstracts io.ReadFull(body). Only complete successful bodies yield a frame;
  incomplete body is permanently poisoned, never a partial successful result.
- EStart checks failed/nil-writer before local size validation. Invalid local size
  leaves stream usable and makes no write. Nil writer rejects even invalid frames.
- EWrite represents one logical destination.Write containing header+body. Both
  exact count and absent error are necessary for success. Short/long/negative count
  is abstracted as not exact. Any uncertain result poisons the encoder permanently;
  uncertain effects remain sticky and are not cancelled by a later refusal.
- Single active phase per codec abstracts the mutex. Decoder and encoder are
  independent objects: the model does not prohibit one of each operating together.
- first-fault history is retained independently of the failed flag, and last-step
  snapshots check no further I/O after poison and no overlapping call admission.

Native correspondence: TestFrameWireAndFragmentation verifies byte contents and
fragmented readers; TestDecoderPoisonAndBounds checks all invalid sizes and all
partial prefix lengths; TestEncoderFailure checks short/invalid-count/error and
nil writers plus local-size refusal without poison; TestConcurrentWholeFrames
checks serialized whole-frame behavior. FuzzDecodeFrame supplies concrete byte
and size correspondence. Those tests remain required alongside bounded model validation.

Excluded: actual uint32/int arithmetic, byte encoding/copy correctness, arbitrary
I/O implementations, Go memory-model/refinement, external concurrent mutation of
caller frame bytes, fairness, physical packet ownership/delivery, blocking reader
interruption, timeouts/revocation wiring and unbounded calls. Source warns codec
cannot interrupt an arbitrary blocked Reader: model transitions may stutter in
reality, and no liveness guarantee is claimed. Bounds do not prove production
14..1514 arithmetic; valid is the checked predicate abstraction.
