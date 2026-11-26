// Use the existing local session/identity proxy. AWS signing and endpoints
// are deliberately absent: a domain name is data, not a request destination.
const namespace = 'http://sdb.amazonaws.com/doc/2009-04-15/';
const pageSize = 25;
const maxResponse = 4 * 1024 * 1024;
const invalid = () => new Error('Invalid local SimpleDB response.');
const encoder = new TextEncoder();
export const simpleDBRegion = 'us-east-1';
export const validDomain = name => typeof name === 'string' && name.length >= 3 && name.length <= 255 && !/[^a-zA-Z0-9_.-]/.test(name);
export const validText = (text, empty = false) => typeof text === 'string' && (empty || text.length > 0) && encoder.encode(text).length <= 1024;

function entries(node, name) {
  return [...node.children].filter(child => child.namespaceURI === namespace && child.localName === name);
}
function element(node, name, optional = false) {
  const found = entries(node, name);
  if (found.length === 1) return found[0];
  if (optional && !found.length) return null;
  throw invalid();
}
function string(node, name, {optional = false, encoded = false} = {}) {
  const field = element(node, name, optional);
  if (!field) return '';
  if (field.children.length) throw invalid();
  const encoding = field.getAttribute('encoding');
  if (!encoding) return field.textContent;
  if (!encoded || encoding !== 'base64' || field.textContent.length > 1368) throw invalid();
  try {
    return new TextDecoder('utf-8', {fatal: true, ignoreBOM: true}).decode(Uint8Array.from(atob(field.textContent), c => c.charCodeAt(0)));
  } catch {
    throw invalid();
  }
}

async function boundedText(response) {
  const reader = response.body?.getReader();
  if (!reader) throw invalid();
  const decoder = new TextDecoder('utf-8', {fatal: true, ignoreBOM: true});
  let length = 0, xml = '';
  try {
    for (;;) {
      const {done, value} = await reader.read();
      if (done) return xml + decoder.decode();
      length += value.byteLength;
      if (length > maxResponse) throw new Error('The item exceeds the SimpleDB console read limit.');
      xml += decoder.decode(value, {stream: true});
    }
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}

export function simpleDBClient(request, parse = text => new DOMParser().parseFromString(text, 'application/xml')) {
  async function query(action, fields, signal) {
    signal.throwIfAborted();
    const response = await request('/', {
      method: 'POST', redirect: 'error', credentials: 'same-origin', signal,
      headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'sdb', 'X-Simulator-Region': simpleDBRegion},
      body: new URLSearchParams({Action: action, Version: '2009-04-15', ...fields}),
    });
    if (!response.ok) throw new Error('SimpleDB request failed (HTTP ' + response.status + ').');
    const mediaType = response.headers.get('Content-Type')?.split(';', 1)[0];
    if (mediaType !== 'text/xml' && mediaType !== 'application/xml') throw invalid();
    const root = parse(await boundedText(response))?.documentElement;
    signal.throwIfAborted();
    if (root?.namespaceURI !== namespace || root.localName !== action + 'Response'
        || entries(root, 'Errors').length || !/^[a-f0-9]{32}$/.test(string(element(root, 'ResponseMetadata'), 'RequestId'))) throw invalid();
    return root;
  }

  function itemFields(domain, item) {
    if (!validDomain(domain) || !validText(item)) throw new Error('Enter a valid local domain and item name.');
    return {DomainName: domain, ItemName: item};
  }

  return {
    async listDomains(token, signal) {
      if (typeof token !== 'string' || token.length > 8192) throw invalid();
      const root = await query('ListDomains', {MaxNumberOfDomains: pageSize, ...(token ? {NextToken: token} : {})}, signal);
      const result = element(root, 'ListDomainsResult');
      const domains = entries(result, 'DomainName').map(node => node.textContent);
      const nextToken = string(result, 'NextToken', {optional: true});
      if (domains.length > pageSize || new Set(domains).size !== domains.length || domains.some(name => !validDomain(name))
          || (nextToken && (nextToken.length > 8192 || nextToken === token || domains.length !== pageSize))) throw invalid();
      return {domains, nextToken};
    },
    async selectNames(domain, attribute, value, signal) {
      if (!validDomain(domain) || !validText(attribute, true) || !validText(value, true) || (!attribute && value))
        throw new Error('Enter a valid local domain and attribute filter.');
      // Only a fixed projection and a single optional equality, not free-form SQL
      // or a second DomainName parameter. Double the SimpleDB delimiters.
      const ident = v => '`' + v.replaceAll('`', '``') + '`';
      const filter = attribute ? ' where ' + ident(attribute) + " = '" + value.replaceAll("'", "''") + "'" : '';
      const expression = 'select itemName() from ' + ident(domain) + filter + ' limit ' + pageSize;
      async function page(token, signal) {
        const root = await query('Select', {SelectExpression: expression, ConsistentRead: 'true',
          ...(token ? {NextToken: token} : {})}, signal);
        const result = element(root, 'SelectResult');
        const items = entries(result, 'Item');
        if (items.length > pageSize) throw invalid();
        const names = items.map(item => {
          const name = string(item, 'Name', {encoded: true});
          if (!validText(name) || entries(item, 'Attribute').length) throw invalid();
          return name;
        });
        const next = string(result, 'NextToken', {optional: true});
        if (new Set(names).size !== names.length ||
            (next && (next.length > 8192 || next === token))) throw invalid();
        // A scan can exhaust its budget with 0 matches. The continuation belongs
        // to this request, not to mutable UI state; do not expose the raw cursor.
        return {names, next: next ? nextSignal => page(next, nextSignal) : null};
      }
      return page('', signal);
    },
    async createDomain(name, signal) {
      if (!validDomain(name)) throw new Error('Enter a valid local domain name.');
      await query('CreateDomain', {DomainName: name}, signal);
    },
    async getAttributes(domain, item, signal) {
      const root = await query('GetAttributes', {...itemFields(domain, item), ConsistentRead: 'true'}, signal);
      const attrs = entries(element(root, 'GetAttributesResult'), 'Attribute');
      if (attrs.length > 256) throw new Error('The item exceeds the SimpleDB console read limit.');
      return attrs.map((attr, index) => {
        const name = string(attr, 'Name', {encoded: true});
        const value = string(attr, 'Value', {encoded: true});
        if (!validText(name) || !validText(value, true)) throw invalid();
        return {id: index, name, value};
      });
    },
    async deleteAttribute(domain, item, {name, value}, signal) {
      if (!validText(name) || !validText(value, true)) throw new Error('Enter a valid attribute to delete.');
      // Never issue a whole-item/unconditional delete from a table row, even
      // for an empty value. Require the single value that the user actually saw.
      await query('DeleteAttributes', {
        ...itemFields(domain, item), 'Attribute.1.Name': name, 'Attribute.1.Value': value,
        'Expected.Name': name, 'Expected.Value': value,
      }, signal);
    },
    async putAttribute(domain, item, {name, value, mode, expected}, signal) {
      if (!validText(name) || !validText(value, true) || !['absent', 'equal'].includes(mode)
          || (mode === 'equal' && !validText(expected, true))) throw new Error('Enter a valid conditional attribute update.');
      await query('PutAttributes', {
        ...itemFields(domain, item), 'Attribute.1.Name': name, 'Attribute.1.Value': value, 'Attribute.1.Replace': 'true',
        'Expected.Name': name, 'Expected.Exists': mode === 'equal' ? 'true' : 'false',
        ...(mode === 'equal' ? {'Expected.Value': expected} : {}),
      }, signal);
    },
  };
}
