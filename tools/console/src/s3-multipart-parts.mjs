const MAX_PART_NUMBER = 10_000;
const MAX_PART_BYTES = 8 * 1024 * 1024;
const s3XML = 'http://s3.amazonaws.com/doc/2006-03-01/';
const strongPartETag = value => typeof value === 'string' && value.length === 34 && /^"[0-9a-f]{32}"$/.test(value);
const canonicalInteger = value => /^(?:0|[1-9][0-9]{0,7})$/.exec(value)?.[0] === value;

function invalidPage() {
  throw Error('Invalid or out-of-scope multipart part listing');
}

function elements(parent, name) {
  return [...parent.children].filter(child => child.localName === name);
}

function field(parent, name, optional = false) {
  const found = elements(parent, name);
  if (optional && !found.length) return '';
  if (found.length !== 1 || found[0].namespaceURI !== s3XML || found[0].children.length) invalidPage();
  return found[0].textContent;
}

// Every live page must belong to the requested upload and marker. Bind the
// metadata used by both Write receipts and completion to the same typed page.
export function parseMultipartPartPage(document, {bucket, key, uploadId, marker}) {
  const root = document?.documentElement;
  if (document?.doctype || root?.localName !== 'ListPartsResult' || root.namespaceURI !== s3XML ||
      field(root, 'Bucket') !== bucket || field(root, 'Key') !== key || field(root, 'UploadId') !== uploadId ||
      field(root, 'PartNumberMarker') !== String(marker) || field(root, 'MaxParts') !== '1000') invalidPage();
  const rows = elements(root, 'Part');
  if (rows.length > 1000 || (marker > 0 && !rows.length)) invalidPage();
  const parts = rows.map(row => {
    if (row.namespaceURI !== s3XML) invalidPage();
    const n = field(row, 'PartNumber'), s = field(row, 'Size'), etag = field(row, 'ETag');
    if (!canonicalInteger(n) || Number(n) < 1 || Number(n) > MAX_PART_NUMBER ||
        !canonicalInteger(s) || Number(s) > MAX_PART_BYTES || !strongPartETag(etag)) invalidPage();
    return {number:Number(n), size:Number(s), etag};
  });
  const truncated = field(root, 'IsTruncated');
  const nextMarker = field(root, 'NextPartNumberMarker', true);
  if (truncated === 'true' && rows.length !== 1000 ||
      truncated === 'false' && elements(root, 'NextPartNumberMarker').length) invalidPage();
  return {parts, truncated, nextMarker};
}

export function confirmsSavedMultipartPart(rows, {number, size, etag, status}) {
  return status === 200 && strongPartETag(etag) &&
    rows.some(row => row.number === number && row.size === size && row.etag === etag);
}

// readPage receives the numeric ListParts marker and returns the wire values
// for the flag/cursor. A partial or inconsistent listing must not be returned
// to the caller, since the caller also uses this list to complete an upload.
export async function listMultipartParts(readPage, isActive) {
  const result = [];
  let marker = 0;
  do {
    const page = await readPage(marker);
    if (page.truncated !== 'true' && page.truncated !== 'false') {
      throw Error('Invalid part list truncation flag');
    }
    let last = marker;
    for (const part of page.parts) {
      if (!Number.isInteger(part.number) || part.number <= last || part.number > MAX_PART_NUMBER) {
        throw Error('Invalid part order or number');
      }
      result.push(part);
      last = part.number;
    }
    if (page.truncated === 'false') return result;
    if (last === marker || last === MAX_PART_NUMBER || page.nextMarker !== String(last)) {
      throw Error('Invalid part cursor');
    }
    marker = last;
  } while (isActive());
  return [];
}
