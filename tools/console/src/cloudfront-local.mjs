// Native CloudFront management and the explicitly simulator-only content route.
const ns = 'http://cloudfront.amazonaws.com/doc/2020-05-31/';
const base = '/2020-05-31/distribution';
const delivery = '/_simulator/cloudfront/distributions/';
const operations = ['ListDistributions', 'GetDistribution', 'CreateInvalidation', 'GetInvalidation'];
const invalid = () => Error('Invalid local CloudFront response.');
const idPattern = /^D[a-f0-9]{24}$/;
const receiptPattern = /^I[a-f0-9]{24}$/;

export function cloudFrontTasks() {
  let active = null;
  return {
    start(kind) {
      if (active?.kind === 'write') return null;
      active?.abort();
      active = new AbortController(); active.kind = kind;
      return active;
    },
    current(task) { return active === task && !task.signal.aborted; },
    writing() { return active?.kind === 'write'; },
    finish(task) { if (active !== task) return false; active = null; return true; },
    cancelRead() {
      if (active?.kind !== 'read') return false;
      active.abort(); active = null; return true;
    },
    abort() { active?.abort(); active = null; },
  };
}
export function cloudFrontAvailable(capabilities) {
  const scope = capabilities?.services?.cloudfront;
  return !capabilities?.consoleScope && scope?.distributionConsole === true
    && scope.protocol === 'REST/XML 2020-05-31' && scope.delivery === delivery
    && scope.distributionOrigin === 'one existing local S3 bucket'
    && scope.externalOriginFetch === false && scope.dataPlane === true
    && Array.isArray(scope.operations) && operations.every(op => scope.operations.includes(op));
}
export function localObjectKey(key) {
  return typeof key === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,1023}$/.test(key)
    && key.split('/').every(part => part && part !== '.' && part !== '..');
}
export function confirmedRefusal(error) {
  return /^(AccessDenied|InvalidArgument|NoSuchDistribution|NoSuchInvalidation|InvalidationBatchAlreadyExists|TooManyInvalidationsInProgress):/.test(error.message);
}
function children(node, name) { return [...node.children].filter(n => n.namespaceURI === ns && n.localName === name); }
function child(node, name, optional = false) {
  const found = children(node, name);
  if (found.length === 1) return found[0];
  if (!found.length && optional) return null;
  throw invalid();
}
function text(node, name, optional = false) {
  const n = child(node, name, optional);
  if (!n) return '';
  if (n.children.length) throw invalid();
  return n.textContent;
}
function flag(node, name) {
  const result = text(node, name);
  if (result !== 'true' && result !== 'false') throw invalid();
  return result === 'true';
}
function integer(node, name, max) {
  const result = text(node, name);
  if (!/^(0|[1-9][0-9]*)$/.test(result) || Number(result) > max) throw invalid();
  return Number(result);
}
function identifier(id) { if (!idPattern.test(id)) throw invalid(); return id; }
function distribution(node, config = node) {
  const id = identifier(text(node, 'Id'));
  const arn = text(node, 'ARN'), domain = text(node, 'DomainName');
  if (arn !== 'arn:aws:cloudfront::000000000000:distribution/' + id || domain !== id + '.cloudfront.invalid') throw invalid();
  const origins = child(config, 'Origins');
  const items = child(origins, 'Items');
  if (integer(origins, 'Quantity', 1) !== 1 || children(items, 'Origin').length !== 1) throw invalid();
  const origin = child(items, 'Origin'), behavior = child(config, 'DefaultCacheBehavior');
  const originId = text(origin, 'Id'), target = text(behavior, 'TargetOriginId');
  const minimum = integer(behavior, 'MinTTL', 86400), ttl = integer(behavior, 'DefaultTTL', 86400), maximum = integer(behavior, 'MaxTTL', 86400);
  if (!originId || originId !== target || minimum > ttl || ttl > maximum) throw invalid();
  return {id, arn, domain, status: text(node, 'Status'), modified: text(node, 'LastModifiedTime'),
    enabled: flag(config, 'Enabled'), comment: text(config, 'Comment'),
    originId, origin: text(origin, 'DomainName'), minimum, ttl, maximum};
}
async function bytes(response, limit) {
  const reader = response.body?.getReader();
  if (!reader) throw invalid();
  const chunks = []; let length = 0;
  try {
    for (;;) {
      const {done, value} = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > limit) throw Error('The local response exceeds the console read limit.');
      chunks.push(value);
    }
  } catch (error) { await reader.cancel().catch(() => {}); throw error; }
  const result = new Uint8Array(length); let offset = 0;
  for (const part of chunks) { result.set(part, offset); offset += part.byteLength; }
  return result;
}
function escapeXML(value) { return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&apos;'); }
export function invalidationXML(caller) {
  if (typeof caller !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(caller)) throw Error('Invalid invalidation caller reference.');
  return `<InvalidationBatch xmlns="${ns}"><Paths><Quantity>1</Quantity><Items><Path>/*</Path></Items></Paths><CallerReference>${escapeXML(caller)}</CallerReference></InvalidationBatch>`;
}

export function cloudFrontClient(request, parse = value => new DOMParser().parseFromString(value, 'application/xml')) {
  async function xml(path, signal, options = {}) {
    const response = await request(path, {method: 'GET', redirect: 'error', credentials: 'same-origin', signal,
      ...options, headers: {'X-Simulator-Service': 'cloudfront', 'X-Simulator-Region': 'us-east-1', ...(options.headers || {})}});
    if (response.status !== (options.method === 'POST' ? 201 : 200)
        || !/^(application|text)\/xml(?:;|$)/i.test(response.headers.get('content-type') || '')) throw invalid();
    const document = parse(new TextDecoder('utf-8', {fatal: true}).decode(await bytes(response, 256 * 1024)));
    if (document.getElementsByTagName('parsererror').length || document.documentElement?.namespaceURI !== ns) throw invalid();
    return {root: document.documentElement, etag: response.headers.get('etag')};
  }
  function receipt(root, id, caller) {
    if (root.localName !== 'Invalidation' || !receiptPattern.test(text(root, 'Id')) || text(root, 'Status') !== 'Completed') throw invalid();
    const batch = child(root, 'InvalidationBatch'), paths = child(batch, 'Paths');
    if (text(batch, 'CallerReference') !== caller || integer(paths, 'Quantity', 100) !== 1
        || children(child(paths, 'Items'), 'Path').length !== 1 || text(child(paths, 'Items'), 'Path') !== '/*') throw invalid();
    return {distribution: id, id: text(root, 'Id'), caller, status: text(root, 'Status'), created: text(root, 'CreateTime')};
  }
  return {
    async list(marker, signal) {
      if (marker) identifier(marker);
      const {root} = await xml(base + '?MaxItems=25' + (marker ? '&Marker=' + marker : ''), signal);
      if (root.localName !== 'DistributionList' || text(root, 'Marker') !== marker || integer(root, 'MaxItems', 50) !== 25) throw invalid();
      const rows = children(child(root, 'Items'), 'DistributionSummary').map(row => distribution(row));
      const next = text(root, 'NextMarker', true), truncated = flag(root, 'IsTruncated');
      if (integer(root, 'Quantity', 25) !== rows.length || new Set(rows.map(row => row.id)).size !== rows.length
          || rows.some((row, i) => (marker && row.id <= marker) || (i && rows[i - 1].id >= row.id)) || truncated !== !!next
          || (next && (identifier(next) !== rows.at(-1)?.id || next === marker))) throw invalid();
      return {rows, next};
    },
    async detail(id, signal) {
      const {root, etag} = await xml(base + '/' + identifier(id), signal);
      if (root.localName !== 'Distribution' || !/^[a-f0-9]{32}$/.test(etag || '')) throw invalid();
      const result = distribution(root, child(root, 'DistributionConfig'));
      if (result.id !== id) throw invalid();
      return {...result, etag};
    },
    async invalidate(id, caller, signal) {
      const {root} = await xml(base + '/' + identifier(id) + '/invalidation', signal,
        {method: 'POST', headers: {'Content-Type': 'application/xml'}, body: invalidationXML(caller)});
      return receipt(root, id, caller);
    },
    async invalidation(saved, signal) {
      if (!receiptPattern.test(saved.id)) throw invalid();
      const {root} = await xml(base + '/' + identifier(saved.distribution) + '/invalidation/' + saved.id, signal);
      const result = receipt(root, saved.distribution, saved.caller);
      if (result.id !== saved.id) throw invalid();
      return result;
    },
    async object(id, key, signal) {
      if (!localObjectKey(key)) throw Error('Enter a supported local object key.');
      const response = await request(delivery + identifier(id) + '/objects/' + key,
        {method: 'GET', redirect: 'error', credentials: 'same-origin', signal});
      if (response.status !== 200 || response.headers.get('content-disposition') !== 'attachment'
          || response.headers.get('x-content-type-options') !== 'nosniff'
          || response.headers.get('content-security-policy') !== "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
          || response.headers.get('cache-control') !== 'no-store') throw invalid();
      const body = await bytes(response, 1 << 20);
      return {body, type: response.headers.get('content-type') || 'application/octet-stream',
        etag: response.headers.get('etag') || '', cache: response.headers.get('x-cache') || '', age: response.headers.get('age') || '',
        preview: new TextDecoder().decode(body.slice(0, 65536)), truncated: body.length > 65536};
    },
  };
}
