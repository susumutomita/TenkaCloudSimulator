// The local Cloud DNS Changes API persists SRV metadata. It never resolves targets.
const absolute = value => typeof value === 'string' && value.length <= 253 && value.endsWith('.') &&
  value.slice(0, -1).split('.').every(label => /^[a-z0-9_][a-z0-9_-]{0,62}$/.test(label) && !label.endsWith('-'));
const decimal = (value, max) => typeof value === 'string' && /^(0|[1-9][0-9]*)$/.test(value) &&
  Number(value) <= max;

export function srvRecord(form, zone) {
  const {name, ttl, endpoints} = form;
  if (!absolute(zone) || !absolute(name) || (name !== zone && !name.endsWith('.' + zone))) throw Error('name');
  if (!decimal(ttl, 2147483647)) throw Error('ttl');
  if (!Array.isArray(endpoints) || endpoints.length < 1 || endpoints.length > 32) throw Error('endpoints');
  const rrdatas = endpoints.map(({priority, weight, port, target}) => {
    if (![priority, weight, port].every(value => decimal(value, 65535))) throw Error('numbers');
    if (target !== '.' && !absolute(target)) throw Error('target');
    return [priority, weight, port, target].join(' ');
  });
  if (new Set(rrdatas).size !== rrdatas.length) throw Error('duplicate');
  return {name, type: 'SRV', ttl: Number(ttl), rrdatas: rrdatas.sort()};
}

export const blankEndpoint = () => ({priority: '0', weight: '0', port: '', target: ''});

export function srvForm(record) {
  return record ? {name: record.name, ttl: String(record.ttl), endpoints: record.rrdatas.map(data => {
    const [priority, weight, port, target] = data.split(' ');
    return {priority, weight, port, target};
  })} : {name: '', ttl: '300', endpoints: [blankEndpoint()]};
}

export function sameSRV(left, right) {
  if (!left || !right || left.type !== 'SRV' || right.type !== 'SRV' || left.name !== right.name ||
      left.ttl !== right.ttl || !Array.isArray(left.rrdatas) || !Array.isArray(right.rrdatas) ||
      left.rrdatas.length !== right.rrdatas.length) return false;
  const a = [...left.rrdatas].sort(), b = [...right.rrdatas].sort();
  return a.every((item, i) => item === b[i]);
}

export function verifySRVReceipt(receipt, previous, next) {
  const matches = (rows, record) => Array.isArray(rows) && (record ? rows.length === 1 && sameSRV(rows[0], record) : !rows.length);
  return receipt?.kind === 'dns#change' && receipt.status === 'done' && receipt.isServing === false &&
    typeof receipt.id === 'string' && /^[1-9][0-9]{0,18}$/.test(receipt.id) &&
    matches(receipt.deletions, previous) && matches(receipt.additions, next);
}
