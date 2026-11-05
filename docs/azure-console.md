# Azure Blob console

The simulator provides a bounded, independent Azure Blob Storage console over
its local Azure HTTP API and SQLite state. It does not connect to Azure. The
provider-specific redesign is a private integration candidate. Its canonical
22-package build and actual embedded CLI passed focused browser acceptance;
whole new-head regression gates and hosted CI remain separate obligations.
This is not authenticated Azure Portal parity or official SDK compatibility.

## Screen and operation sequence

The redesigned resource view uses native React controls, a storage-account
heading, resource navigation, a flat command bar, a checkbox table and a right
operation pane. It does not use AWS Cloudscape layout or table components.
The integration owner supplies the separate provider/service/language/session
controls. The host-configured account is read-only; it is not an ARM resource
created by the user. Only implemented storage operations are actionable.

1. Select Azure and Blob Storage in the host console. The resource menu's
   **Data storage → Containers** entry opens the live container list for the
   configured account. The menu search only filters that resource navigation.
2. Select **+ Container**, enter a valid name, then select **Create** in the
   right pane. Names use 3–63 lowercase letters, digits or hyphens, without
   consecutive hyphens. Access remains private to the local authorized subject;
   no anonymous-access policy switch is implemented.
3. Open a container by its name. Select **Upload**, choose one file, and confirm
   its blob name. The maximum file size is 8 MiB. Creation is conditional by
   default. To replace an existing target and its metadata, enter the literal
   API-independent confirmation word `replace`; the target must already exist.
4. Select a blob's checkbox to download or delete it. Selecting its name or
   **Properties** opens the last-list metadata: name, modification time, ETag,
   type and size. This pane does not issue a new HEAD request or preview content;
   use **Refresh** to obtain a new list snapshot.
5. Deleting a blob requires its exact name and sends the selected ETag. A changed
   blob produces a conflict message rather than an unconditional deletion.
   Return through the **Containers** breadcrumb to select a container for
   deletion. Its confirmation warns that all contained blobs, including later
   pages, are permanently removed.

**First page** reloads the first live page; **Next page** uses the server's opaque
continuation marker. **Filter this page by name** is a local substring filter,
not a search over other pages. Empty, loading, permission-error and successful
states remain distinct. English is the default; Japanese presentation uses the
shared saved locale. Changing language does not change account, resource names,
API values, authority, form data or tokens.

## Composition and native API contract

```jsx
<AzureConsoleShell controls={neutralControls}>
  <AzureBlobView key={'azure:'+subject+':'+environment+':'+account}
    request={request} account={account} />
</AzureConsoleShell>
```

The shell accepts `{children, controls}`. `AzureBlobView` retains
`{request, account='tenkacloudlocal'}`. The integration owner must place this shell
outside AWS AppLayout and preserve a distinct key for each subject, environment
or account. `request(path, options)` is the host's authenticated same-origin HTTP
helper: it preserves body, headers and AbortSignal, handles expired sessions and
must not add AWS selectors to Azure operations. Neither component reads tokens
or constructs trusted Actors from headers. Local owner authorization is not
Microsoft Entra ID, Azure RBAC, SAS or an account-key implementation.

| Action | Actual native operation |
| --- | --- |
| Containers | GET `/_simulator/providers/azure/{account}?comp=list&maxresults=100` |
| Create container | PUT `.../{account}/{container}?restype=container` |
| Blobs | GET `.../{container}?restype=container&comp=list&maxresults=100` |
| Upload | PUT blob with `x-ms-blob-type: BlockBlob` and the selected File |
| Create-only / replace | `If-None-Match: *` / explicit `If-Match: *` |
| Properties pane | Last successful list metadata; no extra request |
| Download | GET blob, bounded bytes saved as an octet-stream attachment |
| Delete blob | DELETE blob with the selected ETag in `If-Match` |
| Delete container | DELETE with `restype=container`; atomic cascade |

Each request carries `x-ms-version: 2023-11-03`. Container names are encoded as
one segment. Blob segments are encoded individually, preserving slash separators
and literal percent spelling. Standalone `.` and `..` path segments are explicitly
refused because browser URL parsing can normalize them; use a native client for
those names. No second decoding or silent name repair is introduced by this UI.

The existing API/read/lifetime functions are byte-identical to the redesign's
`c334f4f` baseline. Actions remain serialized, abortable and generation-guarded,
including checks after asynchronous reads, with a 30-second deadline. XML lists
are limited to 1 MiB, 100 entries, depth 16 and 10,000 nodes, with strict UTF-8,
expected roots and no DTD/entities. No parsed HTML is injected. Downloads have an
8 MiB streaming cap, fixed safe MIME type and revoked object URLs. Error messages
map known status conditions without echoing response bodies or credentials.

