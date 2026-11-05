# Azure Properties typed-target source model

`AzurePropertiesTarget.tla` is registered in the candidate's formal gate, with
seven required invariants and six behavioral controls. A pinned scoped execution
passed SANY, the baseline and all six expected counterexamples; full combined
immutable 193-case validation and hosted CI remain separate requirements. This
is not implementation refinement, native API success or Azure portal parity.

## Concrete correspondence

Candidate source: `tools/console/src/azure-blob.jsx`, derived from main `ce622018`,
SHA-256 `3b4079dbc0c42a0d778579c77fdddde28c04016e49e23e48d2fa5454d3bce558`.

- `showProperties` lines84–89 accepts only `container` or `blob`, refuses the
  wrong current listing level, and captures `kind`, `row.name` and the selected
  blob's parent `container`. Container HEAD uses `containerPath(row.name)` with
  `restype=container`; Blob HEAD uses `blobPath(container,row.name)`.
- Lines94–101 construct kind/name, ETag, modified time and metadata for both
  kinds; **only Blob** gets size, blob type and content type. The model's field
  sets denote displayed property names, not actual header values or parsers.
- Line102 publishes only through the existing current-request predicate. The
  existing `AzurePropertiesEpoch` model separately addresses captured lifecycle,
  epoch, abort, Close and unmount. This small model assumes that predicate already
  admitted a current request; it does not replace or extend the old nine
  invariants/five controls or their finite bounds.
- Line189 additionally checks returned detail kind against the property pane kind
  and renders Blob-only fields conditionally. `Visible` records that expression;
  it is not claimed as an independently tested safety invariant. A dropped-render-
  guard control is deliberately omitted: ordinary current-source completion
  cannot independently produce its mismatch without an invented fixture.

A complete target is **(kind, parent-container, name)**. Parent is `none` for a
container and the selected container for a blob. Four literal targets include a
container named `same`, a blob named `same` in `same`, and the same blob name in
`other`. Thus bare names cannot identify resources. Account and authenticated
subject are fixed by existing root component identity, not invented in this model;
parent container is not put into that component identity.

The model's `detail.provenance` is a **ghost of captured URL/closure identity**.
The actual JavaScript detail has no `parent` field or extra parent-equality guard.
The model must not be interpreted as adding either. A successful parsed native
response is assumed to belong to that captured request, as in ordinary fetch;
transport authenticity and authorization remain independent acceptance checks.

## Finite contract and controls

No constants need broadening: four targets, one request at a time, four phases,
one success/failure reply, finite field sets. Repeated Open/Close cycles reuse
finite state; there is no unbounded history or fairness/liveness assertion.
`ExpectedRoute` is an independent literal carrier table; it does not call the
route construction being checked. The carrier distinguishes container path,
blob tail and `restype`, without pretending to parse real URLs/percent encodings.

| Invariant | Contract |
|---|---|
| TypeOK | finite state domains, including the alternate values needed for semantic controls |
| NativeHeadOnly | issued request remains HEAD, never GET/object body |
| RequestIdentityExact | ghost capture monitor retains kind, parent and name; not an actual JavaScript field or independent runtime guard |
| RouteMatchesTarget | actual symbolic route matches the independent literal table |
| DistinctTargetCarriers | complete finite route carriers do not alias |
| DetailCapturedIdentity | displayed detail's kind/name and ghost provenance belong to the captured request |
| FieldShapeExact | container has common fields only; blob adds exactly its three blob fields |

The registry requires six literal, count-one edits: `RequestGET`,
`WrongKindRoute`, `DropBlobParent`, `WrongRenderedName`,
`ContainerGetsBlobFields`, `BlobOmitsFields`. Each must reach a named semantic
invariant with exit12 and a real behavior trace. Type errors, parse failures,
timeouts or lack of a positive baseline are failures, not successful controls.
`WrongRenderedName` changes the actual completion name, corresponding to JavaScript
`detail.name=row.name`, and must violate `DetailCapturedIdentity`. A prior proposed
`BareNameIdentity` changed only a ghost monitor and was removed before execution;
it is not behavioral source sensitivity. `DropBlobParent` must violate
`DistinctTargetCarriers`: the two same-name blobs then collide globally, even if
the particular issued request happens to target a correctly routed container.
The CFG checks `DistinctTargetCarriers` before `RouteMatchesTarget` so this
expected oracle does not depend on TLC successor enumeration order. Both
properties remain checked; `WrongKindRoute` stays injective and must instead
violate `RouteMatchesTarget`.
The scoped run used pinned TLC 1.7.4, model SHA-256
`930838baf67b7d31638ae8236588e5766c1843f242e9f145b2b99f5f06183639`
and CFG SHA-256
`2c8d957a0b530dc8f84889770cb2a62eaeed0ee17acc1b86d82d621b02df5bcb`.
The baseline generated 85 states / 20 distinct states and exited 0. All six
controls exited 12 with the declared invariant and a real behavior trace. An
earlier SANY attempt failed on an ambiguous adjacent `=<<` token before TLC;
only spacing was corrected, and that failed attempt is not counted as a pass.
This is seven scoped executions, not the full candidate's 193 executions.

## Exclusions and evidence status

This model does not establish real HEAD/restype routing, HTTP header shape,
malformed UTF-8 handling, numeric bounds, metadata limits, URL encoding, native
403/404/500 handling, SQLite rollback, credential/subject isolation, SDK fidelity,
React scheduling, stale callback suppression, compiler correctness or Go/SQL/JS
refinement. Existing native/browser tests must verify those paths independently.
No real cloud, authenticated Azure portal or resource creation was used.
