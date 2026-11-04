/* First-party documentation aid. Native responses and Scalar's DOM stay unchanged. */
(() => {
  'use strict';
  const LIMIT = 65536, MAX_NODES = 2000, MAX_ROWS = 100;
  const trigger = document.getElementById('s3-result-trigger');
  const dialog = document.getElementById('s3-result-dialog');
  const content = document.getElementById('s3-result-content');
  const close = document.getElementById('s3-result-close');
  const announcement = document.getElementById('s3-result-announcement');
  let opener = null;
  let revision = 0, current = null, cancelInspection = null;
  const element = (tag, text, className) => {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = String(text).slice(0, 2048);
    if (className) node.className = className;
    return node;
  };
  const children = (node, name) => [...node.children].filter(child => child.localName === name);
  const child = (node, name) => children(node, name)[0];
  const value = (node, name) => child(node, name)?.textContent ?? '';
  const field = (name, meaning, text) => ({ name, meaning, value: String(text).slice(0, 512) });
  function operation(input, init, url) {
    const method = (init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const source = init?.headers || (input instanceof Request ? input.headers : undefined);
    // Read only routing metadata, never copy/iterate header values or access
    // authorization, cookies, tokens, request bodies or arbitrary headers.
    const routingHeader = name => {
      if (source instanceof Headers) return source.get(name);
      const key = name.toLowerCase();
      if (Array.isArray(source)) return source.find(entry => String(entry[0]).toLowerCase() === key)?.[1];
      if (source && typeof source === 'object') { const found = Object.keys(source).find(entry => entry.toLowerCase() === key); return found ? source[found] : undefined; }
      return undefined;
    };
    if (routingHeader('X-Amz-Target') || !['', 's3'].includes(routingHeader('X-Simulator-Service') || '')) return null;
    let path = url.pathname.replace(/^\/_simulator\/providers\/aws(?=\/)/, '');
    if (method !== 'GET') return null;
    const keys = [...url.searchParams.keys()];
    if (path === '/' && keys.every(key => key === 'x-id')) return { name: 'List buckets', path, root: 'ListAllMyBucketsResult' };
    if (!/^\/[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(path)) return null; // Never inspect an object GET body.
    const selectors = ['versions', 'location', 'versioning', 'publicAccessBlock', 'tagging'].filter(key => url.searchParams.has(key));
    if (selectors.length > 1 || selectors.some(key => url.searchParams.get(key) !== '')) return null;
    const modes = { versions: ['List retained versions', 'ListVersionsResult'], location: ['Read bucket region', 'LocationConstraint'], versioning: ['Read versioning setting', 'VersioningConfiguration'], publicAccessBlock: ['Read public-access settings', 'PublicAccessBlockConfiguration'], tagging: ['Read bucket tags', 'Tagging'] };
    const mode = selectors[0];
    const allowed = mode === 'versions' ? ['versions', 'prefix', 'delimiter', 'max-keys', 'encoding-type', 'key-marker', 'version-id-marker', 'x-id'] : mode ? [mode, 'x-id'] : ['list-type', 'prefix', 'delimiter', 'max-keys', 'continuation-token', 'start-after', 'encoding-type', 'x-id'];
    if (!keys.every(key => allowed.includes(key))) return null;
    if (mode) return { name: modes[mode][0], path, root: modes[mode][1] };
    return { name: 'List current objects', path, root: 'ListBucketResult' };
  }
  async function boundedText(response, id) {
    const reader = response.body?.getReader();
    if (!reader) return '';
    let bytes = 0; const chunks = []; let timedOut = false;
    const cancel = () => { void reader.cancel().catch(() => {}); };
    cancelInspection = cancel;
    const timeout = setTimeout(() => { timedOut = true; cancel(); }, 1500);
    try {
      for (;;) {
        if (id !== revision) throw Error('Superseded');
        const { value: chunk, done } = await reader.read();
        if (timedOut) throw Error("Explanation timed out; the native response is unchanged.");
        if (id !== revision) throw Error("Superseded");
        if (done) break;
        bytes += chunk.byteLength;
        if (bytes > LIMIT) throw Error('Response exceeds the 64 KiB explanation limit.');
        chunks.push(chunk);
      }
      const all = new Uint8Array(bytes); let offset = 0;
      for (const chunk of chunks) { all.set(chunk, offset); offset += chunk.byteLength; }
      return new TextDecoder('utf-8', { fatal: true }).decode(all);
    } finally {
      // Awaiting tee cancellation could wait for Scalar's original consumer.
      clearTimeout(timeout);
      if (cancelInspection === cancel) cancelInspection = null;
      cancel();
      reader.releaseLock();
    }
  }
  function parse(text, op) {
    if (/<!DOCTYPE|<!ENTITY/i.test(text)) throw Error('DTD/entity XML is not interpreted.');
    const doc = new DOMParser().parseFromString(text, 'application/xml');
    if (doc.getElementsByTagName('parsererror').length || !doc.documentElement) throw Error('Response is not well-formed XML.');
    let nodes = 0;
    const walk = (node, depth) => { if (++nodes > MAX_NODES || depth > 16) throw Error('XML exceeds the explanation complexity limit.'); for (const c of node.childNodes) walk(c, depth + 1); };
    walk(doc.documentElement, 0);
    const root = doc.documentElement;
    if (root.localName !== 'Error' && root.localName !== op.root) throw Error('This XML does not match the selected S3 metadata operation.');
    if (root.namespaceURI && root.namespaceURI !== 'http://s3.amazonaws.com/doc/2006-03-01/') throw Error('Unknown XML namespace.');
    const result = { fields: [], items: [] };
    const add = (name, meaning, text) => result.fields.push(field(name, meaning, text));
    const addItems = (rows, build) => { result.items = rows.slice(0, MAX_ROWS).map(build); if (rows.length > MAX_ROWS) result.note = `Showing ${MAX_ROWS} of ${rows.length} returned entries. The full native response remains in Scalar.`; };
    if (root.localName === 'Error') {
      if (!value(root, 'Code') || !value(root, 'Message')) throw Error('Unrecognized error shape.');
      if (!/^[A-Za-z][A-Za-z0-9]{0,127}$/.test(value(root, 'Code'))) throw Error('Unrecognized error code.');
      result.summary = 'S3 error: ' + value(root, 'Code');
      add('Code', 'Machine-readable reason; use this to distinguish a missing bucket, absent setting or denied permission.', value(root, 'Code'));
      add('Message', 'Explanation from this actual response.', value(root, 'Message'));
      return result;
    }
    switch (root.localName) {
      case 'ListAllMyBucketsResult': {
        if (!child(root, 'Buckets')) throw Error('Bucket list is missing its Buckets element.');
        const rows = children(child(root, 'Buckets'), 'Bucket');
        result.summary = `${rows.length} bucket${rows.length === 1 ? '' : 's'} in this response`;
        if (!rows.length) add('Buckets', 'The executed response contains no buckets. This is live state, not the static example.', 'Empty');
        addItems(rows, row => ({ title: value(row, 'Name'), fields: [field('Name', 'Use as {bucket} in the next request.', value(row, 'Name')), field('BucketRegion', 'Saved bucket region.', value(row, 'BucketRegion')), field('CreationDate', 'Creation time in UTC.', value(row, 'CreationDate'))] }));
        break;
      }
      case 'ListBucketResult': {
        if (!child(root, 'Name') || !child(root, 'KeyCount')) throw Error('Unrecognized object-list shape.');
        if (!/^\d{1,4}$/.test(value(root, 'KeyCount')) || !['true', 'false'].includes(value(root, 'IsTruncated'))) throw Error('Unrecognized object-list count or pagination flag.');
        result.summary = value(root, 'KeyCount') + ' current object/prefix entries on this page';
        add('Name', 'Bucket being listed, not an object key.', value(root, 'Name'));
        add('IsTruncated', 'true means another page exists. Use NextContinuationToken with the same filters.', value(root, 'IsTruncated'));
        if (value(root, 'NextContinuationToken')) add('Next page', 'An opaque cursor was returned. Copy it from the native response into continuation-token.', 'Available');
        addItems(children(root, 'Contents'), row => ({ title: value(row, 'Key'), fields: [field('Key', 'Object key; use with this bucket to GET its body.', value(row, 'Key')), field('Size', 'Object length in bytes.', value(row, 'Size')), field('LastModified', 'Last write time in UTC.', value(row, 'LastModified'))] }));
        const prefixes = children(root, 'CommonPrefixes'), prefixLimit = MAX_ROWS - result.items.length;
        if (prefixes.length > prefixLimit) result.note = `Showing at most ${MAX_ROWS} object/prefix entries; the full native response remains in Scalar.`;
        for (const prefix of prefixes.slice(0, prefixLimit)) add('Common prefix', 'A delimiter-based grouping, not a separately stored folder.', value(prefix, 'Prefix'));
        break;
      }
      case 'ListVersionsResult': {
        result.summary = `${children(root, 'Version').length} retained versions; ${children(root, 'DeleteMarker').length} delete markers on this page`;
        add('Name', 'Bucket whose retained history was listed.', value(root, 'Name'));
        add('IsTruncated', 'For the next page, use both NextKeyMarker and NextVersionIdMarker from the native response.', value(root, 'IsTruncated'));
        addItems([...root.children].filter(row => ['Version', 'DeleteMarker'].includes(row.localName)), row => ({ title: row.localName, fields: [field('Key', 'Object key.', value(row, 'Key')), field('VersionId', 'Opaque version/marker identifier.', value(row, 'VersionId')), field('IsLatest', 'Whether this entry is the newest for its key.', value(row, 'IsLatest'))] }));
        break;
      }
      case 'LocationConstraint': result.summary = 'Bucket region: ' + (root.textContent || 'us-east-1'); add('LocationConstraint', 'Empty XML text means us-east-1; it does not mean the bucket is absent.', root.textContent || 'us-east-1 (empty XML text)'); break;
      case 'VersioningConfiguration': result.summary = value(root, 'Status') || 'Versioning has never been enabled'; add('Status', 'Enabled retains versions. Suspended keeps older history. Missing Status means never enabled.', value(root, 'Status') || 'Not returned'); break;
      case 'PublicAccessBlockConfiguration':
        result.summary = 'Four saved public-access settings';
        for (const name of ['BlockPublicAcls', 'IgnorePublicAcls', 'BlockPublicPolicy', 'RestrictPublicBuckets']) { const text = value(root, name); if (!['true', 'false'].includes(text)) throw Error('Unrecognized public-access setting.'); add(name, 'Saved bucket-level boolean. ACL/policy APIs remain unsupported.', text); }
        break;
      case 'Tagging': {
        if (!child(root, 'TagSet')) throw Error('TagSet is missing.');
        const rows = children(child(root, 'TagSet'), 'Tag'); result.summary = `${rows.length} user-defined bucket tags`;
        addItems(rows, row => ({ title: value(row, 'Key'), fields: [field('Key', 'User-defined metadata key, not an IAM grant.', value(row, 'Key')), field('Value', 'Saved metadata value.', value(row, 'Value'))] })); break;
      }
    }
    return result;
  }
  function render(result) {
    content.replaceChildren();
    content.append(element('p', `Live executed response · ${result.operation} · HTTP ${result.status}`, 'reader-eyebrow'));
    content.append(element('h3', result.summary));
    content.append(element('p', result.path, 'reader-path'));
    if (result.fallback) content.append(element('p', result.fallback + ' Read the unchanged native response in the API client.', 'reader-fallback'));
    const fields = rows => { const list = element('dl'); for (const row of rows) { list.append(element('dt', row.name)); const entry = element('dd'); entry.append(element('strong', row.value || '(empty)'), element('small', row.meaning)); list.append(entry); } return list; };
    if (result.fields?.length) content.append(fields(result.fields));
    for (const item of result.items || []) { const section = element('section', undefined, 'reader-item'); section.append(element('h4', item.title || 'Returned entry'), fields(item.fields)); content.append(section); }
    if (result.note) content.append(element('p', result.note));
    content.append(element('p', 'This explanation is memory-only and belongs to the request shown above. It does not change the API response or saved state.', 'reader-note'));
  }
  function open() { if (!current || dialog.open) return; opener = document.activeElement; render(current); dialog.showModal(); close.focus(); }
  trigger.addEventListener('click', open);
  document.addEventListener('keydown', event => { if (current && event.altKey && event.shiftKey && !event.ctrlKey && !event.metaKey && event.code === 'KeyR') { event.preventDefault(); open(); } });
  close.addEventListener('click', () => dialog.close());
  // The embedded client also handles Escape. Close only our top native dialog
  // and prevent the underlying client from consuming or reusing this key.
  document.addEventListener('keydown', event => {
    if (dialog.open && event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); dialog.close(); }
  }, true);
  dialog.addEventListener('close', () => { if (opener?.isConnected && opener.getClientRects().length) opener.focus(); else if (!trigger.hidden) trigger.focus(); opener = null; });
  function clear() { cancelInspection?.(); cancelInspection = null; current = null; trigger.hidden = true; announcement.textContent = ''; if (dialog.open) dialog.close(); }
  async function inspect(id, response, op) {
    try {
      const text = await boundedText(response, id);
      if (id !== revision) return;
      const parsed = parse(text, op);
      current = { ...parsed, operation: op.name, path: 'GET ' + op.path, status: response.status };
    } catch (error) {
      if (id !== revision) return;
      current = { summary: 'Native S3 response — explanation unavailable', fallback: error.message, operation: op.name, path: 'GET ' + op.path, status: response.status };
    }
    if (id !== revision) return;
    trigger.textContent = 'Live S3 result · ' + current.operation + ' · ' + current.summary.slice(0, 160) + ' · Read fields';
    trigger.hidden = false;
    announcement.textContent = 'Live S3 result ready for ' + current.operation + '. ' + current.summary.slice(0, 160) + '. Read fields using the result button or Alt+Shift+R.';
  }
  globalThis.SimulatorS3ResponseGuide = {
    wrapFetch(nativeFetch) {
      return async (input, init) => {
        const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href);
        if (url.origin !== location.origin || url.username || url.password) { ++revision; clear(); throw Error('Only this simulator origin without URL credentials is allowed.'); }
        // Scalar also uses customFetch to load its OpenAPI file. That background
        // read must not supersede or clear the last actual API execution.
        if (url.pathname.startsWith('/_simulator/docs/')) return nativeFetch(input, init);
        const id = ++revision; clear();
        const op = operation(input, init, url);
        const response = await nativeFetch(input, init);
        if (op && id === revision) {
          try { void inspect(id, response.clone(), op); } catch { /* A display-only failure must never change the native fetch result. */ }
        }
        return response; // EXACT native Response identity; no body/header rewrite.
      };
    },
  };
})();
