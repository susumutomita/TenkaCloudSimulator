# Cloud Storage provider console

The source provides a dedicated Google Cloud shell and bounded Cloud Storage
workflow. This UI revision has **not yet passed integrated browser, axe or full
repository gates**. Syntax checks and11 isolated presentation-helper checks
passed; these do not establish rendered operation, native API execution or
current Google Cloud console pixel parity. Earlier backend/console evidence has
its own inputs and is not relabeled as execution of this revision.

No Google account was signed into and no real cloud resource was created. The
React/CSS implementation is independent source; Kumo is architectural reference
only, with no copied upstream service code or new dependencies.

## Shell, language and integration

```jsx
import GCPConsoleShell from './gcp-console-shell.jsx';
import GCPStorageView from './gcp-storage.jsx';

<GCPConsoleShell controls={neutralControls}>
  <GCPStorageView key={'gcp:' + exerciseIdentity + ':' + project}
    request={providerRequest} project={project} />
</GCPConsoleShell>
```

The shell default export accepts `{children, controls}`. The integration owner
supplies neutral provider/service/language/local-session controls; the Google
shell owns its Cloud Storage resource rail, configured project display and local
breadcrumbs. AWS Cloudscape AppLayout must wrap AWS only, never this provider.
The project is read-only host configuration, not a project selector, provisioner
or source of authorization. No unsupported Google service is advertised as an
operational destination. The mobile rail can be expanded with its labelled
navigation button; the table has its own bounded scrolling region.

`useLocale` from `./i18n.jsx` supplies English/Japanese presentation. English is
the default; an explicit Japanese preference is stored by the shared language
owner. Language is not part of component keys, request/effect dependencies or
project/identity authority. Switching language must preserve the form draft,
selected file, creation step and current resource. Resource names, API enum
values, generation identifiers and server error messages/codes are not translated.
Success notices retain a canonical kind and exact resource name and are translated
at render time, including notices completed after a language change. Only typed
internal validation errors use the fixed presentation dictionary; a matching
server error string remains opaque. Names are never parsed to infer notice kinds.

`request(path, options)` retains the existing same-origin authenticated host
transport and returns a raw Fetch `Response`. It must reject non-2xx replies,
handle401 through the host's login flow and accept AbortSignal, ArrayBuffer and
Content-Type. No AWS identity/region/service headers, Google credentials, Actor
or token are created by this UI. Caller remount on exercise-identity/project
change and backend checks on every operation remain required. Locale changes
must not remount the view or mint a session.

Root owns navigation composition, generated assets, authentication, shared locale,
licenses and final acceptance. The Google slice owns only its view, shell and
scoped CSS; no remote fonts, logos or runtime cloud calls are added.

## Buckets and five-step creation

Open **Cloud Storage → Buckets → Create bucket**. The form has these stages:

1. **Get started**: enter the bucket name. The server remains authoritative for
   reserved names and uniqueness within the local environment.
2. **Choose where to store your data**: choose the supported Region or
   Multi-region location type and one supported location value. This saves
   metadata only; no geographic replication or dual-region behavior occurs.
3. **Choose how to store your data**: Standard is fixed. Autoclass, other storage
   classes, hierarchical namespaces and caches are unavailable.
4. **Choose how to control access to objects**: explains the configured local
   project-owner policy. Google IAM, ACL, public access and IP filtering settings
   cannot be configured here.
5. **Choose how to protect object data**: explains unsupported soft delete,
   retention, Object Versioning, encryption options and recovery, then shows the
   name/location/Standard summary. Deletion is immediate locally.

Use **Continue** four times, then **Create**. Back preserves entered settings;
Cancel submits no write. The final Create sends the existing bounded bucket
POST, with no invented access-control or data-protection settings. The local
namespace, metadata-only location and immediate deletion differ from Google
Cloud defaults; the form explicitly describes these limits.

Lists retain native maxResults100 and opaque nextPageToken with Previous/Next
navigation. This is live pagination, not a snapshot or a global-search promise.

## Objects, live details and deletion

Click a bucket name to open **Bucket details → Objects**. Objects is a section
heading, not a disconnected interactive tab widget. Select **Upload → Upload
files**, choose one UTF-8 text file of at most1MiB and review its editable exact
object name. Slash, percent and Unicode remain name content. The create-only
media upload sends `ifGenerationMatch=0`, so an existing object is not silently
overwritten. File validation occurs before upload; Cancel performs no write.

Click an object name to open **Object details**. The view performs a real native
metadata GET with `ifGenerationMatch` from the selected list row and displays the
returned name, bucket, size, content type, generation and timestamps when present.
A changed generation or failed request does not become a fabricated successful
detail response. Metadata editing, prior generations and access grants are
unavailable. **Back to objects** returns to the list.

