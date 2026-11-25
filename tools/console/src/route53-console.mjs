// This UI speaks the existing native Route 53 REST/XML API. It does not create
// authority, change IAM, or contact a DNS server.
const namespace = 'https://route53.amazonaws.com/doc/2013-04-01/';
const base = '/2013-04-01/hostedzone';
const pageSize = 25;
const maxResponse = 4 * 1024 * 1024;
const supportedTypes = new Set(['A', 'AAAA', 'CNAME', 'TXT', 'MX', 'PTR', 'NS', 'SOA']);
const operations = ['ListHostedZones', 'GetHostedZone', 'ListResourceRecordSets', 'ChangeResourceRecordSets'];
const bad = () => new Error('Invalid local Route 53 response.');

export const initialPTRDraft = Object.freeze({
  fields: Object.freeze({name: '', target: '', ttl: '300'}),
  mustRefresh: false,
});

// Held by one mounted (identity, zone) view, not by the record page/form.
export function updatePTRDraft(previous, event) {
  switch (event.type) {
    case 'edit': return {...previous, fields: {...previous.fields, [event.field]:event.value}};
    case 'unconfirmed': return {...previous, mustRefresh:true};
    case 'fresh': return {...previous, mustRefresh:false};
    case 'verified': return initialPTRDraft;
    default: return previous;
  }
}

export function focusPTRResult(origin, focus) {
  // The action controls stay focusable while they are busy/locked. A body
  // focus or an inactive tab is not evidence this action still owns focus.
  if (origin?.isConnected && origin.ownerDocument.hasFocus()
      && origin.ownerDocument.activeElement === origin) focus();
}

export function route53ConsoleAdvertised(capabilities) {
  const scope = capabilities?.services?.route53;
  return !capabilities?.consoleScope && scope?.console === true && scope.publicZonesOnly === true && scope.protocol === 'REST/XML 2013-04-01'
    && Array.isArray(scope.operations) && operations.every(op => scope.operations.includes(op));
}

export function route53ConsoleAvailable(capabilities, authenticationRequired) {
  return authenticationRequired === true && route53ConsoleAdvertised(capabilities);
}

function dnsName(value) {
  if (typeof value !== 'string' || !/^[A-Za-z0-9.-]{1,254}$/.test(value)) return '';
  const name = value.endsWith('.') ? value.slice(0, -1) : value;
  if (name.length > 253 || !name.split('.').every(label =>
    /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(label))) return '';
  return name.toLowerCase() + '.';
}
function number(value, max) {
  return typeof value === 'string' && /^(?:0|[1-9][0-9]{0,9})$/.test(value) && Number(value) <= max
    ? Number(value) : null;
}
function zoneID(id) {
  if (typeof id !== 'string' || !/^Z[A-Z0-9]{1,31}$/.test(id)) throw Error('Invalid local Route 53 hosted zone ID.');
  return id;
}
export function ptrRecord(zone, {name, target, ttl}) {
  const apex = dnsName(zone), recordName = dnsName(name), value = dnsName(target), seconds = number(ttl, 2147483647);
  if (!apex || !recordName || !(recordName === apex || recordName.endsWith('.' + apex))
      || !value || seconds === null) return null;
  return {name: recordName, type: 'PTR', ttl: seconds, values: [value]};
}

