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
