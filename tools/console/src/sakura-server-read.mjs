const endpointPattern = /^\/_simulator\/providers\/sakura\/cloud\/zone\/([a-z0-9][a-z0-9-]{0,31})\/api\/cloud\/1\.1\/server$/;
const idPattern = /^[1-9][0-9]{0,18}$/;
const maxID = '9223372036854775807';
const basic = 'Basic ' + btoa('TCSIMSAKURA:dummy-local-only');
const operations = ['GET /server', 'GET /server/{id}'];
const maxRows = 100;
const timestampPattern = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/;

export function sakuraServerEndpoint(capability) {
  const {endpoint, zone, apiVersion, operations: enabled} = capability || {};
  if (typeof endpoint !== 'string' || typeof zone !== 'string' ||
      endpointPattern.exec(endpoint)?.[1] !== zone ||
      apiVersion !== '1.1' || !Array.isArray(enabled) || enabled.length !== operations.length ||
      operations.some(operation => !enabled.includes(operation))) return null;
  return endpoint;
}

function requireProjection(ok) {
  if (!ok) throw new Error('Invalid Sakura Server metadata');
}
const serverID = value => typeof value === 'string' && idPattern.test(value) &&
  (value.length < 19 || value <= maxID);
function record(value) {
  requireProjection(value && serverID(value.ID) &&
    typeof value.Name === 'string' && typeof value.Description === 'string' &&
    value.ServerPlan?.CPU === 2 && value.ServerPlan?.MemoryMB === 4096 &&
    [value.CreatedAt, value.ModifiedAt].every(date => typeof date === 'string' &&
      timestampPattern.test(date) && Number.isFinite(Date.parse(date))) &&
    !['Availability', 'Instance', 'Disks', 'Interfaces'].some(key => Object.hasOwn(value, key)));
  const {ID, Name, Description, CreatedAt, ModifiedAt} = value;
  return {ID, Name, Description, CreatedAt, ModifiedAt, CPU: value.ServerPlan.CPU, MemoryMB: value.ServerPlan.MemoryMB};
}

export async function readSakuraServers(request, capability, resource, signal) {
  const endpoint = sakuraServerEndpoint(capability);
  if (!endpoint || (resource !== '' && !serverID(resource))) throw new Error('Invalid Sakura Server target');
  const path = resource ? endpoint + '/' + resource :
    endpoint + '?' + encodeURIComponent(JSON.stringify({From: 0, Count: maxRows}));
  const response = await request(path, {method: 'GET', redirect: 'error', headers: {Authorization: basic}, signal});
  if (!response?.ok) throw new Error('Sakura Server read failed');
  const data = await response.json();
  requireProjection(data?.is_ok === true);
  if (resource) {
    const detail = record(data.Server);
    requireProjection(detail.ID === resource);
    return detail;
  }
  requireProjection(Array.isArray(data.Servers) && data.From === 0 &&
    data.Count === data.Servers.length && data.Total === data.Count && data.Count <= maxRows);
  const rows = data.Servers.map(record);
  requireProjection(new Set(rows.map(row => row.ID)).size === rows.length);
  return rows;
}
