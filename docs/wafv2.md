# Regional WAFv2

The bounded WAFv2 control API and Cloudscape console share native SQLite state.
POST `/` (or the AWS provider alias) with JSON 1.1 and
`X-Amz-Target: AWSWAF_20190729.Operation`. The supported operations are
CreateWebACL, GetWebACL, UpdateWebACL, DeleteWebACL, ListWebACLs,
AssociateWebACL, DisassociateWebACL, GetWebACLForResource,
ListResourcesForWebACL and ListTagsForResource. Use `Scope: REGIONAL` where the
native API requires it. SDKs use dummy TCSIM credentials plus the local access
token. Native STS credentials for this service are not supported.

Rules run by ascending unique priority. Count continues; the first matching
Allow or Block terminates; otherwise the default Allow/Block action applies.
Supported statements are nested AndStatement, ByteMatchStatement and
SqliMatchStatement with LOW sensitivity. Inspected fields are UriPath and Body;
transformations are NONE and URL_DECODE in ascending unique priority. ByteMatch
SearchString is base64 on the wire (bytes in boto3). The body inspection window
is 16 KiB, with explicit CONTINUE/MATCH/NO_MATCH oversize handling. Original
request bytes forwarded to Lambda remain unchanged. SQLi detection uses
libinjection-go 0.3.5, not AWS's detector, and is not a security-equivalence claim.

Only same-region API Gateway REST stages can be associated. Block returns 403
before Lambda permission/trust lookup or execution. Associated ACLs, stages and
APIs cannot be deleted until disassociated. GetWebACLForResource and
DisassociateWebACL read, authorize and use their association in one immediate
transaction, so a reassociation cannot reuse authorization for a different ACL.
Stage association changes additionally require `apigateway:SetWebACL`; reading
an associated ACL additionally requires `wafv2:GetWebACL`.

Create/Get/List return native IDs, ARNs and LockTokens; updates/deletes require
the current token. The process limit is 100 ACLs, with 100 rules per ACL and the
backend's bounded statement depth/capacity. Native JSON rejects unknown,
noncanonical, duplicate and null fields. Tags may be set on create and read but
not mutated. Metric/sampled-request flags are retained as configuration only;
there is no delivery or fabricated telemetry.

CloudFront, managed groups, CAPTCHA/Challenge, rate limits, IP sets, logging,
metrics, sampled requests, general statements/transforms, CloudFormation WAF
resources and full original Challenge lifecycle/scoring remain unsupported.
Unsupported problems stay disabled. See the [console reference and limitations](console-wafv2-reference.md).

## Integration verification

The current-main integration preserves its provider catalog, console and login,
API Gateway replacement implementation and required validation gates. A small
WAF-specific REST event projection replaces removed historical proxy structs.
The existing independent-source provenance record's API Gateway digest changes
only for mapping association foreign-key deletion failures to ConflictException;
its replacement-review identity remains the historical replacement evidence,
not a new independent review of this integration.

The dependency declaration is model-scoped. The existing immutable module parser
still permits only require ordering/indirect-annotation metadata with identical
dependencies, directives and go.sum, even for a model-scoped go.mod. Added or
changed dependencies remain subject to model binding; unchanged-byte, file-mode
and Git-flag tamper checks remain required.

The authorization regression fails on the original PR code: an ACL can be
replaced after authorization and then returned or disassociated without that
new ACL's permission. It passes with the single-transaction repair. The finite
WAFAssociationAuthorization model represents that lock boundary with two ACLs
and a competing writer; its controls remove the lock, ACL check or stage check.
These abstractions do not prove the concrete Go/SQL/IAM implementation.

Independent review found a WAF unmount path that could leave the parent console
locked in its saving state after browser history navigation. Cleanup now releases
that flag immediately; a delayed old completion cannot clear a new view's save.
The shipped lifecycle functions are exercised by a deterministic regression test
that fails before this fix. This is not a browser-history acceptance claim.

Focused WAF aws/server tests pass both normally and with race instrumentation.
Both WAF models and all ten sensitivity controls pass. Console rebuilding and
asset/license checks pass. An optional existing Secrets Manager locale source
test fails on its unchanged current-main source; it is not changed or suppressed.
Cloud Chromium cannot start because the executor denies its Unix socket. Real
WAF browser acceptance therefore still requires the exact-head hosted job.

Exact integrated-source full quality, E2E, model and hosted checks remain required.
Historical PR #50 CI is not evidence for this new tree. No release, deployment,
real AWS operation or expansion of playable challenges is included.
