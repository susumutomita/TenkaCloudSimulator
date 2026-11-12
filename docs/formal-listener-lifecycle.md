# Listener source-specific lifecycle abstraction

Scope listener.go only. Initial state has no listener; Create reserves one
logical stack/worker unit alongside one fixed external reservation. Successful
constructor setup is abstracted. Constructor failure rollback, GOMAXPROCS
changes, global mutex races and arbitrary peer configuration are not proved.

Inject records first SYN sequence and retained port; same-sequence unsealed
SYN retransmit is allowed, changed/reused/sealed SYN rejected. Data requires a
retained attempt and seals it. Invalid flags/payload/tuple/parser outcomes and
exhausted cumulative quota cancel admission. Input and output share counters;
charged malformed attempts do not refund quota. Source tuple/parser behavior
is supplied by bounded abstract event classes, not gVisor or byte proof.

Two modeled lifetime Accept invocations use distinct call IDs. At most one is
pending inside the stack, but a failed raw return clears accepting before raw
deadline/Close cleanup and WaitGroup.Done. Hence multiple retained cleanup
debts can coexist even though only one pending Accept is allowed. A successful
Accept publishes an owned wrapper only while active with a recorded peer port
and free retained capacity; it seals that attempt. The stack's TCP uniqueness
and remote-IP semantics are not inferred from the wrapper's port-only check.

Connections remain in the ownership map after Close. Closed flag, timer reset call
and raw Close return are separate transitions. Both successful and erroneous
raw Close returns complete the join; neither proves physical cleanup.
Listener Close first closes admission, then completes shutdown, waits all
retained connections and Accept debts, destroys stack workers, and only then
returns its own reservation. The external reservation is never reset.
No reader WaitGroup is invented: ReadFrame's cancellation/check behavior is
abstracted by Output, not modeled as a separately joined reader.

Time advances once across fixed expiry; traffic never renews it. Deadline
requests0/1/2 represent zero/at-expiry/beyond-expiry and clamp to fixed1.
Actual capacities8sessions/8stacks/32workers/4096frames/1MiB are reduced to
one retained port/connection,two call IDs,two frames/three units. This bounded
instance is not a capacity-scaling or Go scheduling refinement.

Native correspondence: TestListenerMemoryConnectionsAndOwnedCleanup,
TestListenerRejectsMalformedTuplesAndPortChurn,
TestListenerAttemptAndAggregateBounds,
TestListenerConfigurationSharedCapacityAndExpiry,
TestListenerCloseRacesAcceptedIOAndDeadlines,
TestListenerPendingReadCancellationAndAcceptCloseRace,
TestListenerCumulativeWireBudgetsIncludeOutput.


The timer witness means SetDeadline(zero) was invoked and returned, not that
the deadline was successfully cleared: the implementation intentionally ignores
that call's error. Close-return errors likewise establish join completion only.
Neither ghost state certifies gVisor/physical resource cleanup.


Successful Accept also has a registration-to-WaitGroup.Done gap: the wrapper
is inserted under owner.mu, then the lock is released before Done and return.
The model retains a registered call debt until AcceptComplete. Close may close
the registered wrapper during that gap; no claim is made that the eventual
caller return itself occurs before expiry/Close. The active check protects
registration, not an atomic user-visible return. Nil-raw failures and detailed
caller-context cancellation interleavings are omitted representative cases.

Deadline models the value passed to a completed setter call, not successful
application to gonet. The source returns that setter's error. An in-flight
setter holding owner.mu and its interaction with Close is not refined by this
atomic transition; native race tests remain required.


Focused verification: an unseeded finite baseline and28semantic controls pass.
Monotone independent safety monitors retain admission violations without
storing irrelevant last-event history. First-sequence and ownership/accounting
witnesses preserve the expected history; controls mutate the modeled
implementation transitions, not these witnesses.

Two initial attempts failed before usable checking: multiline UNCHANGED
indentation caused a parse error; missing parentheses around a Boolean
assignment left cancellation unconstrained. Both are retained. The first
AcceptUnrecordedPort control expectation was too narrow: sealing an unknown
port violates AttemptRetention before NoLateAdmission. The final expected
property names that exact earlier safety breach; no invariant was removed or
reordered. Final baseline and all28controls use identical final model bytes.
This remains finite model evidence, not full repository acceptance or a proof
of concrete gVisor implementation/physical cleanup.
