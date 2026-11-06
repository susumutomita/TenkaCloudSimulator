# Interactive local architecture page

Open `http://127.0.0.1:4566/_simulator/` after starting the binary. The product
page is embedded and requires no separate web server or runtime dependency.
The S3 SDK endpoint remains `/`; console and API reference authentication are
unchanged. Only the five explicitly listed landing resources are public, for
read-only product information. Host and origin checks still apply.

The nine nodes describe conceptual examples from the published implementation.
They are not an inventory of live resources. Selecting a node describes the
documented local subset and its limits; it does not provision resources or
connect to a real cloud. The diagram does not imply S3 event delivery. Google
Cloud and Azure examples require the host configuration described in
[multi-cloud storage](multicloud-storage.md).

Provider controls and node details are ordinary HTML. All descriptions remain
available without JavaScript. Optional decorative drawing uses the current
[Chrome HTML-in-Canvas API](https://developer.chrome.com/blog/html-in-canvas).
The renderer checks the actual API before using it, retains the HTML controls,
and hides only its decoration on failure. It does not use an origin trial,
experimental browser flags or a canvas polyfill. Reduced motion keeps the same
static interaction; no animation loop is required.

The development environment's Chromium 154 lacks the current API, so its real
fallback path can be tested. This does not verify Chrome 157 drawing or other
browser engines. Those results must be recorded separately rather than inferred
from feature detection or mocked lifecycle tests. The bounded formal models
cover selected admission and stored-selection transitions, not browser layout,
accessibility, JavaScript execution, real-cloud equivalence or implementation
refinement.
