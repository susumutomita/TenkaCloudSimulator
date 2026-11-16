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

## Supervisor death: Darwin candidate and acceptance boundary

The Mac's installed Apple manual pages (`man launchd.plist`, `man launchctl`,
`man kqueue`, read 2026-11-13) document:

- `AbandonProcessGroup`: on job death launchd kills remaining processes with the
  same process-group ID; setting true disables that behavior.
- `ExitTimeOut`: seconds between TERM and KILL when stopping a job. Zero means
  infinity, so the candidate uses 2, never zero.
- `KeepAlive` may relaunch a crashed job; the candidate explicitly disables it.
- `bootstrap`/`bootout` install/remove service definitions in a launchd domain.
- kqueue `NOTE_EXIT` observes exit; it does not promise termination of descendants.

`ProposeSupervisor` generates only an inactive job definition, with RunAtLoad,
KeepAlive and AbandonProcessGroup false. It neither writes a plist nor invokes
launchctl. There is no supervisor entrypoint yet. Do not bootstrap it. The label
is a proposed generation identifier, not a durable uniqueness registry.

The next real acceptance test, after an actual supervisor exists and the transient
service registration is authorized, must use only a benign owned child (not QEMU)
and an independent observer: controller disconnect, supervisor normal exit,
supervisor SIGKILL, child ignoring TERM, no automatic restart, exact-generation
stop, and service removal. Verify child disappearance and distinguish launchd
cleanup from a successful reap by the original parent. No such test has run.
A child deliberately leaving the group is a required negative control: the manual
provides no group-exit containment guarantee. The candidate must remain disabled
if that stronger boundary is required and no other qualified mechanism supplies it.

For real use, a trusted supervisor would own the controller liveness endpoint,
validate assets, spawn QEMU without setpgid/setsid/daemonization, and fence/stop
on controller loss. launchd supplies the *candidate* supervisor-death group cleanup.
This depends on trusted QEMU remaining in-group; it does not contain an exploited
QEMU that escapes the group. HVF is not host-side seccomp. Filesystem/descriptor
confinement, quotas, executable identity, authenticated IPC and image integrity
remain independent blocking work. No claim of Linux safety equivalence follows.

Plist tests check XML escaping, no auto-launch/restart/demand triggers, finite stop
interval, no group abandonment, and rejected malformed inputs. They are not
launchd execution, registration permission, crash-containment or Battle evidence.

## Independent writable image preflight (still no runtime activation)

`OpenIndependentImage` retains a read-only FD for a private candidate copy after
checking owner, mode 0600, link count 1, distinct inode from an already-open trusted
original, canonical non-symlink pathname, private parent directory and exact
SHA-256. Regular-file metadata is checked again after bounded hashing. It opens
with no-follow/nonblocking/close-on-exec flags; it never modifies image bytes.

This preflight has a deliberately narrower format subset than the existing
Linux guestvm validator: QCOW2 v3, 104-byte header or 112-byte header with zero optional fields, refcount order 4, no
backing/encryption/snapshots/feature flags, bounded dimensions, and no extensions
except a bounded feature-name display table. Unknown or external-file extensions
are rejected. The QEMU format reference was read for field/extension semantics:
https://www.qemu.org/docs/master/interop/qcow2.html
The existing Linux validator and invocation are unchanged.

The format check is NOT validation of allocation tables or bootability. Unit
fixtures contain synthetic headers, not runnable guest images. A digest alone is
not publisher provenance; callers must obtain the expected pin from a verified
asset record. The original FD is assumed to be an already-qualified reference.
Mode-bit checks do not audit macOS ACLs or establish a cross-user sandbox.
Same-user concurrent mutations and parent-path replacement are not defended as a
hostile boundary. Retaining an FD pins an inode, not immutable bytes. This API does
not pass the FD into QEMU, replace pathname execution, or qualify writable launch;
that integration must preserve verified identity without reopening by name.
Runtime/VM/launchd gates remain disabled; no real image or VM has been run here.

A read-only header observation of the prepared official Debian ARM original found
v3/112-byte header, 3 GiB virtual size and zero feature flags. The 112-byte case is
therefore accepted only with zero compression type/padding, as documented by QEMU.
This observation is not full original/candidate validation or image boot evidence.

## QEMU native parent-exit option: examined, not admitted

The [QEMU invocation manual](https://www.qemu.org/docs/master/system/invocation.html)
documents `-run-with exit-with-parent=on` for macOS. The previously obtained
QEMU 11.1.3 source archive was read without executing it. Its
`system/exit-with-parent.c` has SHA-256
`a8ac47aad5191b0fd2788c629593f7449acdea65ec22b5b680348eb487b6ef70`.
The macOS implementation creates a detached thread; that thread obtains getppid,
registers kqueue NOTE_EXIT, and requests QEMU shutdown when an event arrives.
It returns after logging a kqueue/registration error without failing startup.
There is no readiness acknowledgement to the spawning parent. Parent exit before
that thread captures/registers its identity therefore has no demonstrated safe
handshake. `system/vl.c` calls set_exit_with_parent without checking its return.
`qemu_system_killed` requests poweroff and wakes the event loop; it does not perform
an unconditional kernel SIGKILL of the VM process.

This is not equivalent to the repository's Linux creator-thread-death SIGKILL
contract for a single trusted child. That Linux contract itself does NOT promise
arbitrary descendant containment or progress while the owner is merely hung.
The distinction avoids demanding stronger guarantees from Mac while still
rejecting the unchecked monitor-start and asynchronous-stop gaps.

No flag was added to the admitted plan, and no QEMU/launchd experiment was run.
Any future use requires a fail-closed parent-identity/monitor-ready handshake,
startup-failure fencing, bounded stop with independently verified owned exit,
and the still-missing executable/descriptor admission. Observing this option in
help or documentation is not runtime qualification. An alternative mechanism
must not be used to retry a previously denied operation.
