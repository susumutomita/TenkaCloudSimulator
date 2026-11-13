# Apple Silicon empty HVF development probe

This is not a Battle runtime or a launcher for participant workloads. It starts
only the installed Homebrew QEMU ARM executable as a paused, empty `virt` machine
with HVF mandatory, no disk/image/kernel, no NIC, no display and no host shares.
QMP uses anonymous process pipes, not a network listener. It checks prelaunch state,
requests quit and waits for its exact child. Timeout or interruption kills only
that child. There is no fallback, installation, Lima/Colima modification or process
adoption. Python is a developer probe dependency, not a simulator runtime change.

```sh
python3 -m unittest discover -s tools/mac-hvf-probe -v
python3 tools/mac-hvf-probe/probe.py
```

2026-11-13: on the connected Darwin ARM64 Mac with QEMU 11.1.3, six validator tests
passed and the real probe returned prelaunch/running=false, host-qmp-quit SHUTDOWN,
exit 0 and a reaped child. `battleReady` and `guestBooted` are deliberately false.
No actual guest code, boot image, filesystem/network isolation, authenticated
transport, parent-crash cleanup or Battle lifecycle has been tested. A paused
machine does not establish successful guest execution or complete containment.

Installed Lima templates describe builtin SLIRP networking even when `networks`
is empty, port-22 forwarding exceptions and fallback forwarding. `plain` disables
mount/forwarding conveniences but is not evidence of no guest egress. Therefore
this probe does not launch a default Lima VM. Next work needs a pinned guest boot
artifact, explicit no-network transport and trusted grading/control separation.
QEMU TCG is not a security boundary:
https://www.qemu.org/docs/master/system/security.html

The prior cloud contract is a separate local branch/commit, not assumed present
here. Full repository gates and real Battle acceptance remain pending.

Follow-up lifecycle check: cleanup now uses a two-second wait on the exact owned
child, not unbounded `communicate` waiting for pipe EOF. Missing termination proof
raises `TeardownUncertain`; it cannot produce the successful result. Nine tests
pass, including a real sleeping child timed out/killed/reaped while an unrelated
child remains running. Keyboard interruption and failed-wait outcomes are mocked,
not real OS signal/permission experiments. The empty HVF probe also passes again.
This still does not supervise parent crash or prove cleanup of a booted guest.

## Trusted ARM64 initramfs boot probe (not Battle)

`boot_probe.py` verifies two fixed SHA256 values and boots only the official
Alpine 3.22.8 aarch64 virt kernel/initramfs via HVF with no NIC, disk or share.
It uses anonymous serial pipes; there is no externally listening console.
It runs fixed BusyBox uname/poweroff commands in the trusted guest. Output is
limited to 1 MiB and boot to 30 seconds, followed by exact-child bounded teardown.
The environment is empty. This is not for participant code or arbitrary images.

Source archive and independently fetched published checksum (HTTPS publisher
checksum checked; OpenPGP signature NOT verified):

- https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/aarch64/alpine-netboot-3.22.8-aarch64.tar.gz
- https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/aarch64/alpine-netboot-3.22.8-aarch64.tar.gz.sha256
- Archive SHA256: `619d568696faf045b63b459259ebe4c347553320a0217a1658e5d277c3d3e1d1`.
- `boot/vmlinuz-virt`: `ebf299aa6829b15166008db24b076ac0a8b3e778c72ed66be488742aaf05d741`.
- `boot/initramfs-virt`: `0f7da92ca19e6b87b8f0a250e3ce84cc8335c16b2369b18d96f68cc486d638b8`.

Verify the archive checksum first. Extract only the two named regular files into
an operator-controlled private directory as `vmlinuz-virt` and `initramfs-virt`.
Do not modify them concurrently: the development probe checks hashes before QEMU
opens paths; it is not a race-proof immutable bundle loader. No artifact bytes
are redistributed in this change.

```sh
python3 tools/mac-hvf-probe/boot_probe.py VERIFIED_ARTIFACT_DIRECTORY
```

2026-11-13 Mac actual result: Linux 6.12.115-0-virt entered `/bin/sh`; BusyBox
returned `TCS_ARCH_aarch64`, then kernel `reboot: Power down`, QEMU exited 0 and
was reaped. End-to-end process duration was 0.655 seconds on this single run,
not a performance benchmark. First two attempts timed out and the exact child
was stopped: initial prompt expectation differed, then uname/poweroff symlinks
were absent in the initramfs. The final uses the observed prompt and explicit
`/bin/busybox` applets. Negative tests cover digest mismatch and symlink refusal.

This proves trusted Linux ARM64 guest execution on this Mac, NOT original Battle,
Amazon Linux compatibility, inner TCG safety, multi-team isolation, clock bootstrap,
network/IMDS/SSM, persistent VM ownership or participant transport. Those remain
unimplemented/unverified; simulator and Host runtime gates are unchanged.

