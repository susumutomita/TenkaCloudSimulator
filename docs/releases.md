# Standalone binary releases

Pushing an authorized `vX.Y.Z` tag runs the complete existing CI workflow
(including browser/SDK, coverage, security and bounded formal checks), builds
artifacts, then publishes them to a GitHub Release. Prerelease suffixes produce
prereleases. The separate publication job alone has `contents: write`. Publication also
requires the reusable immutable-head Formal validation workflow, including its
exact applicability and negative-control gates.
Manual workflow runs build reviewable candidates without publishing. Existing
releases are never overwritten; a retry after publication must be reviewed.
The initial proposed stable version is `v0.1.0`; it is not a published release.
Do not push a release tag without explicit authorization for that version/source.

## Asset contract

Each release contains these uncompressed executable files:

- `tenkacloud-simulator_linux_amd64`
- `tenkacloud-simulator_linux_arm64`
- `tenkacloud-simulator_darwin_amd64`
- `tenkacloud-simulator_darwin_arm64`
- `SHA256SUMS` (SHA-256, two spaces, basename; includes all four binaries and BUILDINFO)
- `BUILDINFO.json` (full source commit, version, toolchain and build options)

Consumers resolve a compatible stable release, fetch its checksum file and
matching binary, verify SHA-256 before execution, and apply executable permissions.
A host may retain the selected version/checksums in its local lock and update it
explicitly; simulator releases do not require host-source version/hash changes.
Checksums detect corruption, not a compromised publisher. No artifact here is
signed or notarized. Darwin execution and Gatekeeper behavior require actual
Mac testing; cross-compilation does not validate them.

## Host compatibility contract

BUILDINFO declares integer `host_protocol: 1`. It describes the launcher contract:
`--listen`, `--state` and `--token-file`, numeric loopback/local token authority,
health/capability control APIs used by the host, and graceful SIGTERM shutdown.
An incompatible launcher/control-API change must bump this protocol; a release
version change alone does not require a bump. Hosts reject unknown protocols
before running a downloaded binary, then still perform their live capability
preflight. This metadata declaration is not proof of full service compatibility.

This protocol does not guarantee SQLite downgrade or rollback compatibility.
Hosts must not silently execute an older binary after a newer binary has touched
persistent state. The same-release checksum manifest detects corruption or
mismatches, not a compromised publisher replacing both binary and manifest;
locally locked hashes detect later same-version replacement.

## Prepare from a fixed source

Use Python 3.11+ with tarfile's data filter and Go 1.26.11. The repository embeds
its runtime/UI assets. Building needs the Go modules cached or registry access;
running the binary needs no Go, Python, Node, Docker or external database.

```sh
python3 scripts/build-release.py --ref <full-commit-or-tag> --version v0.1.0 --output dist/release
(cd dist/release && sha256sum --check SHA256SUMS)
```

The script extracts a Git archive of the resolved commit, so uncommitted local
source changes never enter artifacts. It verifies embedded assets and module
checksums and builds with CGO disabled. Workspace/user Go configuration, ambient
build flags and automatic toolchain selection are disabled; the pinned toolchain
is checked inside the extracted source. It refuses nonempty output directories.
Failed builds leave partial candidates that must not be published.

Before publication, run `make before-commit` and required repository acceptance
gates on the exact release source, inspect the artifact BUILDINFO and checksums,
and smoke-test each platform where possible. Review the normal CI and formal
workflow for the exact source commit; the release workflow reuses CI rather than replacing its gates. Tag an approved commit without moving an existing tag. Publication checks the
remote tag (including annotated tags) against the built source before draft
creation and again before making it public. This detects observed tag movement;
repository tag protections remain an operator responsibility.
Upload the six files together to the matching GitHub Release only after approval.
Do not substitute a CI artifact URL for an official release asset URL or claim
that a local candidate has been published.
