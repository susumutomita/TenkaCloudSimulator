# Azure Blob console source candidate

This independent component is not mounted or bundled into the simulator yet.
No browser, bundle, Go or Azure SDK acceptance was performed in this source-only
phase. It must not be advertised as available Azure support. Existing Cloudscape
components supply accessible controls; this is provider-specific Azure-labelled
content, not an AWS service screen and not an exact Azure portal replica.

## Root composition contract

```jsx
import AzureBlobView from './azure-blob.jsx';
<AzureBlobView key={'azure:'+principal+':'+account}
  request={request} account="tenkacloudlocal" />
```

`request(path, options)` is the host console's same-origin native HTTP function.
It returns a raw fetch `Response`, throws for non-success and handles expired host
login. It must honor `options.signal` and headers/body without injecting an AWS
service selector into Azure requests. Root binds the already-authenticated Azure
exercise subject server-side; the component neither reads credentials nor selects
an Actor through arbitrary headers. Root owns provider navigation, authorization,
account fixture configuration, module import and final asset regeneration.

The parent must remount with a different React key on subject, environment or
account change. Pending work is generation-guarded, serialized and abortable on
unmount; responses are checked again after asynchronous reads before publishing.
30-second action deadlines bound stalled UI work. No token/Entra/SAS inputs exist.
Do not preview an object body as HTML or navigate to an upstream download URL.

The existing leaf is account-in-path, which is different from the Azure cloud
hostname. `tenkacloudlocal` is host configuration, not successful ARM deployment:

| UI action | Actual native request |
| --- | --- |
| Containers | GET `/_simulator/providers/azure/tenkacloudlocal?comp=list&maxresults=100` |
| New container | PUT `.../tenkacloudlocal/{container}?restype=container` |
| Blobs | GET `.../{container}?restype=container&comp=list&maxresults=100` |
| Upload | PUT `.../{container}/{blob}`, `x-ms-blob-type: BlockBlob`, File body |
| Download | GET blob, bounded bytes saved as an octet-stream download |
| Delete blob | DELETE blob, selected ETag as `If-Match` |
| Delete container | DELETE container with `restype=container`; atomic cascade |

Every request carries leaf version `2023-11-03`. Container names are encoded as
one segment; blob segments are individually encoded while literal slash separators
are preserved. A literal percent is encoded once, not decoded again. Browser URL
parsing normalizes standalone `.`/`..` segments even if percent-encoded, so this UI
refuses those exact blob paths and explains the need for a native client. It does
not silently edit their spelling. The generic RawPath router candidate remains a
separate patch and prerequisite to strict account/container path fidelity.

Lists parse only bounded1MiB UTF-8 XML, expected EnumerationResults roots, DTD-free
XML, depth16/nodes10000 and at most100 entries. No innerHTML or parsed-node adoption.
NextMarker is carried opaquely to the next live page; local filtering applies only
to the current page. Upload is one file up to8MiB, create-only by default, with an
explicit `replace` action for an existing target; overwrite replaces metadata too.
Download has an8MiB streaming cap and never runs the blob's MIME type. Destructive
confirmation repeats the exact selected resource name. Container deletion warns
that **all blobs, including later pages, are permanently removed**; no soft-delete
or snapshot recovery is implied. Errors retain useful native codes without showing
raw payloads or credentials. 403 is distinct from an empty successful list.

## Public portal reference and intentional gaps

Checked2026-11-04 against Microsoft primary sources:

- [Portal quickstart](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-quickstart-blobs-portal)
  (updated2026-08-25): account → Data storage/Containers → New container;
  open container → Upload → list/download/delete; deleting a container removes blobs.
- [Manage containers](https://learn.microsoft.com/en-us/azure/storage/blobs/blob-containers-portal):
  New container name/private access, selection and confirmed cascade deletion.
- Official linked screenshots:
  [upload](https://learn.microsoft.com/en-us/azure/storage/blobs/media/storage-quickstart-blobs-portal/upload-blob.png),
  [download](https://learn.microsoft.com/en-us/azure/storage/blobs/media/storage-quickstart-blobs-portal/download-blob.png),
  [delete](https://learn.microsoft.com/en-us/azure/storage/blobs/media/storage-quickstart-blobs-portal/delete-blobs.png).
  Their primary guide links were inspected, but image downloads returned403 in this
  environment. The screenshots themselves were not visually inspected. An initial
  guessed repository image path returned404; it was corrected using guide links.

No authenticated Azure portal was observed. Public page chrome displayed an
access notice, but the documented operation text was available without sign-in.
No real Azure sign-in, subscription, resource, SAS or billable action was used.
The upload interaction is a local modal instead of Azure's blade; this source is
not a pixel-fidelity claim. Anonymous access changes, Entra/RBAC management, SAS,
ARM account creation, leases, access tiers, snapshots, versions, staged blocks and
soft-delete remain unsupported and are not shown as working controls. The parent
must offer an Azure-specific shell without AWS account/region navigation semantics.

## Required integration acceptance (not yet executed)

Use actual host authentication with the Azure Actor adapter and actual leaf/SQLite:
create private container, upload binary/slash/percent/UTF-8 names, compare SDK/list/
download bytes, ETag-conflict rejection, delete blob, confirmed nonempty container
cascade, restart persistence, cross-team token denial, missing actor and operation
403. Check native error/unsupported paths; do not use mocked API responses.

At1440px and390px verify real keyboard navigation/focus, labels, upload limits,
confirmation, stale-request suppression on unmount, page navigation/empty/error
states, no external requests, safe HTML/SVG download and no document horizontal
scroll. Cloudscape file picker/table layouts need actual browser measurement;
the scoped CSS alone does not establish responsive or accessibility success.
Bundle/digest/license checks and final whole regression gates remain required.
