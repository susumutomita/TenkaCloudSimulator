const base = '/_simulator/providers/gcp/dns/v1/projects/';
const resourceName = /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const resourceID = /^[1-9][0-9]{0,18}$/;

export function zoneBody({name, dnsName, description}) {
  const suffix = dnsName.trim();
  const absolute = suffix.endsWith('.') ? suffix : suffix + '.';
  if (!resourceName.test(name)) throw new Error('name');
  if (absolute.length > 253 || !absolute.slice(0, -1).split('.').every(label =>
    /^[a-z0-9_][a-z0-9_-]{0,62}$/.test(label) && !label.endsWith('-'))) throw new Error('dnsName');
  if (new TextEncoder().encode(description).length > 1024) throw new Error('description');
  return {name, dnsName: absolute, description, visibility: 'public'};
}

export function cloudDNS(request, project) {
  if (typeof project !== 'string' || !resourceName.test(project)) throw new Error('Invalid local Cloud DNS project.');
  const zones = base + project + '/managedZones';
  const zonePath = id => {
    // The ID is non-reusable across create/delete cycles. Never fetch a newer
    // resource with the same human-readable name from an older list selection.
    if (typeof id !== 'string' || !resourceID.test(id)) throw new Error('Invalid local Cloud DNS zone ID.');
    return zones + '/' + id;
  };
  const call = async (path, options) => (await request(path, {redirect: 'error', ...options})).json();
  const all = async (path, key, maxPages, signal) => {
    const entries = [], seen = new Set();
    let token = '';
    for (let n = 0; n < maxPages; n++) {
      signal?.throwIfAborted();
      const query = new URLSearchParams({maxResults: '100'});
      if (token) query.set('pageToken', token);
      const result = await call(path + '?' + query, {method: 'GET', signal});
      if (!Array.isArray(result?.[key])) throw new Error('Invalid local Cloud DNS list response.');
      entries.push(...result[key]);
      if (result.nextPageToken === undefined) return entries;
      token = result.nextPageToken;
      if (typeof token !== 'string' || !token || token.length > 4096 || seen.has(token)) break;
      seen.add(token);
    }
    throw new Error('Invalid or over-limit local Cloud DNS pagination.');
  };
  return {
    list: signal => all(zones, 'managedZones', 1, signal),
    create: (form, signal) => call(zones, {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(zoneBody(form)), signal,
    }),
    async details(id, signal) {
      const path = zonePath(id);
      const [zone, records] = await Promise.all([
        call(path, {method: 'GET', signal}), all(path + '/rrsets', 'rrsets', 3, signal),
      ]);
      if (zone?.id !== id) throw new Error('Unexpected local Cloud DNS zone ID.');
      return {zone, records};
    },
  };
}
