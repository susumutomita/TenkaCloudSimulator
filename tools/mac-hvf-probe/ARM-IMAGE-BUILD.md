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
