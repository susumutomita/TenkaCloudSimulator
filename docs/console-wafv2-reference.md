# AWS WAF console reference and bounded implementation

The console uses official `@cloudscape-design/components` and the application's embedded official global styles. It is an independent local Simulator, without AWS logos or an AWS connection. `wafv2.jsx` consumes the native regional JSON1.1 API; it does not maintain an alternate service state.

## Observed public references (2026-11-04)

Authenticated AWS console screens have **not** been observed. No account sign-in, credentials, real resource creation, or charging operation was performed. Public primary documentation now calls the configuration a **protection pack (web ACL)** and describes an updated guided console. The bounded implementation displays both names; it does not claim pixel or feature equivalence with that updated experience.

| Primary reference | Applied behavior | Remaining difference |
| --- | --- | --- |
| [Updated console experience](https://docs.aws.amazon.com/waf/latest/developerguide/working-with-console.html) and [creation](https://docs.aws.amazon.com/waf/latest/developerguide/web-acl-creating.html) | Protection pack (web ACL) naming; creation, region, default action | Bounded form rather than the complete guided protection workflow; no managed-rule recommendations |
| [Rule statements](https://docs.aws.amazon.com/waf/latest/developerguide/waf-rule-statements.html) | One root statement; nested AND; byte and SQL injection inspection | JSON statement editor rather than full visual builder; only backend-supported statements/transforms |
| [Default action](https://docs.aws.amazon.com/waf/latest/developerguide/web-acl-default-action.html) and [testing/tuning](https://docs.aws.amazon.com/waf/latest/developerguide/web-acl-testing.html) | Allow/Block default; Count rule changes to Block without replacing its condition | No traffic dashboard, sampled requests, CloudWatch, or tuning recommendations |
| [Resource associations](https://docs.aws.amazon.com/waf/latest/developerguide/web-acl-associating-aws-resource.html) | Same-region API Gateway REST stage associations in a dedicated tab | ARN input rather than a complete AWS resource picker; no other resource types |
| [GetWebACL](https://docs.aws.amazon.com/waf/latest/APIReference/API_GetWebACL.html) and [UpdateWebACL](https://docs.aws.amazon.com/waf/latest/APIReference/API_UpdateWebACL.html) | Read LockToken, preserve mutable configuration, refresh after updates, explicit stale-token recovery | Local bounded quotas/detection; AWS propagation and detector equivalence are not claimed |

Further read-only reference evidence should cover WAF regional list/create, ACL Rules and associated resources tabs, rule edit Count→Block, statement builder and stale-edit errors, at desktop and narrow width. Acceptable evidence is separately authorized read-only browser access or redacted operator screenshots/recordings with dummy data. Never request credentials or use the newly created account as implicit sign-in permission.

## Native state, editing and limits

The parent supplies authenticated same-origin `request`, region and navigation write locking. Calls POST `/` with `X-Simulator-Service: wafv2`, `X-Simulator-Region`, and `X-Amz-Target: AWSWAF_20190729.Operation`. The backend enforces IAM and resource validation. Token/principal authority is never encoded in rule JSON. Region/identity navigation must remount the component, aborting old reads.

List pages are fetched with native pagination. Detail retrieval obtains both the full WebACL document and LockToken, followed by native associated-resource retrieval. Updates preserve all supported Get-spec mutable fields, unrelated rules and original rule VisibilityConfig. Create defaults sampled requests and CloudWatch metrics to false because their presentation is unavailable. Existing native configurations are retained, not silently rewritten. Native optimistic-lock failure keeps the draft visible and disables saving until the user explicitly reloads the latest configuration, which discards the draft. Delete requires the exact ACL name and never removes associated API/Lambda resources.

Rules expose name, numeric priority, action and native statement JSON. Native ByteMatch `SearchString` is **base64**, whereas boto3 exposes bytes and CloudFormation uses a plaintext property. The original batch rule is AND(UriPath ENDS_WITH `/wp-json/batch/v1` with NONE, SqliMatch Body with URL_DECODE/CONTINUE). Changing its action must preserve its complete statement. The UI does not install a problem-specific rule or mark any problem solved.

Unsupported CloudFront scope, managed groups, CAPTCHA/Challenge, logging, dashboards and sampled requests are explicitly identified. SQL injection detection is a bounded implementation, not a claim of AWS engine equivalence. No fake success, fabricated metrics or external resource discovery is used.

## Verification and integration boundary

`test/e2e/wafv2_console.py` requires the integration owner's WAF service registration/navigation/build. It performs browser create→native SDK read→rule create→concurrent SDK update→stale save rejection→reload→Count-to-Block→SDK preservation checks→delete. It checks desktop/390px reflow, actual keyboard save, browser errors and outside requests. Set `AXE_SCRIPT_PATH` for WCAG checks. Only dummy local data is used.

Component compilation or this console round-trip alone does **not** establish service completion. Integration must additionally verify associated real HTTP requests: Count reaches the saved-role Lambda, Block prevents Lambda execution, ordinary traffic stays available, IAM denies unauthorized mutations, and independent team processes cannot access each other's state. Full problem provisioning/grading/cleanup remains separately gated. Run the repository `make before-commit`, `make e2e`, license/security checks and formal checks through the integration owner before delivery; unrun checks must be reported explicitly.

Focused pre-integration verification on 2026-11-04: official-dependency esbuild bundle succeeded; Python helper byte-compilation and `git diff --check` succeeded. A temporary synthetic transport harness exercised create/rule edit/stale-token rejection/reload/keyboard save without browser errors. axe WCAG A/AA and document reflow passed at 1440px and 390px for list, create, rule editor, associated resources, association form, settings, settings editor and deletion (16 state/viewport combinations), plus saved-rule detail at both widths. This harness is UI evidence only, not native API/HTTP proof. Screenshots are temporary local dummy-data artifacts; no authenticated AWS comparison is claimed. Full repository gates and the real-backend helper remain pending integration.
