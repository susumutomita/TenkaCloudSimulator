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
