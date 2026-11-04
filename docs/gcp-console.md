# Cloud Storage console source candidate

This component is **source-only, unregistered and not browser-verified**. It is
not a Google Cloud console parity claim, an authenticated Cloud Console capture,
or an SDK/provider integration completion. No Google account was signed into and
no real resource was created. Kumo is architecture reference only; this React/CSS
implementation is independent scratch source, with no copied upstream service code.

## Integration contract

```jsx
import GCPStorageView from './gcp-storage.jsx';
<GCPStorageView key={providerIdentityEpoch} request={providerRequest}
  project="tenkacloud-local" />
```

`request(path, options)` must use the existing same-origin authenticated host
transport and return a raw Fetch `Response`. It must throw on non-2xx responses
with a safe provider error, handle401 through the host's login flow, and accept
`AbortSignal`, ArrayBuffer body and Content-Type. It must not inject AWS region,
service or principal headers. The caller owns provider/identity switching and
must remount on exercise-identity change, preventing old identity list state
from staying visible. The fixed project label is host configuration, not authority
or a project provisioning/selector implementation. Backend actor/ownership policy
must still evaluate each actual request; this UI never creates an Actor/token.
Root owns main.jsx navigation/provider selection, shared build/assets, routing,
authorization composition and final integrated acceptance. No shared files are
modified by this source slice. Scoped CSS is imported by the owned component;
there are no new dependencies, external fonts, branding assets or runtime calls.
Native semantic controls provide a provider-specific surface instead of calling
AWS Cloudscape navigation a GCP console. Visual fidelity remains unverified.

The native routes use these prefixes (same-origin only):

- `/_simulator/providers/gcp/storage/v1`: bucket list/create/get/delete and object list/get/delete.
- `/_simulator/providers/gcp/upload/storage/v1`: object media POST.

Objects are encoded once using `encodeURIComponent` as one path segment,
including slash and literal percent. Project/query fields use URLSearchParams.
A request wrapper must not clean/redecode the paired URL path or convert encoded
object slashes into JSON API subresource separators. Root should verify both
prefixes against the final server dispatch before enabling navigation.

## User flow and limits

Cloud Storage → Buckets → Create bucket, with name, location and fixed Standard
storage. Name uniqueness is local-environment global; supported location is
metadata, not geographic replication. No fake billing, access-control wizard,
retention, Autoclass or encryption setting is submitted.

Click a bucket name → Bucket details → Objects. Upload files accepts one file,
validates at most1MiB and valid UTF-8 before sending its bytes as text/plain.
An editable exact object name allows slash/percent/Unicode. `ifGenerationMatch=0`
prevents unintended overwrites; a collision is an error to review, not success.
File selection and input values remain local until Upload. Cancel sends no write.
Downloads read actual `alt=media` through the authenticated request seam and use
an object URL with a download attribute; object HTML is never rendered in-page.
The temporary object URL is revoked after download dispatch. Browser/CSP download
behavior still requires real integrated verification.

Delete object requires typing its exact name and sends the listed generation as
`ifGenerationMatch`; stale state must fail rather than deleting a changed object.
Delete bucket also requires its exact name; nonempty bucket refusal is left to
the server, without silently cascading object deletion. Success notices are set
only after a successful HTTP response. Loading/empty/error states are distinct,
403 is explained as an exercise-authorization refusal, and failed actions retain
the form for correction. Lists use native maxResults100 and opaque nextPageToken
with Previous/Next navigation; this is live pagination, not a snapshot promise.
Async reads are aborted/revision-guarded on navigation and unmount; project/actor
changes must follow the caller remount contract above.

Backend ceilings and limitations remain authoritative: see the separately owned
GCP leaf `docs/gcp-storage.md` when integrated. No default official SDK support,
resumable upload, ACL/IAM editor, version history, retention, signed URL,
Cloud Shell, public website hosting or billing is implied by visible UI labels.

## Public primary references, checked2026-11-04

- [Create a bucket](https://docs.cloud.google.com/storage/docs/creating-buckets):
  Buckets→Create and the name/location/storage-class sequence inform the bounded
  form. Official full configuration is broader than this implementation.
- [Upload objects](https://docs.cloud.google.com/storage/docs/uploading-objects):
  bucket name→Objects tab→Upload→Upload files informs labels and order.
- [List objects](https://docs.cloud.google.com/storage/docs/listing-objects):
  bucket details/Objects and lexicographic listing; local view remains flat.
- [Path encoding](https://docs.cloud.google.com/storage/docs/request-endpoints#encoding):
  encoded slash/percent handling; local escaped-segment subset is not a universal
  claim about every real Google endpoint.

Authenticated reference screens remain unobserved. Useful later read-only
reference material would be project/Buckets list, Create bucket defaults,
Bucket details/Objects, Upload files progress/errors, object download/delete
confirmation and empty/nonempty bucket delete. A separately authorized screenshot
or guided read-only session can establish visual differences; credentials and
resource creation are not needed for this source work and must not be requested
through the component.

## Required acceptance before support/merge claims

Source implementation is not successful execution. Root must integrate a real
backend and run build/assets/license checks, actual HTTP-backed browser lifecycle
and regression gates. Required focused traces include:

1. Two isolated team identities, permitted and403-denied reads/writes, no AWS
   principal leakage, missing/expired local session and provider switch cleanup.
2. Bucket create→text upload→exact native object read/download bytes→conditional
   deletion→empty bucket deletion, restart persistence and denied/nonempty frames.
3. Simultaneously stored slash/percent/dot/Unicode neighbors, raw subresource
   rejection, stale generation412, create-only collision and failed network retry.
4. Pagination past100 entries without repeat/loss on a stable dataset; delayed
   old list cannot replace newer bucket/project/identity state.
5. PC and390px layouts, keyboard/form labels, Cancel0writes, success/error
   announcements, axe and horizontal overflow confined to the named table region.
6. No outside requests or secrets in screenshots/logs; ordinary static Go binary
   still serves all assets offline. Go compilation, SDK, browser and full CI are
   **not executed** by this source-only handoff.
