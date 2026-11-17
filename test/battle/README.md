# Local Battle rehearsal on the real VM runtime

`rehearsal.py` runs the **unchanged** `battles/hello-world-battle` problem from a
TenkaCloudChallenge checkout against a Simulator started with
`--battle-runtime-config`: a real QEMU guest boots, the original UserData runs
in it, and the script drives initialize → operate → grade → cleanup through the
Simulator's authenticated HTTP surfaces. No real AWS endpoint is used.

It is an opt-in local rehearsal for Linux and macOS on amd64 or arm64 (on Windows,
use WSL2). The required `ci` job does not run it, because it needs an emulator and
a guest image that are not in this repository. The `battle-hosts` workflow builds
both and runs it on GitHub's macOS (Apple silicon and Intel) and Linux arm64
runners for changes to the VM runtime; Linux/amd64 is the original rehearsal host.

## Quick start

```sh
# macOS: brew install qemu        Ubuntu 24.04 (amd64 or arm64): apt install qemu-system-x86 qemu-utils genisoimage
make build
python3 test/battle/image/build_bundle.py --out /abs/new-bundle
python3 test/battle/rehearsal.py --binary "$PWD/dist/tenkacloud-simulator" \
  --bundle /abs/new-bundle --challenge-root /abs/TenkaCloudChallenge --work /tmp/hwb/r1
```

`build_bundle.py` performs every step under [Guest image](#guest-image) on the
machine that will run the bundle, with the seed tool and builder QEMU arguments
fixed in the script. It needs Go, network access to `cdn.amazonlinux.com` and
about 6 GB of free disk. Keep `--work` short: guest control sockets live below
it, and Unix socket paths are limited to 107 bytes on Linux and 103 on macOS. A
longer path fails `CreateStack` before any guest starts.

## What a pass means, and what it does not

A pass shows, for the binary and bundle named in `report.json`:

- `CreateStack` with the unchanged template launches one guest and returns
  `CREATE_COMPLETE` only after the private guest agent answers.
- The original UserData installs nginx with `dnf`, starts both services and
  reads the instance hostname from the local metadata service.
- A participant assumes the template's own viewer role, opens a Session Manager
  terminal, keeps it attached for more than a minute while only watching output,
  and recovers nginx with `systemctl start nginx`.
- The owned probes return 200 for `/` and `/healthz`, fail for the stopped
  frontend after the catalog's `AWS-RunShellScript` disruption, and recover.
- Sealing the capability, deleting the stack and stopping the Simulator leave no
  guest process, and the Simulator exits 0.

On macOS each emulator also runs under a private supervisor process (the
simulator executable re-entered by `guestprocess`); the cleanup checks require that
no supervisor remains either. QEMU's seccomp `-sandbox` exists only on Linux.

It does **not** show TenkaCloud host or Portal support: the host keeps this
Battle disabled in simulator mode, so this script stands in for the host. It
computes points itself from `metadata.json` (`uptime-flat`, +100 / −100); the
Simulator never calculates points. The probe deadline uses the same machine's
wall clock for both sides, which is not the independent Host clock qualification
a real event needs. The 600-second automatic revert is a host timer and is not
waited for; only its revert command is run. One team is exercised, not two.

## What the bundle must contain

The runtime takes a pinned archive plus manifest (see
[opt-in runtime setup](../../docs/start.md#explicit-battle-runtime-linux-and-macos)).
`pack_bundle.py TREE ARCHIVE MANIFEST bin/qemu-system-x86_64` writes both from a
directory tree. The rehearsal used this tree:

| Path | Source |
| --- | --- |
| `bin/qemu-system-x86_64` | distribution QEMU (Ubuntu 24.04 `qemu-system-x86` 8.2.2) |
| `lib/x86_64-linux-gnu/qemu/accel-tcg-x86_64.so` | same package; this build loads TCG as a module found relative to the emulator |
| `firmware/bios-256k.bin` | `seabios` |
| `firmware/efi-virtio.rom` | `ipxe-qemu`, loaded by the fixed `virtio-net-pci` device |
| `firmware/kvmvapic.bin` | `qemu-system-data` |
| `images/al2023-battle.qcow2` | official AL2023 KVM image, prepared as below |

On macOS `build_bundle.py` takes the emulator from Homebrew's `qemu` and the
firmware from the directories that `qemu-system-x86_64 -L help` lists. It adds a
TCG module only when the emulator was built with loadable modules.

The emulator starts with an empty environment and uses the shared libraries of
the machine it runs on, so build the bundle on the machine that will run it.

## Guest image

Start from the official Amazon Linux 2023 KVM qcow2 and verify its
`SHA256SUMS` and the RSA signature over that file with the published AL2023
key. Work on a copy; keep its qcow2 header as shipped (the runtime accepts a
version 3 header only at its original 104-byte length, which a fresh
`qemu-img convert` does not produce).

The guest has no DNS and no Internet at Battle time, so everything the UserData
installs must already be on the disk as an offline repository. One builder boot
of the copy prepares it:

1. `image/repo_relay.py LOCAL_DIR PROVENANCE.jsonl 8099` on the build machine.
   It relays `cdn.amazonlinux.com` over verified TLS and records the SHA-256 of
   every file it serves. It uses the build machine's system CA trust by default;
   set `SSL_CERT_FILE` to a valid PEM CA bundle when an explicit trust source is
   needed. A missing or invalid explicit CA file stops the relay; certificate and
   hostname verification remain enabled. RPM signatures are still checked inside
   the guest.
2. Build `guest/ssm-relay` and package it with
   `scripts/guest-image/session-overlay.py`; put the tar in `LOCAL_DIR` and its
   digest in `image/builder-user-data.sh`.
3. Boot the copy once with QEMU user networking and a NoCloud seed whose
   `user-data` is `image/builder-user-data.sh` (volume label `cidata`, written by
   `genisoimage` on Linux or `hdiutil makehybrid` on macOS; see
   `builder_command` in `image/build_bundle.py` for the QEMU arguments). The script installs
   `qemu-guest-agent`, stores nginx and its missing dependencies in a
   signature-checked `file://` repository, disables the network repositories,
   installs the relay overlay, checks that `dnf` resolves nginx offline and that
   nginx is **not** preinstalled, cleans cloud-init state and powers off.

The disk digest in the packed manifest is the session image pin the runtime
requires. This procedure establishes integrity of the inputs it names, not an
independent review of the resulting image.

## Run

```sh
make build
python3 test/battle/rehearsal.py --binary "$PWD/dist/tenkacloud-simulator" \
  --bundle /abs/bundle --challenge-root /abs/TenkaCloudChallenge --work /abs/new-run-dir
```

Python needs the pinned `boto3` from `scripts/requirements-test.txt`. The run
takes about six minutes under TCG and writes `report.json`, `simulator.log`
and `runtime.json` to the work directory. The exit status is nonzero when any
check fails; the failed check stays in the report.

## After the run

Deleting the stack ends the guest process, but the runtime keeps that guest's
directory (its full disk copy, seed and stale socket files) under
`WORK/rt`; there is no removal API. Remove the work directory yourself once the
Simulator has exited 0. If it exited nonzero, keep the directory: the Simulator
is reporting cleanup it could not confirm.
