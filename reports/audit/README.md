# Console flow audit — 2026-11-03

Scope: the actual embedded S3 console, from a fresh standalone binary, at 1440×1000, 390×844, and 720×1000 CSS pixels. Goal: create a bucket, upload an object, understand deletion constraints, and recover from an error. This is a tested **basic console increment**, not a claim of AWS's complete console fidelity or full accessibility conformance.

The screenshots below were captured in this audit run and visually inspected. `axe-results.json` contains the automated WCAG 2 A/AA and WCAG 2.1 AA rule results; `tool-provenance.json` records exact axe-core 4.11.0 integrity. axe-core is a development-only downloaded tool (MPL-2.0, original notices retained in the tool directory), not part of the simulator binary.

| Step | Screenshot | Health and evidence |
| --- | --- | --- |
| 1. Empty bucket list | [01-empty.png](01-empty.png) | Clear empty state and create action. Muted text contrast was insufficient in the first capture and was corrected. |
| 2. Create bucket | [02-create-form.png](02-create-form.png) | Labeled fields, region choice, scope explanation. Keyboard Enter opens; focus starts in the name field; Escape closes and returns focus. Invalid uppercase names are rejected by browser validation; a valid hyphenated name is accepted. |
| 3. Bucket detail | [03-bucket-detail.png](03-bucket-detail.png) | Explicit object empty state, breadcrumb back to storage, working properties tab. This is object storage only. |
| 4. Upload | [04-upload-form.png](04-upload-form.png) | File/key inputs and size limit visible. The actual file is uploaded; SDK/UI byte equality is separately checked in e2e.txt. |
| 5. Delete nonempty bucket | [05-delete-error.png](05-delete-error.png) | Exact-name confirmation, visually distinct destructive action, real BucketNotEmpty error, retained input, and recoverable dialog. No resource disappears. |
| 6. Mobile | [06-mobile.png](06-mobile.png) | Main action remains visible; header labels no longer wrap awkwardly. Resource table scrolls inside its panel rather than overflowing the page. Identity policy management remains reachable through API & scope. |
| 7. Narrow reflow | [07-reflow.png](07-reflow.png) | No page-level horizontal overflow. An additional offscreen storage-class label contrast issue was found at this width and corrected. This approximates narrow-view reflow; it is not a claim that all browser zoom settings were tested. |

Automated result after fixes: **zero reported violations across these seven states**, with incomplete/manual-review results retained in the raw JSON rather than hidden. The initial captures under `before/` document contrast findings; the current numbered captures are the corrected state. A repeated CI audit fails on any reported violation.

Other fixes from inspection: destructive actions use a red treatment rather than the green create treatment; input validation patterns are valid under current browser rules; the copyable CLI example contains actual line breaks; modal focus is explicit; mobile users can open identity policy management.

Remaining UX limits: this increment does not provide AWS-scale service navigation, bulk operations, table sorting/page controls, advanced S3 configuration, or the full IAM console. Bucket selection, region and principal are currently page state rather than durable deep links; a browser reload returns to the initial view. Existing policy documents are inspected through the local API reference, while the console editor creates/replaces them. These remain follow-ups, not simulated completed features.

Evidence limits: no real AWS login or trademarked console assets were used. A direct live SimAWS visual comparison remains unavailable because its domain could not be opened from the cloud Chromium environment. Automated rules and screenshots do not establish screen-reader usability, every keyboard sequence, every OS/browser, touch target compliance, all zoom settings, or complete WCAG conformance. No actual cloud infrastructure, networking, SSH, or compute execution was involved.
