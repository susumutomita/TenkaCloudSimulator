# Mac HVF runtime integration boundary (not enabled)

The selected direction is a trusted host supervisor owning a hardware-virtualized
ARM guest. Host/grader authority remains outside the guest. This package is the
initial fixed invocation admission only, not an executable runtime, asset verifier,
lease database, or safety certification. No caller in production imports it.

## Ownership and shutdown contract still required

The asset owner must verify executable, firmware and image provenance and retain
stable identities before spawn. A writable image must be an independent private
copy, never a link to the original. The qcow2 verifier must also reject backing
files, external data files, encryption/key dependencies and other unadmitted host
file references; a pinned top-level digest alone is not sufficient. The platform
strings are caller-supplied and must be checked against the real runtime.
Path syntax admission here does not establish
any of these facts. No concurrent operator mutation is permitted.

The supervisor must own both the process and its private QMP endpoint from spawn
through a successful kernel reap. It must not expose a participant endpoint while
ownership is uncertain. Failure fences admission before termination; a failed or
uncertain wait keeps capacity occupied, never retryable as a no-effect rejection.
An ordinary PID number, QMP success response, or closed socket is not reap proof.

On Darwin a controller's death does not automatically terminate its QEMU child.
A socket-liveness helper alone does not close the helper-SIGKILL/crash gap. A
qualified supervisor-death containment mechanism remains a blocking requirement;
no process launcher is implemented here. Do not substitute the development Python
probe's kill/wait path or enable the existing Linux runtime on macOS.

## Fixed initial invocation

QEMU `virt,accel=hvf`, CPU `host`, 512 MiB/one vCPU; paused at start; no NIC,
serial, monitor console, default devices or default configuration. QMP alone uses
an inherited supervisor-owned FD 3. The only disk is an explicitly typed qcow2
image supplied by the trusted asset owner. No snapshot flag or implicit temporary
image creation is requested. This is a design constraint, NOT an alternate route
for the previously denied VM operation. This package must not be used to retry it.

The guest may start only after provenance, immutable ownership, shutdown and quota
qualification. Adding network/session/seed devices requires a separate audited
contract. No arbitrary QEMU arguments are accepted. No host share is introduced.
QEMU/HVF are trusted computing base, not evidence of equivalence to Linux seccomp.

QEMU reference for these option semantics:
https://www.qemu.org/docs/master/system/invocation.html
Existing development probe observations establish only that HVF/ARM can boot a
trusted guest on this Mac; they do not qualify this new plan or hostile guests.

## Verification status

The unit tests exercise platform/path/UUID admission, fixed argument generation,
empty environment and defensive copies. They do not run QEMU or inspect files.
Actual process supervision, descriptor pinning, private QMP ownership, quotas,
parent-death containment, image validation and Battle E2E remain unimplemented.
The existing production unsupported-platform gates and Host clock/FD contracts
are unchanged. No runtime capability is advertised.
