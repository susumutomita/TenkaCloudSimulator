# Conditional durable SSM command model

`SSMDurableCommands` describes the internal prerequisite store, not a registered
AWS API or executing guest backend. `guestcommand.DurableBackend` has no
production implementation. Its durable token deduplication and atomic lease
lifecycle fence are assumptions a future backend must separately establish.
Backend mutation controls demonstrate why those assumptions are necessary;
they cannot establish that an existing backend satisfies them.

## Correspondence and bounds

Three disjoint finite scenarios bound the search, not one end-to-end model:

- Dispatch: one command, two targets, two delayed deliveries per target. Commit
  abstracts atomic command/token-index insertion, exact readback and transaction
  commit in `Send`. Crash can occur before commit, before either dispatch, between
  targets or after dispatch. A restarted process cannot dispatch. Delayed effects
  require durable deduplication and the current lease lifecycle fence. Partial
  dispatch may remain unresolved forever.
- Reconciliation: target two starts completed; target one is unresolved. One
  observer captures the whole-record version; a competitor can commit terminal
  success before the first CAS. Version equality abstracts serialized record
  comparison in `Reconcile`. Nine faults cover exact identity, started-time and
  successful-exit evidence. Zero is unknown, one nonterminal, two success.
  Failure/timeout/rejection are not separately expanded. Terminal observations
  cannot be replaced. `Successful` requires both targets successful.
- Pagination: two frozen identities, one later insert, two supplied values for
  scope/filter/size, and expiry. Snapshot membership cannot acquire later inserts.
  Crash/restart retains the snapshot. The model abstracts offsets, serialization
  and token encoding; native tests separately cover replayed-token behavior.

Dispatch acceptance, unknown outcomes and transport loss do not imply success.
`Send` has no recovery dispatch. The backend must preserve its token binding
across restarts and fence effects against release; a provider held-lease read
alone cannot provide that guarantee.

## Sensitivity and verification limits

Negative controls remove persist-before-dispatch, single-dispatch, restart,
crash-retention, backend deduplication/lease, immutable authority, observation
identity/start-time, complete-exit evidence, CAS, all-target, snapshot and page
scope/filter/size/expiry guards. Each requires its designated invariant to fail.
These are transition edits, not disabled assertions. Registry integration and
current-commit execution remain separate gates.

This is not a Go/SQLite refinement proof and does not verify hashes, real durable
storage, guest execution or cancellation. Quotas, sizes, public SSM JSON/IAM and
original Battle acceptance are excluded. Existing native tests use an identified
backend contract test double. Neither that double nor this finite model replaces
a real backend. Checking a predecessor does not validate a future integration.
