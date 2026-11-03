# Official Cloudscape console build

The production source is src/main.jsx. It imports actual @cloudscape-design/components and official global styles, React and ReactDOM. package-lock.json pins the graph. No custom imitation design system, remote CDN, telemetry endpoint, AWS logo or runtime package manager is used.

```sh
npm ci --prefix tools/console --ignore-scripts --no-audit --no-fund
node tools/console/build.mjs
npm audit --prefix tools/console --audit-level=high
python3 scripts/check-assets.py
```

build.mjs bundles self-contained app.js/app.css, data-URL fonts/images, verifies real Cloudscape inputs and collects original package-root license/notice files for every bundled package. Missing original evidence fails. bundle-inputs.json/digests and third_party/console-packages.json bind the output and original notice SHA256. Notices are copied byte-for-byte into third_party/CONSOLE-NOTICES.txt and internal/legal/assets/CONSOLE-NOTICES.txt. Preserve Open Sans/font notices. Runtime Go embed includes all assets automatically.

[Console flow and fidelity limits](../../docs/console.md). Use the development browser/SDK tests and accessibility audit; do not claim matching AWS solely from using Cloudscape.

Cloudscape 3.0.1408's visual-refresh/mobile-toolbar.js places aria-orientation on a nav landmark, which fails axe aria-allowed-attr. A narrow, fail-closed esbuild onLoad removes that single unsupported attribute while preserving the official component and original copyright. It does not suppress an axe rule. Reassess this documented modification when upgrading Cloudscape; a changed source shape fails the build.
