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
