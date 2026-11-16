# ARM relay image build boundary (not executed)

This is a preparation contract, not a runnable or qualified Mac image builder.
The existing AL2023/amd64 recipe is not silently reclassified as Debian/ARM.
The operator's prior probe execution does not authorize a different image build
or automated VM retry. Keep the known denied launch stopped.

## Inputs and ownership

- Use the exact official Debian ARM64 qcow2 pinned by image_probe.py. Preserve it
  unchanged. A new independent writable copy is required; no live image or disk
  attached to another tool may be edited.
- Verify the overlay SHA256 from arm-relay-preparation.json and each source
  digest. The current overlay is diagnostic-only: its compiler is Go 1.26.2,
  and exact release-toolchain qualification has not occurred.
- Existing relay, service and TCS1 codec remain unchanged. Do not install through
  Battle UserData, change its problem script, add a replacement protocol, or
  manufacture a successful Hello/Ready using marker files.

## Required builder qualification before execution

The official virt-customize manual documents `--no-network` (not
`--network=off`), explicit `--format qcow2`, and `--tar-in TAR:/`.
It modifies the supplied image in place. Do not run on the pinned original.
No root host execution is necessary according to the manual.

https://libguestfs.org/virt-customize.1.html
https://libguestfs.org/guestfs-faq.1.html

libguestfs uses an appliance; this is VM/image execution, not an ordinary tar
copy. No installed virt-customize or guestfish was found on the selected Mac.
Do not install tools, select a different executor, enable networking or launch a
wrapper to route around the existing execution restriction. Before a build,
establish an explicitly approved builder environment, pinned builder/appliance
provenance and a fresh output disk. No such environment is claimed here.

## Required output verification

After an authorized build, record input/output disk digests, overlay digest,
compiler/tool versions and builder log. With a qualified read-only inspector,
verify root ownership and fixed paths for relay, service, enablement symlink,
license and marker. Check the ELF is ARM64 and the guest has bash, PTY, systemd
and the correct udev naming rule. No service execution is inferred from files.

A new reviewed launcher must pin the resulting image digest before any test.
The existing image_probe.py must continue rejecting this modified disk: its
hash pin is for the unmodified publisher image. Do not edit its pin merely to
force a newly generated image through without review.

## Runtime acceptance still required

1. Operator-owned private channel receives the actual existing TCS1 Hello and
   checks its UUID against the exact VM owner.
2. Existing protocol Start receives actual PTY Ready, not a fabricated response.
3. A fixed harmless command produces bounded output and actual Exit.
4. Wrong UUID, malformed frames, closure, quota and timeout fail closed and reap
   all owned processes. A shell Exit is not proof of VM teardown or isolation.
5. Only after image and lifecycle qualification can the actual Battle
   initialize/operate/grade/cleanup be attempted. This file is not that result.

## Non-executing builder preparation implementation

`builder_plan.py` is the first independent implementation increment. It validates
pinned input image/overlay hashes, requires a new private output directory,
copies the base to an independent inode, rechecks both copies, and writes a
fixed builder-only local NoCloud script. It does not call QEMU, hdiutil, a shell
or an image editor. The current change does not yet provide an executable VM
builder command to the operator.

The proposed guest script mounts only the read-only `TCSOVERLAY` medium, verifies
the fixed tar digest, applies the reviewed five-file overlay to the guest root,
verifies binary/marker/service link, syncs and powers off. Failure emits a distinct
marker and powers off. It does not start the relay. The eventual launcher must
retain NIC/share/SSH absence, timeout/owned-child cleanup, the fixed wait-online
credential, read-only seed/overlay drives, and write only the new candidate disk.
Do not replace the denied automated launch with this future launcher.

Preparation failures leave an `INCOMPLETE` marker and no success receipt. Never
reuse that directory or promote its disk; no automatic deletion/recovery occurs.
The private-directory/no-concurrent-modification assumption remains necessary:
path checks are not a race-proof hostile-host boundary. Exact binary toolchain,
actual guest installation, cloud-init build-state cleanup for later runtime,
image verification and Hello/Ready/Exit are still unqualified. An installed
file marker alone cannot satisfy any runtime acceptance.

