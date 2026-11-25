const pageSize = 50;
const limit = 100;
const localName = /^(?!aws|amazon|amzn)[A-Za-z0-9_.-]{3,128}$/i;
const versionNumber = /^(?:[1-9][0-9]?|100)$/;

async function native(request, region, signal, operation, body) {
  signal.throwIfAborted();
  const response = await request('/', {
    method: 'POST',
    redirect: 'error',
    signal,
    headers: {
      'Content-Type': 'application/x-amz-json-1.1',
      'X-Amz-Target': 'AmazonSSM.' + operation,
      'X-Simulator-Region': region,
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw Error('Systems Manager request failed: HTTP ' + response.status);
  const data = await response.json();
  signal.throwIfAborted();
  return data;
}

async function collect(request, region, signal, operation, query, field, read) {
  const rows = [], tokens = new Set(), keys = new Set();
  let token = '';
  do {
    const data = await native(request, region, signal, operation, {
      ...query, MaxResults: pageSize, ...(token ? {NextToken: token} : {}),
    });
    const items = data?.[field];
    if (!Array.isArray(items) || items.length > pageSize || rows.length + items.length > limit)
      throw Error('Unexpected Systems Manager response.');
    for (const item of items) {
      const row = read(item, rows.length);
      if (!row || keys.has(row.id)) throw Error('Unexpected Systems Manager listing.');
      keys.add(row.id);
      rows.push(row);
    }
    token = data.NextToken ?? '';
    if (typeof token !== 'string' || token.length > 2048 ||
        (token && (tokens.has(token) || items.length !== pageSize || rows.length === limit)))
      throw Error('Unexpected Systems Manager continuation.');
    tokens.add(token);
  } while (token);
  return rows;
}

export function listOwnedDocuments(request, region, signal) {
  return collect(request, region, signal, 'ListDocuments', {}, 'DocumentIdentifiers',
    item => typeof item?.Name === 'string' && localName.test(item.Name) &&
      item.Owner === '000000000000' && item.DocumentType === 'Command' &&
      item.DocumentFormat === 'JSON' ? {id: item.Name} : null);
}

export async function listDocumentVersions(request, region, signal, name) {
  if (typeof name !== 'string' || !localName.test(name)) throw Error('Invalid local document name.');
  const versions = await collect(request, region, signal, 'ListDocumentVersions', {Name: name},
    'DocumentVersions', (item, index) =>
      item?.Name === name && item.DocumentVersion === String(index + 1) &&
      item.DocumentFormat === 'JSON' && item.Status === 'Active' &&
      typeof item.CreatedDate === 'number' && Number.isFinite(item.CreatedDate) &&
      Number.isFinite(new Date(item.CreatedDate * 1000).getTime()) &&
      typeof item.IsDefaultVersion === 'boolean'
        ? {id: item.DocumentVersion, isDefault: item.IsDefaultVersion, created: item.CreatedDate}
        : null);
  if (versions.filter(item => item.isDefault).length !== 1)
    throw Error('Expected exactly one default document version.');
  return versions;
}

export async function getDocumentVersion(request, region, signal, name, version, created) {
  if (typeof name !== 'string' || !localName.test(name) || typeof version !== 'string' ||
      !versionNumber.test(version) || typeof created !== 'number' || !Number.isFinite(created))
    throw Error('Invalid local document version.');
  const output = await native(request, region, signal, 'GetDocument', {
    Name: name, DocumentVersion: version, DocumentFormat: 'JSON',
  });
  if (output?.Name !== name || typeof output.Content !== 'string' ||
      new TextEncoder().encode(output.Content).length > 65536 ||
      output.DocumentVersion !== version || output.CreatedDate !== created)
    throw Error('Content no longer matches the selected document version. Load versions again.');
  return {id: output.DocumentVersion, content: output.Content};
}