Downloads use the actual authenticated `alt=media` response with the selected
generation precondition. The UI creates a temporary download URL and revokes it
after dispatch; object HTML is never rendered in-page. Actual browser/CSP
behavior still needs the integrated acceptance below.

Deleting an object requires its exact name and sends the selected generation;
a stale generation must fail rather than delete changed data. A successful
object deletion clears its old detail view. Bucket deletion also requires the
exact name; the backend must refuse a nonempty bucket without cascading its
objects. Only successful server replies produce success notices. Loading,
empty, denied and other failure states remain distinct; failed actions retain
input for correction.403 is explained as an exercise-authorization refusal.

## Native request contract and limits

Same-origin prefixes remain:

- `/_simulator/providers/gcp/storage/v1`: bucket list/create/get/delete and object
  list/get/delete.
- `/_simulator/providers/gcp/upload/storage/v1`: object media POST.

Existing request sites retain their methods, paths, bodies, pagination and
preconditions. The added read-only object-details request uses the existing
object metadata API; no new backend API is introduced by this UI revision.
Object names are encoded once with encodeURIComponent as one whole path segment;
project/query fields use URLSearchParams. Do not clean, decode twice or reinterpret
encoded object slashes as JSON API subresource separators. Abort/revision guards
reject old callbacks after navigation, unmount or authority changes.

Backend ceilings remain authoritative; see [Cloud Storage API scope](gcp-storage.md).
There is no Google IAM/OAuth, project provisioning, billing, Cloud Shell, public
sharing, resumable upload, signed URL, retention or version-history UI. Neither
visible labels nor source presence establish complete Google Cloud support.

## Reference observations and visual gap

Public primary **text** was reviewed2026-11-05:

- [Create a bucket](https://docs.cloud.google.com/storage/docs/creating-buckets)
  documents the five sections, Continue sequence and final Create. The real
  service supports substantially more options than this bounded form.
- [Upload objects](https://docs.cloud.google.com/storage/docs/uploading-objects)
  documents bucket selection, Objects and Upload → Upload files.
- [List buckets](https://docs.cloud.google.com/storage/docs/listing-buckets)
  documents project-scoped lists.
- [View object metadata](https://docs.cloud.google.com/storage/docs/viewing-editing-metadata)
  documents Bucket details/Objects → object name → Object details.
- [Discover object storage](https://docs.cloud.google.com/storage/docs/discover-object-storage-console)
  documents the project/bucket/object workflow.

The public console endpoint redirected to Google sign-in; the authentication
wall was not crossed. Four public reference-image retrieval attempts failed
with proxy `Tunnel connection failed:403 Forbidden`; none was retried through
another route and none of those image pixels was inspected. The exact stopped
targets are recorded here to prevent accidental retries:

- `https://storage.googleapis.com/gweb-cloudblog-publish/images/Figure_2_Bucket_Detail.max-1100x1100.jpg`
- `https://codelabs.developers.google.com/static/codelabs/image-classification-bqml/img/a6f6b26cffb53ae0.png`
- `https://codelabs.developers.google.com/static/codelabs/image-classification-bqml/img/1280366a42b7bdf6.png`
- `https://codelabs.developers.google.com/static/codelabs/image-classification-bqml/img/2629ff3eda214946.png`

A current local Simulator screenshot was inspected separately. It is evidence
of the previous AWS-shaped outer shell and duplicated breadcrumbs, not an image
of Google Cloud. This implementation follows documented information architecture;
authenticated current-screen and pixel-parity verification remain unperformed.

## Validation and remaining acceptance

Executed for this source revision: esbuild syntax transforms for the two JSX
modules and scoped CSS, with zero warnings;11 isolated presentation checks,
including one structured notice rendered in both languages, exact resource names,
opaque native errors and absence of disconnected tab roles. A readable source
comparison confirms the five preexisting request sites/seven flows retain their
native request options and key guards, and identifies the additional metadata GET.
These checks do not execute React in a browser or prove native compatibility.

Focused integrated acceptance now passes against the actual embedded CLI at
PC1440/mobile390: all five create stages; upload; stale-generation metadata412
and fresh conditional200 details; exact native metadata/download bytes; confirmed
object and empty-bucket deletion; two-process/DB/token isolation; reader403 and
cross-token/cookie401; language changes during drafts and an in-flight actual
create without replay; current-locale notices; keyboard focus/search cursor keys;
12 language and22 cross-provider audit states with zero axe violations, external
requests or page errors. Reports are `reports/console-language.json` and
`reports/crossprovider-console.json`. This is native HTTP/browser evidence,
not official Google SDK or real-cloud comparison.

Full before-commit/E2E/audit/formal/coverage and exact-HEAD hosted CI remain
required for the final integration commit. Official SDK execution, complete
restart/edge-case compatibility and authenticated current-screen pixel parity
are separate claims and are not established by these focused browser results.
