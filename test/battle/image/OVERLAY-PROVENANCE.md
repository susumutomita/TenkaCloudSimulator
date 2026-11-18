# Session overlay pin provenance

`session-overlay-provenance.json` binds the reviewed Go1.26.11 Linux/amd64,
CGO-disabled, `-trimpath -buildvcs=false` relay build to its source inputs,
binary SHA256 and deterministic tar SHA256. The builder validates those input
hashes before downloading an image, then checks both the built binary and tar.
The guest retains the independent `OVERLAY_SHA256` check before extracting it.

The [comparison run](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/44367447160)
uses immutable diagnostic workflow d1250bcfffda542f368c174282aaebb9ff018ce5 and
source revisions recorded in its artifact. With the same compiler and flags:

| Source | Relay SHA256 | Overlay SHA256 |
| --- | --- | --- |
| main94a0ae2 and lock6233bc7 | 931a371af27390aec9f64aa06ff5e3b90ada70efecfd8354dcd4bcb1a8eabad5 | 22993580ff7a55b918bd062269a192dc647172e6dd906e6fd93bb7d15e4528eb |
| main241cb37 and lockdad073d | 26fb67b5b69a602f8d25f366866d245c03199de88c4bd3c964b34cb59607595b | f553a660f9cd672dc30867b90f28c283e565ad9bf7ee98a30a255e1fc3a789d9 |

The lock introduction does not change either artifact. The gVisor repair in
#492 requires the root module's `go` directive to rise from1.26.0 to1.26.3.
Changing only that directive back in a diagnostic copy of main241cb37 reproduced
the old relay binary exactly, despite retaining the new gVisor version. The
relay itself imports x/sys v0.44.0 and guestsession, not gVisor; its build ID
changes with the directive. The binary build-info dependency list, unit and
license hashes are otherwise unchanged. Lowering the directive is not a fix.

For a future input or compiler update, reproduce the build at an immutable
revision in a separate worktree, use `scripts/guest-image/session-overlay.py`
with the observed binary/unit/license digests, and independently review the
resulting binary, tar, source hashes and compiler. Update the receipt and
user-data pin together, then run the negative helper tests and the complete
three-host Battle workflow. Do not copy an unexplained failure hash into the
pin, remove build IDs, disable the comparison, or claim a pre-boot mismatch
proves a successful guest rehearsal. The diagnostic run builds but never
executes a guest binary; actual VM evidence comes from the Battle jobs.
