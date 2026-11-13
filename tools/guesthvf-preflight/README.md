# Pinned ARM image preflight (no VM)

This developer command reads the previously downloaded official Debian ARM image,
creates one temporary private independent copy, verifies the fixed original SHA512,
runs `guesthvf.OpenIndependentImage` with the copied bytes' SHA256, closes its FD,
and removes the test copy. It performs no QEMU, launchctl, network, registration,
image building, original modification, or Battle operation.

The source artifact and publisher checksum previously verified for this input:
https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2
https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS
These URLs can change; the command refuses any content not matching its fixed pin.
The digest is not a digital-signature claim. Roughly 338 MB scratch space is needed.

On the Mac, this actual file-copy/FD validation completed successfully on 2026-11-13:

```
result: pinned-arm-image-preflight-pass
copySha256: 875c27442df61651297c56e9b92b017c15346c6a2b20942d03680cc37a42058b
copyRemoved: true
battleReady: false
```

This confirms the prepared image fits the limited header/ownership checker after
copying. It does not validate allocation tables, a boot, the relay overlay, private
transport, stop guarantees, guest containment or E2E. The launchd failure remains
unresolved, and this command is not an alternate execution route around it.

`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go run ./tools/guesthvf-preflight
--original <canonical pinned original> --scratch <operator-owned directory>`

Only trusted operator-controlled inputs/storage are supported; no concurrent
mutation/replacement of the original or scratch directory is allowed. A cleanup
error makes the command fail; retain the failed copy for operator diagnosis rather
than treating failure as a usable runtime image. Automated VM launch stays disabled.

Small synthetic QCOW2 tests now cover successful independent copy admission,
wrong-pin rejection after copying, and pinned external-backing rejection, checking
scratch cleanup and unchanged original bytes in each case. The CLI still supplies
only its fixed official size/hash; synthetic fixtures are rejected through that
entry point. These unit tests do not qualify the official image or prove VM boot.
