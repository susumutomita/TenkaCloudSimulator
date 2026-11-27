import {ec2IPv4Prefix} from './ec2-ipv4-route-prefix.mjs';
import {loadVpcSubnets} from './ec2-native-subnets.mjs';
import {parseEC2Document} from './ec2-response.mjs';
import {loadEC2RouteTablePages, readEC2RouteTableReply} from './ec2-route-table-pages.mjs';
import {vpcRouteTablesAvailable} from './ec2-route-tables.mjs';

const vpcID = /^vpc-[0-9a-f]{17}$/;
const tableID = /^rtb-[0-9a-f]{17}$/;
const regionID = /^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const xml = text => new DOMParser().parseFromString(text, 'application/xml');
const exact = (value, pattern) => typeof value === 'string' && pattern.exec(value)?.[0] === value;
const invalid = () => {throw Error('Invalid or incomplete route table context.');};
// The fallback projection can use a complete regional response, never only
// the visible/filtered selection or an inventory produced under another actor.
const inventoryScope = new WeakMap();

export const routeTableConsoleAvailable = vpcRouteTablesAvailable;
export const validRouteTableConsoleID = id => exact(id, tableID);

export async function loadRouteTableConsoleInventory(request, region, {signal, parse = xml} = {}) {
  const {tables} = await loadEC2RouteTablePages(request, region, {signal, parse});
  const snapshot = Object.freeze(tables.map(row => Object.freeze({
    ...row,
    tags: Object.freeze(row.tags.map(Object.freeze)),
    routes: Object.freeze(row.routes.map(Object.freeze)),
    associations: Object.freeze(row.associations.map(Object.freeze)),
    ...(row.localRoute && {localRoute: Object.freeze(row.localRoute)}),
    ...(row.mainAssociation && {mainAssociation: Object.freeze(row.mainAssociation)}),
    name: row.tags.find(tag => tag.key === 'Name')?.value ?? '',
  })));
  inventoryScope.set(snapshot, {request, region});
  return snapshot;
}

export async function loadRouteTableVpcContext(request, region, {signal, parse = xml} = {}) {
  signal?.throwIfAborted();
  if (!exact(region, regionID) || region.length > 32) invalid();
  const response = await request('/', {
    method: 'POST', credentials: 'same-origin', redirect: 'error', signal,
    headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'ec2', 'X-Simulator-Region': region},
    body: new URLSearchParams({Action: 'DescribeVpcs', Version: '2016-11-15'}),
  });
  signal?.throwIfAborted();
  if (!response?.ok) throw Error((response?.status ?? 0) + ': VPC request failed.');
  const document = parse(await readEC2RouteTableReply(response, signal, 2 * 1024 * 1024));
  if (document?.doctype) invalid();
  const rows = parseEC2Document(document, 'vpc');
  const qualified = [];
  for (const candidate of rows) {
    if (!exact(candidate.id, vpcID)) continue;
    if (candidate.managedBy !== 'ec2' && candidate.managedBy !== 'cloudformation') continue;
    const prefix = ec2IPv4Prefix(candidate.cidr);
    if (!prefix || prefix.masked !== candidate.cidr || prefix.bits < 16 || prefix.bits > 28) invalid();
    qualified.push(Object.freeze(candidate));
  }
  return Object.freeze(qualified);
}

export function routeTableMainStatus(tables, table) {
  if (table.mainAssociation) return 'yes';
  return tables.some(row => row.vpc === table.vpc && row.mainAssociation) ? 'no' : 'unreported';
}

export async function loadRouteTableImplicitSubnets(request, region, vpc, tables, {signal, parse = xml} = {}) {
  const scope = inventoryScope.get(tables);
  if (!scope || scope.request !== request || scope.region !== region || vpc?.managedBy !== 'ec2' || !exact(vpc.id, vpcID)) invalid();
  const rows = tables.filter(row => row.vpc === vpc.id);
  const main = rows.find(row => row.mainAssociation);
  if (!main) return null;
  if (main.localRoute?.cidr !== vpc.cidr) invalid();
  const subnets = await loadVpcSubnets(request, region, vpc, {signal, parse});
  signal?.throwIfAborted();
  const explicit = new Set(rows.flatMap(row => row.associations.map(item => item.subnet)));
  const known = new Set(subnets.map(item => item.id));
  if ([...explicit].some(id => !known.has(id))) invalid();
  return {mainId: main.id, items: subnets.filter(item => !explicit.has(item.id))};
}

export function routeTableConsoleFilter(tables, query, selectedVpc) {
  const search = query.trim().toLocaleLowerCase();
  return tables.filter(row => (!selectedVpc || row.vpc === selectedVpc) && (!search ||
    [row.id, row.name, row.vpc, ...row.associations.map(association => association.subnet)]
      .some(value => value.toLocaleLowerCase().includes(search))));
}

export function routeTableConsoleHref(hrefForResource, target) {
  const href = hrefForResource?.(target);
  return typeof href === 'string' && href.length <= 2048 && /^#\/[^\s]*$/.exec(href)?.[0] === href ? href : '';
}
