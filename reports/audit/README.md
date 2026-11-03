# Cloudscape console flow audit — 2026-11-03

Actual embedded console, dummy data, 1440×1000 desktop, 390×844 mobile and 720×1000 reflow. Official Cloudscape components/global styles are bundled. See [primary-source comparison and limits](../../docs/console.md).

| Step | Screenshot | Health / tested behavior |
| --- | --- | --- |
| 1. General purpose buckets | [01-empty.png](01-empty.png) | Clear empty state and create action; axe rules pass |
| 2. Create bucket page | [02-create-form.png](02-create-form.png) | General configuration, labeled name/region, input focus. Uppercase submission fails before mutation |
| 3. Objects | [03-bucket-detail.png](03-bucket-detail.png) | Bucket heading and Objects tab; unsupported tabs disabled; explicit empty state |
| 4. Upload page | [04-upload-form.png](04-upload-form.png) | Files and folders/Add files/Upload; real SDK byte readback |
| 5. Nonempty deletion | [05-delete-error.png](05-delete-error.png) | Exact-name confirmation, actual BucketNotEmpty, recoverable dialog. Escape restores trigger focus |
| 6. Mobile | [06-mobile.png](06-mobile.png) | Document stays within viewport; table overflow stays inside table. No suppressed axe rule |
| 7. Reflow | [07-reflow.png](07-reflow.png) | Document fits 720px; same actions accessible |

| 8–14. Lambda | 08-lambda-empty.png through 14-lambda-configuration-mobile.png | Native function list/create/Test/ZIP/configuration, real Python output, desktop/mobile and wrapping. All axe rules pass |

axe-results.json retains incomplete/manual checks and all reported findings. Fourteen states report zero WCAG2A/AA/WCAG21AA rule violations after fixes. Cloudscape's invalid mobile nav aria-orientation is fixed narrowly at build time, with original license and modification notice retained; no rule exclusion. Separate authenticated browser tests cover actual hello-world SSM Overview, read-only denial and two-team scoring. Long resource identifiers wrap within official components.

The before/ directory is historical custom-UI evidence, not the new Cloudscape source. Scripts are development-only. Screenshots/axe do not certify every screen reader, device, browser, zoom level or full WCAG conformity. Live authenticated AWS pixel matching is unverified; no real AWS actions were performed.
