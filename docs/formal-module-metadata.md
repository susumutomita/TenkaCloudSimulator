# Narrow Go module annotation classification

This proposal extends formal applicability classification on exact base
`ebf5b5d` without exempting general dependency changes or binding them to an
unrelated token-state invariant. It is not a Go parser or a proof of an entire
dependency graph. A caller still supplies an explicit, digest-bound applicability
record, and human dependency/semantic review remains required.

The [official Go Modules Reference, require directive](https://go.dev/ref/mod#go-mod-file-require)
was checked2026-11-04; latest upstream document revision is unknown. A require
entry sets a minimum module version. The `// indirect` annotation describes
whether main-module packages directly import that module. Go also permits
individual and grouped require declarations. This supports recognizing the
annotation/grouping change independently of application state properties; it
does not justify ignoring new dependencies, versions, directives or runtime code.

## Mechanically accepted subset

Only `go.mod` may use this exception, and only when its record explicitly says
`build-metadata` with an explanation and no model claims. The path remains absent
from the general nonformal allowlist. Before and after are read from the stated
immutable base and HEAD, never from staged or working-tree module contents.

Both go.mod and go.sum must exist as ordinary mode100644 Git blobs in both
commits. go.sum bytes must be identical. Each go.mod is at most64KiB, ASCII, LF
terminated, with only tabs/newlines as control whitespace. A fixed identical
header contains exactly one unquoted `module` followed by one `go`, optionally
one `toolchain` directive. Header bytes, including its blank lines, must match.

The rest contains only nonempty individual/grouped `require` declarations and
blank lines. Each module path appears exactly once; stable `vX.Y.Z` releases only.
The unique `(module,version)` mapping must be identical. Require ordering,
grouping, indentation and exact optional `// indirect` annotations may differ.
No other comment is accepted. The recognizer rejects quotes, escapes, CRLF,
non-ASCII, pseudo/prerelease versions, nested/empty/incomplete blocks, duplicates,
unknown directives and all syntax outside this deliberately small grammar—even
when valid in the full Go grammar. Supporting those later needs explicit review.

Consequently any add/remove/version/module/go/toolchain change, replace/exclude/
retract/tool/ignore/godebug directive, semantic comment such as `//go:debug`, or
changed/missing/symlink/executable-mode go.sum/go.mod fails this exception. Existing
current/prior model scope still requires a modeled disposition. Unknown runtime
Go source cannot inherit the metadata exception. Existing clean-HEAD guards reject
dirty/index/untracked inputs before and after applicability validation.

## Integration and evidence limits

`scripts/formal_module_metadata.py` is new formal-policy infrastructure, not a
runtime or general scripts exemption. `formal_applicability.py` invokes it only
for the explicit go.mod case. Its own source is hash-bound as a scripts input.
The existing workflow already runs `scripts/test-formal-applicability.py`; new
real-Git fixtures exercise accepted annotation/group changes and rejection of
semantic/unknown syntax, checksum, dirty/index and unrelated runtime changes.
No workflow, settings, Go module file, application code or finite model changes.

At source handoff, the new fixtures are **UNRUN** pending root's CPU window;
syntax/source construction and `git diff --check` are not execution evidence.
The final committed applicability record is based on full ebf5b5d SHA and labels
only actual helper/checker/test changes as formal-infrastructure and this text
as documentation. Exact record validation and existing negative controls remain
required before integration. Startup's real runtime source changes still need
their own model/property correspondence and required whole CI checks.
