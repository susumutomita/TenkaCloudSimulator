import {networkACLInventoryOperations} from './ec2-network-acls.mjs';
import {routeTableConsoleAvailable} from './ec2-route-table-console.mjs';
import {securityGroupInventoryAvailable} from './ec2-security-group-inventory.mjs';
import {internetGatewayPageOperations} from './ec2-internet-gateway-console.mjs';
import {vpcNativeSubnetsAvailable} from './ec2-native-subnets.mjs';

const sections = Object.freeze({
  vpcs: ['VPCs', 'VPC 一覧', 'vpc'],
  subnets: ['Subnets', 'サブネット', 'subnet'],
  'route-tables': ['Route tables', 'ルートテーブル', 'rtb'],
  'internet-gateways': ['Internet gateways', 'インターネットゲートウェイ', 'igw'],
  'network-acls': ['Network ACLs', 'ネットワーク ACL', 'acl'],
  'security-groups': ['Security groups', 'セキュリティグループ', 'sg'],
});
export const vpcConsoleSections = Object.freeze(Object.keys(sections));
const needsVpc = new Set(['subnets', 'route-tables', 'network-acls', 'security-groups']);
const match = (value, pattern) => typeof value === 'string' && pattern.exec(value)?.[0] === value;
const idTail = /^(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const validId = (id, prefix) => typeof id === 'string' && id.startsWith(prefix + '-') &&
  match(id.slice(prefix.length + 1), idTail);
const validRegion = value => typeof value === 'string' && value.length <= 32 &&
  match(value, /^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/);
function targetAllowed(section, target, vpc) {
  return !target || target === 'create' && ['vpcs', 'subnets'].includes(section) ||
    validId(target, sections[section][2]) && (section !== 'security-groups' || !!vpc);
}

export function vpcConsoleSectionName(section, t) { return t(...sections[section].slice(0, 2)); }

export function vpcConsoleRoute(hash) {
  if (typeof hash !== 'string' || hash.length > 256) return null;
  const entry = /^#\/vpc(?:\/([^/?#]+))?(?:\/([^/?#]+))?(?:\?([^#]*))?$/.exec(hash);
  if (!entry || entry[0] !== hash) return null;
  const [, first = '', second = '', query = ''] = entry;
  const params = new URLSearchParams(query);
  for (const key of ['region', 'vpc', 'view']) if (params.getAll(key).length > 1) return null;
  const region = params.get('region') || '';
  const vpc = params.get('vpc') || '';
  const view = params.get('view') || '';
  if (region && !validRegion(region) || vpc && !validId(vpc, 'vpc')) return null;
  const section = Object.hasOwn(sections, first) && first !== 'vpcs' ? first : 'vpcs';
  if (section === 'vpcs' && second || view && (section !== 'vpcs' || first || !['create','list'].includes(view)) ||
      vpc && !needsVpc.has(section)) return null;
  const target = section === 'vpcs' ? view === 'create' ? 'create' : first : second;
  // 'create' with a VPC view query is the sole historical VPC creation route.
  if (section === 'vpcs' && first === 'create' || !targetAllowed(section, target, vpc)) return null;
  return {section, target, vpc, region};
}

export function vpcConsoleHref(region, section = 'vpcs', target = '', vpc = '') {
  if (!Object.hasOwn(sections, section) || region && !validRegion(region) ||
      vpc && (!needsVpc.has(section) || !validId(vpc, 'vpc')) || !targetAllowed(section, target, vpc)) return '';
  const query = new URLSearchParams();
  if (region) query.set('region', region);
  if (section === 'vpcs' && target === 'create') query.set('view', 'create');
  if (vpc) query.set('vpc', vpc);
  const path = section === 'vpcs' ? '/vpc' + (target && target !== 'create' ? '/' + target : '')
    : '/vpc/' + section + (target ? '/' + target : '');
  return '#' + path + (query.size ? '?' + query : '');
}

export function vpcConsoleAvailability(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2, ops = ec2?.operations;
  const supports = (...actions) => Array.isArray(ops) && actions.every(action => ops.includes(action));
  if (ec2?.console!==true || !supports('DescribeVpcs', 'DescribeInstances') || capabilities.consoleScope) return {};
  return {
    vpcs: true,
    subnets: vpcNativeSubnetsAvailable(capabilities, authenticationRequired) && supports('DescribeSubnets'),
    'route-tables': routeTableConsoleAvailable(capabilities, authenticationRequired) && supports('DescribeRouteTables'),
    'internet-gateways': internetGatewayPageOperations(capabilities, authenticationRequired).read,
    'network-acls': networkACLInventoryOperations(capabilities, authenticationRequired).read,
    'security-groups': securityGroupInventoryAvailable(capabilities, authenticationRequired),
  };
}

const resourceSections = Object.freeze({
  vpc: 'vpcs', subnet: 'subnets', 'route-table': 'route-tables',
  'internet-gateway': 'internet-gateways', 'network-acl': 'network-acls', 'security-group': 'security-groups',
});
// A caller must supply an ID confirmed by its own readback and a destination
// that is advertised in the current session. A link never grants authority.
export function vpcResourceHref(availability, {kind, id, region, vpcId}) {
  if (!Object.hasOwn(resourceSections, kind)) return '';
  const section = resourceSections[kind];
  if (!availability[section] || !validId(id, sections[section][2])) return '';
  return vpcConsoleHref(region, section, id, needsVpc.has(section) ? vpcId : '');
}
