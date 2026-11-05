# Cross-provider console integration acceptance

Historical browser acceptance: **actual standalone integration execution passed** against the root-built
binary recorded in `/workspace/catalog-deliverables/multicloud-browser/execution-receipt.json`.
This is a local integration result, not exact-HEAD hosted CI or a merge approval. This helper is an integration test,
not the GCP/Azure leaf unit evidence or an official SDK/live-cloud comparison.
No guest code, actual Google/Azure resources, credentials or new dependencies
are required. Root owns the final embedded binary and execution window.

Separately, [official Python SDK acceptance](crossprovider-official-storage.md)
executed successfully on 2026-11-05 with pinned Google/Azure packages. GCP keeps
all twelve exact-key cases with its disclosed official session/OAuth-refresh
configuration; Azure keeps ten and explicitly measures/reports the two Requests
dot-segment exclusions. That new SDK run is independent of this browser helper's
native-HTTP oracle and does not establish new final integration CI success.

```sh
AXE_SCRIPT=/path/to/verified/axe.min.js \
SIMULATOR_BINARY=/path/to/integrated/tenkacloud-simulator \
SIMULATOR_REPORTS=/workspace/owned-reports \
/path/to/existing-test-python test/e2e/crossprovider_console.py
```

The canonical axe input is mandatory. The existing test dependencies from
scripts/requirements-test.txt and Chromium must already be available; this helper
does not install them. No missing capability/tool is skipped. It starts two real
copied CLI binaries with separate persistent SQLite paths/private token files,
empty executable PATH and actual minted browser cookies. It closes browser
contexts and CLI processes in finally paths; state fixtures use temporary DBs.

## Assertions

- Both Services entries exist. Real GCP and Azure create/upload/download/delete
  flows run at1440px and390px; exact browser download bytes match a fixed UTF-8
  fixture. Names contain slash, literal-percent and Japanese characters.
- Native HTTP reads independently verify every saved upload/deletion; same names
  in the second DB retain distinct peer bytes. GCP nonempty deletion refuses409;
  UI bucket deletion follows object deletion, with typed confirmations. The
  configured project-owner factory has no project for a missing bucket and
  conceals it with403. Removal is independently proved by successfulDELETE204
  and exclusion from the actual project list; this is not Google IAM parity.
- Participant reader with an AWS wildcard policy still receives native/provider
  UI403, cannot create either provider resource, and list state remains unchanged.
  This is local configured-owner authorization, not GCP IAM/Azure RBAC.
- Real Account-menu keyboard Sign out produces protected401/raw-cookie replay401,
  an unrelated team's cookie/token fails401, re-login mints a new cookie, and the
  header SDK credential remains authenticated (provider data access still403).
- Scalar examples are visibly static before actual Test Request→Send; the native
  S3 response must contain an independently created dummy bucket. This trace does
  not claim new provider-specific Scalar client or complete Scalar accessibility.
- axe WCAG2A/AA/2.1A/AA runs over actual provider create/upload/list/delete and
  reader states. Page geometry uses existing strict reflow checks. Screenshots
  contain dummy names/data, never tokens. Non-local requests and page errors fail.

## Integration dependencies and historical execution

The helper is now registered as a mandatory case in shared `test/e2e/check.py`;
its original source-only registration dependency is resolved. Provider capabilities,
actor policy, UI imports/navigation, OpenAPI and generated bundles are root-owned.
The default fixtures require project tenkacloud-local, account tenkacloudlocal and
simulator-admin as local configured owner. Another subject must not inherit
ownership merely because its AWS policy allows all actions.

Actual execution confirmed Services/menu, row radio, modal labels, responsive
geometry and real downloads. Initial attempts remain recorded: deleted missing
GCP bucket is intentionally concealed403 (removal proved separately by204+list);
reader error selector needed exact visible text because hidden modal copies exist;
Scalar mobile hidden navigation required its visible Test Request control. No
assertion was ignored or scope widened to accept an arbitrary status. Attempt5
passed the complete helper with18 axe states, reflow, outside requests0 and page
errors0. No Go/build/TLC was run by this helper owner. Full regression,
coverage/security/license, formal applicability and exact-HEAD CI remain required.
The existing Scalar vendor client's previously observed open-client ARIA debt is
not declared resolved by this helper; provider-state axe checks remain mandatory.

Evidence is outside Git: `crossprovider-console.json`, `execution-receipt.json`,
`attempt-1.log` through `attempt-5.log` and18 dummy-data PNGs in the report
directory above. Native HTTP remains this helper's state oracle. The separately
executed [official SDK acceptance](crossprovider-official-storage.md) supplies
SDK evidence for its pinned subset, not a reinterpretation of those browser
reports. Scalar static-versus-actual Send passed at390px;
Scalar open-client axe conformance is not asserted.
