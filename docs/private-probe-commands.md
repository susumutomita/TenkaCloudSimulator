# Injected probe commands

The version 1 start/read codec is an in-process boundary around an already
selected HTTPProbeOperationScope. It supplies no endpoint, authentication,
registry, physical owner, clock synchronizer or production capability.
Binding/service equality prevents rebinding; it does not grant authority.

Commands reject unknown and duplicate fields, arrays, oversized JSON, unsafe
millisecond values and zero identity/key values. Start carries a selected clock
domain and absolute Unix-millisecond deadline. New execution uses time.UnixMilli,
never receipt time plus remaining duration. The provider supplies a copied
qualification interval for this backend clock. Existing exact operations are
looked up before fresh expiry checks; duplicates retain their first deadline.
Read remains available after qualification expiry and never executes work.

The command boundary installs no request-handler cancellation. Its deadline-only
parent lets existing scoped Start own the actual operation timer. Returning from
Dispatch cannot cancel an admitted worker. Errors and missing snapshots never
certify non-execution, settlement or permission to retry. A start snapshot is not
a substitute for actual result readiness and settlement discovery.

The Host bridge separately requires an externally qualified anchored relation
between original Host monotonic time and this backend domain. No production
qualification is implemented. Conservative conversion, uncertainty/drift bounds,
physical fencing and restart recovery remain independent obligations. Tests use
memory owners and explicitly selected fixtures. Battle is not enabled by this
component.