## Public visual references and remaining differences

Reference review date: **2026-11-05**. These are official public documentation
images, not observations of an authenticated current Azure Portal session.
Document publication dates do not establish screenshot capture dates.

| Official source | Visually inspected reference and date limits |
| --- | --- |
| [Azure Portal overview](https://learn.microsoft.com/en-us/azure/azure-portal/azure-portal-overview), document dated 2026-01-26 | [Portal callouts image](https://raw.githubusercontent.com/MicrosoftDocs/azure-management-docs/main/articles/azure-portal/media/azure-portal-overview/portal-callouts.png): header, breadcrumbs, resource title, service menu and command bar. It depicts a VM, not Blob Storage. |
| Same overview | [Service-menu image](https://raw.githubusercontent.com/MicrosoftDocs/azure-management-docs/main/articles/azure-portal/media/azure-portal-overview/service-menu-toggle.png): resource-menu search and menu toggle. |
| [Manage containers](https://learn.microsoft.com/en-us/azure/storage/blobs/blob-containers-portal), document dated 2023-06-26 | [Create-container image](https://raw.githubusercontent.com/MicrosoftDocs/azure-docs/main/articles/storage/blobs/media/blob-containers-portal/create-container-lrg.png): account/Containers title, flat actions, checkbox list and right creation pane. This is a historical visual reference. |

The [Portal quickstart](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-quickstart-blobs-portal)
(document dated 2026-08-25) supplies the documented account → container → upload
sequence. The [authorization guide](https://learn.microsoft.com/en-us/azure/storage/blobs/authorize-data-operations-portal)
explains real Portal authorization concepts; this simulator does not claim them.
The public images guide the new structure, not copied runtime artwork or logos.

The exact upload-image fetch at
`https://learn.microsoft.com/en-us/azure/storage/blobs/media/storage-quickstart-blobs-portal/upload-blob.png`
stopped with `Tunnel connection failed: 403 Forbidden`. It has not been retried
through another tool, mirror or proxy. Its pixels remain unconfirmed. The
Portal sign-in wall was not crossed. Current authenticated upload/detail screens,
theme and mobile behavior remain unverified; no sign-in, subscription change,
real resource creation or billable action was performed.

## Unsupported scope

No ARM account/subscription management, Entra sign-in, Azure RBAC editing,
anonymous-access changes, SAS generation, leases, tiers, snapshots, versions,
soft delete, staged blocks or recovery is simulated by these controls. The
resource menu does not present other storage services as working. Blob Properties
is an actual list snapshot, not a claim of every property available in Azure.

## Redesign verification status

This table separates source checks from runtime acceptance. The integration owner
can replace pending entries with exact new-head receipts after running the gates;
older API or browser evidence alone does not validate the redesigned screen.

| Check | Current redesign evidence |
| --- | --- |
| JSX for view/shell and CSS parsing | PASS at `8e199c4`: existing esbuild transform; zero warnings; no asset output or dependency installation |
| API/read/error helpers and lifetime/native CRUD | Byte-identical to `c334f4f`; this is source comparison, not a new API execution |
| Diff whitespace check | PASS at `8e199c4` |
| Canonical bundle, embedded CLI and license/digests | PASS: canonical 22-package bundle and actual embedded CLI; existing original notices retained |
| Real browser lifecycle, keyboard, pane focus/Cancel and axe | PASS: actual two-CLI native HTTP lifecycle; 22 cross-provider audit states, not an SDK claim |
| Desktop 1440 / mobile 390, layout and no horizontal overflow | PASS in focused embedded provider/language tests; authenticated Azure mobile parity unverified |
| Same-SQLite native HTTP, cross-team/auth denial | PASS: exact download bytes, peer isolation, native/UI403, cross-token/cookie401 and logout replay refusal |
| Official Azure SDK / restart persistence | Not certified by the browser helper; separate native/SDK gates remain necessary |
| Locale switching/persistence without losing entered data | PASS: real in-pane selector, draft/file/confirmation and native412 error retained; 12 combined language audit states |
| Whole Go, E2E, formal applicability and hosted CI | Pending for the integrated head |

Runtime acceptance must use real authenticated local HTTP/SQLite, not mocked
responses: container create, binary/slash/percent/UTF-8 upload, native SDK and
safe-download byte comparison, stale ETag refusal, explicit replacement, confirmed
nonempty cascade deletion, restart persistence, missing/cross-team credentials and
nonowner denial with unchanged state. Verify stale responses are discarded on
scope change, no external calls or page errors occur, and only the owned pane
handles its dismissal. The native checkbox selection replaces the earlier radio
selection; browser tests must exercise that real selection without weakening the
state or deletion assertions. No runtime acceptance is inferred from CSS alone.
