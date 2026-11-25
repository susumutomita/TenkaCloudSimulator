const MAX_PART_NUMBER = 10_000;

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