Boot execution regression coverage now checks the actual launch argv and empty
environment, serial-output budget, deadline, missing success markers, nonzero exit
and wrong-platform refusal through mocked process/clock/serial boundaries. All
17 tests pass; the real pinned guest boot/poweroff probe was rerun successfully.
These injected failure tests are not real guest quota/OS-isolation tests. Artifact
path TOCTOU and the unpinned installed QEMU binary remain development limitations;
this change does not promote the probe to a production runtime.

## Existing relay prerequisites on ARM (measurement, not a replacement relay)

`boot_probe.py VERIFIED_ARTIFACT_DIRECTORY --identity` adds a fixed test UUID
and a fixed `org.tenkacloud.session.0` virtserialport connected to a null chardev,
not a socket/listener or arbitrary destination. The trusted guest mounts its own
sysfs and inspects the existing relay's exact DMI/port prerequisites. It does not
create an image marker, install a relay, substitute identities or change gates.

Actual Mac/Alpine result: sysfs reported `org.tenkacloud.session.0`, but
`/sys/class/dmi/id/product_uuid` was absent and the named character-device path
required by the relay was absent. Output reports `matchingDMIUUID:false`,
`namedVirtioPort:true`, `namedCharacterDevice:false` separately from guest boot.
Thus direct kernel/initramfs boot is NOT a qualified relay image. The port exists
in sysfs but this does not prove a usable PTY or byte exchange. No stream bytes
were sent through the null port.

The reviewed repository relay source and overlay were integrated by #465; they
are unchanged here. Their current contract also needs bash, systemd, an image
marker and real TCS1 Hello UUID equality. A proper ARM guest image must establish
DMI/firmware availability and udev device naming without weakening that contract.
The probe does not claim which firmware/image combination supplies them.

## Pinned EDK2 firmware measurement

`boot_probe.py VERIFIED_ARTIFACT_DIRECTORY --firmware` additionally loads only
`edk2-aarch64-code.fd` with SHA256
`47765fe344818cbc464b1c14ae658fb4b854f5c2ceffa982411731eb4865594d`.
The expected source archive was checked against Homebrew's official formula API:
https://formulae.brew.sh/api/formula/qemu.json

- Official source: https://download.qemu.org/qemu-11.1.3.tar.xz
- Archive SHA256: `5a8289c7940dc77d3873c81a6c29ba3afe1446fc9d36e77fa1658cdbecb84b22`.
- Decompressed member: `qemu-11.1.3/pc-bios/edk2-aarch64-code.fd.bz2`.
- Bytes match the installed Homebrew firmware exactly. No installation or system
  configuration was changed. Publisher source checksum verified; detached
  signatures were not verified. No firmware is redistributed by this change.

Actual ARM64 HVF firmware boot now exposes the exact supplied test UUID through
`/sys/class/dmi/id/product_uuid` and the expected virtio port name. Both observations
are true; named character-device observation remains false in this initramfs.
Guest uname and orderly poweroff/reap still pass (one run: 1.662 seconds).
This narrows the next image prerequisite to real device naming plus the existing
bash/systemd/relay/marker requirements. It does not prove relay handshake, byte
transport or Battle execution. Null chardev, no NIC/disk/share and empty environment
remain unchanged. Filename/hash checks retain the private-directory TOCTOU limit.

## Kernel device and private byte-path probe

`--device` includes firmware/identity checks and replaces the null backend with
an anonymous socketpair. Only the selected socket FD is inherited by QEMU; no
filesystem socket or TCP/UDP listener is created. Both host-owned endpoints are
closed after the probe, including launch-failure handling. The trusted guest
mounts its own devtmpfs and tests the observed kernel device `/dev/vport0p1`, then
writes a fixed short diagnostic string. The host checks those bytes from the
private channel independently of the serial log. This is one-way byte-path
measurement, not TCS1, an authenticated relay or participant transport.

Actual Mac result: `kernelCharacterDevice:true`,
`privateChannelBytesObserved:true`, matching DMI UUID and port name true, but
`namedCharacterDevice:false`. Guest poweroff and exact-child reap succeeded.
22 tests pass, including mocked socket ownership/closure and wrong-byte refusal.
The fixed vport number is specific to this one-device probe, never a replacement
for the production relay's fixed named-device check.

The canonical systemd rule explains the missing image setup:
https://github.com/systemd/systemd/blob/v258/rules.d/50-udev-default.rules.in#L11

It creates `virtio-ports/$attr{name}` symlinks for the matching virtio-ports
subsystem. The verified Alpine initramfs instead includes mdev configuration and
has not run that udev rule. No manual symlink, fake identity or substitute relay
was added. A qualified ARM image still needs its normal device manager, bash,
systemd, pinned relay/service/marker and actual TCS1 handshake/PTY verification.