function elements(parent, name) {
  return [...parent.children].filter(n => n.namespaceURI === namespace && n.localName === name);
}
function element(parent, name, optional = false) {
  const matches = elements(parent, name);
  if (matches.length === 1) return matches[0];
  if (optional && matches.length === 0) return null;
  throw bad();
}
function value(parent, name, optional = false) {
  const field = element(parent, name, optional);
  if (!field) return '';
  if (field.children.length) throw bad();
  return field.textContent;
}
async function responseText(response) {
  if (!/^text\/xml(?:;|$)|^application\/xml(?:;|$)/i.test(response.headers.get('Content-Type') || '')) throw bad();
  const reader = response.body?.getReader();
  if (!reader) throw bad();
  const decoder = new TextDecoder('utf-8', {fatal: true});
  let length = 0, text = '';
  try {
    for (;;) {
      const {done, value: chunk} = await reader.read();
      if (done) return text + decoder.decode();
      length += chunk.byteLength;
      if (length > maxResponse) throw Error('The local Route 53 response exceeds the console read limit.');
      text += decoder.decode(chunk, {stream: true});
    }
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}
function readZone(node) {
  const id = /^\/hostedzone\/(Z[A-Z0-9]{1,31})$/.exec(value(node, 'Id'))?.[1];
  const name = value(node, 'Name');
  const count = number(value(node, 'ResourceRecordSetCount'), 1000);
  if (!id || dnsName(name) !== name || count === null || value(element(node, 'Config'), 'PrivateZone') !== 'false') throw bad();
  return {id, name, count};
}
function readRecord(node) {
  const name = value(node, 'Name'), type = value(node, 'Type'), ttl = number(value(node, 'TTL'), 2147483647);
  const values = elements(element(node, 'ResourceRecords'), 'ResourceRecord').map(record => value(record, 'Value'));
  if (dnsName(name) !== name || !supportedTypes.has(type) || ttl === null
      || values.length < 1 || values.length > 100 || values.some(s => !s || s.length > 1024)) throw bad();
  return {name, type, ttl, values};
}
function bool(root, field) {
  const v = value(root, field);
  if (v !== 'true' && v !== 'false') throw bad();
  return v === 'true';
}
function startKey(name, type) {
  if (typeof name !== 'string' || dnsName(name) !== name || !supportedTypes.has(type)) throw bad();
  return {name, type};
}
function changeXML(record) {
  // Name/target are canonical ASCII DNS names, TTL is a bounded integer, and
  // the UI cannot set an arbitrary XML field, Action, resource type or route.
  return '<ChangeResourceRecordSetsRequest xmlns="' + namespace + '"><ChangeBatch><Changes><Change>' +
    '<Action>CREATE</Action><ResourceRecordSet><Name>' + record.name + '</Name><Type>PTR</Type><TTL>' +
    record.ttl + '</TTL><ResourceRecords><ResourceRecord><Value>' + record.values[0] +
    '</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>';
}

export function route53Client(request, parse = text => new DOMParser().parseFromString(text, 'application/xml')) {
  async function call(responseName, path, signal, body) {
    signal.throwIfAborted();
    const headers = {'X-Simulator-Service': 'route53', 'X-Simulator-Region': 'us-east-1'};
    if (body !== undefined) headers['Content-Type'] = 'application/xml';
    const response = await request(path, {method: body === undefined ? 'GET' : 'POST', body, signal,
      credentials: 'same-origin', redirect: 'error', headers});
    if (!response.ok) throw Error('Local Route 53 request failed (HTTP ' + response.status + ').');
    const xml = parse(await responseText(response)), root = xml?.documentElement;
    signal.throwIfAborted();
    if (xml?.doctype || root?.namespaceURI !== namespace || root.localName !== responseName) throw bad();
    return root;
  }

  function zonePath(id) {
    return base + '/' + zoneID(id);
  }
  async function records(id, signal, start = null) {
    const query = new URLSearchParams({maxitems: String(pageSize)});
    if (start) {
      const cursor = startKey(start.name, start.type);
      query.set('name', cursor.name);
      query.set('type', cursor.type);
    }
    const root = await call('ListResourceRecordSetsResponse', zonePath(id) + '/rrset?' + query, signal);
    const rows = elements(element(root, 'ResourceRecordSets'), 'ResourceRecordSet').map(readRecord);
    const truncated = bool(root, 'IsTruncated');
    const name = value(root, 'NextRecordName', true), type = value(root, 'NextRecordType', true);
    const next = truncated ? startKey(name, type) : null;
    if (rows.length > pageSize || (next && (rows.length !== pageSize || (next.name === start?.name && next.type === start?.type)))
        || (!next && (name || type)) || new Set(rows.map(row => row.name + '|' + row.type)).size !== rows.length) throw bad();
    return {rows, next, start};
  }

  return {
    async zones(signal) {
      // The simulator caps the database at 100 hosted zones.
      const root = await call('ListHostedZonesResponse', base + '?maxitems=100', signal);
      const rows = elements(element(root, 'HostedZones'), 'HostedZone').map(readZone);
      if (bool(root, 'IsTruncated') || rows.length > 100 || new Set(rows.map(zone => zone.id)).size !== rows.length) throw bad();
      return rows;
    },
    async zone(id, signal) {
      const root = await call('GetHostedZoneResponse', zonePath(id), signal);
      const zone = readZone(element(root, 'HostedZone'));
      if (zone.id !== id) throw bad();
      return zone;
    },
    records,
    async createPTR(zone, fields, signal) {
      const expected = ptrRecord(zone.name, fields);
      if (!expected) throw Error('Enter a valid record name in the selected zone, TTL and PTR target.');
      const root = await call('ChangeResourceRecordSetsResponse', zonePath(zone.id) + '/rrset', signal, changeXML(expected));
      const change = element(root, 'ChangeInfo');
      const changeID = value(change, 'Id');
      if (!/^\/change\/C[A-Z0-9]{1,31}$/.test(changeID) || value(change, 'Status') !== 'INSYNC'
          || !Number.isFinite(Date.parse(value(change, 'SubmittedAt')))) throw bad();
      const page = await records(zone.id, signal, {name: expected.name, type: expected.type});
      const row = page.rows[0];
      if (!row || row.name !== expected.name || row.type !== expected.type || row.ttl !== expected.ttl
          || row.values.length !== 1 || row.values[0] !== expected.values[0]) {
        throw Error('The saved PTR record could not be confirmed by readback.');
      }
      return {changeID, page};
    },
  };
}