## Operator launcher candidate (not executed)

`builder_launch.py --operator-build PREPARED_DIRECTORY FIRMWARE_DIRECTORY` now
implements the separate, explicit operator action. This is not permission to run
it automatically. It rechecks private-directory/input hashes, refuses prior
attempts/results/ISOs, creates ATTEMPTED before subprocesses, makes two local
read-only ISOs with system hdiutil, and boots only candidate.qcow2 with fixed
HVF/512MiB/one CPU/no NIC/no share/no private relay channel. The original image
is not in its QEMU argument list. It retains the 90-second/4MiB bounds and exact
child kill/wait. Both ISO creation and QEMU receive an empty environment.

A build receipt requires exit zero, the exact installed-files marker, no failure
marker, guest poweroff indication, child wait and final disk digest. It still
says built-not-qualified, battleReady=false, relayExecuted=false and
cloudInitCleanupVerified=false. A receipt alone without complete serial log and
normal operator exit is not acceptance. Any failure retains the candidate and
ATTEMPTED; never resume or reuse it. INCOMPLETE/log writes may themselves fail
on a full disk, so absence of a failure marker never establishes success.

Guest cloud-init may write final-stage state after a script runs. Therefore this
candidate deliberately does not call clean from inside its still-running final
stage and claim a clean reusable image. A separately reviewed offline inspection
and cleanup of builder cloud-init state remains necessary before qualification
or a later relay boot. No runtime image hash was changed to admit the candidate.
Parent crash/SIGKILL remains unsupported; do not force-close the operator
Terminal. No actual ISO creation or VM launch was run for this increment.

Independent-review correction: preparation now records the publisher file's
source device/inode and makes the candidate mode 0600. Launcher requires current
user ownership, no group/other access, one hardlink, and a different inode from
the source receipt. Older prepared directories without sourceIdentity are
rejected, not upgraded implicitly. This is still an operator-private-directory
contract, not protection against concurrent malicious same-user modification.
Serial log persistence now precedes build-result receipt creation. Mock tests
cover successful unqualified receipt, ISO failure, output quota, timeout,
missing marker, spawn failure and hardlinked-source rejection. These mocks do
not prove guest installation or crash-safe storage durability.

## Deferred cleanup candidate (supersedes live-final cleanup limitation)

The proposed builder now queues a fixed, /run-only oneshot after
cloud-final.service. The builder's user-data script returns instead of cleaning
its own active cloud-init state. The separate oneshot checks the final-stage
Result, stops the guest cloud-init-main.service, requires MainPID=0, then invokes
`cloud-init clean --logs --seed --machine-id`, checks absence of instance/seed
state and the uninitialized machine-id, syncs and powers off. No host service is
stopped; the relay is not explicitly started. The oneshot timeout is 30 seconds,
and the outer builder remains limited to 90 seconds. Errors emit the failure
marker or cause absence of the required cleanup marker, so acceptance fails.

Version-matched official source rationale (cloud-init 25.1.4, as seen in the
operator guest log):
- https://github.com/canonical/cloud-init/blob/25.1.4/systemd/cloud-final.service
  describes the final-stage completion shim.
- https://github.com/canonical/cloud-init/blob/25.1.4/systemd/cloud-init-main.service.tmpl
  identifies the separate all-stages process.
- https://github.com/canonical/cloud-init/blob/25.1.4/cloudinit/cmd/clean.py
  implements the selected cleanup flags and uninitialized machine-id.

Launcher additionally requires TCS_BUILD_STATE_CLEAN before a build receipt.
A receipt records cloudInitCleanupObserved=true but still
cloudInitCleanupVerified=false: this code has not run in the pinned guest, and
post-shutdown disk inspection/reboot is not replaced by guest marker output.
The previous claim that no cleanup is attempted describes the earlier candidate,
not this deferred candidate. Runtime, image qualification and Battle remain open.

Review follow-up: cleanup rejects dangling instance/seed symlinks as well as
existing targets. Mock failure injection now confirms missing cleanup marker,
explicit failure marker, and serial-log disk-full cannot publish a build-result
receipt. These remain host-side tests; actual systemd job ordering and final
on-disk cleanup are not yet observed.
