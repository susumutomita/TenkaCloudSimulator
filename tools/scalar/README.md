# Local Scalar rebuild (development only)

The simulator distributes a locally rebuilt Scalar 1.76.4 API reference, not the original upstream standalone bundle. The optional `@replit/codemirror-css-color-picker` is **excluded from every esbuild input**. `no-color-picker.js` is original simulator code supplying an empty CodeMirror extension list. It copies no color-picker implementation. The only deliberate feature removal is CSS color swatch editing; text/JSON/XML editing and request execution remain.

```sh
npm ci --prefix tools/scalar --ignore-scripts --no-audit --no-fund
make test-scalar-source
node tools/scalar/build.mjs
python3 tools/scalar/notices.py
python3 scripts/check-assets.py
```

Node 22+ and npm are needed only to regenerate these assets. Normal `make build` and the delivered executable do not run or require Node/npm. The npm lock pins the development dependency graph. A transitive lock entry can mention the excluded package; its code is never read by esbuild or included in the distributed assets. `bundle-inputs.json` records actual input modules and `bundle-digests.json` records output hashes. CI repeats the rebuild and compares the results.

Original upstream copyright/license notices for the included packages are collected from local package files or immutable upstream commits into `third_party/SCALAR-BUNDLED-NOTICES.txt` and embedded in the executable. The generated JS/CSS legal-comment sidecars are retained too. The notice collector fails on an unknown/missing upstream notice rather than manufacturing one. Scalar workspace packages use the original Scalar MIT license; precompiled Tailwind CSS has its original MIT notice separately included.

Verification includes source-graph exclusion, pinned output digests, offline docs rendering, Scalar GET and PUT execution, exact SDK readback of bytes uploaded through the Scalar request client, console/browser checks, and the standard Go single-binary gates. The earlier color-picker metadata file is retained only as historical evidence of why that optional feature was removed; it is not a runtime dependency inventory.

The small local `go.mod` is a traversal boundary, not a second runtime service.
It prevents root `go test ./...` / `go fmt ./...` / vulnerability scans from
compiling or rewriting third-party Go examples inside optional node_modules.
The production binary still has one root Go module and one process.

## Guarded combobox accessibility adaptation

The first-party build adapter in `collapsed-state-transform.mjs` changes only
CodeInputLite's `aria-expanded` expression: variable-enabled comboboxes expose
`false` when collapsed and `true` when expanded. Non-combobox inputs retain the
upstream absence of the attribute. It checks the exact module path, locked
package versions/integrities, original SHA256, unique source anchor and transformed
SHA256, and fails closed if any upstream input changes. Installed source files
are never rewritten. Original copyrights, licenses and legal sidecars remain.
The build metafile records the transformed input and adapter/lock provenance.

`make test-scalar-source` is mandatory for `make before-commit` and requires the
approved installed pinned module. Pure development checks may use a separately
retained original as data through an explicit absolute `SCALAR_ORIGINAL_CODE_INPUT`
path; CI refuses that override. Such checks do not establish dependency closure,
asset generation or browser accessibility. The current adapter source has passed
17 data-only cases; no rebuilt asset or actual combobox/axe success is claimed.
Rebuild through the approved acquisition process, inspect generated notices and
provenance, then run real Scalar request-client and strict accessibility checks.
